package slack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteInboxIfSubscribed_HolderActive(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	holder, err := AcquireSubscribed(town, session)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()

	ev := InboxEvent{ChatID: "D1", Kind: "dm", MessageTS: "1.0", Text: "hi", SenderUserID: "U1"}
	written, err := writeInboxIfSubscribed(town, session, ev)
	if err != nil {
		t.Fatalf("writeInboxIfSubscribed: %v", err)
	}
	if !written {
		t.Fatal("written=false while subscribed; want true (channels path)")
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
		t.Fatalf("got %d *.json files in inbox, want 1", jsons)
	}
}

func TestWriteInboxIfSubscribed_NoHolder_FallsThrough(t *testing.T) {
	town := t.TempDir()
	session := "test/sess"
	// Don't acquire — simulating "no plugin alive".

	ev := InboxEvent{ChatID: "D1", Kind: "dm", MessageTS: "1.0", Text: "hi"}
	written, err := writeInboxIfSubscribed(town, session, ev)
	if err != nil {
		t.Fatalf("writeInboxIfSubscribed: %v", err)
	}
	if written {
		t.Fatal("written=true while not subscribed; want false (caller falls back to nudge)")
	}
}

func TestWriteInboxIfSubscribed_AtomicWrite(t *testing.T) {
	// Confirms tmp+rename: no .tmp file is left behind on success.
	town := t.TempDir()
	session := "test/sess"
	holder, err := AcquireSubscribed(town, session)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()

	ev := InboxEvent{ChatID: "D1", Kind: "dm", MessageTS: "1.0", Text: "hi"}
	if _, err := writeInboxIfSubscribed(town, session, ev); err != nil {
		t.Fatal(err)
	}

	dir := InboxDir(town, session)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("found leftover .tmp file %q (atomic rename failed)", e.Name())
		}
	}
}
