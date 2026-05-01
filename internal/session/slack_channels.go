// Package session — slack_channels.go
//
// SlackChannelOpts is the session-spawn-side view of slack.json's
// channels_enabled flag. Lives in the session package (NOT internal/slack)
// to avoid an import cycle: internal/slack imports internal/session
// (session.ParseSessionName, session.AgentIdentity), so internal/session
// must not import internal/slack.
//
// Higher layers (cmd, daemon, config) load slack config and pass the resolved
// boolean DOWN to session-spawn helpers.
package session

// SlackChannelOpts gates the auto-injection of --channels plugin:gt-slack@gastown
// into Claude agent argv. Resolved from slack.json's channels_enabled
// field. Default zero value (Enabled=false) matches the "channels off"
// stance expected when slack.json is missing or the flag is unset.
type SlackChannelOpts struct {
	Enabled bool
}
