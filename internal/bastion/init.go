package bastion

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/deployments"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/providers"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/provision"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/pmezard/go-difflib/difflib"
	"gopkg.in/yaml.v3"
)

const (
	// File permissions.
	logDirectoryMode = 0750
	scriptFileMode   = 0600

	// Diff context.
	diffContextLines = 3

	// Worker pool defaults.
	defaultWorkerCount = 3

	// Git clone/update configuration.
	gitBackoffInitial = 2 * time.Second
	gitMaxAttempts    = 4
	gitDefaultDepth   = 1
)

// Bastion initialization errors.
var (
	ErrBlocNameRequired                  = errors.New("bloc name is required")
	ErrProviderRequired                  = errors.New("provider is required")
	ErrBastionProvisioningDidNotComplete = errors.New("bastion provisioning did not complete successfully")
	ErrOCFPConfigurationFileNotFound     = errors.New("OCFP configuration file not found")
	ErrBlocNotFoundInConfig              = errors.New("bloc not found in config file")
	ErrScriptExecutionFailed             = errors.New("script execution failed")
)

// Dynamic error constructor.
func ErrUnsupportedProvider(provider string) error {
	return fmt.Errorf("unsupported provider: %s", provider) //nolint:err113 // dynamic error with context
}

// Manager orchestrates bastion initialization across providers.
type Manager struct {
	config            *config.Config
	options           *ProvisioningOptions
	progress          *ProvisioningProgress
	sshClient         SSHClient
	provConfig        provision.ProvisionConfig
	checkpointManager *CheckpointManager
	errorHandler      *ErrorHandler
	deploymentModes   *deployments.Resolver
	log               logger.Logger
}

// task represents a phase execution task.
type task struct {
	name string
	fn   func(context.Context) error
}

// job represents a git clone/update job.
type job struct {
	index int
	name  string
	cmd   string
}

// NewManager creates a new bastion initialization manager.
func NewManager(cfg *config.Config, opts *ProvisioningOptions) *Manager {
	checkpointMgr := NewCheckpointManager(cfg)

	// Load existing progress if resuming
	var (
		progress   *ProvisioningProgress
		err        error
		checkpoint *CheckpointData
	)

	if opts.Resume {
		checkpoint, err = checkpointMgr.Load()
		if err == nil && checkpoint != nil {
			progress = checkpointMgr.RestoreProgress(checkpoint)
		} else {
			progress = &ProvisioningProgress{
				TotalSteps:     0,
				CompletedSteps: 0,
				CurrentStep:    "",
				StartTime:      time.Now(),
				Errors:         nil,
				Checkpoints:    make(map[string]bool),
			}
		}
	} else {
		progress = &ProvisioningProgress{
			TotalSteps:     0,
			CompletedSteps: 0,
			CurrentStep:    "",
			StartTime:      time.Now(),
			Errors:         nil,
			Checkpoints:    make(map[string]bool),
		}
	}

	return &Manager{
		config:            cfg,
		options:           opts,
		progress:          progress,
		sshClient:         nil,
		provConfig:        nil,
		checkpointManager: checkpointMgr,
		errorHandler:      NewErrorHandler(),
		deploymentModes:   deployments.NewResolver(cfg),
		log:               logger.Get(),
	}
}

// Initialize performs the complete bastion initialization process.
func (m *Manager) Initialize(ctx context.Context) error {
	m.log.Infow("Starting bastion initialization",
		"bloc", m.config.Name,
		"provider", m.config.Provider,
		"ocfp_only", m.options.OCFPOnly)

	err := m.setupInfrastructure(ctx)
	if err != nil {
		return err
	}

	defer m.closeSSHClient()

	// Handle OCFP-only mode
	if m.options.OCFPOnly {
		return m.runOCFPOnlyMode(ctx)
	}

	// Check if already provisioned
	if m.checkAndSkipIfProvisioned(ctx) {
		return nil
	}

	// Handle dry-run preview
	if m.options.DryRun {
		m.previewConfigChanges(ctx)
	}

	return m.executeInitializationPhases(ctx)
}

// closeSSHClient safely closes the SSH client connection.
func (m *Manager) closeSSHClient() {
	err := m.sshClient.Close()
	if err != nil {
		m.log.Warn("Failed to close SSH client", "error", err.Error())
	}
}

// runOCFPOnlyMode handles the OCFP-only installation mode.
func (m *Manager) runOCFPOnlyMode(ctx context.Context) error {
	m.log.Info("OCFP-only mode: installing/updating OCFP CLI binary only")

	err := m.setupOCFPCLI(ctx)
	if err != nil {
		return fmt.Errorf("OCFP CLI setup failed: %w", err)
	}

	m.log.Info("OCFP CLI installation/update completed successfully")

	return nil
}

// checkAndSkipIfProvisioned checks if bastion is already provisioned and reports status.
// Returns true if already provisioned (should skip), false to continue.
func (m *Manager) checkAndSkipIfProvisioned(ctx context.Context) bool {
	if m.options.DryRun {
		return false
	}

	alreadyProvisioned, err := m.isAlreadyProvisioned(ctx)
	if err != nil {
		m.log.Warnw("Failed to check provisioning status", "error", err)

		return false // Continue despite check failure
	}

	if !alreadyProvisioned {
		return false
	}

	m.log.Info("Bastion is already provisioned, skipping initialization")

	if m.options.ProgressOut != nil {
		message := "✓ Bastion already fully provisioned - skipping initialization\n"
		_, _ = m.options.ProgressOut.Write([]byte(message))
	}

	return true
}

// executeInitializationPhases sets up and executes all initialization phases.
func (m *Manager) executeInitializationPhases(ctx context.Context) error {
	phases := m.getInitializationPhases()
	m.progress.TotalSteps = len(phases)
	m.progress.StartTime = time.Now()

	var reporter *ProgressReporter
	if m.options.ProgressOut != nil {
		reporter = NewProgressReporter(m.options.ProgressOut, m.progress)
		reporter.Start(ctx)
	}

	err := m.executePhases(ctx, phases, reporter)
	if err != nil {
		return err
	}

	m.finalizeInitialization(phases)

	return nil
}

// isAlreadyProvisioned checks if the bastion has already been fully provisioned.
// Returns true if the provisioning completion marker exists and is valid.
func (m *Manager) isAlreadyProvisioned(ctx context.Context) (bool, error) {
	if m.options.DryRun {
		return false, nil
	}

	// Check for completion marker on remote system
	markerPath := "${HOME}/.ocfp/provisioned"
	cmd := fmt.Sprintf("test -f '%s' && cat '%s'", markerPath, markerPath)

	result, err := m.sshClient.ExecuteCommand(ctx, cmd)
	if err != nil {
		// SSH command failed - return error for proper handling
		return false, fmt.Errorf("failed to check provisioning marker: %w", err)
	}

	if result.ExitCode != 0 {
		// Marker doesn't exist (expected for first-time provisioning)
		return false, nil
	}

	// Marker exists and is readable
	provisionedDate := strings.TrimSpace(result.Stdout)
	m.log.Infow("Bastion already provisioned",
		"provisioned_date", provisionedDate,
		"marker_path", markerPath)

	return true, nil
}

// setupInfrastructure handles provider setup, SSH connection, and configuration loading.
func (m *Manager) setupInfrastructure(ctx context.Context) error {
	err := m.validatePrerequisites()
	if err != nil {
		return fmt.Errorf("prerequisite validation failed: %w", err)
	}

	// Get provider-specific initializer
	initializer, err := m.getProviderInitializer()
	if err != nil {
		return fmt.Errorf("failed to get provider initializer: %w", err)
	}

	// Validate provider configuration
	err = initializer.Validate()
	if err != nil {
		return fmt.Errorf("provider validation failed: %w", err)
	}

	// Get connection details
	providerConnDetails, err := initializer.GetConnectionDetails()
	if err != nil {
		return fmt.Errorf("failed to get connection details: %w", err)
	}

	// Convert to SSH ConnectionDetails
	connDetails := &ssh.ConnectionDetails{
		Host:           providerConnDetails.Host,
		Port:           providerConnDetails.Port,
		User:           providerConnDetails.User,
		PrivateKeyPath: providerConnDetails.PrivateKeyPath,
		Password:       providerConnDetails.Password,
		SSHOptions:     providerConnDetails.SSHOptions,
		UseSSHPass:     providerConnDetails.UseSSHPass,
	}

	// Create SSH client
	m.sshClient = m.createSSHClient(connDetails)

	if m.options.DryRun {
		m.log.Info("DRY RUN: skipping SSH connection")

		m.provConfig = m.loadProvisioningConfig()

		err := m.deploymentModes.Validate()
		if err != nil {
			return fmt.Errorf("deployment validation failed: %w", err)
		}

		return nil
	}

	// Connect to bastion
	err = m.sshClient.Connect(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to bastion: %w", err)
	}

	// Load provisioning configuration
	m.provConfig = m.loadProvisioningConfig()

	err = m.deploymentModes.Validate()
	if err != nil {
		return fmt.Errorf("deployment validation failed: %w", err)
	}

	return nil
}

