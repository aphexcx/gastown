package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// DaemonOptions configures the daemon.
type DaemonOptions struct {
	// ConfigPath is the path to slack.json. The daemon re-reads this file
	// on every inbound event so config changes take effect without restart.
	ConfigPath string
	TownRoot   string
}

// Daemon is the top-level router process. Run blocks until ctx is cancelled.
type Daemon struct {
	opts      DaemonOptions
	client    *Client
	publisher *Publisher
	handler   *InboundHandler
	tmux      *tmux.Tmux
	startedAt time.Time
	mu        sync.Mutex

	// getConfigCached returns the current *Config, reloading from disk only
	// when slack.json's mtime has changed since the last read. This satisfies
	// the spec's "re-read on every inbound event" without a syscall storm.
	getConfigCached func() *Config
}

// NewDaemon builds a daemon from options. It validates the initial config,
// initializes the Slack client, builds the routing table from live tmux,
// and wires the inbound handler / publisher with real dependencies.
func NewDaemon(opts DaemonOptions) (*Daemon, error) {
	if opts.ConfigPath == "" {
		return nil, fmt.Errorf("daemon: config path is required")
	}
	if opts.TownRoot == "" {
		return nil, fmt.Errorf("daemon: town root is required")
	}

	// Load once up front for validation + client construction.
	initialCfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load initial config: %w", err)
	}
	if err := initialCfg.Validate(); err != nil {
		return nil, err
	}

	// mtime-cached reloader. Keeps a stored pointer to the most recently-
	// read *Config; reloads only when slack.json's mtime has changed.
	var (
		cfgMu      sync.Mutex
		cfgCache   = initialCfg
		cfgModTime time.Time
	)
	if stat, err := os.Stat(opts.ConfigPath); err == nil {
		cfgModTime = stat.ModTime()
	}
	getConfig := func() *Config {
		cfgMu.Lock()
		defer cfgMu.Unlock()
		stat, err := os.Stat(opts.ConfigPath)
		if err != nil {
			return cfgCache // best effort on stat failure
		}
		if stat.ModTime().Equal(cfgModTime) {
			return cfgCache
		}
		fresh, err := LoadConfig(opts.ConfigPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slack: config reload failed: %v (keeping last good config)\n", err)
			return cfgCache
		}
		cfgCache = fresh
		cfgModTime = stat.ModTime()
		return cfgCache
	}

	client := NewClient(initialCfg)

	// Build the routing table from live tmux sessions. This is the truth
	// source: only agents with a running tmux session are routable.
	tm := tmux.NewTmux()
	rt, err := buildRoutingTable(tm)
	if err != nil {
		return nil, fmt.Errorf("build routing table: %w", err)
	}

	outboxDir := filepath.Join(opts.TownRoot, constants.DirRuntime, "slack_outbox")
	publisher := NewPublisher(outboxDir, client, rt)
	publisher.ClearThreadStatus = func(chatID, threadTS string) {
		if err := client.SetAssistantStatus(context.Background(), chatID, threadTS, ""); err != nil {
			fmt.Fprintf(os.Stderr, "slack: clear thread status failed: %v\n", err)
		}
	}
	publisher.NudgeAgent = func(address, reason string) {
		// Best-effort: route outbound failure back to the sending agent.
		sessionName, rerr := ResolveAddressToSession(address)
		if rerr != nil {
			return
		}
		_ = nudge.Enqueue(opts.TownRoot, sessionName, nudge.QueuedNudge{
			Sender:   "slack:daemon",
			Message:  fmt.Sprintf("[slack delivery failure] %s", reason),
			Kind:     "slack",
			Priority: nudge.PriorityNormal,
		})
	}

	handler := &InboundHandler{
		GetConfig: getConfig,
		Routing:   rt,
		Ephemeral: func(chatID, userID, text string) {
			// Fire and forget: best-effort ephemeral reply via
			// chat.postEphemeral (needs channel + user).
			_ = client.PostEphemeral(context.Background(), chatID, userID, text)
		},
		EnqueueNudge: func(address string, ev InboxEvent, fallbackBody string) error {
			// Resolve address → session name, then dispatch:
			//   subscribed (channel-server alive) → write InboxEvent JSON
			//     to slack_inbox/<sess>/<ts>.json
			//   unsubscribed/error → fall back to legacy nudge_queue +
			//     StartPoller (gt-zei3e fix path)
			//
			// StartPoller is idempotent — it no-ops if a poller is already
			// alive for this session.
			sessionName, err := ResolveAddressToSession(address)
			if err != nil {
				return fmt.Errorf("resolve address %s: %w", address, err)
			}

			// Channels path first. writeInboxIfSubscribed returns
			// (true, nil) on channel write, (false, nil) if no plugin is
			// alive, (false, err) on filesystem failure. On err we log
			// and fall through to the legacy path — never lose a message.
			written, werr := writeInboxIfSubscribed(opts.TownRoot, sessionName, ev)
			if werr != nil {
				fmt.Fprintf(os.Stderr,
					"slack: inbox write failed for %s, falling back to nudge_queue: %v\n",
					sessionName, werr)
			} else if written {
				fmt.Fprintf(os.Stderr,
					"slack: routed via channels (slack_inbox) for %s (sender %s)\n",
					sessionName, address)
				return nil
			}

			// Legacy path (non-Claude agents, Claude without --channels
			// enrolled, or Claude with the plugin currently dead).
			// Existing gt-zei3e fix: also start a poller so the queue
			// actually drains for idle agents.
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
					"slack: enqueued for %s but failed to start poller "+
						"(message will deliver on next keystroke): %v\n",
					sessionName, perr)
			}
			fmt.Fprintf(os.Stderr,
				"slack: routed via legacy nudge_queue for %s (sender %s)\n",
				sessionName, address)
			return nil
		},
		ResolveSession: ResolveAddressToSession,
		CheckSession:   tm.HasSession,
		DownloadAttachment: func(ctx context.Context, m AttachmentMeta) (string, error) {
			return DownloadInboundAttachment(ctx, client, opts.TownRoot, m)
		},
		SetThreadStatus: func(chatID, threadTS, status string) {
			if err := client.SetAssistantStatus(context.Background(), chatID, threadTS, status); err != nil {
				fmt.Fprintf(os.Stderr, "slack: set thread status failed: %v\n", err)
			}
		},
		CanAccessConversation: newMembershipCache(client).check,
	}

	return &Daemon{
		opts:            opts,
		client:          client,
		publisher:       publisher,
		handler:         handler,
		tmux:            tm,
		getConfigCached: getConfig,
	}, nil
}

