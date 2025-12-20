package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Spawn command flags
var (
	spawnIssue    string
	spawnMessage  string
	spawnCreate   bool
	spawnNoStart  bool
	spawnPolecat  string
	spawnRig      string
	spawnMolecule string
	spawnForce    bool
)

var spawnCmd = &cobra.Command{
	Use:     "spawn [rig/polecat | rig]",
	Aliases: []string{"sp"},
	Short:   "Spawn a polecat with work assignment",
	Long: `Spawn a polecat with a work assignment.

Assigns an issue or task to a polecat and starts a session. If no polecat
is specified, auto-selects an idle polecat in the rig.

When --molecule is specified, the molecule is first instantiated on the parent
issue (creating child steps), then the polecat is spawned on the first ready step.

Examples:
  gt spawn gastown/Toast --issue gt-abc
  gt spawn gastown --issue gt-def          # auto-select polecat
  gt spawn gastown/Nux -m "Fix the tests"  # free-form task
  gt spawn gastown/Capable --issue gt-xyz --create  # create if missing

  # Flag-based selection (rig inferred from current directory):
  gt spawn --issue gt-xyz --polecat Angharad
  gt spawn --issue gt-abc --rig gastown --polecat Toast

  # With molecule workflow:
  gt spawn --issue gt-abc --molecule mol-engineer-box`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSpawn,
}

func init() {
	spawnCmd.Flags().StringVar(&spawnIssue, "issue", "", "Beads issue ID to assign")
	spawnCmd.Flags().StringVarP(&spawnMessage, "message", "m", "", "Free-form task description")
	spawnCmd.Flags().BoolVar(&spawnCreate, "create", false, "Create polecat if it doesn't exist")
	spawnCmd.Flags().BoolVar(&spawnNoStart, "no-start", false, "Assign work but don't start session")
	spawnCmd.Flags().StringVar(&spawnPolecat, "polecat", "", "Polecat name (alternative to positional arg)")
	spawnCmd.Flags().StringVar(&spawnRig, "rig", "", "Rig name (defaults to current directory's rig)")
	spawnCmd.Flags().StringVar(&spawnMolecule, "molecule", "", "Molecule ID to instantiate on the issue")
	spawnCmd.Flags().BoolVar(&spawnForce, "force", false, "Force spawn even if polecat has unread mail")

	rootCmd.AddCommand(spawnCmd)
}

// BeadsIssue represents a beads issue from JSON output.
type BeadsIssue struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	Type        string `json:"issue_type"`
	Status      string `json:"status"`
}

