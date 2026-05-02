package slack

import (
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"
)

func TestUserDisplayCache_GetMissReturnsEmpty(t *testing.T) {
	c := newUserDisplayCache(1 * time.Minute)
	if name, ok := c.get("U_X"); ok || name != "" {
		t.Fatalf("got (%q, %v), want (\"\", false)", name, ok)
	}
}

func TestUserDisplayCache_PutGetRoundTrip(t *testing.T) {
	c := newUserDisplayCache(1 * time.Minute)
	c.put("U_X", "afik_cohen")
	name, ok := c.get("U_X")
	if !ok || name != "afik_cohen" {
		t.Fatalf("got (%q, %v), want (\"afik_cohen\", true)", name, ok)
	}
}

func TestUserDisplayCache_TTLExpiry(t *testing.T) {
	c := newUserDisplayCache(50 * time.Millisecond)
	c.put("U_X", "afik_cohen")
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.get("U_X"); ok {
		t.Fatal("entry still present after TTL; want expired")
	}
}

func TestUserDisplayCache_PutOverwrites(t *testing.T) {
	c := newUserDisplayCache(1 * time.Minute)
	c.put("U_X", "old_name")
	c.put("U_X", "new_name")
	name, ok := c.get("U_X")
	if !ok || name != "new_name" {
		t.Fatalf("got (%q, %v), want (\"new_name\", true)", name, ok)
	}
}

func TestPickDisplayName_Priority(t *testing.T) {
	cases := []struct {
		name string
		user *slackgo.User
		want string
	}{
		{
			name: "display name preferred",
			user: &slackgo.User{
				Profile: slackgo.UserProfile{DisplayName: "afik", RealName: "Afik Cohen"},
				Name:    "afik_cohen",
			},
			want: "afik",
		},
		{
			name: "real name when display empty",
			user: &slackgo.User{
				Profile: slackgo.UserProfile{RealName: "Afik Cohen"},
				Name:    "afik_cohen",
			},
			want: "Afik Cohen",
		},
		{
			name: "login name when others empty",
			user: &slackgo.User{Name: "afik_cohen"},
			want: "afik_cohen",
		},
		{
			name: "raw ID when all empty",
			user: &slackgo.User{},
			want: "U_X",
		},
		{
			name: "nil user falls back",
			user: nil,
			want: "U_X",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickDisplayName(tt.user, "U_X"); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
