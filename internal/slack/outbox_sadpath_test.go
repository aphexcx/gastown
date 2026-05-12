package slack

// Targeted sad-path tests for outbox.go. The existing outbox_test.go
// covers happy paths; these tests exercise error branches and edge
// cases that the codecov report flagged uncovered.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteOutboxMessage_AutoFillsTimestamp(t *testing.T) {
	dir := t.TempDir()
	before := time.Now()
	msg := &OutboxMessage{From: "test", ChatID: "C01", Text: "hi"}
	path, err := WriteOutboxMessage(dir, msg)
	if err != nil {
		t.Fatalf("WriteOutboxMessage: %v", err)
	}
	if msg.Timestamp.IsZero() {
		t.Error("WriteOutboxMessage should auto-fill Timestamp when zero")
	}
	if msg.Timestamp.Before(before) {
		t.Errorf("Timestamp %v should be after %v", msg.Timestamp, before)
	}
	// Filename should contain the unix-nano timestamp.
	if !strings.HasPrefix(filepath.Base(path), "1") {
		// Unix-nano timestamps in this era start with "17..."; just check
		// it's at least an all-digits prefix.
		t.Errorf("path %q should start with a timestamp-like prefix", path)
	}
}

func TestWriteOutboxMessage_PreservesExplicitTimestamp(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	msg := &OutboxMessage{From: "t", ChatID: "C01", Text: "x", Timestamp: fixed}
	if _, err := WriteOutboxMessage(dir, msg); err != nil {
		t.Fatalf("WriteOutboxMessage: %v", err)
	}
	if !msg.Timestamp.Equal(fixed) {
		t.Errorf("Timestamp = %v, want %v (caller's value preserved)", msg.Timestamp, fixed)
	}
}

func TestWriteOutboxMessage_FailsWhenDirMissing(t *testing.T) {
	// Pointing at a nonexistent directory should fail (we don't auto-create
	// the outbox dir from WriteOutboxMessage — callers do).
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := WriteOutboxMessage(dir, &OutboxMessage{From: "t", ChatID: "C01", Text: "x"})
	if err == nil {
		t.Error("expected error when outbox dir doesn't exist")
	}
}