func runSpawn(cmd *cobra.Command, args []string) error {
	if spawnIssue == "" && spawnMessage == "" {
		return fmt.Errorf("must specify --issue or -m/--message")
	}

	// --molecule requires --issue
	if spawnMolecule != "" && spawnIssue == "" {
		return fmt.Errorf("--molecule requires --issue to be specified")
	}

	// Find workspace first (needed for rig inference)
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	var rigName, polecatName string

	// Determine rig and polecat from positional arg or flags
	if len(args) > 0 {
		// Parse address: rig/polecat or just rig
		rigName, polecatName, err = parseSpawnAddress(args[0])
		if err != nil {
			return err
		}
	} else {
		// No positional arg - use flags
		polecatName = spawnPolecat
		rigName = spawnRig

		// If no --rig flag, infer from current directory
		if rigName == "" {
			rigName, err = inferRigFromCwd(townRoot)
			if err != nil {
				return fmt.Errorf("cannot determine rig: %w\nUse --rig to specify explicitly or provide rig/polecat as positional arg", err)
			}
		}
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return fmt.Errorf("rig '%s' not found", rigName)
	}

	// Get polecat manager
	polecatGit := git.NewGit(r.Path)
	polecatMgr := polecat.NewManager(r, polecatGit)

	// Auto-select polecat if not specified
	if polecatName == "" {
		polecatName, err = selectIdlePolecat(polecatMgr, r)
		if err != nil {
			// If --create is set, allocate a name from the pool
			if spawnCreate {
				polecatName, err = polecatMgr.AllocateName()
				if err != nil {
					return fmt.Errorf("allocating polecat name: %w", err)
				}
				fmt.Printf("Allocated polecat name: %s\n", polecatName)
			} else {
				return fmt.Errorf("auto-select polecat: %w", err)
			}
		} else {
			fmt.Printf("Auto-selected polecat: %s\n", polecatName)
		}
	}

	// Check/create polecat
	pc, err := polecatMgr.Get(polecatName)
	if err != nil {
		if err == polecat.ErrPolecatNotFound {
			if !spawnCreate {
				return fmt.Errorf("polecat '%s' not found (use --create to create)", polecatName)
			}
			fmt.Printf("Creating polecat %s...\n", polecatName)
			pc, err = polecatMgr.Add(polecatName)
			if err != nil {
				return fmt.Errorf("creating polecat: %w", err)
			}
		} else {
			return fmt.Errorf("getting polecat: %w", err)
		}
	}

	// Check polecat state
	if pc.State == polecat.StateWorking {
		return fmt.Errorf("polecat '%s' is already working on %s", polecatName, pc.Issue)
	}

	// Check for unread mail in polecat's inbox (indicates existing unstarted work)
	polecatAddress := fmt.Sprintf("%s/%s", rigName, polecatName)
	router := mail.NewRouter(r.Path)
	mailbox, err := router.GetMailbox(polecatAddress)
	if err == nil {
		_, unread, _ := mailbox.Count()
		if unread > 0 && !spawnForce {
			return fmt.Errorf("polecat '%s' has %d unread message(s) in inbox (possible existing work assignment)\nUse --force to override, or let the polecat process its inbox first",
				polecatName, unread)
		} else if unread > 0 {
			fmt.Printf("%s Polecat has %d unread message(s), proceeding with --force\n",
				style.Dim.Render("Warning:"), unread)
		}
	}

	// Beads operations use rig-level beads (at rig root, not mayor/rig)
	beadsPath := r.Path

	// Sync beads to ensure fresh state before spawn operations
	if err := syncBeads(beadsPath, true); err != nil {
		// Non-fatal - continue with possibly stale beads
		fmt.Printf("%s beads sync: %v\n", style.Dim.Render("Warning:"), err)
	}

	// Handle molecule instantiation if specified
	if spawnMolecule != "" {
		b := beads.New(beadsPath)

		// Get the molecule
		mol, err := b.Show(spawnMolecule)
		if err != nil {
			return fmt.Errorf("getting molecule %s: %w", spawnMolecule, err)
		}

		if mol.Type != "molecule" {
			return fmt.Errorf("%s is not a molecule (type: %s)", spawnMolecule, mol.Type)
		}

		// Validate the molecule
		if err := beads.ValidateMolecule(mol); err != nil {
			return fmt.Errorf("invalid molecule: %w", err)
		}

		// Get the parent issue
		parent, err := b.Show(spawnIssue)
		if err != nil {
			return fmt.Errorf("getting parent issue %s: %w", spawnIssue, err)
		}

		// Instantiate the molecule
		fmt.Printf("Instantiating molecule %s on %s...\n", spawnMolecule, spawnIssue)
		steps, err := b.InstantiateMolecule(mol, parent, beads.InstantiateOptions{})
		if err != nil {
			return fmt.Errorf("instantiating molecule: %w", err)
		}

		fmt.Printf("%s Created %d steps\n", style.Bold.Render("✓"), len(steps))
		for _, step := range steps {
			fmt.Printf("  %s: %s\n", style.Dim.Render(step.ID), step.Title)
		}

		// Find the first ready step (one with no dependencies)
		var firstReadyStep *beads.Issue
		for _, step := range steps {
			if len(step.DependsOn) == 0 {
				firstReadyStep = step
				break
			}
		}

		if firstReadyStep == nil {
			return fmt.Errorf("no ready step found in molecule (all steps have dependencies)")
		}

		// Switch to spawning on the first ready step
		fmt.Printf("\nSpawning on first ready step: %s\n", firstReadyStep.ID)
		spawnIssue = firstReadyStep.ID
	}

	// Get or create issue
	var issue *BeadsIssue
	var assignmentID string
	if spawnIssue != "" {
		// Use existing issue
		issue, err = fetchBeadsIssue(beadsPath, spawnIssue)
		if err != nil {
			return fmt.Errorf("fetching issue %s: %w", spawnIssue, err)
		}
		assignmentID = spawnIssue
	} else {
		// Create a beads issue for free-form task
		fmt.Printf("Creating beads issue for task...\n")
		issue, err = createBeadsTask(beadsPath, spawnMessage)
		if err != nil {
			return fmt.Errorf("creating task issue: %w", err)
		}
		assignmentID = issue.ID
		fmt.Printf("Created issue %s\n", assignmentID)
	}

	// Assign issue to polecat (sets issue.assignee in beads)
	if err := polecatMgr.AssignIssue(polecatName, assignmentID); err != nil {
		return fmt.Errorf("assigning issue: %w", err)
	}

	fmt.Printf("%s Assigned %s to %s/%s\n",
		style.Bold.Render("✓"),
		assignmentID, rigName, polecatName)

	// Sync beads to push assignment changes
	if err := syncBeads(beadsPath, false); err != nil {
		// Non-fatal warning
		fmt.Printf("%s beads push: %v\n", style.Dim.Render("Warning:"), err)
	}

	// Stop here if --no-start
	if spawnNoStart {
		fmt.Printf("\n  %s\n", style.Dim.Render("Use 'gt session start' to start the session"))
		return nil
	}

	// Send work assignment mail to polecat inbox (before starting session)
	// polecatAddress and router already defined above when checking for unread mail
	workMsg := buildWorkAssignmentMail(issue, spawnMessage, polecatAddress)

	fmt.Printf("Sending work assignment to %s inbox...\n", polecatAddress)
	if err := router.Send(workMsg); err != nil {
		return fmt.Errorf("sending work assignment: %w", err)
	}
	fmt.Printf("%s Work assignment sent\n", style.Bold.Render("✓"))

	// Start session
	t := tmux.NewTmux()
	sessMgr := session.NewManager(t, r)

	// Check if already running
	running, _ := sessMgr.IsRunning(polecatName)
	if running {
		// Session already running - send notification to check inbox
		fmt.Printf("Session already running, notifying to check inbox...\n")
		time.Sleep(500 * time.Millisecond) // Brief pause for notification
	} else {
		// Start new session - polecat will check inbox via gt prime startup hook
		fmt.Printf("Starting session for %s/%s...\n", rigName, polecatName)
		if err := sessMgr.Start(polecatName, session.StartOptions{}); err != nil {
			return fmt.Errorf("starting session: %w", err)
		}
		// Wait briefly for session to stabilize
		time.Sleep(1 * time.Second)
	}

	fmt.Printf("%s Session started. Attach with: %s\n",
		style.Bold.Render("✓"),
		style.Dim.Render(fmt.Sprintf("gt session at %s/%s", rigName, polecatName)))

	// TODO: Proper solution requires Witness/Deacon to monitor polecat startup
	// and detect when Claude is ready using AI intelligence. See gt-polecat-ready issue.
	// For now, use a fixed delay - SessionStart hook runs gt prime which tells polecat
	// to check mail, but we also send an explicit instruction as backup.
	sessionName := sessMgr.SessionName(polecatName)
	time.Sleep(5 * time.Second)

	// Send work instruction - backup in case SessionStart hook doesn't trigger action
	workInstruction := "Check your inbox with `gt mail inbox` and begin working on your assigned issue."
	if err := t.SendKeys(sessionName, workInstruction); err != nil {
		fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("Warning: could not send work instruction: %v", err)))
	}

	fmt.Printf("  %s\n", style.Dim.Render("Work instruction sent to polecat"))

	return nil
}

