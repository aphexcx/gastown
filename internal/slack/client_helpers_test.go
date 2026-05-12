package slack

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestFileBaseName(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"unix absolute", "/tmp/foo/bar.png", "bar.png"},
		{"windows absolute", `C:\Users\alice\bar.png`, "bar.png"},
		{"no separator", "bar.png", "bar.png"},
		{"trailing separator unix", "/tmp/foo/", ""},
		{"trailing separator windows", `C:\Users\alice\`, ""},
		{"empty", "", ""},
		{"just separator", "/", ""},
		{"unicode path", "/tmp/föö/báz.png", "báz.png"},
		{"mixed separators (windows-style)", `foo/bar\baz.png`, "baz.png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fileBaseName(tt.in)
			if got != tt.want {
				t.Errorf("fileBaseName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestClassifySlackError(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if got := classifySlackError(nil); got != nil {
			t.Errorf("classifySlackError(nil) = %v, want nil", got)
		}
	})

	permanentCodes := []string{
		"channel_not_found",
		"not_in_channel",
		"is_archived",
		"invalid_auth",
		"not_authed",
		"token_revoked",
		"account_inactive",
		"missing_scope",
		"file_not_found",
		"invalid_file",
	}
	for _, code := range permanentCodes {
		t.Run("permanent: "+code, func(t *testing.T) {
			err := classifySlackError(fmt.Errorf("%s", code))
			if err == nil {
				t.Fatal("expected non-nil error")
			}
			if !errors.Is(err, ErrPermanent) {
				t.Errorf("err should wrap ErrPermanent; got %v", err)
			}
			if errors.Is(err, ErrTransient) {
				t.Errorf("permanent error should NOT also wrap ErrTransient")
			}
			// Underlying message should preserve the code for debugging.
			if want := code; !contains(err.Error(), want) {
				t.Errorf("error %q should contain code %q", err.Error(), want)
			}
		})
	}

	transientCodes := []string{
		"ratelimited",
		"timeout",
		"server_error",
		"some_unknown_code",
		"",
	}
	for _, code := range transientCodes {
		t.Run("transient: "+code, func(t *testing.T) {
			err := classifySlackError(fmt.Errorf("%s", code))
			if err == nil {
				t.Fatal("expected non-nil error")
			}
			if !errors.Is(err, ErrTransient) {
				t.Errorf("err should wrap ErrTransient; got %v", err)
			}
			if errors.Is(err, ErrPermanent) {
				t.Errorf("transient error should NOT also wrap ErrPermanent")
			}
		})
	}
}

// contains is a tiny helper to keep the test self-contained without an import.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// userDisplayCache concurrency — multiple goroutines hammer put/get and we
// verify the cache never crashes or returns inconsistent state.
func TestUserDisplayCache_ConcurrentSafe(t *testing.T) {
	c := newUserDisplayCache(time.Minute)

	const workers = 16
	const ops = 200

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				uid := fmt.Sprintf("U%02d", (seed+i)%8)
				if i%2 == 0 {
					c.put(uid, fmt.Sprintf("name-%s-%d", uid, i))
				} else {
					_, _ = c.get(uid)
				}
			}
		}(w)
	}
	wg.Wait()
	// If we got here without a race or panic, the cache survived. As a smoke
	// check, do one more put+get to confirm it's still functional.
	c.put("U_END", "alice")
	got, ok := c.get("U_END")
	if !ok || got != "alice" {
		t.Errorf("final get: got (%q, %v), want (\"alice\", true)", got, ok)
	}
}

// userDisplayCache TTL: an entry put with a short TTL should be reported
// missing once the deadline passes.
func TestUserDisplayCache_TTLExpiresAndIsEvictedOnGet(t *testing.T) {
	c := newUserDisplayCache(50 * time.Millisecond)
	c.put("U1", "alice")
	if _, ok := c.get("U1"); !ok {
		t.Fatal("freshly-put entry should be present")
	}
	time.Sleep(75 * time.Millisecond)
	if _, ok := c.get("U1"); ok {
		t.Error("entry should be expired after TTL")
	}
	// Subsequent put should re-populate cleanly (eviction-on-get behavior
	// must not corrupt the map).
	c.put("U1", "alice-2")
	if got, ok := c.get("U1"); !ok || got != "alice-2" {
		t.Errorf("after re-put: got (%q, %v), want (\"alice-2\", true)", got, ok)
	}
}

// userDisplayCache nil-cache behavior: the production code path constructs
// a Client via NewClient which always initializes the cache, but for safety
// the get/put methods themselves should not be called on a nil receiver.
// (Document the contract via a test.)
func TestUserDisplayCache_NilReceiverIsNotSupported(t *testing.T) {
	// We don't test that nil works — we document via this test that the
	// production usage path always has a non-nil cache. NewClient
	// initializes it; LookupUserDisplayName guards with `if c.userDisplayCache != nil`
	// at the Client method layer, not the cache itself.
	c := newUserDisplayCache(time.Minute)
	if c == nil {
		t.Fatal("newUserDisplayCache returned nil")
	}
	if c.cache == nil {
		t.Fatal("inner cache map is nil")
	}
}
