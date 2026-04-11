package cmd

import (
	"github.com/spf13/cobra"
)

var slackCmd = &cobra.Command{
	Use:     "slack",
	GroupID: GroupComm,
	Short:   "Unified Slack router for Gas Town agents",
	Long: `Run a local daemon that connects one Slack app to all Gas Town
agents, routing inbound Slack messages to the right agent and posting
agent replies back to Slack.

See 'gt slack daemon --help' and 'gt slack reply --help' for details.`,
	RunE: requireSubcommand,
}

func init() {
	rootCmd.AddCommand(slackCmd)
}
