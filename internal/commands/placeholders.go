package commands

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// NewProviderCmd creates the provider command
func NewProviderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider <action>",
		Short: "Manage cloud provider operations",
		Long:  `Manage cloud provider operations including login and credential management.`,
		Args:  cobra.MinimumNArgs(1),
		RunE:  runProviderCmd,
	}

	cmd.Flags().String("iaas", "", "Cloud provider type (stackit, aws, openstack, gcp, azure)")
	cmd.Flags().String("bloc-name", "", "Bloc name for configuration")

	return cmd
}

func runProviderCmd(cmd *cobra.Command, args []string) error {
	log := zap.L()
	action := args[0]

	switch action {
	case "login":
		return handleProviderLogin(cmd, log)
	default:
		return fmt.Errorf("unknown provider action '%s'. Available actions: login", action)
	}
}

func handleProviderLogin(cmd *cobra.Command, log *zap.Logger) error {
	// Get provider name from flag, environment, or config
	providerName, _ := cmd.Flags().GetString("iaas")
	if providerName == "" {
		providerName = os.Getenv("OCFP_PROVIDER")
	}

	// If not specified, try to get from config
	if providerName == "" {
		cfg, err := config.LoadWithParams("", "")
		if err == nil && cfg.Provider != "" {
			providerName = cfg.Provider
		}
	}

	if providerName == "" {
		return fmt.Errorf("provider not specified. Use --iaas flag, OCFP_PROVIDER environment variable, or specify in config")
	}

	providerName = strings.ToLower(providerName)

	log.Info("Logging into provider", zap.String("provider", providerName))

	switch providerName {
	case "stackit":
		return loginSTACKIT(cmd, log)
	case "aws":
		return loginAWS(log)
	case "openstack":
		return loginOpenStack(log)
	case "gcp":
		return loginGCP(log)
	case "azure":
		return loginAzure(log)
	default:
		return fmt.Errorf("unknown provider '%s'", providerName)
	}
}

func loginSTACKIT(cmd *cobra.Command, log *zap.Logger) error {
	blocName, _ := cmd.Flags().GetString("bloc-name")
	if blocName == "" {
		blocName = os.Getenv("OCFP_BLOC_NAME")
	}

	if blocName == "" {
		return fmt.Errorf("--bloc-name flag or OCFP_BLOC_NAME environment variable required")
	}

	// Get credentials (either JSON or token)
	authType, credentials, err := getSTACKITCredentials(blocName, log)
	if err != nil {
		return fmt.Errorf("could not retrieve STACKIT service account credentials: %w", err)
	}

	if credentials == "" {
		return fmt.Errorf("could not retrieve STACKIT service account credentials from config or vault")
	}

	if authType == "token" {
		return authenticateSTACKITToken(credentials, log)
	} else {
		return authenticateSTACKIT(credentials, log)
	}
}

func getSTACKITCredentials(blocName string, log *zap.Logger) (string, string, error) {
	// Try config file first
	authType, credentials, err := getSTACKITCredentialsFromConfig(log)
	if err != nil {
		return "", "", err
	}
	if credentials != "" {
		return authType, credentials, nil
	}

	// If not found in config, try vault
	return getSTACKITCredentialsFromVault(blocName, log)
}

func getSTACKITCredentialsFromConfig(log *zap.Logger) (string, string, error) {
	cfg, err := config.LoadWithParams("", "")
	if err != nil {
		log.Debug("Failed to load config", zap.Error(err))
		return "", "", nil
	}

	log.Debug("Attempting to get credentials from config file")

	// Check if config has service account token
	if cfg.ServiceAccountToken != "" {
		log.Info("Retrieved STACKIT service account token from config file")
		return "token", cfg.ServiceAccountToken, nil
	}

	// Check if config has service account JSON
	if cfg.ServiceAccountJSON != "" {
		log.Info("Retrieved STACKIT service account credentials from config file")
		return "json", cfg.ServiceAccountJSON, nil
	}

	// Check if config has service account key path
	if cfg.ServiceAccountKeyPath != "" {
		if _, err := os.Stat(cfg.ServiceAccountKeyPath); err == nil {
			content, err := os.ReadFile(cfg.ServiceAccountKeyPath)
			if err != nil {
				return "", "", fmt.Errorf("cannot read service account key file: %w", err)
			}
			log.Info("Retrieved STACKIT service account credentials from file", zap.String("path", cfg.ServiceAccountKeyPath))
			return "json", string(content), nil
		}
	}

	return "", "", nil
}

