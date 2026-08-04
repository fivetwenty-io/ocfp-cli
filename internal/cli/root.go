package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/aws"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/azure"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
	stackit "github.com/ocfp/ocfp-cli-go/internal/cpi/stackit"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/ocfp/ocfp-cli-go/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// lockInfo stores information about the command lock file for cleanup.
type lockInfo struct {
	timestamp time.Time
	pid       int
	tracker   *commands.CommandTracker
}

// Execute constructs the root command, configures flags, and runs it.
// A signal.NotifyContext wrapping os.Interrupt and SIGTERM ensures that
// Ctrl-C propagates cancellation into every cmd.Context() call site.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	flags := setupFlags()
	rootCmd := createRootCommand()

	// Set up config/init hooks
	cobra.OnInitialize(func() { initConfig(*flags.cfgFile, *flags.debug || *flags.verbose) })

	// Configure flags
	defineFlags(rootCmd, flags)
	bindFlagsToViper(rootCmd)

	// Lock info for tracking active commands
	lock := &lockInfo{}

	// Set prerun handler
	rootCmd.PersistentPreRun = createPreRunHandler(flags.blocName, lock)

	// Set postrun handler
	rootCmd.PersistentPostRun = createPostRunHandler(lock)

	// Set custom version template
	rootCmd.SetVersionTemplate(version.Get().String() + "\n")

	// Explicitly register providers
	err := stackit.Register()
	if err != nil {
		logger.Warnf("Failed to register STACKIT provider: %v", err)
	}

	err = aws.Register()
	if err != nil {
		logger.Warnf("Failed to register AWS provider: %v", err)
	}

	err = pve.Register()
	if err != nil {
		logger.Warnf("Failed to register Proxmox provider: %v", err)
	}

	err = azure.Register()
	if err != nil {
		logger.Warnf("Failed to register Azure provider: %v", err)
	}

	// Register all commands
	RegisterCommands(rootCmd)

	err = rootCmd.ExecuteContext(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		// os.Exit skips deferred calls; release the signal handler explicitly.
		stop()
		os.Exit(1) //nolint:gocritic // stop() invoked above before exit
	}
}

// flagConfig holds all flag values.
type flagConfig struct {
	cfgFile     *string
	blocName    *string
	debug       *bool
	verbose     *bool
	trace       *bool
	noLog       *bool
	region      *string
	debugLookup *bool
	asciiTables *bool
}

// setupFlags creates and returns flag configuration.
func setupFlags() *flagConfig {
	return &flagConfig{
		cfgFile:     new(string),
		blocName:    new(string),
		debug:       new(bool),
		verbose:     new(bool),
		trace:       new(bool),
		noLog:       new(bool),
		region:      new(string),
		debugLookup: new(bool),
		asciiTables: new(bool),
	}
}

// createRootCommand creates the root cobra command.
func createRootCommand() *cobra.Command {
	return &cobra.Command{
		Use:        "ocfp",
		Aliases:    nil,
		SuggestFor: nil,
		Short:      "Open Cloud Foundry Platform CLI",
		GroupID:    "",
		Long: `OCFP (Open Cloud Foundry Platform) is a toolkit for bootstrapping
and managing Cloud Foundry deployments across multiple cloud providers.

It provides infrastructure provisioning, configuration management, and
operational tooling for Cloud Foundry environments.`,
		Example:                "",
		ValidArgs:              nil,
		ValidArgsFunction:      nil,
		Args:                   nil,
		ArgAliases:             nil,
		BashCompletionFunction: "",
		Deprecated:             "",
		Annotations:            nil,
		Version:                version.Get().Short(),
		PersistentPreRun:       nil,
		PersistentPreRunE:      nil,
		PreRun:                 nil,
		PreRunE:                nil,
		Run:                    nil,
		RunE:                   nil,
		PostRun:                nil,
		PostRunE:               nil,
		PersistentPostRun:      nil,
		PersistentPostRunE:     nil,
		FParseErrWhitelist: cobra.FParseErrWhitelist{
			UnknownFlags: false,
		},
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd:   false,
			DisableNoDescFlag:   false,
			DisableDescriptions: false,
			HiddenDefaultCmd:    false,
		},
		TraverseChildren:           false,
		Hidden:                     false,
		SilenceErrors:              true,
		SilenceUsage:               true,
		DisableFlagParsing:         false,
		DisableAutoGenTag:          false,
		DisableFlagsInUseLine:      false,
		DisableSuggestions:         false,
		SuggestionsMinimumDistance: 0,
	}
}

