# gt slack: Claude channels delivery

**Status**: design
**Date**: 2026-04-30
**Author**: cog (gastown/crew/cog)
**Related**: gt-zei3e (immediate fix), feat/gt-slack-router

## Goal

Replace the tmux-send-keys / file-queue / nudge-poller delivery path for inbound Slack messages to **Claude Code agents** with Claude's built-in `notifications/claude/channel` MCP mechanism. Outbound replies for Claude agents move to MCP tools that internally produce the same JSON shape `gt slack reply` writes today. Non-Claude agents (Codex, Gemini, Cursor) keep the existing nudge-queue + `gt slack reply` path.

## Non-goals

- Changing how mail or inter-agent nudges are delivered.
- Changing how the central daemon talks to Slack (Socket Mode, routing table, privacy gate, publisher).
- Removing `gt slack reply`. It stays as the permanent CLI for non-Claude agents and as a fallback.
- Marketplace publishing of the plugin (in-repo only for now).
- Multi-token / multi-bot / per-rig Slack instance support.

## Why

Today the daemon delivers a routed event by calling `nudge.Enqueue(townRoot, sessionName, ...)`, which writes a JSON file to `<townRoot>/.runtime/nudge_queue/<session>/`. Claude Code drains that queue via the `UserPromptSubmit` hook on every turn — but the hook only fires when the user types in the session. For a Claude agent who is idle (e.g., the user is on Slack, no one is typing in mayor's terminal), inbound DMs sit in the queue indefinitely, breaking the "chat from the gym" use case.

`gt-zei3e` shipped a tactical fix: `EnqueueNudge` now also calls `nudge.StartPoller`, which spawns a background process that watches the queue and uses `tmux send-keys` to wake idle Claude agents. That works but is brittle: it depends on tmux, fakes user input via keystrokes, and adds a per-session background process.

Claude Code provides a first-party mechanism, `notifications/claude/channel`, used by the official Telegram plugin (`~/.claude/plugins/marketplaces/claude-plugins-official/external_plugins/telegram/`). An MCP server emits the notification; Claude Code injects it into the assistant's context as a `<channel source="..." ...>` tag. No tmux, no fake keystrokes, no UserPromptSubmit hook contortions.

This design adopts that mechanism for gt slack delivery to Claude agents.

## Architecture

```
                   ┌───────────────────────────────────┐
   Slack ◀────────▶│ gt slack daemon (existing)        │
   Socket Mode    │  • Routing table                  │
                   │  • Privacy gate                   │
                   │  • Publisher (slack_outbox/)      │
                   └─┬────────┬───────────┬────────────┘
                     │ writes │ writes    │ reads
                     ▼        ▼           ▼
        slack_inbox/<sess>/  nudge_queue/<sess>/   slack_outbox/
        (Claude+plugin)      (everyone else)        (all)
                     │
                     │ fsnotify
                     ▼
        gt slack channel-server <sess>  (new)
        • emits notifications/claude/channel
        • exposes reply / react / edit_message MCP tools
        • holds flock on .subscribed
                     │
                     ▼
        Claude Code session (<channel> tag in context)
```

The daemon picks the inbound path per-event by checking `flock(slack_inbox/<sess>/.subscribed)`:

- **Locked** → plugin is alive → write event JSON to `slack_inbox/<sess>/<ts>.json`.
- **Unlocked or beacon missing** → fall back to existing `nudge.Enqueue` + `nudge.StartPoller` path.

One delivery per session, no dedup. The flock is the only behavioral switch. Routing, the privacy gate, the publisher, and `slack_outbox/` are all unchanged.

### Path selection table

| Agent runtime | Inbound path | Outbound path |
|---|---|---|
| Claude Code + plugin alive (flock held) | `slack_inbox/<sess>/` → `notifications/claude/channel` | MCP tools (`reply`, `react`, `edit_message`) writing to `slack_outbox/` |
| Claude Code, plugin not running | `nudge_queue/<sess>/` → `UserPromptSubmit` hook (or poller) | `gt slack reply` CLI |
| Codex / Gemini / Cursor | `nudge_queue/<sess>/` → poller + tmux send-keys | `gt slack reply` CLI |

## Components

### Central daemon — modifications

`internal/slack/daemon.go::EnqueueNudge` callback changes:

1. Resolve address → session name (existing).
2. `flock(LOCK_EX|LOCK_NB)` on `<townRoot>/.runtime/slack_inbox/<safe-session>/.subscribed`.
   - Lock acquired → plugin is **not** alive → release → fall back to legacy path (`nudge.Enqueue` + `nudge.StartPoller`).
   - Lock blocked → plugin **is** alive → write event JSON to `slack_inbox/<safe-session>/<ts>.json`.
3. Log which path was taken at stderr for debug visibility.

No changes to the routing table, privacy gate, Socket Mode dispatch, publisher, or `slack_outbox/` schema.

### Per-session MCP plugin — `gt slack channel-server` (new)

New Go subcommand. Lifetime: one process per Claude Code session, launched by Claude Code as an MCP server. Reads `GT_SESSION` env var to know its session name.

Responsibilities:

- **Startup**: open and `flock(LOCK_EX)` `<townRoot>/.runtime/slack_inbox/<safe-session>/.subscribed`. Hold the lock for the process's lifetime.
- **Replay**: read all existing `*.json` files in `slack_inbox/<safe-session>/`, sorted by filename (FIFO via `<unixnano>-<random>.json`), emit `notifications/claude/channel` for each, delete the file.
- **Steady state**: fsnotify-watch the inbox dir; on each new `*.json` file, emit notification, delete.
- **MCP tools** exposed to the Claude session:
  - `reply(chat_id, text, thread_ts?)` — writes the JSON shape `gt slack reply` produces, into `slack_outbox/`.
  - `react(chat_id, message_ts, emoji)` — same path. (Out of scope for v1 if `reply` alone is sufficient.)
  - `edit_message(chat_id, message_ts, text)` — same path.
- **Shutdown**: SIGTERM → release flock → exit. The kernel auto-releases the lock on any other death.

Robustness: panic recovery wrapped around every fsnotify event handler and tool handler. The plugin should run for the full lifetime of the Claude Code session under normal conditions.

### Plugin definition — `gastown/crew/cog/plugins/gt-slack/` (new)

```
plugins/gt-slack/
  .claude-plugin/
    plugin.json     # name, version, description
  .mcp.json         # { "mcpServers": { "gt-slack": { "command": "gt", "args": ["slack", "channel-server"] } } }
```

The plugin is referenced as `plugin:gt-slack@gastown` after the user registers the local marketplace once via `/plugin add-marketplace path/to/gastown/crew/cog/plugins/`.

### Session-spawn helper — modifications

The single helper that builds the `claude` command line for new agent sessions (location TBD during implementation, likely `internal/session/lifecycle.go` or near it) gets one new check:

```go
if cfg.SlackChannelsEnabled && agent == "claude" {
    args = append(args, "--channels", "plugin:gt-slack@gastown")
}
```

Where `cfg.SlackChannelsEnabled` is derived from `slack.json`'s new `channels_enabled` field (default false during dev).

### JSON schemas

**Inbox event** — `slack_inbox/<sess>/<ts>.json`. New schema, daemon-defined; shared via a Go struct in `internal/slack` so the daemon and plugin agree on the wire format.

```json
{
  "chat_id": "D0AT8DU4R08",
  "kind": "dm",
  "message_ts": "1714510123.000200",
  "thread_ts": "",
  "text": "hey mayor, can you check the deploy status?",
  "sender_user_id": "U0AN32RPBFT",
  "sender_label": "afik_cohen",
  "channel_name": "",
  "bot_mentioned": false,
  "attachments": [
    { "name": "screenshot.png", "path": "/Users/.../slack_inbox_files/...", "mime_type": "image/png", "size": 12345 }
  ]
}
```

**Outbox reply** — `slack_outbox/<ts>.json`. Existing schema, unchanged. The plugin's `reply` tool writes the same JSON `gt slack reply` writes today.

## Data flow

### Inbound — Claude with plugin (channels path)

```
Slack DM "hey mayor"
  ↓ Socket Mode
gt slack daemon
  ↓ existing dispatch: events_api → onEventsAPI → handler.Handle
  ↓ filters: echo, owner gate, privacy gate, conversation gate
  ↓ resolve target → session "hq-mayor"
  ↓ flock check on slack_inbox/hq-mayor/.subscribed
  ↓ → locked (plugin alive) → channels path
  ↓ write slack_inbox/hq-mayor/1714510123-a1b2.json
fsnotify → gt slack channel-server (mayor's plugin)
  ↓ read JSON, emit MCP notification
mcp.notification({
  method: "notifications/claude/channel",
  params: { content: "hey mayor", meta: { chat_id, message_ts, thread_ts, sender_label, ... } }
})
  ↓ delete the inbox file
Claude Code injects <channel source="slack" chat_id="..." sender="afik_cohen">hey mayor</channel>
  ↓ in mayor's assistant context
Mayor processes, calls reply MCP tool
```

### Inbound — non-Claude or Claude without plugin

```
Slack DM "hey jasper"
  ↓ Socket Mode → daemon → routing → flock check
  ↓ → unlocked / file missing → legacy path
  ↓ nudge.Enqueue(townRoot, "hw-jasper", { Sender: "slack", Priority: Urgent, ... })
  ↓ nudge.StartPoller(townRoot, "hw-jasper")  // gt-zei3e fix
poller fsnotify-watches → wait for idle → tmux send-keys
Agent's UserPromptSubmit hook (Claude) or direct injection (Codex/Gemini) drains
```

### Outbound — Claude via plugin

```
Mayor calls reply MCP tool: { chat_id, text, thread_ts? }
  ↓ plugin writes slack_outbox/<ts>.json (same shape as gt slack reply produces)
gt slack daemon publisher
  ↓ fsnotify-watches slack_outbox/, picks up the file
  ↓ posts to Slack via chat.postMessage
  ↓ on success: removes file, calls ClearThreadStatus to drop the "working..." indicator
```

### Outbound — non-Claude or fallback

```
Codex agent runs: gt slack reply D0AT8DU4R08 "deploy is green" --thread 1714510123.000200
  ↓ existing CLI: writes slack_outbox/<ts>.json
  ↓ identical from here on — daemon publisher handles it
```

## Lifecycle & failure modes

### Plugin startup race

If two `gt slack channel-server` processes start simultaneously for the same session, the second `flock(LOCK_EX)` blocks until the first releases. The second exits cleanly with a "already running" log line. No double-delivery — both would tail the same inbox dir, and only the lock-holder gets the events.

### Plugin death

| Cause | Behavior |
|---|---|
| Graceful (Claude session exits cleanly) | Plugin handles SIGTERM → releases flock → exits. Some events may queue in `slack_inbox/<sess>/` between death and next plugin start; those replay on next start. |
| Crash (panic, OOM, SIGKILL) | Kernel auto-releases the flock. Daemon's next dispatch falls back to nudge_queue. Pre-crash queued inbox events replay when a new plugin starts. |
| Daemon dies / restarts | Plugin keeps holding flock and watching inbox. State is unaffected. Daemon resumes dispatching when it comes back. |

### Inbox replay on plugin start

The plugin reads + drains existing files BEFORE starting fsnotify, in FIFO order. This handles the "predecessor died before consuming" scenario raised during the gt-zei3e investigation. Bounded by daemon-side TTL — daemon should optionally sweep `slack_inbox/<sess>/` for very old files (>2h) the same way `nudge_queue` Drain handles expiry.

### Subscription beacon staleness — none

Because we use flock instead of PID files or heartbeats, there is no staleness window. The only state that can go wrong is a `.subscribed` file existing but unlocked — which is the correct "no plugin alive" signal. The daemon never has to "clean up" or "verify" beacons.

### Mid-event daemon restart

Daemon writes inbox event files atomically (temp + rename, like the existing status snapshot). If the daemon crashes between filter and write, the event is lost — same as today's `nudge.Enqueue` behavior. No regression.

### Outbox shape mismatch

If the plugin's `reply` tool writes a JSON shape the daemon's publisher doesn't understand, the publisher quarantines to `slack_outbox/failed/` (existing behavior). To prevent drift, the daemon and plugin share a Go struct from `internal/slack`. Tests assert the shape round-trips.

### Same-name display collision

Already handled by `buildRoutingTable` — warns and keeps first-seen. No new logic.

### Auto-restart strategy

Layered defense:

1. **Process robustness** (always). The plugin is ~150 LOC: fsnotify loop + MCP notification + 3 tool handlers. Panic recovery on every event/tool handler. Should run for the full lifetime of the Claude Code session under normal conditions.
2. **Claude Code's MCP supervision** (likely already in place; verify via Spike 2). Claude Code may auto-restart MCP servers on process death. If so, we get auto-restart for free.
3. **Wrapper exec loop** (only if Spike 2 shows we need it). Add a `gt slack channel-supervisor` outer process that exec's the real server in a backoff loop.

## Spikes

Run before any meaningful implementation work. <1 hour each.

### Spike 1: Go MCP support for `notifications/claude/channel`

- Hypothesis: a Go MCP library supports custom notification methods.
- Test: minimal `gt slack channel-server` stub, launch via `claude --channels plugin:gt-slack@gastown`, send one fake notification, confirm Claude injects the `<channel>` tag.
- Outcome A: works → Go subcommand as planned.
- Outcome B: doesn't work → either write minimal MCP wire-protocol code by hand (~200 LOC), or pivot to a Bun/TS plugin (Telegram pattern).

### Spike 2: Claude Code MCP supervision behavior

- Hypothesis: Claude Code auto-restarts MCP servers on process death.
- Test: launch `claude --channels plugin:gt-slack@gastown`, kill the plugin PID externally, observe whether Claude relaunches it.
- Outcome A: yes → done, plugin is just `gt slack channel-server`.
- Outcome B: no → wrap with a tiny `gt slack channel-supervisor` re-exec loop.

## Build sequence

1. Spikes resolved.
2. Implement `gt slack channel-server` (per-session MCP plugin: flock, fsnotify, MCP notification emit, reply/react/edit_message tools).
3. Implement daemon-side dispatch change in `EnqueueNudge` (flock check → either inbox or nudge_queue).
4. Add `gastown/crew/cog/plugins/gt-slack/` plugin definition.
5. Modify gt's session-spawn helper to auto-add `--channels plugin:gt-slack@gastown` for Claude agents when `channels_enabled: true`.
6. End-to-end test: restart mayor's session with the new flag, DM the bot, verify the channel notification path. Use `reply` MCP tool to round-trip.
7. Restart all Claude agents to pick up the new path.

## Testing

### Unit

- **Daemon dispatch logic** (`internal/slack/channel_inbox_test.go`): given a session with a held flock, `EnqueueNudge` writes to `slack_inbox/<sess>/`. Given an unlocked or missing beacon, falls back to `nudge.Enqueue` (tested via stub). Round-trip the inbox JSON struct.
- **Plugin flock acquisition**: starting with no beacon → acquires. Second instance → blocks/exits cleanly. SIGTERM → releases.
- **Plugin replay**: pre-populate `slack_inbox/<sess>/` with N files, launch plugin (with stub MCP transport), verify N notifications emitted in FIFO order, all files deleted.
- **MCP tool handlers**: `reply` writes the right JSON shape into `slack_outbox/`. Existing publisher tests cover the consume side.

### Integration

- One end-to-end test that exercises the full daemon → inbox → plugin → MCP notification path with a stub Slack client and a stub MCP transport (no real Socket Mode, no real Claude). Regression net for the architectural shape.
- Manual test plan documented separately for the dogfood phase: DM bot, verify notification arrives without tmux activity, reply via MCP tool, verify Slack receives it.

## Open questions

These are unresolved but not blocking — flag during implementation.

1. **MCP marketplace mechanism for in-repo plugins.** How does Claude Code resolve `plugin:gt-slack@gastown`? Verify the local-marketplace add-marketplace flow during Spike 1.
2. **`react` and `edit_message` priority.** Telegram has all three; for Gas Town's use case (mostly DMs from one human, replies from one agent), `reply` alone might be sufficient for v1. Defer if unclear.
3. **Attachment handling parity.** Current daemon downloads small attachments and includes paths in the nudge body. The channel notification's `meta` dict should carry the same paths so the assistant can `Read` them. Verify the existing `DownloadInboundAttachment` is still wired.
4. **Cleanup of orphaned `slack_inbox/` dirs** for sessions that no longer exist. Maybe a daemon-side periodic sweep — or just leave empty dirs alone.
