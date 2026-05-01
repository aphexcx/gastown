# gt slack Claude Channels Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the tmux-send-keys / nudge-poller delivery path for inbound Slack messages to Claude Code agents with Claude's `notifications/claude/channel` MCP mechanism. Outbound replies move to MCP tools that write to the existing `slack_outbox/`. Non-Claude agents keep the existing path.

**Architecture:** Central Go daemon (existing) keeps Socket Mode + routing + privacy gate + publisher. New per-session `gt slack channel-server` runs as an MCP server inside each Claude Code session, watches a per-session inbox dir, emits `notifications/claude/channel`, and exposes a `reply` MCP tool. The daemon picks the inbound path per-event by checking flock on a `.subscribed` beacon written by the plugin: locked → channels path; unlocked/missing → legacy nudge_queue path.

**Tech Stack:** Go (existing gt binary), `github.com/fsnotify/fsnotify` v1.9.0 (already in deps), Claude Code's `--channels` plugin mechanism, `github.com/mark3labs/mcp-go` (working assumption — Spike 1 validates), `golang.org/x/sys/unix` for flock + O_CLOEXEC.

**Spec:** `docs/design/gt-slack-channels.md` (commit `fc8c77cf`).

---

## Phase 1: Spikes (must complete before Phase 2)

Two unknowns to resolve before committing to the design. Each spike has explicit go/no-go criteria. If either fails, the plan needs revision before proceeding.

---

### Task 1: Spike 1 — Go MCP library + plugin resolution + meta nesting

Three hypotheses tested in one stub run:
- **A**: a Go MCP library supports custom notification methods (specifically `notifications/claude/channel`).
- **B**: Claude Code resolves `plugin:gt-slack@gastown` from a local marketplace registered via `/plugin add-marketplace`.
- **C**: `notifications/claude/channel`'s `meta` field renders nested arrays/objects cleanly, OR confirms we should keep meta flat.

**Files:**
- Create: `/tmp/spike-gt-slack/server.go` (throwaway stub)
- Create: `/tmp/spike-gt-slack/plugins/gt-slack/.claude-plugin/plugin.json`
- Create: `/tmp/spike-gt-slack/plugins/gt-slack/.mcp.json`
- Create: `/tmp/spike-gt-slack/SPIKE-NOTES.md` (record findings, copy into spec at end)

- [ ] **Step 1: Create the spike workspace and add the candidate Go MCP dep.**

```bash
mkdir -p /tmp/spike-gt-slack/plugins/gt-slack/.claude-plugin
cd /tmp/spike-gt-slack
go mod init spike-gt-slack
go get github.com/mark3labs/mcp-go@latest
```

Expected: `go.mod` created, mcp-go pulled successfully. If the library doesn't exist or the API is incompatible (e.g., no `Notification` method that takes a custom method string), record this in SPIKE-NOTES.md and proceed to Step 1b.

