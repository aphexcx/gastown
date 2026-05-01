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

// slackChannelsPluginRef mirrors session.SlackChannelsPluginRef. Duplicated
// here to keep this file dependency-light (config does not import session).
const slackChannelsPluginRef = "plugin:gt-slack@gastown"

// slackChannelsConfigShape is the minimal subset of slack.json that this
// package cares about. The full Config struct lives in internal/slack.
// We deliberately do NOT pull in that package to avoid an import cycle.
type slackChannelsConfigShape struct {
	ChannelsEnabled bool `json:"channels_enabled"`
	// ChannelsDevMode, when true, makes the auto-inject use Claude Code's
	// --dangerously-load-development-channels flag instead of --channels.
	// Required on Pro/Max accounts: per Spike 1, the curated allowlist gate
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
// followed by plugin:gt-slack@gastown when:
//   - slack channels are enabled (channels_enabled: true in slack.json), AND
//   - rc.Command is "claude" (Claude Code only — non-Claude agents are
//     never affected).
//
// Idempotent: if the chosen flag is already paired with our plugin ref,
// it is not appended again. Safe to call when rc is nil (no-op).
func maybeInjectClaudeChannels(rc *RuntimeConfig) {
	if rc == nil || rc.Command != "claude" {
		return
	}
	enabled, devMode := slackChannelsLookup()
	if !enabled {
		return
	}
	flag := channelsFlagFor(devMode)
	// Idempotency: skip if our flag with the gt-slack ref is already present.
	for i, a := range rc.Args {
		if a == flag && i+1 < len(rc.Args) && rc.Args[i+1] == slackChannelsPluginRef {
			return
		}
	}
	rc.Args = append(rc.Args, flag, slackChannelsPluginRef)
}
