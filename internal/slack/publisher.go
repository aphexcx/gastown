package slack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// SlackPoster is the narrow Slack surface the publisher depends on.
// Production wires this to *Client; tests pass a fake poster to assert
// outbox-drain behavior without hitting the Slack API.
type SlackPoster interface {
	PostMessage(ctx context.Context, args PostMessageArgs) (string, error)
	UploadFile(ctx context.Context, chatID, threadTS, path string) error
}

// Publisher drains the outbox directory and posts messages to Slack. It owns
// no state other than retry bookkeeping. Construct with NewPublisher and run
// with Run(ctx) in a goroutine.
type Publisher struct {
	OutboxDir    string
	Poster       SlackPoster
	Routing      RoutingTable
	RescanPeriod time.Duration
	StaleClaim   time.Duration

	// NudgeAgent is called on permanent failure so the sending agent hears
	// about it. The daemon wires this to internal/nudge.Enqueue.
	NudgeAgent func(address, reason string)

	// ClearThreadStatus is called after a successful outbound post so the
	// Slack "working..." thinking indicator is dismissed. Best-effort:
	// failures are silent. Wired to client.SetAssistantStatus with empty
	// status by the daemon. If nil, the hook is skipped.
	ClearThreadStatus func(chatID, threadTS string)

	// Metrics, atomic for cheap reads from `gt slack status`.
	lastPost atomic.Int64 // unix nanos
	failed   atomic.Int64
	pending  atomic.Int64
}

// NewPublisher validates inputs and returns a ready-to-run publisher.
// The poster parameter is typically a *Client in production; tests pass
// a fake that implements SlackPoster.
func NewPublisher(outboxDir string, poster SlackPoster, rt RoutingTable) *Publisher {
	return &Publisher{
		OutboxDir:    outboxDir,
		Poster:       poster,
		Routing:      rt,
		RescanPeriod: 2 * time.Second,
		StaleClaim:   5 * time.Minute,
	}
}

// LastPost returns the most recent successful post time.
func (p *Publisher) LastPost() time.Time {
	ns := p.lastPost.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// FailedCount returns the dead-letter count (not total failures, current size).
func (p *Publisher) FailedCount() int64 { return p.failed.Load() }

// PendingCount returns the current pending outbox count (last observed).
func (p *Publisher) PendingCount() int64 { return p.pending.Load() }

// Run drains the outbox until ctx is cancelled. Uses fsnotify as a latency
// hint and a periodic rescan as the source of truth.
func (p *Publisher) Run(ctx context.Context) error {
	if err := os.MkdirAll(p.OutboxDir, 0o700); err != nil {
		return fmt.Errorf("mkdir outbox: %w", err)
	}
	// Startup sweep: reclaim stale claims so crashed/killed daemons don't
	// leave outbox messages stuck forever.
	_ = SweepStaleClaims(p.OutboxDir, p.StaleClaim)
	// Initial drain so anything queued before daemon start gets flushed.
	p.drainOnce(ctx)
	// Refresh failed-count metric from disk state.
	p.refreshFailedCount()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify watcher: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(p.OutboxDir); err != nil {
		return fmt.Errorf("watch outbox: %w", err)
	}

	ticker := time.NewTicker(p.RescanPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final drain pass before returning — spec requires up to 5s
			// drain on SIGTERM so in-flight replies don't get stranded.
			drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			p.drainOnce(drainCtx)
			cancel()
			return nil
		case <-ticker.C:
			_ = SweepStaleClaims(p.OutboxDir, p.StaleClaim)
			p.drainOnce(ctx)
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			// fsnotify events are hints. We don't care which file
			// changed — any event triggers a full drain.
			_ = ev
			p.drainOnce(ctx)
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "slack: fsnotify error: %v\n", err)
		}
	}
}

func (p *Publisher) drainOnce(ctx context.Context) {
	pending, err := ListPendingOutbox(p.OutboxDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slack: list outbox: %v\n", err)
		return
	}
	p.pending.Store(int64(len(pending)))

	for _, path := range pending {
		if ctx.Err() != nil {
			return
		}
		p.processOne(ctx, path)
	}
}

