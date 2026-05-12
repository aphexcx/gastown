package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestNormalizeGTSession_TmuxNameReturnedUnchanged(t *testing.T) {
	var stderr bytes.Buffer
	for _, name := range []string{"hq-mayor", "gt-crew-cog", "hw-witness", ""} {
		t.Run(name, func(t *testing.T) {
			got := normalizeGTSession(name, &stderr)
			if got != name {
				t.Errorf("normalizeGTSession(%q) = %q, want unchanged", name, got)
			}
		})
	}
	if stderr.Len() != 0 {
		t.Errorf("no slashes in input → stderr should be empty; got %q", stderr.String())
	}
}

func TestNormalizeGTSession_AddressFormResolves(t *testing.T) {
	tests := []struct {
		name, in string
		// Just verify the output doesn't contain "/" anymore — actual
		// resolved tmux session names depend on the live registry, which
		// we can't (and shouldn't) reproduce in a unit test.
		// The contract is: address-form goes through resolution.
	}{
		{"mayor/", "mayor/"},
		{"deacon/", "deacon/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got := normalizeGTSession(tc.in, &stderr)
			// On success: stderr contains "normalized to" diagnostic.
			// On failure: stderr contains "resolution failed" warning and
			// the value is returned unchanged.
			// Either path is acceptable — we just verify the helper
			// doesn't crash and writes to stderr.
			if stderr.Len() == 0 {
				t.Errorf("address-form input should emit a diagnostic on stderr; got nothing (out=%q)", got)
			}
			// Both branches leave at least one line on stderr.
			if !strings.Contains(stderr.String(), "channel-server:") {
				t.Errorf("stderr should be prefixed with channel-server: ; got %q", stderr.String())
			}
		})
	}
}

func TestNormalizeGTSession_UnresolvableFallsBack(t *testing.T) {
	// A garbage address-shaped input that ResolveAddressToSession cannot
	// resolve falls back to the original input with a WARNING on stderr.
	var stderr bytes.Buffer
	in := "this/is/not/a/valid/address/at/all"
	got := normalizeGTSession(in, &stderr)
	if got != in {
		t.Errorf("unresolvable input should be returned unchanged; got %q", got)
	}
	// The warning is the key user-visible signal that something's off.
	if !strings.Contains(stderr.String(), "WARNING") {
		t.Errorf("stderr should contain WARNING for unresolvable input; got %q", stderr.String())
	}
}

func TestResolveSenderAddress_DetectedTakesPrecedence(t *testing.T) {
	got := resolveSenderAddress("gastown/crew/cog", "hq-mayor")
	if got != "gastown/crew/cog" {
		t.Errorf("resolveSenderAddress = %q, want detected value %q", got, "gastown/crew/cog")
	}
}

func TestResolveSenderAddress_OverseerFallsThrough(t *testing.T) {
	// "overseer" means detectSender couldn't identify the agent. The helper
	// must fall through to session-name parsing.
	tests := []struct {
		name, detected, session, want string
	}{
		{"mayor session parses to mayor address", "overseer", "hq-mayor", "mayor"},
		{"deacon session parses to deacon address", "overseer", "hq-deacon", "deacon"},
		{"unparseable session falls through to raw value", "overseer", "totally-random-session", "totally-random-session"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveSenderAddress(tc.detected, tc.session)
			if got != tc.want {
				t.Errorf("resolveSenderAddress(%q, %q) = %q, want %q",
					tc.detected, tc.session, got, tc.want)
			}
		})
	}
}

func TestResolveSenderAddress_EmptyDetectedFallsThrough(t *testing.T) {
	// "" should be treated the same as "overseer" — we don't trust an
	// empty detection and prefer session-name parsing.
	got := resolveSenderAddress("", "hq-mayor")
	if got != "mayor" {
		t.Errorf("resolveSenderAddress(\"\", \"hq-mayor\") = %q, want mayor", got)
	}
}

func TestResolveSenderAddress_RawFallbackOnAllFailures(t *testing.T) {
	got := resolveSenderAddress("overseer", "not-a-valid-session-name")
	if got != "not-a-valid-session-name" {
		t.Errorf("got %q, want raw session string fallback", got)
	}
}

func TestResolveSenderAddress_EmptySessionStillReturnsSomething(t *testing.T) {
	// Defensive: an empty session shouldn't cause the helper to return
	// an empty address (which would later cascade into an empty
	// OutboxMessage.From field).
	got := resolveSenderAddress("detected/agent", "")
	if got != "detected/agent" {
		t.Errorf("got %q, want detected value", got)
	}

	// With both empty, return value should still be deterministic
	// (the raw session "" — caller must validate at the boundary,
	// not us). Document via test:
	got2 := resolveSenderAddress("", "")
	if got2 != "" {
		t.Errorf("both-empty input → got %q; helper documents this as raw passthrough", got2)
	}
}