// getInitializationPhases returns the list of initialization phases.
func (m *Manager) getInitializationPhases() []struct {
	name string
	fn   func(context.Context) error
} {
	return []struct {
		name string
		fn   func(context.Context) error
	}{
		// Phase 0: CRITICAL - SSH agent forwarding MUST be first
		// This enables agent forwarding on the server before any git operations
		{"ssh_agent_forwarding", m.setupSSHAgentForwarding},

		// Phase 1: Prerequisites and system setup
		{"prerequisite_check", m.runPrerequisiteChecks},
		{"system_setup", m.setupSystem},
		{"directories", m.createDirectories},

		// Phase 2: OCFP structure and configuration files
		{"config_files", m.copyConfigFiles},
		{"ocfp_directories", m.setupOCFPDirectories},
		{"configuration_files", m.createConfigFiles},

		// Phase 3: Repositories and package management
		{"repositories", m.setupRepositories},
		{"packages", m.installPackages},
		{"snap_packages", m.installSnapPackages},

		// Phase 4: Binary tools and advanced installations
		{"binary_tools", m.installBinaryTools},
		{"cpan_modules", m.installCPANModules},
		{"cf_plugins", m.installCFPlugins},

		// Phase 5: Git repositories and Genesis
		{"git_repos", m.cloneGitRepositories},
		{"genesis", m.setupGenesis},

		// Phase 6: Environment configuration
		{"shell_environment", m.setupShellEnvironment},
		{"system_environment", m.setupSystemEnvironment},

		// Phase 7: OCFP CLI and integration
		{"ocfp_cli_setup", m.setupOCFPCLI},
		{"vault_inception", m.setupVaultInception},
		{"ocfp_configure", m.runOCFPConfigure},
		{"vault_populate", m.runVaultPopulate},

		// Phase 8: Custom scripts and verification
		{"custom_scripts", m.runCustomScripts},
		{"verification", m.verifyInstallation},
		{"health_check", m.runHealthCheck},
	}
}

// executePhases runs phases either in parallel or sequential mode.
func (m *Manager) executePhases(ctx context.Context, phases []struct {
	name string
	fn   func(context.Context) error
}, reporter *ProgressReporter) error {
	if m.options.Parallel {
		return m.executeParallelPhases(ctx, reporter)
	}

	return m.executeSequentialPhases(ctx, phases, reporter)
}

// executeParallelPhases handles parallel execution of initialization phases.
func (m *Manager) executeParallelPhases(ctx context.Context, _ *ProgressReporter) error {
	// Sequential pre-parallel phases
	// CRITICAL: ssh_agent_forwarding MUST be first before anything else
	pre := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"ssh_agent_forwarding", m.setupSSHAgentForwarding}, // MUST BE FIRST
		{"prerequisite_check", m.runPrerequisiteChecks},
		{"system_setup", m.setupSystem},
		{"directories", m.createDirectories},
		{"config_files", m.copyConfigFiles},
		{"ocfp_directories", m.setupOCFPDirectories},
		{"configuration_files", m.createConfigFiles},
		{"repositories", m.setupRepositories},
		{"packages", m.installPackages}, // avoid dpkg lock issues
	}

	err := m.runPhasesSequential(ctx, pre)
	if err != nil {
		return err
	}

	// Parallel-safe phases (no apt/dpkg)
	par := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"snap_packages", m.installSnapPackages},
		{"binary_tools", m.installBinaryTools},
		{"cpan_modules", m.installCPANModules},
		{"cf_plugins", m.installCFPlugins},
		{"git_repos", m.cloneGitRepositories},
	}

	err = m.runPhasesParallel(ctx, par)
	if err != nil {
		return err
	}

	// Post-parallel sequential phases
	post := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"genesis", m.setupGenesis},
		{"shell_environment", m.setupShellEnvironment},
		{"system_environment", m.setupSystemEnvironment},
		{"ocfp_cli_setup", m.setupOCFPCLI},
		{"vault_inception", m.setupVaultInception},
		{"ocfp_configure", m.runOCFPConfigure},
		{"vault_populate", m.runVaultPopulate},
		{"custom_scripts", m.runCustomScripts},
		{"verification", m.verifyInstallation},
		{"health_check", m.runHealthCheck},
	}

	return m.runPhasesSequential(ctx, post)
}

// executeSequentialPhases handles sequential execution of all phases.
func (m *Manager) executeSequentialPhases(ctx context.Context, phases []struct {
	name string
	fn   func(context.Context) error
}, reporter *ProgressReporter) error {
	for index, phase := range phases {
		err := m.executePhase(ctx, phase, index, len(phases), reporter)
		if err != nil {
			return err
		}
	}

	return nil
}

// executePhase handles the execution of a single phase.
func (m *Manager) executePhase(ctx context.Context, phase struct {
	name string
	fn   func(context.Context) error
}, index, total int, reporter *ProgressReporter) error {
	if m.shouldSkipPhase(phase.name) {
		m.log.Infow("Skipping phase", "phase", phase.name, "reason", "checkpoint exists")

		if reporter != nil {
			reporter.ReportPhaseSkipped(phase.name, "resumed and previously completed")
		}

		return nil
	}

	m.updatePhaseProgress(phase.name, index, total, reporter)

	if m.options.DryRun {
		m.log.Infow("DRY RUN: Would execute phase", "phase", phase.name)

		return nil
	}

	return m.executePhaseWithErrorHandling(ctx, phase, index, total, reporter)
}

// updatePhaseProgress updates progress tracking and logging for a phase.
func (m *Manager) updatePhaseProgress(phaseName string, index, total int, reporter *ProgressReporter) {
	m.progress.CurrentStep = phaseName
	m.progress.CompletedSteps = index

	m.log.Infow("Executing phase",
		"phase", phaseName,
		"progress", fmt.Sprintf("%d/%d", index+1, total))

	if reporter != nil {
		reporter.ReportPhaseStart(phaseName, index, total)
	}
}

// executePhaseWithErrorHandling executes a phase with retry logic and checkpoint management.
func (m *Manager) executePhaseWithErrorHandling(ctx context.Context, phase struct {
	name string
	fn   func(context.Context) error
}, index, total int, reporter *ProgressReporter) error {
	err := m.errorHandler.ExecuteWithRetry(ctx, phase.name, func() error {
		return phase.fn(ctx)
	})
	if err != nil {
		return m.handlePhaseFailure(phase.name, index, err, reporter)
	}

	return m.handlePhaseSuccess(phase.name, index, total, reporter)
}

// handlePhaseFailure handles phase execution failure.
func (m *Manager) handlePhaseFailure(phaseName string, index int, err error, reporter *ProgressReporter) error {
	m.progress.Errors = append(m.progress.Errors, err)

	metadata := map[string]interface{}{
		"failed_phase": phaseName,
		"error_type":   "execution_failure",
		"attempt":      index + 1,
	}

	saveErr := m.checkpointManager.Save(m.progress, metadata)
	if saveErr != nil {
		m.log.Warnw("Failed to save failure checkpoint", "error", saveErr.Error())
	}

	if reporter != nil {
		reporter.ReportError(phaseName, err, m.errorHandler.maxRetries, m.errorHandler.maxRetries)
	}

	return fmt.Errorf("phase %s failed: %w", phaseName, err)
}

// handlePhaseSuccess handles phase execution success.
func (m *Manager) handlePhaseSuccess(phaseName string, index, total int, reporter *ProgressReporter) error {
	m.checkpointManager.MarkPhaseCompleted(m.progress, phaseName)

	metadata := map[string]interface{}{
		"completed_phase": phaseName,
		"progress":        float64(index+1) / float64(total) * percentageMultiplier,
		"timestamp":       time.Now(),
	}

	err := m.checkpointManager.Save(m.progress, metadata)
	if err != nil {
		m.log.Warnw("Failed to save checkpoint", "error", err)
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(phaseName, time.Since(m.progress.StartTime))
	}

	return nil
}

