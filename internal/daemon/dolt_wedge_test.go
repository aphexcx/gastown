package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// wedgeTestManager returns a manager simulating a running server whose
// connectivity probe (active_branch()) succeeds. Tests control the executor
// probe to simulate the connection-handler wedge (gt-hcvs0).
func wedgeTestManager(t *testing.T) (*DoltServerManager, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var stopCount, startCount atomic.Int32
	var running atomic.Bool
	running.Store(true)

	m := newTestManager(t)
	m.runningFn = func() (int, bool) {
		if running.Load() {
			return 1234, true
		}
		return 0, false
	}
	m.healthCheckFn = func() error { return nil }
	m.writeProbeCheckFn = func() error { return nil }
	m.stopFn = func() {
		stopCount.Add(1)
		running.Store(false)
	}
	m.startFn = func() error {
		startCount.Add(1)
		running.Store(true)
		return nil
	}
	m.sleepFn = func(time.Duration) {}
	return m, &stopCount, &startCount
}

func TestEnsureRunning_ExecutorWedgeTriggersRestart(t *testing.T) {
	m, stopCount, startCount := wedgeTestManager(t)
	m.executorProbeFn = func() error {
		return fmt.Errorf("executor probe timed out: %w", context.DeadlineExceeded)
	}

	// First timeout: below threshold — warning only, no restart.
	if err := m.EnsureRunning(); err != nil {
		t.Fatalf("first EnsureRunning returned error: %v", err)
	}
	if got := startCount.Load(); got != 0 {
		t.Errorf("expected no restart after first timeout, got %d starts", got)
	}
	warned := false
	for _, w := range m.LastWarnings() {
		if strings.Contains(w, "connection-handler wedge") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected wedge warning after first timeout, got %v", m.LastWarnings())
	}

	// Second consecutive timeout: threshold reached — controlled restart.
	if err := m.EnsureRunning(); err != nil {
		t.Fatalf("second EnsureRunning returned error: %v", err)
	}
	if got := stopCount.Load(); got != 1 {
		t.Errorf("expected 1 stop after wedge detection, got %d", got)
	}
	if got := startCount.Load(); got != 1 {
		t.Errorf("expected 1 start after wedge detection, got %d", got)
	}
}

func TestEnsureRunning_ExecutorProbeSuccessResetsWedgeCounter(t *testing.T) {
	m, stopCount, startCount := wedgeTestManager(t)

	// Alternate timeout/success: the counter never reaches the threshold.
	var calls atomic.Int32
	m.executorProbeFn = func() error {
		if calls.Add(1)%2 == 1 {
			return fmt.Errorf("executor probe timed out: %w", context.DeadlineExceeded)
		}
		return nil
	}

	for i := 0; i < 6; i++ {
		if err := m.EnsureRunning(); err != nil {
			t.Fatalf("EnsureRunning %d returned error: %v", i, err)
		}
	}
	if got := stopCount.Load(); got != 0 {
		t.Errorf("expected no stops for intermittent timeouts, got %d", got)
	}
	if got := startCount.Load(); got != 0 {
		t.Errorf("expected no starts for intermittent timeouts, got %d", got)
	}
}

func TestEnsureRunning_ExecutorProbeNonTimeoutErrorDoesNotRestart(t *testing.T) {
	m, stopCount, startCount := wedgeTestManager(t)
	m.executorProbeFn = func() error {
		return errors.New("access denied for user")
	}

	// Non-timeout errors mean the executor answered — never a wedge, no
	// matter how many times they repeat.
	for i := 0; i < 3; i++ {
		if err := m.EnsureRunning(); err != nil {
			t.Fatalf("EnsureRunning %d returned error: %v", i, err)
		}
	}
	if got := stopCount.Load(); got != 0 {
		t.Errorf("expected no stops for non-timeout probe errors, got %d", got)
	}
	if got := startCount.Load(); got != 0 {
		t.Errorf("expected no starts for non-timeout probe errors, got %d", got)
	}
	warned := false
	for _, w := range m.LastWarnings() {
		if strings.Contains(w, "non-timeout") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected non-timeout warning, got %v", m.LastWarnings())
	}
}

func TestCheckHealth_WedgeErrorIsSentinel(t *testing.T) {
	m, _, _ := wedgeTestManager(t)
	m.executorProbeFn = func() error {
		return fmt.Errorf("executor probe timed out: %w", context.DeadlineExceeded)
	}

	if err := m.checkHealth(); err != nil {
		t.Fatalf("first checkHealth should warn, not fail: %v", err)
	}
	err := m.checkHealth()
	if err == nil {
		t.Fatal("second consecutive timeout should fail health check")
	}
	if !errors.Is(err, errDoltExecutorWedge) {
		t.Errorf("wedge error should match errDoltExecutorWedge sentinel, got %v", err)
	}

	// Counter resets after detection: the next timeout is 1/2 again.
	if err := m.checkHealth(); err != nil {
		t.Errorf("counter should reset after wedge detection, got error: %v", err)
	}
}

func TestExecutorProbeSkippedWhenHealthCheckStubbed(t *testing.T) {
	// Managers with a stubbed connectivity probe but no executor probe stub
	// (i.e., every pre-existing test) must not shell out to a real dolt
	// binary and must stay healthy.
	m, stopCount, startCount := wedgeTestManager(t)
	m.executorProbeFn = nil

	if err := m.EnsureRunning(); err != nil {
		t.Fatalf("EnsureRunning returned error: %v", err)
	}
	if stopCount.Load() != 0 || startCount.Load() != 0 {
		t.Errorf("expected no restart, got %d stops / %d starts", stopCount.Load(), startCount.Load())
	}
}
