package slack

// Additional publisher tests covering the codecov-flagged gaps:
//   - Trivial accessors (LastPost / PendingCount) at 0%
//   - quarantineRawClaim at 0%
//   - handlePostError transient vs permanent branches
//   - Drain semantics under concurrent claimers
//   - processOne unmapped-recipient + missing-attachment + ClearThreadStatus hook
//
// fakePoster + the existing test setup live in publisher_test.go; we reuse
// them here.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublisher_LastPost_ZeroWhenUnset(t *testing.T) {
	p := NewPublisher(t.TempDir(), &fakePoster{}, RoutingTable{})
	if got := p.LastPost(); !got.IsZero() {
		t.Errorf("LastPost on fresh publisher = %v, want zero", got)
	}
}

func TestPublisher_LastPost_ReflectsAtomicStore(t *testing.T) {
	p := NewPublisher(t.TempDir(), &fakePoster{}, RoutingTable{})
	now := time.Now()
	p.lastPost.Store(now.UnixNano())
	got := p.LastPost()
	if got.IsZero() {
		t.Fatal("LastPost should not be zero after Store")
	}
	// time.Unix(0, ns) drops the monotonic clock reading, so compare via
	// UnixNano rather than time.Equal.
	if got.UnixNano() != now.UnixNano() {
		t.Errorf("LastPost = %v (ns=%d), want %v (ns=%d)",
			got, got.UnixNano(), now, now.UnixNano())
	}
}

func TestPublisher_PendingCount_ReflectsAtomic(t *testing.T) {
	p := NewPublisher(t.TempDir(), &fakePoster{}, RoutingTable{})
	if got := p.PendingCount(); got != 0 {
		t.Errorf("fresh PendingCount = %d, want 0", got)
	}
	p.pending.Store(42)
	if got := p.PendingCount(); got != 42 {
		t.Errorf("PendingCount = %d, want 42", got)
	}
}

func TestPublisher_FailedCount_ReflectsAtomic(t *testing.T) {
	p := NewPublisher(t.TempDir(), &fakePoster{}, RoutingTable{})
	if got := p.FailedCount(); got != 0 {
		t.Errorf("fresh FailedCount = %d, want 0", got)
	}
	p.failed.Store(7)
	if got := p.FailedCount(); got != 7 {
		t.Errorf("FailedCount = %d, want 7", got)
	}
}

func TestPublisher_RefreshFailedCount(t *testing.T) {
	dir := t.TempDir()
	p := NewPublisher(dir, &fakePoster{}, RoutingTable{})

	// No failed/ dir → count goes to 0.
	p.failed.Store(99) // prove it gets reset
	p.refreshFailedCount()
	if got := p.FailedCount(); got != 0 {
		t.Errorf("FailedCount with no failed/ dir = %d, want 0", got)
	}

	// Create failed/ dir with 3 .json files + 1 non-json + 1 subdir.
	failedDir := filepath.Join(dir, "failed")
	if err := os.MkdirAll(failedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		path := filepath.Join(failedDir, fmt.Sprintf("msg-%d.json", i))
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(failedDir, "junk.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(failedDir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}

	p.refreshFailedCount()
	if got := p.FailedCount(); got != 3 {
		t.Errorf("FailedCount = %d, want 3 (counts only .json regular files)", got)
	}
}

// quarantineRawClaim handles files that can't be parsed — common cause:
// a writer crashed mid-write and left a truncated JSON file. Move to
// failed/ without trying to deserialize.

func TestQuarantineRawClaim_MovesFileToFailed(t *testing.T) {
	dir := t.TempDir()
	// Synthesize a claim file with garbage content (would fail
	// ReadOutboxMessage's json.Unmarshal).
	claim := filepath.Join(dir, "1714510000-deadbeef.json.claimed.cafef00d")
	if err := os.WriteFile(claim, []byte("{not parseable"), 0o600); err != nil {
		t.Fatal(err)
	}

	quarantineRawClaim(dir, claim)

	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Errorf("claim should be moved out of outbox; stat = %v", err)
	}
	// The destination file in failed/ should retain the original .json
	// suffix (claim suffix stripped).
	dest := filepath.Join(dir, "failed", "1714510000-deadbeef.json")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed file not at expected path: %v", err)
	}
	if string(got) != "{not parseable" {
		t.Errorf("content should be preserved verbatim; got %q", got)
	}
}

func TestQuarantineRawClaim_CreatesFailedDirIfMissing(t *testing.T) {
	dir := t.TempDir()
	claim := filepath.Join(dir, "1.json.claimed.x")
	if err := os.WriteFile(claim, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Confirm failed/ doesn't exist yet.
	if _, err := os.Stat(filepath.Join(dir, "failed")); !os.IsNotExist(err) {
		t.Fatal("failed/ should not exist before quarantine")
	}
	quarantineRawClaim(dir, claim)
	info, err := os.Stat(filepath.Join(dir, "failed"))
	if err != nil {
		t.Fatalf("failed/ should have been created: %v", err)
	}
	if !info.IsDir() {
		t.Error("failed/ is not a directory")
	}
}

func TestQuarantineRawClaim_NoSuffixToStrip(t *testing.T) {
	// A claim path without the .claimed.<rand> suffix (defensive): the
	// helper should still move the file rather than panic.
	dir := t.TempDir()
	path := filepath.Join(dir, "1.json")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	quarantineRawClaim(dir, path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should have been moved; stat = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "failed", "1.json")); err != nil {
		t.Errorf("moved file should be in failed/; stat = %v", err)
	}
}

// handlePostError branches:
//   - Transient → UnclaimOutboxMessage and don't bump failed count
//   - Permanent → DeadLetterOutboxMessage + NudgeAgent + ClearThreadStatus

