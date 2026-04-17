package slack

import (
	"context"
	"fmt"
	"strings"
)

// IncomingMessage is the router's internal form of a Slack message event.
// The Socket Mode event loop (in daemon.go) translates slack-go events into
// this shape so the handler can be tested without the SDK.
type IncomingMessage struct {
	SenderUserID string
	SenderName   string
	Kind         ConversationKind // DM or Channel
	ChatID       string           // channel or DM ID
	ChannelName  string           // pretty name for nudge body, e.g. "crew-chat"
	Text         string
	ThreadTS     string

	// Attachments are file metadata pulled off the Slack event. Empty if
	// the message has no files.
	Attachments []AttachmentMeta
}

// InboundHandler runs the filter → resolve → enqueue pipeline. It has no
// direct dependency on slack-go or internal/nudge; callers inject those via
// function fields so the handler is pure-ish and easy to test.
//
// GetConfig is a function (not a stored *Config) so the daemon can re-read
// slack.json on every inbound event — the spec requires config changes to
// take effect without a daemon restart.
type InboundHandler struct {
	GetConfig func() *Config
	Routing   RoutingTable
	BotUserID string

	// Ephemeral posts a user-visible warning back to the originating chat.
	// The router uses Slack's chat.postEphemeral API, which needs BOTH the
	// channel ID and the target user ID — so this callback takes both plus
	// the warning text.
	Ephemeral func(chatID, userID, text string)

	// EnqueueNudge delivers the inbound Slack message to the target agent.
	// The daemon wires this to `gt nudge <address> --mode=wait-idle --stdin`
	// so the message is pushed into the agent's tmux session promptly when
	// the agent is idle, falling back to the file queue if the agent is busy.
	EnqueueNudge func(address string, body string) error

	// ResolveSession converts a gt address to a tmux session name. The
	// daemon wires this to ResolveAddressToSession (routing.go).
	ResolveSession func(address string) (string, error)

	// CheckSession verifies the resolved tmux session is actually alive
	// before enqueueing. Writing to a dead queue is a silent failure mode
	// that the spec explicitly rules out. The daemon wires this to
	// (*tmux.Tmux).HasSession.
	CheckSession func(sessionName string) (bool, error)

	// DownloadAttachment is called once per file the handler decides to
	// fetch eagerly. The daemon wires this to DownloadInboundAttachment
	// with a real Client; tests pass a fake. A nil hook means the handler
	// only reports attachments as metadata and never downloads.
	DownloadAttachment func(ctx context.Context, m AttachmentMeta) (string, error)
}

