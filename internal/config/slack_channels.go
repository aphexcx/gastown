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
}

// slackChannelsEnabledFromHome returns true when ~/gt/config/slack.json
// exists, is readable, parses successfully, and has channels_enabled: true.
// Any error (missing file, parse failure, IO error) is treated as
// "channels disabled" — the spawn must never fail because slack config
// is absent or malformed.
//
// The lookup uses os.UserHomeDir() to mirror slack.DefaultConfigPath().
// Tests override the lookup via slackChannelsEnabledLookup.
var slackChannelsEnabledFromHome = func() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	path := filepath.Join(home, "gt", "config", "slack.json")
	return slackChannelsEnabledFromPath(path)
}

// slackChannelsEnabledFromPath reads slack.json at the given path and
// returns ChannelsEnabled. Any error returns false.
func slackChannelsEnabledFromPath(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cfg slackChannelsConfigShape
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return cfg.ChannelsEnabled
}

// maybeInjectClaudeChannels mutates rc.Args in-place by appending
// --channels plugin:gt-slack@gastown when:
//   - slack channels are enabled (channels_enabled: true in slack.json), AND
//   - rc.Command is "claude" (Claude Code only — non-Claude agents are
//     never affected).
//
// Idempotent: if the flag is already present, it is not appended again.
// Safe to call when rc is nil (no-op).
func maybeInjectClaudeChannels(rc *RuntimeConfig) {
	if rc == nil || rc.Command != "claude" {
		return
	}
	if !slackChannelsEnabledFromHome() {
		return
	}
	// Idempotency: skip if --channels with our plugin ref is already present.
	for i, a := range rc.Args {
		if a == "--channels" && i+1 < len(rc.Args) && rc.Args[i+1] == slackChannelsPluginRef {
			return
		}
	}
	rc.Args = append(rc.Args, "--channels", slackChannelsPluginRef)
}
