package cli

import (
	"fmt"
	"os"
	"path/filepath"

	stackit "github.com/ocfp/ocfp-cli-go/internal/cpi/stackit"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/ocfp/ocfp-cli-go/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Execute constructs the root command, configures flags, and runs it.
func Execute() {
	// Local flag variables (no globals)
	var (
		cfgFile     string
		blocName    string
		debug       bool
		verbose     bool
		trace       bool
		noLog       bool
		region      string
		debugLookup bool
		asciiTables bool
	)

	rootCmd := &cobra.Command{
		Use:   "ocfp",
		Short: "Open Cloud Foundry Platform CLI",
		Long: `OCFP (Open Cloud Foundry Platform) is a toolkit for bootstrapping 
and managing Cloud Foundry deployments across multiple cloud providers.

It provides infrastructure provisioning, configuration management, and
operational tooling for Cloud Foundry environments.`,
		Version: version.Get().Short(),
	}

	// Set up config/init hooks
	cobra.OnInitialize(func() { initConfig(cfgFile, debug || verbose) })

	// Global flags
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "f", "", "config file path")
	rootCmd.PersistentFlags().StringVar(&blocName, "bloc", "", "bloc name (key under blocs: in config)")
	// Backwards-compat: deprecated alias for --bloc
	rootCmd.PersistentFlags().StringVar(&blocName, "bloc-name", "", "deprecated: use --bloc instead")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "enable debug output")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose output")
	rootCmd.PersistentFlags().BoolVar(&trace, "trace", false, "enable trace-level debugging")
	rootCmd.PersistentFlags().BoolVar(&noLog, "no-log", false, "disable logging to ~/.ocfp/logs/")
	rootCmd.PersistentFlags().StringVar(&region, "region", "", "cloud region")
	// Provider is derived from bloc config; no global --iaas flag
	rootCmd.PersistentFlags().BoolVar(&debugLookup, "debug-lookup", false, "print bastion lookup strategy matches")
	// ASCII tables (global rendering toggle in ui package)
	rootCmd.PersistentFlags().BoolVar(&asciiTables, "ascii", false, "use ASCII-only tables in output")

	// Bind flags to viper
	if err := viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config")); err != nil {
		logger.Warnf("Failed to bind config flag: %v", err)
	}

	if err := viper.BindPFlag("bloc_name", rootCmd.PersistentFlags().Lookup("bloc")); err != nil {
		logger.Warnf("Failed to bind bloc flag: %v", err)
	}

	// Ensure viper sees the chosen bloc value even if only --bloc-name is used
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if blocName != "" {
			viper.Set("bloc_name", blocName)
		}
		// Apply global UI settings
		ui.SetASCII(viper.GetBool("ascii"))

		// Initialize logger once flags and viper are available
		_ = logger.Initialize(logger.Config{
			Debug:    viper.GetBool("debug"),
			Verbose:  viper.GetBool("verbose"),
			Trace:    viper.GetBool("trace"),
			NoLog:    viper.GetBool("no_log"),
			BlocName: viper.GetString("bloc_name"),
			Command:  cmd.Name(),
		})
	}

	if err := viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug")); err != nil {
		logger.Warnf("Failed to bind debug flag: %v", err)
	}

	if err := viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose")); err != nil {
		logger.Warnf("Failed to bind verbose flag: %v", err)
	}

	if err := viper.BindPFlag("trace", rootCmd.PersistentFlags().Lookup("trace")); err != nil {
		logger.Warnf("Failed to bind trace flag: %v", err)
	}

	if err := viper.BindPFlag("no_log", rootCmd.PersistentFlags().Lookup("no-log")); err != nil {
		logger.Warnf("Failed to bind no-log flag: %v", err)
	}

	if err := viper.BindPFlag("region", rootCmd.PersistentFlags().Lookup("region")); err != nil {
		logger.Warnf("Failed to bind region flag: %v", err)
	}
	// No binding for iaas; provider should come from bloc config
	if err := viper.BindPFlag("debug_lookup", rootCmd.PersistentFlags().Lookup("debug-lookup")); err != nil {
		logger.Warnf("Failed to bind debug-lookup flag: %v", err)
	}

	if err := viper.BindPFlag("ascii", rootCmd.PersistentFlags().Lookup("ascii")); err != nil {
		logger.Warnf("Failed to bind ascii flag: %v", err)
	}

	// Set custom version template
	rootCmd.SetVersionTemplate(version.Get().String() + "\n")

	// Deprecated flags for backward compatibility
	_ = rootCmd.PersistentFlags().MarkDeprecated("bloc-name", "use --bloc instead")
	_ = rootCmd.PersistentFlags().MarkHidden("bloc-name")
	rootCmd.PersistentFlags().StringVar(&blocName, "env-name", "", "deprecated: use --bloc instead")
	_ = rootCmd.PersistentFlags().MarkDeprecated("env-name", "use --bloc instead")
	_ = rootCmd.PersistentFlags().MarkHidden("env-name")

	// Explicitly register providers
	if err := stackit.Register(); err != nil {
		logger.Warnf("Failed to register STACKIT provider: %v", err)
	}

	// Register all commands
	RegisterCommands(rootCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
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
	if err := viper.ReadInConfig(); err == nil {
		if verbose {
			fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
		}
		// Surface the used config path for downstream consumers expecting viper("config")
		viper.Set("config", viper.ConfigFileUsed())
	}
}
