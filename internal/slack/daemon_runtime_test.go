package slack

// Tests for daemon.go's internal runtime helpers: runGoroutine panic
// recovery, the membership-cache TTL+fail-closed semantics, and the
// pure constructors (NewDaemon happy/sad paths).
//
// dispatchSocketModeEvents isn't directly tested here because constructing
// a socketmode.Client is invasive; its two exit paths (ctx-cancel → nil,
// channel-close → error) are simple enough that the call site contract
// is easier to keep correct via review than via a transport mock.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// runGoroutine recovers from panics and surfaces them as an error on the
// done channel — so the daemon's main loop never silently loses a
// goroutine to a panic.
func TestRunGoroutine_PanicRecovers(t *testing.T) {
	d := &Daemon{}
	done := make(chan error, 1)
	d.runGoroutine("test", done, func() error {
		panic("intentional test panic")
	})

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected panic to be reported as an error; got nil")
		}
		if !contains(err.Error(), "test") {
			t.Errorf("error should name the goroutine; got %v", err)
		}
		if !contains(err.Error(), "intentional test panic") {
			t.Errorf("error should preserve panic value; got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for done")
	}
}

// Non-panic returns propagate through the done channel unchanged.
func TestRunGoroutine_NormalReturn(t *testing.T) {
	d := &Daemon{}
	done := make(chan error, 1)
	want := errors.New("planned exit")
	d.runGoroutine("normal", done, func() error {
		return want
	})
	select {
	case got := <-done:
		if !errors.Is(got, want) && got != want {
			t.Errorf("done received %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for done")
	}
}

func TestRunGoroutine_NilReturn(t *testing.T) {
	d := &Daemon{}
	done := make(chan error, 1)
	d.runGoroutine("nilreturn", done, func() error { return nil })
	select {
	case got := <-done:
		if got != nil {
			t.Errorf("done = %v, want nil", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for done")
	}
}

// membershipCache: positive results cache for positiveTTL, negative for
// negativeTTL, API errors fail closed without caching.

func TestMembershipCache_PositiveResultCaches(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeJSON(t, w, map[string]any{
			"ok": true,
			"channel": map[string]any{
				"id":        "C01",
				"is_member": true,
			},
		})
	}))
	defer srv.Close()

	mc := newMembershipCache(newTestClient(srv))
	for i := 0; i < 5; i++ {
		ok, reason := mc.check("C01")
		if !ok {
			t.Fatalf("iter %d: want ok=true; reason=%s", i, reason)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("API hit %d times, want 1 (positive result should cache)", got)
	}
}

func TestMembershipCache_NegativeResultCaches(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeJSON(t, w, map[string]any{"ok": false, "error": "channel_not_found"})
	}))
	defer srv.Close()

	mc := newMembershipCache(newTestClient(srv))
	for i := 0; i < 5; i++ {
		ok, reason := mc.check("C01")
		if ok {
			t.Fatalf("iter %d: want ok=false; reason=%s", i, reason)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("API hit %d times, want 1 (negative result should cache too)", got)
	}
}

func TestMembershipCache_APIErrorFailsClosedWithoutCaching(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
	}))
	defer srv.Close()

	mc := newMembershipCache(newTestClient(srv))
	for i := 0; i < 3; i++ {
		ok, reason := mc.check("C01")
		if ok {
			t.Fatalf("iter %d: want ok=false (fail-closed); reason=%s", i, reason)
		}
		if !contains(reason, "api_error") {
			t.Errorf("iter %d: reason should mention api_error; got %q", i, reason)
		}
	}
	// Critical: API errors should NOT be cached, so retries hit the API
	// again. Otherwise a transient ratelimit poisons the cache.
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("API hit %d times, want 3 (error must NOT cache)", got)
	}
}

func TestMembershipCache_PositiveExpiresAfterTTL(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeJSON(t, w, map[string]any{
			"ok":      true,
			"channel": map[string]any{"id": "C01", "is_member": true},
		})
	}))
	defer srv.Close()

	mc := newMembershipCache(newTestClient(srv))
	// Force a short positive TTL so the test runs fast.
	mc.positiveTTL = 30 * time.Millisecond

	if ok, _ := mc.check("C01"); !ok {
		t.Fatal("first check should return ok=true")
	}
	if ok, _ := mc.check("C01"); !ok {
		t.Fatal("second check (within TTL) should hit cache and return ok=true")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("after 2 fast checks, expected 1 API hit; got %d", got)
	}

	time.Sleep(50 * time.Millisecond) // exceed positiveTTL

	if ok, _ := mc.check("C01"); !ok {
		t.Fatal("post-expiry check should re-call API and still return ok=true")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("post-expiry API hit count = %d, want 2", got)
	}
}

func TestMembershipCache_DifferentChatIDsTrackedIndependently(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Need to read the channel ID from the form to vary the response.
		_ = r.ParseForm()
		channelID := r.Form.Get("channel")
		switch channelID {
		case "C_OK":
			writeJSON(t, w, map[string]any{
				"ok":      true,
				"channel": map[string]any{"id": channelID, "is_member": true},
			})
		default:
			writeJSON(t, w, map[string]any{"ok": false, "error": "channel_not_found"})
		}
	}))
	defer srv.Close()

	mc := newMembershipCache(newTestClient(srv))
	if ok, _ := mc.check("C_OK"); !ok {
		t.Error("C_OK should return ok=true")
	}
	if ok, _ := mc.check("C_DENY"); ok {
		t.Error("C_DENY should return ok=false")
	}
	// Sanity: subsequent checks return the right cached value per chatID.
	if ok, _ := mc.check("C_OK"); !ok {
		t.Error("C_OK cached value should still be ok=true")
	}
	if ok, _ := mc.check("C_DENY"); ok {
		t.Error("C_DENY cached value should still be ok=false")
	}
}

// NewDaemon: minimal happy + sad path coverage.

func TestNewDaemon_RejectsMissingConfig(t *testing.T) {
	_, err := NewDaemon(DaemonOptions{
		ConfigPath: filepath.Join(t.TempDir(), "does-not-exist.json"),
		TownRoot:   t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestNewDaemon_RejectsInvalidConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "slack.json")
	// Missing required fields → Validate fails.
	if err := os.WriteFile(cfgPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewDaemon(DaemonOptions{
		ConfigPath: cfgPath,
		TownRoot:   t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for empty config (missing required fields)")
	}
}

func TestNewDaemon_RejectsMalformedJSON(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "slack.json")
	if err := os.WriteFile(cfgPath, []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewDaemon(DaemonOptions{
		ConfigPath: cfgPath,
		TownRoot:   t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestNewDaemon_WidePermsRejected(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "slack.json")
	good := `{"bot_token":"xoxb-x","app_token":"xapp-x","owner_user_id":"U01"}`
	if err := os.WriteFile(cfgPath, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewDaemon(DaemonOptions{
		ConfigPath: cfgPath,
		TownRoot:   t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for 0644 config file (must be 0600)")
	}
}

// Daemon.StartedAt and Daemon.Publisher are trivial accessors;
// exercise them on a zero-value Daemon for completeness.
func TestDaemonAccessors_ZeroValues(t *testing.T) {
	d := &Daemon{}
	if !d.StartedAt().IsZero() {
		t.Errorf("StartedAt on zero Daemon = %v, want zero", d.StartedAt())
	}
	if d.Publisher() != nil {
		t.Errorf("Publisher on zero Daemon = %v, want nil", d.Publisher())
	}
}
