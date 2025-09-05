package bastion

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/providers"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/provision"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/pmezard/go-difflib/difflib"
)

// Manager orchestrates bastion initialization across providers
type Manager struct {
	config            *config.Config
	options           *ProvisioningOptions
	progress          *ProvisioningProgress
	sshClient         SSHClient
	provConfig        provision.ProvisionConfig
	checkpointManager *CheckpointManager
	errorHandler      *ErrorHandler
	log               logger.Logger
}

// NewManager creates a new bastion initialization manager
func NewManager(cfg *config.Config, opts *ProvisioningOptions) *Manager {
	checkpointMgr := NewCheckpointManager(cfg)

	// Load existing progress if resuming
	var progress *ProvisioningProgress
	if opts.Resume {
		if checkpoint, err := checkpointMgr.Load(); err == nil && checkpoint != nil {
			progress = checkpointMgr.RestoreProgress(checkpoint)
		} else {
			progress = &ProvisioningProgress{
				StartTime:   time.Now(),
				Checkpoints: make(map[string]bool),
			}
		}
	} else {
		progress = &ProvisioningProgress{
			StartTime:   time.Now(),
			Checkpoints: make(map[string]bool),
		}
	}

	return &Manager{
		config:            cfg,
		options:           opts,
		progress:          progress,
		checkpointManager: checkpointMgr,
		errorHandler:      NewErrorHandler(),
		log:               logger.Get(),
	}
}

