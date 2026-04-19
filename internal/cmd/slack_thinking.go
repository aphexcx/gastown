package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/slack"
)

var slackThinkingThread string

var slackThinkingCmd = &cobra.Command{
	Use:         "thinking <chat-id> <status-text>",
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Update the Slack 'thinking' status indicator (agent-facing)",
	Long: `Set a dynamic status indicator in a Slack Assistant thread, like
Claude or ChatGPT's "Reading files…" / "Drafting reply…" progress messages.

Agents call this during their work to give the user real-time visibility
into what's happening. The chat-id and thread timestamp come from the
inbound nudge body (same values as 'gt slack reply').

Usage pattern — sprinkle throughout your work:

  gt slack thinking C01234567 "reading ace's pane" --thread 1234567890.123456
  gt slack thinking C01234567 "checking recent beads"
  gt slack thinking C01234567 "drafting reply"

Pass an empty status to clear the indicator manually:

  gt slack thinking C01234567 "" --thread 1234567890.123456

The daemon clears the indicator automatically after 'gt slack reply' posts
your actual reply, so explicit clearing is rarely needed.

Requires the Slack app to have 'assistant:write' scope and Assistant mode
enabled. Failures are non-fatal for the caller — this command exits
non-zero but does not block anything else.`,
	Args: cobra.ExactArgs(2),
	RunE: runSlackThinking,
}

func init() {
	slackCmd.AddCommand(slackThinkingCmd)
	slackThinkingCmd.Flags().StringVar(&slackThinkingThread, "thread", "",
		"thread timestamp (from inbound nudge metadata); required unless chat-id is a Slack thread root")
}

func runSlackThinking(cmd *cobra.Command, args []string) error {
	chatID := args[0]
	status := args[1]

	if chatID == "" {
		return fmt.Errorf("chat-id is required")
	}

	cfgPath, err := slack.DefaultConfigPath()
	if err != nil {
		return err
	}
	cfg, err := slack.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w (run 'gt slack configure' first)", err)
	}

	client := slack.NewClient(cfg)
	if err := client.SetAssistantStatus(context.Background(), chatID, slackThinkingThread, status); err != nil {
		// Non-fatal: surface the error so the agent knows, but don't
		// propagate it as a catastrophic failure — status updates are
		// best-effort and must not block the agent's actual work.
		fmt.Fprintf(os.Stderr, "slack thinking: %v\n", err)
		os.Exit(1)
	}

	if status == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "cleared")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "set: %s\n", status)
	}
	return nil
}
