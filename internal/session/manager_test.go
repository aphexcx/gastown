package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/tmux"
)

func TestSessionName(t *testing.T) {
	r := &rig.Rig{
		Name:     "gastown",
		Polecats: []string{"Toast"},
	}
	m := NewManager(tmux.NewTmux(), r)

	name := m.SessionName("Toast")
	if name != "gt-gastown-Toast" {
		t.Errorf("sessionName = %q, want gt-gastown-Toast", name)
	}
}

func TestPolecatDir(t *testing.T) {
	r := &rig.Rig{
		Name:     "gastown",
		Path:     "/home/user/ai/gastown",
		Polecats: []string{"Toast"},
	}
	m := NewManager(tmux.NewTmux(), r)

	dir := m.polecatDir("Toast")
	expected := "/home/user/ai/gastown/polecats/Toast"
	if dir != expected {
		t.Errorf("polecatDir = %q, want %q", dir, expected)
	}
}

func TestHasPolecat(t *testing.T) {
	root := t.TempDir()
	// hasPolecat checks filesystem, so create actual directories
	for _, name := range []string{"Toast", "Cheedo"} {
		if err := os.MkdirAll(filepath.Join(root, "polecats", name), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	r := &rig.Rig{
		Name:     "gastown",
		Path:     root,
		Polecats: []string{"Toast", "Cheedo"},
	}
	m := NewManager(tmux.NewTmux(), r)

	if !m.hasPolecat("Toast") {
		t.Error("expected hasPolecat(Toast) = true")
	}
	if !m.hasPolecat("Cheedo") {
		t.Error("expected hasPolecat(Cheedo) = true")
	}
	if m.hasPolecat("Unknown") {
		t.Error("expected hasPolecat(Unknown) = false")
	}
}

func TestStartPolecatNotFound(t *testing.T) {
	r := &rig.Rig{
		Name:     "gastown",
		Polecats: []string{"Toast"},
	}
	m := NewManager(tmux.NewTmux(), r)

	err := m.Start("Unknown", StartOptions{})
	if err == nil {
		t.Error("expected error for unknown polecat")
	}
}

func TestIsRunningNoSession(t *testing.T) {
	r := &rig.Rig{
		Name:     "gastown",
		Polecats: []string{"Toast"},
	}
	m := NewManager(tmux.NewTmux(), r)

	running, err := m.IsRunning("Toast")
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if running {
		t.Error("expected IsRunning = false for non-existent session")
	}
}

func TestListEmpty(t *testing.T) {
	r := &rig.Rig{
		Name:     "test-rig-unlikely-name",
		Polecats: []string{},
	}
	m := NewManager(tmux.NewTmux(), r)

	infos, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("infos count = %d, want 0", len(infos))
	}
}

func TestStopNotFound(t *testing.T) {
	r := &rig.Rig{
		Name:     "test-rig",
		Polecats: []string{"Toast"},
	}
	m := NewManager(tmux.NewTmux(), r)

	err := m.Stop("Toast", false)
	if err != ErrSessionNotFound {
		t.Errorf("Stop = %v, want ErrSessionNotFound", err)
	}
}

func TestCaptureNotFound(t *testing.T) {
	r := &rig.Rig{
		Name:     "test-rig",
		Polecats: []string{"Toast"},
	}
	m := NewManager(tmux.NewTmux(), r)

	_, err := m.Capture("Toast", 50)
	if err != ErrSessionNotFound {
		t.Errorf("Capture = %v, want ErrSessionNotFound", err)
	}
}

func TestInjectNotFound(t *testing.T) {
	r := &rig.Rig{
		Name:     "test-rig",
		Polecats: []string{"Toast"},
	}
	m := NewManager(tmux.NewTmux(), r)

	err := m.Inject("Toast", "hello")
	if err != ErrSessionNotFound {
		t.Errorf("Inject = %v, want ErrSessionNotFound", err)
	}
}
