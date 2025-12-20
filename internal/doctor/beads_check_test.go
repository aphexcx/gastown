package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewBeadsDatabaseCheck(t *testing.T) {
	check := NewBeadsDatabaseCheck()

	if check.Name() != "beads-database" {
		t.Errorf("expected name 'beads-database', got %q", check.Name())
	}

	if !check.CanFix() {
		t.Error("expected CanFix to return true")
	}
}

func TestBeadsDatabaseCheck_NoBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()

	check := NewBeadsDatabaseCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", result.Status)
	}
}

func TestBeadsDatabaseCheck_NoDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsDatabaseCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v", result.Status)
	}
}

func TestBeadsDatabaseCheck_EmptyDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create empty database
	dbPath := filepath.Join(beadsDir, "issues.db")
	if err := os.WriteFile(dbPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Create JSONL with content
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsDatabaseCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusError {
		t.Errorf("expected StatusError for empty db with content in jsonl, got %v", result.Status)
	}
}

func TestBeadsDatabaseCheck_PopulatedDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create database with content
	dbPath := filepath.Join(beadsDir, "issues.db")
	if err := os.WriteFile(dbPath, []byte("SQLite format 3"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsDatabaseCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for populated db, got %v", result.Status)
	}
}
