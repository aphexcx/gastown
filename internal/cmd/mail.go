package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Mail command flags
var (
	mailSubject       string
	mailBody          string
	mailPriority      int
	mailUrgent        bool
	mailType          string
	mailReplyTo       string
	mailNotify        bool
	mailInboxJSON     bool
	mailReadJSON      bool
	mailInboxUnread   bool
	mailInboxIdentity string
	mailCheckInject   bool
	mailCheckJSON     bool
	mailCheckIdentity string
	mailThreadJSON    bool
	mailReplySubject  string
	mailReplyMessage  string
)

var mailCmd = &cobra.Command{
	Use:   "mail",
	Short: "Agent messaging system",
	Long: `Send and receive messages between agents.

The mail system allows Mayor, polecats, and the Refinery to communicate.
Messages are stored in beads as issues with type=message.`,
}

var mailSendCmd = &cobra.Command{
	Use:   "send <address>",
	Short: "Send a message",
	Long: `Send a message to an agent.

Addresses:
  mayor/           - Send to Mayor
  <rig>/refinery   - Send to a rig's Refinery
  <rig>/<polecat>  - Send to a specific polecat
  <rig>/           - Broadcast to a rig

Message types:
  task          - Required processing
  scavenge      - Optional first-come work
  notification  - Informational (default)
  reply         - Response to message

Priority levels (compatible with bd mail send):
  0 - urgent/critical
  1 - high
  2 - normal (default)
  3 - low
  4 - backlog

Use --urgent as shortcut for --priority 0.

Examples:
  gt mail send gastown/Toast -s "Status check" -m "How's that bug fix going?"
  gt mail send mayor/ -s "Work complete" -m "Finished gt-abc"
  gt mail send gastown/ -s "All hands" -m "Swarm starting" --notify
  gt mail send gastown/Toast -s "Task" -m "Fix bug" --type task --priority 1
  gt mail send gastown/Toast -s "Urgent" -m "Help!" --urgent
  gt mail send mayor/ -s "Re: Status" -m "Done" --reply-to msg-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runMailSend,
}

var mailInboxCmd = &cobra.Command{
	Use:   "inbox [address]",
	Short: "Check inbox",
	Long: `Check messages in an inbox.

If no address is specified, shows the current context's inbox.
Use --identity for polecats to explicitly specify their identity.

Examples:
  gt mail inbox                       # Current context (auto-detected)
  gt mail inbox mayor/                # Mayor's inbox
  gt mail inbox gastown/Toast         # Polecat's inbox
  gt mail inbox --identity gastown/Toast  # Explicit polecat identity`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMailInbox,
}

var mailReadCmd = &cobra.Command{
	Use:   "read <message-id>",
	Short: "Read a message",
	Long: `Read a specific message and mark it as read.

The message ID can be found from 'gt mail inbox'.`,
	Args: cobra.ExactArgs(1),
	RunE: runMailRead,
}

var mailDeleteCmd = &cobra.Command{
	Use:   "delete <message-id>",
	Short: "Delete a message",
	Long: `Delete (acknowledge) a message.

This closes the message in beads.`,
	Args: cobra.ExactArgs(1),
	RunE: runMailDelete,
}

var mailCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check for new mail (for hooks)",
	Long: `Check for new mail - useful for Claude Code hooks.

Exit codes (normal mode):
  0 - New mail available
  1 - No new mail

Exit codes (--inject mode):
  0 - Always (hooks should never block)
  Output: system-reminder if mail exists, silent if no mail

Use --identity for polecats to explicitly specify their identity.

Examples:
  gt mail check                           # Simple check (auto-detect identity)
  gt mail check --inject                  # For hooks
  gt mail check --identity gastown/Toast  # Explicit polecat identity`,
	RunE: runMailCheck,
}