// Initialize performs the complete bastion initialization process
func (m *Manager) Initialize(ctx context.Context) error {
	m.log.Info("Starting bastion initialization",
		"bloc", m.config.Name,
		"provider", m.config.Provider)

	if err := m.validatePrerequisites(); err != nil {
		return fmt.Errorf("prerequisite validation failed: %w", err)
	}

	// Get provider-specific initializer
	initializer, err := m.getProviderInitializer()
	if err != nil {
		return fmt.Errorf("failed to get provider initializer: %w", err)
	}

	// Validate provider configuration
	if err := initializer.Validate(); err != nil {
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
	m.sshClient, err = m.createSSHClient(connDetails)
	if err != nil {
		return fmt.Errorf("failed to create SSH client: %w", err)
	}
	defer func() {
		if err := m.sshClient.Close(); err != nil {
			m.log.Warn("Failed to close SSH client", "error", err.Error())
		}
	}()

	// Connect to bastion
	if err := m.sshClient.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to bastion: %w", err)
	}

	// Load provisioning configuration
	m.provConfig, err = m.loadProvisioningConfig()
	if err != nil {
		return fmt.Errorf("failed to load provisioning config: %w", err)
	}

	// If dry-run, preview configuration file changes
	if m.options.DryRun {
		_ = m.previewConfigChanges(ctx)
	}

	// Execute initialization phases - comprehensive implementation
	phases := []struct {
		name string
		fn   func(context.Context) error
	}{
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

	m.progress.TotalSteps = len(phases)

	// Start progress reporting if configured
	var reporter *ProgressReporter
	if m.options.ProgressOut != nil {
		reporter = NewProgressReporter(m.options.ProgressOut, m.progress)
		reporter.Start(ctx)
	}

	// If parallel is enabled, run a coarse-grained parallel block for safe phases
	if m.options.Parallel {
		// Sequential pre-parallel phases
		pre := []struct {
			name string
			fn   func(context.Context) error
		}{
			{"prerequisite_check", m.runPrerequisiteChecks},
			{"system_setup", m.setupSystem},
			{"directories", m.createDirectories},
			{"config_files", m.copyConfigFiles},
			{"ocfp_directories", m.setupOCFPDirectories},
			{"configuration_files", m.createConfigFiles},
			{"repositories", m.setupRepositories},
			{"packages", m.installPackages}, // avoid dpkg lock issues
		}
		if err := m.runPhasesSequential(ctx, pre); err != nil {
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
		if err := m.runPhasesParallel(ctx, par); err != nil {
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
		if err := m.runPhasesSequential(ctx, post); err != nil {
			return err
		}

		// Set completed steps and finalize reporter below
	} else {
		for index, phase := range phases {
			if m.shouldSkipPhase(phase.name) {
				m.log.Info("Skipping phase", "phase", phase.name, "reason", "checkpoint exists")
				if reporter != nil {
					reporter.ReportPhaseSkipped(phase.name, "resumed and previously completed")
				}
				continue
			}

			m.progress.CurrentStep = phase.name
			m.progress.CompletedSteps = index

			m.log.Info("Executing phase",
				"phase", phase.name,
				"progress", fmt.Sprintf("%d/%d", index+1, len(phases)))

			if reporter != nil {
				reporter.ReportPhaseStart(phase.name, index, len(phases))
			}

			if m.options.DryRun {
				m.log.Info("DRY RUN: Would execute phase", "phase", phase.name)
				continue
			}

			// Execute phase with error handling and retry
			err := m.errorHandler.ExecuteWithRetry(ctx, phase.name, func() error {
				return phase.fn(ctx)
			})

			if err != nil {
				m.progress.Errors = append(m.progress.Errors, err)

				// Save checkpoint with failure information
				metadata := map[string]interface{}{
					"failed_phase": phase.name,
					"error_type":   "execution_failure",
					"attempt":      index + 1,
				}
				if saveErr := m.checkpointManager.Save(m.progress, metadata); saveErr != nil {
					m.log.Warn("Failed to save failure checkpoint", "error", saveErr.Error())
				}

				if reporter != nil {
					// Best effort to surface the error context
					reporter.ReportError(phase.name, err, m.errorHandler.maxRetries, m.errorHandler.maxRetries)
				}
				return fmt.Errorf("phase %s failed: %w", phase.name, err)
			}

			// Mark phase as completed
			m.checkpointManager.MarkPhaseCompleted(m.progress, phase.name)

			// Save checkpoint with success information
			metadata := map[string]interface{}{
				"completed_phase": phase.name,
				"progress":        float64(index+1) / float64(len(phases)) * 100,
				"timestamp":       time.Now(),
			}
			if err := m.checkpointManager.Save(m.progress, metadata); err != nil {
				m.log.Warn("Failed to save checkpoint", "error", err)
			}

			if reporter != nil {
				reporter.ReportPhaseComplete(phase.name, time.Since(m.progress.StartTime))
			}
		}
	}

	m.progress.CompletedSteps = len(phases)
	duration := time.Since(m.progress.StartTime)

	// Clear checkpoint on successful completion
	if err := m.checkpointManager.Clear(); err != nil {
		m.log.Warn("Failed to clear checkpoint", "error", err)
	}

	// Cleanup old checkpoints (older than 7 days)
	if err := m.checkpointManager.CleanupOldCheckpoints(7 * 24 * time.Hour); err != nil {
		m.log.Warn("Failed to cleanup old checkpoints", "error", err)
	}

	// Report final success
	if reporter != nil {
		reporter.ReportFinalSummary(true, duration, len(phases), len(m.progress.Errors))
	}

	m.log.Info("Bastion initialization completed successfully",
		"duration", duration.String(),
		"total_phases", len(phases),
		"errors_encountered", len(m.progress.Errors))

	return nil
}

// validatePrerequisites checks that required prerequisites are met
func (m *Manager) validatePrerequisites() error {
	m.log.Debug("Validating prerequisites")

	// Check required configuration
	if m.config.Name == "" {
		return fmt.Errorf("bloc name is required")
	}
	if m.config.Provider == "" {
		return fmt.Errorf("provider is required")
	}

	// Check local tools
	requiredTools := []string{"ssh", "scp"}
	for _, tool := range requiredTools {
		if _, err := exec.LookPath(tool); err != nil {
			m.log.Warn("Required tool not found", "tool", tool)
		}
	}

	// Create local directories
	logDir := filepath.Join(os.Getenv("HOME"), ".ocfp", "logs", "provision")
	if err := os.MkdirAll(logDir, 0750); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Validate config overrides names (non-fatal warnings)
	m.validateOverrides()

	return nil
}

// validateOverrides warns about unknown names in enable/disable and override maps
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
				m.log.Warn("Unknown override name", "type", kind, "name", n)
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
			m.log.Warn("Unknown tool override key", "name", k)
		}
	}
	for k := range m.config.Bastion.SnapOverrides {
		if _, ok := snapNames[strings.ToLower(k)]; !ok {
			m.log.Warn("Unknown snap override key", "name", k)
		}
	}
	for k := range m.config.Bastion.CFPluginOverrides {
		if _, ok := pluginNames[strings.ToLower(k)]; !ok {
			m.log.Warn("Unknown CF plugin override key", "name", k)
		}
	}
}

