// Package config — slack_channels.go
//
// Helpers to detect whether slack channel auto-delivery is enabled for the
// current user, and to mutate a RuntimeConfig's argv to add the
// --channels plugin:gt-slack@gastown flag for Claude agents accordingly.
//
// Lives in internal/config (instead of internal/slack) to avoid an import
// cycle: internal/slack imports internal/session, and internal/session
// imports internal/config. If internal/config imported internal/slack we
// would have a cycle. So this file does a small inline JSON read of
// slack.json instead of pulling in the slack package.
//
// Tests: see slack_channels_test.go.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// slackChannelsPluginRef is the plugin reference passed to Claude Code's
// --channels flag for in-repo gt-slack delivery. The "@gastown" suffix
// matches the marketplace name in plugins/.claude-plugin/marketplace.json.
const slackChannelsPluginRef = "plugin:gt-slack@gastown"

// slackChannelsConfigShape is the minimal subset of slack.json that this
// package cares about. The full Config struct lives in internal/slack.
// We deliberately do NOT pull in that package to avoid an import cycle.
type slackChannelsConfigShape struct {
	ChannelsEnabled bool `json:"channels_enabled"`
	// ChannelsDevMode, when true, makes the auto-inject use Claude Code's
	// --dangerously-load-development-channels flag instead of --channels.
	// Required on Pro/Max accounts: Claude Code's curated allowlist gate
	// only honors managed-settings overrides on team/enterprise tier, so
	// individual-tier users running an in-repo plugin (gt-slack@gastown)
	// must use the dev flag. Default false (production deployment via
	// allowlisted --channels).
	ChannelsDevMode bool `json:"channels_dev_mode,omitempty"`
}

// slackChannelsLookup returns (enabled, devMode) for the current user.
// Tests override this for hermetic coverage.
var slackChannelsLookup = func() (enabled, devMode bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, false
	}
	return slackChannelsLookupFromPath(filepath.Join(home, "gt", "config", "slack.json"))
}

// slackChannelsEnabledFromHome is kept as a thin shim for the existing
// test seam — see slack_channels_test.go which overrides it.
var slackChannelsEnabledFromHome = func() bool {
	enabled, _ := slackChannelsLookup()
	return enabled
}

// slackChannelsLookupFromPath reads slack.json at the given path and
// returns (channels_enabled, channels_dev_mode). Any error returns
// (false, false).
func slackChannelsLookupFromPath(path string) (enabled, devMode bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	var cfg slackChannelsConfigShape
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, false
	}
	return cfg.ChannelsEnabled, cfg.ChannelsDevMode
}

// slackChannelsEnabledFromPath is kept for backwards-compatible tests.
func slackChannelsEnabledFromPath(path string) bool {
	enabled, _ := slackChannelsLookupFromPath(path)
	return enabled
}

// channelsFlagFor returns the launch-flag name corresponding to the dev
// mode setting: --channels for production (allowlisted plugins on
// team/enterprise tier) or --dangerously-load-development-channels for
// local dev / individual-tier accounts.
func channelsFlagFor(devMode bool) string {
	if devMode {
		return "--dangerously-load-development-channels"
	}
	return "--channels"
}

// maybeInjectClaudeChannels mutates rc.Args in-place by appending the
// channels launch flag (--channels or --dangerously-load-development-channels)
// in the `--flag=value` form when:
//   - slack channels are enabled (channels_enabled: true in slack.json), AND
//   - the runtime is Claude (Provider equals AgentClaude).
//
// We check Provider rather than Command because Claude's command can be
// rewritten from the literal "claude" to a resolved path like
// ~/.claude/local/claude (see resolveClaudePath in agents.go). Provider
// is set from the preset name and doesn't get rewritten.
//
// **Why the `--flag=value` form (not space-separated):** Claude Code parses
// `--channels` and `--dangerously-load-development-channels` as variadic
// flags that consume positional tokens until the next `-`-prefixed argument.
// Argv from BuildCommandWithPrompt ends with the startup-prompt positional
// arg, so a space-separated form like `--channels plugin:gt-slack@gastown
// "<prompt>"` makes the parser eat the prompt as a second channel value and
// claude exits with `--dangerously-load-development-channels entries must
// be tagged: plugin:<name>@<marketplace>`. The `--flag=value` form binds
// the value to the flag as a single argv token, leaving the prompt arg
// unambiguous. (Verified empirically against the shipped claude binary.)
//
// Idempotent: if the chosen flag-with-value token is already present, it
// is not appended again. Safe to call when rc is nil (no-op).
func maybeInjectClaudeChannels(rc *RuntimeConfig) {
	if rc == nil || rc.Provider != string(AgentClaude) {
		return
	}
	enabled, devMode := slackChannelsLookup()
	if !enabled {
		return
	}
	token := channelsFlagFor(devMode) + "=" + slackChannelsPluginRef
	for _, a := range rc.Args {
		if a == token {
			return
		}
	}
	rc.Args = append(rc.Args, token)
}
