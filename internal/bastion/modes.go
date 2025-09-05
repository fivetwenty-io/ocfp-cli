package bastion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/security"
)

// ExecutionMode represents the mode of bastion initialization.
type ExecutionMode int

const (
	// RemoteMode executes bastion initialization remotely via SSH.
	RemoteMode ExecutionMode = iota
	// LocalMode executes bastion initialization locally on the bastion itself.
	LocalMode
)

// ModeDetector detects and handles different execution modes.
type ModeDetector struct {
	config *config.Config
	log    logger.Logger
}

// NewModeDetector creates a new mode detector.
func NewModeDetector(cfg *config.Config) *ModeDetector {
	return &ModeDetector{
		config: cfg,
		log:    logger.Get(),
	}
}

// DetectExecutionMode determines whether we're running locally on bastion or remotely.
func (md *ModeDetector) DetectExecutionMode(ctx context.Context) (ExecutionMode, error) {
	md.log.Debug("Detecting execution mode")

	// Check if we're on the bastion host itself
	if md.isRunningOnBastion() {
		md.log.Info("Detected local execution mode (running on bastion)")

		return LocalMode, nil
	}

	md.log.Info("Detected remote execution mode (running from external host)")

	return RemoteMode, nil
}

// isRunningOnBastion determines if we're currently running on the bastion host.
func (md *ModeDetector) isRunningOnBastion() bool {
	// Strategy 1: Check hostname pattern
	if hostname, err := os.Hostname(); err == nil {
		expectedHostname := md.config.Name + "-bastion"
		if hostname == expectedHostname {
			md.log.Debug("Hostname matches bastion pattern", "hostname", hostname)

			return true
		}
	}

	// Strategy 2: Check for bastion marker files
	markerFiles := []string{
		os.Getenv("HOME") + "/.ocfp/provisioned",
		os.Getenv("HOME") + "/.ocfp/bastion-init-completed",
	}

	for _, marker := range markerFiles {
		if _, err := os.Stat(marker); err == nil {
			md.log.Debug("Found bastion marker file", "file", marker)

			return true
		}
	}

	// Strategy 3: Check for OCFP directory structure
	ocfpDirs := []string{
		os.Getenv("HOME") + "/ocfp",
		os.Getenv("HOME") + "/ocfp/deployments",
		os.Getenv("HOME") + "/.ocfp",
	}

	allDirsExist := true

	for _, dir := range ocfpDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			allDirsExist = false

			break
		}
	}

	if allDirsExist {
		md.log.Debug("Found complete OCFP directory structure")

		return true
	}

	// Strategy 4: Check environment variables that would be set on bastion
	bastionEnvVars := []string{
		"OCFP_ROOT",
		"DEPLOYMENTS_DIR",
		"OCFP_CLI",
	}

	envVarsSet := 0

	for _, envVar := range bastionEnvVars {
		if os.Getenv(envVar) != "" {
			envVarsSet++
		}
	}

	if envVarsSet >= 2 {
		md.log.Debug("Found bastion environment variables", "count", envVarsSet)

		return true
	}

	return false
}

// LocalExecutor handles local bastion initialization.
type LocalExecutor struct {
	config  *config.Config
	options *ProvisioningOptions
	log     logger.Logger
}

// NewLocalExecutor creates a new local executor.
func NewLocalExecutor(cfg *config.Config, opts *ProvisioningOptions) *LocalExecutor {
	return &LocalExecutor{
		config:  cfg,
		options: opts,
		log:     logger.Get(),
	}
}

