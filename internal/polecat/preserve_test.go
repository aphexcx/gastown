package polecat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

func TestPreservePushRefspec(t *testing.T) {
	now := time.Date(2026, 6, 12, 9, 30, 45, 0, time.UTC)

	tests := []struct {
		name          string
		branch        string
		defaultBranch string
		wantRefspec   string
		wantRescue    string
	}{
		{
			name:          "polecat branch pushes to its own name",
			branch:        "polecat/nux/gt-abc@xyz",
			defaultBranch: "main",
			wantRefspec:   "polecat/nux/gt-abc@xyz:refs/heads/polecat/nux/gt-abc@xyz",
			wantRescue:    "",
		},
		{
			name:          "default branch main goes to rescue ref",
			branch:        "main",
			defaultBranch: "main",
			wantRefspec:   "main:refs/heads/rescue/nux-main-20260612-093045",
			wantRescue:    "rescue/nux-main-20260612-093045",
		},
		{
			name:          "default branch master goes to rescue ref",
			branch:        "master",
			defaultBranch: "master",
			wantRefspec:   "master:refs/heads/rescue/nux-master-20260612-093045",
			wantRescue:    "rescue/nux-master-20260612-093045",
		},
		{
			name:          "branch named main is safe when default is master",
			branch:        "main",
			defaultBranch: "master",
			wantRefspec:   "main:refs/heads/main",
			wantRescue:    "",
		},
		{
			name:          "slashes in default branch name are flattened in rescue ref",
			branch:        "release/v1",
			defaultBranch: "release/v1",
			wantRefspec:   "release/v1:refs/heads/rescue/nux-release-v1-20260612-093045",
			wantRescue:    "rescue/nux-release-v1-20260612-093045",
		},
		{
			name:          "detached HEAD goes to rescue ref, never a branch named HEAD",
			branch:        "HEAD",
			defaultBranch: "main",
			wantRefspec:   "HEAD:refs/heads/rescue/nux-detached-20260612-093045",
			wantRescue:    "rescue/nux-detached-20260612-093045",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refspec, rescue := PreservePushRefspec(tt.branch, tt.defaultBranch, "nux", now)
			if refspec != tt.wantRefspec {
				t.Errorf("refspec = %q, want %q", refspec, tt.wantRefspec)
			}
			if rescue != tt.wantRescue {
				t.Errorf("rescueBranch = %q, want %q", rescue, tt.wantRescue)
			}
		})
	}
}

func TestRigDefaultBranch_ConfigTakesPriority(t *testing.T) {
	rigPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte(`{"default_branch":"develop"}`), 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	// The git handle is only consulted when config has no default_branch;
	// pointing it at an empty dir proves config wins without a repo.
	got := RigDefaultBranch(rigPath, git.NewGit(t.TempDir()))
	if got != "develop" {
		t.Errorf("RigDefaultBranch = %q, want %q", got, "develop")
	}
}