// finalizeInitialization handles completion tasks like checkpointing and reporting.
func (m *Manager) finalizeInitialization(phases []struct {
	name string
	fn   func(context.Context) error
}) {
	reporter := m.getProgressReporter()
	m.progress.CompletedSteps = len(phases)
	duration := time.Since(m.progress.StartTime)

	// Clear checkpoint on successful completion
	err := m.checkpointManager.Clear()
	if err != nil {
		m.log.Warnw("Failed to clear checkpoint", "error", err)
	}

	// Cleanup old checkpoints (older than 7 days)
	err = m.checkpointManager.CleanupOldCheckpoints(7 * 24 * time.Hour)
	if err != nil {
		m.log.Warnw("Failed to cleanup old checkpoints", "error", err)
	}

	// Report final success
	if reporter != nil {
		reporter.ReportFinalSummary(true, duration, len(phases), len(m.progress.Errors))
	}

	m.log.Infow("Bastion initialization completed successfully",
		"duration", duration.String(),
		"total_phases", len(phases),
		"errors_encountered", len(m.progress.Errors))
}

// validatePrerequisites checks that required prerequisites are met.
func (m *Manager) validatePrerequisites() error {
	m.log.Debug("Validating prerequisites")

	// Check required configuration
	if m.config.Name == "" {
		return ErrBlocNameRequired
	}

	if m.config.Provider == "" {
		return ErrProviderRequired
	}

	// Check local tools
	requiredTools := []string{"ssh", "scp"}
	for _, tool := range requiredTools {
		_, err := exec.LookPath(tool)
		if err != nil {
			m.log.Warnw("Required tool not found", "tool", tool)
		}
	}

	// Create local directories
	logDir := filepath.Join(os.Getenv("HOME"), ".ocfp", "logs", "provision")

	err := os.MkdirAll(logDir, logDirectoryMode)
	if err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Validate config overrides names (non-fatal warnings)
	m.validateOverrides()

	return nil
}

// validateOverrides warns about unknown names in enable/disable and override maps.
func (m *Manager) validateOverrides() {
	// Build name sets from managers
	toolMgr := provision.NewAdvancedToolManager(m.config.Provider, m.config)
	snapMgr := provision.NewSnapManager(m.config.Provider, m.config)
	cfMgr := provision.NewCFPluginManager(m.config.Provider, m.config)

	toolNames := map[string]struct{}{}
	for _, t := range toolMgr.GetAdvancedBinaryTools() {
		toolNames[strings.ToLower(t.Name)] = struct{}{}
	}

	snapNames := map[string]struct{}{}
	for _, s := range snapMgr.GetSnapPackages() {
		snapNames[strings.ToLower(s.Name)] = struct{}{}
	}

	pluginNames := map[string]struct{}{}
	for _, p := range cfMgr.GetCFPlugins() {
		pluginNames[strings.ToLower(p.Name)] = struct{}{}
	}

	// Helper to check lists
	checkList := func(kind string, names map[string]struct{}, list []string) {
		for _, n := range list {
			if _, ok := names[strings.ToLower(n)]; !ok {
				m.log.Warnw("Unknown override name", "type", kind, "name", n)
			}
		}
	}

	// enable/disable
	checkList("tool", toolNames, m.config.Bastion.Tools.Enable)
	checkList("tool", toolNames, m.config.Bastion.Tools.Disable)
	checkList("snap", snapNames, m.config.Bastion.Snaps.Enable)
	checkList("snap", snapNames, m.config.Bastion.Snaps.Disable)
	checkList("cf_plugin", pluginNames, m.config.Bastion.CFPlugins.Enable)
	checkList("cf_plugin", pluginNames, m.config.Bastion.CFPlugins.Disable)

	// Per-item override maps keys
	for k := range m.config.Bastion.ToolOverrides {
		if _, ok := toolNames[strings.ToLower(k)]; !ok {
			m.log.Warnw("Unknown tool override key", "name", k)
		}
	}

	for k := range m.config.Bastion.SnapOverrides {
		if _, ok := snapNames[strings.ToLower(k)]; !ok {
			m.log.Warnw("Unknown snap override key", "name", k)
		}
	}

	for k := range m.config.Bastion.CFPluginOverrides {
		if _, ok := pluginNames[strings.ToLower(k)]; !ok {
			m.log.Warnw("Unknown CF plugin override key", "name", k)
		}
	}
}

// previewConfigChanges shows diffs for managed config files in dry-run.
func (m *Manager) previewConfigChanges(ctx context.Context) {
	cfm := provision.NewConfigFileManager(m.config.Provider, m.config)
	files := cfm.GetConfigFiles()

	if len(files) == 0 || m.options.ProgressOut == nil {
		m.previewSystemChanges(ctx)

		return
	}

	_, _ = fmt.Fprintln(m.options.ProgressOut, "\n== Dry-run: configuration file changes ==")
	for _, file := range files {
		m.previewFileChange(ctx, file)
	}
}

// previewSystemChanges shows system-level changes in dry-run mode.
func (m *Manager) previewSystemChanges(ctx context.Context) {
	_, _ = fmt.Fprintln(m.options.ProgressOut, "\n== Dry-run: system file changes ==")
	m.previewProfileChanges(ctx)
	m.previewEnvironmentChanges(ctx)
	m.previewAPTRepositories(ctx)
}

// previewFileChange shows the diff for a single configuration file.
func (m *Manager) previewFileChange(ctx context.Context, file provision.ConfigFile) {
	path := m.expandPathVariables(file.Path)
	current := m.readFileContent(ctx, path)

	if file.Content == "" {
		return
	}

	m.outputFileDiff(path, current, file.Content, os.FileMode(file.Mode))
}

// expandPathVariables expands environment variables in file paths.
func (m *Manager) expandPathVariables(path string) string {
	path = strings.ReplaceAll(path, "${HOME}", "$HOME")
	path = strings.ReplaceAll(path, "${USER}", "$USER")
	path = strings.ReplaceAll(path, "$HOME", os.Getenv("HOME"))

	return path
}

// readFileContent reads file content from remote or local filesystem.
func (m *Manager) readFileContent(ctx context.Context, path string) string {
	if m.sshClient != nil {
		res, execErr := m.sshClient.ExecuteCommand(ctx, fmt.Sprintf("cat '%s' 2>/dev/null || true", path))
		if execErr == nil {
			return res.Stdout
		}

		return ""
	}

	data, readErr := os.ReadFile(path) // #nosec G304 - path is validated above
	if readErr == nil {
		return string(data)
	}

	return ""
}

// outputFileDiff outputs the diff between current and desired file content.
func (m *Manager) outputFileDiff(path, current, desired string, mode os.FileMode) {
	if current == "" {
		_, _ = fmt.Fprintf(m.options.ProgressOut, "\n+ create %s (mode %o)\n", path, mode)

		return
	}

	if current == desired {
		_, _ = fmt.Fprintf(m.options.ProgressOut, "\n= no change %s\n", path)

		return
	}

	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(current),
		B:        difflib.SplitLines(desired),
		FromFile: path + " (current)",
		ToFile:   path + " (proposed)",
		FromDate: "",
		ToDate:   "",
		Eol:      "",
		Context:  diffContextLines,
	}
	text, _ := difflib.GetUnifiedDiffString(diff)
	_, _ = fmt.Fprintf(m.options.ProgressOut, "\n%s\n", text)
}

