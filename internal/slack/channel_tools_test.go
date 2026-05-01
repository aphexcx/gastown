package slack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

func TestHandleReply_HappyPath(t *testing.T) {
	town := t.TempDir()
	dir := filepath.Join(town, constants.DirRuntime, "slack_outbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	args := ReplyArgs{
		ChatID:   "D1",
		Text:     "hello",
		ThreadTS: "1714510123.000200",
	}
	out, err := HandleReply(context.Background(), town, "gastown/crew/cog", args)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !out.OK {
		t.Fatalf("OK=false; result=%+v", out)
	}
	if out.Path == "" {
		t.Fatalf("Path is empty; result=%+v", out)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	jsons := 0
	var written string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			jsons++
			written = filepath.Join(dir, e.Name())
		}
	}
	if jsons != 1 {
		t.Fatalf("got %d *.json files in outbox, want 1", jsons)
	}

	data, err := os.ReadFile(written)
	if err != nil {
		t.Fatal(err)
	}
	var parsed OutboxMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.From != "gastown/crew/cog" {
		t.Errorf("From = %q, want %q", parsed.From, "gastown/crew/cog")
	}
	if parsed.ChatID != "D1" {
		t.Errorf("ChatID = %q, want %q", parsed.ChatID, "D1")
	}
	if parsed.Text != "hello" {
		t.Errorf("Text = %q, want %q", parsed.Text, "hello")
	}
	if parsed.ThreadTS != "1714510123.000200" {
		t.Errorf("ThreadTS = %q, want %q", parsed.ThreadTS, "1714510123.000200")
	}
	if parsed.Timestamp.IsZero() {
		t.Error("Timestamp is zero, want non-zero")
	}
}

func TestHandleReply_ValidatesEmptyChatID(t *testing.T) {
	town := t.TempDir()
	out, err := HandleReply(context.Background(), town, "gastown/crew/cog", ReplyArgs{
		ChatID: "",
		Text:   "hello",
	})
	if err != nil {
		t.Fatalf("HandleReply returned a Go error %v; want a validation result instead", err)
	}
	if out.OK {
		t.Fatal("OK=true with empty chat_id; want OK=false")
	}
	if !strings.Contains(strings.ToLower(out.Error), "chat_id") {
		t.Errorf("Error should mention chat_id; got %q", out.Error)
	}
}

func TestHandleReply_ValidatesEmptyText(t *testing.T) {
	town := t.TempDir()
	out, err := HandleReply(context.Background(), town, "gastown/crew/cog", ReplyArgs{
		ChatID: "D1",
		Text:   "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.OK {
		t.Fatal("OK=true with empty text; want OK=false")
	}
	if !strings.Contains(strings.ToLower(out.Error), "text") {
		t.Errorf("Error should mention text; got %q", out.Error)
	}
}

func TestHandleReply_ValidatesEmptySender(t *testing.T) {
	town := t.TempDir()
	out, err := HandleReply(context.Background(), town, "", ReplyArgs{
		ChatID: "D1",
		Text:   "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.OK {
		t.Fatal("OK=true with empty sender; want OK=false")
	}
	if !strings.Contains(strings.ToLower(out.Error), "sender") {
		t.Errorf("Error should mention sender; got %q", out.Error)
	}
}

func TestHandleReply_TrimsWhitespace(t *testing.T) {
	town := t.TempDir()
	dir := filepath.Join(town, constants.DirRuntime, "slack_outbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := HandleReply(context.Background(), town, "gastown/crew/cog", ReplyArgs{
		ChatID:   "  D1  ",
		Text:     "  hello  ",
		ThreadTS: "  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("OK=false on whitespace input; result=%+v", out)
	}

	// Verify the saved JSON has trimmed values.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			var parsed OutboxMessage
			_ = json.Unmarshal(data, &parsed)
			if parsed.ChatID != "D1" {
				t.Errorf("ChatID not trimmed: %q", parsed.ChatID)
			}
			if parsed.Text != "hello" {
				t.Errorf("Text not trimmed: %q", parsed.Text)
			}
			if parsed.ThreadTS != "" {
				t.Errorf("ThreadTS not trimmed (whitespace-only should be empty): %q", parsed.ThreadTS)
			}
		}
	}
}
