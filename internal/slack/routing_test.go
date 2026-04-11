package slack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMentions_Basic(t *testing.T) {
	got := ParseMentions("hey @cody can you check this?", "BOTID")
	require.Equal(t, []string{"cody"}, got)
}

func TestParseMentions_StripsBotID(t *testing.T) {
	got := ParseMentions("<@BOTID> @cody fix the bug", "BOTID")
	require.Equal(t, []string{"cody"}, got)
}

func TestParseMentions_Multiple(t *testing.T) {
	got := ParseMentions("@cody and @gus please review", "BOTID")
	require.Equal(t, []string{"cody", "gus"}, got)
}

func TestParseMentions_None(t *testing.T) {
	got := ParseMentions("hello world", "BOTID")
	require.Empty(t, got)
}

func TestParseMentions_UnderscoresAndDashes(t *testing.T) {
	got := ParseMentions("@houmanoids-refinery on it?", "BOTID")
	require.Equal(t, []string{"houmanoids-refinery"}, got)
}

func TestRoutingTable_Resolve(t *testing.T) {
	rt := RoutingTable{
		"cody":  "houmanoids_www/crew/cody",
		"mayor": "mayor/",
	}
	addr, ok := rt.Resolve("cody")
	require.True(t, ok)
	require.Equal(t, "houmanoids_www/crew/cody", addr)

	_, ok = rt.Resolve("ghost")
	require.False(t, ok)
}

func TestRoutingTable_Names(t *testing.T) {
	rt := RoutingTable{
		"cody":  "houmanoids_www/crew/cody",
		"mayor": "mayor/",
		"gus":   "houmanoids_www/polecats/gus",
	}
	names := rt.Names()
	require.ElementsMatch(t, []string{"cody", "mayor", "gus"}, names)
}

func TestResolveAddressToSession(t *testing.T) {
	cases := []struct {
		address string
		want    string
		wantErr bool
	}{
		{"mayor/", "hq-mayor", false},
		{"deacon/", "hq-deacon", false},
		{"", "", true},
		{"overseer", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.address, func(t *testing.T) {
			got, err := ResolveAddressToSession(tc.address)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
