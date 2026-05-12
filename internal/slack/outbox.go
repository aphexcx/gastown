package slack

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OutboxMessage is the JSON envelope agents write via `gt slack reply` and
// the daemon publisher consumes. The publisher uses From to look up the
// display name and ChatID/ThreadTS for the post target.
type OutboxMessage struct {
	From      string    `json:"from"`                // gt address, e.g. "acme/crew/cody"
	ChatID    string    `json:"chat_id"`             // Slack channel or DM ID
	Text      string    `json:"text"`
	ThreadTS  string    `json:"thread_ts,omitempty"` // reply-in-thread timestamp
	Files     []string  `json:"files,omitempty"`     // absolute file paths for upload
	Timestamp time.Time `json:"timestamp"`           // when gt slack reply was invoked

	// FailureReason is populated only in dead-letter files.
	FailureReason string `json:"failure_reason,omitempty"`
}

const (
	outboxExt        = ".json"
	claimInfix       = ".claimed."
	failedDirName    = "failed"
	tmpExt           = ".tmp"
	randomSuffixBits = 4
)

// WriteOutboxMessage atomically writes a message to dir and returns the
// final path. dir must already exist. The caller sets Timestamp if it
// matters; we fill in zero values as now.
func WriteOutboxMessage(dir string, msg *OutboxMessage) (string, error) {
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	data, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal outbox message: %w", err)
	}

	name := fmt.Sprintf("%d-%s%s", msg.Timestamp.UnixNano(), randomSuffix(), outboxExt)
	final := filepath.Join(dir, name)
	tmp := final + tmpExt

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("rename %s -> %s: %w", tmp, final, err)
	}
	return final, nil
}

// ReadOutboxMessage loads and unmarshals a message file. Valid for both
// pending .json and claimed .json.claimed.* paths.
func ReadOutboxMessage(path string) (*OutboxMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var msg OutboxMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &msg, nil
}

// ListPendingOutbox returns all non-claimed, non-tmp .json files in dir,
// sorted lexicographically (which matches FIFO since filenames are nano-
// timestamp prefixed). It ignores the failed/ subdir.
func ListPendingOutbox(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read outbox dir: %w", err)
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, outboxExt) {
			continue
		}
		if strings.Contains(name, claimInfix) {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	sort.Strings(paths)
	return paths, nil
}

// ClaimOutboxMessage atomically renames path to path.claimed.<rand>. Returns
// the claimed path on success. If another claimer won, returns an error.
func ClaimOutboxMessage(path string) (string, error) {
	claim := path + claimInfix + randomSuffix()
	if err := os.Rename(path, claim); err != nil {
		return "", fmt.Errorf("claim %s: %w", path, err)
	}
	return claim, nil
}

// UnclaimOutboxMessage reverses a claim, renaming back to the original .json
// path. Used on transient failures so the next rescan re-picks it up.
func UnclaimOutboxMessage(claim string) error {
	idx := strings.Index(claim, claimInfix)
	if idx < 0 {
		return fmt.Errorf("path %q is not a claim", claim)
	}
	// claim = "<base>.json.claimed.<rand>"; claim[:idx] already ends in ".json".
	original := claim[:idx]
	if err := os.Rename(claim, original); err != nil {
		return fmt.Errorf("unclaim %s: %w", claim, err)
	}
	return nil
}

// DeadLetterOutboxMessage moves a claimed file into <dir>/failed/ with a
// failure_reason recorded in the payload. Called on permanent outbound errors.
// The dir argument is the outbox root (parent of failed/), derived from the
// claim path.
func DeadLetterOutboxMessage(claim, reason string) error {
	msg, err := ReadOutboxMessage(claim)
	if err != nil {
		return fmt.Errorf("read claim for dead-letter: %w", err)
	}
	msg.FailureReason = reason

	outboxDir := filepath.Dir(claim)
	failedDir := filepath.Join(outboxDir, failedDirName)
	if err := os.MkdirAll(failedDir, 0o700); err != nil {
		return fmt.Errorf("mkdir failed/: %w", err)
	}

	// Use the original filename (without .claimed.*) under failed/.
	// base = "<ts>-<rand>.json.claimed.<rand>"; base[:idx] already ends in ".json".
	base := filepath.Base(claim)
	if idx := strings.Index(base, claimInfix); idx > 0 {
		base = base[:idx]
	}
	dest := filepath.Join(failedDir, base)

	data, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal dead-letter: %w", err)
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return fmt.Errorf("write dead-letter: %w", err)
	}
	if err := os.Remove(claim); err != nil {
		return fmt.Errorf("remove claim after dead-letter: %w", err)
	}
	return nil
}

// SweepStaleClaims finds claim files older than threshold and renames them
// back to their original .json names so the next scan re-processes them.
// Called on daemon startup and periodically by the publisher.
func SweepStaleClaims(dir string, threshold time.Duration) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read outbox dir: %w", err)
	}
	cutoff := time.Now().Add(-threshold)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.Contains(name, claimInfix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		claim := filepath.Join(dir, name)
		if err := UnclaimOutboxMessage(claim); err != nil {
			// Best effort — log to stderr and keep going.
			fmt.Fprintf(os.Stderr, "slack: sweep stale claim %s: %v\n", name, err)
		}
	}
	return nil
}

func randomSuffix() string {
	var b [randomSuffixBits]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
