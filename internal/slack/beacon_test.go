package slack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSubscribed_NoBeaconFile(t *testing.T) {
	town := t.TempDir()
	// Inbox dir exists but no .subscribed file.
	if err := os.MkdirAll(InboxDir(town, "test/sess"), 0o755); err != nil {
		t.Fatal(err)
	}
	if IsSubscribed(town, "test/sess") {
		t.Fatal("IsSubscribed=true with no beacon file, want false")
	}
}

func TestIsSubscribed_BeaconExistsButNoLock(t *testing.T) {
	town := t.TempDir()
	dir := InboxDir(town, "test/sess")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create the beacon file, no holder.
	if err := os.WriteFile(filepath.Join(dir, ".subscribed"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if IsSubscribed(town, "test/sess") {
		t.Fatal("IsSubscribed=true with unlocked beacon, want false")
	}
}

func TestAcquireSubscribed_HolderBlocksProbe(t *testing.T) {
	town := t.TempDir()
	holder, err := AcquireSubscribed(town, "test/sess")
	if err != nil {
		t.Fatalf("AcquireSubscribed: %v", err)
	}
	defer holder.Release()

	if !IsSubscribed(town, "test/sess") {
		t.Fatal("IsSubscribed=false while another process holds the lock, want true")
	}
}

func TestAcquireSubscribed_SecondHolderBlocked(t *testing.T) {
	town := t.TempDir()
	first, err := AcquireSubscribed(town, "test/sess")
	if err != nil {
		t.Fatalf("first AcquireSubscribed: %v", err)
	}
	defer first.Release()

	// Second non-blocking attempt should fail.
	if _, err := AcquireSubscribed(town, "test/sess"); err == nil {
		t.Fatal("second AcquireSubscribed succeeded; want error (already locked)")
	}
}

func TestAcquireSubscribed_ReleaseAllowsReacquire(t *testing.T) {
	town := t.TempDir()
	first, err := AcquireSubscribed(town, "test/sess")
	if err != nil {
		t.Fatal(err)
	}
	first.Release()

	// After release, a new holder should succeed.
	second, err := AcquireSubscribed(town, "test/sess")
	if err != nil {
		t.Fatalf("re-acquire after release failed: %v", err)
	}
	defer second.Release()
}

func TestSubscriptionHolder_ReleaseIdempotent(t *testing.T) {
	town := t.TempDir()
	holder, err := AcquireSubscribed(town, "test/sess")
	if err != nil {
		t.Fatal(err)
	}
	holder.Release()
	holder.Release() // should not panic / fail
}

func TestSubscriptionHolder_ReleaseNilSafe(t *testing.T) {
	var holder *SubscriptionHolder
	holder.Release() // should not panic
}
