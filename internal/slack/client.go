package slack

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	slackgo "github.com/slack-go/slack"
)

// Client is a minimal Slack client facade used by the router. Keep the
// surface small so the rest of the package doesn't import slack-go directly.
type Client struct {
	api      *slackgo.Client
	botToken string
}

// NewClient constructs a Client using the bot token from cfg. Socket Mode
// uses its own connection setup (see inbound.go) — this client is for the
// Web API side: posting, uploading, fetching.
func NewClient(cfg *Config) *Client {
	return &Client{
		api:      slackgo.New(cfg.BotToken),
		botToken: cfg.BotToken,
	}
}

// PostMessageArgs is the input to PostMessage.
type PostMessageArgs struct {
	ChatID   string
	Text     string
	ThreadTS string
	Username string // display name via chat:write.customize
}

// SetAssistantStatus posts a lightweight "thinking" indicator in the thread
// via assistant.threads.setStatus. Pass empty status to clear the indicator
// after the agent has replied. Requires the 'assistant:write' scope and
// Assistant-mode enabled on the Slack app. Best-effort: a missing scope or
// the API being unavailable should not block message delivery — callers
// log and continue.
func (c *Client) SetAssistantStatus(ctx context.Context, chatID, threadTS, status string) error {
	if chatID == "" || threadTS == "" {
		return nil // no thread context — nothing to set status on
	}
	// Slack has two indicator surfaces:
	//   - Status: shown below the input box (single string).
	//   - LoadingMessages: shown inline in the chatlog (cycles). If empty,
	//     Slack falls back to a generic "Analyzing..." placeholder.
	// We populate both with the same phrase so the inline indicator
	// reflects the current activity instead of the generic default.
	params := slackgo.AssistantThreadsSetStatusParameters{
		ChannelID: chatID,
		ThreadTS:  threadTS,
		Status:    status, // empty string clears the footer indicator
	}
	if status != "" {
		params.LoadingMessages = []string{status}
	}
	if err := c.api.SetAssistantThreadsStatusContext(ctx, params); err != nil {
		return classifySlackError(err)
	}
	return nil
}

// PostEphemeral posts a user-visible warning via chat.postEphemeral. Only
// the target user sees it. Used for inbound failure notices ("unknown
// agent", "queue full", etc.) so they don't spam the channel for everyone.
func (c *Client) PostEphemeral(ctx context.Context, chatID, userID, text string) error {
	_, err := c.api.PostEphemeralContext(ctx, chatID, userID,
		slackgo.MsgOptionText(text, false),
	)
	if err != nil {
		return classifySlackError(err)
	}
	return nil
}

// PostMessage posts a text message to Slack. Returns the posted message
// timestamp on success. Network/5xx errors are returned wrapped in
// ErrTransient; anything else (auth, channel_not_found, etc.) as ErrPermanent.
func (c *Client) PostMessage(ctx context.Context, args PostMessageArgs) (string, error) {
	opts := []slackgo.MsgOption{
		slackgo.MsgOptionText(args.Text, false),
		slackgo.MsgOptionUsername(args.Username),
		slackgo.MsgOptionAsUser(false), // required for chat:write.customize
	}
	if args.ThreadTS != "" {
		opts = append(opts, slackgo.MsgOptionTS(args.ThreadTS))
	}

	_, ts, err := c.api.PostMessageContext(ctx, args.ChatID, opts...)
	if err != nil {
		return "", classifySlackError(err)
	}
	return ts, nil
}

// CanAccessConversation returns true if the bot is confirmed to have
// access to this conversation, false if Slack says the bot is NOT a
// member (channel_not_found), and an error for any other case (transient
// network failure, unknown API error). Callers decide how to treat the
// error case — for the privacy gate we fail closed (treat unknown as
// no-access) to avoid leaking events on API hiccups.
//
// This is the documented pattern from Slack's assistant_thread_started
// guidance: apps must call conversations.info to verify access, because
// Agent/Assistant mode delivers events for conversations the bot isn't
// formally in.
func (c *Client) CanAccessConversation(ctx context.Context, chatID string) (bool, error) {
	_, err := c.api.GetConversationInfoContext(ctx, &slackgo.GetConversationInfoInput{
		ChannelID: chatID,
	})
	if err == nil {
		return true, nil
	}
	// "channel_not_found" is Slack's way of saying the bot isn't a member
	// and shouldn't see this conversation. Definitively NOT accessible.
	if strings.Contains(err.Error(), "channel_not_found") {
		return false, nil
	}
	// Anything else (5xx, timeout, rate limit, unknown API code) is
	// ambiguous — return the error so the caller decides.
	return false, err
}

// UploadFile uploads a single local file to a channel (optionally threaded).
// Uses the slack-go v0.21 UploadFileContext API (files.getUploadURLExternal
// + files.completeUploadExternal under the hood).
func (c *Client) UploadFile(ctx context.Context, chatID, threadTS, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w: stat %s: %v", ErrPermanent, path, err)
	}

	params := slackgo.UploadFileParameters{
		File:            path,
		Filename:        fileBaseName(path),
		FileSize:        int(fi.Size()),
		Channel:         chatID,
		ThreadTimestamp: threadTS,
	}
	_, err = c.api.UploadFileContext(ctx, params)
	if err != nil {
		return classifySlackError(err)
	}
	return nil
}

// DownloadFile fetches a Slack file URL using bot token auth and writes it
// to destPath. Slack requires the bot token as Authorization: Bearer.
func (c *Client) DownloadFile(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrPermanent, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.botToken)

	httpClient := &http.Client{Timeout: 60 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: http get: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: slack file download status %d", ErrTransient, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: slack file download status %d", ErrPermanent, resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("%w: create %s: %v", ErrPermanent, destPath, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("%w: copy download: %v", ErrTransient, err)
	}
	return nil
}

// AuthTest validates the bot token and returns the bot's own user ID — used
// on daemon startup to populate the echo filter and verify auth works.
func (c *Client) AuthTest(ctx context.Context) (botUserID string, err error) {
	resp, err := c.api.AuthTestContext(ctx)
	if err != nil {
		return "", classifySlackError(err)
	}
	return resp.UserID, nil
}

// classifySlackError maps slack-go errors into ErrTransient / ErrPermanent.
func classifySlackError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch msg {
	case "channel_not_found", "not_in_channel", "is_archived",
		"invalid_auth", "not_authed", "token_revoked", "account_inactive",
		"missing_scope", "file_not_found", "invalid_file":
		return fmt.Errorf("%w: %s", ErrPermanent, msg)
	}
	return fmt.Errorf("%w: %s", ErrTransient, msg)
}

func fileBaseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}
