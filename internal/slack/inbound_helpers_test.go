package slack

// Tests for the pure helpers in inbound.go: ThreadKey, isoFromSlackTS,
// summarizeAttachments. These were under-covered (15-77%) because the
// existing inbound_test.go focuses on the full Handle pipeline; the
// table-driven tests below exercise edge cases that don't naturally
// surface through the integration-style Handle tests.

import (
	"strings"
	"testing"
)

func TestIncomingMessage_ThreadKey(t *testing.T) {
	tests := []struct {
		name     string
		threadTS string
		msgTS    string
		want     string
	}{
		{"thread present uses ThreadTS", "1714510000.000100", "1714510123.000200", "1714510000.000100"},
		{"no thread falls back to message", "", "1714510123.000200", "1714510123.000200"},
		{"both empty returns empty", "", "", ""},
		{"thread present even when message empty", "1714510000.000100", "", "1714510000.000100"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := IncomingMessage{ThreadTS: tc.threadTS, MessageTS: tc.msgTS}
			if got := m.ThreadKey(); got != tc.want {
				t.Errorf("ThreadKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsoFromSlackTS(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		// Slack timestamps are <unix-seconds>.<microseconds>. The format
		// string is "2006-01-02T15:04:05.000000Z" — UTC, microsecond
		// precision, trailing Z.
		{"empty → empty", "", ""},
		{"no dot → empty", "1714510123", ""},
		{"non-numeric seconds → empty", "abc.000200", ""},
		{"non-numeric micros → empty", "1714510123.xyz", ""},
		{"zero epoch", "0.000000", "1970-01-01T00:00:00.000000Z"},
		{"known timestamp", "1714510123.000200", "2024-04-30T20:48:43.000200Z"},
		{"micros zero-padded correctly", "1714510123.000001", "2024-04-30T20:48:43.000001Z"},
		{"micros not padded (large)", "1714510123.999999", "2024-04-30T20:48:43.999999Z"},
		{"only dot → empty (no seconds, no micros)", ".", ""},
		{"trailing dot → empty (empty micros)", "1714510123.", ""},
		{"leading dot → empty (empty seconds)", ".000200", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isoFromSlackTS(tc.in)
			if got != tc.want {
				t.Errorf("isoFromSlackTS(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsoFromSlackTS_AlwaysUTC(t *testing.T) {
	// Regardless of the host's timezone, the output must end with Z.
	out := isoFromSlackTS("1714510123.000200")
	if !strings.HasSuffix(out, "Z") {
		t.Errorf("output should end with Z (UTC); got %q", out)
	}
}

func TestSummarizeAttachments(t *testing.T) {
	tests := []struct {
		name string
		meta []AttachmentMeta
		want string
	}{
		{"nil → empty", nil, ""},
		{"empty → empty", []AttachmentMeta{}, ""},
		{
			"single file format",
			[]AttachmentMeta{{Name: "screenshot.png", MimeType: "image/png", Size: 12345}},
			"1 file: screenshot.png (image/png, 12345 B)",
		},
		{
			"two files: count summary",
			[]AttachmentMeta{
				{Name: "a.png", MimeType: "image/png", Size: 1},
				{Name: "b.txt", MimeType: "text/plain", Size: 2},
			},
			"2 files attached",
		},
		{
			"many files",
			[]AttachmentMeta{
				{Name: "1"}, {Name: "2"}, {Name: "3"}, {Name: "4"}, {Name: "5"},
			},
			"5 files attached",
		},
		{
			"single file with empty mimetype still formats",
			[]AttachmentMeta{{Name: "x", MimeType: "", Size: 0}},
			"1 file: x (, 0 B)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeAttachments(tc.meta)
			if got != tc.want {
				t.Errorf("summarizeAttachments(%v) = %q, want %q", tc.meta, got, tc.want)
			}
		})
	}
}
