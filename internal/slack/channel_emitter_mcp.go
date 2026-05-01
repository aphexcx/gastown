// MCPEmitter implements Emitter by sending notifications/claude/channel
// via an mcp-go MCPServer's notification facility. Spike 1 verified this
// reaches the assistant context as a <channel source="..." flat-meta-key="...">
// content body</channel> tag.
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
//	    "attachments_summary": "<flat string; nested arrays are silently dropped per Spike 1>",
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
	meta := map[string]any{
		"chat_id":             ev.ChatID,
		"kind":                ev.Kind,
		"message_ts":          ev.MessageTS,
		"ts_iso":              ev.TSISO,
		"thread_ts":           ev.ThreadTS,
		"sender_user_id":      ev.SenderUserID,
		"user":                ev.User,
		"channel_name":        ev.ChannelName,
		"bot_mentioned":       ev.BotMentioned,
		"attachments_summary": ev.AttachmentsSummary,
	}
	params := map[string]any{
		"content": ev.Text,
		"meta":    meta,
	}
	e.srv.SendNotificationToAllClients("notifications/claude/channel", params)
	return nil
}