// Run starts Socket Mode and the publisher and blocks until ctx is cancelled
// or a fatal error occurs.
func (d *Daemon) Run(ctx context.Context) error {
	d.mu.Lock()
	d.startedAt = time.Now()
	d.mu.Unlock()

	// Verify auth and grab our own bot user ID for echo filtering.
	botID, err := d.client.AuthTest(ctx)
	if err != nil {
		return fmt.Errorf("slack auth failed: %w", err)
	}
	d.handler.BotUserID = botID

	// Write an initial status snapshot so `gt slack status` works the
	// moment the PID file exists.
	d.writeStatusSnapshot("connecting")

	// Start publisher goroutine with a panic-recovery wrapper so a
	// goroutine crash surfaces as an error instead of killing the process.
	pubDone := make(chan error, 1)
	go d.runGoroutine("publisher", pubDone, func() error {
		return d.publisher.Run(ctx)
	})

	// Start Socket Mode client. slack-go's socketmode.Client needs its own
	// wrapping slack.Client configured with the app token.
	smInitCfg := d.getConfigCached()
	api := slackgo.New(
		smInitCfg.BotToken,
		slackgo.OptionAppLevelToken(smInitCfg.AppToken),
	)
	smClient := socketmode.New(api)

	smDone := make(chan error, 1)
	go d.runGoroutine("socketmode", smDone, func() error {
		return smClient.RunContext(ctx)
	})

	// Event dispatch loop (also panic-recovering).
	dispDone := make(chan error, 1)
	go d.runGoroutine("dispatcher", dispDone, func() error {
		d.dispatchSocketModeEvents(ctx, smClient)
		return nil
	})

	// Periodic status snapshot writer.
	statusTicker := time.NewTicker(5 * time.Second)
	defer statusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.writeStatusSnapshot("disconnected")
			// Best-effort: remove the status file on clean shutdown so
			// `gt slack status` doesn't report stale data when the
			// daemon is gone. The PID file is removed by the command
			// layer (slack_daemon.go).
			_ = os.Remove(StatusPath(d.opts.TownRoot))
			return nil
		case <-statusTicker.C:
			d.writeStatusSnapshot("connected")
		case err := <-pubDone:
			return fmt.Errorf("publisher exited: %w", err)
		case err := <-smDone:
			return fmt.Errorf("socket mode exited: %w", err)
		case err := <-dispDone:
			return fmt.Errorf("dispatcher exited: %w", err)
		}
	}
}

