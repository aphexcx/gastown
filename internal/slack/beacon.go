// Subscription beacon for the Claude channels delivery path.
//
// The per-session channel-server holds an exclusive flock on
// slack_inbox/<safe-session>/.subscribed for its entire lifetime. The
// daemon probes this lock per inbound event:
//
//   - lock acquired (FlockTryAcquire ok=true)  → no plugin alive →
//     fall back to legacy nudge_queue path
//   - lock blocked (FlockTryAcquire ok=false)  → plugin alive →
//     write event to slack_inbox/<safe-session>/<ts>.json
//   - error                                    → log + treat as
//     "not subscribed" so we fail closed to the legacy path rather
//     than losing messages on a probe glitch
//
// We delegate the actual flock to internal/lock.FlockTryAcquire so we
// inherit its cross-platform stub (Windows is a no-op) and its
// EWOULDBLOCK-vs-real-error distinction. This keeps beacon.go tiny —
// it's just slack-specific path resolution + a typed Holder.
package slack

import (
	"fmt"
	"os"

	"github.com/steveyegge/gastown/internal/lock"
)

// SubscriptionHolder owns the cleanup function returned by FlockTryAcquire.
// Created by AcquireSubscribed; released via Release. Release is idempotent
// and nil-safe so callers can defer it without panicking on already-released
// or nil holders.
type SubscriptionHolder struct {
	release func()
}

// Release the lock and close the underlying fd. Idempotent: calling more
// than once is a no-op. Safe on a nil receiver (so `defer holder.Release()`
// works even if AcquireSubscribed returned (nil, err)).
func (h *SubscriptionHolder) Release() {
	if h == nil || h.release == nil {
		return
	}
	h.release()
	h.release = nil
}

// AcquireSubscribed creates the per-session inbox directory if needed and
// takes an exclusive non-blocking flock on the beacon file. Returns an
// error if another process already holds the lock.
//
// The returned holder owns the lock for the caller's lifetime. Defer
// holder.Release() in the caller; the kernel auto-releases the flock on
// process death so a forgotten Release is bounded by the process lifecycle.
func AcquireSubscribed(townRoot, session string) (*SubscriptionHolder, error) {
	dir := InboxDir(townRoot, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create inbox dir: %w", err)
	}
	path := SubscribedBeaconPath(townRoot, session)
	cleanup, ok, err := lock.FlockTryAcquire(path)
	if err != nil {
		return nil, fmt.Errorf("flock beacon: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("beacon already locked: %s", path)
	}
	return &SubscriptionHolder{release: cleanup}, nil
}

// IsSubscribed returns true if some process currently holds the exclusive
// flock on the session's subscription beacon. Probes by trying to acquire
// the lock non-blocking and immediately releasing on success.
//
// Returns false if no process holds the lock OR on any error — we fail
// closed to the legacy nudge_queue path rather than losing inbound
// messages. Errors are logged to stderr for operator visibility but
// never propagated, since the daemon's per-event dispatch must always
// reach a delivery decision.
func IsSubscribed(townRoot, session string) bool {
	path := SubscribedBeaconPath(townRoot, session)
	cleanup, ok, err := lock.FlockTryAcquire(path)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"slack: IsSubscribed probe failed for %s, treating as not-subscribed: %v\n",
			session, err)
		return false
	}
	if ok {
		// We acquired — no one else holds it. Release immediately.
		cleanup()
		return false
	}
	// !ok && err == nil means EWOULDBLOCK — someone holds it.
	return true
}
