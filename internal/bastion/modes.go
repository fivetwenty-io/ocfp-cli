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

const (
	// File permissions for local transfer.
	localTransferDirMode = 0750

	// Bastion detection thresholds.
	minimumEnvVarsForBastionDetection = 2
)

// Mode execution errors.
var (
	ErrTunnelCreationNotApplicableForLocal = errors.New("tunnel creation not applicable for local execution")
)

// ErrUnknownExecutionMode returns an error for an unrecognized execution mode value.
func ErrUnknownExecutionMode(mode int) error {
	return fmt.Errorf("unknown execution mode: %d", mode) //nolint:err113 // dynamic error with context
}

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
func (md *ModeDetector) DetectExecutionMode(_ctx context.Context) (ExecutionMode, error) {
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
	return md.checkHostnamePattern() ||
		md.checkMarkerFiles() ||
		md.checkDirectoryStructure() ||
		md.checkEnvironmentVariables()
}

// checkHostnamePattern checks if hostname matches bastion pattern.
func (md *ModeDetector) checkHostnamePattern() bool {
	hostname, err := os.Hostname()
	if err != nil {
		return false
	}

	expectedHostname := md.config.Name + "-bastion"
	if hostname == expectedHostname {
		md.log.Debugw("Hostname matches bastion pattern", "hostname", hostname)

		return true
	}

	return false
}

// checkMarkerFiles checks for bastion-specific marker files.
func (md *ModeDetector) checkMarkerFiles() bool {
	markerFiles := []string{
		filepath.Join(config.OcfpHome(), "provisioned"),
		filepath.Join(config.OcfpHome(), "bastion-init-completed"),
	}

	for _, marker := range markerFiles {
		_, err := os.Stat(marker) //nolint:gosec // path components are from trusted HOME env
		if err == nil {
			md.log.Debugw("Found bastion marker file", "file", marker)

			return true
		}
	}

	return false
}

// checkDirectoryStructure checks for OCFP directory structure.
func (md *ModeDetector) checkDirectoryStructure() bool {
	ocfpDirs := []string{
		os.Getenv("HOME") + "/ocfp",
		os.Getenv("HOME") + "/ocfp/deployments",
		config.OcfpHome(),
	}

	for _, dir := range ocfpDirs {
		_, err := os.Stat(dir) //nolint:gosec // path components are from trusted HOME env
		if os.IsNotExist(err) {
			return false
		}
	}

	md.log.Debug("Found complete OCFP directory structure")

	return true
}

// checkEnvironmentVariables checks for bastion-specific environment variables.
func (md *ModeDetector) checkEnvironmentVariables() bool {
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

	if envVarsSet >= minimumEnvVarsForBastionDetection {
		md.log.Debugw("Found bastion environment variables", "count", envVarsSet)

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
		config:  le.config,
		options: le.options,
		progress: &ProvisioningProgress{
			TotalSteps:     0,
			CompletedSteps: 0,
			CurrentStep:    "",
			StartTime:      time.Time{},
			Errors:         nil,
			Checkpoints:    nil,
		},
		sshClient:         localExecutor,
		provConfig:        nil,
		checkpointManager: nil,
		errorHandler:      nil,
		log:               le.log,
	}

	// Load provisioning configuration
	manager.provConfig = manager.loadProvisioningConfig()

	// Execute local initialization
	return le.executeLocalPhases(ctx, manager)
}

// executeLocalPhases executes initialization phases locally.
func (le *LocalExecutor) executeLocalPhases(ctx context.Context, manager *Manager) error {
	phases := le.getLocalPhases(manager)
	manager.progress.TotalSteps = len(phases)

	// Set start time before executing phases
	manager.progress.StartTime = time.Now()

	for phaseIndex, phase := range phases {
		err := le.executeLocalPhase(ctx, manager, phase, phaseIndex, len(phases))
		if err != nil {
			return err
		}
	}

	manager.progress.CompletedSteps = len(phases)

	le.log.Info("Local bastion initialization completed successfully")

	return nil
}

// getLocalPhases returns the list of local initialization phases.
func (le *LocalExecutor) getLocalPhases(manager *Manager) []struct {
	name string
	fn   func(context.Context) error
} {
	return []struct {
		name string
		fn   func(context.Context) error
	}{
		{"bastion_keys", manager.configureBastionKeys},
		{"prerequisite_check", manager.runPrerequisiteChecks},
		{"system_setup", manager.setupSystem},
		{"directories", manager.createDirectories},
		{"ocfp_directories", manager.setupOCFPDirectories},
		{"apt_repositories", manager.setupAPTRepositories},
		{"packages", manager.installPackages},
		{"brew_install", manager.installBrew},
		{"brew_packages", manager.installBrewPackages},
		{"post_brew_apt", manager.installPostBrewPackages},
		{"binary_tools", manager.installBinaryTools},
		{"cpan_modules", manager.installCPANModules},
		{"git_repos", manager.cloneGitRepositories},
		{"cf_plugins", manager.installCFPlugins},
		{"config_files", manager.createConfigFiles},
		{"shell_environment", manager.setupShellEnvironment},
		{"system_environment", manager.setupSystemEnvironment},
		{"ocfp_cli_setup", manager.setupOCFPCLI},
		{"helper_scripts", manager.installHelperScripts},
		{"genesis", manager.setupGenesis},
		{"vault_inception", manager.setupVaultInception},
		{"ocfp_configure", manager.runOCFPConfigure},
		{"vault_populate", manager.runVaultPopulate},
		{"verification", manager.verifyInstallation},
		{"health_check", manager.runHealthCheck},
	}
}