// runGoroutine wraps fn with a panic recovery that logs and surfaces the
// panic as an error on the done channel. Socket Mode reconnection is
// already handled by slack-go; this recovery is for unexpected panics in
// our own code that would otherwise kill the whole process.
func (d *Daemon) runGoroutine(name string, done chan<- error, fn func() error) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("goroutine %s panicked: %v", name, r)
			fmt.Fprintf(os.Stderr, "slack: %v\n", err)
			done <- err
		}
	}()
	done <- fn()
}

func (d *Daemon) dispatchSocketModeEvents(ctx context.Context, sm *socketmode.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sm.Events:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "slack: event type=%s\n", ev.Type)
			switch ev.Type {
			case socketmode.EventTypeEventsAPI:
				payload, ok := ev.Data.(slackevents.EventsAPIEvent)
				if !ok {
					fmt.Fprintf(os.Stderr, "slack: EventsAPI data assertion failed: %T\n", ev.Data)
					continue
				}
				sm.Ack(*ev.Request)
				fmt.Fprintf(os.Stderr, "slack: EventsAPI callback=%s inner=%s\n",
					payload.Type, payload.InnerEvent.Type)
				d.onEventsAPI(ctx, payload)
			case socketmode.EventTypeConnected:
				fmt.Fprintf(os.Stderr, "slack: Socket Mode connected\n")
			case socketmode.EventTypeConnecting:
				fmt.Fprintf(os.Stderr, "slack: Socket Mode connecting...\n")
			case socketmode.EventTypeConnectionError:
				fmt.Fprintf(os.Stderr, "slack: Socket Mode connection error\n")
			case socketmode.EventTypeDisconnect:
				fmt.Fprintf(os.Stderr, "slack: Socket Mode disconnected\n")
			default:
				fmt.Fprintf(os.Stderr, "slack: unhandled event type: %s\n", ev.Type)
			}
		}
	}
}

func (d *Daemon) onEventsAPI(ctx context.Context, payload slackevents.EventsAPIEvent) {
	if payload.Type != slackevents.CallbackEvent {
		return
	}
	inner := payload.InnerEvent
	switch e := inner.Data.(type) {
	case *slackevents.AppMentionEvent:
		// AppMention events are, by definition, bot-mentioned.
		d.handler.Handle(ctx, IncomingMessage{
			SenderUserID: e.User,
			Kind:         classifyChannel(e.Channel),
			ChatID:       e.Channel,
			Text:         e.Text,
			ThreadTS:     e.ThreadTimeStamp,
			MessageTS:    e.TimeStamp,
			Attachments:  extractAttachmentsFromSlackFiles(e.Files),
			BotMentioned: true,
		})
	case *slackevents.MessageEvent:
		// Ignore non-user message subtypes (joins, edits, etc.).
		if e.SubType != "" && e.SubType != "thread_broadcast" && e.SubType != "file_share" {
			return
		}
		if e.BotID != "" {
			return
		}
		// MessageEvent doesn't have a Files field directly. For file_share
		// messages, files arrive in the embedded Message.Files (slack.Msg).
		var attachments []AttachmentMeta
		if e.SubType == "file_share" && e.Message != nil {
			attachments = extractAttachmentsFromSlackFiles(e.Message.Files)
		}
		// Detect both bare <@ID> and labeled <@ID|name> forms.
		botMentioned := d.handler.BotUserID != "" &&
			(strings.Contains(e.Text, "<@"+d.handler.BotUserID+">") ||
				strings.Contains(e.Text, "<@"+d.handler.BotUserID+"|"))
		d.handler.Handle(ctx, IncomingMessage{
			SenderUserID: e.User,
			Kind:         classifyChannel(e.Channel),
			ChatID:       e.Channel,
			Text:         e.Text,
			ThreadTS:     e.ThreadTimeStamp,
			MessageTS:    e.TimeStamp,
			Attachments:  attachments,
			BotMentioned: botMentioned,
		})
	}
}

