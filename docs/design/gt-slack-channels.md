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
- A new Slack-side authorization/access layer. The existing daemon-level privacy gate (`CanAccessConversation` + owner gate + conversation gate) remains the single source of access control. The plugin must not grow its own.
- Network-filesystem support for `slack_inbox/`. flock semantics are assumed to be local POSIX. Sharing `~/gt/.runtime/` over NFS/SMB is out of scope.
- Implementing `notifications/claude/channel/permission` (the permission-prompt mechanism Telegram exposes). The privacy gate already handles access decisions before any inbox write; per-message permission prompts are not part of this design.
- v1 tools: only `reply`. `react` and `edit_message` are deferred — referenced in the design for completeness but explicitly out of scope for the first implementation pass. Add later if Gas Town's use case demands them.
- Eager attachment download for the channels path. v1 carries attachment metadata in the inbox event but does not download files. The `DownloadInboundAttachment` path used by `gt slack reply` today still works for the legacy nudge_queue path; channels-side download parity is a follow-up.

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

- **Startup**:
  1. Open and `flock(LOCK_EX)` `<townRoot>/.runtime/slack_inbox/<safe-session>/.subscribed`. Hold the lock for the process's lifetime. The lock fd is opened with `O_CLOEXEC` (or set `FD_CLOEXEC` after open) to prevent unintended fd inheritance by any child process the plugin might fork.
  2. Start the fsnotify watch on the inbox directory **before** the replay scan. This eliminates the "gap" where files written between replay and watch-start could be missed. fsnotify events that fire for files the replay scan also picks up are deduplicated by the rename-then-process pattern (see below).
  3. Replay: read all existing `*.json` files in the inbox dir, sorted by filename (FIFO via `<unixnano>-<random>.json`), and process each as a normal event.
- **Per-event processing** (atomic, used by both replay and steady-state):
  1. Atomically rename `slack_inbox/<sess>/<name>.json` → `<name>.claimed.<rand>` (same pattern `nudge.Drain` uses). If rename fails with ENOENT, another claim won — skip.
  2. Read + parse the claimed file.
  3. Emit `notifications/claude/channel` via the MCP transport, await success.
  4. **Only on notification success**: delete the claimed file. On error, `os.Rename` it back to `.json` so a subsequent attempt can retry. Log the error to stderr.
- **MCP tools** exposed to the Claude session:
  - `reply(chat_id, text, thread_ts?)` — v1. Writes the JSON shape `gt slack reply` produces, into `slack_outbox/`.
  - `react`, `edit_message` — explicitly out of scope for v1; add in a follow-up.
- **Shutdown**: SIGTERM → release flock → exit. The kernel auto-releases the lock on any other death.

Robustness: panic recovery wrapped around every fsnotify event handler and tool handler. The plugin should run for the full lifetime of the Claude Code session under normal conditions.

