package cmd

import (
	"testing"
	"time"
)

func TestSupervisorBackoffSchedule(t *testing.T) {
	// First crash retries quickly; subsequent crashes back off, capped at
	// 30s so a chronically-failing server doesn't burn CPU or fill logs.
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 250 * time.Millisecond},
		{2, 1 * time.Second},
		{3, 5 * time.Second},
		{4, 30 * time.Second},
		{5, 30 * time.Second},
		{20, 30 * time.Second},
	}
	for _, tt := range cases {
		got := supervisorBackoff(tt.attempt)
		if got != tt.want {
			t.Errorf("supervisorBackoff(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestSupervisorBackoffSchedule_ZeroAndNegative(t *testing.T) {
	// Defensive: attempt=0 or negative shouldn't panic; should clamp to
	// the initial backoff so the supervisor doesn't accidentally tight-loop.
	for _, n := range []int{0, -1, -100} {
		got := supervisorBackoff(n)
		if got != 250*time.Millisecond {
			t.Errorf("supervisorBackoff(%d) = %v, want 250ms (clamped)", n, got)
		}
	}
}
