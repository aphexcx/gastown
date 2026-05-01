// ChannelServer drives the per-session channel-server's event-processing
// loop: hold the subscription beacon, watch the per-session inbox dir,
// atomically claim each .json file, emit it via an injected Emitter,
// delete on success or restore on failure for a future retry.
//
// The Emitter interface is the seam between this package and the MCP
// transport. Production code passes an Emitter that calls
// notifications/claude/channel via the MCP server (Task 8). Tests pass
// a fake Emitter that records into a slice.
package slack

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Emitter receives an InboxEvent ready for delivery to the assistant
// context. Production: emits notifications/claude/channel via MCP.
// Tests: records into a slice.
//
// Returning a non-nil error causes the channel server to RESTORE the
// inbox file (rename .claimed.* back to .json) so a future Run pass
// retries delivery. Panics are also treated as failed emits (recovered
// inside processOne) — both are intentional so a misbehaving Emitter
// can never permanently lose a queued message.
type Emitter interface {
	Emit(InboxEvent) error
}

// ChannelServer runs the inbox watch + claim + emit loop for one session.
// One ChannelServer per Claude Code session; Run blocks until ctx is
// cancelled or a fatal error occurs.
type ChannelServer struct {
	townRoot string
	session  string
	emitter  Emitter

	// failures tracks per-path retry state in memory so a permanently-
	// failing Emit doesn't hot-loop via fsnotify after each restore-
	// rename. Cleared on process restart — durability is preserved by
	// leaving the file on disk (a future plugin start retries from scratch).
	failuresMu sync.Mutex
	failures   map[string]*pathRetry
}

type pathRetry struct {
	count  int
	nextOK time.Time
}

const (
	// maxRetriesPerPath: after this many consecutive emit failures for
	// the same path, we stop retrying for the lifetime of this process.
	// The file stays on disk so a future channel-server start (after the
	// bug is fixed) will pick it up cleanly.
	maxRetriesPerPath = 5
)

// backoffForAttempt returns the wait window before processOne is willing
// to re-attempt a previously failed path. The schedule is 100ms, 500ms,
// 2s, 5s, 10s — caps at 10s.
func backoffForAttempt(attempt int) time.Duration {
	table := []time.Duration{
		100 * time.Millisecond,
		500 * time.Millisecond,
		2 * time.Second,
		5 * time.Second,
		10 * time.Second,
	}
	if attempt <= 0 {
		return table[0]
	}
	if attempt-1 >= len(table) {
		return table[len(table)-1]
	}
	return table[attempt-1]
}

// NewChannelServer constructs a server. Acquisition of the subscription
// beacon happens in Run, not here, so tests can construct one without
// filesystem side effects beyond MkdirAll(InboxDir).
func NewChannelServer(townRoot, session string, emitter Emitter) *ChannelServer {
	return &ChannelServer{
		townRoot: townRoot,
		session:  session,
		emitter:  emitter,
		failures: map[string]*pathRetry{},
	}
}

// Run blocks until ctx is cancelled or a fatal error occurs.
//
// Order:
//  1. Acquire flock on the subscription beacon.
//  2. Start fsnotify watch on the inbox dir BEFORE replay scan, so files
//     written during the brief startup window are caught by either the
//     replay scan or fsnotify.
//  3. Replay scan: process all existing *.json files in FIFO order
//     (filenames are <unixnano>-<random>.json — lexically sortable).
//  4. Steady state: process new files as fsnotify reports them.
//  5. On ctx done: release beacon (deferred), return.
func (s *ChannelServer) Run(ctx context.Context) error {
	holder, err := AcquireSubscribed(s.townRoot, s.session)
	if err != nil {
		return fmt.Errorf("acquire subscribed beacon: %w", err)
	}
	defer holder.Release()

	dir := InboxDir(s.townRoot, s.session)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify new: %w", err)
	}
	defer watcher.Close()
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("fsnotify watch %s: %w", dir, err)
	}

	// Replay AFTER watcher is registered so any file written during the
	// brief gap is caught by either the replay scan or fsnotify.
	if err := s.replay(ctx, dir); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-watcher.Events:
			if !ok {
				// Closed channels mean fsnotify is shutting down (likely
				// because we called watcher.Close() via the defer) —
				// normal termination.
				return nil
			}
			// Only act on Create / Write of .json files. The atomic-
			// rename pattern (tmp → final) shows up as Create on the
			// final path. Bare WriteFile shows up as Create + Write.
			if ev.Op&fsnotify.Create == 0 && ev.Op&fsnotify.Write == 0 {
				continue
			}
			if !strings.HasSuffix(ev.Name, ".json") {
				continue
			}
			s.processOne(ctx, ev.Name)
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "channel-server: fsnotify error: %v\n", err)
		}
	}
}

// replay processes pre-existing files in FIFO order before the watch loop
// goes live. Bounded by ctx cancellation.
func (s *ChannelServer) replay(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read inbox: %w", err)
	}
	var jsons []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		jsons = append(jsons, e.Name())
	}
	sort.Strings(jsons) // FIFO via timestamp-prefixed names.
	for _, name := range jsons {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		s.processOne(ctx, filepath.Join(dir, name))
	}
	return nil
}

