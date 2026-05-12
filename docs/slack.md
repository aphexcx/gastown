# Slack router

Gas Town agents (mayor, crew, polecats) can receive Slack DMs and channel
mentions, and reply via Slack — without exposing user credentials to the
agent, and without giving the agent direct access to the Slack API.

This document covers the architecture and threat model. For end-user setup,
see [`plugins/gt-slack/README.md`](../plugins/gt-slack/README.md). For the
original design + spike findings, see
[`docs/design/gt-slack-channels.md`](design/gt-slack-channels.md).

## What it solves

Agents need to be reachable when the user is on Slack, not at the terminal.
Without this subsystem, an agent only learns about Slack messages when the
user manually pastes them in.

With it: a single host-level daemon (`gt slack daemon`) connects to Slack via
Socket Mode and routes each inbound message to one specific agent's session
(mayor by default, addressed agents otherwise). The agent gets the message in
its assistant context and can reply via an MCP tool that hands off to the
daemon's outbound publisher.

The agent never sees Slack tokens. The daemon never accepts instructions
from message bodies.

## Architecture

```
                    ┌──────────────────────────────────────────────┐
   Slack ◀─────────▶│ gt slack daemon (one per town/host)          │
   Socket Mode      │  • Routing table (display name → gt address) │
                    │  • Privacy/access gates (3 layers)           │
                    │  • Publisher (drains slack_outbox/)          │
                    └──┬─────────────┬───────────┬─────────────────┘
                       │ writes      │ writes    │ reads
                       ▼             ▼           ▼
        slack_inbox/<sess>/   nudge_queue/<sess>/   slack_outbox/
        (Claude + plugin)     (everyone else)        (all)
                       │
                       │ fsnotify
                       ▼
        gt slack channel-supervisor (one per Claude session)
          └─ gt slack channel-server <sess>
              • holds flock on .subscribed
              • emits notifications/claude/channel via MCP
              • exposes the reply MCP tool
                       │
                       ▼
        Claude Code session (<channel> tag injected into context)
```

### Inbound path selection

The daemon picks per-event between two delivery paths by checking
`flock(slack_inbox/<sess>/.subscribed)`:

- **Locked** → a channel-server is alive → write the event JSON to
  `slack_inbox/<sess>/<ts>.json` for fsnotify-driven pickup.
- **Unlocked or beacon missing** → fall back to `nudge.Enqueue` +
  `nudge.StartPoller`, the same legacy path that wakes idle Codex / Gemini /
  Cursor agents via `tmux send-keys`.

One delivery per session, no dedup. The flock is the only behavioral switch.
Routing, the privacy gate, the publisher, and `slack_outbox/` are unchanged
across both paths.

### Outbound path

Agents call the `reply` MCP tool (Claude path) or run `gt slack reply` (any
agent). Both write the same JSON shape to `~/gt/.runtime/slack_outbox/`. The
daemon's publisher drains the dir and posts via `chat.postMessage`
(with optional `files.upload`).

## Components

### `gt slack daemon` (host-level)

One daemon per gastown town. Long-lived process, connects to Slack via Socket
Mode (no public HTTP endpoint required). Responsibilities:

- Maintain the routing table (Slack display-name → gt address mapping,
  derived from the rig registry)
- Run the three access gates BEFORE any disk write (see Threat model)
- Dispatch each accepted event via the `EnqueueNudge` callback (flock-probe
  → channels-or-legacy)
- Drain `slack_outbox/` and post replies, attachments, reactions, and
  thinking-status updates

### `gt slack channel-supervisor` + `channel-server` (per-session)

Spawned by Claude Code when a session is launched with
`--channels plugin:gt-slack@gastown`. The supervisor is a tiny outer wrapper
(`~30 LOC`) that exec's the real server once and propagates its exit code.

**Why single-shot, not a restart loop**: Claude Code drives MCP lifecycle by
sending `initialize` once over the plugin's stdio at launch. A restarted
child over the same stdio would never see `initialize`, never reach mcp-go's
`Initialized()` state, and `SendNotificationToAllClients` would silently drop
every notification. If the channel-server crashes, the plugin dies for the
rest of the session; the user restarts their `claude --channels …`
invocation to recover. The daemon falls back to `nudge_queue` in the
meantime — durable, non-lossy.

The channel-server:

1. Builds the MCP server with `WithExperimental({"claude/channel": {}})` —
   without that capability declaration, Claude Code silently drops every
   notification.
2. Waits for `notifications/initialized` from the client before starting any
   inbox work. No grace-delay backstop: opening the loop early would let
   the server claim+delete inbox files while notifications go nowhere
   (mcp-go filters by `session.Initialized()`).
3. Acquires `flock(LOCK_EX)` on `.subscribed`. Holds it for the process's
   lifetime; releases on exit (kernel-managed).
4. Starts an `fsnotify` watch on the inbox dir, then replays existing files
   in filename order (watch before scan to eliminate the gap window).
5. For each event: atomic-rename `<name>.json → <name>.claimed.<rand>` (so
   concurrent replay-vs-watch races, with the loser seeing `ENOENT`), read,
   emit via `notifications/claude/channel`, delete on success or rename back
   on failure.