func TestWriteOutboxMessage_WritesValidJSON(t *testing.T) {
	dir := t.TempDir()
	msg := &OutboxMessage{From: "agent/x", ChatID: "C01", Text: "hi", ThreadTS: "1.0"}
	path, err := WriteOutboxMessage(dir, msg)
	if err != nil {
		t.Fatalf("WriteOutboxMessage: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got OutboxMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("written file is not valid JSON: %v\ncontent: %s", err, data)
	}
	if got.From != msg.From || got.ChatID != msg.ChatID || got.Text != msg.Text || got.ThreadTS != msg.ThreadTS {
		t.Errorf("round-trip mismatch: %+v vs %+v", got, msg)
	}
}

func TestWriteOutboxMessage_Mode0600(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteOutboxMessage(dir, &OutboxMessage{From: "t", ChatID: "C", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perms = %o, want 0600 (outbox messages can carry user text)", perm)
	}
}

func TestReadOutboxMessage_MissingFile(t *testing.T) {
	_, err := ReadOutboxMessage(filepath.Join(t.TempDir(), "no-such-file.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadOutboxMessage_MalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "msg.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadOutboxMessage(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("error should mention unmarshal; got %v", err)
	}
}

func TestListPendingOutbox_MissingDirReturnsNilNoError(t *testing.T) {
	got, err := ListPendingOutbox(filepath.Join(t.TempDir(), "no-such-dir"))
	if err != nil {
		t.Fatalf("missing dir should not error; got %v", err)
	}
	if got != nil {
		t.Errorf("missing dir should return nil slice; got %v", got)
	}
}

func TestListPendingOutbox_IgnoresClaimedAndDirs(t *testing.T) {
	dir := t.TempDir()
	// Create a real pending message.
	pendingPath, err := WriteOutboxMessage(dir, &OutboxMessage{From: "t", ChatID: "C01", Text: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	// Create a claimed sibling (would have been renamed via Claim).
	claimedPath := pendingPath + ".claimed.deadbeef"
	if err := os.WriteFile(claimedPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Create a non-.json file (should be ignored).
	if err := os.WriteFile(filepath.Join(dir, "junk.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Create the failed/ subdir (should be ignored even if it's there).
	if err := os.Mkdir(filepath.Join(dir, "failed"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := ListPendingOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != pendingPath {
		t.Errorf("got %v, want [%s]", got, pendingPath)
	}
}

func TestListPendingOutbox_SortedDeterministic(t *testing.T) {
	dir := t.TempDir()
	// Write three messages in non-lexical order via explicit timestamps.
	now := time.Now()
	for i, ts := range []time.Time{now.Add(2 * time.Second), now, now.Add(time.Second)} {
		_, err := WriteOutboxMessage(dir, &OutboxMessage{
			From:      "t",
			ChatID:    "C01",
			Text:      "msg",
			Timestamp: ts,
		})
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	got, err := ListPendingOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	// Lexical sort = chronological (since filenames are <unix-nano>-<rand>.json).
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Errorf("not sorted: %q >= %q", got[i-1], got[i])
		}
	}
}

func TestUnclaimOutboxMessage_RejectsNonClaim(t *testing.T) {
	// Passing a plain .json path (no .claimed.<rand> suffix) should error.
	err := UnclaimOutboxMessage("/tmp/foo.json")
	if err == nil {
		t.Error("expected error for non-claim path")
	}
}

func TestUnclaimOutboxMessage_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteOutboxMessage(dir, &OutboxMessage{From: "t", ChatID: "C", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimOutboxMessage(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("original should be gone after claim; stat = %v", err)
	}
	if err := UnclaimOutboxMessage(claim); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("original should be restored after unclaim; stat = %v", err)
	}
	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Errorf("claim should be gone after unclaim; stat = %v", err)
	}
}

func TestUnclaimOutboxMessage_FailsOnMissingClaim(t *testing.T) {
	err := UnclaimOutboxMessage("/tmp/nonexistent.json.claimed.deadbeef")
	if err == nil {
		t.Error("expected error for nonexistent claim path")
	}
}

func TestDeadLetterOutboxMessage_MovesToFailedAndRecordsReason(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteOutboxMessage(dir, &OutboxMessage{From: "t", ChatID: "C01", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimOutboxMessage(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeadLetterOutboxMessage(claim, "channel_not_found"); err != nil {
		t.Fatalf("DeadLetterOutboxMessage: %v", err)
	}

	// Claim file should be gone.
	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Errorf("claim should be removed after dead-letter; stat = %v", err)
	}

	// Failed copy should exist with the failure_reason set.
	failedDir := filepath.Join(dir, "failed")
	entries, err := os.ReadDir(failedDir)
	if err != nil {
		t.Fatalf("failed/ dir not created: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry in failed/, got %d", len(entries))
	}
	msg, err := ReadOutboxMessage(filepath.Join(failedDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if msg.FailureReason != "channel_not_found" {
		t.Errorf("FailureReason = %q, want channel_not_found", msg.FailureReason)
	}
	// Filename in failed/ should be the original .json (no .claimed.* suffix).
	if !strings.HasSuffix(entries[0].Name(), ".json") || strings.Contains(entries[0].Name(), ".claimed.") {
		t.Errorf("failed file name = %q, want .json suffix without .claimed", entries[0].Name())
	}
}

func TestDeadLetterOutboxMessage_FailsOnMissingClaim(t *testing.T) {
	err := DeadLetterOutboxMessage("/tmp/no-such-claim.json.claimed.x", "reason")
	if err == nil {
		t.Error("expected error for nonexistent claim")
	}
}

func TestSweepStaleClaims_MissingDirIsNoop(t *testing.T) {
	if err := SweepStaleClaims(filepath.Join(t.TempDir(), "no-such-dir"), time.Hour); err != nil {
		t.Errorf("missing dir should be a no-op; got %v", err)
	}
}

func TestSweepStaleClaims_FreshClaimUnchanged(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteOutboxMessage(dir, &OutboxMessage{From: "t", ChatID: "C01", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimOutboxMessage(path)
	if err != nil {
		t.Fatal(err)
	}
	// Sweep with a long threshold — fresh claim must NOT be swept.
	if err := SweepStaleClaims(dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(claim); err != nil {
		t.Errorf("fresh claim should remain; stat = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("original should still be claimed; stat = %v", err)
	}
}

func TestSweepStaleClaims_OldClaimRestored(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteOutboxMessage(dir, &OutboxMessage{From: "t", ChatID: "C01", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimOutboxMessage(path)
	if err != nil {
		t.Fatal(err)
	}
	// Backdate the claim file's mtime past the threshold.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(claim, old, old); err != nil {
		t.Fatal(err)
	}
	// Sweep with a 1-hour threshold — this claim is 2h old, must be swept.
	if err := SweepStaleClaims(dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Errorf("stale claim should be removed after sweep; stat = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("original should be restored after sweep; stat = %v", err)
	}
}

func TestSweepStaleClaims_IgnoresPendingFiles(t *testing.T) {
	dir := t.TempDir()
	// A pending (non-claimed) message — sweep should ignore it.
	path, err := WriteOutboxMessage(dir, &OutboxMessage{From: "t", ChatID: "C01", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := SweepStaleClaims(dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("non-claim should be untouched by sweep; stat = %v", err)
	}
}