var mailThreadCmd = &cobra.Command{
	Use:   "thread <thread-id>",
	Short: "View a message thread",
	Long: `View all messages in a conversation thread.

Shows messages in chronological order (oldest first).

Examples:
  gt mail thread thread-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runMailThread,
}

var mailReplyCmd = &cobra.Command{
	Use:   "reply <message-id>",
	Short: "Reply to a message",
	Long: `Reply to a specific message.

This is a convenience command that automatically:
- Sets the reply-to field to the original message
- Prefixes the subject with "Re: " (if not already present)
- Sends to the original sender

Examples:
  gt mail reply msg-abc123 -m "Thanks, working on it now"
  gt mail reply msg-abc123 -s "Custom subject" -m "Reply body"`,
	Args: cobra.ExactArgs(1),
	RunE: runMailReply,
}

func init() {
	// Send flags
	mailSendCmd.Flags().StringVarP(&mailSubject, "subject", "s", "", "Message subject (required)")
	mailSendCmd.Flags().StringVarP(&mailBody, "message", "m", "", "Message body")
	mailSendCmd.Flags().IntVar(&mailPriority, "priority", 2, "Message priority (0=urgent, 1=high, 2=normal, 3=low, 4=backlog)")
	mailSendCmd.Flags().BoolVar(&mailUrgent, "urgent", false, "Set priority=0 (urgent)")
	mailSendCmd.Flags().StringVar(&mailType, "type", "notification", "Message type (task, scavenge, notification, reply)")
	mailSendCmd.Flags().StringVar(&mailReplyTo, "reply-to", "", "Message ID this is replying to")
	mailSendCmd.Flags().BoolVarP(&mailNotify, "notify", "n", false, "Send tmux notification to recipient")
	_ = mailSendCmd.MarkFlagRequired("subject")

	// Inbox flags
	mailInboxCmd.Flags().BoolVar(&mailInboxJSON, "json", false, "Output as JSON")
	mailInboxCmd.Flags().BoolVarP(&mailInboxUnread, "unread", "u", false, "Show only unread messages")
	mailInboxCmd.Flags().StringVar(&mailInboxIdentity, "identity", "", "Explicit identity for inbox (e.g., gastown/Toast)")
	mailInboxCmd.Flags().StringVar(&mailInboxIdentity, "address", "", "Alias for --identity")

	// Read flags
	mailReadCmd.Flags().BoolVar(&mailReadJSON, "json", false, "Output as JSON")

	// Check flags
	mailCheckCmd.Flags().BoolVar(&mailCheckInject, "inject", false, "Output format for Claude Code hooks")
	mailCheckCmd.Flags().BoolVar(&mailCheckJSON, "json", false, "Output as JSON")
	mailCheckCmd.Flags().StringVar(&mailCheckIdentity, "identity", "", "Explicit identity for inbox (e.g., gastown/Toast)")
	mailCheckCmd.Flags().StringVar(&mailCheckIdentity, "address", "", "Alias for --identity")

	// Thread flags
	mailThreadCmd.Flags().BoolVar(&mailThreadJSON, "json", false, "Output as JSON")

	// Reply flags
	mailReplyCmd.Flags().StringVarP(&mailReplySubject, "subject", "s", "", "Override reply subject (default: Re: <original>)")
	mailReplyCmd.Flags().StringVarP(&mailReplyMessage, "message", "m", "", "Reply message body (required)")
	mailReplyCmd.MarkFlagRequired("message")

	// Add subcommands
	mailCmd.AddCommand(mailSendCmd)
	mailCmd.AddCommand(mailInboxCmd)
	mailCmd.AddCommand(mailReadCmd)
	mailCmd.AddCommand(mailDeleteCmd)
	mailCmd.AddCommand(mailCheckCmd)
	mailCmd.AddCommand(mailThreadCmd)
	mailCmd.AddCommand(mailReplyCmd)

	rootCmd.AddCommand(mailCmd)
}

func runMailSend(cmd *cobra.Command, args []string) error {
	to := args[0]

	// Find workspace - we need a directory with .beads
	workDir, err := findBeadsWorkDir()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine sender
	from := detectSender()

	// Create message
	msg := &mail.Message{
		From:    from,
		To:      to,
		Subject: mailSubject,
		Body:    mailBody,
	}

	// Set priority (--urgent overrides --priority)
	if mailUrgent {
		msg.Priority = mail.PriorityUrgent
	} else {
		msg.Priority = mail.PriorityFromInt(mailPriority)
	}
	if mailNotify && msg.Priority == mail.PriorityNormal {
		msg.Priority = mail.PriorityHigh
	}

	// Set message type
	msg.Type = mail.ParseMessageType(mailType)

	// Handle reply-to: auto-set type to reply and look up thread
	if mailReplyTo != "" {
		msg.ReplyTo = mailReplyTo
		if msg.Type == mail.TypeNotification {
			msg.Type = mail.TypeReply
		}

		// Look up original message to get thread ID
		router := mail.NewRouter(workDir)
		mailbox, err := router.GetMailbox(from)
		if err == nil {
			if original, err := mailbox.Get(mailReplyTo); err == nil {
				msg.ThreadID = original.ThreadID
			}
		}
	}

	// Generate thread ID for new threads
	if msg.ThreadID == "" {
		msg.ThreadID = generateThreadID()
	}

	// Send via router
	router := mail.NewRouter(workDir)
	if err := router.Send(msg); err != nil {
		return fmt.Errorf("sending message: %w", err)
	}

	fmt.Printf("%s Message sent to %s\n", style.Bold.Render("✓"), to)
	fmt.Printf("  Subject: %s\n", mailSubject)
	if msg.Type != mail.TypeNotification {
		fmt.Printf("  Type: %s\n", msg.Type)
	}

	return nil
}

func runMailInbox(cmd *cobra.Command, args []string) error {
	// Find workspace
	workDir, err := findBeadsWorkDir()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine which inbox to check (priority: --identity flag, positional arg, auto-detect)
	address := ""
	if mailInboxIdentity != "" {
		address = mailInboxIdentity
	} else if len(args) > 0 {
		address = args[0]
	} else {
		address = detectSender()
	}

	// Get mailbox
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(address)
	if err != nil {
		return fmt.Errorf("getting mailbox: %w", err)
	}

	// Get messages
	var messages []*mail.Message
	if mailInboxUnread {
		messages, err = mailbox.ListUnread()
	} else {
		messages, err = mailbox.List()
	}
	if err != nil {
		return fmt.Errorf("listing messages: %w", err)
	}

	// JSON output
	if mailInboxJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(messages)
	}

	// Human-readable output
	total, unread, _ := mailbox.Count()
	fmt.Printf("%s Inbox: %s (%d messages, %d unread)\n\n",
		style.Bold.Render("📬"), address, total, unread)

	if len(messages) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no messages)"))
		return nil
	}

	for _, msg := range messages {
		readMarker := "●"
		if msg.Read {
			readMarker = "○"
		}
		typeMarker := ""
		if msg.Type != "" && msg.Type != mail.TypeNotification {
			typeMarker = fmt.Sprintf(" [%s]", msg.Type)
		}
		priorityMarker := ""
		if msg.Priority == mail.PriorityHigh || msg.Priority == mail.PriorityUrgent {
			priorityMarker = " " + style.Bold.Render("!")
		}

		fmt.Printf("  %s %s%s%s\n", readMarker, msg.Subject, typeMarker, priorityMarker)
		fmt.Printf("    %s from %s\n",
			style.Dim.Render(msg.ID),
			msg.From)
		fmt.Printf("    %s\n",
			style.Dim.Render(msg.Timestamp.Format("2006-01-02 15:04")))
	}

	return nil
}

func runMailRead(cmd *cobra.Command, args []string) error {
	msgID := args[0]

	// Find workspace
	workDir, err := findBeadsWorkDir()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine which inbox
	address := detectSender()

	// Get mailbox and message
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(address)
	if err != nil {
		return fmt.Errorf("getting mailbox: %w", err)
	}

	msg, err := mailbox.Get(msgID)
	if err != nil {
		return fmt.Errorf("getting message: %w", err)
	}

	// Note: We intentionally do NOT mark as read/ack on read.
	// User must explicitly delete/ack the message.
	// This preserves handoff messages for reference.

	// JSON output
	if mailReadJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(msg)
	}

	// Human-readable output
	priorityStr := ""
	if msg.Priority == mail.PriorityUrgent {
		priorityStr = " " + style.Bold.Render("[URGENT]")
	} else if msg.Priority == mail.PriorityHigh {
		priorityStr = " " + style.Bold.Render("[HIGH PRIORITY]")
	}

	typeStr := ""
	if msg.Type != "" && msg.Type != mail.TypeNotification {
		typeStr = fmt.Sprintf(" [%s]", msg.Type)
	}

	fmt.Printf("%s %s%s%s\n\n", style.Bold.Render("Subject:"), msg.Subject, typeStr, priorityStr)
	fmt.Printf("From: %s\n", msg.From)
	fmt.Printf("To: %s\n", msg.To)
	fmt.Printf("Date: %s\n", msg.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Printf("ID: %s\n", style.Dim.Render(msg.ID))

	if msg.ThreadID != "" {
		fmt.Printf("Thread: %s\n", style.Dim.Render(msg.ThreadID))
	}
	if msg.ReplyTo != "" {
		fmt.Printf("Reply-To: %s\n", style.Dim.Render(msg.ReplyTo))
	}

	if msg.Body != "" {
		fmt.Printf("\n%s\n", msg.Body)
	}

	return nil
}

func runMailDelete(cmd *cobra.Command, args []string) error {
	msgID := args[0]

	// Find workspace
	workDir, err := findBeadsWorkDir()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine which inbox
	address := detectSender()

	// Get mailbox
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(address)
	if err != nil {
		return fmt.Errorf("getting mailbox: %w", err)
	}

	if err := mailbox.Delete(msgID); err != nil {
		return fmt.Errorf("deleting message: %w", err)
	}

	fmt.Printf("%s Message deleted\n", style.Bold.Render("✓"))
	return nil
}

// findBeadsWorkDir finds a directory with a .beads database.
// Walks up from CWD looking for .beads/ directory.
func findBeadsWorkDir() (string, error) {
	// First try workspace root
	townRoot, err := workspace.FindFromCwdOrError()
	if err == nil {
		// Check if town root has .beads
		if _, err := os.Stat(filepath.Join(townRoot, ".beads")); err == nil {
			return townRoot, nil
		}
	}

	// Walk up from CWD looking for .beads
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	path := cwd
	for {
		if _, err := os.Stat(filepath.Join(path, ".beads")); err == nil {
			return path, nil
		}

		parent := filepath.Dir(path)
		if parent == path {
			break // Reached root
		}
		path = parent
	}

	return "", fmt.Errorf("no .beads directory found")
}

// detectSender determines the current context's address.
func detectSender() string {
	// Check environment variables (set by session start)
	rig := os.Getenv("GT_RIG")
	polecat := os.Getenv("GT_POLECAT")

	if rig != "" && polecat != "" {
		return fmt.Sprintf("%s/%s", rig, polecat)
	}

	// Check current directory
	cwd, err := os.Getwd()
	if err != nil {
		return "mayor/"
	}

	// If in a rig's polecats directory, extract address
	if strings.Contains(cwd, "/polecats/") {
		parts := strings.Split(cwd, "/polecats/")
		if len(parts) >= 2 {
			rigPath := parts[0]
			polecatPath := strings.Split(parts[1], "/")[0]
			rigName := filepath.Base(rigPath)
			return fmt.Sprintf("%s/%s", rigName, polecatPath)
		}
	}

	// If in a rig's crew directory, extract address
	if strings.Contains(cwd, "/crew/") {
		parts := strings.Split(cwd, "/crew/")
		if len(parts) >= 2 {
			rigPath := parts[0]
			crewName := strings.Split(parts[1], "/")[0]
			rigName := filepath.Base(rigPath)
			return fmt.Sprintf("%s/%s", rigName, crewName)
		}
	}

	// Default to mayor
	return "mayor/"
}

func runMailCheck(cmd *cobra.Command, args []string) error {
	// Find workspace
	workDir, err := findBeadsWorkDir()
	if err != nil {
		if mailCheckInject {
			// Inject mode: always exit 0, silent on error
			return nil
		}
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine which inbox (priority: --identity flag, auto-detect)
	address := ""
	if mailCheckIdentity != "" {
		address = mailCheckIdentity
	} else {
		address = detectSender()
	}

	// Get mailbox
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(address)
	if err != nil {
		if mailCheckInject {
			return nil
		}
		return fmt.Errorf("getting mailbox: %w", err)
	}

	// Count unread
	_, unread, err := mailbox.Count()
	if err != nil {
		if mailCheckInject {
			return nil
		}
		return fmt.Errorf("counting messages: %w", err)
	}

	// JSON output
	if mailCheckJSON {
		result := map[string]interface{}{
			"address": address,
			"unread":  unread,
			"has_new": unread > 0,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	// Inject mode: output system-reminder if mail exists
	if mailCheckInject {
		if unread > 0 {
			// Get subjects for context
			messages, _ := mailbox.ListUnread()
			var subjects []string
			for _, msg := range messages {
				subjects = append(subjects, fmt.Sprintf("- From %s: %s", msg.From, msg.Subject))
			}

			fmt.Println("<system-reminder>")
			fmt.Printf("You have %d unread message(s) in your inbox.\n\n", unread)
			for _, s := range subjects {
				fmt.Println(s)
			}
			fmt.Println()
			fmt.Println("Run 'gt mail inbox' to see your messages, or 'gt mail read <id>' for a specific message.")
			fmt.Println("</system-reminder>")
		}
		return nil
	}

	// Normal mode
	if unread > 0 {
		fmt.Printf("%s %d unread message(s)\n", style.Bold.Render("📬"), unread)
		os.Exit(0)
	} else {
		fmt.Println("No new mail")
		os.Exit(1)
	}
	return nil
}

func runMailThread(cmd *cobra.Command, args []string) error {
	threadID := args[0]

	// Find workspace
	workDir, err := findBeadsWorkDir()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine which inbox
	address := detectSender()

	// Get mailbox and thread messages
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(address)
	if err != nil {
		return fmt.Errorf("getting mailbox: %w", err)
	}

	messages, err := mailbox.ListByThread(threadID)
	if err != nil {
		return fmt.Errorf("getting thread: %w", err)
	}

	// JSON output
	if mailThreadJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(messages)
	}

	// Human-readable output
	fmt.Printf("%s Thread: %s (%d messages)\n\n",
		style.Bold.Render("🧵"), threadID, len(messages))

	if len(messages) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no messages in thread)"))
		return nil
	}

	for i, msg := range messages {
		typeMarker := ""
		if msg.Type != "" && msg.Type != mail.TypeNotification {
			typeMarker = fmt.Sprintf(" [%s]", msg.Type)
		}
		priorityMarker := ""
		if msg.Priority == mail.PriorityHigh || msg.Priority == mail.PriorityUrgent {
			priorityMarker = " " + style.Bold.Render("!")
		}

		if i > 0 {
			fmt.Printf("  %s\n", style.Dim.Render("│"))
		}
		fmt.Printf("  %s %s%s%s\n", style.Bold.Render("●"), msg.Subject, typeMarker, priorityMarker)
		fmt.Printf("    %s from %s to %s\n",
			style.Dim.Render(msg.ID),
			msg.From, msg.To)
		fmt.Printf("    %s\n",
			style.Dim.Render(msg.Timestamp.Format("2006-01-02 15:04")))

		if msg.Body != "" {
			fmt.Printf("    %s\n", msg.Body)
		}
	}

	return nil
}

func runMailReply(cmd *cobra.Command, args []string) error {
	msgID := args[0]

	// Find workspace
	workDir, err := findBeadsWorkDir()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine current address
	from := detectSender()

	// Get the original message
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(from)
	if err != nil {
		return fmt.Errorf("getting mailbox: %w", err)
	}

	original, err := mailbox.Get(msgID)
	if err != nil {
		return fmt.Errorf("getting message: %w", err)
	}

	// Build reply subject
	subject := mailReplySubject
	if subject == "" {
		if strings.HasPrefix(original.Subject, "Re: ") {
			subject = original.Subject
		} else {
			subject = "Re: " + original.Subject
		}
	}

	// Create reply message
	reply := &mail.Message{
		From:     from,
		To:       original.From, // Reply to sender
		Subject:  subject,
		Body:     mailReplyMessage,
		Type:     mail.TypeReply,
		Priority: mail.PriorityNormal,
		ReplyTo:  msgID,
		ThreadID: original.ThreadID,
	}

	// If original has no thread ID, create one
	if reply.ThreadID == "" {
		reply.ThreadID = generateThreadID()
	}

	// Send the reply
	if err := router.Send(reply); err != nil {
		return fmt.Errorf("sending reply: %w", err)
	}

	fmt.Printf("%s Reply sent to %s\n", style.Bold.Render("✓"), original.From)
	fmt.Printf("  Subject: %s\n", subject)
	if original.ThreadID != "" {
		fmt.Printf("  Thread: %s\n", style.Dim.Render(original.ThreadID))
	}

	return nil
}

// generateThreadID creates a random thread ID for new message threads.
func generateThreadID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "thread-" + hex.EncodeToString(b)
}
