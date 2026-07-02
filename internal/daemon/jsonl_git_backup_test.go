package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestIsTestPollution(t *testing.T) {
	tests := []struct {
		name     string
		record   map[string]interface{}
		expected bool
	}{
		{
			name:     "normal issue",
			record:   map[string]interface{}{"id": "gt-abc1", "title": "Fix login bug"},
			expected: false,
		},
		{
			name:     "title starts with Test Issue",
			record:   map[string]interface{}{"id": "gt-xyz2", "title": "Test Issue for validation"},
			expected: true,
		},
		{
			name:     "title starts with test issue lowercase",
			record:   map[string]interface{}{"id": "gt-xyz2", "title": "test issue for validation"},
			expected: true,
		},
		{
			name:     "short test id bd-1",
			record:   map[string]interface{}{"id": "bd-1", "title": "Something"},
			expected: true,
		},
		{
			name:     "short test id bd-99",
			record:   map[string]interface{}{"id": "bd-99", "title": "Something"},
			expected: true,
		},
		{
			name:     "test-style id bd-abc12",
			record:   map[string]interface{}{"id": "bd-abc12", "title": "Something"},
			expected: true,
		},
		{
			name:     "testdb prefix id",
			record:   map[string]interface{}{"id": "testdb_foo", "title": "Something"},
			expected: true,
		},
		{
			name:     "beads_t prefix id",
			record:   map[string]interface{}{"id": "beads_t123", "title": "Something"},
			expected: true,
		},
		{
			name:     "beads_pt prefix id",
			record:   map[string]interface{}{"id": "beads_pt456", "title": "Something"},
			expected: true,
		},
		{
			name:     "doctest prefix id",
			record:   map[string]interface{}{"id": "doctest_foo", "title": "Something"},
			expected: true,
		},
		{
			name:     "title starts with test_",
			record:   map[string]interface{}{"id": "gt-ok1", "title": "test_something"},
			expected: true,
		},
		{
			name:     "title starts with test space",
			record:   map[string]interface{}{"id": "gt-ok1", "title": "test something"},
			expected: true,
		},
		{
			name:     "normal id with test in middle",
			record:   map[string]interface{}{"id": "gt-test1", "title": "Normal title"},
			expected: false,
		},
		{
			name:     "longer legitimate id bd-abcde12",
			record:   map[string]interface{}{"id": "bd-abcde12", "title": "Something"},
			expected: true,
		},
		{
			name:     "legitimate bd id bd-abcdef",
			record:   map[string]interface{}{"id": "bd-abcdef", "title": "Something"},
			expected: false,
		},
		{
			name:     "empty record",
			record:   map[string]interface{}{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTestPollution(tt.record)
			if got != tt.expected {
				t.Errorf("isTestPollution(%v) = %v, want %v", tt.record, got, tt.expected)
			}
		})
	}
}

func TestFilterTestPollution(t *testing.T) {
	// Build JSONL with mix of good and bad records.
	good1, _ := json.Marshal(map[string]interface{}{"id": "gt-abc1", "title": "Fix bug"})
	good2, _ := json.Marshal(map[string]interface{}{"id": "gt-def2", "title": "Add feature"})
	bad1, _ := json.Marshal(map[string]interface{}{"id": "bd-1", "title": "test thing"})
	bad2, _ := json.Marshal(map[string]interface{}{"id": "gt-xyz3", "title": "Test Issue 42"})

	input := string(good1) + "\n" + string(bad1) + "\n" + string(good2) + "\n" + string(bad2) + "\n"

	filtered, removed := filterTestPollution([]byte(input))

	if removed != 2 {
		t.Errorf("expected 2 removed, got %d", removed)
	}

	// Verify only good records remain.
	lines := splitNonEmpty(string(filtered))
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines after filter, got %d: %v", len(lines), lines)
	}

	// Verify the good records are preserved.
	for _, line := range lines {
		var rec map[string]interface{}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("failed to parse filtered line: %v", err)
		}
		if isTestPollution(rec) {
			t.Errorf("test pollution record survived filtering: %v", rec)
		}
	}
}