// defineFlags sets up command line flags.
func defineFlags(cmd *cobra.Command, flags *flagConfig) {
	cmd.PersistentFlags().StringVar(flags.cfgFile, "config", "", "config file path")
	cmd.PersistentFlags().StringVar(flags.blocName, "bloc", "", "bloc name (key under blocs: in config)")
	cmd.PersistentFlags().StringVar(flags.blocName, "bloc-name", "", "deprecated: use --bloc instead")
	cmd.PersistentFlags().BoolVarP(flags.debug, "debug", "d", false, "enable debug output")
	cmd.PersistentFlags().BoolVarP(flags.verbose, "verbose", "v", false, "enable verbose output")
	cmd.PersistentFlags().BoolVar(flags.trace, "trace", false, "enable trace-level debugging")
	cmd.PersistentFlags().BoolVar(flags.noLog, "no-log", false, "disable logging to the OCFP state log directory (default ~/.local/state/ocfp/logs)")
	cmd.PersistentFlags().StringVar(flags.region, "region", "", "cloud region")
	cmd.PersistentFlags().BoolVar(flags.debugLookup, "debug-lookup", false, "print bastion lookup strategy matches")
	cmd.PersistentFlags().BoolVar(flags.asciiTables, "ascii", false, "use ASCII-only tables in output")

	// Deprecated flags for backward compatibility
	_ = cmd.PersistentFlags().MarkDeprecated("bloc-name", "use --bloc instead")
	_ = cmd.PersistentFlags().MarkHidden("bloc-name")
	cmd.PersistentFlags().StringVar(flags.blocName, "env-name", "", "deprecated: use --bloc instead")
	_ = cmd.PersistentFlags().MarkDeprecated("env-name", "use --bloc instead")
	_ = cmd.PersistentFlags().MarkHidden("env-name")
}

// bindFlagsToViper binds command flags to viper configuration.
func bindFlagsToViper(cmd *cobra.Command) {
	flagBindings := map[string]string{
		"config":       "config",
		"bloc":         "bloc",
		"debug":        "debug",
		"verbose":      "verbose",
		"trace":        "trace",
		"no_log":       "no-log",
		"region":       "region",
		"debug_lookup": "debug-lookup",
		"ascii":        "ascii",
	}

	for viperKey, flagName := range flagBindings {
		err := viper.BindPFlag(viperKey, cmd.PersistentFlags().Lookup(flagName))
		if err != nil {
			logger.Warnf("Failed to bind %s flag: %v", flagName, err)
		}
	}
}