**Size estimate**: ~250–350 LOC (revised from earlier 150-LOC estimate after cross-referencing the Telegram plugin's comparable surface area).

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
  "ts_iso": "2026-04-30T14:48:43.000200Z",
  "thread_ts": "",
  "text": "hey mayor, can you check the deploy status?",
  "sender_user_id": "U0AN32RPBFT",
  "sender_label": "afik_cohen",
  "channel_name": "",
  "bot_mentioned": false,
  "attachments_summary": "1 file: screenshot.png (image/png, 12 KB)"
}
```

The `ts_iso` field is a human-readable mirror of `message_ts` so the rendered `<channel>` tag has a useful timestamp even if Slack's float TS is opaque.

**Attachments**: in v1, the inbox JSON carries a flat string summary (`attachments_summary`) rather than a nested array, because Telegram's MCP `meta` consumer (the closest reference for what Claude renders) uses flat scalar fields. If Spike 1 confirms Claude's `meta` handler accepts arrays/objects cleanly, we can lift `attachments_summary` to a structured field in a follow-up. For v1 the assistant gets enough info to know "there's a screenshot attached" but cannot `Read` the file; that capability is part of the deferred attachment-parity follow-up.

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

### Subscription beacon staleness — none, given assumptions

Because we use flock instead of PID files or heartbeats, there is no staleness window — provided two cooperative assumptions hold:

1. **Local POSIX filesystem.** flock is advisory and reliable on local POSIX FS (APFS, ext4, etc.). On NFS / SMB / FUSE filesystems, semantics may degrade. `~/gt/.runtime/` must live on a local disk. Documented in non-goals.
2. **No fd inheritance.** The lock fd must not leak into a long-lived child process the plugin spawns, or the kernel will hold the lock until that child exits. `O_CLOEXEC` (or `FD_CLOEXEC` set immediately after open) ensures any subprocess the plugin runs (today: none) doesn't inadvertently extend the lock's lifetime.

Given those, the only state that can go wrong is a `.subscribed` file existing but unlocked — which is the correct "no plugin alive" signal. The daemon never has to "clean up" or "verify" beacons.

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

### Spike 1: Go MCP + plugin resolution + meta nesting

Three things to verify in one pass — a working stub answers all three.

- Hypothesis A: a Go MCP library supports custom notification methods.
- Hypothesis B: Claude Code resolves `plugin:gt-slack@gastown` from a local marketplace registered via `/plugin add-marketplace path/to/gastown/crew/cog/plugins/`.
- Hypothesis C: Claude Code's `notifications/claude/channel` `meta` field renders nested arrays/objects (e.g., `attachments`) cleanly in the `<channel>` tag, OR confirms we should keep meta flat as the design currently specifies.

Test: minimal `gt slack channel-server` stub. Build a plugin definition under `plugins/gt-slack/`. Launch `claude --channels plugin:gt-slack@gastown` after registering the local marketplace. Have the stub emit one notification with both flat fields and one nested array. Confirm:
- Claude resolves the plugin without error (Hypothesis B).
- The notification reaches the assistant context as a `<channel>` tag (Hypothesis A).
- Inspect how meta nesting renders, decide on schema (Hypothesis C).

Outcomes:
- A fail → either patch the upstream Go MCP lib, write minimal MCP wire-protocol code by hand (~200 LOC, well-specified), or pivot to a Bun/TS plugin (Telegram pattern).
- B fail → research alternative resolution (env var pointing at plugin dir, or absolute path in `--channels`), or publish the plugin to a marketplace.
- C says "nested works fine" → consider lifting `attachments_summary` to structured `attachments` array.
- C says "nested loses fidelity" → schema as designed is correct.

### Spike 2: Claude Code MCP supervision behavior

- Hypothesis: Claude Code auto-restarts MCP servers on process death.
- Test: launch `claude --channels plugin:gt-slack@gastown`, kill the plugin PID externally, observe whether Claude relaunches it.
- Outcome A: yes → done, plugin is just `gt slack channel-server`.
- Outcome B: no → wrap with a tiny `gt slack channel-supervisor` re-exec loop.

## Build sequence

1. **Spike 1** — minimal stub, plugin definition under `plugins/gt-slack/`, register local marketplace, launch with `--channels`, verify notification + meta rendering. Plugin definition gets written here, not in a later step, so the daemon dispatch change in step 3 already has a real referent.
2. **Spike 2** — kill the stub plugin PID, observe whether Claude relaunches it. Decide whether to add a `channel-supervisor` wrapper.
3. Implement `gt slack channel-server` proper: flock + O_CLOEXEC, fsnotify-then-replay, atomic claim/process/delete, MCP notification emission with delete-on-success semantics, `reply` MCP tool.
4. Implement daemon-side dispatch change in `EnqueueNudge`: flock check → either `slack_inbox/<sess>/<ts>.json` or legacy `nudge.Enqueue + StartPoller`.
5. Modify gt's session-spawn helper to auto-add `--channels plugin:gt-slack@gastown` for Claude agents when `channels_enabled: true`.
6. End-to-end test: restart mayor's session with the new flag, DM the bot, verify the channel notification path. Use `reply` MCP tool to round-trip a response.
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

Items previously listed as open are now resolved in the spec or moved to Spike 1 acceptance criteria. Remaining genuinely-open:

1. **Cleanup of orphaned `slack_inbox/` dirs** for sessions that no longer exist. Probably a daemon-side periodic sweep similar to `nudge_queue` TTL, but not blocking. Leaving empty dirs alone is harmless. Defer until we observe accumulation.
2. **TTL for inbox files.** Daemon side should expire un-drained inbox events after some duration (matching the existing `nudge_queue` urgent TTL of 2h?). Not strictly required if Claude sessions are restarted often, but good hygiene. Defer to follow-up.