// Initialize performs local bastion initialization.
func (le *LocalExecutor) Initialize(ctx context.Context) error {
	le.log.Info("Starting local bastion initialization")

	// Create a local command executor that implements the SSHClient interface
	localExecutor := &LocalCommandExecutor{
		log: le.log,
	}

	// Create a manager that uses the local executor
	manager := &Manager{
		config:    le.config,
		options:   le.options,
		progress:  &ProvisioningProgress{},
		sshClient: localExecutor,
		log:       le.log,
	}

	// Load provisioning configuration
	var err error

	manager.provConfig, err = manager.loadProvisioningConfig()
	if err != nil {
		return fmt.Errorf("failed to load provisioning config: %w", err)
	}

	// Execute local initialization
	return le.executeLocalPhases(ctx, manager)
}

// executeLocalPhases executes initialization phases locally.
func (le *LocalExecutor) executeLocalPhases(ctx context.Context, manager *Manager) error {
	// Define local-specific phases (no SSH connection needed)
	phases := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"prerequisite_check", manager.runPrerequisiteChecks},
		{"system_setup", manager.setupSystem},
		{"directories", manager.createDirectories},
		{"ocfp_directories", manager.setupOCFPDirectories},
		{"repositories", manager.setupRepositories},
		{"packages", manager.installPackages},
		{"snap_packages", manager.installSnapPackages},
		{"binary_tools", manager.installBinaryTools},
		{"cpan_modules", manager.installCPANModules},
		{"git_repos", manager.cloneGitRepositories},
		{"cf_plugins", manager.installCFPlugins},
		{"config_files", manager.createConfigFiles},
		{"shell_environment", manager.setupShellEnvironment},
		{"system_environment", manager.setupSystemEnvironment},
		{"ocfp_cli_setup", manager.setupOCFPCLI},
		{"genesis", manager.setupGenesis},
		{"vault_inception", manager.setupVaultInception},
		{"ocfp_configure", manager.runOCFPConfigure},
		{"vault_populate", manager.runVaultPopulate},
		{"verification", manager.verifyInstallation},
		{"health_check", manager.runHealthCheck},
	}

	manager.progress.TotalSteps = len(phases)

	for phaseIndex, phase := range phases {
		if manager.shouldSkipPhase(phase.name) {
			le.log.Info("Skipping phase", "phase", phase.name, "reason", "checkpoint exists")

			continue
		}

		manager.progress.CurrentStep = phase.name
		manager.progress.CompletedSteps = phaseIndex

		le.log.Info("Executing local phase",
			"phase", phase.name,
			"progress", fmt.Sprintf("%d/%d", phaseIndex+1, len(phases)))

		if le.options.DryRun {
			le.log.Info("DRY RUN: Would execute local phase", "phase", phase.name)

			continue
		}

		err := phase.fn(ctx)
		if err != nil {
			return fmt.Errorf("local phase %s failed: %w", phase.name, err)
		}

		// Create checkpoint
		manager.progress.Checkpoints[phase.name] = true

		err = manager.saveCheckpoint()
		if err != nil {
			le.log.Warn("Failed to save checkpoint", "error", err)
		}
	}

	manager.progress.CompletedSteps = len(phases)

	le.log.Info("Local bastion initialization completed successfully")

	return nil
}

// LocalCommandExecutor implements SSHClient interface for local execution.
type LocalCommandExecutor struct {
	log logger.Logger
}

// Connect is a no-op for local execution.
func (lce *LocalCommandExecutor) Connect(ctx context.Context) error {
	return nil
}

// ExecuteCommand executes a command locally.
func (lce *LocalCommandExecutor) ExecuteCommand(ctx context.Context, cmd string) (*ssh.CommandResult, error) {
	lce.log.Debug("Executing local command", "command", cmd)

	start := time.Now()

	c := exec.CommandContext(ctx, "bash", "-lc", cmd)

	var stdoutBuf, stderrBuf bytes.Buffer

	c.Stdout = &stdoutBuf
	c.Stderr = &stderrBuf

	err := c.Run()

	res := &ssh.CommandResult{
		Command:  cmd,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		Duration: time.Since(start),
	}

	if err != nil {
		// Extract exit code if possible
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = 1
		}

		lce.log.Debug("Local command failed", "exit_code", res.ExitCode, "stderr", res.Stderr)

		return res, fmt.Errorf("command failed with exit code %d: %w", res.ExitCode, err)
	}

	res.ExitCode = 0
	lce.log.Debug("Local command completed successfully", "duration", res.Duration.String())

	return res, nil
}

