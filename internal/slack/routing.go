package slack

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/session"
)

// RoutingTable maps a Slack display name (case-folded) to a gt address
// understood by session.ParseAddress. The routing table is rebuilt from the
// live crew registry; callers should treat it as immutable after build.
type RoutingTable map[string]string

// Resolve looks up a display name. Names are matched case-insensitively.
// Returns the gt address and true on hit, empty and false on miss.
func (r RoutingTable) Resolve(name string) (string, bool) {
	addr, ok := r[strings.ToLower(name)]
	return addr, ok
}

// Names returns a sorted list of display names for error messages.
func (r RoutingTable) Names() []string {
	out := make([]string, 0, len(r))
	for k := range r {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// slackBotMentionPattern matches Slack's machine-readable user mention format,
// including both the bare form <@UABCDEF> and the labeled form <@UABCDEF|name>
// that Slack emits when the user has a display name set. The daemon strips
// these before text-based @name parsing so they don't pollute the routing pass.
var slackBotMentionPattern = regexp.MustCompile(`<@[A-Z0-9-]+(?:\|[^>]*)?>`)

// textMentionPattern matches plain-text @name mentions. Names can contain
// letters, digits, hyphens, and underscores — matching Slack's display name
// rules for bot users. The leading `(?:^|[^A-Za-z0-9_])` requires a word
// boundary before `@` so email addresses like `user@example.com` don't
// produce false-positive mentions.
var textMentionPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])@([a-zA-Z0-9][a-zA-Z0-9_-]*)`)

// ParseMentions returns the text-based @names found in message text, in order.
// It strips all machine-format Slack mentions (`<@UID>` and `<@UID|label>`)
// before matching so the bot's own mention never counts as a routing hint.
//
// The bot user ID parameter is currently unused: we strip every machine-format
// mention, not just the bot's own. Kept in the signature so a future refactor
// can tighten this to the specific bot ID without a breaking API change.
func ParseMentions(text, _ string) []string {
	cleaned := slackBotMentionPattern.ReplaceAllString(text, "")
	matches := textMentionPattern.FindAllStringSubmatch(cleaned, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.ToLower(m[1]))
	}
	return out
}

// ResolveAddressToSession wraps session.ParseAddress and returns the tmux
// session name used as the nudge queue key. It surfaces a clear error if the
// address is malformed or the role has no session (e.g. overseer).
//
// Note: this does NOT check that the tmux session is actually alive — that's
// what SessionChecker below is for. The router inbound path must call both:
// resolve then verify-alive. Writing to a dead queue is a silent failure mode,
// and the spec's failure rule requires us to surface "no live session" to Slack.
func ResolveAddressToSession(address string) (string, error) {
	id, err := session.ParseAddress(address)
	if err != nil {
		return "", fmt.Errorf("parse address %q: %w", address, err)
	}
	name := id.SessionName()
	if name == "" {
		return "", fmt.Errorf("address %q has no session name", address)
	}
	return name, nil
}

// SessionChecker is the minimal tmux surface the router needs to verify a
// session is actually alive before enqueueing. Kept as an interface so
// InboundHandler can be unit-tested without spawning tmux.
type SessionChecker interface {
	HasSession(name string) (bool, error)
}