// parseSpawnAddress parses "rig/polecat" or "rig".
func parseSpawnAddress(addr string) (rigName, polecatName string, err error) {
	if strings.Contains(addr, "/") {
		parts := strings.SplitN(addr, "/", 2)
		if parts[0] == "" {
			return "", "", fmt.Errorf("invalid address: missing rig name")
		}
		return parts[0], parts[1], nil
	}
	return addr, "", nil
}


// selectIdlePolecat finds an idle polecat in the rig.
func selectIdlePolecat(mgr *polecat.Manager, r *rig.Rig) (string, error) {
	polecats, err := mgr.List()
	if err != nil {
		return "", err
	}

	// Prefer idle polecats
	for _, pc := range polecats {
		if pc.State == polecat.StateIdle {
			return pc.Name, nil
		}
	}

	// Accept active polecats without current work
	for _, pc := range polecats {
		if pc.State == polecat.StateActive && pc.Issue == "" {
			return pc.Name, nil
		}
	}

	// Check rig's polecat list for any we haven't loaded yet
	for _, name := range r.Polecats {
		found := false
		for _, pc := range polecats {
			if pc.Name == name {
				found = true
				break
			}
		}
		if !found {
			return name, nil
		}
	}

	return "", fmt.Errorf("no available polecats in rig '%s'", r.Name)
}

// fetchBeadsIssue gets issue details from beads CLI.
func fetchBeadsIssue(rigPath, issueID string) (*BeadsIssue, error) {
	cmd := exec.Command("bd", "show", issueID, "--json")
	cmd.Dir = rigPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return nil, fmt.Errorf("%s", errMsg)
		}
		return nil, err
	}

	// bd show --json returns an array, take the first element
	var issues []BeadsIssue
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return nil, fmt.Errorf("parsing issue: %w", err)
	}
	if len(issues) == 0 {
		return nil, fmt.Errorf("issue not found: %s", issueID)
	}

	return &issues[0], nil
}