func TestFilterTestPollution_NoRemoval(t *testing.T) {
	good1, _ := json.Marshal(map[string]interface{}{"id": "gt-abc1", "title": "Fix bug"})
	good2, _ := json.Marshal(map[string]interface{}{"id": "gt-def2", "title": "Add feature"})
	input := string(good1) + "\n" + string(good2) + "\n"

	filtered, removed := filterTestPollution([]byte(input))

	if removed != 0 {
		t.Errorf("expected 0 removed, got %d", removed)
	}

	lines := splitNonEmpty(string(filtered))
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d", len(lines))
	}
}

func TestFilterTestPollution_EmptyInput(t *testing.T) {
	filtered, removed := filterTestPollution([]byte(""))
	if removed != 0 {
		t.Errorf("expected 0 removed, got %d", removed)
	}
	if len(filtered) != 0 {
		t.Errorf("expected empty output, got %q", filtered)
	}
}

func TestSpikeThreshold(t *testing.T) {
	// nil config → default
	if got := spikeThreshold(nil); got != defaultSpikeThreshold {
		t.Errorf("expected %v, got %v", defaultSpikeThreshold, got)
	}

	// nil SpikeThreshold → default
	config := &JsonlGitBackupConfig{}
	if got := spikeThreshold(config); got != defaultSpikeThreshold {
		t.Errorf("expected %v, got %v", defaultSpikeThreshold, got)
	}

	// custom threshold
	threshold := 0.10
	config.SpikeThreshold = &threshold
	if got := spikeThreshold(config); got != 0.10 {
		t.Errorf("expected 0.10, got %v", got)
	}

	// invalid threshold (> 1.0) → default
	invalid := 1.5
	config.SpikeThreshold = &invalid
	if got := spikeThreshold(config); got != defaultSpikeThreshold {
		t.Errorf("expected default for invalid threshold, got %v", got)
	}

	// zero threshold → default
	zero := 0.0
	config.SpikeThreshold = &zero
	if got := spikeThreshold(config); got != defaultSpikeThreshold {
		t.Errorf("expected default for zero threshold, got %v", got)
	}
}

func TestFormatSpikeReport(t *testing.T) {
	spikes := []spikeInfo{
		{DB: "prod_beads", File: "prod_beads/issues.jsonl", Previous: 100, Current: 150, Delta: 0.50},
		{DB: "dev_beads", File: "dev_beads/issues.jsonl", Previous: 200, Current: 50, Delta: 0.75},
	}
	report := formatSpikeReport(spikes)
	if report == "" {
		t.Fatal("expected non-empty report")
	}
	// Verify it mentions both databases.
	if got := report; !contains(got, "prod_beads") || !contains(got, "dev_beads") {
		t.Errorf("report should mention both databases: %s", got)
	}
	if !contains(report, "JUMP") {
		t.Errorf("report should mention JUMP for increase: %s", report)
	}
	if !contains(report, "DROP") {
		t.Errorf("report should mention DROP for decrease: %s", report)
	}
}

func TestVerifyExportCounts_FirstExport(t *testing.T) {
	// Set up a git repo with no prior commits containing our file.
	gitRepo := t.TempDir()
	initGitRepo(t, gitRepo)

	d := &Daemon{logger: log.New(io.Discard, "", 0)}

	counts := map[string]int{"testdb": 100}
	spikes := d.verifyExportCounts(gitRepo, []string{"testdb"}, counts, 0.20)
	if len(spikes) != 0 {
		t.Errorf("expected no spikes on first export, got %v", spikes)
	}
}

