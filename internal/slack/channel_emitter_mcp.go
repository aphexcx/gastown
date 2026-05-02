// MCPEmitter implements Emitter by sending notifications/claude/channel
// via an mcp-go MCPServer's notification facility. Claude Code renders
// the notification as a <channel source="..." meta-key="..."> content
// </channel> tag injected into the assistant context.
//
// MUST be paired with a server constructed via:
//
//	server.NewMCPServer(name, version,
//	    server.WithExperimental(map[string]any{
//	        "claude/channel": map[string]any{},
//	    }),
//	    ...)
//
// Without the experimental capability, Claude Code silently drops the
// notifications with reason="server did not declare claude/channel
// capability".
package slack

import (
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"
)

// MCPEmitter holds a reference to a running MCPServer so it can emit
// notifications on the same stdio transport Claude Code is reading.
type MCPEmitter struct {
	srv *server.MCPServer
}

// NewMCPEmitter wraps an MCPServer as an Emitter.
func NewMCPEmitter(srv *server.MCPServer) *MCPEmitter {
	return &MCPEmitter{srv: srv}
}

// Emit sends a notifications/claude/channel notification to all initialized
// clients. The notification's params shape:
//
//	{
//	  "content": "<text>",
//	  "meta": {
//	    "chat_id":             "<id>",
//	    "kind":                "<dm|channel>",
//	    "message_ts":          "<slack-ts>",
//	    "ts_iso":              "<iso8601>",
//	    "thread_ts":           "<slack-ts or empty>",
//	    "sender_user_id":      "<U...>",
//	    "user":                "<display name; rendered as channel sender label>",
//	    "channel_name":        "<channel name>",
//	    "bot_mentioned":       <bool>,
//	    "attachments_summary": "<flat string; nested arrays are silently dropped by the renderer>",
//	  }
//	}
//
// The InboxEvent is flattened into the meta map verbatim. Empty/zero
// values are still sent (the daemon already omits them via omitempty
// tags before write — but mcp-go marshals into a map anyway, so we
// emit the keys regardless of zero-ness for transport simplicity).
//
// mcp-go's SendNotificationToAllClients does not return an error; it
// fans out to every Initialized session and logs per-session errors
// internally. We therefore always return nil — the channel-server's
// processOne treats only non-nil errors as failed emits, which matches
// the "fire and forget" semantics of MCP notifications (no per-message
// ack from the client).
func (e *MCPEmitter) Emit(ev InboxEvent) error {
	// Only send non-empty/non-zero meta fields. Claude Code's renderer
	// silently drops the entire notification when ANY meta value is an
	// empty string or false bool, so we must omit them rather than send
	// "" or false. Matches the InboxEvent struct's omitempty JSON tags.
	meta := map[string]any{
		"chat_id":    ev.ChatID,
		"kind":       ev.Kind,
		"message_ts": ev.MessageTS,
	}
	if ev.TSISO != "" {
		meta["ts_iso"] = ev.TSISO
	}
	if ev.ThreadTS != "" {
		meta["thread_ts"] = ev.ThreadTS
	}
	if ev.SenderUserID != "" {
		meta["sender_user_id"] = ev.SenderUserID
	}
	if ev.User != "" {
		meta["user"] = ev.User
	}
	if ev.ChannelName != "" {
		meta["channel_name"] = ev.ChannelName
	}
	if ev.BotMentioned {
		meta["bot_mentioned"] = true
	}
	if ev.AttachmentsSummary != "" {
		meta["attachments_summary"] = ev.AttachmentsSummary
	}
	params := map[string]any{
		"content": ev.Text,
		"meta":    meta,
	}
	// Log only chat+message identifiers + length, never message body.
	// Slack DMs can be sensitive; the daemon's stderr could end up in
	// shared logs (e.g., supervisor tee, third-party log aggregators).
	fmt.Fprintf(os.Stderr,
		"channel-server: emitting notifications/claude/channel chat=%s message_ts=%s len=%d\n",
		ev.ChatID, ev.MessageTS, len(ev.Text))
	e.srv.SendNotificationToAllClients("notifications/claude/channel", params)
	return nil
}
