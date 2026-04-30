package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/slack"
)

var slackDaemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the Slack router in the foreground",
	Long: `Run the Slack router daemon in the foreground. Normally you want
'gt slack start' instead, which runs this in the background with a PID file.`,
	RunE: runSlackDaemonForeground,
}

var slackStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Slack router daemon in the background",
	Long: `Fork 'gt slack daemon' into the background. Writes a PID file at
~/gt/.runtime/slack.pid. Prints an error if a daemon is already running.`,
	RunE: runSlackStart,
}

var slackStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running Slack router daemon",
	RunE:  runSlackStop,
}

var slackStatusCmd = &cobra.Command{
	Use:         "status",
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Show Slack router daemon status",
	RunE:        runSlackStatus,
}

func init() {
	slackCmd.AddCommand(slackDaemonCmd)
	slackCmd.AddCommand(slackStartCmd)
	slackCmd.AddCommand(slackStopCmd)
	slackCmd.AddCommand(slackStatusCmd)
}

// ---- daemon (foreground) ----

func runSlackDaemonForeground(cmd *cobra.Command, _ []string) error {
	cfgPath, err := slack.DefaultConfigPath()
	if err != nil {
		return err
	}
	// Don't load the config here — NewDaemon does that and sets up the
	// mtime-cached reloader. We only pass the path.

	townRoot, err := findMailWorkDir()
	if err != nil {
		return fmt.Errorf("find town root: %w", err)
	}

	pidPath, err := slack.PIDFilePath()
	if err != nil {
		return err
	}
	if existingPID, err := slack.ReadPIDFile(pidPath); err == nil && slack.IsProcessAlive(existingPID) {
		return fmt.Errorf("daemon already running with pid %d (stop it first: gt slack stop)", existingPID)
	}
	if err := slack.WritePIDFile(pidPath); err != nil {
		return err
	}
	defer slack.RemovePIDFile(pidPath)

	d, err := slack.NewDaemon(slack.DaemonOptions{
		ConfigPath: cfgPath,
		TownRoot:   townRoot,
	})
	if err != nil {
		return fmt.Errorf("init daemon: %w", err)
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		logShutdownForensics(cmd.OutOrStderr(), sig)
		cancel()
	}()

	fmt.Fprintf(cmd.OutOrStdout(), "slack daemon starting (pid %d, town %s)\n", os.Getpid(), townRoot)
	if err := d.Run(ctx); err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "slack daemon stopped")
	return nil
}

// ---- start (background) ----

