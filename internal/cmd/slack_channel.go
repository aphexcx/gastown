package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/slack"
)

var slackChannelCmd = &cobra.Command{
	Use:   "channel <chat-id> <on|off>",
	Short: "Opt a Slack channel in or out of the router",
	Long: `Enable or disable routing for a specific Slack channel.

Channels default to disabled — the daemon ignores messages in channels that
aren't explicitly opted in. DMs from the configured owner are always allowed
regardless of this setting.`,
	Args: cobra.ExactArgs(2),
	RunE: runSlackChannel,
}

func init() {
	slackCmd.AddCommand(slackChannelCmd)
}

func runSlackChannel(cmd *cobra.Command, args []string) error {
	chatID := args[0]
	mode := args[1]

	var enable bool
	switch mode {
	case "on", "enable":
		enable = true
	case "off", "disable":
		enable = false
	default:
		return fmt.Errorf("mode must be 'on' or 'off', got %q", mode)
	}

	path, err := slack.DefaultConfigPath()
	if err != nil {
		return err
	}
	cfg, err := slack.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("load config: %w (run 'gt slack configure' first)", err)
	}

	if cfg.Channels == nil {
		cfg.Channels = map[string]slack.ChannelConfig{}
	}
	cfg.Channels[chatID] = slack.ChannelConfig{
		Enabled:        enable,
		RequireMention: true, // v1: always required for channels
	}

	if err := slack.SaveConfig(path, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	state := "disabled"
	if enable {
		state = "enabled"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "channel %s %s\n", chatID, state)
	return nil
}