func (p *Publisher) processOne(ctx context.Context, path string) {
	claim, err := ClaimOutboxMessage(path)
	if err != nil {
		// Lost the race to another iteration — normal under high load.
		return
	}
	msg, err := ReadOutboxMessage(claim)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slack: read claim %s: %v\n", claim, err)
		// Malformed JSON — can't use DeadLetterOutboxMessage (it re-reads
		// the file and would fail the same way). Move the raw file directly
		// to failed/ so it doesn't jam the queue in an infinite loop.
		quarantineRawClaim(p.OutboxDir, claim)
		p.refreshFailedCount()
		return
	}

	// Look up display name. Missing → fall back to the raw address.
	displayName := msg.From
	for name, addr := range p.Routing {
		if addr == msg.From {
			displayName = name
			break
		}
	}

	_, err = p.Poster.PostMessage(ctx, PostMessageArgs{
		ChatID:   msg.ChatID,
		Text:     msg.Text,
		ThreadTS: msg.ThreadTS,
		Username: displayName,
	})
	if err != nil {
		p.handlePostError(claim, msg, err)
		return
	}

	// Upload any attached files in the same thread. Text + files is treated
	// as one atomic delivery unit: if ANY upload fails transiently, unclaim
	// the whole message for retry. If permanent, dead-letter the whole thing.
	for _, file := range msg.Files {
		if err := p.Poster.UploadFile(ctx, msg.ChatID, msg.ThreadTS, file); err != nil {
			p.handlePostError(claim, msg, err)
			return
		}
	}

	// Success — remove the claim file.
	if err := os.Remove(claim); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "slack: remove claim %s: %v\n", claim, err)
	}
	p.lastPost.Store(time.Now().UnixNano())

	// Clear the "working..." thread indicator set on inbound receipt.
	// We use ThreadTS if set; otherwise there's no thread to clear.
	if p.ClearThreadStatus != nil && msg.ThreadTS != "" {
		p.ClearThreadStatus(msg.ChatID, msg.ThreadTS)
	}
}

func (p *Publisher) handlePostError(claim string, msg *OutboxMessage, err error) {
	if IsTransient(err) {
		// Unclaim for retry on next tick.
		if unerr := UnclaimOutboxMessage(claim); unerr != nil {
			fmt.Fprintf(os.Stderr, "slack: unclaim after transient error: %v\n", unerr)
		}
		fmt.Fprintf(os.Stderr, "slack: transient error, will retry: %v\n", err)
		return
	}
	// Permanent (or unclassified): dead-letter + nudge the sender.
	if derr := DeadLetterOutboxMessage(claim, err.Error()); derr != nil {
		fmt.Fprintf(os.Stderr, "slack: dead-letter failed: %v\n", derr)
	}
	if p.NudgeAgent != nil {
		p.NudgeAgent(msg.From, fmt.Sprintf("slack post failed: %v", err))
	}
	// Clear any lingering "working..." thread indicator so the user in
	// Slack isn't left staring at a perpetually-working status when the
	// message is actually dead-lettered.
	if p.ClearThreadStatus != nil && msg.ThreadTS != "" {
		p.ClearThreadStatus(msg.ChatID, msg.ThreadTS)
	}
	p.refreshFailedCount()
}

func (p *Publisher) refreshFailedCount() {
	failedDir := filepath.Join(p.OutboxDir, failedDirName)
	entries, err := os.ReadDir(failedDir)
	if err != nil {
		p.failed.Store(0)
		return
	}
	var n int64
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			n++
		}
	}
	p.failed.Store(n)
}

// quarantineRawClaim moves a claimed file that can't be parsed as JSON
// directly into the failed/ directory. Unlike DeadLetterOutboxMessage, this
// does NOT attempt to re-read or re-marshal the file — it just renames it.
func quarantineRawClaim(outboxDir, claim string) {
	failedDir := filepath.Join(outboxDir, failedDirName)
	if err := os.MkdirAll(failedDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "slack: mkdir failed/ for quarantine: %v\n", err)
		return
	}
	base := filepath.Base(claim)
	// Strip .claimed.* suffix to get a clean filename in failed/.
	if idx := strings.Index(base, ".claimed."); idx > 0 {
		base = base[:idx]
	}
	dest := filepath.Join(failedDir, base)
	if err := os.Rename(claim, dest); err != nil {
		fmt.Fprintf(os.Stderr, "slack: quarantine %s: %v\n", claim, err)
	}
}