func TestVerifyExportCounts_WithinThreshold(t *testing.T) {
	gitRepo := t.TempDir()
	initGitRepo(t, gitRepo)

	// Create a baseline: 100 lines in testdb/issues.jsonl
	dbDir := filepath.Join(gitRepo, "testdb")
	os.MkdirAll(dbDir, 0755)
	writeNLines(t, filepath.Join(dbDir, "issues.jsonl"), 100)
	commitAll(t, gitRepo, "baseline")

	d := &Daemon{logger: log.New(io.Discard, "", 0)}

	// 130 records = 30% increase. With 0.20 threshold and 2x asymmetric
	// multiplier for increases, effective threshold is 0.40, so 30% is fine.
	counts := map[string]int{"testdb": 130}
	spikes := d.verifyExportCounts(gitRepo, []string{"testdb"}, counts, 0.20)
	if len(spikes) != 0 {
		t.Errorf("expected no spikes for 30%% increase (effective threshold 40%%), got %v", spikes)
	}
}

func TestVerifyExportCounts_ExceedsThreshold(t *testing.T) {
	gitRepo := t.TempDir()
	initGitRepo(t, gitRepo)

	// Create baseline: 100 lines
	dbDir := filepath.Join(gitRepo, "testdb")
	os.MkdirAll(dbDir, 0755)
	writeNLines(t, filepath.Join(dbDir, "issues.jsonl"), 100)
	commitAll(t, gitRepo, "baseline")

	d := &Daemon{logger: log.New(io.Discard, "", 0)}

	// 200 records = 100% jump. Even with 2x asymmetric multiplier (effective
	// threshold 0.40), 100% exceeds it.
	counts := map[string]int{"testdb": 200}
	spikes := d.verifyExportCounts(gitRepo, []string{"testdb"}, counts, 0.20)
	if len(spikes) != 1 {
		t.Fatalf("expected 1 spike, got %d", len(spikes))
	}
	if spikes[0].DB != "testdb" {
		t.Errorf("expected spike for testdb, got %s", spikes[0].DB)
	}
	if spikes[0].Previous != 100 || spikes[0].Current != 200 {
		t.Errorf("expected 100→200, got %d→%d", spikes[0].Previous, spikes[0].Current)
	}
}

func TestVerifyExportCounts_Drop(t *testing.T) {
	gitRepo := t.TempDir()
	initGitRepo(t, gitRepo)

	dbDir := filepath.Join(gitRepo, "testdb")
	os.MkdirAll(dbDir, 0755)
	writeNLines(t, filepath.Join(dbDir, "issues.jsonl"), 100)
	commitAll(t, gitRepo, "baseline")

	d := &Daemon{logger: log.New(io.Discard, "", 0)}

	// 60 records = 40% drop. Drops use the base threshold (no 2x multiplier)
	// because losing data is more suspicious than gaining it.
	counts := map[string]int{"testdb": 60}
	spikes := d.verifyExportCounts(gitRepo, []string{"testdb"}, counts, 0.20)
	if len(spikes) != 1 {
		t.Fatalf("expected 1 spike for drop, got %d", len(spikes))
	}
	if spikes[0].Delta < 0.3 {
		t.Errorf("expected delta > 0.3, got %f", spikes[0].Delta)
	}
}

func TestVerifyExportCounts_SmallAbsoluteChangeIgnored(t *testing.T) {
	gitRepo := t.TempDir()
	initGitRepo(t, gitRepo)

	// Small database: 10 records
	dbDir := filepath.Join(gitRepo, "testdb")
	os.MkdirAll(dbDir, 0755)
	writeNLines(t, filepath.Join(dbDir, "issues.jsonl"), 10)
	commitAll(t, gitRepo, "baseline")

	d := &Daemon{logger: log.New(io.Discard, "", 0)}

	// 5 records = 50% drop, but only 5 records absolute change.
	// Below minAbsoluteDelta (20), so should NOT spike.
	counts := map[string]int{"testdb": 5}
	spikes := d.verifyExportCounts(gitRepo, []string{"testdb"}, counts, 0.20)
	if len(spikes) != 0 {
		t.Errorf("expected no spikes for small absolute change (<20 records), got %v", spikes)
	}
}

