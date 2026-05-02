// channel-supervisor is the outer process Claude Code launches via the
// gt-slack plugin's .mcp.json. It exec's `gt slack channel-server` in a
// backoff loop and exits cleanly when stdin closes (Claude Code unloading
// the plugin).
//
// Why this exists: Spike 2 (2026-04-30, Claude Code v2.1.123) confirmed
// Claude Code does NOT auto-restart MCP servers when they die. Without
// this wrapper, a single panic in channel-server means inbound Slack DMs
// stop reaching this session permanently — the plugin shows as "Listening
// for channel messages" but the actual transport is dead.
//
// Lifecycle:
//  1. Plugin .mcp.json runs `gt slack channel-supervisor`
//  2. Supervisor exec's `gt slack channel-server` as a child, plumbing
//     stdin/stdout/stderr through (the MCP transport).
//  3. When the child exits non-zero, sleep with exponential backoff
//     (250ms → 1s → 5s → 30s cap), then re-exec.
//  4. When the child exits zero (clean shutdown — typically because its
//     own stdin saw EOF when Claude unloaded the plugin), supervisor
//     also exits.
//  5. When supervisor receives SIGTERM/SIGINT, cancel ctx and exit
//     cleanly.
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var slackChannelSupervisorCmd = &cobra.Command{
	Use:   "channel-supervisor",
	Short: "Auto-restart wrapper for channel-server (internal)",
	Long: `Outer process that Claude Code launches via the gt-slack plugin's
.mcp.json. exec's gt slack channel-server in a backoff loop because
Claude Code does NOT auto-restart MCP servers on death (verified in
Spike 2). Exits cleanly when stdin closes — Claude Code closes the
plugin's stdin when it unloads.

Not for direct invocation. Launched automatically when an agent's
session starts with --channels plugin:gt-slack@gastown.`,
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runSlackChannelSupervisor,
}

func init() {
	slackCmd.AddCommand(slackChannelSupervisorCmd)
}

// supervisorBackoff returns the wait duration before retrying after the
// Nth consecutive non-zero exit (attempt is 1-indexed). The schedule:
//
//	attempt 1 → 250ms
//	attempt 2 → 1s
//	attempt 3 → 5s
//	attempt 4+ → 30s (cap)
//
// Capping at 30s bounds wasted CPU/log volume when channel-server is
// permanently broken (e.g., a malformed slack.json that fails parse on
// every start) without giving up — a manual fix in the editor will be
// picked up on the next 30s tick.
//
// Defensive: attempt <= 0 clamps to the initial 250ms so a buggy caller
// can't accidentally tight-loop.
func supervisorBackoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 250 * time.Millisecond
	case attempt == 2:
		return 1 * time.Second
	case attempt == 3:
		return 5 * time.Second
	default:
		return 30 * time.Second
	}
}

func runSlackChannelSupervisor(cmd *cobra.Command, _ []string) error {
	gtBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find gt binary: %w", err)
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// SIGTERM/SIGINT → cancel context → child gets killed via
	// CommandContext, supervisor returns nil. Note: we deliberately do
	// NOT spawn a goroutine to drain our own stdin: stdin is plumbed
	// straight to the child each iteration, and the child's own
	// ServeStdio handles EOF (which is how Claude Code signals "unload"
	// — closing the plugin's stdin propagates to our stdin which
	// propagates to the child's stdin). When the child sees EOF and
	// exits zero, we exit zero too.
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

	// Single-shot launch: exec channel-server once, plumb stdio, exit
	// when the child exits.
	//
	// CRITICAL: do NOT restart the child over the same stdio. Claude's
	// MCP `initialize` request has already been consumed by the dead
	// first instance — a fresh child would never see initialize, would
	// never mark its mcp-go session Initialized, and would silently
	// drop every notifications/claude/channel emit (mcp-go filters by
	// Initialized()). Daemon-side dispatch would still see the beacon
	// as held and route inbox events there, where they'd be deleted
	// after a ghost emit. Net result: every Slack DM after a crash
	// would be silently lost.
	//
	// Instead, we exit when the child exits. Claude Code's MCP
	// transport closes; the plugin is gone for the rest of the session.
	// Spike 2 confirmed Claude does not auto-restart MCP servers, so
	// the user must restart their `claude --channels ...` invocation
	// to recover. This trades transparent recovery for the durability
	// guarantee that no inbound Slack message is silently lost.
	//
	// During the dead-channel window, the daemon's flock probe sees
	// the beacon as unowned (channel-server's process exit released
	// the lock) and falls back to the legacy nudge_queue path —
	// durable, non-lossy.
	fmt.Fprintln(cmd.OutOrStderr(), "channel-supervisor: launching channel-server")
	c := exec.CommandContext(ctx, gtBin, "slack", "channel-server")
	c.Stdin = os.Stdin   // MCP transport read side
	c.Stdout = os.Stdout // MCP transport write side
	c.Stderr = os.Stderr
	// Inherit env (GT_SESSION, etc.).

	runErr := c.Run()
	if ctx.Err() != nil {
		// We were cancelled (SIGTERM/SIGINT or parent ctx done).
		return nil
	}
	if runErr == nil {
		fmt.Fprintln(cmd.OutOrStderr(),
			"channel-supervisor: channel-server exited cleanly, stopping")
		return nil
	}
	// Crashed. Don't restart — surface the error and exit.
	fmt.Fprintf(cmd.OutOrStderr(),
		"channel-supervisor: channel-server exited with %v; NOT restarting "+
			"(would silently lose messages; see comment in channel-supervisor source). "+
			"Restart your claude --channels session to recover.\n",
		runErr)
	return runErr
}