// previewProfileChanges shows diffs for /etc/profile.d/ocfp.sh changes.
func (m *Manager) previewProfileChanges(ctx context.Context) {
	envMgr := provision.NewEnvironmentManager(m.config.Provider, m.config)
	// Build expected profile content
	var builder strings.Builder
	builder.WriteString("#!/bin/sh\n# OCFP environment variables\n# Generated by ocfp bastion provisioning\n\n")

	for k, v := range envMgr.GetSystemEnvironmentVarsForPreview() {
		builder.WriteString(fmt.Sprintf("export %s='%s'\n", k, v))
	}

	desired := builder.String()
	current := ""

	if m.sshClient != nil {
		res, err := m.sshClient.ExecuteCommand(ctx, "cat /etc/profile.d/ocfp.sh 2>/dev/null || true")
		if err == nil {
			current = res.Stdout
		}
	}

	switch {
	case current == "":
		_, _ = fmt.Fprintln(m.options.ProgressOut, "+ create /etc/profile.d/ocfp.sh")
	case current != desired:
		diff := difflib.UnifiedDiff{A: difflib.SplitLines(current), B: difflib.SplitLines(desired), FromFile: "/etc/profile.d/ocfp.sh (current)", ToFile: "/etc/profile.d/ocfp.sh (proposed)", FromDate: "", ToDate: "", Eol: "", Context: diffContextLines}
		if text, _ := difflib.GetUnifiedDiffString(diff); text != "" {
			_, _ = fmt.Fprintln(m.options.ProgressOut, text)
		}
	default:
		_, _ = fmt.Fprintln(m.options.ProgressOut, "= no change /etc/profile.d/ocfp.sh")
	}
}

// previewEnvironmentChanges shows diffs for /etc/environment changes.
func (m *Manager) previewEnvironmentChanges(ctx context.Context) {
	envMgr := provision.NewEnvironmentManager(m.config.Provider, m.config)
	envVars := envMgr.GetSystemEnvironmentVarsForPreview()
	current := ""

	if m.sshClient != nil {
		res, err := m.sshClient.ExecuteCommand(ctx, "cat /etc/environment 2>/dev/null || true")
		if err == nil {
			current = res.Stdout
		}
	}

	proposed := m.buildProposedEnvironment(current, envVars)
	_, _ = fmt.Fprintln(m.options.ProgressOut, "\n== /etc/environment changes ==")

	switch {
	case strings.TrimSpace(current) == "":
		// Treat as create
		diff := difflib.UnifiedDiff{A: []string{}, B: difflib.SplitLines(proposed), FromFile: "/etc/environment (current)", ToFile: "/etc/environment (proposed)", FromDate: "", ToDate: "", Eol: "", Context: diffContextLines}
		if text, _ := difflib.GetUnifiedDiffString(diff); text != "" {
			_, _ = fmt.Fprintln(m.options.ProgressOut, text)
		} else {
			_, _ = fmt.Fprintln(m.options.ProgressOut, "+ create /etc/environment")
		}
	case current != proposed:
		diff := difflib.UnifiedDiff{A: difflib.SplitLines(current), B: difflib.SplitLines(proposed), FromFile: "/etc/environment (current)", ToFile: "/etc/environment (proposed)", FromDate: "", ToDate: "", Eol: "", Context: diffContextLines}
		if text, _ := difflib.GetUnifiedDiffString(diff); text != "" {
			_, _ = fmt.Fprintln(m.options.ProgressOut, text)
		}
	default:
		_, _ = fmt.Fprintln(m.options.ProgressOut, "= no change /etc/environment")
	}
}

// previewAPTRepositories shows planned changes for APT repositories.
func (m *Manager) previewAPTRepositories(ctx context.Context) {
	repos := m.provConfig.GetAPTRepositories()
	if len(repos) == 0 {
		return
	}

	_, _ = fmt.Fprintln(m.options.ProgressOut, "\nAPT repos plan:")
	for _, repo := range repos {
		if !repo.Enabled {
			continue
		}

		m.previewAPTRepoGPGKey(ctx, repo)
		m.previewAPTRepoSourceLine(ctx, repo)
	}
}

// previewAPTRepoGPGKey checks and reports GPG key status for an APT repository.
func (m *Manager) previewAPTRepoGPGKey(ctx context.Context, repo provision.APTRepository) {
	if repo.GPGKey.Dest == "" {
		return
	}

	exists := m.fileExistsOnRemote(ctx, repo.GPGKey.Dest)
	if exists {
		_, _ = fmt.Fprintf(m.options.ProgressOut, "= key exists %s\n", repo.GPGKey.Dest)
	} else {
		_, _ = fmt.Fprintf(m.options.ProgressOut, "+ install key %s\n", repo.GPGKey.Dest)
	}
}

// previewAPTRepoSourceLine checks and reports source line status for an APT repository.
func (m *Manager) previewAPTRepoSourceLine(ctx context.Context, repo provision.APTRepository) {
	if repo.SourceFile == "" || repo.SourceLine == "" {
		return
	}

	present := m.sourceLineExistsOnRemote(ctx, repo.SourceFile, repo.SourceLine)
	if present {
		_, _ = fmt.Fprintf(m.options.ProgressOut, "= repo line present in %s\n", repo.SourceFile)
	} else {
		_, _ = fmt.Fprintf(m.options.ProgressOut, "+ write repo line to %s\n", repo.SourceFile)
	}
}

// fileExistsOnRemote checks if a file exists on the remote system.
func (m *Manager) fileExistsOnRemote(ctx context.Context, path string) bool {
	if m.sshClient == nil {
		return false
	}

	_, err := m.sshClient.ExecuteCommand(ctx, fmt.Sprintf("test -f '%s'", path))

	return err == nil
}

// sourceLineExistsOnRemote checks if a source line exists in a file on the remote system.
func (m *Manager) sourceLineExistsOnRemote(ctx context.Context, file, line string) bool {
	if m.sshClient == nil {
		return false
	}

	_, err := m.sshClient.ExecuteCommand(ctx, fmt.Sprintf("grep -qF '%s' '%s'", line, file))

	return err == nil
}

// runPhasesSequential executes phases one by one (internal helper).
func (m *Manager) runPhasesSequential(ctx context.Context, phases []struct {
	name string
	fn   func(context.Context) error
}) error {
	for index, phase := range phases {
		if m.shouldSkipPhase(phase.name) {
			m.log.Infow("Skipping phase", "phase", phase.name, "reason", "checkpoint exists")

			if m.options.ProgressOut != nil {
				NewProgressReporter(m.options.ProgressOut, m.progress).ReportPhaseSkipped(phase.name, "resumed and previously completed")
			}

			continue
		}

		m.progress.CurrentStep = phase.name

		m.progress.CompletedSteps++
		if m.options.ProgressOut != nil {
			NewProgressReporter(m.options.ProgressOut, m.progress).ReportPhaseStart(phase.name, index, m.progress.TotalSteps)
		}

		if m.options.DryRun {
			m.log.Infow("DRY RUN: Would execute phase", "phase", phase.name)

			continue
		}

		err := m.errorHandler.ExecuteWithRetry(ctx, phase.name, func() error { return phase.fn(ctx) })
		if err != nil {
			m.progress.Errors = append(m.progress.Errors, err)
			metadata := map[string]interface{}{"failed_phase": phase.name, "error_type": "execution_failure", "timestamp": time.Now()}
			_ = m.checkpointManager.Save(m.progress, metadata)

			return fmt.Errorf("phase %s failed: %w", phase.name, err)
		}

		m.checkpointManager.MarkPhaseCompleted(m.progress, phase.name)

		_ = m.checkpointManager.Save(m.progress, map[string]interface{}{"completed_phase": phase.name, "timestamp": time.Now()})
		if m.options.ProgressOut != nil {
			NewProgressReporter(m.options.ProgressOut, m.progress).ReportPhaseComplete(phase.name, time.Since(m.progress.StartTime))
		}
	}

	return nil
}

// runPhasesParallel executes phases concurrently with a worker limit.
func (m *Manager) runPhasesParallel(ctx context.Context, phases []struct {
	name string
	fn   func(context.Context) error
}) error {
	workers := m.options.MaxWorkers
	if workers <= 0 {
		workers = defaultWorkerCount
	}

	tasks := make(chan task)
	errs := make(chan error, len(phases))

	// Start workers
	for range workers {
		go m.phaseWorker(ctx, tasks, errs)
	}

	// Enqueue tasks
	for _, p := range phases {
		tasks <- task{name: p.name, fn: p.fn}
	}

	close(tasks)

	// Collect results
	return m.collectWorkerResults(errs, len(phases))
}

