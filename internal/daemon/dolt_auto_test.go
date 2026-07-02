package daemon

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// neutralizeDoltEnv clears environment overrides that would otherwise leak
// the host Gas Town's Dolt configuration into doltserver.DefaultConfig.
func neutralizeDoltEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GT_DOLT_PORT", "")
	t.Setenv("GT_DOLT_HOST", "")
	t.Setenv("GT_DOLT_USER", "")
	t.Setenv("GT_DOLT_PASSWORD", "")
}

// newAutoTestTown creates a temp town root with daemon/ and .dolt-data/
// directories, mirroring a town whose Dolt server is started by gt dolt start.
func newAutoTestTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	for _, dir := range []string{"daemon", ".dolt-data"} {
		if err := os.MkdirAll(filepath.Join(townRoot, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return townRoot
}

// TestNewAutoDoltServerManager_NoDataDir verifies that towns without a Dolt
// data directory (server mode not in use) get no auto-manager.
func TestNewAutoDoltServerManager_NoDataDir(t *testing.T) {
	neutralizeDoltEnv(t)
	townRoot := t.TempDir()

	m := NewAutoDoltServerManager(townRoot, t.Logf)
	if m != nil {
		t.Fatalf("expected nil manager without .dolt-data, got %+v", m.config)
	}
}

// TestNewAutoDoltServerManager_DerivesCanonicalConfig verifies the auto
// manager targets the same server gt dolt start manages: canonical port,
// data dir, log file, and pid file — not the daemon package's legacy
// defaults (port 3306, townRoot/dolt).
func TestNewAutoDoltServerManager_DerivesCanonicalConfig(t *testing.T) {
	neutralizeDoltEnv(t)
	townRoot := newAutoTestTown(t)

	m := NewAutoDoltServerManager(townRoot, t.Logf)
	if m == nil {
		t.Fatal("expected auto manager for town with .dolt-data")
	}
	if !m.IsEnabled() {
		t.Error("auto manager should be enabled")
	}
	if m.IsExternal() {
		t.Error("local server should not be external")
	}
	if got, want := m.config.Port, 3307; got != want {
		t.Errorf("Port = %d, want %d", got, want)
	}
	if got, want := m.config.DataDir, filepath.Join(townRoot, ".dolt-data"); got != want {
		t.Errorf("DataDir = %q, want %q", got, want)
	}
	if got, want := m.config.LogFile, filepath.Join(townRoot, "daemon", "dolt.log"); got != want {
		t.Errorf("LogFile = %q, want %q", got, want)
	}
	if got, want := m.pidFile(), filepath.Join(townRoot, "daemon", "dolt.pid"); got != want {
		t.Errorf("pidFile() = %q, want %q", got, want)
	}
	if m.startDelegate == nil {
		t.Error("auto manager should delegate starts to doltserver.Start")
	}
	if !m.autoManaged {
		t.Error("auto manager must be marked autoManaged so daemon shutdown leaves the adopted server running")
	}
	// Backoff/escalation limits must be populated so restartWithBackoff
	// keeps its cap semantics.
	if m.config.MaxRestartsInWindow <= 0 {
		t.Error("MaxRestartsInWindow should be positive")
	}
	if m.config.RestartDelay <= 0 {
		t.Error("RestartDelay should be positive")
	}
}

// TestNewAutoDoltServerManager_CustomPortKeepsCanonicalPidFile verifies that
// a custom port (GT_DOLT_PORT) still uses the canonical dolt.pid written by
// gt dolt start, instead of the port-suffixed fallback that would make the
// daemon blind to the CLI-started server.
func TestNewAutoDoltServerManager_CustomPortKeepsCanonicalPidFile(t *testing.T) {
	neutralizeDoltEnv(t)
	t.Setenv("GT_DOLT_PORT", "3308")
	townRoot := newAutoTestTown(t)

	m := NewAutoDoltServerManager(townRoot, t.Logf)
	if m == nil {
		t.Fatal("expected auto manager for town with .dolt-data")
	}
	if got, want := m.config.Port, 3308; got != want {
		t.Errorf("Port = %d, want %d", got, want)
	}
	if got, want := m.pidFile(), filepath.Join(townRoot, "daemon", "dolt.pid"); got != want {
		t.Errorf("pidFile() = %q, want %q", got, want)
	}
}

// TestNewAutoDoltServerManager_RemoteHostIsExternal verifies that a
// non-loopback Dolt host makes the auto manager monitor-only: the daemon
// must never try to start or stop a local process for a remote server.
func TestNewAutoDoltServerManager_RemoteHostIsExternal(t *testing.T) {
	neutralizeDoltEnv(t)
	t.Setenv("GT_DOLT_HOST", "203.0.113.5") // TEST-NET-3: never loopback
	townRoot := newAutoTestTown(t)

	m := NewAutoDoltServerManager(townRoot, t.Logf)
	if m == nil {
		t.Fatal("expected auto manager for town with .dolt-data")
	}
	if !m.IsExternal() {
		t.Error("remote host should force External (monitor-only) mode")
	}
}

// TestPidFile_ConfigOverride verifies the explicit PidFile config field wins
// over the port-derived default.
func TestPidFile_ConfigOverride(t *testing.T) {
	m := newTestManager(t)
	override := filepath.Join(m.townRoot, "daemon", "custom.pid")
	m.config.PidFile = override
	if got := m.pidFile(); got != override {
		t.Errorf("pidFile() = %q, want override %q", got, override)
	}
}

// deadTestPID returns the PID of a process that has already exited.
func deadTestPID(t *testing.T) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("dead-PID probe requires POSIX process semantics")
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running true: %v", err)
	}
	return cmd.Process.Pid
}