// extractAttachmentsFromSlackFiles converts slack.File entries (used by both
// AppMentionEvent.Files and slack.Msg.Files) into our internal AttachmentMeta
// type. Nil input yields nil output.
func extractAttachmentsFromSlackFiles(files []slackgo.File) []AttachmentMeta {
	if len(files) == 0 {
		return nil
	}
	out := make([]AttachmentMeta, 0, len(files))
	for _, f := range files {
		out = append(out, AttachmentMeta{
			ID:         f.ID,
			Name:       f.Name,
			MimeType:   f.Mimetype,
			Size:       int64(f.Size),
			URLPrivate: f.URLPrivateDownload,
		})
	}
	return out
}

func classifyChannel(chatID string) ConversationKind {
	if len(chatID) > 0 && chatID[0] == 'D' {
		return ConversationDM
	}
	return ConversationChannel
}

// buildRoutingTable snapshots the currently-live agent roster into a
// name -> address map. The truth source is the live tmux session list:
// for each session the prefix registry recognizes, we parse it into an
// AgentIdentity and key the table by the agent's display name.
//
// Using tmux as the source of truth (rather than a static config) means:
//   - the routing table matches reality — only live agents are routable
//   - no separate registration step is needed — spawning a session registers
//     the agent for free, killing it removes the entry on the next rebuild
//
// We rebuild on daemon startup; v1.1 can refresh periodically or on signals.
func buildRoutingTable(tm *tmux.Tmux) (RoutingTable, error) {
	sessions, err := tm.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("tmux list sessions: %w", err)
	}
	rt := RoutingTable{}
	for _, s := range sessions {
		id, err := session.ParseSessionName(s)
		if err != nil {
			// Not a gas-town session (the default tmux session, an
			// unrelated user session, etc.). Skip silently.
			continue
		}
		displayName := displayNameFor(id)
		if displayName == "" {
			continue
		}
		addr := id.Address()
		if addr == "" || addr == "overseer" {
			continue
		}
		key := strings.ToLower(displayName)
		if existing, exists := rt[key]; exists {
			fmt.Fprintf(os.Stderr, "slack: WARNING: duplicate display name %q — "+
				"@%s routes to %s (first seen), ignoring %s. "+
				"Rename one agent or use different crew names to resolve.\n",
				displayName, displayName, existing, addr)
			continue
		}
		rt[key] = addr
	}
	return rt, nil
}

// displayNameFor returns the Slack-facing @name for an agent identity.
// Mayor and Deacon use their role names; everyone else uses their Name field
// (crew/polecat/dog name). Witnesses and refineries are per-rig, so they're
// keyed as "<rig>-witness" / "<rig>-refinery" to avoid collisions.
func displayNameFor(id *session.AgentIdentity) string {
	switch id.Role {
	case session.RoleMayor:
		return "mayor"
	case session.RoleDeacon:
		return "deacon"
	case session.RoleWitness:
		if id.Rig == "" {
			return ""
		}
		return id.Rig + "-witness"
	case session.RoleRefinery:
		if id.Rig == "" {
			return ""
		}
		return id.Rig + "-refinery"
	case session.RoleCrew, session.RolePolecat, session.RoleDog:
		return id.Name
	default:
		return ""
	}
}

// StartedAt returns when Run began — for `gt slack status`.
func (d *Daemon) StartedAt() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.startedAt
}

// Publisher exposes the publisher for status reporting.
func (d *Daemon) Publisher() *Publisher { return d.publisher }

// ---- Status snapshot ---------------------------------------------------

