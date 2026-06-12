package polecat

import (
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
)

// preserveUnpushedWork best-effort pushes the polecat's current branch to
// origin before its worktree is removed, so committed-but-unpushed work
// survives the nuke. Failures are warnings, not errors — the nuke proceeds.
//
// The rig's default branch is guarded via PreservePushRefspec: its work goes
// to a rescue ref, never to origin/<default> directly (P0 gt-3zkxr).
func (m *Manager) preserveUnpushedWork(clonePath, name string) {
	polecatGit := git.NewGit(clonePath)
	branch, brErr := polecatGit.CurrentBranch()
	if brErr != nil || branch == "" {
		return
	}
	pushed, unpushedCount, checkErr := polecatGit.BranchPushedToRemote(branch, "origin")
	if checkErr != nil || pushed || unpushedCount == 0 {
		return
	}

	refspec, rescueBranch := PreservePushRefspec(branch, RigDefaultBranch(m.rig.Path, polecatGit), name, time.Now())
	if pushErr := polecatGit.Push("origin", refspec, false); pushErr != nil {
		style.PrintWarning("could not push branch %s before removal (%d unpushed commit(s)): %v",
			branch, unpushedCount, pushErr)
		style.PrintWarning("WORK AT RISK: branch %s has %d unpushed commit(s) in worktree %s",
			branch, unpushedCount, clonePath)
		return
	}
	if rescueBranch != "" {
		style.PrintWarning("not pushing %s directly — preserved %d unpushed commit(s) to origin/%s",
			branch, unpushedCount, rescueBranch)
	}
}

// RigDefaultBranch resolves the rig's default branch for push-guard decisions.
// Order: rig config (default_branch in config.json), then the remote's
// origin/HEAD via the given repo handle. Never returns "" — the git fallback
// chain ends at "main".
func RigDefaultBranch(rigPath string, g *git.Git) string {
	if rigCfg, err := rig.LoadRigConfig(rigPath); err == nil && rigCfg.DefaultBranch != "" {
		return rigCfg.DefaultBranch
	}
	return g.RemoteDefaultBranch()
}

// PreservePushRefspec decides where a best-effort work-preservation push
// (nuke, doctor --fix) should send a polecat's branch.
//
// The rig's default branch is never pushed to its own name: a polecat
// worktree sitting on a local default branch with unpushed commits would
// fast-forward mainline with unreviewed WIP (P0 gt-3zkxr, where a nuke
// published 49 unreviewed commits to origin/main). Default-branch work is
// parked on a timestamped rescue ref instead. All other branches
// (polecat/*, dog/*, pr/*, feature branches) push to their own name.
//
// Returns the refspec to push and, when the guard fired, the rescue branch
// name so callers can report where the work went.
func PreservePushRefspec(branch, defaultBranch, polecatName string, now time.Time) (refspec, rescueBranch string) {
	ts := now.UTC().Format("20060102-150405")
	// Detached HEAD: rev-parse --abbrev-ref reports the literal string "HEAD".
	// Pushing it to its own name would create a remote branch named "HEAD";
	// park the detached commits on a rescue ref instead.
	if branch == "HEAD" {
		rescueBranch = fmt.Sprintf("rescue/%s-detached-%s", polecatName, ts)
		return "HEAD:refs/heads/" + rescueBranch, rescueBranch
	}
	if branch != defaultBranch {
		return branch + ":refs/heads/" + branch, ""
	}
	rescueBranch = fmt.Sprintf("rescue/%s-%s-%s",
		polecatName, strings.ReplaceAll(branch, "/", "-"), ts)
	return branch + ":refs/heads/" + rescueBranch, rescueBranch
}
