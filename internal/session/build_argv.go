// Package session — build_argv.go
package session

// SlackChannelsPluginRef is the plugin reference passed to Claude Code's
// --channels flag to enable Slack channel delivery via the in-repo
// gt-slack plugin.
const SlackChannelsPluginRef = "plugin:gt-slack@gastown"

// BuildAgentArgv constructs the argv for launching an agent, optionally
// auto-injecting --channels plugin:gt-slack@gastown for Claude sessions
// when slack channels are enabled in town config.
//
// Non-Claude commands (codex, gemini, opencode, etc.) are unaffected
// even when opts.Enabled is true — channels are a Claude-Code-only
// mechanism. The Claude check is by command name (the "claude" binary
// name); presets that use a different binary won't trigger injection.
//
// Always returns a fresh slice (does not mutate the caller's args).
func BuildAgentArgv(cmdName string, args []string, opts SlackChannelOpts) []string {
	out := make([]string, 0, len(args)+3)
	out = append(out, cmdName)
	out = append(out, args...)
	if shouldInjectChannels(cmdName, opts) {
		out = append(out, "--channels", SlackChannelsPluginRef)
	}
	return out
}

// InjectChannelsArgs appends --channels plugin:gt-slack@gastown to args when
// shouldInjectChannels reports true for cmdName + opts. Returns a fresh slice.
// Used by code paths that already track command and args separately (e.g.
// RuntimeConfig.Args mutation in the config layer).
func InjectChannelsArgs(cmdName string, args []string, opts SlackChannelOpts) []string {
	if !shouldInjectChannels(cmdName, opts) {
		out := make([]string, len(args))
		copy(out, args)
		return out
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, args...)
	out = append(out, "--channels", SlackChannelsPluginRef)
	return out
}

// shouldInjectChannels returns true when the channels flag should be
// auto-injected: opts.Enabled AND cmdName == "claude".
func shouldInjectChannels(cmdName string, opts SlackChannelOpts) bool {
	return opts.Enabled && cmdName == "claude"
}