// Handle runs one inbound message through the pipeline. It never returns
// an error — it either enqueues successfully or sends an ephemeral reply
// explaining what went wrong.
func (h *InboundHandler) Handle(ctx context.Context, msg IncomingMessage) {
	// Snapshot config for this message. The daemon's GetConfig re-reads
	// slack.json on every call so opt-in changes take effect without restart.
	cfg := h.GetConfig()
	if cfg == nil {
		return
	}

	// 1. Echo filter: drop messages from the bot itself.
	if msg.SenderUserID == h.BotUserID {
		return
	}

	// 1b. Loop guard: drop messages that were posted via a Slack integration
	// using a user token (e.g., Claude Code's Slack MCP plugin). When an
	// agent replies through such an integration instead of `gt slack reply`,
	// Slack appends "*Sent using* <@USER_ID>" to the message. These echoes
	// would otherwise loop: agent replies → Slack → our daemon → agent → ...
	if strings.Contains(msg.Text, "*Sent using* <@") {
		return
	}

	// 2. Owner gate: silent drop (spec exception to no-silent-failures).
	if !CheckSender(cfg, msg.SenderUserID) {
		return
	}

	// 3. Conversation gate.
	if !CheckConversation(cfg, msg.Kind, msg.ChatID) {
		// Channels not opted in → silent drop. The user should run
		// 'gt slack channel <id> on' to opt in. We don't reply here
		// because replying in a channel the bot isn't allowed in would
		// leak presence.
		return
	}

	// 4. Resolve target.
	mentions := ParseMentions(msg.Text, h.BotUserID)
	var address string
	switch {
	case len(mentions) > 0:
		if addr, ok := h.Routing.Resolve(mentions[0]); ok {
			address = addr
		} else {
			h.Ephemeral(msg.ChatID, msg.SenderUserID, fmt.Sprintf(
				"⚠️ Unknown agent \"@%s\". Known: %s",
				mentions[0], strings.Join(h.Routing.Names(), ", "),
			))
			return
		}
	case msg.Kind == ConversationDM && cfg.DefaultTarget != "":
		address = cfg.DefaultTarget
	case msg.Kind == ConversationDM:
		h.Ephemeral(msg.ChatID, msg.SenderUserID,
			"⚠️ No default target configured. @mention an agent or run 'gt slack configure' to set one.")
		return
	default:
		// Channel with no mention.
		h.Ephemeral(msg.ChatID, msg.SenderUserID,
			"⚠️ Channel messages require an explicit @agent mention. "+
				"Known: "+strings.Join(h.Routing.Names(), ", "))
		return
	}

	// 5. Resolve address → session name.
	sessionName, err := h.ResolveSession(address)
	if err != nil {
		h.Ephemeral(msg.ChatID, msg.SenderUserID,
			fmt.Sprintf("⚠️ Couldn't resolve agent %q: %v", address, err))
		return
	}

	// 6. Verify the resolved session is actually alive. Writing to a dead
	// tmux queue is a silent failure mode the spec explicitly rules out.
	alive, err := h.CheckSession(sessionName)
	if err != nil {
		h.Ephemeral(msg.ChatID, msg.SenderUserID,
			fmt.Sprintf("⚠️ Couldn't check session for %q: %v", address, err))
		return
	}
	if !alive {
		h.Ephemeral(msg.ChatID, msg.SenderUserID,
			fmt.Sprintf("⚠️ Agent %q has no active session", address))
		return
	}

	// 7a. Attachment handling: download small files, record metadata for
	// larger ones. Downloads are best-effort — a failure never drops the
	// whole message, we just fall back to metadata-only for that file.
	var downloaded []string
	var metaLines []string
	for _, m := range msg.Attachments {
		switch DecideAttachment(m) {
		case AttachmentActionDownload:
			if h.DownloadAttachment == nil {
				metaLines = append(metaLines, fmt.Sprintf(
					"  [%s] %s (%.1f KB, %s) — no downloader configured",
					m.ID, m.Name, float64(m.Size)/1024.0, m.MimeType))
				continue
			}
			path, err := h.DownloadAttachment(ctx, m)
			if err != nil {
				metaLines = append(metaLines, fmt.Sprintf(
					"  [%s] %s — download failed: %v", m.ID, m.Name, err))
				continue
			}
			downloaded = append(downloaded, path)
		default:
			metaLines = append(metaLines, fmt.Sprintf(
				"  [%s] %s (%.1f MB, %s) — larger than %d MB limit, not downloaded",
				m.ID, m.Name, float64(m.Size)/(1024.0*1024.0), m.MimeType,
				EagerDownloadLimit/(1024*1024)))
		}
	}

	// 8. Build nudge body. The body is directive — it tells the agent this
	// is a live conversation and they should reply back to Slack. Treat the
	// inbound message like a user DM in their terminal.
	senderLabel := msg.SenderName
	if senderLabel == "" {
		senderLabel = msg.SenderUserID
	}
	var where string
	switch msg.Kind {
	case ConversationDM:
		where = "DM"
	default:
		if msg.ChannelName != "" {
			where = "#" + msg.ChannelName
		} else {
			where = msg.ChatID
		}
	}

	replyArgs := fmt.Sprintf("%s \"your response here\"", msg.ChatID)
	if msg.ThreadTS != "" {
		replyArgs += fmt.Sprintf(" --thread %s", msg.ThreadTS)
	}

	body := fmt.Sprintf(
		"📨 Slack message from %s (%s):\n\n%s\n\n"+
			"---\n"+
			"This is a live Slack conversation. Treat this exactly like the "+
			"user DMing you in your terminal — respond now. If it's a question, "+
			"answer it. If it's a task, do it and report back.\n\n"+
			"⚠️ REPLY ONLY by running this exact command:\n\n"+
			"    gt slack reply %s\n\n"+
			"Replace \"your response here\" with your actual reply. "+
			"The --thread flag (if shown) keeps the reply in the right Slack thread.\n\n"+
			"🚫 DO NOT use plugin:slack:slack, any Slack MCP integration, or any "+
			"other Slack tool. Those post using the USER's token (not the Gas Town "+
			"Router bot token) which causes message echo loops — your reply will "+
			"come right back to you as a new Slack DM and you'll loop forever. "+
			"The ONLY safe way to reply is `gt slack reply`.",
		senderLabel, where, msg.Text, replyArgs,
	)
	if len(downloaded) > 0 {
		body += "\n\nDownloaded files:\n"
		for _, p := range downloaded {
			body += "  " + p + "\n"
		}
	}
	if len(metaLines) > 0 {
		body += "\n\nAttachment metadata:\n" + strings.Join(metaLines, "\n")
	}

	// 9. Deliver.
	if err := h.EnqueueNudge(address, body); err != nil {
		h.Ephemeral(msg.ChatID, msg.SenderUserID,
			fmt.Sprintf("⚠️ Couldn't deliver to %q: %v", address, err))
		return
	}
}