// TransferFile is a no-op for local execution (files are already local).
func (lce *LocalCommandExecutor) TransferFile(ctx context.Context, local, remote string, opts ssh.TransferOptions) error {
	// Local mode: treat as copying a file from local path to target path on the same machine
	// Strip any accidental "bastion:" prefix if present
	remote = strings.TrimPrefix(remote, "bastion:")

	// Validate paths for security
	if err := security.ValidatePath(local); err != nil {
		return fmt.Errorf("invalid source path: %w", err)
	}

	if err := security.ValidatePath(remote); err != nil {
		return fmt.Errorf("invalid destination path: %w", err)
	}

	// Ensure destination directory exists
	destDir := filepath.Dir(remote)
	if err := os.MkdirAll(destDir, 0750); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Copy file contents
	inputFile, err := os.Open(local) // #nosec G304 - path validated above
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}

	defer func() { _ = inputFile.Close() }()

	out, err := os.Create(remote) // #nosec G304 - path validated above
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}

	defer func() {
		// Best effort close
		_ = out.Close()
	}()

	if _, err := io.Copy(out, inputFile); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	// Optionally verify copy by comparing sizes or checksum if requested
	if opts.Verify {
		srcInfo, err := os.Stat(local)
		if err == nil {
			if dstInfo, err2 := os.Stat(remote); err2 == nil {
				if srcInfo.Size() != dstInfo.Size() {
					lce.log.Warn("Local transfer size mismatch", "src", srcInfo.Size(), "dst", dstInfo.Size())
				}
			}
		}
	}

	return nil
}

// CreateTunnel is not applicable for local execution.
func (lce *LocalCommandExecutor) CreateTunnel(ctx context.Context, localPort, remotePort int) error {
	return errors.New("tunnel creation not applicable for local execution")
}

// Close is a no-op for local execution.
func (lce *LocalCommandExecutor) Close() error {
	return nil
}

// InitializeBastionWithMode initializes bastion based on detected execution mode.
func InitializeBastionWithMode(ctx context.Context, cfg *config.Config, opts *ProvisioningOptions) error {
	detector := NewModeDetector(cfg)

	mode, err := detector.DetectExecutionMode(ctx)
	if err != nil {
		return fmt.Errorf("failed to detect execution mode: %w", err)
	}

	switch mode {
	case LocalMode:
		executor := NewLocalExecutor(cfg, opts)

		return executor.Initialize(ctx)
	case RemoteMode:
		manager := NewManager(cfg, opts)

		return manager.Initialize(ctx)
	default:
		return fmt.Errorf("unknown execution mode: %d", mode)
	}
}

// IsBastion returns true if running on a bastion host.
func IsBastion(cfg *config.Config) bool {
	detector := NewModeDetector(cfg)

	return detector.isRunningOnBastion()
}

// GetExecutionInfo returns information about the current execution environment.
func GetExecutionInfo(cfg *config.Config) map[string]interface{} {
	detector := NewModeDetector(cfg)

	info := map[string]interface{}{
		"hostname":         getHostname(),
		"user":             os.Getenv("USER"),
		"home":             os.Getenv("HOME"),
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"is_bastion":       detector.isRunningOnBastion(),
		"ocfp_provisioned": isOCFPProvisioned(),
	}

	return info
}

// Helper functions

func getHostname() string {
	if hostname, err := os.Hostname(); err == nil {
		return hostname
	}

	return "unknown"
}

func isOCFPProvisioned() bool {
	markerFile := os.Getenv("HOME") + "/.ocfp/provisioned"
	_, err := os.Stat(markerFile)

	return err == nil
}
