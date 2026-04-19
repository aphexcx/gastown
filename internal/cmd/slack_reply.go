package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/slack"
)

var (
	slackReplyThread string
	slackReplyFiles  []string
)

var slackReplyCmd = &cobra.Command{
	Use:         "reply <chat-id> <text>",
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Send a reply back to Slack (agent-facing)",
	Long: `Write an outbound message to the Slack router outbox. The running
gt slack daemon picks it up and posts to Slack with your agent's display
name.

Inbound Slack messages arrive as nudges that include the exact command to
run — copy it verbatim. This command is fire-and-forget: it returns success
as soon as the message is written to disk, not when Slack acknowledges the
post. Check 'gt slack status' for delivery state.

RULES (these prevent the two known failure modes):

  1. This is the ONLY sanctioned way to reply to Slack. Do not use
     plugin:slack:slack, any Slack MCP integration, or any other Slack
     tool. Those post via the USER's token (not the Gas Town Router bot),
     causing echo loops where your reply comes back to you as a new DM.

  2. If the inbound nudge references file attachments, use the Read tool
     to examine them only if relevant. Never paste the raw absolute path
     into your reply or other output — Claude Code auto-attaches images
     on every turn when it sees such paths, which triggers Anthropic API
     400 errors if the image is malformed and wedges the session.

For dynamic progress indicators during long-running work, see
'gt slack thinking --help'.`,
	Args: cobra.ExactArgs(2),
	RunE: runSlackReply,
}

func init() {
	slackCmd.AddCommand(slackReplyCmd)
	slackReplyCmd.Flags().StringVar(&slackReplyThread, "thread", "",
		"thread timestamp to reply into (from inbound metadata)")
	slackReplyCmd.Flags().StringSliceVar(&slackReplyFiles, "files", nil,
		"absolute paths of files to upload (repeatable)")
}

func runSlackReply(cmd *cobra.Command, args []string) error {
	// Trim whitespace from chat-id (Slack channel IDs never contain spaces).
	// Trim-check text so whitespace-only messages are rejected — almost
	// certainly a bug if an agent sends one.
	chatID := strings.TrimSpace(args[0])
	text := args[1]

	if chatID == "" {
		return fmt.Errorf("chat-id is required")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("text is required (whitespace-only not allowed)")
	}

	// Validate each file exists and is readable — fail fast before writing
	// the outbox message.
	for _, path := range slackReplyFiles {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("file path %q must be absolute", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("file %s: %w", path, err)
		}
		if info.IsDir() {
			return fmt.Errorf("file %s is a directory", path)
		}
	}

	// Town root + caller's gt address. Reuse the existing mail identity
	// detection — it already handles GT_TOWN_ROOT/GT_ROOT/env vars/cwd.
	townRoot, err := findMailWorkDir()
	if err != nil {
		return fmt.Errorf("find town root: %w", err)
	}
	sender := detectSender()
	if sender == "overseer" {
		return fmt.Errorf("cannot detect calling agent identity — set GT_ROLE or run from an agent session")
	}

	outboxDir := filepath.Join(townRoot, constants.DirRuntime, "slack_outbox")
	if err := os.MkdirAll(outboxDir, 0o700); err != nil {
		return fmt.Errorf("create outbox dir: %w", err)
	}

	msg := slack.OutboxMessage{
		From:     sender,
		ChatID:   chatID,
		Text:     text,
		ThreadTS: slackReplyThread,
		Files:    slackReplyFiles,
	}
	path, err := slack.WriteOutboxMessage(outboxDir, &msg)
	if err != nil {
		// Exit code 2 per spec: filesystem write failure.
		fmt.Fprintf(os.Stderr, "slack reply: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "queued: %s\n", filepath.Base(path))
	return nil
}
