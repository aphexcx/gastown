package slack

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeDeps captures the calls the inbound handler makes on its dependencies.
type fakeDeps struct {
	sentToSlack      []fakeEphemeral // ephemeral replies (channel + user + text)
	enqueued         []fakeEnqueue   // successful enqueues
	enqueueErr       error           // simulated enqueue failure
	resolveErrReturn error           // simulated resolve failure
	sessionAlive     bool            // controls CheckSession result
	sessionErr       error           // simulated tmux check failure
}

type fakeEphemeral struct {
	chatID string
	userID string
	text   string
}

type fakeEnqueue struct {
	session string
	body    string
}

func (f *fakeDeps) ephemeral(chatID, userID, text string) {
	f.sentToSlack = append(f.sentToSlack, fakeEphemeral{chatID: chatID, userID: userID, text: text})
}
func (f *fakeDeps) enqueue(session, body string) error {
	if f.enqueueErr != nil {
		return f.enqueueErr
	}
	f.enqueued = append(f.enqueued, fakeEnqueue{session: session, body: body})
	return nil
}
func (f *fakeDeps) resolveSession(address string) (string, error) {
	if f.resolveErrReturn != nil {
		return "", f.resolveErrReturn
	}
	// Pretend every address maps to "session-" + address.
	return "session-" + address, nil
}
func (f *fakeDeps) checkSession(sessionName string) (bool, error) {
	if f.sessionErr != nil {
		return false, f.sessionErr
	}
	return f.sessionAlive, nil
}

func testHandler(t *testing.T, cfg *Config, rt RoutingTable, deps *fakeDeps) *InboundHandler {
	// Default: pretend every resolved session is alive unless a test overrides it.
	if !deps.sessionAlive && deps.sessionErr == nil {
		deps.sessionAlive = true
	}
	return &InboundHandler{
		GetConfig:      func() *Config { return cfg },
		Routing:        rt,
		BotUserID:      "BOTID",
		Ephemeral:      deps.ephemeral,
		EnqueueNudge:   deps.enqueue,
		ResolveSession: deps.resolveSession,
		CheckSession:   deps.checkSession,
	}
}

func TestInbound_DropsNonOwner(t *testing.T) {
	cfg := &Config{OwnerUserID: "UOWNER"}
	rt := RoutingTable{"cody": "houmanoids/crew/cody"}
	deps := &fakeDeps{}
	h := testHandler(t, cfg, rt, deps)

	h.Handle(context.Background(), IncomingMessage{
		SenderUserID: "USTRANGER",
		Kind:         ConversationChannel,
		ChatID:       "C1",
		Text:         "@cody hi",
	})
	require.Empty(t, deps.enqueued)
	require.Empty(t, deps.sentToSlack)
}

func TestInbound_DropsEcho(t *testing.T) {
	cfg := &Config{OwnerUserID: "UOWNER"}
	rt := RoutingTable{"cody": "houmanoids/crew/cody"}
	deps := &fakeDeps{}
	h := testHandler(t, cfg, rt, deps)

	h.Handle(context.Background(), IncomingMessage{
		SenderUserID: "BOTID",
		Kind:         ConversationChannel,
		ChatID:       "C1",
		Text:         "@cody",
	})
	require.Empty(t, deps.enqueued)
	require.Empty(t, deps.sentToSlack)
}

func TestInbound_ChannelRequiresOptIn(t *testing.T) {
	cfg := &Config{OwnerUserID: "UOWNER"}
	rt := RoutingTable{"cody": "houmanoids/crew/cody"}
	deps := &fakeDeps{}
	h := testHandler(t, cfg, rt, deps)

	h.Handle(context.Background(), IncomingMessage{
		SenderUserID: "UOWNER",
		Kind:         ConversationChannel,
		ChatID:       "C1", // not opted in
		Text:         "@cody",
	})
	require.Empty(t, deps.enqueued)
	require.Empty(t, deps.sentToSlack)
}

func TestInbound_UnknownMention(t *testing.T) {
	cfg := &Config{
		OwnerUserID: "UOWNER",
		Channels:    map[string]ChannelConfig{"C1": {Enabled: true}},
	}
	rt := RoutingTable{"cody": "houmanoids/crew/cody"}
	deps := &fakeDeps{}
	h := testHandler(t, cfg, rt, deps)

	h.Handle(context.Background(), IncomingMessage{
		SenderUserID: "UOWNER",
		Kind:         ConversationChannel,
		ChatID:       "C1",
		Text:         "@ghost help",
	})
	require.Empty(t, deps.enqueued)
	require.Len(t, deps.sentToSlack, 1)
	require.Equal(t, "C1", deps.sentToSlack[0].chatID)
	require.Equal(t, "UOWNER", deps.sentToSlack[0].userID)
	require.Contains(t, deps.sentToSlack[0].text, "Unknown agent")
	require.Contains(t, deps.sentToSlack[0].text, "cody")
}

func TestInbound_DMWithoutMentionUsesDefault(t *testing.T) {
	cfg := &Config{
		OwnerUserID:   "UOWNER",
		DefaultTarget: "mayor/",
	}
	rt := RoutingTable{"cody": "houmanoids/crew/cody"}
	deps := &fakeDeps{}
	h := testHandler(t, cfg, rt, deps)

	h.Handle(context.Background(), IncomingMessage{
		SenderUserID: "UOWNER",
		Kind:         ConversationDM,
		ChatID:       "D1",
		Text:         "hello agent",
	})
	require.Len(t, deps.enqueued, 1)
	require.Equal(t, "session-mayor/", deps.enqueued[0].session)
}

