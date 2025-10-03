package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ocfp/ocfp-cli-go/internal/cpi/aws"
	stackit "github.com/ocfp/ocfp-cli-go/internal/cpi/stackit"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/ocfp/ocfp-cli-go/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Execute constructs the root command, configures flags, and runs it.
func Execute() {
	flags := setupFlags()
	rootCmd := createRootCommand()

	// Set up config/init hooks
	cobra.OnInitialize(func() { initConfig(*flags.cfgFile, *flags.debug || *flags.verbose) })

	// Configure flags
	defineFlags(rootCmd, flags)
	bindFlagsToViper(rootCmd)

	// Set prerun handler
	rootCmd.PersistentPreRun = createPreRunHandler(flags.blocName)

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

	// Register all commands
	RegisterCommands(rootCmd)

	err = rootCmd.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
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
		SilenceUsage:               false,
		DisableFlagParsing:         false,
		DisableAutoGenTag:          false,
		DisableFlagsInUseLine:      false,
		DisableSuggestions:         false,
		SuggestionsMinimumDistance: 0,
	}
}

// defineFlags sets up command line flags.
func defineFlags(cmd *cobra.Command, flags *flagConfig) {
	cmd.PersistentFlags().StringVarP(flags.cfgFile, "config", "f", "", "config file path")
	cmd.PersistentFlags().StringVar(flags.blocName, "bloc", "", "bloc name (key under blocs: in config)")
	cmd.PersistentFlags().StringVar(flags.blocName, "bloc-name", "", "deprecated: use --bloc instead")
	cmd.PersistentFlags().BoolVarP(flags.debug, "debug", "d", false, "enable debug output")
	cmd.PersistentFlags().BoolVarP(flags.verbose, "verbose", "v", false, "enable verbose output")
	cmd.PersistentFlags().BoolVar(flags.trace, "trace", false, "enable trace-level debugging")
	cmd.PersistentFlags().BoolVar(flags.noLog, "no-log", false, "disable logging to ~/.ocfp/logs/")
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
func createPreRunHandler(blocName *string) func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, args []string) {
		if *blocName != "" {
			viper.Set("bloc", *blocName)
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
		_ = logger.Initialize(logger.Config{
			Level:      "",
			Debug:      viper.GetBool("debug"),
			Verbose:    viper.GetBool("verbose"),
			Trace:      viper.GetBool("trace"),
			NoLog:      viper.GetBool("no_log"),
			LogDir:     "",
			BlocName:   viper.GetString("bloc"),
			Command:    commandName,
			Subcommand: subcommandName,
			RequestID:  "",
			DirectorID: "",
		})
	}
}

// initConfig reads in config file and ENV variables if set.
func initConfig(cfgFile string, verbose bool) {
	// Set environment variable prefix
	viper.SetEnvPrefix("OCFP")
	viper.AutomaticEnv()

	// Set default config path
	if os.Getenv("OCFP_CONFIG_PATH") == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			_ = os.Setenv("OCFP_CONFIG_PATH", filepath.Join(home, ".ocfp"))
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
		// Prefer ~/.ocfp
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