// createPreRunHandler creates the persistent pre-run handler.
func createPreRunHandler(blocName *string, lock *lockInfo) func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, _args []string) {
		// Warn on unstamped builds: cobra's --version flag path returns before
		// PersistentPreRun ever runs (see (*cobra.Command).execute), so this
		// never touches --version or any stdout output; it always goes to
		// stderr and never blocks command execution. GitCommit=="unknown" is
		// the ldflags default (internal/version/version.go), so this fires
		// for any binary built without `go build $(LDFLAGS)` — e.g. a bare
		// `go build ./...` or `go run` — which is exactly the failure class
		// that produced the stale `bin/ocfp` binary in this repo.
		warnUnstampedBuild()

		// Determine bloc name: flag > env var > state file > viper config.
		// Check the invoked command's own flag set first: subcommands that
		// declare a local --bloc (artifacts, bastion, …) shadow the root
		// persistent flag, so *blocName stays empty and the state-file
		// fallback would silently override the user's explicit --bloc —
		// sending the command at whichever bloc was used last.
		effectiveBlocName := *blocName
		if f := cmd.Flags().Lookup("bloc"); f != nil && f.Changed {
			effectiveBlocName = f.Value.String()
		}

		if effectiveBlocName == "" {
			effectiveBlocName = resolveBlocName()
		}

		// Set in viper for other components to use
		if effectiveBlocName != "" {
			viper.Set("bloc", effectiveBlocName)
		}

		// Apply global UI settings
		ui.SetASCII(viper.GetBool("ascii"))

		// Extract command hierarchy for logging
		commandName := cmd.Name()
		subcommandName := ""

		// Check if this command has a parent that isn't root
		if cmd.Parent() != nil && cmd.Parent().Name() != "ocfp" {
			// This is a subcommand
			subcommandName = commandName
			commandName = cmd.Parent().Name()
		}

		// Initialize logger once flags and viper are available
		// Use effectiveBlocName directly to ensure correct value
		initErr := logger.Initialize(logger.Config{
			Level:      "",
			Debug:      viper.GetBool("debug"),
			Verbose:    viper.GetBool("verbose"),
			Trace:      viper.GetBool("trace"),
			NoLog:      viper.GetBool("no_log"),
			LogDir:     "",
			BlocName:   effectiveBlocName,
			Command:    commandName,
			Subcommand: subcommandName,
			RequestID:  "",
			DirectorID: "",
		})
		if initErr != nil {
			fmt.Fprintln(os.Stderr, "logger init:", initErr)
		}

		// Create lock file for active command tracking
		// Skip if no-log is enabled or if this is the logs command itself
		if !viper.GetBool("no_log") && commandName != "logs" {
			setupCommandTracking(lock, commandName, subcommandName)
		}
	}
}

// warnUnstampedBuild prints a one-line stderr warning when the running
// binary was built without ldflags version stamping (version.GitCommit still
// at its zero-value default of "unknown"). Unstamped builds run silently
// otherwise, which is exactly how the stale-binary incident this guards
// against went undetected: a `go build ./...` or `go run` invocation
// produces a working binary with no way to tell it apart from a properly
// stamped one except by inspecting `ocfp --version`. Always writes to
// stderr so it never contaminates stdout (JSON/table output, --version).
func warnUnstampedBuild() {
	if version.GitCommit != "unknown" {
		return
	}

	fmt.Fprintln(os.Stderr,
		"warning: unstamped build (git commit unknown) — run `make build` or `make install-local` to embed version info")
}

// resolveBlocName determines the bloc name from env var, state file, or viper config.
func resolveBlocName() string {
	if envBloc := os.Getenv("OCFP_BLOC"); envBloc != "" {
		return envBloc
	}

	stateBloc, err := config.GetCurrentBloc()
	if err == nil && stateBloc != "" {
		return stateBloc
	}

	if viperBloc := viper.GetString("bloc"); viperBloc != "" {
		return viperBloc
	}

	// If exactly one bloc defined in config, use it
	configFile := viper.GetString("config")

	blocs, err := config.ListBlocNames(configFile)
	if err == nil && len(blocs) == 1 {
		return blocs[0]
	}

	return ""
}

// setupCommandTracking initializes command tracking and lock file creation.
func setupCommandTracking(lock *lockInfo, commandName, subcommandName string) {
	baseDir, err := getBaseDir()
	if err != nil {
		if viper.GetBool("debug") {
			fmt.Fprintf(os.Stderr, "DEBUG: Failed to get base directory for command tracking: %v\n", err)
		}

		return
	}

	lock.timestamp = time.Now()
	lock.pid = os.Getpid()
	lock.tracker = commands.NewCommandTracker(baseDir)

	logPath := getExpectedLogPath(baseDir, viper.GetString("bloc"), commandName, subcommandName, lock.timestamp)
	activeCmd := commands.ActiveCommand{
		Timestamp:  lock.timestamp,
		PID:        lock.pid,
		Bloc:       viper.GetString("bloc"),
		Command:    commandName,
		Subcommand: subcommandName,
		LogPath:    logPath,
	}

	if viper.GetBool("debug") {
		fmt.Fprintf(os.Stderr, "DEBUG: Creating lock file for %s/%s at expected log path: %s\n",
			commandName, subcommandName, logPath)
	}

	err = lock.tracker.CreateLockFile(activeCmd)
	if err != nil {
		// Log the error but don't fail the command
		// This allows commands to continue even if tracking fails
		if viper.GetBool("debug") {
			fmt.Fprintf(os.Stderr, "DEBUG: Failed to create lock file for command tracking: %v\n", err)
			fmt.Fprintf(os.Stderr, "DEBUG: Active command tracking will not be available for this execution\n")
		}
		// Clear tracker to prevent cleanup attempts
		lock.tracker = nil

		return
	}

	if viper.GetBool("debug") {
		fmt.Fprintf(os.Stderr, "DEBUG: Successfully created lock file for command tracking: %s/%s\n",
			commandName, subcommandName)
	}
}

