package slack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriteOutboxMessage_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	msg := OutboxMessage{
		From:      "houmanoids_www/crew/cody",
		ChatID:    "C123",
		Text:      "Fixed!",
		Timestamp: time.Now(),
	}
	path, err := WriteOutboxMessage(dir, &msg)
	require.NoError(t, err)
	require.FileExists(t, path)
	require.Contains(t, path, ".json")
	require.NotContains(t, path, ".tmp")

	loaded, err := ReadOutboxMessage(path)
	require.NoError(t, err)
	require.Equal(t, msg.From, loaded.From)
	require.Equal(t, msg.ChatID, loaded.ChatID)
	require.Equal(t, msg.Text, loaded.Text)
}

func TestListPendingOutbox(t *testing.T) {
	dir := t.TempDir()

	_, err := WriteOutboxMessage(dir, &OutboxMessage{From: "a", ChatID: "C1", Text: "hi"})
	require.NoError(t, err)
	_, err = WriteOutboxMessage(dir, &OutboxMessage{From: "b", ChatID: "C2", Text: "hi"})
	require.NoError(t, err)

	// Leave a .tmp file behind — should be ignored.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bogus.json.tmp"), []byte("partial"), 0o600))
	// Leave a .claimed file behind — should be ignored by ListPending.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "claimed.json.claimed.xyz"), []byte("{}"), 0o600))

	pending, err := ListPendingOutbox(dir)
	require.NoError(t, err)
	require.Len(t, pending, 2)
}

func TestClaimAndUnclaim(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteOutboxMessage(dir, &OutboxMessage{From: "a", ChatID: "C1", Text: "hi"})
	require.NoError(t, err)

	claim, err := ClaimOutboxMessage(path)
	require.NoError(t, err)
	require.Contains(t, claim, ".claimed.")
	require.NoFileExists(t, path)
	require.FileExists(t, claim)

	// A second claim of the original path fails (already renamed).
	_, err = ClaimOutboxMessage(path)
	require.Error(t, err)

	// Unclaim should restore the original .json file.
	require.NoError(t, UnclaimOutboxMessage(claim))
	require.FileExists(t, path)
	require.NoFileExists(t, claim)
}

func TestDeadLetter(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteOutboxMessage(dir, &OutboxMessage{From: "a", ChatID: "C1", Text: "hi"})
	require.NoError(t, err)

	claim, err := ClaimOutboxMessage(path)
	require.NoError(t, err)

	require.NoError(t, DeadLetterOutboxMessage(claim, "channel_not_found"))
	require.NoFileExists(t, claim)

	// A file with .json (and exactly one .json suffix — not .json.json) should
	// now exist under dir/failed/. This assertion catches the common bug of
	// stripping .claimed.* and then re-appending .json.
	entries, err := os.ReadDir(filepath.Join(dir, "failed"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.True(t, strings.HasSuffix(entries[0].Name(), ".json"))
	require.False(t, strings.HasSuffix(entries[0].Name(), ".json.json"),
		"dead-letter filename must not double-append .json")

	// And the failure reason is embedded in the JSON.
	data, err := os.ReadFile(filepath.Join(dir, "failed", entries[0].Name()))
	require.NoError(t, err)
	require.Contains(t, string(data), "channel_not_found")
}

func TestSweepStaleClaims(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteOutboxMessage(dir, &OutboxMessage{From: "a", ChatID: "C1", Text: "hi"})
	require.NoError(t, err)

	claim, err := ClaimOutboxMessage(path)
	require.NoError(t, err)

	// Make the claim look stale.
	old := time.Now().Add(-10 * time.Minute)
	require.NoError(t, os.Chtimes(claim, old, old))

	require.NoError(t, SweepStaleClaims(dir, 5*time.Minute))

	// Original .json should be back.
	pending, err := ListPendingOutbox(dir)
	require.NoError(t, err)
	require.Len(t, pending, 1)
}
