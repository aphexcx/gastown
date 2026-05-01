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

	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}

		attempt++
		fmt.Fprintf(cmd.OutOrStderr(),
			"channel-supervisor: launching channel-server (attempt %d)\n", attempt)

		c := exec.CommandContext(ctx, gtBin, "slack", "channel-server")
		c.Stdin = os.Stdin   // MCP transport read side
		c.Stdout = os.Stdout // MCP transport write side
		c.Stderr = os.Stderr
		// Inherit env (GT_SESSION, etc.).

		runErr := c.Run()
		if ctx.Err() != nil {
			// We were cancelled (SIGTERM/SIGINT or parent ctx done);
			// child likely died because of that.
			return nil
		}
		if runErr == nil {
			// Clean exit (e.g., child saw stdin EOF and shut down).
			// Treat as graceful end and stop supervising.
			fmt.Fprintln(cmd.OutOrStderr(),
				"channel-supervisor: channel-server exited cleanly, stopping")
			return nil
		}

		wait := supervisorBackoff(attempt)
		fmt.Fprintf(cmd.OutOrStderr(),
			"channel-supervisor: channel-server exited with %v; restarting in %s\n",
			runErr, wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil
		}
	}
}
