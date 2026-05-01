// Tests for the config-layer slack-channels auto-injection. Exercises the
// production path: maybeInjectClaudeChannels mutates a RuntimeConfig.Args
// based on the contents of <home>/gt/config/slack.json.
//
// Tests override slackChannelsEnabledFromHome via a stub so we don't depend
// on the real ~/gt/config/slack.json.

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// withChannelsEnabled swaps slackChannelsEnabledFromHome for a stub that
// returns the given value, restoring the original on test cleanup.
func withChannelsEnabled(t *testing.T, enabled bool) {
	t.Helper()
	prev := slackChannelsEnabledFromHome
	slackChannelsEnabledFromHome = func() bool { return enabled }
	t.Cleanup(func() { slackChannelsEnabledFromHome = prev })
}

func TestMaybeInjectClaudeChannels_ClaudeAndEnabled(t *testing.T) {
	withChannelsEnabled(t, true)
	rc := &RuntimeConfig{
		Command: "claude",
		Args:    []string{"--model", "sonnet[1m]"},
	}
	maybeInjectClaudeChannels(rc)
	want := []string{"--model", "sonnet[1m]", "--channels", "plugin:gt-slack@gastown"}
	if len(rc.Args) != len(want) {
		t.Fatalf("Args = %v, want %v", rc.Args, want)
	}
	for i := range want {
		if rc.Args[i] != want[i] {
			t.Fatalf("Args[%d] = %q, want %q", i, rc.Args[i], want[i])
		}
	}
}

func TestMaybeInjectClaudeChannels_ChannelsDisabled(t *testing.T) {
	withChannelsEnabled(t, false)
	rc := &RuntimeConfig{
		Command: "claude",
		Args:    []string{"--model", "sonnet[1m]"},
	}
	maybeInjectClaudeChannels(rc)
	if len(rc.Args) != 2 {
		t.Fatalf("Args = %v, want unchanged", rc.Args)
	}
}

func TestMaybeInjectClaudeChannels_NonClaudeAgent(t *testing.T) {
	withChannelsEnabled(t, true)
	rc := &RuntimeConfig{
		Command: "codex",
		Args:    []string{"--config", "x"},
	}
	maybeInjectClaudeChannels(rc)
	if len(rc.Args) != 2 || rc.Args[0] != "--config" || rc.Args[1] != "x" {
		t.Fatalf("Args = %v, want unchanged for non-Claude command", rc.Args)
	}
}

func TestMaybeInjectClaudeChannels_NilSafe(t *testing.T) {
	withChannelsEnabled(t, true)
	maybeInjectClaudeChannels(nil) // must not panic
}

func TestMaybeInjectClaudeChannels_Idempotent(t *testing.T) {
	withChannelsEnabled(t, true)
	rc := &RuntimeConfig{
		Command: "claude",
		Args:    []string{"--channels", "plugin:gt-slack@gastown"},
	}
	maybeInjectClaudeChannels(rc)
	if len(rc.Args) != 2 {
		t.Fatalf("Args = %v, want unchanged (idempotent)", rc.Args)
	}
}

func TestSlackChannelsEnabledFromPath_ReadsTrueValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slack.json")
	if err := os.WriteFile(path, []byte(`{"channels_enabled":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !slackChannelsEnabledFromPath(path) {
		t.Fatal("expected true; got false")
	}
}

func TestSlackChannelsEnabledFromPath_ReadsFalseValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slack.json")
	if err := os.WriteFile(path, []byte(`{"channels_enabled":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if slackChannelsEnabledFromPath(path) {
		t.Fatal("expected false; got true")
	}
}

func TestSlackChannelsEnabledFromPath_MissingFile(t *testing.T) {
	if slackChannelsEnabledFromPath("/nonexistent/slack.json") {
		t.Fatal("expected false for missing file; got true")
	}
}

func TestSlackChannelsEnabledFromPath_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slack.json")
	if err := os.WriteFile(path, []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if slackChannelsEnabledFromPath(path) {
		t.Fatal("expected false for malformed JSON; got true")
	}
}

func TestSlackChannelsEnabledFromPath_MissingField(t *testing.T) {
	// Slack config without channels_enabled should be treated as disabled
	// (zero-value bool).
	dir := t.TempDir()
	path := filepath.Join(dir, "slack.json")
	if err := os.WriteFile(path, []byte(`{"bot_token":"xoxb-test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if slackChannelsEnabledFromPath(path) {
		t.Fatal("expected false when channels_enabled field absent; got true")
	}
}
