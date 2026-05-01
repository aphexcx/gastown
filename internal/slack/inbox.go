// Package slack — InboxEvent is the wire format the gt slack daemon writes
// to slack_inbox/<safe-session>/<ts>.json for the per-session MCP plugin
// (gt slack channel-server) to read and emit as notifications/claude/channel.
//
// This struct is the contract between the daemon (writer) and the plugin
// (reader). Field names map directly to <channel> tag attributes Claude
// renders — keep JSON tags stable; tests assert the canonical key set.
package slack

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
)

// InboxEvent is the JSON shape the daemon writes to a per-session inbox
// directory. Field names match what Claude Code renders as <channel>
// attributes (Spike 1 confirmed: flat scalars only — no nested arrays
// or objects in meta).
//
// HARD REQUIREMENT: this struct must stay flat-scalar-only. Adding any
// non-scalar field (slice, map, nested struct) would break Claude's
// <channel> rendering — Spike 1 showed Claude silently drops nested
// objects/arrays from MCP notification meta, so a nested field would
// vanish at the renderer with no error. If you need richer payloads,
// flatten them into scalar summary fields (see AttachmentsSummary).
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create inbox dir: %w", err)
	}
	data, err := json.MarshalIndent(&ev, "", "  ")
	if err != nil {
		return false, err
	}
	name := fmt.Sprintf("%d-%s.json", time.Now().UnixNano(), inboxRandSuffix())
	tmp := filepath.Join(dir, name+".tmp")
	final := filepath.Join(dir, name)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// inboxRandSuffix is a 4-byte hex helper, distinct name from
// channel_server.go's channelRandSuffix and outbox.go's randomSuffix
// to avoid same-package collision.
func inboxRandSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
