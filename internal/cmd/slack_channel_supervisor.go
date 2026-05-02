// channel-supervisor is the outer process Claude Code launches via the
// gt-slack plugin's .mcp.json. It exec's `gt slack channel-server` once,
// plumbing stdio through, and exits when the child exits.
//
// Why a wrapper at all (vs pointing the plugin spec directly at
// channel-server): keeps the plugin's .mcp.json command stable while
// future supervisor logic (health probes, structured stderr capture)
// can land without touching the plugin definition.
//
// Why single-shot, not a restart loop: Claude Code sends MCP `initialize`
// once over the plugin's stdio at launch. A restarted child over the
// same stdio would never see initialize, never reach mcp-go's
// Initialized state, and SendNotificationToAllClients would silently
// drop every notifications/claude/channel emit. Crash → plugin dies;
// the user must restart their `claude --channels ...` invocation. In
// the dead-channel window the daemon's flock probe sees the beacon as
// unowned and falls back to the legacy nudge_queue path — durable,
// non-lossy.
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var slackChannelSupervisorCmd = &cobra.Command{
	Use:   "channel-supervisor",
	Short: "Single-shot exec wrapper for channel-server (internal)",
	Long: `Outer process that Claude Code launches via the gt-slack plugin's
.mcp.json. exec's gt slack channel-server once, plumbing stdio through,
and exits when the child exits.

Not for direct invocation. Launched automatically when an agent's
session starts with --channels plugin:gt-slack@gastown.`,
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runSlackChannelSupervisor,
}

func init() {
	slackCmd.AddCommand(slackChannelSupervisorCmd)
}

func runSlackChannelSupervisor(cmd *cobra.Command, _ []string) error {
	gtBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find gt binary: %w", err)
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case <-sigCh:
			fmt.Fprintln(cmd.OutOrStderr(), "channel-supervisor: shutdown signal received")
			cancel()
		case <-ctx.Done():
		}
	}()

	fmt.Fprintln(cmd.OutOrStderr(), "channel-supervisor: launching channel-server")
	c := exec.CommandContext(ctx, gtBin, "slack", "channel-server")
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	runErr := c.Run()
	if ctx.Err() != nil {
		return nil
	}
	if runErr == nil {
		fmt.Fprintln(cmd.OutOrStderr(),
			"channel-supervisor: channel-server exited cleanly, stopping")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStderr(),
		"channel-supervisor: channel-server exited with %v; NOT restarting. "+
			"Restart your claude --channels session to recover.\n",
		runErr)
	return runErr
}
