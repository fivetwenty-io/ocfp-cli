package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ocfp/ocfp-cli-go/internal/version"
	_ "github.com/ocfp/ocfp-cli-go/internal/cpi/stackit" // Register STACKIT provider
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile   string
	blocName  string
	debug     bool
	verbose   bool
	trace     bool
	noLog     bool
	region    string
	iaas      string
)

// rootCmd represents the base command
var rootCmd = &cobra.Command{
	Use:   "ocfp",
	Short: "Open Cloud Foundry Platform CLI",
	Long: `OCFP (Open Cloud Foundry Platform) is a toolkit for bootstrapping 
and managing Cloud Foundry deployments across multiple cloud providers.

It provides infrastructure provisioning, configuration management, and
operational tooling for Cloud Foundry environments.`,
	Version: version.Get().Short(),
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Global flags
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "f", "", "config file path")
	rootCmd.PersistentFlags().StringVar(&blocName, "bloc-name", "", "bloc name (uses config/<bloc-name>.yml)")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "enable debug output")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose output")
	rootCmd.PersistentFlags().BoolVar(&trace, "trace", false, "enable trace-level debugging")
	rootCmd.PersistentFlags().BoolVar(&noLog, "no-log", false, "disable logging to ~/.ocfp/logs/")
	rootCmd.PersistentFlags().StringVar(&region, "region", "", "cloud region")
	rootCmd.PersistentFlags().StringVar(&iaas, "iaas", "", "cloud provider (stackit, openstack, aws, gcp, azure)")

	// Bind flags to viper
	viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	viper.BindPFlag("bloc_name", rootCmd.PersistentFlags().Lookup("bloc-name"))
	viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))
	viper.BindPFlag("trace", rootCmd.PersistentFlags().Lookup("trace"))
	viper.BindPFlag("no_log", rootCmd.PersistentFlags().Lookup("no-log"))
	viper.BindPFlag("region", rootCmd.PersistentFlags().Lookup("region"))
	viper.BindPFlag("iaas", rootCmd.PersistentFlags().Lookup("iaas"))

	// Set custom version template
	rootCmd.SetVersionTemplate(version.Get().String() + "\n")

	// Deprecated flags for backward compatibility
	rootCmd.PersistentFlags().StringVar(&blocName, "env-name", "", "deprecated: use --bloc-name instead")
	rootCmd.PersistentFlags().MarkDeprecated("env-name", "use --bloc-name instead")
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	// Set environment variable prefix
	viper.SetEnvPrefix("OCFP")
	viper.AutomaticEnv()

	// Set default config path
	if os.Getenv("OCFP_CONFIG_PATH") == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			os.Setenv("OCFP_CONFIG_PATH", filepath.Join(home, ".ocfp"))
		}
	}

	// Determine config file to use
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else if blocName != "" {
		// Use config/<bloc-name>.yml
		viper.SetConfigFile(fmt.Sprintf("config/%s.yml", blocName))
	} else {
		// Search for config in standard locations
		viper.SetConfigName("bootstrap")
		viper.SetConfigType("yml")
		viper.AddConfigPath("config")
		viper.AddConfigPath(".")
		viper.AddConfigPath(os.Getenv("OCFP_CONFIG_PATH"))
	}

	// Read config file if it exists
	if err := viper.ReadInConfig(); err == nil {
		if debug || verbose {
			fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
		}
	}
}