// processOne handles one inbox file: atomic claim → parse → emit
// (panic-recovered) → delete-on-success / restore-on-failure.
//
// The claim rename moves <name>.json → <name>.claimed.<rand> atomically.
// On a lost race (another claimer won, or the file vanished) the rename
// fails with ENOENT — we silently move on. On a parsing failure we
// quarantine by leaving the .claimed file in place so we don't loop
// forever on a poison pill.
//
// Emit failures consult an in-memory per-path retry tracker. After
// maxRetriesPerPath consecutive failures we stop retrying for the
// lifetime of this process — the file is restored to disk so a future
// process can pick it up after whatever was wrong is fixed. This caps
// the fsnotify rename-back hot loop that would otherwise burn CPU when
// the Emitter is permanently broken.
func (s *ChannelServer) processOne(ctx context.Context, path string) {
	if !strings.HasSuffix(path, ".json") {
		return
	}

	// Check failure-tracking BEFORE claiming the file. If a path has
	// burned through its retries OR is still in a backoff window, leave
	// the file alone — claiming and restoring would fire a fresh
	// fsnotify Create that immediately re-enters this function and
	// causes a rename-back hot loop. The file stays on disk; the
	// previously-scheduled retry timer (if any) will pick it up.
	s.failuresMu.Lock()
	if state := s.failures[path]; state != nil {
		if state.count >= maxRetriesPerPath {
			s.failuresMu.Unlock()
			// Permanent skip in this process. The transition was
			// already logged when the retry budget was exhausted;
			// subsequent fsnotify-driven re-entries are silent.
			return
		}
		if time.Now().Before(state.nextOK) {
			s.failuresMu.Unlock()
			// Still in backoff — the timer scheduled at the failure
			// site will retry once the window elapses. No need to
			// touch the file or schedule another timer.
			return
		}
	}
	s.failuresMu.Unlock()

	claimed := path + ".claimed." + channelRandSuffix()
	if err := os.Rename(path, claimed); err != nil {
		// Lost the race or file already gone — fine.
		return
	}

	data, err := os.ReadFile(claimed)
	if err != nil {
		// Best-effort restore for a future retry.
		_ = os.Rename(claimed, path)
		fmt.Fprintf(os.Stderr, "channel-server: read claimed %s: %v\n", claimed, err)
		return
	}
	var ev InboxEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		// Malformed JSON — quarantine by leaving .claimed in place.
		// Restoring would loop forever on the same poison pill.
		fmt.Fprintf(os.Stderr, "channel-server: malformed inbox file %s: %v\n", claimed, err)
		return
	}

	// Panic-recovered emit. Both errors and panics restore the file.
	emitErr := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("emit panicked: %v", r)
				fmt.Fprintf(os.Stderr,
					"channel-server: emit panic for %s: %v\n", path, r)
			}
		}()
		return s.emitter.Emit(ev)
	}()

	if emitErr != nil {
		_ = os.Rename(claimed, path)

		s.failuresMu.Lock()
		if s.failures[path] == nil {
			s.failures[path] = &pathRetry{}
		}
		state := s.failures[path]
		state.count++
		state.nextOK = time.Now().Add(backoffForAttempt(state.count))
		permanent := state.count >= maxRetriesPerPath
		retryAfter := backoffForAttempt(state.count)
		failCount := state.count
		s.failuresMu.Unlock()

		if permanent {
			fmt.Fprintf(os.Stderr,
				"channel-server: emit for %s failed %d times (%v); giving up for this process — file preserved for future restart\n",
				path, failCount, emitErr)
		} else {
			fmt.Fprintf(os.Stderr,
				"channel-server: emit failed for %s: %v (will retry after %s)\n",
				path, emitErr, retryAfter)
			// Schedule a deferred re-attempt: the rename-back fires an
			// immediate fsnotify Create that will be gated by the
			// in-memory backoff window, so we cannot rely on that event
			// to drive the retry. A timer aligned with the backoff is
			// what actually advances state.
			s.scheduleRetry(ctx, path, retryAfter)
		}
		return
	}

	// Success — clear any prior failure tracking for this path.
	s.failuresMu.Lock()
	delete(s.failures, path)
	s.failuresMu.Unlock()

	_ = os.Remove(claimed)
}

// scheduleRetry spawns a goroutine that waits the given duration (or
// ctx cancellation) and then re-invokes processOne for the path. This
// is what actually drives backoff-bounded retries: fsnotify Create
// events fired by the failure rename-back are gated by the in-memory
// backoff window, so we need a timer to retry once the window elapses.
//
// Multiple overlapping retries for the same path are harmless — the
// rename-claim is atomic, so only the first one wins; the others see
// ENOENT and silently move on.
func (s *ChannelServer) scheduleRetry(ctx context.Context, path string, wait time.Duration) {
	go func() {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.processOne(ctx, path)
	}()
}

// channelRandSuffix returns a 4-byte hex string. Distinct name from the
// outbox.go randomSuffix to avoid same-package collision.
func channelRandSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
