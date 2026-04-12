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

	// EnqueueNudge hands the message off to internal/nudge. The daemon
	// wires this to nudge.Enqueue with the resolved session name.
	EnqueueNudge func(sessionName, body string) error

	// ResolveSession converts a gt address to a tmux session name. The
	// daemon wires this to ResolveAddressToSession (routing.go).
	ResolveSession func(address string) (string, error)

	// CheckSession verifies the resolved tmux session is actually alive
	// before enqueueing. Writing to a dead queue is a silent failure mode
	// that the spec explicitly rules out. The daemon wires this to
	// (*tmux.Tmux).HasSession.
	CheckSession func(sessionName string) (bool, error)
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

	// 7. Build reply command string.
	reply := fmt.Sprintf("gt slack reply %s \"...\"", msg.ChatID)
	if msg.ThreadTS != "" {
		reply += fmt.Sprintf(" --thread %s", msg.ThreadTS)
	}

	// 8. Build nudge body.
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
	body := fmt.Sprintf(
		"[slack from %s in %s]\n\n%s\n\nTo reply: %s",
		senderLabel, where, msg.Text, reply,
	)

	// 9. Enqueue.
	if err := h.EnqueueNudge(sessionName, body); err != nil {
		h.Ephemeral(msg.ChatID, msg.SenderUserID,
			fmt.Sprintf("⚠️ Couldn't deliver to %q: %v", address, err))
		return
	}
}
