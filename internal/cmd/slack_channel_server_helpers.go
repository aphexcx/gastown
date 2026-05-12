package cmd

import (
	"fmt"
	"io"
	"strings"

	sessionpkg "github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/slack"
)

// normalizeGTSession converts a GT_SESSION value into a tmux session name
// the daemon's flock-probe path expects.
//
// GT_SESSION may be set to a gt address ("mayor/", "gastown/crew/cog") OR
// to a tmux session name directly ("hq-mayor", "gt-crew-cog"). The daemon
// dispatch resolves addresses → tmux session names via
// slack.ResolveAddressToSession, then probes
// slack_inbox/<sessionName>/.subscribed. The channel-server must use the
// same tmux session name as the beacon path key, otherwise the daemon
// never writes events to the inbox channel-server is watching.
//
// Heuristic: if the value contains "/", treat it as an address and try to
// resolve. Otherwise return it unchanged. Resolution failures fall back to
// the original value with a stderr warning so a misconfigured session
// degrades to "daemon dispatch may not match" rather than crashing.
//
// stderr receives one diagnostic line per call when the path resolves or
// fails to resolve — useful for debugging session-name drift, and
// quiet otherwise.
func normalizeGTSession(session string, stderr io.Writer) string {
	if !strings.Contains(session, "/") {
		return session
	}
	normalized, err := slack.ResolveAddressToSession(session)
	if err != nil {
		fmt.Fprintf(stderr,
			"channel-server: WARNING: GT_SESSION=%q looks like an address but resolution failed: %v (using as-is — daemon dispatch may not match)\n",
			session, err)
		return session
	}
	fmt.Fprintf(stderr,
		"channel-server: GT_SESSION=%q is an address; normalized to session=%q\n",
		session, normalized)
	return normalized
}

// resolveSenderAddress picks the gt address used as OutboxMessage.From for
// every reply call from this channel-server.
//
// Resolution order:
//
//  1. detected — whatever detectSender() returned. detectSender reads
//     GT_ROLE + GT_RIG/GT_CREW/GT_POLECAT/GT_DOG_NAME with cwd and
//     .agent fallbacks. Same path `gt slack reply` uses, so MCP-tool
//     replies and CLI replies attribute to the same identity.
//  2. session as a parseable session name — ParseSessionName converts
//     "hq-mayor" → AgentIdentity{Role: mayor}, whose Address() is "mayor/".
//     Used when detectSender failed to identify the agent (extremely
//     unusual for a channel-server but possible during dev/test).
//  3. raw session string — last-resort label so OutboxMessage.From is
//     never empty.
//
// detected == "" or "overseer" falls through to (2)/(3).
func resolveSenderAddress(detected, session string) string {
	if detected != "" && detected != "overseer" {
		return detected
	}
	if id, err := sessionpkg.ParseSessionName(session); err == nil {
		if addr := id.Address(); addr != "" {
			return addr
		}
	}
	return session
}
