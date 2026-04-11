package cmd

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/slack"
)

var slackListCmd = &cobra.Command{
	Use:         "list",
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "List opted-in Slack channels",
	Long: `Print the channels currently opted in to the Slack router.

This reads ~/gt/config/slack.json directly — it does not talk to the running
daemon. Use 'gt slack status' for live daemon state including the routing
table.`,
	RunE: runSlackList,
}

func init() {
	slackCmd.AddCommand(slackListCmd)
}

func runSlackList(cmd *cobra.Command, _ []string) error {
	path, err := slack.DefaultConfigPath()
	if err != nil {
		return err
	}
	cfg, err := slack.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if len(cfg.Channels) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "(no channels opted in)")
		return nil
	}

	ids := make([]string, 0, len(cfg.Channels))
	for id := range cfg.Channels {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		state := "disabled"
		if cfg.Channels[id].Enabled {
			state = "enabled"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s\n", id, state)
	}
	return nil
}