// createPostRunHandler creates the persistent post-run handler.
func createPostRunHandler(lock *lockInfo) func(*cobra.Command, []string) {
	return func(_cmd *cobra.Command, _args []string) {
		// Clean up lock file
		if lock.tracker != nil && !lock.timestamp.IsZero() {
			if viper.GetBool("debug") {
				fmt.Fprintf(os.Stderr, "DEBUG: PostRun cleanup: Removing lock file for timestamp %v, pid %d\n",
					lock.timestamp, lock.pid)
			}

			_ = lock.tracker.RemoveLockFile(lock.timestamp, lock.pid)
		}
	}
}

// getBaseDir returns the base directory for OCFP command-tracking state
// (active-command lock files and per-command log paths, see
// getExpectedLogPath). Resolves under StateHome() -- the same class helper
// GetLogDir() uses -- with a dual-read fallback to the pre-migration
// ~/.ocfp directory when only that already exists.
func getBaseDir() (string, error) {
	baseDir := config.StateHome()
	if baseDir == "" {
		return "", config.ErrOcfpHomeNotFound
	}

	baseDir, _ = config.ResolveExisting(baseDir, config.OcfpHome())

	return baseDir, nil
}

// getExpectedLogPath constructs the expected log path for a command.
func getExpectedLogPath(baseDir, bloc, command, subcommand string, timestamp time.Time) string {
	var pathParts []string
	if bloc != "" {
		pathParts = append(pathParts, baseDir, bloc, "logs", command)
	} else {
		pathParts = append(pathParts, baseDir, "logs", command)
	}

	if subcommand != "" {
		pathParts = append(pathParts, subcommand)
	}

	dir := filepath.Join(pathParts...)
	timestampStr := timestamp.Format("20060102-150405")
	filename := timestampStr + ".log"

	return filepath.Join(dir, filename)
}

// initConfig reads in config file and ENV variables if set.
func initConfig(cfgFile string, verbose bool) {
	// Set environment variable prefix
	viper.SetEnvPrefix("OCFP")
	viper.AutomaticEnv()

	// Explicitly bind bloc to environment variable
	// This ensures OCFP_BLOC takes precedence over config file
	_ = viper.BindEnv("bloc", "OCFP_BLOC")

	// Set default config path: the directory holding config.yml. Resolves
	// under ConfigHome() (the XDG config-class base), with a dual-read
	// fallback to the pre-migration ~/.ocfp/config.yml when only that
	// already exists.
	if os.Getenv("OCFP_CONFIG_PATH") == "" {
		if configHome := config.ConfigHome(); configHome != "" {
			newConfigFile := filepath.Join(configHome, "config.yml")
			legacyConfigFile := filepath.Join(config.OcfpHome(), "config.yml")

			resolvedFile, _ := config.ResolveExisting(newConfigFile, legacyConfigFile)
			_ = os.Setenv("OCFP_CONFIG_PATH", filepath.Dir(resolvedFile))
		}
	}

	// Determine config file to use
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Search for config.yml in standard locations
		viper.SetConfigName("config")
		viper.SetConfigType("yml")
		// Prefer the resolved config-class directory (ConfigHome(), or the
		// legacy ~/.ocfp when only that has a config.yml).
		viper.AddConfigPath(os.Getenv("OCFP_CONFIG_PATH"))
		// Then repo config directory
		viper.AddConfigPath("config")
		// And current directory
		viper.AddConfigPath(".")
	}

	// Read config file if it exists
	err := viper.ReadInConfig()
	if err == nil {
		if verbose {
			fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
		}
		// Surface the used config path for downstream consumers expecting viper("config")
		viper.Set("config", viper.ConfigFileUsed())
	}
}
