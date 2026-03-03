package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/spf13/cobra"
)

const (
	// File permission constants.
	TmuxScriptExecuteMode = 0755 // Standard executable permissions for scripts
)

var (
	validScriptPathPattern = regexp.MustCompile(`^[a-zA-Z0-9/._-]+\.sh$`)
)

// NewTmuxCmd creates the tmux command.
func NewTmuxCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	return &cobra.Command{
		Use:   "tmux",
		Short: "Create tmux session for OCFP deployments",
		Long:  `Create and manage tmux sessions optimized for OCFP deployment workflows.`,
		RunE:  runTmuxCmd,
	}
}

func runTmuxCmd(cmd *cobra.Command, args []string) error {
	// Check if tmux is available
	_, err := exec.LookPath("tmux")
	if err != nil {
		return ErrTmuxNotInstalled
	}

	// Find the tmux script
	scriptPath, err := FindTmuxScript()
	if err != nil {
		return fmt.Errorf("tmux script not found: %w", err)
	}

	// Check if script is executable
	err = EnsureExecutable(scriptPath)
	if err != nil {
		return fmt.Errorf("failed to make tmux script executable: %w", err)
	}

	// Execute the tmux script
	err = security.ValidateInput(scriptPath, validScriptPathPattern)
	if err != nil {
		return fmt.Errorf("invalid script path: %w", err)
	}

	execCmd := exec.CommandContext(context.Background(), scriptPath) // #nosec G204 - scriptPath is validated above
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr
	execCmd.Stdin = os.Stdin

	err = execCmd.Run()
	if err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	return nil
}

func FindTmuxScript() (string, error) {
	// Get the directory where the binary is located
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
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
		_, err := os.Stat(path) //nolint:gosec // path components are from trusted config
		if err == nil {
			return path, nil
		}
	}

	// If not found, create a basic tmux session creator
	return CreateBasicTmuxScript()
}

func CreateBasicTmuxScript() (string, error) {
	// Create a temporary script that creates the basic tmux session
	tempFile, err := os.CreateTemp("", "ocfp-tmux-*.sh")
	if err != nil {
		return "", fmt.Errorf("failed to create temp script file: %w", err)
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

	_, err = tempFile.WriteString(scriptContent)
	if err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempFile.Name()) //nolint:gosec // path from os.CreateTemp is trusted

		return "", fmt.Errorf("failed to write script content: %w", err)
	}

	_ = tempFile.Close()

	return tempFile.Name(), nil
}

func EnsureExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	if info.Mode()&0111 == 0 {
		err := os.Chmod(path, info.Mode()|TmuxScriptExecuteMode)
		if err != nil {
			return fmt.Errorf("failed to make file executable: %w", err)
		}
	}

	return nil
}
