package slack

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfig_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slack.json")

	_, err := LoadConfig(path)
	require.Error(t, err, "missing config should error, not return defaults")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slack.json")

	in := &Config{
		BotToken:    "xoxb-test-bot",
		AppToken:    "xapp-test-app",
		OwnerUserID: "U0123ABC",
		Channels: map[string]ChannelConfig{
			"C456": {Enabled: true, RequireMention: true},
		},
	}
	require.NoError(t, SaveConfig(path, in))

	// File should be mode 0600.
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	out, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, in.BotToken, out.BotToken)
	require.Equal(t, in.AppToken, out.AppToken)
	require.Equal(t, in.OwnerUserID, out.OwnerUserID)
	require.Equal(t, in.Channels, out.Channels)
}

func TestLoadConfig_WidePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slack.json")

	require.NoError(t, os.WriteFile(path, []byte(`{"bot_token":"xoxb-x"}`), 0o644))

	_, err := LoadConfig(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "permissions")
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"missing bot token", Config{AppToken: "xapp-a", OwnerUserID: "U12345"}, "bot_token is required"},
		{"bad bot prefix", Config{BotToken: "nope", AppToken: "xapp-a", OwnerUserID: "U12345"}, "xoxb-"},
		{"missing app token", Config{BotToken: "xoxb-b", OwnerUserID: "U12345"}, "app_token is required"},
		{"bad app prefix", Config{BotToken: "xoxb-b", AppToken: "nope", OwnerUserID: "U12345"}, "xapp-"},
		{"missing owner", Config{BotToken: "xoxb-b", AppToken: "xapp-a"}, "owner_user_id is required"},
		{"bad owner", Config{BotToken: "xoxb-b", AppToken: "xapp-a", OwnerUserID: "bogus"}, "valid Slack user ID"},
		{"ok", Config{BotToken: "xoxb-b", AppToken: "xapp-a", OwnerUserID: "U0123ABC"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
