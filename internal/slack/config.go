package slack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ChannelConfig controls how a single Slack channel is handled.
// In v1 require_mention is always treated as true regardless of value —
// the field is reserved for future use.
type ChannelConfig struct {
	Enabled        bool `json:"enabled"`
	RequireMention bool `json:"require_mention"`
}

// Config is the on-disk ~/gt/config/slack.json shape.
type Config struct {
	BotToken      string                   `json:"bot_token"`
	AppToken      string                   `json:"app_token"`
	OwnerUserID   string                   `json:"owner_user_id"`
	DefaultTarget string                   `json:"default_target,omitempty"`
	Channels      map[string]ChannelConfig `json:"channels,omitempty"`

	// ChannelsEnabled, when true, opts Claude Code agents in this town
	// into receiving inbound Slack messages via Claude's
	// notifications/claude/channel mechanism instead of the legacy
	// nudge_queue + tmux-send-keys path. Non-Claude agents (Codex,
	// Gemini, Cursor) always use the legacy path regardless of this
	// flag. Defaults to false during development; turning it on
	// requires the gt slack channel-server plugin to be installed and
	// the agent's session to be launched with the --channels flag
	// (Task 12 wires the auto-inject for Claude crew sessions).
	ChannelsEnabled bool `json:"channels_enabled,omitempty"`
}

// LoadConfig reads and parses the config file at path. It returns
// os.ErrNotExist (wrapped) if the file is missing, so callers can distinguish
// "not configured yet" from malformed JSON. It refuses to read files with
// permissions wider than 0600.
func LoadConfig(path string) (*Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if mode := info.Mode().Perm(); mode&^0o600 != 0 {
		return nil, fmt.Errorf("%s has permissions %o, want 0600 — run chmod 0600 %s",
			path, mode, path)
	}

	// Also check parent directory — spec requires mode ≤0700.
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err == nil {
		if dirMode := dirInfo.Mode().Perm(); dirMode&^0o700 != 0 {
			return nil, fmt.Errorf("config directory %s has permissions %o, want 0700 or stricter — run chmod 0700 %s",
				filepath.Dir(path), dirMode, filepath.Dir(path))
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// SaveConfig writes the config atomically (write to .tmp, fsync, rename)
// with mode 0600. The parent directory must already exist.
func SaveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// Validate returns an error if the config is missing required fields or
// contains syntactically bad values. It does NOT check that tokens are live.
func (c *Config) Validate() error {
	if c.BotToken == "" {
		return fmt.Errorf("bot_token is required")
	}
	if !startsWith(c.BotToken, "xoxb-") {
		return fmt.Errorf("bot_token must start with xoxb-")
	}
	if c.AppToken == "" {
		return fmt.Errorf("app_token is required")
	}
	if !startsWith(c.AppToken, "xapp-") {
		return fmt.Errorf("app_token must start with xapp-")
	}
	if c.OwnerUserID == "" {
		return fmt.Errorf("owner_user_id is required")
	}
	if !isSlackUserID(c.OwnerUserID) {
		return fmt.Errorf("owner_user_id %q is not a valid Slack user ID (want U<alphanumeric>)",
			c.OwnerUserID)
	}
	return nil
}

// DefaultConfigPath returns the conventional path: $HOME/gt/config/slack.json.
// Callers may override for tests.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, "gt", "config", "slack.json"), nil
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func isSlackUserID(s string) bool {
	if len(s) < 2 || s[0] != 'U' {
		return false
	}
	for _, r := range s[1:] {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}
