package slack

import (
	"context"
	"fmt"
	"os"
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
	MessageTS    string // the message's own timestamp; used as the thread key when ThreadTS is empty

	// Attachments are file metadata pulled off the Slack event. Empty if
	// the message has no files.
	Attachments []AttachmentMeta

	// BotMentioned is true when the message @mentions the router bot itself
	// (e.g., "@Gas Town how many crew running"). Used to route channel
	// messages with no specific agent mention to the default target — the
	// bot-mention is an explicit signal that the user wants routing.
	BotMentioned bool
}

// ThreadKey returns the Slack thread identifier to use for assistant
// status updates and replies — ThreadTS if the message is in a thread,
// otherwise the message's own timestamp.
func (m IncomingMessage) ThreadKey() string {
	if m.ThreadTS != "" {
		return m.ThreadTS
	}
	return m.MessageTS
}

// statusWorking is the initial indicator posted when we enqueue a nudge.
// Agents can overwrite it with more specific phrases via `gt slack thinking`.
const statusWorking = "working..."

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

	// SetThreadStatus posts a lightweight "thinking" indicator in the
	// Slack thread while the agent is working. Pass an empty status to
	// clear it. Best-effort — failures are silent (log only). The daemon
	// wires this to client.SetAssistantStatus.
	SetThreadStatus func(chatID, threadTS, status string)

	// CanAccessConversation returns true if the bot is a formal member of
	// the given chat (can call conversations.info on it). Used as a
	// membership gate to prevent routing of events the bot receives from
	// DMs it shouldn't have visibility into — a privacy issue discovered
	// when Slack's Agent/Assistant mode delivered events from unrelated
	// user↔user DMs. If nil, membership is not checked (backward compat).
	CanAccessConversation func(chatID string) bool

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

	// 2a. Membership gate for DMs: Slack's Agent/Assistant mode delivers
	// message.im events for DMs the bot isn't in (between two humans),
	// which would leak private conversations. Drop if the bot can't see
	// the conversation via conversations.info. Channels are protected by
	// the explicit opt-in list (step 3), so we skip this roundtrip there.
	if msg.Kind == ConversationDM && h.CanAccessConversation != nil &&
		!h.CanAccessConversation(msg.ChatID) {
		fmt.Fprintf(os.Stderr,
			"slack: DROPPED DM event for %s — bot is not a member (privacy gate)\n",
			msg.ChatID)
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
	case msg.BotMentioned && cfg.DefaultTarget != "":
		// Channel message that @mentions the bot itself (e.g., "@Gas Town
		// how many crew running") with no specific agent — an explicit
		// signal that the user wants routing. Send to default target.
		address = cfg.DefaultTarget
	default:
		// Channel with no mention (neither agent nor bot).
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
	//
	// IMPORTANT: we list file paths with a leading marker in the nudge body
	// (not as bare absolute paths) to avoid Claude Code's auto-attach
	// behavior, which reads the image on every turn and triggers
	// "Could not process image" 400s from Anthropic when the image is
	// malformed or just because of context carryover. Agents who actually
	// want to look at an attachment must explicitly Read the file.
	var downloadedLines []string
	var metaLines []string
	for _, m := range msg.Attachments {
		switch DecideAttachment(m) {
		case AttachmentActionDownload:
			if h.DownloadAttachment == nil {
				metaLines = append(metaLines, fmt.Sprintf(
					"  - %s (%.1f KB, %s) [id=%s] — no downloader configured",
					m.Name, float64(m.Size)/1024.0, m.MimeType, m.ID))
				continue
			}
			path, err := h.DownloadAttachment(ctx, m)
			if err != nil {
				metaLines = append(metaLines, fmt.Sprintf(
					"  - %s [id=%s] — download failed: %v", m.Name, m.ID, err))
				continue
			}
			downloadedLines = append(downloadedLines, fmt.Sprintf(
				"  - %s (%.1f KB, %s) [id=%s] saved at: %s",
				m.Name, float64(m.Size)/1024.0, m.MimeType, m.ID, path))
		default:
			metaLines = append(metaLines, fmt.Sprintf(
				"  - %s (%.1f MB, %s) [id=%s] — larger than %d MB limit, not downloaded",
				m.Name, float64(m.Size)/(1024.0*1024.0), m.MimeType, m.ID,
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

	// Always include --thread so OutboxMessage.ThreadTS is set, which lets
	// the publisher clear the "working..." status indicator on success.
	threadKey := msg.ThreadKey()
	replyArgs := fmt.Sprintf("%s \"your response here\"", msg.ChatID)
	thinkingArgs := fmt.Sprintf("%s \"short verb phrase\"", msg.ChatID)
	if threadKey != "" {
		replyArgs += fmt.Sprintf(" --thread %s", threadKey)
		thinkingArgs += fmt.Sprintf(" --thread %s", threadKey)
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
			"💭 OPTIONAL progress status (like Claude's \"Thinking…\" indicator): "+
			"sprinkle these during your work so the user sees what you're doing:\n\n"+
			"    gt slack thinking %s\n\n"+
			"Use a short verb phrase each time (e.g. \"reading ace's pane\", "+
			"\"checking beads\", \"drafting reply\"). No need to clear at the end — "+
			"the daemon clears automatically when your `gt slack reply` posts.\n\n"+
			"🚫 DO NOT use plugin:slack:slack, any Slack MCP integration, or any "+
			"other Slack tool. Those post using the USER's token (not the Gas Town "+
			"Router bot token) which causes message echo loops — your reply will "+
			"come right back to you as a new Slack DM and you'll loop forever. "+
			"The ONLY safe way to reply is `gt slack reply`.",
		senderLabel, where, msg.Text, replyArgs, thinkingArgs,
	)
	if len(downloadedLines) > 0 || len(metaLines) > 0 {
		body += "\n\n📎 Attachments (DO NOT auto-attach — only read if explicitly relevant):\n"
		if len(downloadedLines) > 0 {
			body += strings.Join(downloadedLines, "\n") + "\n"
		}
		if len(metaLines) > 0 {
			body += strings.Join(metaLines, "\n") + "\n"
		}
		body += "\nIf you need to examine an attachment, use the Read tool "+
			"with the path shown above. Do NOT include these paths verbatim "+
			"in your reply or in any other output — Claude Code may try to "+
			"auto-attach the image, which can cause Anthropic API 400 errors "+
			"if the image is malformed."
	}

	// 9. Deliver.
	if err := h.EnqueueNudge(address, body); err != nil {
		h.Ephemeral(msg.ChatID, msg.SenderUserID,
			fmt.Sprintf("⚠️ Couldn't deliver to %q: %v", address, err))
		return
	}

	if h.SetThreadStatus != nil && threadKey != "" {
		// Async: Slack API latency must not backpressure Socket Mode intake.
		go h.SetThreadStatus(msg.ChatID, threadKey, statusWorking)
	}
}
