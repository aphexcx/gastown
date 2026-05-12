package slack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakePoster implements SlackPoster for publisher_test.go. Each call goes
// through postFn / uploadFn so tests can inject behavior per case. Thread
// safe because the publisher calls into it from a single goroutine at a
// time, but we lock anyway for test-side inspection.
type fakePoster struct {
	mu       sync.Mutex
	posts    []PostMessageArgs
	uploads  []string
	postFn   func(args PostMessageArgs) (string, error)
	uploadFn func(path string) error
}

func (f *fakePoster) PostMessage(ctx context.Context, args PostMessageArgs) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, args)
	if f.postFn == nil {
		return "1234.5678", nil
	}
	return f.postFn(args)
}

func (f *fakePoster) UploadFile(ctx context.Context, chatID, threadTS, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, path)
	if f.uploadFn == nil {
		return nil
	}
	return f.uploadFn(path)
}

func (f *fakePoster) postsSnapshot() []PostMessageArgs {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]PostMessageArgs, len(f.posts))
	copy(out, f.posts)
	return out
}

func newTestPublisher(t *testing.T, poster SlackPoster) (string, *Publisher) {
	t.Helper()
	dir := t.TempDir()
	outbox := filepath.Join(dir, "slack_outbox")
	require.NoError(t, os.MkdirAll(outbox, 0o700))

	p := NewPublisher(outbox, poster, RoutingTable{})
	// Tight loop so the test finishes quickly.
	p.RescanPeriod = 30 * time.Millisecond
	p.StaleClaim = 5 * time.Minute
	return outbox, p
}

func runUntil(t *testing.T, p *Publisher, cond func() bool, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publisher did not exit after cancel")
	}
}

func TestPublisher_PostsAndRemoves(t *testing.T) {
	poster := &fakePoster{}
	outbox, p := newTestPublisher(t, poster)

	_, err := WriteOutboxMessage(outbox, &OutboxMessage{
		From: "mayor/", ChatID: "C1", Text: "hello",
	})
	require.NoError(t, err)

	runUntil(t, p, func() bool { return len(poster.postsSnapshot()) > 0 }, 2*time.Second)

	posts := poster.postsSnapshot()
	require.Len(t, posts, 1)
	require.Equal(t, "C1", posts[0].ChatID)
	require.Equal(t, "hello", posts[0].Text)

	pending, err := ListPendingOutbox(outbox)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestPublisher_TransientUnclaims(t *testing.T) {
	var calls int
	var mu sync.Mutex
	poster := &fakePoster{
		postFn: func(args PostMessageArgs) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return "", fmt.Errorf("%w: flaky network", ErrTransient)
			}
			return "1234.5678", nil
		},
	}
	outbox, p := newTestPublisher(t, poster)

	_, err := WriteOutboxMessage(outbox, &OutboxMessage{From: "mayor/", ChatID: "C1", Text: "hi"})
	require.NoError(t, err)

	runUntil(t, p, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls >= 2
	}, 2*time.Second)

	mu.Lock()
	require.GreaterOrEqual(t, calls, 2, "publisher should retry after transient error")
	mu.Unlock()
}

func TestPublisher_PermanentDeadLetters(t *testing.T) {
	poster := &fakePoster{
		postFn: func(args PostMessageArgs) (string, error) {
			return "", fmt.Errorf("%w: channel_not_found", ErrPermanent)
		},
	}
	outbox, p := newTestPublisher(t, poster)

	_, err := WriteOutboxMessage(outbox, &OutboxMessage{From: "mayor/", ChatID: "C1", Text: "hi"})
	require.NoError(t, err)

	runUntil(t, p, func() bool { return p.FailedCount() >= 1 }, 2*time.Second)

	require.Equal(t, int64(1), p.FailedCount())
	pending, err := ListPendingOutbox(outbox)
	require.NoError(t, err)
	require.Empty(t, pending)

	// Verify the failed dir actually contains the message with failure_reason.
	failedDir := filepath.Join(outbox, "failed")
	entries, err := os.ReadDir(failedDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	data, err := os.ReadFile(filepath.Join(failedDir, entries[0].Name()))
	require.NoError(t, err)
	require.Contains(t, string(data), "channel_not_found")
}