// phaseWorker processes tasks from the task channel and reports results to the error channel.
func (m *Manager) phaseWorker(ctx context.Context, tasks <-chan task, errs chan<- error) {
	for task := range tasks {
		if m.shouldSkipPhase(task.name) {
			if m.options.ProgressOut != nil {
				NewProgressReporter(m.options.ProgressOut, m.progress).ReportPhaseSkipped(task.name, "resumed and previously completed")
			}

			errs <- nil

			continue
		}

		if m.options.ProgressOut != nil {
			NewProgressReporter(m.options.ProgressOut, m.progress).ReportPhaseStart(task.name, 0, 0)
		}

		var err error

		if m.options.DryRun {
			m.log.Infow("DRY RUN: Would execute phase", "phase", task.name)
		} else {
			err = m.errorHandler.ExecuteWithRetry(ctx, task.name, func() error { return task.fn(ctx) })
		}

		if err == nil {
			m.checkpointManager.MarkPhaseCompleted(m.progress, task.name)

			_ = m.checkpointManager.Save(m.progress, map[string]interface{}{"completed_phase": task.name, "timestamp": time.Now()})
			if m.options.ProgressOut != nil {
				NewProgressReporter(m.options.ProgressOut, m.progress).ReportPhaseComplete(task.name, time.Since(m.progress.StartTime))
			}
		} else {
			m.progress.Errors = append(m.progress.Errors, err)
			_ = m.checkpointManager.Save(m.progress, map[string]interface{}{"failed_phase": task.name, "error_type": "execution_failure", "timestamp": time.Now()})
		}

		errs <- err
	}
}

// collectWorkerResults collects errors from worker goroutines and returns the first error encountered.
func (m *Manager) collectWorkerResults(errs <-chan error, numPhases int) error {
	var anyErr error

	for range numPhases {
		err := <-errs
		if err != nil {
			anyErr = err
		}
	}

	return anyErr
}

// getProviderInitializer returns the appropriate provider initializer.
//
//nolint:ireturn // returning interface type is intentional for provider pluggability
func (m *Manager) getProviderInitializer() (providers.BastionInitializer, error) {
	switch m.config.Provider {
	case "stackit":
		return providers.NewStackitBastionInit(m.config), nil
	case "aws":
		return providers.NewAWSBastionInit(m.config), nil
	case "azure":
		return providers.NewAzureBastionInit(m.config), nil
	case "gcp":
		return providers.NewGCPBastionInit(m.config), nil
	case "openstack":
		return providers.NewOpenStackBastionInit(m.config), nil
	case "vmware", "vsphere":
		return providers.NewVMwareBastionInit(m.config), nil
	default:
		return nil, ErrUnsupportedProvider(m.config.Provider)
	}
}

// createSSHClient creates an SSH client with the given connection details.
//
//nolint:ireturn // returning interface type is intentional to abstract SSH client
func (m *Manager) createSSHClient(details *ssh.ConnectionDetails) SSHClient {
	sshOptions := &ssh.ProvisioningOptions{
		DryRun:      m.options.DryRun,
		Force:       m.options.Force,
		Parallel:    m.options.Parallel,
		Resume:      m.options.Resume,
		Verbose:     m.options.Verbose,
		MaxWorkers:  m.options.MaxWorkers,
		ProgressOut: m.options.ProgressOut,
		LogFile:     m.options.LogFile,
	}

	return ssh.NewClient(details, sshOptions)
}

// loadProvisioningConfig loads the provisioning configuration.
//
//nolint:ireturn // returning interface type is intentional to abstract provision config
func (m *Manager) loadProvisioningConfig() provision.ProvisionConfig {
	return provision.NewConfig(m.config.Provider, m.config, m.deploymentModes)
}

// shouldSkipPhase determines if a phase should be skipped based on checkpoints.
func (m *Manager) shouldSkipPhase(phase string) bool {
	if !m.options.Resume {
		return false
	}

	return m.progress.Checkpoints[phase]
}

// getProgressReporter returns a progress reporter if configured.
func (m *Manager) getProgressReporter() *ProgressReporter {
	if m.options.ProgressOut != nil {
		return NewProgressReporter(m.options.ProgressOut, m.progress)
	}

	return nil
}