- [ ] **Step 1b (only if Step 1's library is unsuitable): Try alternates.**

Alternates in order of preference:
1. `github.com/metoro-io/mcp-golang`
2. Hand-rolled JSON-RPC over stdio (~200 LOC; the MCP wire protocol is well-specified at <https://modelcontextprotocol.io/docs/specification>).

Record which path the spike took.

- [ ] **Step 2: Write the minimal MCP server stub.**

Create `/tmp/spike-gt-slack/server.go`. The stub must:
- Run as an MCP server over stdio (Claude Code's transport).
- On startup, send ONE notification with method `notifications/claude/channel`, params:
  ```json
  {
    "content": "spike test message — flat meta",
    "meta": {
      "chat_id": "DTEST123",
      "message_ts": "1714510123.000200",
      "ts_iso": "2026-04-30T14:48:43Z",
      "user": "spike",
      "attachments_summary": "1 file: screenshot.png (image/png, 12 KB)"
    }
  }
  ```
- Then send a SECOND notification with a nested array attachment (Hypothesis C):
  ```json
  {
    "content": "spike test message — nested meta",
    "meta": {
      "chat_id": "DTEST123",
      "message_ts": "1714510124.000300",
      "user": "spike",
      "attachments": [
        { "name": "screenshot.png", "mime_type": "image/png", "size": 12345 }
      ]
    }
  }
  ```
- Stay alive (don't exit) so Claude can show what it received.

- [ ] **Step 3: Build the plugin definition.**

Create `/tmp/spike-gt-slack/plugins/gt-slack/.claude-plugin/plugin.json`:
```json
{
  "name": "gt-slack",
  "description": "Spike: Gas Town Slack channel for Claude Code",
  "version": "0.0.1"
}
```

Create `/tmp/spike-gt-slack/plugins/gt-slack/.mcp.json`:
```json
{
  "mcpServers": {
    "gt-slack": {
      "command": "/tmp/spike-gt-slack/spike-server",
      "args": []
    }
  }
}
```

(For the spike, the absolute path keeps it simple. The real plugin will use `gt slack channel-server`.)

- [ ] **Step 4: Build the spike binary.**

```bash
cd /tmp/spike-gt-slack && go build -o spike-server server.go
```

Expected: `/tmp/spike-gt-slack/spike-server` executable exists.

- [ ] **Step 5: Register the local marketplace and install the plugin.**

In a Claude Code session:
```
/plugin add-marketplace /tmp/spike-gt-slack/plugins
/plugin install gt-slack@spike-gt-slack
```

Expected (Hypothesis B): both commands succeed, no error. Record what marketplace name Claude assigns it (likely the directory name `spike-gt-slack` or whatever's in `plugin.json`'s `name`).

If resolution fails: try variations — register with a different marketplace name, use absolute path in `--channels` instead of `plugin:name@market`, etc. Record the working invocation in SPIKE-NOTES.md.

- [ ] **Step 6: Launch a fresh Claude Code session with --channels and observe.**

```bash
claude --channels plugin:gt-slack@spike-gt-slack
```

(Substitute the actual marketplace name found in Step 5.)

Expected (Hypothesis A): the assistant context shows TWO `<channel>` tags, one per emitted notification. Take a screenshot or record what shows up.

For Hypothesis C: inspect the second `<channel>` tag's meta — does the nested `attachments` array render as JSON, render as a flattened representation, or get dropped entirely? Record verbatim.

- [ ] **Step 7: Record findings and decide.**

In `/tmp/spike-gt-slack/SPIKE-NOTES.md`, document:
- Which Go MCP library worked (mcp-go, mcp-golang, or hand-rolled).
- Working `--channels plugin:NAME@MARKET` invocation.
- Whether nested meta arrays work; if not, design schema is correct.
- Any Claude Code errors or surprises.

**Go/no-go**:
- If A passes (some way to emit the notification from Go) AND B passes (some way to point Claude at the plugin): proceed to Task 2.
- If either fails fundamentally: stop, return to design (channel-server may need to be Bun/TS like Telegram).

- [ ] **Step 8: Commit spike notes back into the design spec.**

```bash
cd ~/gt/gastown/crew/cog
# Manually copy /tmp/spike-gt-slack/SPIKE-NOTES.md content into a new
# section "Spike 1 results" at the bottom of docs/design/gt-slack-channels.md
git add docs/design/gt-slack-channels.md
git commit -m "docs(slack): record Spike 1 findings (Go MCP + plugin resolution)"
git push
```

Then nuke the spike workspace: `rm -rf /tmp/spike-gt-slack`.

---

### Task 2: Spike 2 — Claude Code MCP supervision behavior

- **Hypothesis**: Claude Code auto-restarts MCP servers on process death.

**Files:** No project files; uses the same spike workspace transiently if it still exists, else a tiny new stub.

- [ ] **Step 1: Set up a simple long-lived stub.**

Reuse the Spike 1 binary if still present. Otherwise:
```bash
mkdir -p /tmp/spike-supervision
cat > /tmp/spike-supervision/server.go <<'GO'
package main

import (
	"bufio"
	"fmt"
	"os"
	"time"
)

func main() {
	pidFile := "/tmp/spike-supervision/server.pid"
	_ = os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0644)
	fmt.Fprintln(os.Stderr, "supervision spike: server alive, pid=", os.Getpid())
	// Read stdin (MCP transport is stdio); block forever.
	r := bufio.NewReader(os.Stdin)
	for {
		_, err := r.ReadString('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "supervision spike: stdin closed, exiting")
			return
		}
		time.Sleep(time.Second)
	}
}
GO
cd /tmp/spike-supervision
go mod init spike-supervision
go build -o spike-server server.go
```

- [ ] **Step 2: Wrap it as a plugin and launch Claude with --channels.**

Same plugin structure as Spike 1, pointing `command` at `/tmp/spike-supervision/spike-server`. Launch `claude --channels plugin:...`.

- [ ] **Step 3: Kill the plugin process externally.**

```bash
PID=$(cat /tmp/spike-supervision/server.pid)
kill -9 $PID
```

- [ ] **Step 4: Observe.**

Wait 10–30 seconds. Check whether `cat /tmp/spike-supervision/server.pid` shows a NEW pid, and whether `ps -p <new-pid>` is alive. Watch Claude Code's stderr or status indicators for any MCP-server-related messages.

- [ ] **Step 5: Record outcome.**

Append to `docs/design/gt-slack-channels.md` under "Spike 2 results":
- Outcome A (Claude restarted the server): no supervisor wrapper needed; proceed with the plan as written.
- Outcome B (no restart): the plan needs a new task between Task 9 and Task 10 to add a `gt slack channel-supervisor` outer process that exec's `gt slack channel-server` in a backoff loop. Stop here, append a "supervisor task spec" section to the plan, and rerun planning if the scope materially changes.

- [ ] **Step 6: Commit and clean up.**

```bash
git add docs/design/gt-slack-channels.md
git commit -m "docs(slack): record Spike 2 findings (MCP supervision)"
git push
rm -rf /tmp/spike-supervision
```

---

## Phase 2: Shared types and helpers

These tasks add the foundation both the daemon and the channel-server depend on. Test-first because the structs are wire-format and any drift breaks delivery.

---

### Task 3: Inbox event Go struct + path helpers

**Files:**
- Create: `internal/slack/inbox.go`
- Test: `internal/slack/inbox_test.go`

- [ ] **Step 1: Write the failing test for `InboxEvent` round-trip.**

```go
// internal/slack/inbox_test.go
package slack

import (
	"encoding/json"
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
		SenderLabel:        "afik_cohen",
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
}
```

- [ ] **Step 2: Run the test, verify it fails.**

```bash
cd ~/gt/gastown/crew/cog
go test -run TestInboxEventJSONRoundTrip ./internal/slack/ -v
```

Expected: FAIL with "undefined: InboxEvent".

- [ ] **Step 3: Implement `InboxEvent` and helpers.**

```go
// internal/slack/inbox.go
package slack

import (
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// InboxEvent is the wire format the daemon writes to slack_inbox/<sess>/<ts>.json
// for the channel-server to read and emit as notifications/claude/channel.
//
// This struct is shared by the daemon (writer) and the channel-server (reader).
// Any drift in field names or JSON tags breaks delivery — keep tests on the
// round-trip and don't add fields without updating both sides simultaneously.
type InboxEvent struct {
	ChatID             string `json:"chat_id"`
	Kind               string `json:"kind"` // "dm" | "channel"
	MessageTS          string `json:"message_ts"`
	TSISO              string `json:"ts_iso,omitempty"`
	ThreadTS           string `json:"thread_ts,omitempty"`
	Text               string `json:"text"`
	SenderUserID       string `json:"sender_user_id"`
	SenderLabel        string `json:"sender_label,omitempty"`
	ChannelName        string `json:"channel_name,omitempty"`
	BotMentioned       bool   `json:"bot_mentioned,omitempty"`
	AttachmentsSummary string `json:"attachments_summary,omitempty"`
}

// safeSession converts a session name (which may contain "/") into a
// filesystem-safe directory name.
func safeSession(session string) string {
	return strings.ReplaceAll(session, "/", "_")
}

// InboxDir returns the directory where the daemon writes inbox events for
// a given session.
func InboxDir(townRoot, session string) string {
	return filepath.Join(townRoot, constants.DirRuntime, "slack_inbox", safeSession(session))
}

// SubscribedBeaconPath returns the lock file path for the subscription beacon.
func SubscribedBeaconPath(townRoot, session string) string {
	return filepath.Join(InboxDir(townRoot, session), ".subscribed")
}
```

- [ ] **Step 4: Run the test, verify it passes.**

```bash
go test -run TestInboxEventJSONRoundTrip ./internal/slack/ -v
```

Expected: PASS.

- [ ] **Step 5: Add a path-helper test.**

```go
// in internal/slack/inbox_test.go
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
}
```

- [ ] **Step 6: Run all slack tests, verify pass.**

```bash
go test ./internal/slack/... -v
```

Expected: all PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/slack/inbox.go internal/slack/inbox_test.go
git commit -m "slack: InboxEvent type and path helpers for channels delivery"
```

---

### Task 4: Add `channels_enabled` field to slack config

**Files:**
- Modify: `internal/slack/config.go`
- Test: `internal/slack/config_test.go` (existing — add a case)

- [ ] **Step 1: Read the current Config struct.**

```bash
grep -n "type Config struct" ~/gt/gastown/crew/cog/internal/slack/config.go
```

Expected: shows the start of the Config struct so the next step's edit lands in the right place.

- [ ] **Step 2: Add the `ChannelsEnabled` field.**

In `internal/slack/config.go`, add to the Config struct (alongside `BotToken`, `AppToken`, etc.):

```go
	// ChannelsEnabled, when true, opts Claude Code agents into receiving
	// inbound Slack messages via Claude's notifications/claude/channel
	// mechanism instead of the legacy nudge_queue path. Non-Claude agents
	// (Codex, Gemini) always use the legacy path regardless. Defaults to
	// false during development.
	ChannelsEnabled bool `json:"channels_enabled,omitempty"`
```

- [ ] **Step 3: Write a test asserting the JSON field name and default.**

Add to `internal/slack/config_test.go`:

```go
func TestConfigChannelsEnabledDefault(t *testing.T) {
	cfg := Config{}
	if cfg.ChannelsEnabled != false {
		t.Fatalf("default ChannelsEnabled = %v, want false", cfg.ChannelsEnabled)
	}
}

func TestConfigChannelsEnabledRoundTrip(t *testing.T) {
	in := Config{
		BotToken:        "xoxb-test",
		AppToken:        "xapp-test",
		OwnerUserID:     "U0",
		ChannelsEnabled: true,
	}
	data, _ := json.Marshal(&in)
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if !out.ChannelsEnabled {
		t.Fatalf("round trip lost ChannelsEnabled")
	}
	// Sanity-check the JSON tag is what we expect.
	if !strings.Contains(string(data), `"channels_enabled":true`) {
		t.Fatalf("JSON missing channels_enabled tag: %s", string(data))
	}
}
```

If `internal/slack/config_test.go` doesn't import `encoding/json` or `strings`, add them.

- [ ] **Step 4: Run the test, verify pass.**

```bash
go test -run "ChannelsEnabled" ./internal/slack/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/slack/config.go internal/slack/config_test.go
git commit -m "slack: add channels_enabled config field"
```

---

### Task 5: Subscription beacon — flock acquire/check helpers

**Files:**
- Create: `internal/slack/beacon.go`
- Create: `internal/slack/beacon_test.go`

- [ ] **Step 1: Write the failing test for `IsSubscribed`.**

```go
// internal/slack/beacon_test.go
package slack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSubscribed_NoBeaconFile(t *testing.T) {
	town := t.TempDir()
	_ = os.MkdirAll(InboxDir(town, "test/sess"), 0o755)
	if IsSubscribed(town, "test/sess") {
		t.Fatal("IsSubscribed=true with no beacon file, want false")
	}
}

func TestIsSubscribed_BeaconExistsButNoLock(t *testing.T) {
	town := t.TempDir()
	dir := InboxDir(town, "test/sess")
	_ = os.MkdirAll(dir, 0o755)
	// Create the beacon file with no holder.
	if err := os.WriteFile(filepath.Join(dir, ".subscribed"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if IsSubscribed(town, "test/sess") {
		t.Fatal("IsSubscribed=true with unlocked beacon, want false")
	}
}

func TestIsSubscribed_LockHeld(t *testing.T) {
	town := t.TempDir()
	holder, err := AcquireSubscribed(town, "test/sess")
	if err != nil {
		t.Fatalf("AcquireSubscribed: %v", err)
	}
	defer holder.Release()

	if !IsSubscribed(town, "test/sess") {
		t.Fatal("IsSubscribed=false while another process holds the lock, want true")
	}
}

func TestAcquireSubscribed_BlockedBySecondHolder(t *testing.T) {
	town := t.TempDir()
	first, err := AcquireSubscribed(town, "test/sess")
	if err != nil {
		t.Fatalf("first AcquireSubscribed: %v", err)
	}
	defer first.Release()

	// Second non-blocking attempt should fail.
	if _, err := AcquireSubscribed(town, "test/sess"); err == nil {
		t.Fatal("second AcquireSubscribed succeeded; want error (already locked)")
	}
}
```

- [ ] **Step 2: Run, verify fail.**

```bash
go test ./internal/slack/ -run "Subscribed" -v
```

Expected: FAIL with "undefined: AcquireSubscribed", "undefined: IsSubscribed".

- [ ] **Step 3: Implement `beacon.go`.**

```go
// internal/slack/beacon.go
//
// Subscription beacon for the Claude channels delivery path.
//
// The per-session channel-server holds an exclusive flock on
// slack_inbox/<safe-session>/.subscribed for its entire lifetime. The daemon
// dispatch layer probes this lock per event: if it can acquire LOCK_EX|LOCK_NB
// → no plugin alive → fall back to nudge_queue. If the acquire fails with
// EWOULDBLOCK → plugin alive → write to slack_inbox.
//
// The lock fd is opened with O_CLOEXEC so that any subprocess the channel-server
// might spawn doesn't unintentionally inherit the lock and extend its lifetime
// past the plugin's death.
package slack

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// SubscriptionHolder represents a held flock on the subscription beacon.
// Release releases the lock and closes the fd.
type SubscriptionHolder struct {
	f *os.File
}

// Release the lock. Idempotent.
func (h *SubscriptionHolder) Release() {
	if h == nil || h.f == nil {
		return
	}
	_ = unix.Flock(int(h.f.Fd()), unix.LOCK_UN)
	_ = h.f.Close()
	h.f = nil
}

// AcquireSubscribed opens (creating if needed) the subscription beacon and
// takes an exclusive non-blocking flock. Returns an error if another process
// already holds the lock.
//
// The fd is opened with O_CLOEXEC so child processes don't inherit it.
func AcquireSubscribed(townRoot, session string) (*SubscriptionHolder, error) {
	dir := InboxDir(townRoot, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create inbox dir: %w", err)
	}
	path := SubscribedBeaconPath(townRoot, session)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open beacon: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock beacon (already held?): %w", err)
	}
	return &SubscriptionHolder{f: f}, nil
}

// IsSubscribed returns true if some process currently holds the exclusive
// flock on the session's subscription beacon. Probes by trying to acquire
// LOCK_EX|LOCK_NB and immediately releasing on success.
//
// Returns false if the file doesn't exist, can't be opened, or the lock is
// available — all of which mean "no plugin alive for this session".
func IsSubscribed(townRoot, session string) bool {
	path := SubscribedBeaconPath(townRoot, session)
	f, err := os.OpenFile(path, os.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		// Lock held by someone else.
		return true
	}
	// We got the lock, meaning no one else holds it. Release it.
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return false
}
```

- [ ] **Step 4: Add `golang.org/x/sys/unix` to go.mod if not present.**

```bash
cd ~/gt/gastown/crew/cog
go get golang.org/x/sys/unix
go mod tidy
```

Expected: no errors. (Likely already a transitive dep; this is a no-op in that case.)

- [ ] **Step 5: Run beacon tests, verify pass.**

```bash
go test ./internal/slack/ -run "Subscribed" -v
```

Expected: all four PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/slack/beacon.go internal/slack/beacon_test.go go.mod go.sum
git commit -m "slack: subscription beacon (flock + O_CLOEXEC) for channels"
```

---

## Phase 3: Channel-server plugin (gt slack channel-server)

This is the per-session MCP plugin. Building it incrementally, each step adding one capability with a test.

---

### Task 6: `gt slack channel-server` cobra subcommand skeleton

**Files:**
- Create: `internal/cmd/slack_channel_server.go`

- [ ] **Step 1: Create the subcommand stub.**

```go
// internal/cmd/slack_channel_server.go
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var slackChannelServerCmd = &cobra.Command{
	Use:    "channel-server",
	Short:  "MCP server for Claude Code channels delivery (internal)",
	Long: `Per-session MCP server launched by Claude Code via --channels.

Reads GT_SESSION env var to know its session name. Watches
<townRoot>/.runtime/slack_inbox/<session>/ for new events and emits
notifications/claude/channel for each. Exposes a 'reply' MCP tool that
writes to slack_outbox/.

Not intended for direct user invocation. Launched automatically when a
gastown session starts with --channels plugin:gt-slack@gastown.`,
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runSlackChannelServer,
}

func init() {
	slackCmd.AddCommand(slackChannelServerCmd)
}

func runSlackChannelServer(cmd *cobra.Command, _ []string) error {
	session := os.Getenv("GT_SESSION")
	if session == "" {
		return fmt.Errorf("GT_SESSION env var not set; channel-server must be launched by gt session-spawn")
	}
	fmt.Fprintf(cmd.OutOrStderr(), "slack channel-server: starting for session %q\n", session)
	// TODO Task 7+: acquire beacon, watch inbox, emit notifications, expose tools.
	return fmt.Errorf("not yet implemented")
}
```

- [ ] **Step 2: Verify the command registers.**

```bash
cd ~/gt/gastown/crew/cog
make build
./gt slack channel-server --help 2>&1 | head -10
```

Expected: prints the command help. The command appears under `gt slack`. Confirm with `./gt slack --help` showing it as a subcommand.

- [ ] **Step 3: Verify error paths.**

```bash
./gt slack channel-server
```

Expected: exits non-zero with "GT_SESSION env var not set". This confirms the command gates on the env var.

```bash
GT_SESSION=test/sess ./gt slack channel-server
```

Expected: exits with "not yet implemented" — confirms we read the env var.

- [ ] **Step 4: Commit.**

```bash
git add internal/cmd/slack_channel_server.go
git commit -m "slack: channel-server cobra subcommand skeleton"
```

---

### Task 7: Channel-server beacon acquisition + replay scan + watch loop (without MCP)

This puts the file-watching and event-processing logic in place before adding the MCP transport, so we can test it independently.

**Files:**
- Modify: `internal/cmd/slack_channel_server.go`
- Create: `internal/slack/channel_server.go` (the loop logic, separated from CLI for testability)
- Create: `internal/slack/channel_server_test.go`

- [ ] **Step 1: Write a test for the inbox processing loop.**

```go
// internal/slack/channel_server_test.go
package slack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// fakeEmitter records every emit call for assertions.
type fakeEmitter struct {
	mu     sync.Mutex
	events []InboxEvent
	failNext bool
}

func (e *fakeEmitter) Emit(ev InboxEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failNext {
		e.failNext = false
		return assertableError("emit fail")
	}
	e.events = append(e.events, ev)
	return nil
}

type assertableError string

func (a assertableError) Error() string { return string(a) }

func writeInboxFile(t *testing.T, dir string, name string, ev InboxEvent) string {
	t.Helper()
	data, err := json.Marshal(&ev)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestChannelServer_ReplayPreExistingFiles(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	dir := InboxDir(town, session)
	_ = os.MkdirAll(dir, 0o755)

	// Write 3 events out of order; lexicographic-by-name should put them
	// in the order ts=100, 200, 300 regardless of write order.
	writeInboxFile(t, dir, "300-c.json", InboxEvent{ChatID: "D3", Text: "third"})
	writeInboxFile(t, dir, "100-a.json", InboxEvent{ChatID: "D1", Text: "first"})
	writeInboxFile(t, dir, "200-b.json", InboxEvent{ChatID: "D2", Text: "second"})

	emitter := &fakeEmitter{}
	srv := NewChannelServer(town, session, emitter)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = srv.Run(ctx) }()

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		emitter.mu.Lock()
		n := len(emitter.events)
		emitter.mu.Unlock()
		if n == 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if len(emitter.events) != 3 {
		t.Fatalf("emitted %d events, want 3", len(emitter.events))
	}
	want := []string{"first", "second", "third"}
	for i, ev := range emitter.events {
		if ev.Text != want[i] {
			t.Fatalf("event[%d].Text = %q, want %q", i, ev.Text, want[i])
		}
	}

	// All inbox files should be deleted post-emit.
	leftover, _ := os.ReadDir(dir)
	jsons := 0
	for _, e := range leftover {
		if filepath.Ext(e.Name()) == ".json" {
			jsons++
		}
	}
	if jsons != 0 {
		t.Fatalf("%d .json files left after replay, want 0", jsons)
	}
}

func TestChannelServer_NewFileWhileWatching(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	dir := InboxDir(town, session)
	_ = os.MkdirAll(dir, 0o755)

	emitter := &fakeEmitter{}
	srv := NewChannelServer(town, session, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Run(ctx) }()

	// Give the watcher a moment to start.
	time.Sleep(100 * time.Millisecond)

	writeInboxFile(t, dir, "500-x.json", InboxEvent{ChatID: "DX", Text: "live"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		emitter.mu.Lock()
		n := len(emitter.events)
		emitter.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if len(emitter.events) != 1 || emitter.events[0].Text != "live" {
		t.Fatalf("got events %+v, want one [live]", emitter.events)
	}
}

func TestChannelServer_DeleteOnlyOnEmitSuccess(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	dir := InboxDir(town, session)
	_ = os.MkdirAll(dir, 0o755)

	writeInboxFile(t, dir, "1-a.json", InboxEvent{ChatID: "DA", Text: "fail-once"})

	emitter := &fakeEmitter{failNext: true}
	srv := NewChannelServer(town, session, emitter)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = srv.Run(ctx) }()

	// Wait long enough for the first failed emit + retry on next claim cycle.
	time.Sleep(1 * time.Second)
	cancel()

	// File should still be present (renamed back from .claimed) for retry.
	entries, _ := os.ReadDir(dir)
	hasJSON := false
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			hasJSON = true
		}
	}
	if !hasJSON {
		t.Fatal("inbox file deleted on emit failure; want it preserved for retry")
	}

	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	// Sort to be order-independent.
	sort.Slice(emitter.events, func(i, j int) bool { return emitter.events[i].Text < emitter.events[j].Text })
	// We expect at most 1 successful emit (the retry after failNext consumed itself).
	if len(emitter.events) > 1 {
		t.Fatalf("emitted %d events, want 0 or 1", len(emitter.events))
	}
}
```

- [ ] **Step 2: Run, verify fail.**

```bash
go test ./internal/slack/ -run "ChannelServer" -v
```

Expected: FAIL with "undefined: NewChannelServer".

- [ ] **Step 3: Implement `ChannelServer`.**

```go
// internal/slack/channel_server.go
//
// ChannelServer drives the per-session channel-server's event-processing loop:
// hold the subscription beacon, watch the inbox dir, atomically claim each
// .json file, emit it via an injected Emitter, delete on success or restore
// on failure for a future retry.
//
// The Emitter interface is the seam between this package and the MCP
// transport — production code passes an Emitter that calls
// notifications/claude/channel; tests pass a fake.
package slack

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Emitter abstracts how an InboxEvent reaches the assistant.
// Production: emits notifications/claude/channel via MCP.
// Test: records into a slice.
type Emitter interface {
	Emit(InboxEvent) error
}

// ChannelServer runs the inbox watch + claim + emit loop.
type ChannelServer struct {
	townRoot string
	session  string
	emitter  Emitter
	holder   *SubscriptionHolder
}

// NewChannelServer constructs a ChannelServer. Acquisition of the beacon
// happens in Run, not here, so tests can construct one without filesystem
// side effects beyond MkdirAll(InboxDir).
func NewChannelServer(townRoot, session string, emitter Emitter) *ChannelServer {
	return &ChannelServer{
		townRoot: townRoot,
		session:  session,
		emitter:  emitter,
	}
}

// Run blocks until ctx is cancelled or a fatal error occurs.
//
// Order:
//   1. Acquire flock on the subscription beacon.
//   2. Start fsnotify watch on the inbox dir BEFORE replay scan, so files
//      written during the transition aren't missed.
//   3. Replay scan: process all existing *.json files in FIFO order.
//   4. Steady-state: process new files as fsnotify reports them.
//   5. On ctx done: release beacon, return.
func (s *ChannelServer) Run(ctx context.Context) error {
	holder, err := AcquireSubscribed(s.townRoot, s.session)
	if err != nil {
		return fmt.Errorf("acquire subscribed beacon: %w", err)
	}
	s.holder = holder
	defer s.holder.Release()

	dir := InboxDir(s.townRoot, s.session)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify new: %w", err)
	}
	defer watcher.Close()
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("fsnotify watch %s: %w", dir, err)
	}

	// Replay AFTER watcher is registered so any file written during the
	// brief gap is caught by either the replay scan or fsnotify.
	if err := s.replay(ctx, dir); err != nil {
		return err
	}

	// Steady state.
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-watcher.Events:
			if !ok {
				return fmt.Errorf("fsnotify events channel closed")
			}
			if ev.Op&fsnotify.Create == 0 && ev.Op&fsnotify.Write == 0 {
				continue
			}
			if !strings.HasSuffix(ev.Name, ".json") {
				continue
			}
			s.processOne(ev.Name)
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "channel-server: fsnotify error: %v\n", err)
		}
	}
}

// replay drains pre-existing files in FIFO order.
func (s *ChannelServer) replay(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read inbox: %w", err)
	}
	var jsons []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		jsons = append(jsons, e.Name())
	}
	sort.Strings(jsons) // FIFO via timestamp-prefixed names.
	for _, name := range jsons {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		s.processOne(filepath.Join(dir, name))
	}
	return nil
}

// processOne handles a single inbox file: rename-claim, parse, emit, delete-on-success.
func (s *ChannelServer) processOne(path string) {
	if !strings.HasSuffix(path, ".json") {
		return
	}
	suffix := randSuffix()
	claimed := path + ".claimed." + suffix
	if err := os.Rename(path, claimed); err != nil {
		// Lost the race or file already gone — fine.
		return
	}
	data, err := os.ReadFile(claimed)
	if err != nil {
		// Best-effort restore.
		_ = os.Rename(claimed, path)
		fmt.Fprintf(os.Stderr, "channel-server: read claimed %s: %v\n", claimed, err)
		return
	}
	var ev InboxEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		// Malformed — quarantine by leaving the .claimed file in place
		// rather than restoring (so we don't loop forever on a poison file).
		fmt.Fprintf(os.Stderr, "channel-server: malformed inbox file %s: %v\n", claimed, err)
		return
	}
	if err := s.emitter.Emit(ev); err != nil {
		// Restore for future retry.
		_ = os.Rename(claimed, path)
		fmt.Fprintf(os.Stderr, "channel-server: emit failed for %s: %v (will retry)\n", path, err)
		return
	}
	_ = os.Remove(claimed)
}

func randSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Make time import used.
var _ = time.Second
```

- [ ] **Step 4: Run the new tests.**

```bash
go test ./internal/slack/ -run "ChannelServer" -v
```

Expected: all three PASS. If `TestChannelServer_DeleteOnlyOnEmitSuccess` is flaky on slow systems, increase the inner sleep.

- [ ] **Step 5: Wire the loop into the cobra command (still without MCP).**

Modify `internal/cmd/slack_channel_server.go`:

```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/slack"
)

var slackChannelServerCmd = &cobra.Command{
	Use:    "channel-server",
	Short:  "MCP server for Claude Code channels delivery (internal)",
	Long:   `...`, // unchanged
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runSlackChannelServer,
}

func init() {
	slackCmd.AddCommand(slackChannelServerCmd)
}

// stderrEmitter is a placeholder Emitter that prints events to stderr.
// Task 8 replaces this with the real MCP transport.
type stderrEmitter struct{}

func (stderrEmitter) Emit(ev slack.InboxEvent) error {
	fmt.Fprintf(os.Stderr, "channel-server: would emit notification: chat=%s text=%q\n",
		ev.ChatID, ev.Text)
	return nil
}

func runSlackChannelServer(cmd *cobra.Command, _ []string) error {
	session := os.Getenv("GT_SESSION")
	if session == "" {
		return fmt.Errorf("GT_SESSION env var not set; channel-server must be launched by gt session-spawn")
	}
	townRoot, err := findMailWorkDir()
	if err != nil {
		return fmt.Errorf("find town root: %w", err)
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		fmt.Fprintln(cmd.OutOrStderr(), "channel-server: shutdown signal")
		cancel()
	}()

	srv := slack.NewChannelServer(townRoot, session, stderrEmitter{})
	fmt.Fprintf(cmd.OutOrStderr(), "channel-server: running for session %q\n", session)
	return srv.Run(ctx)
}
```

- [ ] **Step 6: Smoke-test by hand.**

```bash
make build
mkdir -p ~/gt/.runtime/slack_inbox/manual_smoke
GT_SESSION=manual/smoke ./gt slack channel-server &
PID=$!
sleep 0.5
echo '{"chat_id":"D1","text":"hello"}' > ~/gt/.runtime/slack_inbox/manual_smoke/1-a.json
sleep 0.5
kill $PID
wait $PID
ls ~/gt/.runtime/slack_inbox/manual_smoke/
rm -rf ~/gt/.runtime/slack_inbox/manual_smoke
```

Expected: stderr shows "would emit notification: chat=D1 text=\"hello\"". The inbox dir is empty after the run (file was claimed and deleted post-emit).

- [ ] **Step 7: Commit.**

```bash
git add internal/slack/channel_server.go internal/slack/channel_server_test.go internal/cmd/slack_channel_server.go
git commit -m "slack: channel-server loop (beacon, replay, watch, claim/emit)"
```

---

### Task 8: MCP transport — emit notifications/claude/channel

This task wires the real MCP server. The exact API depends on Spike 1's outcome. The plan below assumes `github.com/mark3labs/mcp-go` v0.x with a `NewServer` + a custom notification API. If Spike 1 chose a different library, adapt the import + method names but keep the structure.

**Files:**
- Create: `internal/slack/channel_emitter_mcp.go`
- Modify: `internal/cmd/slack_channel_server.go`

- [ ] **Step 1: Add the MCP library dependency (assumed mark3labs/mcp-go).**

```bash
cd ~/gt/gastown/crew/cog
go get github.com/mark3labs/mcp-go@latest
go mod tidy
```

If Spike 1 chose a different library, substitute its module path here.

- [ ] **Step 2: Implement the MCP-backed Emitter.**

```go
// internal/slack/channel_emitter_mcp.go
//
// MCPEmitter implements Emitter by sending notifications/claude/channel via
// an MCP server's notification facility. The exact import path and method
// names below assume github.com/mark3labs/mcp-go and may need to be adjusted
// based on Spike 1 results.
package slack

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"     // <- adjust per Spike 1
	"github.com/mark3labs/mcp-go/server"  // <- adjust per Spike 1
)

// MCPEmitter holds a reference to the running MCP server so it can emit
// notifications on the same transport Claude Code is reading.
type MCPEmitter struct {
	srv *server.MCPServer
}

func NewMCPEmitter(srv *server.MCPServer) *MCPEmitter {
	return &MCPEmitter{srv: srv}
}

// Emit sends one notifications/claude/channel notification with the event's
// fields formatted into content + meta.
func (e *MCPEmitter) Emit(ev InboxEvent) error {
	meta := map[string]any{
		"chat_id":         ev.ChatID,
		"kind":            ev.Kind,
		"message_ts":      ev.MessageTS,
		"ts_iso":          ev.TSISO,
		"thread_ts":       ev.ThreadTS,
		"sender_user_id":  ev.SenderUserID,
		"sender_label":    ev.SenderLabel,
		"channel_name":    ev.ChannelName,
		"bot_mentioned":   ev.BotMentioned,
		"attachments":     ev.AttachmentsSummary,
	}
	notif := mcp.Notification{
		Method: "notifications/claude/channel",
		Params: mcp.NotificationParams{
			AdditionalFields: map[string]any{
				"content": ev.Text,
				"meta":    meta,
			},
		},
	}
	return e.srv.SendNotification(context.Background(), notif)
}
```

If the chosen library doesn't have `SendNotification`, look for `Notify`, `Notification`, or similar. The semantic is "send a JSON-RPC notification (no response expected)".

- [ ] **Step 3: Update the cobra command to spin up the MCP server with the emitter.**

In `internal/cmd/slack_channel_server.go`, replace `runSlackChannelServer`:

```go
func runSlackChannelServer(cmd *cobra.Command, _ []string) error {
	session := os.Getenv("GT_SESSION")
	if session == "" {
		return fmt.Errorf("GT_SESSION env var not set; channel-server must be launched by gt session-spawn")
	}
	townRoot, err := findMailWorkDir()
	if err != nil {
		return fmt.Errorf("find town root: %w", err)
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		cancel()
	}()

	mcpSrv := server.NewMCPServer("gt-slack", "0.1.0",
		server.WithToolCapabilities(true),
	)

	emitter := slack.NewMCPEmitter(mcpSrv)
	chanSrv := slack.NewChannelServer(townRoot, session, emitter)

	// Run the inbox loop in a goroutine; serve MCP on the main goroutine
	// (it owns stdin/stdout, which is the MCP transport).
	loopErr := make(chan error, 1)
	go func() { loopErr <- chanSrv.Run(ctx) }()

	if err := server.ServeStdio(mcpSrv); err != nil {
		cancel()
		<-loopErr
		return fmt.Errorf("mcp serve: %w", err)
	}
	cancel()
	return <-loopErr
}
```

Adjust `server.NewMCPServer`, `server.WithToolCapabilities`, `server.ServeStdio` based on Spike 1's chosen library.

- [ ] **Step 4: Build, verify it links cleanly.**

```bash
make build
```

Expected: build succeeds. If imports fail, fix import paths to match the Spike 1 library's actual API.

- [ ] **Step 5: Smoke test against a real Claude Code session.**

```bash
# Register the in-repo plugin (one-time per dev machine).
# (Plugin definition lands in Task 11; for this step, stand up a temporary
# .mcp.json that points at the just-built binary at ./gt slack channel-server.)
mkdir -p /tmp/cs-smoke/plugins/gt-slack/.claude-plugin
cat > /tmp/cs-smoke/plugins/gt-slack/.claude-plugin/plugin.json <<'JSON'
{ "name": "gt-slack", "version": "0.0.1" }
JSON
cat > /tmp/cs-smoke/plugins/gt-slack/.mcp.json <<JSON
{
  "mcpServers": {
    "gt-slack": {
      "command": "$(pwd)/gt",
      "args": ["slack", "channel-server"],
      "env": { "GT_SESSION": "manual/smoke" }
    }
  }
}
JSON
```

In Claude Code:
```
/plugin add-marketplace /tmp/cs-smoke/plugins
/plugin install gt-slack@cs-smoke
```

Then in a fresh terminal:
```bash
mkdir -p ~/gt/.runtime/slack_inbox/manual_smoke
echo '{"chat_id":"D1","kind":"dm","message_ts":"1.0","text":"smoke test","sender_label":"smoker"}' \
  > ~/gt/.runtime/slack_inbox/manual_smoke/1-a.json
claude --channels plugin:gt-slack@cs-smoke
```

Expected: in Claude Code, the assistant context should contain a `<channel source="..." chat_id="D1" sender_label="smoker">smoke test</channel>` tag (or however Claude renders it from the meta). The inbox file should be deleted from disk.

- [ ] **Step 6: Clean up + commit.**

```bash
rm -rf /tmp/cs-smoke ~/gt/.runtime/slack_inbox/manual_smoke
git add internal/slack/channel_emitter_mcp.go internal/cmd/slack_channel_server.go go.mod go.sum
git commit -m "slack: emit notifications/claude/channel via MCP transport"
```

---

### Task 9: `reply` MCP tool

**Files:**
- Modify: `internal/cmd/slack_channel_server.go`
- Modify: `internal/slack/channel_emitter_mcp.go` (or split into `channel_tools.go`)

- [ ] **Step 1: Locate the existing slack_outbox JSON shape.**

```bash
grep -n "slack_outbox\|OutboxMessage" ~/gt/gastown/crew/cog/internal/slack/publisher.go ~/gt/gastown/crew/cog/internal/cmd/slack_reply.go | head -20
```

Expected: shows the struct `OutboxMessage` (or similar) and how `gt slack reply` writes it. The plugin's `reply` tool must produce the same JSON shape.

- [ ] **Step 2: Add a helper in `internal/slack/` to write an outbox message.**

Look for an existing helper (`WriteOutbox` or similar). If absent, add one to `internal/slack/outbox_writer.go` (path TBD if not present):

```go
// WriteOutbox writes a reply for the publisher to drain. The shape MUST
// match what gt slack reply writes today; both the daemon's publisher and
// this writer share the same on-disk schema.
func WriteOutbox(townRoot string, msg OutboxMessage) error {
	dir := filepath.Join(townRoot, constants.DirRuntime, "slack_outbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(&msg, "", "  ")
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%s.json", time.Now().UnixNano(), randSuffix())
	tmp := filepath.Join(dir, name+".tmp")
	final := filepath.Join(dir, name)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}
```

If `slack_reply.go` already has this logic inline, refactor it into the helper (so the plugin can call the same helper, eliminating drift).

- [ ] **Step 3: Register the `reply` MCP tool.**

In `runSlackChannelServer`, before `server.ServeStdio`:

```go
mcpSrv.AddTool(
	mcp.NewTool("reply",
		mcp.WithDescription("Send a Slack reply to the chat that triggered this <channel> notification. "+
			"Required: chat_id from the notification meta. text is the message body. "+
			"Optional: thread_ts to thread the reply under the original message."),
		mcp.WithString("chat_id", mcp.Required(), mcp.Description("Slack channel/DM ID, from the channel notification meta.chat_id")),
		mcp.WithString("text", mcp.Required(), mcp.Description("Reply text. Plain text or Slack mrkdwn.")),
		mcp.WithString("thread_ts", mcp.Description("Thread timestamp from meta.thread_ts or meta.message_ts. Optional but recommended.")),
	),
	func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		chatID := req.Params.Arguments["chat_id"].(string)
		text := req.Params.Arguments["text"].(string)
		threadTS, _ := req.Params.Arguments["thread_ts"].(string)
		msg := slack.OutboxMessage{
			ChatID:   chatID,
			Text:     text,
			ThreadTS: threadTS,
		}
		if err := slack.WriteOutbox(townRoot, msg); err != nil {
			return nil, fmt.Errorf("write outbox: %w", err)
		}
		return mcp.NewToolResultText("queued"), nil
	},
)
```

Adjust to the actual `OutboxMessage` field names; the publisher tests will catch any mismatch.

- [ ] **Step 4: Add a test for `WriteOutbox` round-trip.**

```go
// internal/slack/outbox_writer_test.go
package slack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteOutbox_FileShape(t *testing.T) {
	town := t.TempDir()
	msg := OutboxMessage{
		ChatID:   "D1",
		Text:     "hello",
		ThreadTS: "123.456",
	}
	if err := WriteOutbox(town, msg); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(town, ".runtime", "slack_outbox")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("got %d files in outbox, want 1", len(entries))
	}
	data, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	var parsed OutboxMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ChatID != "D1" || parsed.Text != "hello" || parsed.ThreadTS != "123.456" {
		t.Fatalf("round trip mismatch: %+v", parsed)
	}
	if !strings.HasSuffix(entries[0].Name(), ".json") {
		t.Fatalf("file %q missing .json suffix", entries[0].Name())
	}
}
```

- [ ] **Step 5: Run, verify pass.**

```bash
go test ./internal/slack/ -run "Outbox" -v
```

Expected: PASS.

- [ ] **Step 6: Smoke-test reply round-trip.**

Spin up the slack daemon (`./gt slack start`), launch a Claude Code session with the plugin, ask the assistant to call the `reply` MCP tool with a known DM ID, observe Slack receives the message.

- [ ] **Step 7: Commit.**

```bash
git add internal/slack/outbox_writer.go internal/slack/outbox_writer_test.go internal/cmd/slack_channel_server.go
git commit -m "slack: reply MCP tool writes to slack_outbox"
```

---

## Phase 4: Daemon dispatch

### Task 10: Daemon EnqueueNudge — flock check + inbox write

**Files:**
- Modify: `internal/slack/daemon.go`
- Test: `internal/slack/daemon_test.go` (or new file `daemon_dispatch_test.go`)

- [ ] **Step 1: Write a test for the dispatch decision.**

```go
// internal/slack/daemon_dispatch_test.go
package slack

import (
	"os"
	"path/filepath"
	"testing"
)

// dispatchInbound chooses between slack_inbox/ (subscribed) and the legacy
// nudge.Enqueue path. We test the file-based half here; nudge fallback is
// tested via the existing nudge tests.
func TestDispatchInbound_SubscribedWritesInbox(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	holder, err := AcquireSubscribed(town, session)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()

	ev := InboxEvent{ChatID: "D1", Text: "hi"}
	written, err := writeInboxIfSubscribed(town, session, ev)
	if err != nil {
		t.Fatalf("writeInboxIfSubscribed: %v", err)
	}
	if !written {
		t.Fatal("written=false while subscribed; want true")
	}
	dir := InboxDir(town, session)
	entries, _ := os.ReadDir(dir)
	jsons := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			jsons++
		}
	}
	if jsons != 1 {
		t.Fatalf("got %d .json files, want 1", jsons)
	}
}

func TestDispatchInbound_NotSubscribedFallsThrough(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	// Don't acquire — simulating "no plugin alive".

	ev := InboxEvent{ChatID: "D1", Text: "hi"}
	written, err := writeInboxIfSubscribed(town, session, ev)
	if err != nil {
		t.Fatalf("writeInboxIfSubscribed: %v", err)
	}
	if written {
		t.Fatal("written=true while not subscribed; want false (caller falls back to nudge)")
	}
}
```

- [ ] **Step 2: Run, verify fail.**

```bash
go test ./internal/slack/ -run "DispatchInbound" -v
```

Expected: FAIL with "undefined: writeInboxIfSubscribed".

- [ ] **Step 3: Implement the dispatch helper.**

Add to `internal/slack/inbox.go`:

```go
import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// writeInboxIfSubscribed writes ev to slack_inbox/<sess>/<ts>.json IF a
// channel-server is alive (holds the subscription beacon flock).
//
// Returns (true, nil) if the event was written to the inbox.
// Returns (false, nil) if no plugin is alive — the caller should fall back
// to the legacy nudge_queue path.
// Returns (false, err) on filesystem failure.
func writeInboxIfSubscribed(townRoot, session string, ev InboxEvent) (bool, error) {
	if !IsSubscribed(townRoot, session) {
		return false, nil
	}
	dir := InboxDir(townRoot, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create inbox dir: %w", err)
	}
	data, err := json.MarshalIndent(&ev, "", "  ")
	if err != nil {
		return false, err
	}
	name := fmt.Sprintf("%d-%s.json", time.Now().UnixNano(), inboxRandSuffix())
	tmp := filepath.Join(dir, name+".tmp")
	final := filepath.Join(dir, name)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

func inboxRandSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

- [ ] **Step 4: Run, verify pass.**

```bash
go test ./internal/slack/ -run "DispatchInbound" -v
```

Expected: PASS for both tests.

- [ ] **Step 5: Wire into daemon's EnqueueNudge.**

In `internal/slack/daemon.go`, modify the `EnqueueNudge` callback (the version after the gt-zei3e fix) to dispatch based on `writeInboxIfSubscribed`:

```go
EnqueueNudge: func(address, body string) error {
	sessionName, err := ResolveAddressToSession(address)
	if err != nil {
		return fmt.Errorf("resolve address %s: %w", address, err)
	}

	// Build the InboxEvent from the inbound message context. The handler
	// has the IncomingMessage in scope where it calls EnqueueNudge — we
	// need to thread that through. For now, the body string carries the
	// pre-formatted directive used by the legacy nudge path; the channel
	// path needs structured fields, so we'll need to enhance the
	// InboundHandler.EnqueueNudge signature in step 6.
	// ... (continued in step 6)
}
```

(The current `EnqueueNudge(address, body string)` signature only carries the formatted body, not structured fields. The next step refactors it.)

- [ ] **Step 6: Refactor `InboundHandler.EnqueueNudge` to pass structured fields.**

In `internal/slack/inbound.go`, change the `EnqueueNudge` field signature from `func(address, body string) error` to `func(address string, ev InboxEvent, fallbackBody string) error`. The `fallbackBody` is the legacy formatted string, used only when the dispatch falls through to `nudge.Enqueue`.

Update the call site in `Handle()` to construct an `InboxEvent`:

```go
// In handler.Handle, where we currently call h.EnqueueNudge(address, body):
ev := InboxEvent{
	ChatID:             msg.ChatID,
	Kind:               kindString(msg.Kind), // helper: ConversationDM → "dm" etc.
	MessageTS:          msg.MessageTS,
	TSISO:              isoFromSlackTS(msg.MessageTS),
	ThreadTS:           msg.ThreadTS,
	Text:               msg.Text,
	SenderUserID:       msg.SenderUserID,
	SenderLabel:        msg.SenderName,
	ChannelName:        msg.ChannelName,
	BotMentioned:       msg.BotMentioned,
	AttachmentsSummary: summarizeAttachments(msg.Attachments, downloadedLines, metaLines),
}
if err := h.EnqueueNudge(address, ev, body); err != nil {
	h.Ephemeral(...)
	return
}
```

Add the `kindString`, `isoFromSlackTS`, `summarizeAttachments` helpers in `inbox.go` or `inbound.go`:

```go
func kindString(k ConversationKind) string {
	if k == ConversationDM {
		return "dm"
	}
	return "channel"
}

// isoFromSlackTS parses Slack's "1714510123.000200" float-string and returns
// an ISO 8601 UTC timestamp.
func isoFromSlackTS(ts string) string {
	if ts == "" {
		return ""
	}
	// Slack TS is "<unix>.<microseconds>" — split, parse, format.
	dot := strings.Index(ts, ".")
	if dot < 0 {
		return ""
	}
	secs, err := strconv.ParseInt(ts[:dot], 10, 64)
	if err != nil {
		return ""
	}
	micros, err := strconv.ParseInt(ts[dot+1:], 10, 64)
	if err != nil {
		return ""
	}
	t := time.Unix(secs, micros*1000).UTC()
	return t.Format("2006-01-02T15:04:05.000000Z")
}

func summarizeAttachments(meta []AttachmentMeta, downloaded, fallback []string) string {
	if len(meta) == 0 {
		return ""
	}
	if len(meta) == 1 {
		m := meta[0]
		return fmt.Sprintf("1 file: %s (%s, %d B)", m.Name, m.MimeType, m.Size)
	}
	return fmt.Sprintf("%d files attached", len(meta))
}
```

- [ ] **Step 7: Update the daemon's `EnqueueNudge` callback to use the new signature and `writeInboxIfSubscribed`.**

In `internal/slack/daemon.go`:

```go
EnqueueNudge: func(address string, ev InboxEvent, fallbackBody string) error {
	sessionName, err := ResolveAddressToSession(address)
	if err != nil {
		return fmt.Errorf("resolve address %s: %w", address, err)
	}

	// Channels path first.
	written, werr := writeInboxIfSubscribed(opts.TownRoot, sessionName, ev)
	if werr != nil {
		fmt.Fprintf(os.Stderr,
			"slack: inbox write failed for %s, falling back to nudge: %v\n",
			sessionName, werr)
	} else if written {
		fmt.Fprintf(os.Stderr,
			"slack: routed via channels (slack_inbox) for %s (sender %s)\n",
			sessionName, address)
		return nil
	}

	// Legacy nudge path (gt-zei3e fix path).
	if err := nudge.Enqueue(opts.TownRoot, sessionName, nudge.QueuedNudge{
		Sender:   "slack",
		Message:  fallbackBody,
		Kind:     "slack",
		Priority: nudge.PriorityUrgent,
	}); err != nil {
		return err
	}
	if _, perr := nudge.StartPoller(opts.TownRoot, sessionName); perr != nil {
		fmt.Fprintf(os.Stderr,
			"slack: enqueued for %s but failed to start poller: %v\n",
			sessionName, perr)
	}
	fmt.Fprintf(os.Stderr,
		"slack: routed via legacy nudge_queue for %s (sender %s)\n",
		sessionName, address)
	return nil
},
```

- [ ] **Step 8: Run the full test suite.**

```bash
go test ./internal/slack/... -v
```

Expected: all pass. If any existing inbound tests break due to the signature change, update them with the new shape.

- [ ] **Step 9: Commit.**

```bash
git add internal/slack/inbox.go internal/slack/daemon.go internal/slack/inbound.go internal/slack/daemon_dispatch_test.go
git commit -m "slack: dispatch inbound to channels (slack_inbox) when subscribed"
```

---

## Phase 5: Plugin definition + session-spawn integration

### Task 11: In-repo plugin definition

**Files:**
- Create: `plugins/gt-slack/.claude-plugin/plugin.json`
- Create: `plugins/gt-slack/.mcp.json`

- [ ] **Step 1: Create the plugin manifest.**

```bash
mkdir -p ~/gt/gastown/crew/cog/plugins/gt-slack/.claude-plugin
```

Create `plugins/gt-slack/.claude-plugin/plugin.json`:
```json
{
  "name": "gt-slack",
  "description": "Gas Town Slack channel for Claude Code agents — receive routed Slack DMs/mentions as channel notifications and reply via MCP tools.",
  "version": "0.1.0",
  "keywords": ["gastown", "slack", "channel", "mcp"]
}
```

Create `plugins/gt-slack/.mcp.json`:
```json
{
  "mcpServers": {
    "gt-slack": {
      "command": "gt",
      "args": ["slack", "channel-server"]
    }
  }
}
```

The plugin relies on `gt` being on PATH (it always is for gastown agents, since `make install` puts it at `~/.local/bin/gt`) and on `GT_SESSION` being in the environment (set by gt's session-spawn before launching `claude`).

- [ ] **Step 2: Verify Claude can resolve the plugin from the local marketplace.**

```bash
# In a Claude Code session:
/plugin add-marketplace ~/gt/gastown/crew/cog/plugins
/plugin install gt-slack@<marketplace-name>
```

(Marketplace name was determined during Spike 1.)

Expected: success.

- [ ] **Step 3: Commit.**

```bash
git add plugins/gt-slack/
git commit -m "slack: in-repo plugin definition for Claude channels"
```

---

### Task 12: Auto-inject `--channels` in session-spawn for Claude agents

**Files:**
- Modify: the session-spawn helper that builds the `claude` invocation. Find via:

```bash
grep -rn "RuntimeConfig\|preset.Args\|cfg.Command" ~/gt/gastown/crew/cog/internal --include="*.go" | grep -v "_test.go" | head -20
```

Likely candidates: `internal/session/lifecycle.go`, `internal/polecat/session_manager.go`, or the function that materializes a `RuntimeConfig` into an `exec.Cmd`.

- [ ] **Step 1: Identify the exact injection point.**

Trace from `claudeSonnetPreset()` (in `internal/config/cost_tier.go`) through to where `Command + Args` becomes an `exec.Cmd` argv. Note the file:line.

- [ ] **Step 2: Write a test for the injection (table-driven).**

If the injection lives in a function `BuildAgentCmd(cfg RuntimeConfig, slackCfg SlackConfig) []string`:

```go
func TestBuildAgentCmd_AutoInjectsChannelsWhenEnabled(t *testing.T) {
	rc := RuntimeConfig{Provider: "claude", Command: "claude", Args: []string{"--model", "sonnet[1m]"}}
	t.Run("enabled+claude", func(t *testing.T) {
		got := BuildAgentCmd(rc, SlackChannelOpts{Enabled: true})
		want := []string{"claude", "--model", "sonnet[1m]", "--channels", "plugin:gt-slack@gastown"}
		if !equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		got := BuildAgentCmd(rc, SlackChannelOpts{Enabled: false})
		want := []string{"claude", "--model", "sonnet[1m]"}
		if !equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("non-claude provider", func(t *testing.T) {
		rcCodex := RuntimeConfig{Provider: "codex", Command: "codex", Args: []string{}}
		got := BuildAgentCmd(rcCodex, SlackChannelOpts{Enabled: true})
		// Should NOT inject for non-Claude even when enabled.
		want := []string{"codex"}
		if !equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}
```

If the actual function name / signature differs, adapt the test. The three cases (enabled+Claude → inject, disabled → skip, non-Claude → skip) are mandatory.

- [ ] **Step 3: Run, verify fail.**

```bash
go test ./internal/<package>/ -run "AutoInjectsChannels" -v
```

- [ ] **Step 4: Implement the injection.**

```go
// In the function that builds the claude argv:
if slackOpts.Enabled && cfg.Provider == "claude" {
	args = append(args, "--channels", "plugin:gt-slack@gastown")
}
```

(Marketplace name `gastown` is the convention; if Spike 1 found a different one, substitute here.)

- [ ] **Step 5: Wire `slackOpts.Enabled` from the loaded slack.json config.**

Find where `SessionConfig` (or whatever passes session-spawn args) gets built, and propagate `cfg.ChannelsEnabled` from the slack config:

```go
slackCfg, _ := slack.LoadConfig(slack.DefaultConfigPath())
slackOpts := SlackChannelOpts{
	Enabled: slackCfg != nil && slackCfg.ChannelsEnabled,
}
```

- [ ] **Step 6: Run all tests, verify pass.**

```bash
go test ./internal/<package>/... -v
```

- [ ] **Step 7: Commit.**

```bash
git add internal/<package>/...
git commit -m "session: auto-inject --channels plugin:gt-slack for Claude agents when channels_enabled"
```

---

## Phase 6: End-to-end verification

### Task 13: Manual end-to-end test

**Files:** none — this is a manual verification step.

- [ ] **Step 1: Enable channels in slack config.**

```bash
# Edit ~/gt/config/slack.json
# Add or set:
#   "channels_enabled": true
```

Verify via:
```bash
grep channels_enabled ~/gt/config/slack.json
```

- [ ] **Step 2: Restart slack daemon with the new build.**

```bash
cd ~/gt/gastown/crew/cog
make build install
gt slack stop
sleep 2
gt slack start
gt slack status
```

Expected: daemon shows running with new pid.

- [ ] **Step 3: Restart mayor's session so it picks up `--channels` injection.**

Use `gt handoff` or manual restart per the project's session restart protocol. Confirm new mayor's tmux session was launched with `--channels plugin:gt-slack@gastown` by inspecting the `claude` process args:

```bash
ps aux | grep -E "claude.*channels.*gt-slack"
```

Expected: at least one matching process.

- [ ] **Step 4: Verify the subscription beacon is held.**

```bash
ls -la ~/gt/.runtime/slack_inbox/hq-mayor/
# Expect: .subscribed file present
lsof ~/gt/.runtime/slack_inbox/hq-mayor/.subscribed 2>&1 | head -5
# Expect: gt slack channel-server holding it
```

- [ ] **Step 5: Send a Slack DM to the bot.**

From your Slack client, DM the gt bot any message.

- [ ] **Step 6: Verify routing via channels path.**

```bash
tail -20 ~/gt/.runtime/slack.log
```

Expected: at least one `slack: routed via channels (slack_inbox) for hq-mayor` line. NO new `routed via legacy nudge_queue` lines.

Inspect mayor's tmux pane: the assistant context should show a `<channel source="slack" ...>` tag with your DM content. No tmux send-keys ghost characters; no UserPromptSubmit hook activity.

- [ ] **Step 7: Verify reply via MCP tool.**

Watch mayor process the message and respond. Confirm the response appears in Slack.

```bash
ls ~/gt/.runtime/slack_outbox/
# Should briefly show a JSON file then disappear as the publisher drains it
```

- [ ] **Step 8: Verify legacy fallback still works.**

Pick a non-Claude agent (a Codex polecat, if running) or a Claude agent without the plugin (e.g., temporarily flip `channels_enabled: false`, restart that one agent without --channels, restart slack daemon).

Send a DM destined for that agent. Verify slack.log shows `routed via legacy nudge_queue`.

- [ ] **Step 9: Document any deviations.**

If anything didn't behave as designed, append a "Phase 1 dogfood notes" section to `docs/design/gt-slack-channels.md` and commit.

---

## Phase 7: Cleanup + ship

### Task 14: Push the branch and prepare for upstream

- [ ] **Step 1: Run the full test suite once more.**

```bash
cd ~/gt/gastown/crew/cog
go test ./internal/slack/... ./internal/cmd/... -count=1 2>&1 | tail -10
```

Expected: all pass (or known-environmental failures only — `make build` -required tests are noise).

- [ ] **Step 2: Run go vet.**

```bash
go vet ./...
```

Expected: clean.

- [ ] **Step 3: Push.**

```bash
git push
```

- [ ] **Step 4: Update PR description.**

If a PR for `feat/gt-slack-router` exists, append a summary of the channels work to the description, calling out:
- The new `gt slack channel-server` command and plugin.
- The new `channels_enabled` config flag (default false).
- The flock-based dispatch decision in EnqueueNudge.
- The session-spawn auto-inject for Claude agents.

- [ ] **Step 5: Close the bead.**

```bash
bd close gt-zei3e --reason "Channels delivery shipped (commit <SHA>); legacy nudge_queue path still serves non-Claude agents and unsubscribed Claude agents."
```

(Note: gt-zei3e was already closed by the polish commit. If it's been reopened, close again. If still closed, skip.)

---

## Summary

- **Spikes resolve risks first** (Tasks 1–2): Go MCP library + plugin path resolution + supervision.
- **Foundations are test-first** (Tasks 3–5): InboxEvent, channels_enabled config, beacon helpers.
- **Plugin builds incrementally** (Tasks 6–9): cobra skeleton → loop without MCP → MCP transport → reply tool.
- **Daemon dispatch flips at the end** (Task 10): once both ends work, swap EnqueueNudge to the new path.
- **Plugin + session integration close the loop** (Tasks 11–12): in-repo manifest + auto-inject `--channels`.
- **Manual e2e proves it** (Task 13).
- **Push** (Task 14).

Each task either compiles + tests cleanly or stops the chain. No "fix later" steps. Plan assumes spike happy paths; if either spike fails, revise the plan before continuing past Task 2.
