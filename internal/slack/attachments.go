package slack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EagerDownloadLimit is the size threshold for eager inbound downloads.
// Files at or below this size are downloaded and their local path is
// included in the nudge body. Files above it appear as metadata only.
const EagerDownloadLimit int64 = 5 * 1024 * 1024 // 5 MB

// AttachmentMeta is the per-file metadata the slackevents package gives us
// on a MessageEvent. We model it as a plain struct so inbound.go doesn't
// depend on slack-go's File type.
type AttachmentMeta struct {
	ID         string
	Name       string
	MimeType   string
	Size       int64
	URLPrivate string // bot-token-authenticated download URL
}

// AttachmentAction is the router's decision on how to treat one file.
type AttachmentAction int

const (
	AttachmentActionMetadataOnly AttachmentAction = iota
	AttachmentActionDownload
)

// DecideAttachment returns the router's action for a single file.
func DecideAttachment(m AttachmentMeta) AttachmentAction {
	if m.Size <= EagerDownloadLimit {
		return AttachmentActionDownload
	}
	return AttachmentActionMetadataOnly
}

// SanitizeAttachmentFilename strips path traversal attempts and unsafe
// characters from a Slack-provided filename before using it on disk.
// Empty / all-dot filenames become "attachment".
func SanitizeAttachmentFilename(name string) string {
	// Strip traversal sequences first, before touching separators.
	for strings.Contains(name, "..") {
		name = strings.ReplaceAll(name, "..", "")
	}

	// Replace path separators.
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")

	// Allow only ASCII alphanumerics plus a small set of safe punctuation;
	// replace anything else with underscore.
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	cleaned := strings.Trim(b.String(), "._")
	if cleaned == "" {
		return "attachment"
	}
	return cleaned
}

// DownloadInboundAttachment fetches a single file into the shared slack_inbox
// directory and returns the local absolute path. Uses the Client for the
// authenticated fetch; uses the router's inbox layout under townRoot/.runtime.
func DownloadInboundAttachment(
	ctx context.Context,
	client *Client,
	townRoot string,
	m AttachmentMeta,
) (string, error) {
	if m.URLPrivate == "" {
		return "", fmt.Errorf("attachment %s has no URL", m.ID)
	}
	inboxDir := filepath.Join(townRoot, ".runtime", "slack_inbox")
	if err := os.MkdirAll(inboxDir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir slack_inbox: %w", err)
	}
	dest := filepath.Join(inboxDir, m.ID+"-"+SanitizeAttachmentFilename(m.Name))
	if err := client.DownloadFile(ctx, m.URLPrivate, dest); err != nil {
		return "", err
	}
	return dest, nil
}