// TestIsRunning_StalePidFileReportsDeadPid verifies that a pid file pointing
// at a dead process yields (pid, false) — not (0, false) — so EnsureRunning
// can attribute the crash and send an alert. Regression: gt-9uwuz (silent
// no-alert path when Dolt fully dies).
func TestIsRunning_StalePidFileReportsDeadPid(t *testing.T) {
	m := newTestManager(t)
	m.runningFn = nil // exercise the real implementation

	deadPID := deadTestPID(t)
	if _, err := writePIDFile(m.pidFile(), deadPID); err != nil {
		t.Fatal(err)
	}

	pid, running := m.isRunning()
	if running {
		t.Fatal("dead process reported as running")
	}
	if pid != deadPID {
		t.Errorf("isRunning() pid = %d, want dead pid %d", pid, deadPID)
	}
	if _, err := os.Stat(m.pidFile()); !os.IsNotExist(err) {
		t.Error("stale pid file should have been removed")
	}
}

// TestEnsureRunning_DeadServerAlertsAndRestarts verifies the full dead-server
// recovery path: crash alert sent with the dead PID, DOLT_UNHEALTHY signal
// written, and the server started again.
func TestEnsureRunning_DeadServerAlertsAndRestarts(t *testing.T) {
	var startCount atomic.Int32
	var alertPID atomic.Int32

	m := newTestManager(t)
	m.runningFn = func() (int, bool) { return 4321, false }
	m.startFn = func() error {
		startCount.Add(1)
		return nil
	}
	m.crashAlertFn = func(pid int) { alertPID.Store(int32(pid)) }
	m.sleepFn = func(d time.Duration) {}

	if err := m.EnsureRunning(); err != nil {
		t.Fatalf("EnsureRunning returned error: %v", err)
	}
	if got := startCount.Load(); got != 1 {
		t.Errorf("expected 1 start, got %d", got)
	}
	if got := alertPID.Load(); got != 4321 {
		t.Errorf("crash alert pid = %d, want 4321", got)
	}
	data, err := os.ReadFile(m.unhealthySignalFile())
	if err != nil {
		t.Fatalf("DOLT_UNHEALTHY signal not written: %v", err)
	}
	if !strings.Contains(string(data), "server_dead") {
		t.Errorf("signal file missing server_dead reason: %s", data)
	}
}

// TestShutdown_AutoManagedDoltSurvives verifies Daemon.shutdown leaves an
// adopted (auto-managed) Dolt server running — bd across all rigs depends on
// it surviving daemon restarts — while an explicitly configured local
// manager is still stopped, as before.
func TestShutdown_AutoManagedDoltSurvives(t *testing.T) {
	for _, tc := range []struct {
		name        string
		autoManaged bool
		wantStops   int32
	}{
		{"auto-managed adopted server survives", true, 0},
		{"explicitly configured server is stopped", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stops atomic.Int32
			m := newTestManager(t)
			m.autoManaged = tc.autoManaged
			m.stopFn = func() { stops.Add(1) }

			d := &Daemon{
				config:     &Config{TownRoot: m.townRoot},
				logger:     log.New(io.Discard, "", 0),
				doltServer: m,
			}
			if err := d.shutdown(&State{}); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			if got := stops.Load(); got != tc.wantStops {
				t.Errorf("Stop called %d times, want %d", got, tc.wantStops)
			}
		})
	}
}

// TestStartLocked_UsesStartDelegate verifies that when a start delegate is
// wired (auto-managed mode), startLocked routes through it instead of the
// built-in start path.
func TestStartLocked_UsesStartDelegate(t *testing.T) {
	var delegateCount atomic.Int32

	m := newTestManager(t)
	m.startFn = nil // force the real startLocked body
	m.runningFn = func() (int, bool) { return 0, false }
	m.startDelegate = func() error {
		delegateCount.Add(1)
		return nil
	}

	if err := m.Start(); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if got := delegateCount.Load(); got != 1 {
		t.Errorf("delegate called %d times, want 1", got)
	}
}

// TestStartLocked_DelegateSkippedWhenAlreadyRunning verifies the TOCTOU
// re-check still applies before the delegate fires.
func TestStartLocked_DelegateSkippedWhenAlreadyRunning(t *testing.T) {
	var delegateCount atomic.Int32

	m := newTestManager(t)
	m.startFn = nil
	m.runningFn = func() (int, bool) { return 999, true }
	m.startDelegate = func() error {
		delegateCount.Add(1)
		return nil
	}

	if err := m.Start(); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if got := delegateCount.Load(); got != 0 {
		t.Errorf("delegate called %d times for running server, want 0", got)
	}
}
