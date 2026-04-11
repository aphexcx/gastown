package slack

// ConversationKind distinguishes DMs from channels/groups for access rules.
type ConversationKind int

const (
	ConversationDM ConversationKind = iota
	ConversationChannel
)

// CheckSender returns true if the Slack user ID is the configured owner.
// v1 is owner-only: a single user ID is allowed.
func CheckSender(cfg *Config, senderUserID string) bool {
	if cfg == nil || cfg.OwnerUserID == "" {
		return false
	}
	return senderUserID == cfg.OwnerUserID
}

// CheckConversation returns true if the daemon should process messages from
// this conversation. DMs are always allowed for an approved sender (the owner
// gate already ran). Channels must be explicitly opted in via Config.Channels.
func CheckConversation(cfg *Config, kind ConversationKind, chatID string) bool {
	if kind == ConversationDM {
		return true
	}
	if cfg == nil {
		return false
	}
	ch, ok := cfg.Channels[chatID]
	return ok && ch.Enabled
}

// RequireMention returns true when the daemon should require an explicit
// @mention in the message text before routing it. Channels always require
// mentions in v1; DMs never do.
func RequireMention(cfg *Config, kind ConversationKind, chatID string) bool {
	// v1 policy: channels always require a mention, regardless of the
	// require_mention field in config. The field is reserved for future use.
	return kind == ConversationChannel
}
