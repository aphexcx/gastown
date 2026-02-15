package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/workspace"
)

// stopHookResponse is the JSON response for Claude Code's Stop hook.
// See: https://docs.anthropic.com/en/docs/agents-and-tools/claude-code/hooks
type stopHookResponse struct {
	Decision string `json:"decision"`         // "block" or "approve"
	Reason   string `json:"reason,omitempty"` // Message to inject when blocking
}

var signalStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop hook handler — check for queued work at turn boundaries",
	Long: `Called by Claude Code's Stop hook at every turn boundary.

Checks for queued work or messages for the current agent:
1. Unread mail (high/critical priority first)
2. Slung work (hooked beads assigned to this agent)

If work is found, outputs {"decision":"block","reason":"<message>"} which
prevents the turn from ending and injects the message as new context.

If nothing is queued, outputs {"decision":"approve"} and the agent goes idle.

This command must complete in <500ms as it runs on every turn boundary.
All output goes to stdout as JSON for Claude Code to consume.`,
	Args:    cobra.NoArgs,
	RunE:    runSignalStop,
	// Silence usage on error — this is a machine-consumed command
	SilenceUsage:  true,
	SilenceErrors: true,
}

func runSignalStop(cmd *cobra.Command, args []string) error {
	// Detect agent identity
	address := detectSender()
	if address == "" || address == "overseer" {
		// Not an agent session — allow the stop
		return outputStopAllow()
	}

	// Find town root for mail and beads operations
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		// Not in a Gas Town workspace — allow the stop
		return outputStopAllow()
	}

	// Run checks in parallel for speed (<500ms budget).
	// Mail and slung-work checks are independent and each shells out to bd.
	var mailReason, workReason string
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		mailReason = checkUnreadMail(townRoot, address)
	}()

	go func() {
		defer wg.Done()
		workReason = checkStopSlungWork(townRoot)
	}()

	wg.Wait()

	// Mail takes priority (messages from other agents are time-sensitive)
	if mailReason != "" {
		return outputStopBlock(mailReason)
	}
	if workReason != "" {
		return outputStopBlock(workReason)
	}

	// Nothing queued — allow the agent to stop
	return outputStopAllow()
}

// checkUnreadMail checks for unread mail and returns a block reason if found.
func checkUnreadMail(townRoot, address string) string {
	router := mail.NewRouterWithTownRoot(townRoot, townRoot)
	mailbox, err := router.GetMailbox(address)
	if err != nil {
		return ""
	}

	unread, err := mailbox.ListUnread()
	if err != nil || len(unread) == 0 {
		return ""
	}

	// Filter out handoff mail from self (avoid infinite loops where
	// the agent's own handoff mail keeps blocking it)
	var relevant []*mail.Message
	for _, msg := range unread {
		if isSelfHandoff(msg, address) {
			continue
		}
		relevant = append(relevant, msg)
	}

	if len(relevant) == 0 {
		return ""
	}

	// Build the block reason with the most important message
	msg := relevant[0]
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[gt signal stop] You have %d unread message(s). ", len(relevant)))
	sb.WriteString(fmt.Sprintf("Most recent from %s: \"%s\"", msg.From, msg.Subject))
	if len(relevant) > 1 {
		sb.WriteString(fmt.Sprintf(" (+%d more)", len(relevant)-1))
	}
	sb.WriteString("\n\nRun `gt mail inbox` to read your messages, then continue working.")
	return sb.String()
}

// isSelfHandoff returns true if the message is a handoff mail from this agent to itself.
func isSelfHandoff(msg *mail.Message, address string) bool {
	if msg.From == address && strings.Contains(msg.Subject, "HANDOFF") {
		return true
	}
	return false
}

// checkStopSlungWork checks for hooked beads assigned to this agent.
func checkStopSlungWork(townRoot string) string {
	// Get role info for building the agent bead ID
	roleInfo, err := GetRole()
	if err != nil {
		return ""
	}

	identity := roleInfo.ActorString()
	agentBeadID := buildAgentBeadID(identity, roleInfo.Role, townRoot)
	if agentBeadID == "" {
		return ""
	}

	// Check agent bead for hook_bead field (preferred, fast path)
	b := beads.New(townRoot)
	agentBead, err := b.Show(agentBeadID)
	if err == nil && agentBead != nil && agentBead.HookBead != "" {
		// Agent has hooked work — check if it's actually something new
		// (vs. work already being processed in this session)
		hookBead, err := b.Show(agentBead.HookBead)
		if err == nil && hookBead != nil {
			// Only block if the hooked work is in "hooked" status (not yet claimed)
			if hookBead.Status == beads.StatusHooked {
				return fmt.Sprintf("[gt signal stop] Work slung to you: %s — \"%s\"\n\n"+
					"Run `gt hook` to see details, then execute the work.",
					hookBead.ID, hookBead.Title)
			}
		}
		// Agent bead found with hook — no need for fallback
		return ""
	}

	// Fallback: query for any hooked beads assigned to this agent.
	// This catches cases where the agent bead doesn't exist yet.
	hookedBeads, err := b.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: identity,
		Priority: -1,
		Limit:    1,
	})
	if err == nil && len(hookedBeads) > 0 {
		bead := hookedBeads[0]
		return fmt.Sprintf("[gt signal stop] Work slung to you: %s — \"%s\"\n\n"+
			"Run `gt hook` to see details, then execute the work.",
			bead.ID, bead.Title)
	}

	return ""
}

// outputStopAllow outputs the JSON response to approve the agent stopping.
func outputStopAllow() error {
	return outputStopResponse(stopHookResponse{Decision: "approve"})
}

// outputStopBlock outputs the JSON response to block the agent and inject a message.
func outputStopBlock(reason string) error {
	return outputStopResponse(stopHookResponse{Decision: "block", Reason: reason})
}

// outputStopResponse marshals and outputs the JSON response to stdout.
func outputStopResponse(resp stopHookResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		// If we can't marshal, approve the stop rather than crash
		fmt.Fprintln(os.Stdout, `{"decision":"approve"}`)
		return nil
	}
	fmt.Fprintln(os.Stdout, string(data))
	return nil
}