func runSlackStart(cmd *cobra.Command, _ []string) error {
	pidPath, err := slack.PIDFilePath()
	if err != nil {
		return err
	}
	if existingPID, err := slack.ReadPIDFile(pidPath); err == nil && slack.IsProcessAlive(existingPID) {
		return fmt.Errorf("daemon already running with pid %d", existingPID)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find gt binary: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	logPath := filepath.Join(home, "gt", ".runtime", "slack.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	fg := exec.Command(exe, "slack", "daemon")
	fg.Stdout = logFile
	fg.Stderr = logFile
	fg.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := fg.Start(); err != nil {
		return fmt.Errorf("fork daemon: %w", err)
	}

	// Detach so the background process doesn't inherit our wait.
	go func() { _ = fg.Wait() }()

	// Wait up to 3s for the PID file to show up.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pid, err := slack.ReadPIDFile(pidPath); err == nil && slack.IsProcessAlive(pid) {
			fmt.Fprintf(cmd.OutOrStdout(), "slack daemon started (pid %d, logs %s)\n", pid, logPath)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not come up within 3s — check %s", logPath)
}

// ---- stop ----

func runSlackStop(cmd *cobra.Command, _ []string) error {
	pidPath, err := slack.PIDFilePath()
	if err != nil {
		return err
	}
	pid, err := slack.ReadPIDFile(pidPath)
	if err != nil {
		return fmt.Errorf("no PID file at %s — is the daemon running?", pidPath)
	}
	if !slack.IsProcessAlive(pid) {
		_ = slack.RemovePIDFile(pidPath)
		return fmt.Errorf("pid %d not alive — removed stale PID file", pid)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM: %w", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !slack.IsProcessAlive(pid) {
			_ = slack.RemovePIDFile(pidPath)
			fmt.Fprintf(cmd.OutOrStdout(), "slack daemon stopped (pid %d)\n", pid)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Fall through to SIGKILL with a warning.
	fmt.Fprintf(cmd.OutOrStderr(), "slack: daemon did not exit within 10s, sending SIGKILL\n")
	_ = proc.Signal(syscall.SIGKILL)
	_ = slack.RemovePIDFile(pidPath)
	return nil
}

// logShutdownForensics captures evidence about who/what asked the daemon to
// stop. The cycling-SIGTERM bug (mayor 2026-04-30) was diagnosed blind because
// the signal handler only logged "shutdown signal received" with no source
// info. We now record:
//   - timestamp + signal name
//   - our pid + parent pid (launchd/1 means we were orphaned long ago)
//   - a ps snapshot of current `gt slack stop|kill` invocations, to catch
//     the sender if they're still alive
func logShutdownForensics(w io.Writer, sig os.Signal) {
	ppid := os.Getppid()
	fmt.Fprintf(w, "slack: shutdown signal received sig=%v pid=%d ppid=%d t=%s\n",
		sig, os.Getpid(), ppid, time.Now().Format(time.RFC3339Nano))

	// Best-effort ppid identity. On macOS ppid==1 is launchd, meaning the
	// original `gt slack start` parent exited long ago — expected after
	// daemonization, not a smoking gun on its own.
	if out, err := exec.Command("ps", "-o", "pid,user,command", "-p",
		fmt.Sprint(ppid)).Output(); err == nil {
		fmt.Fprintf(w, "slack: shutdown ppid info:\n%s", out)
	}

	// Snapshot any currently-running `gt slack stop`, `kill <pid>`, or `pkill`
	// processes. The sender of a SIGTERM usually exits within milliseconds, so
	// this is racy — but when it works it's the smoking gun.
	out, err := exec.Command("ps", "-axo", "pid,ppid,start,command").Output()
	if err != nil {
		return
	}
	myPID := fmt.Sprint(os.Getpid())
	flags := []string{"gt slack stop", "pkill", "killall", "slack daemon"}
	for _, line := range strings.Split(string(out), "\n") {
		flagged := false
		for _, f := range flags {
			if strings.Contains(line, f) {
				flagged = true
				break
			}
		}
		if !flagged && strings.Contains(line, "kill") && strings.Contains(line, myPID) {
			flagged = true
		}
		if flagged {
			fmt.Fprintf(w, "slack: shutdown nearby proc: %s\n", line)
		}
	}
}

// ---- status ----

func runSlackStatus(cmd *cobra.Command, _ []string) error {
	pidPath, err := slack.PIDFilePath()
	if err != nil {
		return err
	}
	pid, err := slack.ReadPIDFile(pidPath)
	if err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), "slack daemon: not running (no PID file)")
		return nil
	}
	if !slack.IsProcessAlive(pid) {
		fmt.Fprintf(cmd.OutOrStdout(), "slack daemon: pid %d recorded but not alive (stale PID file at %s)\n",
			pid, pidPath)
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "slack daemon: running (pid %d)\n", pid)
	fmt.Fprintf(cmd.OutOrStdout(), "  pid file:   %s\n", pidPath)

	// Live observability comes from the snapshot file the daemon writes
	// every 5s. If the file is missing, the daemon is alive but hasn't
	// written yet — that's fine on a fresh start, report what we can.
	townRoot, err := findMailWorkDir()
	if err != nil {
		return nil
	}
	snap, err := slack.LoadStatusSnapshot(slack.StatusPath(townRoot))
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "  status:     snapshot unavailable (%v)\n", err)
		return nil
	}

	uptime := time.Since(snap.StartedAt).Round(time.Second)
	fmt.Fprintf(cmd.OutOrStdout(), "  uptime:     %s\n", uptime)
	fmt.Fprintf(cmd.OutOrStdout(), "  connection: %s\n", snap.Connection)
	if !snap.LastInboundAt.IsZero() {
		fmt.Fprintf(cmd.OutOrStdout(), "  last in:    %s ago\n",
			time.Since(snap.LastInboundAt).Round(time.Second))
	}
	if !snap.LastPostAt.IsZero() {
		fmt.Fprintf(cmd.OutOrStdout(), "  last out:   %s ago\n",
			time.Since(snap.LastPostAt).Round(time.Second))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  pending:    %d outbox\n", snap.Pending)
	if snap.Failed > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  failed:     %d in %s/slack_outbox/failed/\n",
			snap.Failed, townRoot)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  channels:   %d opted in\n", len(snap.Channels))
	if len(snap.Routing) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "  routing:    (empty — no live agent sessions)")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "  routing:    %d agents\n", len(snap.Routing))
		names := make([]string, 0, len(snap.Routing))
		for name := range snap.Routing {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(cmd.OutOrStdout(), "              @%s → %s\n", name, snap.Routing[name])
		}
	}
	return nil
}