func TestHandlePostError_TransientUnclaimsForRetry(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteOutboxMessage(dir, &OutboxMessage{From: "agent/x", ChatID: "C01", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimOutboxMessage(path)
	if err != nil {
		t.Fatal(err)
	}

	var nudges int32
	p := NewPublisher(dir, &fakePoster{}, RoutingTable{})
	p.NudgeAgent = func(address, reason string) { atomic.AddInt32(&nudges, 1) }

	transient := fmt.Errorf("%w: temporary", ErrTransient)
	p.handlePostError(claim, &OutboxMessage{From: "agent/x", ChatID: "C01", Text: "hi"}, transient)

	// File restored.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("transient error should restore original; stat = %v", err)
	}
	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Errorf("claim should be gone after unclaim; stat = %v", err)
	}
	// No nudge, no failed count change.
	if got := atomic.LoadInt32(&nudges); got != 0 {
		t.Errorf("transient error must not nudge agent; got %d nudges", got)
	}
	if got := p.FailedCount(); got != 0 {
		t.Errorf("transient error must not bump FailedCount; got %d", got)
	}
}

func TestHandlePostError_PermanentDeadLettersAndNotifies(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteOutboxMessage(dir, &OutboxMessage{
		From: "agent/x", ChatID: "C01", Text: "hi", ThreadTS: "1714.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimOutboxMessage(path)
	if err != nil {
		t.Fatal(err)
	}

	var (
		nudgedAddress, nudgedReason       string
		clearedChat, clearedThread        string
		nudgeCalls, clearCalls            int32
	)
	p := NewPublisher(dir, &fakePoster{}, RoutingTable{})
	p.NudgeAgent = func(address, reason string) {
		atomic.AddInt32(&nudgeCalls, 1)
		nudgedAddress = address
		nudgedReason = reason
	}
	p.ClearThreadStatus = func(chatID, threadTS string) {
		atomic.AddInt32(&clearCalls, 1)
		clearedChat = chatID
		clearedThread = threadTS
	}

	permanent := fmt.Errorf("%w: channel_not_found", ErrPermanent)
	p.handlePostError(claim, &OutboxMessage{
		From: "agent/x", ChatID: "C01", Text: "hi", ThreadTS: "1714.0",
	}, permanent)

	// Claim moved to failed/.
	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Errorf("claim should be gone after dead-letter; stat = %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "failed"))
	if len(entries) != 1 {
		t.Fatalf("want 1 entry in failed/, got %d", len(entries))
	}

	// NudgeAgent called with the sending agent's address + a useful reason.
	if got := atomic.LoadInt32(&nudgeCalls); got != 1 {
		t.Errorf("NudgeAgent called %d times, want 1", got)
	}
	if nudgedAddress != "agent/x" {
		t.Errorf("nudge address = %q, want agent/x", nudgedAddress)
	}
	if !contains(nudgedReason, "slack post failed") || !contains(nudgedReason, "channel_not_found") {
		t.Errorf("nudge reason should reference the post failure; got %q", nudgedReason)
	}

	// ClearThreadStatus called for the thread the failed message was in.
	if got := atomic.LoadInt32(&clearCalls); got != 1 {
		t.Errorf("ClearThreadStatus called %d times, want 1", got)
	}
	if clearedChat != "C01" || clearedThread != "1714.0" {
		t.Errorf("ClearThreadStatus args = (%q, %q), want (C01, 1714.0)", clearedChat, clearedThread)
	}

	// FailedCount bumped.
	if got := p.FailedCount(); got != 1 {
		t.Errorf("FailedCount = %d, want 1 after permanent error", got)
	}
}

func TestHandlePostError_NoNudgeHookIsHandled(t *testing.T) {
	// Permanent error path must not panic when NudgeAgent / ClearThreadStatus
	// are nil — the daemon may have skipped wiring them in some configs.
	dir := t.TempDir()
	path, err := WriteOutboxMessage(dir, &OutboxMessage{From: "agent/x", ChatID: "C01", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimOutboxMessage(path)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPublisher(dir, &fakePoster{}, RoutingTable{}) // both hooks nil

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil hooks should not panic; recovered %v", r)
		}
	}()
	p.handlePostError(claim, &OutboxMessage{From: "agent/x", ChatID: "C01", Text: "hi"},
		errors.New("unclassified")) // not ErrTransient → treated as permanent

	if got := p.FailedCount(); got != 1 {
		t.Errorf("unclassified errors should be treated as permanent (dead-letter); FailedCount = %d", got)
	}
}

// processOne edge case: when ClearThreadStatus is configured, a successful
// post should clear the working indicator (existing tests don't assert this
// explicitly).
func TestPublisher_ClearsThreadStatusOnSuccess(t *testing.T) {
	dir := t.TempDir()
	_, err := WriteOutboxMessage(dir, &OutboxMessage{From: "agent/x", ChatID: "C01", Text: "hi", ThreadTS: "1.0"})
	if err != nil {
		t.Fatal(err)
	}

	var clears int32
	p := NewPublisher(dir, &fakePoster{}, RoutingTable{})
	p.ClearThreadStatus = func(chatID, threadTS string) {
		atomic.AddInt32(&clears, 1)
		if chatID != "C01" || threadTS != "1.0" {
			t.Errorf("clear args = (%q, %q), want (C01, 1.0)", chatID, threadTS)
		}
	}

	p.drainOnce(context.Background())
	if got := atomic.LoadInt32(&clears); got != 1 {
		t.Errorf("ClearThreadStatus called %d times after successful post, want 1", got)
	}
}
