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

// withChannelsEnabled swaps slackChannelsLookup for a stub that returns
// (enabled, devMode=false), restoring the original on test cleanup.
func withChannelsEnabled(t *testing.T, enabled bool) {
	t.Helper()
	withChannelsLookup(t, enabled, false)
}

// withChannelsLookup swaps slackChannelsLookup for a stub returning the
// given (enabled, devMode), restoring the original on test cleanup.
func withChannelsLookup(t *testing.T, enabled, devMode bool) {
	t.Helper()
	prevLookup := slackChannelsLookup
	prevHome := slackChannelsEnabledFromHome
	slackChannelsLookup = func() (bool, bool) { return enabled, devMode }
	slackChannelsEnabledFromHome = func() bool { return enabled }
	t.Cleanup(func() {
		slackChannelsLookup = prevLookup
		slackChannelsEnabledFromHome = prevHome
	})
}

func TestMaybeInjectClaudeChannels_DevModeUsesDangerouslyLoadFlag(t *testing.T) {
	withChannelsLookup(t, true, true)
	rc := &RuntimeConfig{
		Provider: string(AgentClaude),
		Command:  "claude",
		Args:    []string{"--model", "sonnet[1m]"},
	}
	maybeInjectClaudeChannels(rc)
	want := []string{"--model", "sonnet[1m]", "--dangerously-load-development-channels=plugin:gt-slack@gastown"}
	if len(rc.Args) != len(want) {
		t.Fatalf("Args = %v, want %v", rc.Args, want)
	}
	for i := range want {
		if rc.Args[i] != want[i] {
			t.Fatalf("Args[%d] = %q, want %q", i, rc.Args[i], want[i])
		}
	}
}

func TestMaybeInjectClaudeChannels_DevModeIdempotent(t *testing.T) {
	withChannelsLookup(t, true, true)
	rc := &RuntimeConfig{
		Provider: string(AgentClaude),
		Command:  "claude",
		Args:    []string{"--dangerously-load-development-channels=plugin:gt-slack@gastown"},
	}
	maybeInjectClaudeChannels(rc)
	if len(rc.Args) != 1 {
		t.Fatalf("Args = %v, want unchanged (idempotent)", rc.Args)
	}
}

func TestSlackChannelsLookupFromPath_ReadsDevMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slack.json")
	if err := os.WriteFile(path, []byte(`{"channels_enabled":true,"channels_dev_mode":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	enabled, devMode := slackChannelsLookupFromPath(path)
	if !enabled {
		t.Error("expected enabled=true")
	}
	if !devMode {
		t.Error("expected devMode=true")
	}
}

func TestMaybeInjectClaudeChannels_ClaudeAndEnabled(t *testing.T) {
	withChannelsEnabled(t, true)
	rc := &RuntimeConfig{
		Provider: string(AgentClaude),
		Command:  "claude",
		Args:    []string{"--model", "sonnet[1m]"},
	}
	maybeInjectClaudeChannels(rc)
	want := []string{"--model", "sonnet[1m]", "--channels=plugin:gt-slack@gastown"}
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
		Provider: string(AgentClaude),
		Command:  "claude",
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
		Provider: string(AgentCodex),
		Command:  "codex",
		Args:     []string{"--config", "x"},
	}
	maybeInjectClaudeChannels(rc)
	if len(rc.Args) != 2 || rc.Args[0] != "--config" || rc.Args[1] != "x" {
		t.Fatalf("Args = %v, want unchanged for non-Claude command", rc.Args)
	}
}

// Regression: Provider check (not Command check). Claude's Command is
// rewritten from "claude" to a resolved path like ~/.claude/local/claude
// in agents.go's resolveClaudePath flow. Make sure the injection still
// fires when Command is a path but Provider is AgentClaude.
func TestMaybeInjectClaudeChannels_ResolvedClaudeBinaryPath(t *testing.T) {
	withChannelsEnabled(t, true)
	rc := &RuntimeConfig{
		Provider: string(AgentClaude),
		Command:  "/Users/dev/.claude/local/claude", // path, not literal "claude"
		Args:     []string{"--model", "sonnet[1m]"},
	}
	maybeInjectClaudeChannels(rc)
	want := []string{"--model", "sonnet[1m]", "--channels=plugin:gt-slack@gastown"}
	if len(rc.Args) != len(want) {
		t.Fatalf("Args = %v, want %v (Provider check should fire even when Command is a resolved path)", rc.Args, want)
	}
	for i := range want {
		if rc.Args[i] != want[i] {
			t.Fatalf("Args[%d] = %q, want %q", i, rc.Args[i], want[i])
		}
	}
}

func TestMaybeInjectClaudeChannels_NilSafe(t *testing.T) {
	withChannelsEnabled(t, true)
	maybeInjectClaudeChannels(nil) // must not panic
}

func TestMaybeInjectClaudeChannels_Idempotent(t *testing.T) {
	withChannelsEnabled(t, true)
	rc := &RuntimeConfig{
		Provider: string(AgentClaude),
		Command:  "claude",
		Args:    []string{"--channels=plugin:gt-slack@gastown"},
	}
	maybeInjectClaudeChannels(rc)
	if len(rc.Args) != 1 {
		t.Fatalf("Args = %v, want unchanged (idempotent)", rc.Args)
	}
}

// Regression: prompt arg must not be eaten by the variadic --channels flag.
// Claude's parser treats `--channels` and
// `--dangerously-load-development-channels` as variadic; a space-separated
// `--channels plugin:gt-slack@gastown <prompt>` makes the prompt land in
// the channels list and claude exits with "entries must be tagged: ...".
// The `--flag=value` form binds the value to the flag as one argv token.
func TestMaybeInjectClaudeChannels_DoesNotConsumeFollowingPositional(t *testing.T) {
	withChannelsLookup(t, true, true)
	rc := &RuntimeConfig{
		Provider: string(AgentClaude),
		Command:  "claude",
		Args:     []string{"--dangerously-skip-permissions"},
	}
	maybeInjectClaudeChannels(rc)
	// Caller appends a prompt as a positional; the channels flag must be
	// `--flag=value`-form so the prompt isn't slurped as a second value.
	rc.Args = append(rc.Args, "[GAS TOWN] mayor <- human")
	for _, a := range rc.Args {
		if a == "plugin:gt-slack@gastown" {
			t.Fatalf("plugin ref appears as a standalone token in %v — claude will eat the prompt", rc.Args)
		}
	}
	found := false
	for _, a := range rc.Args {
		if a == "--dangerously-load-development-channels=plugin:gt-slack@gastown" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected --flag=value token in %v", rc.Args)
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
