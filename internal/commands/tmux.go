package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/spf13/cobra"
)

var (
	validScriptPathPattern = regexp.MustCompile(`^[a-zA-Z0-9/._-]+\.sh$`)
)

// NewTmuxCmd creates the tmux command
func NewTmuxCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tmux",
		Short: "Create tmux session for OCFP deployments",
		Long:  `Create and manage tmux sessions optimized for OCFP deployment workflows.`,
		RunE:  runTmuxCmd,
	}
}

func runTmuxCmd(cmd *cobra.Command, args []string) error {
	// Check if tmux is available
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux is not installed. Please install tmux to use this command")
	}

	// Find the tmux script
	scriptPath, err := findTmuxScript()
	if err != nil {
		return fmt.Errorf("tmux script not found: %w", err)
	}

	// Check if script is executable
	if err := ensureExecutable(scriptPath); err != nil {
		return fmt.Errorf("failed to make tmux script executable: %w", err)
	}

	// Execute the tmux script
	if err := security.ValidateInput(scriptPath, validScriptPathPattern); err != nil {
		return fmt.Errorf("invalid script path: %w", err)
	}
	execCmd := exec.Command(scriptPath) // #nosec G204 - scriptPath is validated above
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr
	execCmd.Stdin = os.Stdin

	if err := execCmd.Run(); err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	return nil
}

func findTmuxScript() (string, error) {
	// Get the directory where the binary is located
	execPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	execDir := filepath.Dir(execPath)

	// Look for the tmux script in various locations
	searchPaths := []string{
		filepath.Join(execDir, "..", "scripts", "tmux", "ocfp"),
		"scripts/tmux/ocfp",
		"/opt/ocfp/scripts/tmux/ocfp",
		filepath.Join(os.Getenv("HOME"), "ocfp", "ocfp-cli", "scripts", "tmux", "ocfp"),
	}

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	// If not found, create a basic tmux session creator
	return createBasicTmuxScript()
}

func createBasicTmuxScript() (string, error) {
	// Create a temporary script that creates the basic tmux session
	tempFile, err := os.CreateTemp("", "ocfp-tmux-*.sh")
	if err != nil {
		return "", err
	}

	scriptContent := `#!/bin/bash
# OCFP tmux session creator

# Check if session already exists
if tmux has-session -t ocfp 2>/dev/null; then
    echo "OCFP tmux session already exists. Attaching..."
    exec tmux attach-session -t ocfp
fi

# Create new session with first window
tmux new-session -d -s ocfp -n "bosh" -c "$HOME/ocfp/deployments/bosh" || exit 1

# Create additional windows for common deployments
windows=("vault" "shield" "doomsday" "prometheus" "concourse" "cf" "autoscaler" "scheduler" "jumpbox" "blacksmith")

for window in "${windows[@]}"; do
    deployment_dir="$HOME/ocfp/deployments/$window"
    if [ -d "$deployment_dir" ]; then
        tmux new-window -t ocfp -n "$window" -c "$deployment_dir"
    else
        tmux new-window -t ocfp -n "$window" -c "$HOME/ocfp"
    fi
done

# Configure tmux session
tmux set-option -t ocfp mouse on
tmux set-option -t ocfp base-index 0

# Select first window
tmux select-window -t ocfp:0

echo "OCFP tmux session created successfully"
echo "Attach with: tmux attach-session -t ocfp"
`

	if _, err := tempFile.WriteString(scriptContent); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempFile.Name())
		return "", err
	}
	_ = tempFile.Close()

	return tempFile.Name(), nil
}

func ensureExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.Mode()&0111 == 0 {
		return os.Chmod(path, info.Mode()|0755)
	}

	return nil
}