// previewConfigChanges shows diffs for managed config files in dry-run
func (m *Manager) previewConfigChanges(ctx context.Context) error {
	cfm := provision.NewConfigFileManager(m.config.Provider, m.config)
	files := cfm.GetConfigFiles()
	if len(files) == 0 || m.options.ProgressOut == nil {
		// Preview system-managed files
		_, _ = fmt.Fprintln(m.options.ProgressOut, "\n== Dry-run: system file changes ==")
		// /etc/profile.d/ocfp.sh diff
		{
			envMgr := provision.NewEnvironmentManager(m.config.Provider, m.config)
			// Build expected profile content
			var b strings.Builder
			b.WriteString("#!/bin/sh\n# OCFP environment variables\n# Generated by ocfp bastion provisioning\n\n")
			for k, v := range envMgr.GetSystemEnvironmentVarsForPreview() {
				b.WriteString(fmt.Sprintf("export %s='%s'\n", k, v))
			}
			desired := b.String()
			current := ""
			if m.sshClient != nil {
				if res, err := m.sshClient.ExecuteCommand(ctx, "cat /etc/profile.d/ocfp.sh 2>/dev/null || true"); err == nil {
					current = res.Stdout
				}
			}
			if current == "" {
				_, _ = fmt.Fprintln(m.options.ProgressOut, "+ create /etc/profile.d/ocfp.sh")
			} else if current != desired {
				diff := difflib.UnifiedDiff{A: difflib.SplitLines(current), B: difflib.SplitLines(desired), FromFile: "/etc/profile.d/ocfp.sh (current)", ToFile: "/etc/profile.d/ocfp.sh (proposed)", Context: 3}
				if text, _ := difflib.GetUnifiedDiffString(diff); text != "" {
					_, _ = fmt.Fprintln(m.options.ProgressOut, text)
				}
			} else {
				_, _ = fmt.Fprintln(m.options.ProgressOut, "= no change /etc/profile.d/ocfp.sh")
			}
		}

		// /etc/environment full diff
		{
			envMgr := provision.NewEnvironmentManager(m.config.Provider, m.config)
			envVars := envMgr.GetSystemEnvironmentVarsForPreview()
			current := ""
			if m.sshClient != nil {
				if res, err := m.sshClient.ExecuteCommand(ctx, "cat /etc/environment 2>/dev/null || true"); err == nil {
					current = res.Stdout
				}
			}
			proposed := m.buildProposedEnvironment(current, envVars)
			_, _ = fmt.Fprintln(m.options.ProgressOut, "\n== /etc/environment changes ==")
			if strings.TrimSpace(current) == "" {
				// Treat as create
				diff := difflib.UnifiedDiff{A: []string{}, B: difflib.SplitLines(proposed), FromFile: "/etc/environment (current)", ToFile: "/etc/environment (proposed)", Context: 3}
				if text, _ := difflib.GetUnifiedDiffString(diff); text != "" {
					_, _ = fmt.Fprintln(m.options.ProgressOut, text)
				} else {
					_, _ = fmt.Fprintln(m.options.ProgressOut, "+ create /etc/environment")
				}
			} else if current != proposed {
				diff := difflib.UnifiedDiff{A: difflib.SplitLines(current), B: difflib.SplitLines(proposed), FromFile: "/etc/environment (current)", ToFile: "/etc/environment (proposed)", Context: 3}
				if text, _ := difflib.GetUnifiedDiffString(diff); text != "" {
					_, _ = fmt.Fprintln(m.options.ProgressOut, text)
				}
			} else {
				_, _ = fmt.Fprintln(m.options.ProgressOut, "= no change /etc/environment")
			}
		}

		// APT keys and sources plan
		{
			repos := m.provConfig.GetAPTRepositories()
			if len(repos) > 0 {
				_, _ = fmt.Fprintln(m.options.ProgressOut, "\nAPT repos plan:")
				for _, repo := range repos {
					if !repo.Enabled {
						continue
					}
					if repo.GPGKey.Dest != "" {
						exists := false
						if m.sshClient != nil {
							if _, err := m.sshClient.ExecuteCommand(ctx, fmt.Sprintf("test -f '%s'", repo.GPGKey.Dest)); err == nil {
								exists = true
							}
						}
						if exists {
							_, _ = fmt.Fprintf(m.options.ProgressOut, "= key exists %s\n", repo.GPGKey.Dest)
						} else {
							_, _ = fmt.Fprintf(m.options.ProgressOut, "+ install key %s\n", repo.GPGKey.Dest)
						}
					}
					if repo.SourceFile != "" && repo.SourceLine != "" {
						present := false
						if m.sshClient != nil {
							if _, err := m.sshClient.ExecuteCommand(ctx, fmt.Sprintf("grep -qF '%s' '%s'", repo.SourceLine, repo.SourceFile)); err == nil {
								present = true
							}
						}
						if present {
							_, _ = fmt.Fprintf(m.options.ProgressOut, "= repo line present in %s\n", repo.SourceFile)
						} else {
							_, _ = fmt.Fprintf(m.options.ProgressOut, "+ write repo line to %s\n", repo.SourceFile)
						}
					}
				}
			}
		}
		return nil
	}

	_, _ = fmt.Fprintln(m.options.ProgressOut, "\n== Dry-run: configuration file changes ==")
	for _, file := range files {
		// Expand path variables similar to script
		path := file.Path
		path = strings.ReplaceAll(path, "${HOME}", "$HOME")
		path = strings.ReplaceAll(path, "${USER}", "$USER")
		// Resolve $HOME locally if running local mode; otherwise leave tilde expanded by shell
		path = strings.ReplaceAll(path, "$HOME", os.Getenv("HOME"))

		var current string
		if m.sshClient != nil {
			// Read remote content if available
			res, execErr := m.sshClient.ExecuteCommand(ctx, fmt.Sprintf("cat '%s' 2>/dev/null || true", path))
			if execErr == nil {
				current = res.Stdout
			}
		} else {
			data, readErr := os.ReadFile(path) // #nosec G304 - path is validated above
			if readErr == nil {
				current = string(data)
			}
		}

		desired := file.Content
		if desired == "" {
			// Skip empty content creators
			continue
		}

		if current == "" {
			_, _ = fmt.Fprintf(m.options.ProgressOut, "\n+ create %s (mode %o)\n", path, file.Mode)
			continue
		}

		if current == desired {
			_, _ = fmt.Fprintf(m.options.ProgressOut, "\n= no change %s\n", path)
			continue
		}

		// Generate unified diff
		diff := difflib.UnifiedDiff{
			A:        difflib.SplitLines(current),
			B:        difflib.SplitLines(desired),
			FromFile: path + " (current)",
			ToFile:   path + " (proposed)",
			Context:  3,
		}
		text, _ := difflib.GetUnifiedDiffString(diff)
		_, _ = fmt.Fprintf(m.options.ProgressOut, "\n%s\n", text)
	}
	return nil
}

