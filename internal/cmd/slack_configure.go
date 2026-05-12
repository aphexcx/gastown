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

	// Preserve all non-credential fields if reconfiguring — tokens rotate
	// independently of channel routing, channels-delivery enrollment, etc.
	var cfg slack.Config
	if existing, loadErr := slack.LoadConfig(path); loadErr == nil && existing != nil {
		cfg = *existing
	}
	cfg.BotToken = slackConfigBotToken
	cfg.AppToken = slackConfigAppToken
	cfg.OwnerUserID = slackConfigOwnerUserID
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	// MkdirAll doesn't tighten perms on an existing dir, so an old wider
	// config dir would still be wider after we just printed "Saved" with
	// a 0600 file inside. Chmod explicitly.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("tighten config dir perms: %w", err)
	}
	if err := slack.SaveConfig(path, &cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Saved %s\n", path)
	return nil
}