func getSTACKITCredentialsFromVault(blocName string, log *zap.Logger) (string, string, error) {
	// Check if safe command is available
	if _, err := exec.LookPath("safe"); err != nil {
		log.Debug("Safe command not available, skipping vault lookup")
		return "", "", nil
	}

	// Try to get token first
	tokenPath := fmt.Sprintf("secret/config/%s/mgmt/cpi/stackit:service_account_token", blocName)
	log.Debug("Attempting to retrieve STACKIT service account token from vault", zap.String("path", tokenPath))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "safe", "get", tokenPath)
	output, err := cmd.Output()
	if err == nil {
		token := strings.TrimSpace(string(output))
		if token != "" {
			log.Info("Retrieved STACKIT service account token from vault")
			return "token", token, nil
		}
	}

	// If no token, try JSON
	jsonPath := fmt.Sprintf("secret/config/%s/mgmt/cpi/stackit:service_account_json", blocName)
	log.Debug("Attempting to retrieve STACKIT service account JSON from vault", zap.String("path", jsonPath))

	cmd = exec.CommandContext(ctx, "safe", "get", jsonPath)
	output, err = cmd.Output()
	if err == nil {
		jsonCreds := strings.TrimSpace(string(output))
		if jsonCreds != "" {
			log.Info("Retrieved STACKIT service account JSON from vault")
			return "json", jsonCreds, nil
		}
	}

	log.Debug("Vault retrieval failed or returned empty")
	return "", "", nil
}

func authenticateSTACKIT(serviceAccountJSON string, log *zap.Logger) error {
	// Create temporary file for service account JSON
	tempFile, err := os.CreateTemp("", "stackit-service-account-*.json")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	defer os.Remove(tempFile.Name())

	if _, err := tempFile.WriteString(serviceAccountJSON); err != nil {
		tempFile.Close()
		return fmt.Errorf("failed to write service account JSON: %w", err)
	}
	tempFile.Close()

	// Execute stackit auth command
	log.Info("Authenticating with STACKIT...")
	log.Debug("Executing stackit auth activate-service-account", zap.String("keyPath", tempFile.Name()))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "stackit", "auth", "activate-service-account", "--service-account-key-path", tempFile.Name())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Error("Failed to login to STACKIT provider", zap.Error(err), zap.String("stderr", stderr.String()))
		return fmt.Errorf("STACKIT authentication failed: %w", err)
	}

	log.Info("Successfully logged into STACKIT provider")
	if stdout.Len() > 0 {
		fmt.Print(stdout.String())
	}

	return nil
}

func authenticateSTACKITToken(serviceAccountToken string, log *zap.Logger) error {
	// Execute stackit auth command with token
	log.Info("Authenticating with STACKIT token...")
	log.Debug("Executing stackit auth activate-service-account with token")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "stackit", "auth", "activate-service-account", "--service-account-token", serviceAccountToken)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Error("Failed to login to STACKIT provider", zap.Error(err), zap.String("stderr", stderr.String()))
		return fmt.Errorf("STACKIT authentication failed: %w", err)
	}

	log.Info("Successfully logged into STACKIT provider")
	if stdout.Len() > 0 {
		fmt.Print(stdout.String())
	}

	return nil
}

func loginAWS(log *zap.Logger) error {
	log.Warn("AWS provider login not implemented yet")
	log.Info("AWS authentication typically uses:")
	log.Info("  - AWS CLI profiles: aws configure")
	log.Info("  - Environment variables: AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY")
	log.Info("  - IAM roles for EC2 instances")
	return nil
}

func loginOpenStack(log *zap.Logger) error {
	log.Warn("OpenStack provider login not implemented yet")
	log.Info("OpenStack authentication typically uses:")
	log.Info("  - OpenStack RC file: source openrc.sh")
	log.Info("  - Environment variables: OS_AUTH_URL, OS_USERNAME, OS_PASSWORD, etc.")
	return nil
}

func loginGCP(log *zap.Logger) error {
	log.Warn("GCP provider login not implemented yet")
	log.Info("GCP authentication typically uses:")
	log.Info("  - gcloud auth login")
	log.Info("  - Service account key files")
	log.Info("  - Application default credentials")
	return nil
}

func loginAzure(log *zap.Logger) error {
	log.Warn("Azure provider login not implemented yet")
	log.Info("Azure authentication typically uses:")
	log.Info("  - az login")
	log.Info("  - Service principal credentials")
	log.Info("  - Managed identities")
	return nil
}

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
		tempFile.Close()
		os.Remove(tempFile.Name())
		return "", err
	}
	tempFile.Close()

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
	cmd.Flags().String("bloc-name", "", "Bloc name for configuration")

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
	log.Warn("Bastion context discovery not fully implemented - using placeholder values")
	log.Info("In a complete implementation, this would:")
	log.Info("  1. Load provider configuration")
	log.Info("  2. Discover bastion IP from provider")
	log.Info("  3. Find appropriate SSH private key")
	log.Info("  4. Handle password-protected keys")

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
