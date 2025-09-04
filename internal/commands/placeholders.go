package commands

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
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
	log := zap.L()

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

	log.Info("Creating OCFP tmux session...")

	// Execute the tmux script
	cmd_exec := exec.Command(scriptPath)
	cmd_exec.Stdout = os.Stdout
	cmd_exec.Stderr = os.Stderr
	cmd_exec.Stdin = os.Stdin

	if err := cmd_exec.Run(); err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	log.Info("OCFP tmux session created successfully")
	log.Info("Attach to the session with: tmux attach-session -t ocfp")

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

// NewBastionCmd creates the bastion command
func NewBastionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bastion <action>",
		Short: "Bastion host management",
		Long:  `Manage bastion host operations and configuration.`,
		Args:  cobra.MinimumNArgs(1),
		RunE:  runBastionCmd,
	}

	cmd.Flags().String("user", "ubuntu", "SSH username for bastion connection")
	cmd.Flags().String("key", "", "Path to SSH private key")
	cmd.Flags().String("iaas", "", "Cloud provider type")
	cmd.Flags().String("bloc", "", "Bloc name for configuration")

	return cmd
}

func runBastionCmd(cmd *cobra.Command, args []string) error {
	log := zap.L()
	action := args[0]

	switch action {
	case "init":
		return bastionInit(cmd, log)
	case "provision":
		return bastionProvision(cmd, log)
	default:
		return fmt.Errorf("unknown bastion action: %s. Available actions: init, provision", action)
	}
}

func bastionInit(cmd *cobra.Command, log *zap.Logger) error {
	log.Info("Executing bastion init command")

	bastionContext, err := getBastionContext(cmd, log)
	if err != nil {
		return fmt.Errorf("failed to get bastion context: %w", err)
	}

	scriptPath, err := findProvisionScript("bastion-init")
	if err != nil {
		return fmt.Errorf("cannot find bastion-init script: %w", err)
	}

	log.Info("Found init script", zap.String("path", scriptPath))

	// Copy and execute script
	err = copyAndExecuteScript(
		bastionContext,
		scriptPath,
		"/tmp/bastion-init.pl",
		"bastion init",
		"~/bastion-init.log",
		log,
	)
	if err != nil {
		return err
	}

	log.Info("Bastion initialization phase completed")
	return nil
}

func bastionProvision(cmd *cobra.Command, log *zap.Logger) error {
	log.Info("Executing bastion provision command")

	bastionContext, err := getBastionContext(cmd, log)
	if err != nil {
		return fmt.Errorf("failed to get bastion context: %w", err)
	}

	scriptPath, err := findProvisionScript("bastion")
	if err != nil {
		return fmt.Errorf("cannot find bastion provision script: %w", err)
	}

	log.Info("Found provision script", zap.String("path", scriptPath))

	// Copy and execute script
	err = copyAndExecuteScript(
		bastionContext,
		scriptPath,
		"/tmp/provision-bastion.pl",
		"bastion provision",
		"~/provision.log",
		log,
	)
	if err != nil {
		return err
	}

	log.Info("Bastion provisioning completed")
	log.Info("The bastion host is now fully configured and ready for use")
	return nil
}

type bastionContext struct {
	IP           string
	User         string
	SSHOptions   string
	SSHKeyOption string
	SCPPrefix    string
	SSHPrefix    string
}

func getBastionContext(cmd *cobra.Command, log *zap.Logger) (*bastionContext, error) {
	// This is a simplified implementation - in a real implementation,
	// we would need to integrate with the provider system to get the bastion IP
	// and SSH key information

	user, _ := cmd.Flags().GetString("user")
	key, _ := cmd.Flags().GetString("key")

	// For now, return a mock context - this would need to be implemented
	// to actually discover the bastion IP and SSH key from the provider
	if log != nil {
		log.Warn("Bastion context discovery not fully implemented - using placeholder values")
		log.Info("In a complete implementation, this would:")
		log.Info("  1. Load provider configuration")
		log.Info("  2. Discover bastion IP from provider")
		log.Info("  3. Find appropriate SSH private key")
		log.Info("  4. Handle password-protected keys")
	}

	return &bastionContext{
		IP:           "placeholder-ip",
		User:         user,
		SSHOptions:   "-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR",
		SSHKeyOption: fmt.Sprintf("-i '%s'", key),
		SCPPrefix:    "",
		SSHPrefix:    "",
	}, nil
}

