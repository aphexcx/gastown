package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/slack"
)

var (
	slackConfigBotToken    string
	slackConfigAppToken    string
	slackConfigOwnerUserID string
)

var slackConfigureCmd = &cobra.Command{
	Use:   "configure",
	Short: "Save Slack tokens and owner user ID",
	Long: `Write credentials to ~/gt/config/slack.json (mode 0600).

The daemon refuses to start unless all three fields are present:
  --bot-token      Slack bot token (xoxb-...)
  --app-token      Slack app-level token (xapp-...)
  --owner-user-id  Your Slack user ID (U...) — the only sender allowed in v1

To find your Slack user ID: open Slack in a browser, click your avatar,
View profile, click the kebab menu, "Copy member ID".`,
	RunE: runSlackConfigure,
}

func init() {
	slackCmd.AddCommand(slackConfigureCmd)
	slackConfigureCmd.Flags().StringVar(&slackConfigBotToken, "bot-token", "", "Slack bot token (xoxb-...)")
	slackConfigureCmd.Flags().StringVar(&slackConfigAppToken, "app-token", "", "Slack app-level token (xapp-...)")
	slackConfigureCmd.Flags().StringVar(&slackConfigOwnerUserID, "owner-user-id", "", "Your Slack user ID (U...)")
	_ = slackConfigureCmd.MarkFlagRequired("bot-token")
	_ = slackConfigureCmd.MarkFlagRequired("app-token")
	_ = slackConfigureCmd.MarkFlagRequired("owner-user-id")
}

func runSlackConfigure(cmd *cobra.Command, _ []string) error {
	path, err := slack.DefaultConfigPath()
	if err != nil {
		return err
	}

	// Preserve channels if reconfiguring.
	existing, loadErr := slack.LoadConfig(path)
	var channels map[string]slack.ChannelConfig
	var defaultTarget string
	if loadErr == nil && existing != nil {
		channels = existing.Channels
		defaultTarget = existing.DefaultTarget
	}

	cfg := &slack.Config{
		BotToken:      slackConfigBotToken,
		AppToken:      slackConfigAppToken,
		OwnerUserID:   slackConfigOwnerUserID,
		DefaultTarget: defaultTarget,
		Channels:      channels,
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := slack.SaveConfig(path, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Saved %s\n", path)
	return nil
}
