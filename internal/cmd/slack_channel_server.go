package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/slack"
)

// stderrEmitter is a placeholder Emitter that prints events to stderr.
// Task 8 replaces this with an MCP-backed Emitter that emits
// notifications/claude/channel.
type stderrEmitter struct{}

func (stderrEmitter) Emit(ev slack.InboxEvent) error {
	fmt.Fprintf(os.Stderr, "channel-server: would emit notification: chat=%s text=%q\n",
		ev.ChatID, ev.Text)
	return nil
}

// slackChannelServerCmd is the per-session MCP server Claude Code launches
// via the gt-slack plugin's .mcp.json. It reads GT_SESSION from the
// environment, watches a per-session inbox dir for events written by the
// gt slack daemon, and emits notifications/claude/channel via MCP. Task 7
// wires the watch-and-emit loop using a placeholder stderr Emitter; Task 8
// swaps in an MCP-backed Emitter; Task 9 adds the reply tool.
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
	townRoot, err := findMailWorkDir()
	if err != nil {
		return fmt.Errorf("find town root: %w", err)
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		fmt.Fprintln(cmd.OutOrStderr(), "channel-server: shutdown signal received")
		cancel()
	}()

	// TODO Task 8: replace stderrEmitter with an MCP-backed Emitter that emits
	// notifications/claude/channel.
	// TODO Task 9: register the reply MCP tool.
	srv := slack.NewChannelServer(townRoot, session, stderrEmitter{})
	fmt.Fprintf(cmd.OutOrStderr(), "channel-server: running for session %q\n", session)
	return srv.Run(ctx)
}