// runPhasesSequential executes phases one by one (internal helper)
func (m *Manager) runPhasesSequential(ctx context.Context, phases []struct {
	name string
	fn   func(context.Context) error
}) error {
	for index, phase := range phases {
		if m.shouldSkipPhase(phase.name) {
			m.log.Info("Skipping phase", "phase", phase.name, "reason", "checkpoint exists")
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
			m.log.Info("DRY RUN: Would execute phase", "phase", phase.name)
			continue
		}

		if err := m.errorHandler.ExecuteWithRetry(ctx, phase.name, func() error { return phase.fn(ctx) }); err != nil {
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

// runPhasesParallel executes phases concurrently with a worker limit
func (m *Manager) runPhasesParallel(ctx context.Context, phases []struct {
	name string
	fn   func(context.Context) error
}) error {
	workers := m.options.MaxWorkers
	if workers <= 0 {
		workers = 3
	}
	type task struct {
		name string
		fn   func(context.Context) error
	}
	tasks := make(chan task)
	errs := make(chan error, len(phases))

	// Workers
	for w := 0; w < workers; w++ {
		go func() {
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
					m.log.Info("DRY RUN: Would execute phase", "phase", task.name)
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
		}()
	}

	// Enqueue
	for _, p := range phases {
		tasks <- task{name: p.name, fn: p.fn}
	}
	close(tasks)

	// Collect
	var anyErr error
	for i := 0; i < len(phases); i++ {
		if err := <-errs; err != nil {
			anyErr = err
		}
	}
	return anyErr
}

// getProviderInitializer returns the appropriate provider initializer
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
		return nil, fmt.Errorf("unsupported provider: %s", m.config.Provider)
	}
}

// createSSHClient creates an SSH client with the given connection details
func (m *Manager) createSSHClient(details *ssh.ConnectionDetails) (SSHClient, error) {
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
	return ssh.NewClient(details, sshOptions), nil
}

// loadProvisioningConfig loads the provisioning configuration
func (m *Manager) loadProvisioningConfig() (provision.ProvisionConfig, error) {
	return provision.NewConfig(m.config.Provider, m.config), nil
}

// shouldSkipPhase determines if a phase should be skipped based on checkpoints
func (m *Manager) shouldSkipPhase(phase string) bool {
	if !m.options.Resume {
		return false
	}
	return m.progress.Checkpoints[phase]
}

// saveCheckpoint saves the current progress state
func (m *Manager) saveCheckpoint() error {
	checkpointPath := filepath.Join(os.Getenv("HOME"), ".ocfp", "checkpoints",
		fmt.Sprintf("bastion-%s.json", m.config.Name))

	// Implementation would save checkpoint data to file
	// For now, just create the directory
	dir := filepath.Dir(checkpointPath)
	return os.MkdirAll(dir, 0750)
}

// Phase implementation functions

func (m *Manager) setupSystem(ctx context.Context) error {
	m.log.Info("Setting up system configuration")

	systemConfig := m.provConfig.GetSystemConfig()

	// Set hostname if configured
	if systemConfig.Hostname.Enabled && systemConfig.Hostname.Pattern != "" {
		desired := m.expandVariables(systemConfig.Hostname.Pattern)

		// Read current hostname
		res, err := m.sshClient.ExecuteCommand(ctx, "hostname")
		if err != nil {
			m.log.Warn("Failed to read current hostname", "error", err.Error())
		}
		current := strings.TrimSpace(res.Stdout)

		if current == desired {
			m.log.Info("Hostname already set", "hostname", desired)
		} else {
			cmd := fmt.Sprintf("sudo hostnamectl set-hostname '%s' && echo '127.0.0.1 %s' | sudo tee -a /etc/hosts >/dev/null", desired, desired)
			if _, err := m.sshClient.ExecuteCommand(ctx, cmd); err != nil {
				m.log.Warn("Failed to set hostname", "error", err.Error())
			} else {
				m.log.Info("Hostname set", "hostname", desired)
			}
		}
	}

	// Wait for system stabilization
	if systemConfig.WaitTime > 0 {
		time.Sleep(time.Duration(systemConfig.WaitTime) * time.Second)
	}

	return nil
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

		if _, err := m.sshClient.ExecuteCommand(ctx, cmd); err != nil {
			m.log.Error("Failed to create directory",
				"path", expandedPath,
				"error", err.Error())
			return fmt.Errorf("failed to create directory %s: %w", expandedPath, err)
		}

		m.log.Debug("Directory created", "path", expandedPath)
	}

	return nil
}