// StatusSnapshot is the JSON blob the daemon periodically writes to
// `<townRoot>/.runtime/slack-status.json`. `gt slack status` reads this
// file directly rather than IPC'ing with the daemon.
type StatusSnapshot struct {
	PID           int               `json:"pid"`
	StartedAt     time.Time         `json:"started_at"`
	LastInboundAt time.Time         `json:"last_inbound_at,omitempty"`
	LastPostAt    time.Time         `json:"last_post_at,omitempty"`
	Pending       int64             `json:"pending"`
	Failed        int64             `json:"failed"`
	Routing       map[string]string `json:"routing"`
	Channels      map[string]bool   `json:"channels"`   // enabled channels only
	Connection    string            `json:"connection"`  // "connected" | "disconnected"
}

// writeStatusSnapshot emits the current daemon state to slack-status.json.
// Called periodically and on shutdown. Uses atomic tmp+rename.
func (d *Daemon) writeStatusSnapshot(connection string) {
	snap := StatusSnapshot{
		PID:        os.Getpid(),
		StartedAt:  d.StartedAt(),
		LastPostAt: d.publisher.LastPost(),
		Pending:    d.publisher.PendingCount(),
		Failed:     d.publisher.FailedCount(),
		Routing:    map[string]string(d.handler.Routing),
		Connection: connection,
	}
	// Snapshot current enabled channels from the live config reloader.
	if cfg := d.getConfigCached(); cfg != nil {
		snap.Channels = map[string]bool{}
		for id, ch := range cfg.Channels {
			if ch.Enabled {
				snap.Channels[id] = true
			}
		}
	}

	path := filepath.Join(d.opts.TownRoot, constants.DirRuntime, "slack-status.json")
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// StatusPath returns the full status-file path for a given town root.
// Exported so `gt slack status` can find it without reconstructing it.
func StatusPath(townRoot string) string {
	return filepath.Join(townRoot, constants.DirRuntime, "slack-status.json")
}

// LoadStatusSnapshot reads a status snapshot from disk. Missing file returns
// os.ErrNotExist wrapped — callers can distinguish "not running" from "parse
// error".
func LoadStatusSnapshot(path string) (*StatusSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s StatusSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse status file: %w", err)
	}
	return &s, nil
}

// membershipCache memoizes conversations.info membership checks used by the
// privacy gate. Positive results (bot IS a member) are stable and cached
// for 5 min. Negative results get a shorter TTL so access changes propagate
// quickly. On API errors we fail closed — returning no-access — but do not
// cache the failure, so the next event retries.
type membershipCache struct {
	client      *Client
	mu          sync.Mutex
	entries     map[string]membershipEntry
	positiveTTL time.Duration
	negativeTTL time.Duration
}

type membershipEntry struct {
	ok     bool
	expiry time.Time
}

func newMembershipCache(c *Client) *membershipCache {
	return &membershipCache{
		client:      c,
		entries:     map[string]membershipEntry{},
		positiveTTL: 5 * time.Minute,
		negativeTTL: 30 * time.Second,
	}
}

// check returns whether the bot can access chatID and a human-readable reason
// for the result. The reason is meant for log lines so operators can tell
// "Slack returned channel_not_found" from "API error, failing closed" without
// digging into stderr context.
func (m *membershipCache) check(chatID string) (bool, string) {
	m.mu.Lock()
	if e, ok := m.entries[chatID]; ok && time.Now().Before(e.expiry) {
		m.mu.Unlock()
		if e.ok {
			return true, "cached_ok"
		}
		return false, "cached_channel_not_found"
	}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	allowed, err := m.client.CanAccessConversation(ctx, chatID)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"slack: membership check failed for %s (failing closed, not caching): %v\n",
			chatID, err)
		return false, fmt.Sprintf("api_error: %v", err)
	}

	ttl := m.positiveTTL
	reason := "ok"
	if !allowed {
		ttl = m.negativeTTL
		reason = "channel_not_found"
	}
	m.mu.Lock()
	m.entries[chatID] = membershipEntry{ok: allowed, expiry: time.Now().Add(ttl)}
	m.mu.Unlock()
	return allowed, reason
}
