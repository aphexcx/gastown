package slack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckSender(t *testing.T) {
	cfg := &Config{OwnerUserID: "U123OWNER"}

	require.True(t, CheckSender(cfg, "U123OWNER"))
	require.False(t, CheckSender(cfg, "UOTHERGUY"))
	require.False(t, CheckSender(cfg, ""))
}

func TestCheckConversation_DM(t *testing.T) {
	cfg := &Config{OwnerUserID: "U1"}

	// DMs are always allowed for approved senders.
	require.True(t, CheckConversation(cfg, ConversationDM, "D123"))
}

func TestCheckConversation_Channel_NotOptedIn(t *testing.T) {
	cfg := &Config{OwnerUserID: "U1"}
	require.False(t, CheckConversation(cfg, ConversationChannel, "C999"))
}

func TestCheckConversation_Channel_OptedIn(t *testing.T) {
	cfg := &Config{
		OwnerUserID: "U1",
		Channels: map[string]ChannelConfig{
			"C123": {Enabled: true, RequireMention: true},
		},
	}
	require.True(t, CheckConversation(cfg, ConversationChannel, "C123"))
}

func TestCheckConversation_Channel_Disabled(t *testing.T) {
	cfg := &Config{
		OwnerUserID: "U1",
		Channels: map[string]ChannelConfig{
			"C123": {Enabled: false},
		},
	}
	require.False(t, CheckConversation(cfg, ConversationChannel, "C123"))
}

func TestRequireMention_AlwaysTrueForChannels(t *testing.T) {
	cfg := &Config{
		Channels: map[string]ChannelConfig{
			// require_mention=false in config is deliberately ignored in v1.
			"C1": {Enabled: true, RequireMention: false},
		},
	}
	require.True(t, RequireMention(cfg, ConversationChannel, "C1"))
	require.False(t, RequireMention(cfg, ConversationDM, "D1"))
}