func (m *Manager) copyConfigFiles(ctx context.Context) error {
	m.log.Info("Copying configuration files")

	// Copy OCFP configuration file
	if err := m.copyOCFPConfig(ctx); err != nil {
		m.log.Warn("Failed to copy OCFP config", "error", err.Error())
	}

	// Copy SSH keys
	if err := m.copySSHKeys(ctx); err != nil {
		m.log.Warn("Failed to copy SSH keys", "error", err.Error())
	}

	return nil
}

func (m *Manager) setupRepositories(ctx context.Context) error {
	m.log.Info("Setting up repositories")

	// Generate and execute repository setup script
	scriptGen := provision.NewScriptGenerator(m.config.Provider, m.config)
	envVars := m.getEnvironmentVariables()

	// Emit subtask planning progress for phases executed by the script
	if m.options.ProgressOut != nil {
		reporter := NewProgressReporter(m.options.ProgressOut, m.progress)

		// Snaps
		snapMgr := provision.NewSnapManager(m.config.Provider, m.config)
		snaps := snapMgr.GetSnapPackages()
		totalSnaps := 0
		for _, s := range snaps {
			if s.Enabled {
				totalSnaps++
			}
		}
		idx := 0
		for _, s := range snaps {
			if !s.Enabled {
				continue
			}
			reporter.ReportSubtaskProgress("snap_packages", 0, totalSnaps, s.Name)
			idx++
		}

		// CPAN modules
		cpanMgr := provision.NewCPANManager(m.config.Provider, m.config)
		mods := cpanMgr.GetCPANModules()
		totalMods := 0
		for _, mdu := range mods {
			if mdu.Enabled || mdu.Name != "" {
				totalMods++
			}
		}
		for _, mdu := range mods {
			if mdu.Enabled || mdu.Name != "" {
				reporter.ReportSubtaskProgress("cpan_modules", 0, totalMods, mdu.Name)
			}
		}

		// CF plugins
		cfMgr := provision.NewCFPluginManager(m.config.Provider, m.config)
		plugins := cfMgr.GetCFPlugins()
		totalPlugins := 0
		for _, p := range plugins {
			if p.Enabled {
				totalPlugins++
			}
		}
		for _, p := range plugins {
			if p.Enabled {
				reporter.ReportSubtaskProgress("cf_plugins", 0, totalPlugins, p.Name)
			}
		}

		// Binary tools (base + advanced)
		baseTools := m.provConfig.GetBinaryTools()
		advMgr := provision.NewAdvancedToolManager(m.config.Provider, m.config)
		advTools := advMgr.GetAdvancedBinaryTools()
		totalTools := 0
		for _, t := range baseTools {
			if t.Enabled {
				totalTools++
			}
		}
		for _, t := range advTools {
			if t.Enabled {
				totalTools++
			}
		}
		for _, t := range baseTools {
			if t.Enabled {
				reporter.ReportSubtaskProgress("binary_tools", 0, totalTools, t.Name)
			}
		}
		for _, t := range advTools {
			if t.Enabled {
				reporter.ReportSubtaskProgress("binary_tools", 0, totalTools, t.Name)
			}
		}
	}

	script, err := scriptGen.GenerateProvisioningScript(ctx, m.provConfig, envVars)
	if err != nil {
		return fmt.Errorf("failed to generate provisioning script: %w", err)
	}

	// Copy script to bastion
	scriptPath := "/tmp/provision-bastion.sh"

	// Create temporary script file locally
	localScriptPath := filepath.Join(os.TempDir(), "provision-bastion.sh")
	if err := os.WriteFile(localScriptPath, []byte(script), 0600); err != nil {
		return fmt.Errorf("failed to write script file: %w", err)
	}
	defer func() {
		if err := os.Remove(localScriptPath); err != nil {
			m.log.Warn("Failed to remove temporary script file", "error", err.Error())
		}
	}()

	// Transfer script to bastion
	transferOpts := ssh.TransferOptions{
		Verify: true,
	}
	if err := m.sshClient.TransferFile(ctx, localScriptPath, scriptPath, transferOpts); err != nil {
		return fmt.Errorf("failed to transfer script to bastion: %w", err)
	}

	// Execute script
	cmd := fmt.Sprintf("chmod +x '%s' && '%s'", scriptPath, scriptPath)
	result, err := m.sshClient.ExecuteCommand(ctx, cmd)
	if err != nil {
		m.log.Error("Script execution failed",
			"exit_code", result.ExitCode,
			"stderr", result.Stderr)
		return fmt.Errorf("script execution failed: %w", err)
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

	type job struct {
		index int
		name  string
		cmd   string
	}

	// Worker pool
	workers := m.options.MaxWorkers
	if workers <= 0 {
		workers = 3
	}
	jobs := make(chan job)
	errs := make(chan error, total)

	// spawn workers
	for w := 0; w < workers; w++ {
		go func() {
			for job := range jobs {
				// Execute with retry + backoff for rate limits
				backoff := 2 * time.Second
				maxAttempts := 4
				var err error
				for attempt := 1; attempt <= maxAttempts; attempt++ {
					_, err = m.sshClient.ExecuteCommand(ctx, job.cmd)
					if err == nil {
						break
					}
					// Detect rate limit / transient
					emsg := strings.ToLower(err.Error())
					if strings.Contains(emsg, "rate limit") || strings.Contains(emsg, "429") || strings.Contains(emsg, "temporarily") || strings.Contains(emsg, "timeout") {
						m.log.Warn("Git op limited, backing off", "repo", job.name, "attempt", attempt, "delay", backoff.String())
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
				completed++
				if reporter != nil {
					reporter.ReportSubtaskProgress("git_repos", completed, total, job.name)
				}
			}
		}()
	}

	// enqueue jobs
	for index, repo := range repos {
		dest := m.expandVariables(repo.Dest)
		branch := repo.Branch
		depth := repo.Depth
		// Build command: update if exists
		var cmd string
		// sanitize defaults
		if depth <= 0 {
			depth = 1
		}
		if branch != "" {
			cmd = fmt.Sprintf("if [ -d '%s/.git' ]; then cd '%s' && git fetch --all --prune && git checkout '%s' && git pull --ff-only; else git clone '%s' -b '%s' --depth %d '%s'; fi", dest, dest, branch, repo.URL, branch, depth, dest)
		} else {
			cmd = fmt.Sprintf("if [ -d '%s/.git' ]; then cd '%s' && git fetch --all --prune && git pull --ff-only; else git clone '%s' --depth %d '%s'; fi", dest, dest, repo.URL, depth, dest)
		}
		jobs <- job{index: index, name: repo.Name, cmd: cmd}
	}
	close(jobs)

	var anyErr error
	for i := 0; i < total; i++ {
		if err := <-errs; err != nil {
			anyErr = err
		}
	}
	return anyErr
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
		return fmt.Errorf("bastion provisioning did not complete successfully")
	}

	// Verify key tools are available
	tools := []string{"genesis", "safe", "spruce", "vault", "bosh", "cf"}
	for _, tool := range tools {
		cmd := fmt.Sprintf("command -v %s", tool)
		if _, err := m.sshClient.ExecuteCommand(ctx, cmd); err != nil {
			m.log.Warn("Tool not available", "tool", tool)
		} else {
			m.log.Debug("Tool verified", "tool", tool)
		}
	}

	return nil
}

// Helper methods

func (m *Manager) copyOCFPConfig(ctx context.Context) error {
	homeDir := os.Getenv("HOME")
	configPaths := []string{
		fmt.Sprintf("%s/.ocfp/config.yml", homeDir),
		"config/config.yml",
	}

	var configPath string
	for _, path := range configPaths {
		if _, err := os.Stat(path); err == nil {
			configPath = path
			break
		}
	}

	if configPath == "" {
		return fmt.Errorf("OCFP configuration file not found")
	}

	remoteConfigPath := "~/.ocfp/config.yml"
	transferOpts := ssh.TransferOptions{
		Verify: true,
	}

	return m.sshClient.TransferFile(ctx, configPath, remoteConfigPath, transferOpts)
}

func (m *Manager) copySSHKeys(ctx context.Context) error {
	keyBaseName := fmt.Sprintf("%s-bastion", m.config.Name)
	homeDir, _ := os.UserHomeDir()

	privateKeyPath := filepath.Join(homeDir, ".ssh", keyBaseName)
	publicKeyPath := filepath.Join(homeDir, ".ssh", keyBaseName+".pub")

	// Copy private key
	if _, err := os.Stat(privateKeyPath); err == nil {
		remotePrivateKey := fmt.Sprintf("~/.ssh/%s", keyBaseName)
		transferOpts := ssh.TransferOptions{}

		if err := m.sshClient.TransferFile(ctx, privateKeyPath, remotePrivateKey, transferOpts); err != nil {
			return fmt.Errorf("failed to copy private key: %w", err)
		}

		// Set proper permissions
		cmd := fmt.Sprintf("chmod 600 ~/.ssh/%s", keyBaseName)
		if _, err := m.sshClient.ExecuteCommand(ctx, cmd); err != nil {
			m.log.Warn("Failed to set private key permissions", "error", err.Error())
		}
	}

	// Copy public key
	if _, err := os.Stat(publicKeyPath); err == nil {
		remotePublicKey := fmt.Sprintf("~/.ssh/%s.pub", keyBaseName)
		transferOpts := ssh.TransferOptions{}

		if err := m.sshClient.TransferFile(ctx, publicKeyPath, remotePublicKey, transferOpts); err != nil {
			return fmt.Errorf("failed to copy public key: %w", err)
		}
	}

	return nil
}

func (m *Manager) getEnvironmentVariables() map[string]string {
	// This would get environment variables from the provider initializer
	// For now, return basic variables
	env := map[string]string{
		"OCFP_BLOC_NAME": m.config.Name,
		"OCFP_PROVIDER":  m.config.Provider,
	}

	// Add provider-specific variables based on the provider
	switch m.config.Provider {
	case "stackit":
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

	return env
}

func (m *Manager) expandVariables(text string) string {
	// Simple variable expansion
	text = strings.ReplaceAll(text, "${OCFP_BLOC_NAME}", m.config.Name)
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
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			kept = append(kept, line)
			continue
		}
		key := line[:eq]
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
