package slack

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInboxEventJSONRoundTrip(t *testing.T) {
	original := InboxEvent{
		ChatID:             "D0AT8DU4R08",
		Kind:               "dm",
		MessageTS:          "1714510123.000200",
		TSISO:              "2026-04-30T14:48:43.000200Z",
		ThreadTS:           "",
		Text:               "hey mayor",
		SenderUserID:       "U0AN32RPBFT",
		User:               "afik_cohen",
		ChannelName:        "",
		BotMentioned:       false,
		AttachmentsSummary: "1 file: screenshot.png (image/png, 12 KB)",
	}
	data, err := json.Marshal(&original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed InboxEvent
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed != original {
		t.Fatalf("round trip mismatch:\nwant %+v\ngot  %+v", original, parsed)
	}

	// Confirm the canonical JSON keys are what the spec / Spike 1 results
	// require. The "user" key (not "sender_label") is what Claude renders
	// as the sender attribute in the <channel> tag.
	//
	// Anchor every assertion on the trailing colon — encoding/json always
	// emits "key": with no whitespace, and a substring check for "user"
	// alone would also match inside "sender_user_id".
	js := string(data)
	for _, want := range []string{
		`"chat_id":`, `"kind":`, `"message_ts":`, `"text":`,
		`"sender_user_id":`, `"user":`, `"attachments_summary":`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("missing JSON key %s in: %s", want, js)
		}
	}
	// Negative — make sure we did NOT use the old name.
	if strings.Contains(js, `"sender_label":`) {
		t.Errorf("found old key sender_label; should be 'user' per Spike 1")
	}
}

// TestInboxEventOmitemptyMinimal verifies the omitempty contract: a minimal
// InboxEvent with only the required fields populated must NOT serialize the
// optional keys. Without this we'd silently emit empty fields like
// "user":"" or "thread_ts":"" — Claude would then render an empty sender
// label, and the daemon would waste bytes on every dispatch.
func TestInboxEventOmitemptyMinimal(t *testing.T) {
	minimal := InboxEvent{
		ChatID:       "D1",
		Kind:         "dm",
		MessageTS:    "1.0",
		Text:         "hi",
		SenderUserID: "U1",
	}
	data, err := json.Marshal(&minimal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(data)
	// These optional fields must NOT appear when empty (omitempty contract).
	for _, absent := range []string{
		`"user":`, `"channel_name":`, `"thread_ts":`,
		`"ts_iso":`, `"attachments_summary":`,
		`"bot_mentioned":`, // bool false also omits
	} {
		if strings.Contains(js, absent) {
			t.Errorf("optional key %s present in minimal JSON: %s", absent, js)
		}
	}
	// The required fields must appear.
	for _, present := range []string{
		`"chat_id":`, `"kind":`, `"message_ts":`, `"text":`, `"sender_user_id":`,
	} {
		if !strings.Contains(js, present) {
			t.Errorf("required key %s missing from minimal JSON: %s", present, js)
		}
	}
}

func TestInboxDirAndBeaconPath(t *testing.T) {
	got := InboxDir("/town", "gastown/crew/cog")
	want := "/town/.runtime/slack_inbox/gastown_crew_cog"
	if got != want {
		t.Fatalf("InboxDir: got %q, want %q", got, want)
	}
	gotB := SubscribedBeaconPath("/town", "gastown/crew/cog")
	wantB := want + "/.subscribed"
	if gotB != wantB {
		t.Fatalf("SubscribedBeaconPath: got %q, want %q", gotB, wantB)
	}
	// Sanity: a session name with no slashes should still work.
	if got := InboxDir("/town", "hq-mayor"); got != "/town/.runtime/slack_inbox/hq-mayor" {
		t.Fatalf("InboxDir simple session: got %q", got)
	}
}
