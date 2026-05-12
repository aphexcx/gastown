package slack

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"
	"github.com/steveyegge/gastown/internal/session"
)

func TestExtractAttachmentsFromSlackFiles_Empty(t *testing.T) {
	if got := extractAttachmentsFromSlackFiles(nil); got != nil {
		t.Errorf("nil input → got %v, want nil", got)
	}
	if got := extractAttachmentsFromSlackFiles([]slackgo.File{}); got != nil {
		t.Errorf("empty input → got %v, want nil", got)
	}
}

func TestExtractAttachmentsFromSlackFiles_MapsFields(t *testing.T) {
	files := []slackgo.File{
		{
			ID:                 "F01",
			Name:               "screenshot.png",
			Mimetype:           "image/png",
			Size:               12345,
			URLPrivateDownload: "https://files.slack.com/F01",
		},
		{
			ID:                 "F02",
			Name:               "log.txt",
			Mimetype:           "text/plain",
			Size:               42,
			URLPrivateDownload: "https://files.slack.com/F02",
		},
	}
	got := extractAttachmentsFromSlackFiles(files)
	if len(got) != 2 {
		t.Fatalf("got %d attachments, want 2", len(got))
	}
	if got[0].ID != "F01" || got[0].Name != "screenshot.png" || got[0].MimeType != "image/png" || got[0].Size != 12345 || got[0].URLPrivate != "https://files.slack.com/F01" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].ID != "F02" {
		t.Errorf("got[1].ID = %q, want F02", got[1].ID)
	}
}

func TestClassifyChannel(t *testing.T) {
	tests := []struct {
		chatID string
		want   ConversationKind
	}{
		{"D01EXAMPLE01", ConversationDM},
		{"C01EXAMPLE01", ConversationChannel},
		{"G01EXAMPLE01", ConversationChannel}, // private group: also channel-like
		{"", ConversationChannel},
	}
	for _, tt := range tests {
		if got := classifyChannel(tt.chatID); got != tt.want {
			t.Errorf("classifyChannel(%q) = %v, want %v", tt.chatID, got, tt.want)
		}
	}
}

func TestDisplayNameFor(t *testing.T) {
	tests := []struct {
		name string
		id   session.AgentIdentity
		want string
	}{
		{"mayor", session.AgentIdentity{Role: session.RoleMayor}, "mayor"},
		{"deacon", session.AgentIdentity{Role: session.RoleDeacon}, "deacon"},
		{"witness with rig", session.AgentIdentity{Role: session.RoleWitness, Rig: "acme"}, "acme-witness"},
		{"witness without rig", session.AgentIdentity{Role: session.RoleWitness}, ""},
		{"refinery with rig", session.AgentIdentity{Role: session.RoleRefinery, Rig: "acme"}, "acme-refinery"},
		{"refinery without rig", session.AgentIdentity{Role: session.RoleRefinery}, ""},
		{"crew", session.AgentIdentity{Role: session.RoleCrew, Name: "cody"}, "cody"},
		{"polecat", session.AgentIdentity{Role: session.RolePolecat, Name: "gus"}, "gus"},
		{"dog", session.AgentIdentity{Role: session.RoleDog, Name: "checkpoint"}, "checkpoint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := tt.id
			if got := displayNameFor(&id); got != tt.want {
				t.Errorf("displayNameFor(%+v) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestStatusPath(t *testing.T) {
	got := StatusPath("/town")
	want := "/town/.runtime/slack-status.json"
	if got != want {
		t.Errorf("StatusPath = %q, want %q", got, want)
	}
}

func TestLoadStatusSnapshot_Missing(t *testing.T) {
	_, err := LoadStatusSnapshot("/nonexistent/slack-status.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error should wrap os.ErrNotExist; got %v", err)
	}
}

func TestLoadStatusSnapshot_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slack-status.json")
	want := &StatusSnapshot{
		PID:        12345,
		StartedAt:  time.Now().UTC().Truncate(time.Second),
		LastPostAt: time.Now().UTC().Truncate(time.Second),
		Pending:    3,
		Failed:     1,
		Routing:    map[string]string{"mayor": "mayor/"},
		Channels:   map[string]bool{"C01": true},
		Connection: "connected",
	}
	data, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadStatusSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != want.PID || got.Pending != want.Pending || got.Connection != want.Connection {
		t.Errorf("got = %+v, want %+v", got, want)
	}
	if got.Routing["mayor"] != "mayor/" {
		t.Errorf("routing = %v, want mayor: mayor/", got.Routing)
	}
}

func TestLoadStatusSnapshot_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slack-status.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadStatusSnapshot(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}