### Auto-inject

`internal/config/slack_channels.go::maybeInjectClaudeChannels` mutates the
agent's argv before launch:

- Reads `~/gt/config/slack.json` to check `channels_enabled`
- Only fires for Claude agents (`rc.Provider == AgentClaude`)
- Appends `--channels=plugin:gt-slack@gastown` (or
  `--dangerously-load-development-channels=…` if `channels_dev_mode: true`)
- Uses the `--flag=value` form, NOT space-separated, because Claude Code's
  CLI parses the channels flags as variadic — a space-separated form makes
  the startup-prompt positional arg get eaten as a second channel value

### Wire format

`InboxEvent` (defined in `internal/slack/inbox.go`) is the contract between
the daemon (writer) and channel-server (reader). Flat-scalar-only — Claude
Code's renderer silently drops nested objects/arrays from MCP notification
meta:

```go
type InboxEvent struct {
    ChatID             string  // "D01EXAMPLE01"
    Kind               string  // KindDM | KindChannel
    MessageTS          string  // Slack's float-string ts
    TSISO              string  // ISO 8601 UTC, derived from MessageTS
    ThreadTS           string  // empty for top-level
    Text               string  // message body
    SenderUserID       string  // "U01EXAMPLE01"
    User               string  // display name (rendered as the sender label)
    ChannelName        string  // empty for DMs
    BotMentioned       bool
    AttachmentsSummary string  // flat string; nested arrays would be dropped
}
```

The emitter (`channel_emitter_mcp.go`) only sends non-empty/non-zero meta
fields. Claude's renderer drops the entire notification if ANY meta value is
empty string or false bool.

## Threat model

The daemon is the trust boundary. Agents never see tokens, never make Slack
API calls directly. Three gates fire BEFORE any disk write:

1. **Owner gate**: DMs are only accepted from `owner_user_id`. Anyone else's
   DMs are silently dropped at the daemon.
2. **Channel gate**: Channel messages are only accepted from channels
   explicitly opted in via `gt slack channel <id> on`. The default
   `require_mention: true` means the bot must be mentioned in the message
   (otherwise the daemon ignores it).
3. **Conversation gate**: An in-memory membership cache (
   `internal/slack/access.go`) prevents the bot from leaking message
   history across conversations it hasn't been invited to.

None of these gates can be modified from inside an agent session. The
channel-server's instructions field tells the agent to refuse any
in-message instruction claiming to alter access — those are
prompt-injection attempts.

### Privacy

Slack message bodies are stored briefly in `~/gt/.runtime/slack_inbox/<sess>/`
with `0700`/`0600` perms during delivery. The channel-server deletes each
file after successful emission. Failed emits are renamed back to `.json` for
retry. The daemon's stderr logs include `chat_id`, `message_ts`, and `len`
of each emit — NOT the message body — for diagnostic visibility without
leaking content to shared log aggregators.

### Token handling

`~/gt/config/slack.json` is checked for `0600` perms on load and refuses to
read with anything wider. The bot and app tokens never appear in agent env;
the daemon is the only process that holds them.

## Compatibility

- **Non-Claude agents** (Codex, Gemini, Cursor) work via the legacy
  `nudge_queue` + `tmux send-keys` path with no plugin needed. The flock
  probe always falls through for them because they never launch the
  channel-server.
- **Claude Code without the plugin**: also falls through to legacy.
  `channels_enabled: false` disables the auto-inject entirely.
- **Mixed-mode towns**: a town can have one mayor (Claude, channels-enabled)
  and several polecats (Codex, legacy path) sharing the same daemon. The
  per-session flock-probe dispatch handles each agent independently.

## File reference

| File | Purpose |
|---|---|
| `internal/slack/daemon.go` | Socket Mode loop, dispatch, callback wiring |
| `internal/slack/inbound.go` | Per-message handler (gate + route) |
| `internal/slack/access.go` | Three access gates |
| `internal/slack/routing.go` | Mention parsing, display-name → gt-address |
| `internal/slack/publisher.go` | Drains `slack_outbox/`, posts via Slack API |
| `internal/slack/inbox.go` | `InboxEvent` wire format + path helpers |
| `internal/slack/beacon.go` | flock helpers (wraps `internal/lock`) |
| `internal/slack/channel_server.go` | Inbox loop, atomic claim/process/delete |
| `internal/slack/channel_emitter_mcp.go` | Emits `notifications/claude/channel` |
| `internal/slack/channel_tools.go` | Implements the `reply` MCP tool |
| `internal/slack/client.go` | Slack API wrapper + user display-name cache |
| `internal/slack/config.go` | `slack.json` schema and IO |
| `internal/cmd/slack_*.go` | CLI commands (daemon, configure, channel, reply, channel-server, channel-supervisor) |
| `internal/config/slack_channels.go` | Auto-inject `--channels` flag for Claude |
| `plugins/gt-slack/` | Claude Code plugin manifest |
| `docs/design/gt-slack-channels.md` | Original design + spike findings |