// executeLocalPhase executes a single local phase.
func (le *LocalExecutor) executeLocalPhase(ctx context.Context, manager *Manager, phase struct {
	name string
	fn   func(context.Context) error
}, phaseIndex, totalPhases int) error {
	if manager.shouldSkipPhase(phase.name) {
		le.log.Infow("Skipping phase", "phase", phase.name, "reason", "checkpoint exists")

		return nil
	}

	le.updateLocalPhaseProgress(manager, phase.name, phaseIndex, totalPhases)

	if le.options.DryRun {
		le.log.Infow("DRY RUN: Would execute local phase", "phase", phase.name)

		return nil
	}

	return le.runLocalPhaseWithCheckpoint(ctx, manager, phase)
}

// updateLocalPhaseProgress updates progress tracking for local phases.
func (le *LocalExecutor) updateLocalPhaseProgress(manager *Manager, phaseName string, phaseIndex, totalPhases int) {
	manager.progress.CurrentStep = phaseName
	manager.progress.CompletedSteps = phaseIndex

	le.log.Info("Executing local phase",
		"phase", phaseName,
		"progress", fmt.Sprintf("%d/%d", phaseIndex+1, totalPhases))
}

// runLocalPhaseWithCheckpoint executes a phase and saves checkpoint.
func (le *LocalExecutor) runLocalPhaseWithCheckpoint(ctx context.Context, manager *Manager, phase struct {
	name string
	fn   func(context.Context) error
}) error {
	err := phase.fn(ctx)
	if err != nil {
		return fmt.Errorf("local phase %s failed: %w", phase.name, err)
	}

	manager.progress.Checkpoints[phase.name] = true

	err = manager.saveCheckpoint()
	if err != nil {
		le.log.Warnw("Failed to save checkpoint", "error", err)
	}

	return nil
}

// LocalCommandExecutor implements SSHClient interface for local execution.
type LocalCommandExecutor struct {
	log logger.Logger
}

// Connect is a no-op for local execution.
func (lce *LocalCommandExecutor) Connect(_ctx context.Context) error {
	return nil
}

// ExecuteCommand executes a command locally.
func (lce *LocalCommandExecutor) ExecuteCommand(ctx context.Context, cmd string) (*ssh.CommandResult, error) {
	lce.log.Debugw("Executing local command", "command", logger.RedactSecrets(cmd))

	start := time.Now()

	command := exec.CommandContext(ctx, "bash", "-lc", cmd) //nolint:gosec // command args are from trusted config

	var stdoutBuf, stderrBuf bytes.Buffer

	command.Stdout = &stdoutBuf
	command.Stderr = &stderrBuf

	err := command.Run()

	res := &ssh.CommandResult{
		Command:  cmd,
		ExitCode: 0,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		Duration: time.Since(start),
	}

	if err != nil {
		// Extract exit code if possible
		exitErr := &exec.ExitError{
			ProcessState: nil,
			Stderr:       nil,
		}
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = 1
		}

		lce.log.Debugw("Local command failed", "exit_code", res.ExitCode, "stderr", logger.RedactSecrets(res.Stderr))

		return res, fmt.Errorf("command failed with exit code %d: %w", res.ExitCode, err)
	}

	res.ExitCode = 0
	lce.log.Debugw("Local command completed successfully", "duration", res.Duration.String())

	return res, nil
}

// TransferFile is a no-op for local execution (files are already local).
func (lce *LocalCommandExecutor) TransferFile(_ctx context.Context, local, remote string, opts ssh.TransferOptions) error {
	// Local mode: treat as copying a file from local path to target path on the same machine
	// Strip any accidental "bastion:" prefix if present
	remote = strings.TrimPrefix(remote, "bastion:")

	// Validate paths for security
	err := security.ValidatePath(local)
	if err != nil {
		return fmt.Errorf("invalid source path: %w", err)
	}

	err = security.ValidatePath(remote)
	if err != nil {
		return fmt.Errorf("invalid destination path: %w", err)
	}

	// Ensure destination directory exists
	destDir := filepath.Dir(remote)

	err = os.MkdirAll(destDir, localTransferDirMode)
	if err != nil {
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

	_, err = io.Copy(out, inputFile)
	if err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	// Optionally verify copy by comparing sizes or checksum if requested
	if opts.Verify {
		lce.verifyTransfer(local, remote)
	}

	return nil
}

// CreateTunnel is not applicable for local execution.
func (lce *LocalCommandExecutor) CreateTunnel(_ctx context.Context, _localPort, _remotePort int) error {
	return ErrTunnelCreationNotApplicableForLocal
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
		manager := NewManager(ctx, cfg, opts)

		return manager.Initialize(ctx)
	default:
		return ErrUnknownExecutionMode(int(mode))
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

// verifyTransfer verifies the transfer was successful.
func (lce *LocalCommandExecutor) verifyTransfer(local, remote string) {
	srcInfo, err := os.Stat(local)
	if err != nil {
		return
	}

	dstInfo, err := os.Stat(remote)
	if err != nil {
		return
	}

	if srcInfo.Size() != dstInfo.Size() {
		lce.log.Warnw("Local transfer size mismatch", "src", srcInfo.Size(), "dst", dstInfo.Size())
	}
}

// getHostname returns the system hostname.
func getHostname() string {
	hostname, err := os.Hostname()
	if err == nil {
		return hostname
	}

	return "unknown"
}

// isOCFPProvisioned checks if OCFP is provisioned.
func isOCFPProvisioned() bool {
	markerFile := filepath.Join(config.OcfpHome(), "provisioned")
	_, err := os.Stat(markerFile)

	return err == nil
}