func TestInbound_ChannelWithoutMentionAsksForOne(t *testing.T) {
	cfg := &Config{
		OwnerUserID:   "UOWNER",
		DefaultTarget: "mayor/", // should NOT apply to channels
		Channels:      map[string]ChannelConfig{"C1": {Enabled: true}},
	}
	rt := RoutingTable{"cody": "houmanoids/crew/cody"}
	deps := &fakeDeps{}
	h := testHandler(t, cfg, rt, deps)

	h.Handle(context.Background(), IncomingMessage{
		SenderUserID: "UOWNER",
		Kind:         ConversationChannel,
		ChatID:       "C1",
		Text:         "what's up team",
	})
	require.Empty(t, deps.enqueued)
	require.Len(t, deps.sentToSlack, 1)
	require.Contains(t, deps.sentToSlack[0].text, "@agent")
}

func TestInbound_EnqueueFailureSurfaces(t *testing.T) {
	cfg := &Config{
		OwnerUserID: "UOWNER",
		Channels:    map[string]ChannelConfig{"C1": {Enabled: true}},
	}
	rt := RoutingTable{"cody": "houmanoids/crew/cody"}
	deps := &fakeDeps{enqueueErr: errors.New("queue full")}
	h := testHandler(t, cfg, rt, deps)

	h.Handle(context.Background(), IncomingMessage{
		SenderUserID: "UOWNER",
		Kind:         ConversationChannel,
		ChatID:       "C1",
		Text:         "@cody help",
	})
	require.Empty(t, deps.enqueued)
	require.Len(t, deps.sentToSlack, 1)
	require.Contains(t, deps.sentToSlack[0].text, "Couldn't deliver")
	require.Contains(t, deps.sentToSlack[0].text, "queue full")
}

func TestInbound_ResolveFailureSurfaces(t *testing.T) {
	cfg := &Config{
		OwnerUserID: "UOWNER",
		Channels:    map[string]ChannelConfig{"C1": {Enabled: true}},
	}
	rt := RoutingTable{"cody": "houmanoids/crew/cody"}
	deps := &fakeDeps{resolveErrReturn: errors.New("bad address")}
	h := testHandler(t, cfg, rt, deps)

	h.Handle(context.Background(), IncomingMessage{
		SenderUserID: "UOWNER",
		Kind:         ConversationChannel,
		ChatID:       "C1",
		Text:         "@cody help",
	})
	require.Empty(t, deps.enqueued)
	require.Len(t, deps.sentToSlack, 1)
	require.Contains(t, deps.sentToSlack[0].text, "bad address")
}

func TestInbound_DeadSessionSurfaces(t *testing.T) {
	// Address resolves fine, but no live tmux session exists — spec requires
	// ephemeral reply + abort, NOT a silent enqueue into a dead queue.
	cfg := &Config{
		OwnerUserID: "UOWNER",
		Channels:    map[string]ChannelConfig{"C1": {Enabled: true}},
	}
	rt := RoutingTable{"cody": "houmanoids/crew/cody"}
	deps := &fakeDeps{sessionAlive: false}
	// Build handler directly (not via testHandler) because testHandler
	// defaults sessionAlive to true when it's the zero value.
	h := &InboundHandler{
		GetConfig:      func() *Config { return cfg },
		Routing:        rt,
		BotUserID:      "BOTID",
		Ephemeral:      deps.ephemeral,
		EnqueueNudge:   deps.enqueue,
		ResolveSession: deps.resolveSession,
		CheckSession:   deps.checkSession,
	}

	h.Handle(context.Background(), IncomingMessage{
		SenderUserID: "UOWNER",
		Kind:         ConversationChannel,
		ChatID:       "C1",
		Text:         "@cody help",
	})
	require.Empty(t, deps.enqueued)
	require.Len(t, deps.sentToSlack, 1)
	require.Contains(t, deps.sentToSlack[0].text, "no active session")
}

func TestInbound_ConfigReRead(t *testing.T) {
	// GetConfig is a func, so a config change mid-flight takes effect on the
	// next Handle without restarting the handler. This satisfies the spec's
	// "re-read config on every inbound event" requirement.
	rt := RoutingTable{"cody": "houmanoids/crew/cody"}
	deps := &fakeDeps{}

	var current *Config
	h := &InboundHandler{
		GetConfig:      func() *Config { return current },
		Routing:        rt,
		BotUserID:      "BOTID",
		Ephemeral:      deps.ephemeral,
		EnqueueNudge:   deps.enqueue,
		ResolveSession: deps.resolveSession,
		CheckSession:   func(string) (bool, error) { return true, nil },
	}

	// First: channel NOT opted in — message gets dropped silently.
	current = &Config{OwnerUserID: "UOWNER"}
	h.Handle(context.Background(), IncomingMessage{
		SenderUserID: "UOWNER",
		Kind:         ConversationChannel,
		ChatID:       "C1",
		Text:         "@cody help",
	})
	require.Empty(t, deps.enqueued)

	// Now: opt channel in, same handler, no restart — next message goes through.
	current = &Config{
		OwnerUserID: "UOWNER",
		Channels:    map[string]ChannelConfig{"C1": {Enabled: true}},
	}
	h.Handle(context.Background(), IncomingMessage{
		SenderUserID: "UOWNER",
		Kind:         ConversationChannel,
		ChatID:       "C1",
		Text:         "@cody help",
	})
	require.Len(t, deps.enqueued, 1)
}