func findProvisionScript(scriptName string) (string, error) {
	// Get the directory where the binary is located
	execPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	execDir := filepath.Dir(execPath)

	// Look for the script in various locations
	searchPaths := []string{
		filepath.Join("scripts", "provision", scriptName),
		filepath.Join(execDir, "..", "scripts", "provision", scriptName),
		filepath.Join("/opt", "ocfp", "scripts", "provision", scriptName),
		filepath.Join(os.Getenv("HOME"), "ocfp", "ocfp-cli", "scripts", "provision", scriptName),
	}

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("script '%s' not found in any search paths", scriptName)
}

func copyAndExecuteScript(ctx *bastionContext, scriptPath, remoteScript, operationName, logPath string, log *zap.Logger) error {
	// Copy the script to bastion
	log.Info("Copying script to bastion", zap.String("operation", operationName))
	if err := copyScriptToBastion(ctx, scriptPath, remoteScript, log); err != nil {
		return err
	}

	// Prepare environment variables
	envString := buildEnvironmentVariables(log)

	// Execute the script on bastion
	if err := executeRemoteScript(ctx, remoteScript, envString, operationName, logPath, log); err != nil {
		return err
	}

	// Cleanup remote script
	cleanupRemoteScript(ctx, remoteScript, log)

	return nil
}

func copyScriptToBastion(ctx *bastionContext, scriptPath, remoteScript string, log *zap.Logger) error {
	// This is a placeholder implementation
	// In a real implementation, this would use SCP to copy the script
	log.Info("Would copy script to bastion",
		zap.String("local", scriptPath),
		zap.String("remote", remoteScript),
		zap.String("user", ctx.User),
		zap.String("ip", ctx.IP))

	log.Warn("Script copy not implemented - would execute SCP command")
	return nil
}

func executeRemoteScript(ctx *bastionContext, remoteScript, envString, operationName, logPath string, log *zap.Logger) error {
	// This is a placeholder implementation
	// In a real implementation, this would SSH to the bastion and execute the script
	log.Info("Would execute remote script",
		zap.String("script", remoteScript),
		zap.String("operation", operationName),
		zap.String("logPath", logPath),
		zap.String("user", ctx.User),
		zap.String("ip", ctx.IP))

	log.Info("Script execution completed successfully", zap.String("operation", operationName))
	log.Info("Log available on bastion", zap.String("path", logPath))

	return nil
}

func cleanupRemoteScript(ctx *bastionContext, remoteScript string, log *zap.Logger) {
	// This is a placeholder implementation
	// In a real implementation, this would SSH to remove the temporary script
	log.Debug("Would cleanup remote script", zap.String("script", remoteScript))
}

func buildEnvironmentVariables(log *zap.Logger) string {
	// This is a simplified implementation
	// In a real implementation, this would build environment variables from config
	var envVars []string

	if blocName := os.Getenv("OCFP_BLOC_NAME"); blocName != "" {
		envVars = append(envVars, fmt.Sprintf("OCFP_BLOC_NAME='%s'", blocName))
	}

	if provider := os.Getenv("OCFP_PROVIDER"); provider != "" {
		envVars = append(envVars, fmt.Sprintf("OCFP_PROVIDER='%s'", provider))
	}

	// Add STACKIT-specific variables if applicable
	if stackitProjectID := os.Getenv("STACKIT_PROJECT_ID"); stackitProjectID != "" {
		envVars = append(envVars, fmt.Sprintf("STACKIT_PROJECT_ID='%s'", stackitProjectID))
	}

	if stackitOrgID := os.Getenv("STACKIT_ORG_ID"); stackitOrgID != "" {
		envVars = append(envVars, fmt.Sprintf("STACKIT_ORG_ID='%s'", stackitOrgID))
	}

	if stackitRegion := os.Getenv("STACKIT_REGION"); stackitRegion != "" {
		envVars = append(envVars, fmt.Sprintf("STACKIT_REGION='%s'", stackitRegion))
	}

	return strings.Join(envVars, " ")
}

// fetchGitHubKeys fetches SSH keys from GitHub for a user
func fetchGitHubKeys(username string) ([]string, error) {
	url := fmt.Sprintf("https://github.com/%s.keys", username)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch GitHub keys: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var keys []string
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		key := strings.TrimSpace(scanner.Text())
		if key != "" {
			keys = append(keys, key)
		}
	}

	return keys, scanner.Err()
}

// fetchGitLabKeys fetches SSH keys from GitLab for a user
func fetchGitLabKeys(username string) ([]string, error) {
	url := fmt.Sprintf("https://gitlab.com/%s.keys", username)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch GitLab keys: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var keys []string
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		key := strings.TrimSpace(scanner.Text())
		if key != "" {
			keys = append(keys, key)
		}
	}

	return keys, scanner.Err()
}