func TestVerifyExportCounts_AsymmetricThreshold(t *testing.T) {
	gitRepo := t.TempDir()
	initGitRepo(t, gitRepo)

	dbDir := filepath.Join(gitRepo, "testdb")
	os.MkdirAll(dbDir, 0755)
	writeNLines(t, filepath.Join(dbDir, "issues.jsonl"), 100)
	commitAll(t, gitRepo, "baseline")

	d := &Daemon{logger: log.New(io.Discard, "", 0)}

	// 70 records = 30% drop at 0.20 threshold → should spike (drops use base threshold)
	counts := map[string]int{"testdb": 70}
	spikes := d.verifyExportCounts(gitRepo, []string{"testdb"}, counts, 0.20)
	if len(spikes) != 1 {
		t.Fatalf("expected 1 spike for 30%% drop at 20%% threshold, got %d", len(spikes))
	}

	// 130 records = 30% increase at 0.20 threshold → should NOT spike (increases use 2x = 0.40)
	counts = map[string]int{"testdb": 130}
	spikes = d.verifyExportCounts(gitRepo, []string{"testdb"}, counts, 0.20)
	if len(spikes) != 0 {
		t.Errorf("expected no spike for 30%% increase at 40%% effective threshold, got %v", spikes)
	}
}

func TestVerifyExportCounts_SpikeBaselineRecovery(t *testing.T) {
	gitRepo := t.TempDir()
	initGitRepo(t, gitRepo)

	// Create baseline: 1000 lines in testdb/issues.jsonl
	dbDir := filepath.Join(gitRepo, "testdb")
	os.MkdirAll(dbDir, 0755)
	writeNLines(t, filepath.Join(dbDir, "issues.jsonl"), 1000)
	commitAll(t, gitRepo, "baseline")

	d := &Daemon{logger: log.New(io.Discard, "", 0)}

	// First run: 400 records = 60% drop → should spike and save baseline.
	counts := map[string]int{"testdb": 400}
	spikes := d.verifyExportCounts(gitRepo, []string{"testdb"}, counts, 0.50)
	if len(spikes) != 1 {
		t.Fatalf("expected 1 spike on first detection, got %d", len(spikes))
	}

	// Verify spike baseline was saved.
	sb := loadSpikeBaseline(gitRepo)
	if sb == nil {
		t.Fatal("expected spike baseline to be saved after spike detection")
	}
	if sb.Counts["testdb"] != 400 {
		t.Errorf("expected baseline count 400, got %d", sb.Counts["testdb"])
	}

	// Second run: same count (400) → stable vs spike baseline → should NOT spike.
	counts = map[string]int{"testdb": 400}
	spikes = d.verifyExportCounts(gitRepo, []string{"testdb"}, counts, 0.50)
	if len(spikes) != 0 {
		t.Errorf("expected no spikes on second run (stable vs baseline), got %v", spikes)
	}
}

func TestVerifyExportCounts_SpikeBaselineUnstable(t *testing.T) {
	gitRepo := t.TempDir()
	initGitRepo(t, gitRepo)

	dbDir := filepath.Join(gitRepo, "testdb")
	os.MkdirAll(dbDir, 0755)
	writeNLines(t, filepath.Join(dbDir, "issues.jsonl"), 1000)
	commitAll(t, gitRepo, "baseline")

	d := &Daemon{logger: log.New(io.Discard, "", 0)}

	// First run: 400 records = 60% drop → spikes and saves baseline at 400.
	counts := map[string]int{"testdb": 400}
	spikes := d.verifyExportCounts(gitRepo, []string{"testdb"}, counts, 0.50)
	if len(spikes) != 1 {
		t.Fatalf("expected 1 spike, got %d", len(spikes))
	}

	// Second run: 100 records = still a spike vs HEAD, AND unstable vs baseline
	// (400 → 100 = 75% drop, exceeds threshold). Should still spike.
	counts = map[string]int{"testdb": 100}
	spikes = d.verifyExportCounts(gitRepo, []string{"testdb"}, counts, 0.50)
	if len(spikes) != 1 {
		t.Errorf("expected 1 spike for unstable count vs baseline, got %d", len(spikes))
	}
}

