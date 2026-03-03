package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// testSocket is the package-level isolated tmux socket used by all tests.
// Set by TestMain so newTestTmux() can return a properly isolated Tmux instance.
var testSocket string

// TestMain sets up a shared isolated tmux server for the package, runs all
// tests, then tears down the server. Using a single named socket:
//   - isolates tests from the user's interactive tmux sessions
//   - avoids session name collisions across packages running in parallel
//   - ensures cleanup even if individual tests panic
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("tmux"); err != nil {
		// tmux not installed; all tests that need it will self-skip via hasTmux().
		os.Exit(m.Run())
	}

	testSocket = fmt.Sprintf("gt-test-%d", os.Getpid())
	SetDefaultSocket(testSocket)

	// Start the server by creating a throwaway session.
	_ = exec.Command("tmux", "-L", testSocket, "new-session", "-d", "-s", "bootstrap").Run()

	code := m.Run()

	// Tear down the entire test tmux server.
	_ = exec.Command("tmux", "-L", testSocket, "kill-server").Run()

	os.Exit(code)
}
