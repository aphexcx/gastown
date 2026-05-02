// Package slack — InboxEvent is the wire format the gt slack daemon writes
// to slack_inbox/<safe-session>/<ts>.json for the per-session MCP plugin
// (gt slack channel-server) to read and emit as notifications/claude/channel.
//
// This struct is the contract between the daemon (writer) and the plugin
// (reader). Field names map directly to <channel> tag attributes Claude
// renders — keep JSON tags stable; tests assert the canonical key set.
package slack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/constants"
)

// InboxEvent is the JSON shape the daemon writes to a per-session inbox
// directory. Field names map to <channel> tag attributes Claude renders.
//
// HARD REQUIREMENT: this struct must stay flat-scalar-only. Claude
// Code's renderer silently drops nested objects/arrays from MCP
// notification meta, so any new non-scalar field would vanish with no
// error. Flatten richer payloads into scalar summary fields instead
// (see AttachmentsSummary).
//
// The "user" field (not "sender_label") is the sender display name —
// Claude uses it in the rendered tag's user-visible label
// ("← gt-slack · <user> · <content>"). Matches the Telegram plugin's
// meta key for parity.
type InboxEvent struct {
	ChatID             string `json:"chat_id"`
	Kind               string `json:"kind"` // "dm" | "channel"
	MessageTS          string `json:"message_ts"`
	TSISO              string `json:"ts_iso,omitempty"`
	ThreadTS           string `json:"thread_ts,omitempty"`
	Text               string `json:"text"`
	SenderUserID       string `json:"sender_user_id"`
	User               string `json:"user,omitempty"` // sender display name; rendered as the channel sender label
	ChannelName        string `json:"channel_name,omitempty"`
	BotMentioned       bool   `json:"bot_mentioned,omitempty"`
	AttachmentsSummary string `json:"attachments_summary,omitempty"`
}

// safeSession converts a gt session name (which may contain "/") into a
// filesystem-safe directory name. Mirrors the same transformation
// internal/nudge uses for nudge_queue/<session>/ paths so the two
// directory roots have parallel structure.
//
// NOTE: this is path-flattening, not a security boundary. gt session
// names are internal identifiers ("gastown/crew/cog", "hq-mayor", etc.)
// constrained by the gt CLI — they cannot contain "..", NUL, backslashes,
// or other shell-hostile characters. We only need to collapse "/" so a
// nested session name lands in a single directory.
func safeSession(session string) string {
	return strings.ReplaceAll(session, "/", "_")
}

// InboxDir returns the per-session inbox directory the daemon writes to.
// Form: <townRoot>/.runtime/slack_inbox/<safe-session>/
func InboxDir(townRoot, session string) string {
	return filepath.Join(townRoot, constants.DirRuntime, "slack_inbox", safeSession(session))
}

// SubscribedBeaconPath returns the lock-file path the per-session plugin
// holds an exclusive flock on for its lifetime. Daemon dispatch checks
// this lock to decide between channels delivery (locked → plugin alive)
// and the legacy nudge_queue fallback (unlocked or missing).
func SubscribedBeaconPath(townRoot, session string) string {
	return filepath.Join(InboxDir(townRoot, session), ".subscribed")
}

// writeInboxIfSubscribed writes ev to slack_inbox/<sess>/<ts>.json IF a
// channel-server is alive (holds the subscription beacon flock).
//
// Returns:
//   - (true, nil) if the event was written to the inbox.
//   - (false, nil) if no plugin is alive — caller should fall back to
//     the legacy nudge_queue path.
//   - (false, err) on filesystem failure (caller should also fall back
//     so an inbox-write hiccup doesn't lose the message).
//
// Uses atomic temp+rename so partial writes never appear to the
// channel-server's fsnotify watcher.
func writeInboxIfSubscribed(townRoot, session string, ev InboxEvent) (bool, error) {
	if !IsSubscribed(townRoot, session) {
		return false, nil
	}
	dir := InboxDir(townRoot, session)
	// 0700: inbox dirs hold Slack message bodies which can be sensitive.
	// Matches slack_outbox/ permissions (publisher uses 0700/0600 too).
	// 0700 to keep Slack message bodies private; matches slack_outbox/.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create inbox dir: %w", err)
	}
	name := fmt.Sprintf("%d-%s.json", time.Now().UnixNano(), randomSuffix())
	final := filepath.Join(dir, name)
	if err := atomicfile.WriteJSONWithPerm(final, &ev, 0o600); err != nil {
		return false, err
	}
	return true, nil
}