func TestSpikeBaselineSaveLoadRemove(t *testing.T) {
	dir := t.TempDir()

	// No baseline file → nil.
	if sb := loadSpikeBaseline(dir); sb != nil {
		t.Errorf("expected nil, got %+v", sb)
	}

	// Save and load.
	counts := map[string]int{"db1": 100, "db2": 200}
	if err := saveSpikeBaseline(dir, counts); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	sb := loadSpikeBaseline(dir)
	if sb == nil {
		t.Fatal("expected non-nil after save")
	}
	if sb.Counts["db1"] != 100 || sb.Counts["db2"] != 200 {
		t.Errorf("unexpected counts: %v", sb.Counts)
	}

	// Remove.
	removeSpikeBaseline(dir)
	if sb := loadSpikeBaseline(dir); sb != nil {
		t.Errorf("expected nil after remove, got %+v", sb)
	}
}

func TestCountFileLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	writeNLines(t, path, 42)

	got, err := countFileLines(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 42 {
		t.Errorf("expected 42 lines, got %d", got)
	}
}

func TestCountFileLines_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	os.WriteFile(path, []byte(""), 0644)

	got, err := countFileLines(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("expected 0 lines, got %d", got)
	}
}

func TestParseLineCount(t *testing.T) {
	tests := []struct {
		input    string
		expected int
		wantErr  bool
	}{
		{"42", 42, false},
		{"  42 filename.jsonl", 42, false},
		{"  0", 0, false},
		{"", 0, true},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		got, err := parseLineCount(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseLineCount(%q): error = %v, wantErr %v", tt.input, err, tt.wantErr)
		}
		if got != tt.expected {
			t.Errorf("parseLineCount(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

// --- helpers ---

func splitNonEmpty(s string) []string {
	var result []string
	for _, line := range splitLines(s) {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init failed: %v: %s", err, out)
		}
	}
	// Need at least one commit for HEAD to exist.
	readme := filepath.Join(dir, "README")
	os.WriteFile(readme, []byte("init\n"), 0644)
	commitAll(t, dir, "init")
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	cmds := [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", msg, "--author=Test <test@test.com>"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v: %s", args, err, out)
		}
	}
}

func writeNLines(t *testing.T, path string, n int) {
	t.Helper()
	var buf []byte
	for i := 0; i < n; i++ {
		line, _ := json.Marshal(map[string]interface{}{"id": "rec-" + itoa(i), "title": "Record " + itoa(i)})
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

// --- gt-gct3r: silent-death fixes (repo path default, auto-create, loud failures) ---

func TestJsonlGitRepoPath_DefaultsToTownRoot(t *testing.T) {
	got := jsonlGitRepoPath(&JsonlGitBackupConfig{}, "/town")
	want := filepath.Join("/town", ".dolt-archive", "git")
	if got != want {
		t.Errorf("jsonlGitRepoPath default = %q, want %q", got, want)
	}
}

func TestJsonlGitRepoPath_ConfigOverride(t *testing.T) {
	got := jsonlGitRepoPath(&JsonlGitBackupConfig{GitRepo: "/custom/archive"}, "/town")
	if got != "/custom/archive" {
		t.Errorf("jsonlGitRepoPath override = %q, want /custom/archive", got)
	}
}

func TestEnsureJsonlGitRepo_CreatesWhenMissing(t *testing.T) {
	tmp := t.TempDir()
	gitRepo := filepath.Join(tmp, ".dolt-archive", "git")
	d := &Daemon{logger: log.New(io.Discard, "", 0)}

	if err := d.ensureJsonlGitRepo(gitRepo); err != nil {
		t.Fatalf("ensureJsonlGitRepo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gitRepo, ".git")); err != nil {
		t.Errorf("expected initialized git repo at %s: %v", gitRepo, err)
	}
}

func TestEnsureJsonlGitRepo_NoopWhenExists(t *testing.T) {
	gitRepo := t.TempDir()
	initGitRepo(t, gitRepo)
	d := &Daemon{logger: log.New(io.Discard, "", 0)}

	if err := d.ensureJsonlGitRepo(gitRepo); err != nil {
		t.Fatalf("ensureJsonlGitRepo on existing repo: %v", err)
	}
}

func TestJsonlBackupTickFailed_EscalatesAfterThreeConsecutive(t *testing.T) {
	var escalations []string
	d := &Daemon{
		logger:     log.New(io.Discard, "", 0),
		escalateFn: func(source, message string) { escalations = append(escalations, message) },
	}
	mol := &dogMol{stepIDs: map[string]string{}, logger: d.logger}

	d.jsonlBackupTickFailed(mol, "export", "boom")
	d.jsonlBackupTickFailed(mol, "export", "boom")
	if len(escalations) != 0 {
		t.Fatalf("escalated after %d failures, want none before 3", 2)
	}
	d.jsonlBackupTickFailed(mol, "export", "boom")
	if len(escalations) != 1 {
		t.Fatalf("got %d escalations after 3 failures, want 1", len(escalations))
	}
	if d.jsonlTickFailures != 0 {
		t.Errorf("counter = %d after escalation, want 0 (reset to avoid flooding)", d.jsonlTickFailures)
	}
	// Next failure starts a fresh streak — no immediate re-escalation.
	d.jsonlBackupTickFailed(mol, "export", "boom")
	if len(escalations) != 1 {
		t.Errorf("got %d escalations after 4th failure, want still 1", len(escalations))
	}
}

func TestSyncJsonlGitBackup_ZeroExportTicksAreLoud(t *testing.T) {
	tmp := t.TempDir()
	var escalations []string
	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: tmp},
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				JsonlGitBackup: &JsonlGitBackupConfig{
					Enabled: true,
					// Explicit database list so no auto-discovery happens in tests.
					Databases: []string{"nonexistent_test_db"},
				},
			},
		},
		logger:     log.New(&logBuf, "", 0),
		bdPath:     "/nonexistent/bd", // dogMol degrades gracefully, no real wisps
		escalateFn: func(source, message string) { escalations = append(escalations, message) },
	}

	// Data dir does not exist under tmp town root → every tick exports nothing.
	for i := 0; i < 3; i++ {
		d.syncJsonlGitBackup()
	}

	if !strings.Contains(logBuf.String(), "WARNING") {
		t.Errorf("zero-export tick did not log at WARNING level; log:\n%s", logBuf.String())
	}
	if len(escalations) != 1 {
		t.Fatalf("got %d escalations after 3 zero-export ticks, want 1; log:\n%s", len(escalations), logBuf.String())
	}
	// The repo should have been auto-created even though the tick failed later.
	if _, err := os.Stat(filepath.Join(tmp, ".dolt-archive", "git", ".git")); err != nil {
		t.Errorf("expected auto-created archive repo under town root: %v", err)
	}
}

