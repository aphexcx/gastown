package slack

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPIDFilePath_RespectsHome(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	got, err := PIDFilePath()
	if err != nil {
		t.Fatalf("PIDFilePath: %v", err)
	}
	want := filepath.Join(tmpHome, "gt", ".runtime", "slack.pid")
	if got != want {
		t.Errorf("PIDFilePath = %q, want %q", got, want)
	}
}

func TestWritePIDFile_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "nested", "slack.pid")
	if err := WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("parent is not a directory")
	}
	// Permissions: parent should be 0700 to keep the PID private to user.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("parent dir perms = %o, want no group/other access", perm)
	}
}

func TestWritePIDFile_WritesCurrentPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slack.pid")
	if err := WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gotPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if gotPID != os.Getpid() {
		t.Errorf("pid in file = %d, want current pid %d", gotPID, os.Getpid())
	}
	// File perms must be 0600 (sensitive).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perms = %o, want 0600", perm)
	}
}

func TestWritePIDFile_BadPathIsError(t *testing.T) {
	// Force MkdirAll to fail by giving it a path component that can't be
	// created (a file as a parent).
	dir := t.TempDir()
	blocker := filepath.Join(dir, "imafile")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "subdir", "slack.pid")
	if err := WritePIDFile(path); err == nil {
		t.Error("expected error when parent path component is a file")
	}
}

func TestReadPIDFile_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slack.pid")
	if err := WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	pid, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("ReadPIDFile: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", pid, os.Getpid())
	}
}

func TestReadPIDFile_StripsTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slack.pid")
	if err := os.WriteFile(path, []byte("12345\n\n  "), 0o600); err != nil {
		t.Fatal(err)
	}
	pid, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("ReadPIDFile: %v", err)
	}
	if pid != 12345 {
		t.Errorf("pid = %d, want 12345", pid)
	}
}

func TestReadPIDFile_Missing(t *testing.T) {
	_, err := ReadPIDFile(filepath.Join(t.TempDir(), "does-not-exist.pid"))
	if err == nil {
		t.Error("expected error for missing file")
	}
	if !os.IsNotExist(err) {
		t.Errorf("err should wrap ErrNotExist; got %v", err)
	}
}

func TestReadPIDFile_Malformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slack.pid")
	if err := os.WriteFile(path, []byte("not a number"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadPIDFile(path)
	if err == nil {
		t.Error("expected parse error for non-numeric content")
	}
}

func TestRemovePIDFile_MissingIsNotError(t *testing.T) {
	if err := RemovePIDFile(filepath.Join(t.TempDir(), "missing.pid")); err != nil {
		t.Errorf("RemovePIDFile(missing) = %v, want nil", err)
	}
}

func TestRemovePIDFile_ExistingDeletes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slack.pid")
	if err := WritePIDFile(path); err != nil {
		t.Fatal(err)
	}
	if err := RemovePIDFile(path); err != nil {
		t.Fatalf("RemovePIDFile: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists after RemovePIDFile; stat err = %v", err)
	}
}

func TestRemovePIDFile_PropagatesUnexpectedError(t *testing.T) {
	// Documents the contract that ONLY ENOENT is silently swallowed —
	// other errors propagate. We trigger ENOTEMPTY by pointing at a
	// non-empty directory.
	dir := filepath.Join(t.TempDir(), "non-empty-dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := RemovePIDFile(dir)
	if err == nil {
		t.Error("expected error when target is a non-empty directory")
	}
	if os.IsNotExist(err) {
		t.Errorf("err should NOT be ErrNotExist; got %v", err)
	}
}

func TestIsProcessAlive_SelfIsAlive(t *testing.T) {
	if !IsProcessAlive(os.Getpid()) {
		t.Error("current process should report alive")
	}
}

func TestIsProcessAlive_NonexistentIsDead(t *testing.T) {
	// PID 0 is reserved by the kernel; signal 0 will be EPERM or ESRCH.
	// Either way, IsProcessAlive treats it as "can't signal" → dead.
	if IsProcessAlive(0) {
		t.Error("pid 0 should not report alive (kernel reserved)")
	}
	// Pick an impossibly-high pid. On macOS the pid_max is around 99999;
	// on Linux it's higher but 4_000_000_000 is well beyond any normal limit.
	if IsProcessAlive(4_000_000_000) {
		t.Error("absurd pid should not report alive")
	}
}

func TestPIDFileFullLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slack.pid")

	// Initially: no file, ReadPIDFile errors, RemovePIDFile is a no-op.
	if _, err := ReadPIDFile(path); !os.IsNotExist(err) {
		t.Errorf("ReadPIDFile pre-write: want ErrNotExist; got %v", err)
	}
	if err := RemovePIDFile(path); err != nil {
		t.Errorf("RemovePIDFile pre-write should be no-op; got %v", err)
	}

	// Write → file present, content parses to current pid.
	if err := WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	pid, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("ReadPIDFile post-write: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", pid, os.Getpid())
	}

	// IsProcessAlive on the read pid should be true (it's us).
	if !IsProcessAlive(pid) {
		t.Errorf("IsProcessAlive(%d) on self = false", pid)
	}

	// Remove → file gone, subsequent ReadPIDFile fails again.
	if err := RemovePIDFile(path); err != nil {
		t.Fatalf("RemovePIDFile: %v", err)
	}
	if _, err := ReadPIDFile(path); !os.IsNotExist(err) {
		t.Errorf("ReadPIDFile post-remove: want ErrNotExist; got %v", err)
	}

	// Idempotent remove.
	if err := RemovePIDFile(path); err != nil {
		t.Errorf("RemovePIDFile on already-removed = %v, want nil", err)
	}
}