// saveCheckpoint saves the current progress state.
func (m *Manager) saveCheckpoint() error {
	checkpointPath := filepath.Join(os.Getenv("HOME"), ".ocfp", "checkpoints",
		fmt.Sprintf("bastion-%s.json", m.config.Name))

	// Implementation would save checkpoint data to file
	// For now, just create the directory
	dir := filepath.Dir(checkpointPath)

	err := os.MkdirAll(dir, logDirectoryMode)
	if err != nil {
		return fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	return nil
}

// Phase implementation functions

func (m *Manager) setupSystem(ctx context.Context) error {
	m.log.Info("Setting up system configuration")

	systemConfig := m.provConfig.GetSystemConfig()

	// Set hostname if configured
	if systemConfig.Hostname.Enabled && systemConfig.Hostname.Pattern != "" {
		m.configureHostname(ctx, systemConfig.Hostname.Pattern)
	}

	// Wait for system stabilization
	if systemConfig.WaitTime > 0 {
		time.Sleep(time.Duration(systemConfig.WaitTime) * time.Second)
	}

	return nil
}

func (m *Manager) configureHostname(ctx context.Context, pattern string) {
	desired := m.expandVariables(pattern)

	current, err := m.getCurrentHostname(ctx)
	if err != nil {
		m.log.Warn("Failed to read current hostname", "error", err.Error())

		return
	}

	if current == desired {
		m.log.Infow("Hostname already set", "hostname", desired)

		return
	}

	m.setHostname(ctx, desired)
}

func (m *Manager) getCurrentHostname(ctx context.Context) (string, error) {
	res, err := m.sshClient.ExecuteCommand(ctx, "hostname")
	if err != nil {
		return "", fmt.Errorf("failed to get current hostname: %w", err)
	}

	return strings.TrimSpace(res.Stdout), nil
}

func (m *Manager) setHostname(ctx context.Context, desired string) {
	cmd := fmt.Sprintf("sudo hostnamectl set-hostname '%s' && echo '127.0.0.1 %s' | sudo tee -a /etc/hosts >/dev/null", desired, desired)

	_, err := m.sshClient.ExecuteCommand(ctx, cmd)
	if err != nil {
		m.log.Warn("Failed to set hostname", "error", err.Error())
	} else {
		m.log.Infow("Hostname set", "hostname", desired)
	}
}

func (m *Manager) setupSSHAgentForwarding(ctx context.Context) error {
	m.log.Info("Configuring SSH agent forwarding")

	err := m.enableSSHDAgentForwarding(ctx)
	if err != nil {
		return err
	}

	m.configureSSHClientForwarding(ctx)
	m.addGitHubHostKeys(ctx)

	m.log.Info("SSH agent forwarding configured successfully")

	return nil
}

// enableSSHDAgentForwarding enables AllowAgentForwarding in sshd_config and restarts the SSH server.
func (m *Manager) enableSSHDAgentForwarding(ctx context.Context) error {
	checkCmd := "grep -q '^AllowAgentForwarding yes' /etc/ssh/sshd_config && echo 'enabled' || echo 'disabled'"

	result, err := m.sshClient.ExecuteCommand(ctx, checkCmd)
	if err != nil {
		m.log.Warn("Failed to check SSH agent forwarding status", "error", err.Error())

		return fmt.Errorf("failed to check SSH agent forwarding status: %w", err)
	}

	if strings.TrimSpace(result.Stdout) == "enabled" {
		return nil
	}

	m.log.Info("Enabling SSH agent forwarding in sshd_config")

	configureCmd := `sudo bash -c "grep -q '^AllowAgentForwarding' /etc/ssh/sshd_config && sudo sed -i 's/^AllowAgentForwarding.*/AllowAgentForwarding yes/' /etc/ssh/sshd_config || echo 'AllowAgentForwarding yes' | sudo tee -a /etc/ssh/sshd_config >/dev/null"`

	_, err = m.sshClient.ExecuteCommand(ctx, configureCmd)
	if err != nil {
		return fmt.Errorf("failed to configure SSH agent forwarding: %w", err)
	}

	return m.restartSSHDAndReconnect(ctx)
}

// restartSSHDAndReconnect restarts the SSH server and reconnects the client.
func (m *Manager) restartSSHDAndReconnect(ctx context.Context) error {
	m.log.Info("Restarting SSH server")

	restartCmd := "sudo systemctl restart sshd || sudo systemctl restart ssh"

	_, err := m.sshClient.ExecuteCommand(ctx, restartCmd)
	if err != nil {
		m.log.Warn("Failed to restart SSH server", "error", err.Error())

		return fmt.Errorf("failed to restart SSH server: %w", err)
	}

	m.log.Info("Waiting for SSH service to stabilize...")
	time.Sleep(3 * time.Second) //nolint:mnd // reasonable delay for SSH restart

	m.log.Info("Reconnecting with agent forwarding enabled")

	err = m.sshClient.Close()
	if err != nil {
		m.log.Warn("Failed to close SSH client during reconnection", "error", err.Error())
	}

	err = m.sshClient.Connect(ctx)
	if err != nil {
		return fmt.Errorf("failed to reconnect after SSH server restart: %w", err)
	}

	return nil
}

// configureSSHClientForwarding creates SSH client config to enable ForwardAgent for github.com.
func (m *Manager) configureSSHClientForwarding(ctx context.Context) {
	m.log.Info("Configuring SSH client config for agent forwarding")

	sshConfigContent := `# Auto-generated by OCFP bastion init
# Enable SSH agent forwarding for GitHub
Host github.com
    ForwardAgent yes
    StrictHostKeyChecking accept-new
`

	createSSHConfigCmd := fmt.Sprintf(`mkdir -p ~/.ssh && cat > ~/.ssh/config << 'EOF'
%s
EOF
chmod 600 ~/.ssh/config`, sshConfigContent)

	_, err := m.sshClient.ExecuteCommand(ctx, createSSHConfigCmd)
	if err != nil {
		m.log.Warn("Failed to create SSH client config", "error", err.Error())
	}
}

// addGitHubHostKeys adds GitHub host keys to known_hosts.
func (m *Manager) addGitHubHostKeys(ctx context.Context) {
	m.log.Info("Adding GitHub host keys to known_hosts")

	addGitHubKeysCmd := `mkdir -p ~/.ssh && ssh-keyscan -t rsa,ecdsa,ed25519 github.com >> ~/.ssh/known_hosts 2>/dev/null && chmod 600 ~/.ssh/known_hosts`

	_, err := m.sshClient.ExecuteCommand(ctx, addGitHubKeysCmd)
	if err != nil {
		m.log.Warn("Failed to add GitHub host keys", "error", err.Error())
	} else {
		m.log.Info("GitHub host keys added to known_hosts")
	}
}

func (m *Manager) createDirectories(ctx context.Context) error {
	m.log.Info("Creating directories")

	directories := m.provConfig.GetDirectories()
	for _, dir := range directories {
		expandedPath := m.expandVariables(dir.Path)

		cmd := fmt.Sprintf("mkdir -p '%s'", expandedPath)
		if dir.Mode != 0 {
			cmd += fmt.Sprintf(" && chmod %o '%s'", dir.Mode, expandedPath)
		}

		_, err := m.sshClient.ExecuteCommand(ctx, cmd)
		if err != nil {
			m.log.Error("Failed to create directory",
				"path", expandedPath,
				"error", err.Error())

			return fmt.Errorf("failed to create directory %s: %w", expandedPath, err)
		}

		m.log.Debugw("Directory created", "path", expandedPath)
	}

	return nil
}

func (m *Manager) copyConfigFiles(ctx context.Context) error {
	m.log.Info("Copying configuration files")

	// Copy OCFP configuration file
	err := m.copyOCFPConfig(ctx)
	if err != nil {
		m.log.Warn("Failed to copy OCFP config", "error", err.Error())
	}

	// Copy SSH keys
	err = m.copySSHKeys(ctx)
	if err != nil {
		m.log.Warn("Failed to copy SSH keys", "error", err.Error())
	}

	return nil
}

func (m *Manager) setupRepositories(ctx context.Context) error {
	m.log.Info("Setting up repositories")

	// Generate and execute repository setup script
	scriptGen := provision.NewScriptGenerator(m.config.Provider, m.config)
	envVars := m.getEnvironmentVariables()

	// Report progress for phases executed by the script
	m.reportRepositoryProgress()

	// Generate and execute provisioning script
	script, err := scriptGen.GenerateProvisioningScript(ctx, m.provConfig, envVars)
	if err != nil {
		return fmt.Errorf("failed to generate provisioning script: %w", err)
	}

	// Save script for debugging
	debugScriptPath := filepath.Join(os.TempDir(), "bastion-provision-debug.sh")
	_ = os.WriteFile(debugScriptPath, []byte(script), scriptFileMode)
	m.log.Debugw("Generated provisioning script saved", "path", debugScriptPath)

	return m.executeProvisioningScript(ctx, script)
}

// reportRepositoryProgress emits subtask planning progress for phases executed by the script.
func (m *Manager) reportRepositoryProgress() {
	if m.options.ProgressOut == nil {
		return
	}

	reporter := NewProgressReporter(m.options.ProgressOut, m.progress)

	m.reportSnapPackages(reporter)
	m.reportCPANModules(reporter)
	m.reportCFPlugins(reporter)
	m.reportBinaryTools(reporter)
}

// reportSnapPackages reports progress for snap packages.
func (m *Manager) reportSnapPackages(reporter *ProgressReporter) {
	snapMgr := provision.NewSnapManager(m.config.Provider, m.config)
	snaps := snapMgr.GetSnapPackages()

	enabledSnaps := filterEnabledSnaps(snaps)
	for i, s := range enabledSnaps {
		reporter.ReportSubtaskProgress("snap_packages", i, len(enabledSnaps), s.Name)
	}
}

// filterEnabledSnaps returns only enabled snap packages.
func filterEnabledSnaps(snaps []provision.SnapPackage) []provision.SnapPackage {
	var enabled []provision.SnapPackage

	for _, s := range snaps {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}

	return enabled
}

// reportCPANModules reports progress for CPAN modules.
func (m *Manager) reportCPANModules(reporter *ProgressReporter) {
	cpanMgr := provision.NewCPANManager(m.config.Provider, m.config)
	mods := cpanMgr.GetCPANModules()

	activeMods := filterActiveCPANModules(mods)
	for i, mdu := range activeMods {
		reporter.ReportSubtaskProgress("cpan_modules", i, len(activeMods), mdu.Name)
	}
}

// filterActiveCPANModules returns CPAN modules that are enabled or have a name.
func filterActiveCPANModules(mods []provision.CPANModule) []provision.CPANModule {
	var active []provision.CPANModule

	for _, mdu := range mods {
		if mdu.Enabled || mdu.Name != "" {
			active = append(active, mdu)
		}
	}

	return active
}

// reportCFPlugins reports progress for CF plugins.
func (m *Manager) reportCFPlugins(reporter *ProgressReporter) {
	cfMgr := provision.NewCFPluginManager(m.config.Provider, m.config)
	plugins := cfMgr.GetCFPlugins()

	enabledPlugins := filterEnabledCFPlugins(plugins)
	for i, p := range enabledPlugins {
		reporter.ReportSubtaskProgress("cf_plugins", i, len(enabledPlugins), p.Name)
	}
}

// filterEnabledCFPlugins returns only enabled CF plugins.
func filterEnabledCFPlugins(plugins []provision.CFPlugin) []provision.CFPlugin {
	var enabled []provision.CFPlugin

	for _, p := range plugins {
		if p.Enabled {
			enabled = append(enabled, p)
		}
	}

	return enabled
}

// reportBinaryTools reports progress for binary tools.
func (m *Manager) reportBinaryTools(reporter *ProgressReporter) {
	baseTools := m.provConfig.GetBinaryTools()
	advMgr := provision.NewAdvancedToolManager(m.config.Provider, m.config)
	advTools := advMgr.GetAdvancedBinaryTools()

	enabledBase := filterEnabledBinaryTools(baseTools)
	enabledAdv := filterEnabledAdvancedTools(advTools)
	totalTools := len(enabledBase) + len(enabledAdv)

	current := 0
	for _, t := range enabledBase {
		reporter.ReportSubtaskProgress("binary_tools", current, totalTools, t.Name)
		current++
	}

	for _, t := range enabledAdv {
		reporter.ReportSubtaskProgress("binary_tools", current, totalTools, t.Name)
		current++
	}
}

// filterEnabledBinaryTools returns only enabled binary tools.
func filterEnabledBinaryTools(tools []provision.BinaryTool) []provision.BinaryTool {
	var enabled []provision.BinaryTool

	for _, t := range tools {
		if t.Enabled {
			enabled = append(enabled, t)
		}
	}

	return enabled
}

// filterEnabledAdvancedTools returns only enabled advanced binary tools.
func filterEnabledAdvancedTools(tools []provision.AdvancedBinaryTool) []provision.AdvancedBinaryTool {
	var enabled []provision.AdvancedBinaryTool

	for _, t := range tools {
		if t.Enabled {
			enabled = append(enabled, t)
		}
	}

	return enabled
}

// executeProvisioningScript creates, transfers and executes a provisioning script on the bastion.
func (m *Manager) executeProvisioningScript(ctx context.Context, script string) error {
	// Copy script to bastion
	scriptPath := "/tmp/provision-bastion.sh"

	// Create temporary script file locally
	localScriptPath := filepath.Join(os.TempDir(), "provision-bastion.sh")

	err := os.WriteFile(localScriptPath, []byte(script), scriptFileMode)
	if err != nil {
		return fmt.Errorf("failed to write script file: %w", err)
	}

	defer func() {
		err := os.Remove(localScriptPath)
		if err != nil {
			m.log.Warn("Failed to remove temporary script file", "error", err.Error())
		}
	}()

	// Transfer script to bastion
	transferOpts := ssh.TransferOptions{
		Recursive:    false,
		Preserve:     false,
		Compress:     false,
		Progress:     nil,
		MaxRetries:   0,
		ChunkSize:    0,
		Verify:       true,
		BackupRemote: false,
	}

	err = m.sshClient.TransferFile(ctx, localScriptPath, scriptPath, transferOpts)
	if err != nil {
		return fmt.Errorf("failed to transfer script to bastion: %w", err)
	}

	// Execute script
	cmd := fmt.Sprintf("chmod +x '%s' && '%s'", scriptPath, scriptPath)

	result, err := m.sshClient.ExecuteCommand(ctx, cmd)
	if err != nil {
		m.log.Error("Script execution failed",
			"exit_code", result.ExitCode,
			"stdout", result.Stdout,
			"stderr", result.Stderr)

		return fmt.Errorf("%w: exit code %d, stderr: %s", ErrScriptExecutionFailed, result.ExitCode, result.Stderr)
	}

	m.log.Info("Provisioning script executed successfully")

	return nil
}

func (m *Manager) installPackages(ctx context.Context) error {
	// This is handled by the provisioning script
	return nil
}

func (m *Manager) installBinaryTools(ctx context.Context) error {
	// This is handled by the provisioning script
	return nil
}

func (m *Manager) cloneGitRepositories(ctx context.Context) error {
	m.log.Info("Cloning/updating git repositories")

	repos := m.provConfig.GetGitRepositories()
	if len(repos) == 0 {
		return nil
	}

	total := len(repos)
	completed := 0

	var reporter *ProgressReporter
	if m.options.ProgressOut != nil {
		reporter = NewProgressReporter(m.options.ProgressOut, m.progress)
	}

	// Worker pool setup
	workers := m.options.MaxWorkers
	if workers <= 0 {
		workers = defaultWorkerCount
	}

	jobs := make(chan job)
	errs := make(chan error, total)

	// Start workers
	for range workers {
		go m.gitCloneWorker(ctx, jobs, errs, reporter, total, &completed)
	}

	// Create and enqueue jobs
	m.createGitJobs(interface{}(repos), jobs)

	// Collect results
	var anyErr error

	for range total {
		err := <-errs
		if err != nil {
			anyErr = err
		}
	}

	return anyErr
}

// gitCloneWorker processes git clone/update jobs with retry logic for rate limits.
func (m *Manager) gitCloneWorker(ctx context.Context, jobs <-chan job, errs chan<- error, reporter *ProgressReporter, total int, completed *int) {
	for job := range jobs {
		// Execute with retry + backoff for rate limits
		backoff := gitBackoffInitial
		maxAttempts := gitMaxAttempts

		var err error
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			_, err = m.sshClient.ExecuteCommand(ctx, job.cmd)
			if err == nil {
				break
			}
			// Detect rate limit / transient
			emsg := strings.ToLower(err.Error())
			if strings.Contains(emsg, "rate limit") || strings.Contains(emsg, "429") || strings.Contains(emsg, "temporarily") || strings.Contains(emsg, "timeout") {
				m.log.Warnw("Git op limited, backing off", "repo", job.name, "attempt", attempt, "delay", backoff.String())

				select {
				case <-ctx.Done():
					break
				case <-time.After(backoff):
				}

				backoff *= 2
				// On final attempt, try without shallow depth
				if attempt == maxAttempts-1 {
					job.cmd = strings.ReplaceAll(job.cmd, " --depth 1", "")
				}

				continue
			}
			// Non-retryable
			break
		}

		if err != nil {
			errs <- fmt.Errorf("git op failed for %s: %w", job.name, err)
		} else {
			errs <- nil
		}

		*completed++
		if reporter != nil {
			reporter.ReportSubtaskProgress("git_repos", *completed, total, job.name)
		}
	}
}