// TestSyncJsonlGitBackup_EndToEnd_TwoTicks runs the full patrol twice against
// the package's shared Dolt test container: seed data → tick (export+commit) →
// mutate data → tick again. Verifies fresh JSONL exports and git commits per
// tick with zero escalations — the recovery criteria for gt-gct3r.
func TestSyncJsonlGitBackup_EndToEnd_TwoTicks(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt CLI not available")
	}
	portStr := os.Getenv("GT_DOLT_PORT")
	if portStr == "" {
		t.Skip("no shared Dolt container (GT_DOLT_PORT unset)")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("bad GT_DOLT_PORT %q: %v", portStr, err)
	}

	conn, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/?timeout=5s", port))
	if err != nil {
		t.Fatalf("connecting to Dolt container: %v", err)
	}
	defer conn.Close()

	const dbName = "jsonl_e2e"
	mustExec := func(q string) {
		t.Helper()
		if _, err := conn.Exec(q); err != nil {
			t.Fatalf("SQL %q: %v", q, err)
		}
	}
	mustExec("DROP DATABASE IF EXISTS " + dbName)
	mustExec("CREATE DATABASE " + dbName)
	t.Cleanup(func() { _, _ = conn.Exec("DROP DATABASE IF EXISTS " + dbName) })
	mustExec("CREATE TABLE " + dbName + ".issues (id varchar(64) PRIMARY KEY, title text, status varchar(32), issue_type varchar(32), ephemeral tinyint)")
	mustExec("INSERT INTO " + dbName + ".issues VALUES ('gt-e2e1','Backup record one','open','bug',0),('gt-e2e2','Backup record two','open','task',0)")

	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, ".dolt-data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}

	var escalations []string
	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: tmp},
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				JsonlGitBackup: &JsonlGitBackupConfig{
					Enabled:   true,
					Databases: []string{dbName},
				},
			},
		},
		doltServer: &DoltServerManager{config: &DoltServerConfig{
			Enabled: true,
			Host:    "127.0.0.1",
			Port:    port,
			User:    "root",
			DataDir: dataDir,
		}},
		logger:     log.New(&logBuf, "", 0),
		bdPath:     "/nonexistent/bd", // dogMol degrades gracefully, no real wisps
		escalateFn: func(source, message string) { escalations = append(escalations, message) },
	}

	gitRepo := filepath.Join(tmp, ".dolt-archive", "git")
	issuesPath := filepath.Join(gitRepo, dbName, "issues.jsonl")
	commitCount := func() int {
		t.Helper()
		out, err := exec.Command("git", "-C", gitRepo, "rev-list", "--count", "HEAD").Output()
		if err != nil {
			t.Fatalf("git rev-list: %v", err)
		}
		n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		return n
	}

	// Tick 1: repo auto-created, both records exported and committed.
	d.syncJsonlGitBackup()
	data, err := os.ReadFile(issuesPath)
	if err != nil {
		t.Fatalf("tick 1 produced no export: %v; log:\n%s", err, logBuf.String())
	}
	if got := strings.Count(string(data), "\n"); got != 2 {
		t.Errorf("tick 1 exported %d records, want 2; content:\n%s", got, data)
	}
	if got := commitCount(); got != 1 {
		t.Errorf("tick 1: %d commits, want 1; log:\n%s", got, logBuf.String())
	}

	// Mutate data, tick 2: export refreshed and committed again.
	mustExec("UPDATE " + dbName + ".issues SET title='Backup record one updated' WHERE id='gt-e2e1'")
	d.syncJsonlGitBackup()
	data, err = os.ReadFile(issuesPath)
	if err != nil {
		t.Fatalf("tick 2 export missing: %v", err)
	}
	if !strings.Contains(string(data), "Backup record one updated") {
		t.Errorf("tick 2 export is stale — updated title not present; content:\n%s", data)
	}
	if got := commitCount(); got != 2 {
		t.Errorf("tick 2: %d commits, want 2; log:\n%s", got, logBuf.String())
	}

	if len(escalations) != 0 {
		t.Errorf("healthy ticks escalated: %v", escalations)
	}
	if d.jsonlTickFailures != 0 {
		t.Errorf("jsonlTickFailures = %d after healthy ticks, want 0", d.jsonlTickFailures)
	}
}