// createBeadsTask creates a new beads task issue for a free-form task message.
func createBeadsTask(rigPath, message string) (*BeadsIssue, error) {
	// Truncate message for title if too long
	title := message
	if len(title) > 60 {
		title = title[:57] + "..."
	}

	// Use bd create to make a new task issue
	cmd := exec.Command("bd", "create",
		"--title="+title,
		"--type=task",
		"--priority=2",
		"--description="+message,
		"--json")
	cmd.Dir = rigPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return nil, fmt.Errorf("%s", errMsg)
		}
		return nil, err
	}

	// bd create --json returns the created issue
	var issue BeadsIssue
	if err := json.Unmarshal(stdout.Bytes(), &issue); err != nil {
		return nil, fmt.Errorf("parsing created issue: %w", err)
	}

	return &issue, nil
}

// syncBeads runs bd sync in the given directory.
// This ensures beads state is fresh before spawn operations.
func syncBeads(workDir string, fromMain bool) error {
	args := []string{"sync"}
	if fromMain {
		args = append(args, "--from-main")
	}
	cmd := exec.Command("bd", args...)
	cmd.Dir = workDir
	return cmd.Run()
}

// buildSpawnContext creates the initial context message for the polecat.
// Deprecated: Use buildWorkAssignmentMail instead for mail-based work assignment.
func buildSpawnContext(issue *BeadsIssue, message string) string {
	var sb strings.Builder

	sb.WriteString("[SPAWN] You have been assigned work.\n\n")

	if issue != nil {
		sb.WriteString(fmt.Sprintf("Issue: %s\n", issue.ID))
		sb.WriteString(fmt.Sprintf("Title: %s\n", issue.Title))
		sb.WriteString(fmt.Sprintf("Priority: P%d\n", issue.Priority))
		sb.WriteString(fmt.Sprintf("Type: %s\n", issue.Type))
		if issue.Description != "" {
			sb.WriteString(fmt.Sprintf("\nDescription:\n%s\n", issue.Description))
		}
	} else if message != "" {
		sb.WriteString(fmt.Sprintf("Task: %s\n", message))
	}

	sb.WriteString("\n## Workflow\n")
	sb.WriteString("1. Run `gt prime` to load polecat context\n")
	sb.WriteString("2. Run `bd sync --from-main` to get fresh beads\n")
	sb.WriteString("3. Work on your task, commit changes\n")
	sb.WriteString("4. Run `bd close <issue-id>` when done\n")
	sb.WriteString("5. Run `bd sync` to push beads changes\n")
	sb.WriteString("6. Push code: `git push origin HEAD`\n")
	sb.WriteString("7. Signal DONE with summary\n")

	return sb.String()
}

// buildWorkAssignmentMail creates a work assignment mail message for a polecat.
// This replaces tmux-based context injection with persistent mailbox delivery.
func buildWorkAssignmentMail(issue *BeadsIssue, message, polecatAddress string) *mail.Message {
	var subject string
	var body strings.Builder

	if issue != nil {
		subject = fmt.Sprintf("📋 Work Assignment: %s", issue.Title)

		body.WriteString(fmt.Sprintf("Issue: %s\n", issue.ID))
		body.WriteString(fmt.Sprintf("Title: %s\n", issue.Title))
		body.WriteString(fmt.Sprintf("Priority: P%d\n", issue.Priority))
		body.WriteString(fmt.Sprintf("Type: %s\n", issue.Type))
		if issue.Description != "" {
			body.WriteString(fmt.Sprintf("\nDescription:\n%s\n", issue.Description))
		}
	} else if message != "" {
		// Truncate for subject if too long
		titleText := message
		if len(titleText) > 50 {
			titleText = titleText[:47] + "..."
		}
		subject = fmt.Sprintf("📋 Work Assignment: %s", titleText)
		body.WriteString(fmt.Sprintf("Task: %s\n", message))
	}

	body.WriteString("\n## Workflow\n")
	body.WriteString("1. Run `gt prime` to load polecat context\n")
	body.WriteString("2. Run `bd sync --from-main` to get fresh beads\n")
	body.WriteString("3. Work on your task, commit changes\n")
	body.WriteString("4. Run `bd close <issue-id>` when done\n")
	body.WriteString("5. Run `bd sync` to push beads changes\n")
	body.WriteString("6. Push code: `git push origin HEAD`\n")
	body.WriteString("7. Signal DONE with summary\n")

	return &mail.Message{
		From:     "mayor/",
		To:       polecatAddress,
		Subject:  subject,
		Body:     body.String(),
		Priority: mail.PriorityHigh,
		Type:     mail.TypeTask,
	}
}