// createGitJobs creates git clone/update jobs for each repository and enqueues them.
func (m *Manager) createGitJobs(repos interface{}, jobs chan<- job) {
	// Convert interface{} to git repositories
	gitRepos, ok := repos.([]provision.GitRepository)
	if !ok {
		m.log.Error("Invalid type for repos parameter, expected []provision.GitRepository")

		return
	}

	for index, repo := range gitRepos {
		dest := m.expandVariables(repo.Dest)
		branch := repo.Branch
		depth := repo.Depth
		// Build command: update if exists
		var cmd string
		// sanitize defaults
		if depth <= 0 {
			depth = gitDefaultDepth
		}

		if branch != "" {
			cmd = fmt.Sprintf("if [ -d '%s/.git' ]; then cd '%s' && git fetch --all --prune && git checkout '%s' && git pull --ff-only; else git clone '%s' -b '%s' --depth %d '%s'; fi", dest, dest, branch, repo.URL, branch, depth, dest)
		} else {
			cmd = fmt.Sprintf("if [ -d '%s/.git' ]; then cd '%s' && git fetch --all --prune && git pull --ff-only; else git clone '%s' --depth %d '%s'; fi", dest, dest, repo.URL, depth, dest)
		}

		jobs <- job{index: index, name: repo.Name, cmd: cmd}
	}

	close(jobs)
}

func (m *Manager) setupGenesis(ctx context.Context) error {
	// This is handled by the provisioning script
	return nil
}

func (m *Manager) runCustomScripts(ctx context.Context) error {
	// This is handled by the provisioning script
	return nil
}

func (m *Manager) verifyInstallation(ctx context.Context) error {
	m.log.Info("Verifying installation")

	// Check if provisioning completed successfully
	cmd := "test -f ~/.ocfp/provisioned && echo 'provisioned' || echo 'not-provisioned'"

	result, err := m.sshClient.ExecuteCommand(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to check provisioning status: %w", err)
	}

	if strings.TrimSpace(result.Stdout) != "provisioned" {
		return ErrBastionProvisioningDidNotComplete
	}

	// Verify key tools are available
	tools := []string{"genesis", "safe", "spruce", "vault", "bao", "bosh", "cf", "credhub", "uaa"}
	for _, tool := range tools {
		cmd := "command -v " + tool

		_, err := m.sshClient.ExecuteCommand(ctx, cmd)
		if err != nil {
			m.log.Warnw("Tool not available", "tool", tool)
		} else {
			m.log.Debugw("Tool verified", "tool", tool)
		}
	}

	return nil
}

// Helper methods

