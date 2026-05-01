package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// slackChannelServerCmd is the per-session MCP server Claude Code launches
// via the gt-slack plugin's .mcp.json. It reads GT_SESSION from the
// environment, watches a per-session inbox dir for events written by the
// gt slack daemon, and emits notifications/claude/channel via MCP. Task 7
// fills in the watch-and-emit loop; Task 8 wires the MCP transport; Task 9
// adds the reply tool.
//
// Hidden because it's only invoked by Claude Code (when an agent's session
// is launched with --channels plugin:gt-slack@gastown) — never by humans.
var slackChannelServerCmd = &cobra.Command{
	Use:   "channel-server",
	Short: "MCP server for Claude Code channels delivery (internal)",
	Long: `Per-session MCP server launched by Claude Code via --channels.

Reads GT_SESSION env var to know its session name. Watches
<townRoot>/.runtime/slack_inbox/<safe-session>/ for new events written by
the gt slack daemon and emits notifications/claude/channel for each.
Exposes a 'reply' MCP tool that writes outbound messages to slack_outbox/.

Not for direct invocation. Launched automatically when a Gas Town session
starts with --channels plugin:gt-slack@gastown.`,
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runSlackChannelServer,
}

func init() {
	slackCmd.AddCommand(slackChannelServerCmd)
}

func runSlackChannelServer(cmd *cobra.Command, _ []string) error {
	session := os.Getenv("GT_SESSION")
	if session == "" {
		return fmt.Errorf("GT_SESSION env var not set; channel-server must be launched by gt session-spawn (use --channels plugin:gt-slack@gastown)")
	}
	fmt.Fprintf(cmd.OutOrStderr(), "slack channel-server: starting for session %q\n", session)
	// TODO Task 7: acquire subscription beacon, drain replay, fsnotify-watch
	// the inbox dir, emit notifications/claude/channel for each event.
	// TODO Task 8: wire MCP transport so notifications actually reach Claude.
	// TODO Task 9: register the reply tool.
	return fmt.Errorf("channel-server not yet implemented (Task 7+)")
}
