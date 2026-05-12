package slack

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeEmitter records every emit call. Concurrent-safe so the watch
// goroutine can append while the test goroutine reads.
type fakeEmitter struct {
	mu     sync.Mutex
	events []InboxEvent
}

func (e *fakeEmitter) Emit(ev InboxEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return nil
}

func (e *fakeEmitter) snapshot() []InboxEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]InboxEvent, len(e.events))
	copy(out, e.events)
	return out
}

// alwaysFailEmitter rejects every emit. Deterministic.
type alwaysFailEmitter struct {
	mu    sync.Mutex
	calls int
}

func (e *alwaysFailEmitter) Emit(InboxEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return errors.New("permanent emit failure")
}

func (e *alwaysFailEmitter) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// flakyOnceEmitter fails the first call, succeeds afterward.
type flakyOnceEmitter struct {
	mu     sync.Mutex
	called int
	events []InboxEvent
}

func (e *flakyOnceEmitter) Emit(ev InboxEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.called++
	if e.called == 1 {
		return errors.New("first call fails")
	}
	e.events = append(e.events, ev)
	return nil
}

func (e *flakyOnceEmitter) snapshot() (called int, events []InboxEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]InboxEvent, len(e.events))
	copy(out, e.events)
	return e.called, out
}

// panickyEmitter panics on every call. Tests panic recovery.
type panickyEmitter struct {
	mu    sync.Mutex
	calls int
}

func (e *panickyEmitter) Emit(InboxEvent) error {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	panic("boom")
}

// directWriteInboxFile bypasses tmp+rename — used for the simple replay
// test where ordering doesn't depend on watch behavior.
func directWriteInboxFile(t *testing.T, dir, name string, ev InboxEvent) {
	t.Helper()
	data, err := json.Marshal(&ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// atomicWriteInboxFile mirrors the daemon's writeInboxIfSubscribed
// pattern: write tmp, then rename to .json. fsnotify Create fires on the
// rename target, not on the tmp open. Used for live-watch tests so the
// test stays coupled to production write semantics.
func atomicWriteInboxFile(t *testing.T, dir, name string, ev InboxEvent) {
	t.Helper()
	data, err := json.Marshal(&ev)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, name+".tmp")
	final := filepath.Join(dir, name)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, final); err != nil {
		t.Fatal(err)
	}
}

// countJSONFiles returns the number of *.json files in dir, excluding
// .claimed.* (in-progress) files.
func countJSONFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) == ".json" {
			n++
		}
	}
	return n
}

func TestChannelServer_ReplayPreExistingFiles(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	dir := InboxDir(town, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Three events out of write order; lexicographic-by-name puts them
	// in the order ts=100, 200, 300.
	directWriteInboxFile(t, dir, "300-c.json", InboxEvent{ChatID: "D3", Text: "third"})
	directWriteInboxFile(t, dir, "100-a.json", InboxEvent{ChatID: "D1", Text: "first"})
	directWriteInboxFile(t, dir, "200-b.json", InboxEvent{ChatID: "D2", Text: "second"})

	emitter := &fakeEmitter{}
	srv := NewChannelServer(town, session, emitter)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = srv.Run(ctx) }()

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(emitter.snapshot()) == 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	got := emitter.snapshot()
	if len(got) != 3 {
		t.Fatalf("emitted %d events, want 3", len(got))
	}
	want := []string{"first", "second", "third"}
	for i, ev := range got {
		if ev.Text != want[i] {
			t.Fatalf("event[%d].Text = %q, want %q", i, ev.Text, want[i])
		}
	}

	// All json files must be deleted post-emit.
	if n := countJSONFiles(t, dir); n != 0 {
		t.Fatalf("%d .json files left after replay, want 0", n)
	}
}

func TestChannelServer_NewFileWhileWatching(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	dir := InboxDir(town, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	emitter := &fakeEmitter{}
	srv := NewChannelServer(town, session, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Run(ctx) }()

	// Wait for the watch loop to start.
	time.Sleep(150 * time.Millisecond)

	// Use the same tmp+rename pattern the daemon uses.
	atomicWriteInboxFile(t, dir, "500-x.json", InboxEvent{ChatID: "DX", Text: "live"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(emitter.snapshot()) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := emitter.snapshot()
	if len(got) != 1 || got[0].Text != "live" {
		t.Fatalf("got events %+v, want one [live]", got)
	}
}

func TestChannelServer_PermanentEmitFailurePreservesFile(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	dir := InboxDir(town, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	directWriteInboxFile(t, dir, "1-a.json", InboxEvent{ChatID: "DA", Text: "always-fail"})

	emitter := &alwaysFailEmitter{}
	srv := NewChannelServer(town, session, emitter)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_ = srv.Run(ctx) // blocks until ctx done; replay happens during it

	if n := countJSONFiles(t, dir); n != 1 {
		t.Fatalf("got %d .json files after permanent failure, want 1 (file preserved for retry)", n)
	}
	calls := emitter.callCount()
	if calls < 1 {
		t.Fatalf("emit was never called; replay may not be running")
	}
	// With the in-memory failure tracker, processOne stops retrying after
	// maxRetriesPerPath. The fsnotify rename-back loop should be capped by
	// the same gate, so total calls in 1500ms must not exceed the retry cap
	// by much. Allow some slack for backoff timing.
	if calls > 10 {
		t.Fatalf("emit called %d times; expected bounded retries (max %d)", calls, maxRetriesPerPath)
	}
}

func TestChannelServer_FailOnceThenSucceed(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	dir := InboxDir(town, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	directWriteInboxFile(t, dir, "1-a.json", InboxEvent{ChatID: "DA", Text: "retry-me"})

	emitter := &flakyOnceEmitter{}
	srv := NewChannelServer(town, session, emitter)

	// Single Run pass: replay encounters the file, emit fails (call 1),
	// backoff (~100ms), the scheduled retry timer fires, emit succeeds
	// (call 2), file deleted. We poll for the success state — both the
	// successful emit recorded AND the .json file gone — because there
	// is a transient window where the file is renamed to .claimed.* and
	// counting only .json would falsely report zero before the emit
	// resolves.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = srv.Run(ctx) }()

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		called, events := emitter.snapshot()
		if called >= 2 && len(events) == 1 && countJSONFiles(t, dir) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	called, events := emitter.snapshot()
	if called < 2 {
		t.Fatalf("emit called %d times, want at least 2 (one fail + one success)", called)
	}
	if len(events) != 1 || events[0].Text != "retry-me" {
		t.Fatalf("got events %+v, want exactly one [retry-me]", events)
	}
	if n := countJSONFiles(t, dir); n != 0 {
		t.Fatalf("%d .json files still present after successful retry, want 0", n)
	}
}

func TestChannelServer_RecoversFromEmitterPanic(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	dir := InboxDir(town, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	directWriteInboxFile(t, dir, "1-a.json", InboxEvent{ChatID: "DA", Text: "panic-me"})

	emitter := &panickyEmitter{}
	srv := NewChannelServer(town, session, emitter)

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	select {
	case <-done:
		// Good — Run returned without panic crashing the goroutine
		// outside our recovery.
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel; panic likely killed goroutine")
	}

	// Panic during emit should be treated as a failed emit; file preserved.
	if n := countJSONFiles(t, dir); n != 1 {
		t.Fatalf("got %d .json files after panic, want 1 (preserved for retry)", n)
	}
}