// copyOCFPConfig transfers a filtered OCFP configuration to the bastion.
// The filtered config includes global settings (debug, verbose) and only
// the bloc configuration being initialized (m.config.Name).
//
// The filtering process:
//  1. Loads the full ConfigFile from ~/.ocfp/config.yml
//  2. Extracts global debug/verbose settings
//  3. Includes only the bloc specified by m.config.Name
//  4. Marshals filtered config to YAML
//  5. Transfers via SSH to bastion ~/.ocfp/config.yml
//
// Returns an error if:
//   - Config file not found
//   - Bloc not present in config
//   - YAML marshaling fails
//   - SSH transfer fails
func (m *Manager) copyOCFPConfig(ctx context.Context) error { //nolint:funlen // multi-step config filtering process
	homeDir := os.Getenv("HOME")
	configPaths := []string{
		homeDir + "/.ocfp/config.yml",
		"config/config.yml",
	}

	var configPath string

	for _, path := range configPaths {
		_, err := os.Stat(path)
		if err == nil {
			configPath = path

			break
		}
	}

	if configPath == "" {
		return ErrOCFPConfigurationFileNotFound
	}

	// Load full ConfigFile structure
	configFileData := &config.ConfigFile{
		Debug:   false,
		Verbose: false,
		Blocs:   map[string]*config.Config{},
	}

	m.log.Debug("Loading config for filtering",
		"config_path", configPath,
		"bloc_name", m.config.Name)

	// Validate config path for security
	err := security.ValidatePath(configPath)
	if err != nil {
		return fmt.Errorf("invalid config path %s: %w", configPath, err)
	}

	configBytes, err := os.ReadFile(configPath) //nolint:gosec // path validated above
	if err != nil {
		return fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	err = yaml.Unmarshal(configBytes, configFileData)
	if err != nil {
		return fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	m.log.Debug("Config file loaded",
		"total_blocs", len(configFileData.Blocs),
		"debug", configFileData.Debug,
		"verbose", configFileData.Verbose)

	// Verify the bloc being initialized exists
	blocName := m.config.Name

	blocConfig, exists := configFileData.Blocs[blocName]
	if !exists {
		return fmt.Errorf("%w: bloc '%s' in file %s", ErrBlocNotFoundInConfig, blocName, configPath)
	}

	// Create filtered config with globals + single bloc
	filteredConfig := &config.ConfigFile{
		Debug:   configFileData.Debug,
		Verbose: configFileData.Verbose,
		Blocs: map[string]*config.Config{
			blocName: blocConfig,
		},
	}

	m.log.Info("Created filtered config",
		"bloc", blocName,
		"included_blocs", 1)

	// Marshal filtered config to YAML
	yamlBytes, err := yaml.Marshal(filteredConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal filtered config: %w", err)
	}

	// Create temp file for transfer
	tmpFile, err := os.CreateTemp("", "ocfp-config-*.yml")
	if err != nil {
		return fmt.Errorf("failed to create temp config file: %w", err)
	}

	tmpPath := tmpFile.Name()

	defer func() {
		removeErr := os.Remove(tmpPath)
		if removeErr != nil {
			m.log.Warn("Failed to remove temp config file", "path", tmpPath, "error", removeErr)
		}
	}()

	_, err = tmpFile.Write(yamlBytes)
	if err != nil {
		closeErr := tmpFile.Close()
		if closeErr != nil {
			m.log.Warn("Failed to close temp file after write error", "error", closeErr)
		}

		return fmt.Errorf("failed to write temp config file: %w", err)
	}

	err = tmpFile.Close()
	if err != nil {
		return fmt.Errorf("failed to close temp config file: %w", err)
	}

	// Transfer filtered config
	remoteConfigPath := "~/.ocfp/config.yml"
	transferOpts := ssh.TransferOptions{
		Recursive:    false,
		Preserve:     false,
		Compress:     false,
		Progress:     nil,
		MaxRetries:   0,
		ChunkSize:    0,
		Verify:       true,
		BackupRemote: false,
	}

	err = m.sshClient.TransferFile(ctx, tmpPath, remoteConfigPath, transferOpts)
	if err != nil {
		return fmt.Errorf("failed to transfer filtered config file: %w", err)
	}

	m.log.Info("Transferred filtered config to bastion",
		"bloc", blocName,
		"remote_path", remoteConfigPath)

	return nil
}

func (m *Manager) copySSHKeys(ctx context.Context) error {
	keyBaseName := m.config.Name + "-bastion"
	homeDir, _ := os.UserHomeDir()

	privateKeyPath := filepath.Join(homeDir, ".ssh", keyBaseName)
	publicKeyPath := filepath.Join(homeDir, ".ssh", keyBaseName+".pub")

	// Copy private key
	_, err := os.Stat(privateKeyPath)
	if err == nil {
		remotePrivateKey := "~/.ssh/" + keyBaseName
		transferOpts := ssh.TransferOptions{
			Recursive:    false,
			Preserve:     false,
			Compress:     false,
			Progress:     nil,
			MaxRetries:   0,
			ChunkSize:    0,
			Verify:       false,
			BackupRemote: false,
		}

		err := m.sshClient.TransferFile(ctx, privateKeyPath, remotePrivateKey, transferOpts)
		if err != nil {
			return fmt.Errorf("failed to copy private key: %w", err)
		}

		// Set proper permissions
		cmd := "chmod 600 ~/.ssh/" + keyBaseName

		_, err = m.sshClient.ExecuteCommand(ctx, cmd)
		if err != nil {
			m.log.Warn("Failed to set private key permissions", "error", err.Error())
		}
	}

	// Copy public key
	_, err = os.Stat(publicKeyPath)
	if err == nil {
		remotePublicKey := fmt.Sprintf("~/.ssh/%s.pub", keyBaseName)
		transferOpts := ssh.TransferOptions{
			Recursive:    false,
			Preserve:     false,
			Compress:     false,
			Progress:     nil,
			MaxRetries:   0,
			ChunkSize:    0,
			Verify:       false,
			BackupRemote: false,
		}

		err := m.sshClient.TransferFile(ctx, publicKeyPath, remotePublicKey, transferOpts)
		if err != nil {
			return fmt.Errorf("failed to copy public key: %w", err)
		}
	}

	return nil
}

func (m *Manager) getEnvironmentVariables() map[string]string {
	// This would get environment variables from the provider initializer
	// For now, return basic variables
	env := map[string]string{
		"OCFP_BLOC":     m.config.Name,
		"OCFP_PROVIDER": m.config.Provider,
	}

	// Add provider-specific variables based on the provider
	m.addProviderEnvVars(env)

	return env
}

// addProviderEnvVars adds provider-specific environment variables to the given map.
func (m *Manager) addProviderEnvVars(env map[string]string) {
	switch m.config.Provider {
	case "stackit":
		m.addStackitEnvVars(env)
	case "aws":
		m.addAWSEnvVars(env)
	}
}

// addStackitEnvVars adds STACKIT-specific environment variables.
func (m *Manager) addStackitEnvVars(env map[string]string) {
	if m.config.ProjectID != "" {
		env["STACKIT_PROJECT_ID"] = m.config.ProjectID
	}

	if m.config.OrgID != "" {
		env["STACKIT_ORG_ID"] = m.config.OrgID
	}

	if m.config.Region != "" {
		env["STACKIT_REGION"] = m.config.Region
	}
}

// addAWSEnvVars adds AWS-specific environment variables.
func (m *Manager) addAWSEnvVars(env map[string]string) {
	if m.config.AccessKeyID != "" {
		env["AWS_ACCESS_KEY_ID"] = m.config.AccessKeyID
	}

	if m.config.SecretAccessKey != "" {
		env["AWS_SECRET_ACCESS_KEY"] = m.config.SecretAccessKey
	}

	if m.config.Region != "" {
		env["AWS_DEFAULT_REGION"] = m.config.Region
	}
}

func (m *Manager) expandVariables(text string) string {
	// Simple variable expansion
	text = strings.ReplaceAll(text, "${OCFP_BLOC}", m.config.Name)
	text = strings.ReplaceAll(text, "${HOME}", "$HOME") // Let shell expand
	text = strings.ReplaceAll(text, "${USER}", "$USER") // Let shell expand

	return text
}

// buildProposedEnvironment merges desired env vars into current /etc/environment content
// It removes existing lines for the specified keys and appends the desired key=value lines.
func (m *Manager) buildProposedEnvironment(current string, desired map[string]string) string {
	// Normalize current lines
	lines := []string{}
	if current != "" {
		lines = strings.Split(current, "\n")
	}

	// Track keys to remove
	remove := map[string]struct{}{}
	order := []string{}

	for k := range desired {
		if _, seen := remove[k]; !seen {
			order = append(order, k)
		}

		remove[k] = struct{}{}
	}

	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}

		equalIndex := strings.IndexByte(line, '=')
		if equalIndex <= 0 {
			kept = append(kept, line)

			continue
		}

		key := line[:equalIndex]
		if _, ok := remove[key]; ok {
			// skip existing entry; it will be replaced
			continue
		}

		kept = append(kept, line)
	}

	// Append desired lines (key=value)
	for _, k := range order {
		kept = append(kept, fmt.Sprintf("%s=%s", k, desired[k]))
	}

	return strings.Join(kept, "\n") + "\n"
}