// runPreserveGit runs a git command for preserve tests, failing the test on error.
func runPreserveGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupPreserveTestRepos creates a bare origin with one pushed commit on main
// and a working clone standing in for a polecat worktree. Returns the manager
// (rig path has no config.json, so default branch resolves from the remote),
// the origin path, and the clone path.
func setupPreserveTestRepos(t *testing.T) (*Manager, string, string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	clone := filepath.Join(root, "clone")

	runPreserveGit(t, "", "init", "--bare", "-b", "main", origin)
	runPreserveGit(t, "", "init", "-b", "main", clone)
	runPreserveGit(t, clone, "config", "user.email", "test@example.com")
	runPreserveGit(t, clone, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("# test\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	runPreserveGit(t, clone, "add", "README.md")
	runPreserveGit(t, clone, "commit", "-m", "initial")
	runPreserveGit(t, clone, "remote", "add", "origin", origin)
	runPreserveGit(t, clone, "push", "-u", "origin", "main")

	r := &rig.Rig{Name: "testrig", Path: root}
	return NewManager(r, git.NewGit(root), nil), origin, clone
}

// commitFile adds a commit touching the named file in the given repo.
func commitFile(t *testing.T, repo, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(name+"\n"), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runPreserveGit(t, repo, "add", name)
	runPreserveGit(t, repo, "commit", "-m", "add "+name)
}

// originRescueRefs lists refs/heads/rescue/* in the origin repo.
func originRescueRefs(t *testing.T, origin string) []string {
	t.Helper()
	out := runPreserveGit(t, origin, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func TestPreserveUnpushedWork_DefaultBranchGoesToRescueRef(t *testing.T) {
	m, origin, clone := setupPreserveTestRepos(t)

	mainBefore := runPreserveGit(t, origin, "rev-parse", "refs/heads/main")
	commitFile(t, clone, "wip.txt") // unpushed commit on local main
	wipSHA := runPreserveGit(t, clone, "rev-parse", "HEAD")

	m.preserveUnpushedWork(clone, "nux")

	if mainAfter := runPreserveGit(t, origin, "rev-parse", "refs/heads/main"); mainAfter != mainBefore {
		t.Errorf("origin main moved: %s -> %s (default branch must never be auto-pushed)", mainBefore, mainAfter)
	}
	rescues := originRescueRefs(t, origin)
	if len(rescues) != 1 {
		t.Fatalf("expected exactly 1 rescue ref, got %v", rescues)
	}
	if !strings.HasPrefix(rescues[0], "refs/heads/rescue/nux-main-") {
		t.Errorf("rescue ref %q, want prefix refs/heads/rescue/nux-main-", rescues[0])
	}
	if got := runPreserveGit(t, origin, "rev-parse", rescues[0]); got != wipSHA {
		t.Errorf("rescue ref points at %s, want unpushed commit %s", got, wipSHA)
	}
}

func TestPreserveUnpushedWork_PolecatBranchPushedAsIs(t *testing.T) {
	m, origin, clone := setupPreserveTestRepos(t)

	mainBefore := runPreserveGit(t, origin, "rev-parse", "refs/heads/main")
	branch := "polecat/nux/gt-test@abc123"
	runPreserveGit(t, clone, "checkout", "-b", branch)
	commitFile(t, clone, "work.txt")
	workSHA := runPreserveGit(t, clone, "rev-parse", "HEAD")

	m.preserveUnpushedWork(clone, "nux")

	if got := runPreserveGit(t, origin, "rev-parse", "refs/heads/"+branch); got != workSHA {
		t.Errorf("origin %s = %s, want %s", branch, got, workSHA)
	}
	if mainAfter := runPreserveGit(t, origin, "rev-parse", "refs/heads/main"); mainAfter != mainBefore {
		t.Errorf("origin main moved: %s -> %s", mainBefore, mainAfter)
	}
	if rescues := originRescueRefs(t, origin); len(rescues) != 0 {
		t.Errorf("unexpected rescue refs for non-default branch: %v", rescues)
	}
}

func TestPreserveUnpushedWork_DetachedHEADGoesToRescueRef(t *testing.T) {
	m, origin, clone := setupPreserveTestRepos(t)

	mainBefore := runPreserveGit(t, origin, "rev-parse", "refs/heads/main")
	runPreserveGit(t, clone, "checkout", "--detach")
	commitFile(t, clone, "detached.txt") // unpushed commit on detached HEAD
	detachedSHA := runPreserveGit(t, clone, "rev-parse", "HEAD")

	m.preserveUnpushedWork(clone, "nux")

	if mainAfter := runPreserveGit(t, origin, "rev-parse", "refs/heads/main"); mainAfter != mainBefore {
		t.Errorf("origin main moved: %s -> %s", mainBefore, mainAfter)
	}
	rescues := originRescueRefs(t, origin)
	if len(rescues) != 1 {
		t.Fatalf("expected exactly 1 rescue ref, got %v", rescues)
	}
	if !strings.HasPrefix(rescues[0], "refs/heads/rescue/nux-detached-") {
		t.Errorf("rescue ref %q, want prefix refs/heads/rescue/nux-detached-", rescues[0])
	}
	if got := runPreserveGit(t, origin, "rev-parse", rescues[0]); got != detachedSHA {
		t.Errorf("rescue ref points at %s, want detached commit %s", got, detachedSHA)
	}
}

func TestPreserveUnpushedWork_NothingUnpushedIsNoop(t *testing.T) {
	m, origin, clone := setupPreserveTestRepos(t)

	mainBefore := runPreserveGit(t, origin, "rev-parse", "refs/heads/main")

	m.preserveUnpushedWork(clone, "nux")

	if mainAfter := runPreserveGit(t, origin, "rev-parse", "refs/heads/main"); mainAfter != mainBefore {
		t.Errorf("origin main moved: %s -> %s", mainBefore, mainAfter)
	}
	if rescues := originRescueRefs(t, origin); len(rescues) != 0 {
		t.Errorf("unexpected rescue refs when nothing unpushed: %v", rescues)
	}
}
