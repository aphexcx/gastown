// channel_tools.go — pure-function implementations of the MCP tools the
// channel-server exposes to Claude. Pure (no MCP SDK types in their
// signatures) so they're testable without spinning up a real MCP server.
//
// The cobra command's tool registrations call into these functions and
// adapt the typed result to mcp-go's CallToolResult shape.

package slack

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
)

// ReplyArgs is the parsed/validated input to the reply tool. Strings are
// trimmed before validation; whitespace-only inputs become empty.
type ReplyArgs struct {
	ChatID   string
	Text     string
	ThreadTS string // optional; omit for top-level DM replies
}

// ReplyResult is what the tool reports back to the model.
//
//   - OK=true means the reply was queued for the daemon's publisher to
//     post to Slack. Path is the JSON file the publisher will pick up.
//   - OK=false with Error set means validation failed or the write
//     failed. The model should surface the Error string to the user.
type ReplyResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Path  string `json:"path,omitempty"`
}

// HandleReply validates inputs, builds an OutboxMessage attributed to the
// running session's gt address, and writes it via WriteOutboxMessage.
//
// Errors are returned as ReplyResult.OK=false rather than Go errors so
// the MCP tool dispatcher can surface a model-visible isError result
// instead of an exception. The returned `error` is reserved for truly
// internal problems (e.g., panics caught in defer) — currently always
// nil.
//
// senderAddress is the gt address of the running session, e.g.
// "gastown/crew/cog" or "mayor/". The cobra command resolves this via
// session.ParseSessionName(GT_SESSION).Address() at call time.
func HandleReply(_ context.Context, townRoot, senderAddress string, args ReplyArgs) (ReplyResult, error) {
	args.ChatID = strings.TrimSpace(args.ChatID)
	args.Text = strings.TrimSpace(args.Text)
	args.ThreadTS = strings.TrimSpace(args.ThreadTS)
	senderAddress = strings.TrimSpace(senderAddress)

	if senderAddress == "" {
		return ReplyResult{
			Error: "channel-server: sender address unresolved (GT_SESSION env var missing or unparseable)",
		}, nil
	}
	if args.ChatID == "" {
		return ReplyResult{
			Error: "chat_id is required (use the chat_id from the inbound channel notification meta)",
		}, nil
	}
	if args.Text == "" {
		return ReplyResult{
			Error: "text must be non-empty",
		}, nil
	}

	dir := filepath.Join(townRoot, constants.DirRuntime, "slack_outbox")
	msg := &OutboxMessage{
		From:      senderAddress,
		ChatID:    args.ChatID,
		Text:      args.Text,
		ThreadTS:  args.ThreadTS,
		Timestamp: time.Now(),
	}
	path, err := WriteOutboxMessage(dir, msg)
	if err != nil {
		return ReplyResult{Error: fmt.Sprintf("write outbox: %v", err)}, nil
	}
	return ReplyResult{OK: true, Path: path}, nil
}
