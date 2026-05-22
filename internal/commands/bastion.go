package commands

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/bastion"
	bastionproviders "github.com/ocfp/ocfp-cli-go/internal/bastion/providers"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewBastionCmd creates the bastion command.
func NewBastionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                    "bastion <action>",
		Short:                  "Bastion host management",
		Long:                   `Manage bastion host operations and configuration.`,
		Aliases:                []string{},
		SuggestFor:             []string{},
		GroupID:                "",
		Example:                "",
		ValidArgs:              []string{},
		ValidArgsFunction:      nil,
		Args:                   cobra.MinimumNArgs(1),
		ArgAliases:             []string{},
		BashCompletionFunction: "",
		Deprecated:             "",
		Annotations:            map[string]string{},
		Version:                "",
		PersistentPreRun:       nil,
		PersistentPreRunE:      nil,
		PreRun:                 nil,
		PreRunE:                nil,
		Run:                    nil,
		RunE:                   runBastionCmd,
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
		SilenceErrors:              false,
		SilenceUsage:               false,
		DisableFlagParsing:         false,
		DisableAutoGenTag:          false,
		DisableFlagsInUseLine:      false,
		DisableSuggestions:         false,
		SuggestionsMinimumDistance: 0,
	}

	// Flags align with SSH/SCP commands for consistency
	cmd.Flags().String("user", "ubuntu", "SSH username for bastion connection")
	cmd.Flags().String("key", "", "Path to SSH private key")
	cmd.Flags().String("bloc", "", "Bloc name for configuration")

	// Bind to viper for reuse
	_ = viper.BindPFlag("ssh.user", cmd.Flags().Lookup("user"))
	_ = viper.BindPFlag("ssh.key", cmd.Flags().Lookup("key"))
	_ = viper.BindPFlag("bloc", cmd.Flags().Lookup("bloc"))

	return cmd
}

func runBastionCmd(cmd *cobra.Command, args []string) error {
	log := logger.WithOperation("bastion")
	action := args[0]

	switch action {
	case "init":
		return bastionInit(cmd, log)
	case "provision":
		return bastionProvision(cmd, log)
	default:
		return ErrUnknownBastionAction(action)
	}
}

func bastionInit(cmd *cobra.Command, log logger.Logger) error {
	ctx := cmd.Context()

	// Get bloc name from viper (bound to --bloc flag)
	blocName := viper.GetString("bloc")
	if blocName == "" {
		return ErrBlocIsRequired
	}

	// Load configuration
	cfg, err := config.LoadWithParams(viper.GetString("config"), blocName)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create bastion manager
	bastionMgr := bastion.NewManager(cfg, &bastion.ProvisioningOptions{
		DryRun:      false,
		Resume:      false,
		Parallel:    false,
		ProgressOut: os.Stdout,
	})

	// Initialize bastion
	err = bastionMgr.Initialize(ctx)
	if err != nil {
		return fmt.Errorf("bastion initialization failed: %w", err)
	}

	log.Info("Bastion initialization completed successfully")

	return nil
}

func bastionProvision(cmd *cobra.Command, log logger.Logger) error {
	bastionContext, err := GetBastionContext(cmd, log)
	if err != nil {
		return fmt.Errorf("failed to get bastion context: %w", err)
	}

	scriptPath, err := FindProvisionScript("bastion")
	if err != nil {
		return fmt.Errorf("cannot find bastion provision script: %w", err)
	}

	// Load provider config env vars so bloc-level settings (bridge, storage
	// pool, ISO storage) flow into the remote execution environment even when
	// the operator has not manually exported them.  Operator-exported values
	// always take precedence (BuildEnvironmentVariables checks os.Getenv and
	// skips vars that are already set there).
	configEnv := loadProviderEnv(cmd, log)

	// Copy and execute script
	err = copyAndExecuteScript(
		bastionContext,
		scriptPath,
		"/tmp/provision-bastion.pl",
		"bastion provision",
		"~/provision.log",
		configEnv,
		log,
	)
	if err != nil {
		return err
	}

	return nil
}

// loadProviderEnv loads the PrepareEnvironment map from the bloc's bastion
// initializer.  Returns an empty map on any error so callers fall back to
// the operator-environment-only path gracefully.
func loadProviderEnv(_ *cobra.Command, log logger.Logger) map[string]string {
	blocName := viper.GetString("bloc")
	if blocName == "" {
		return map[string]string{}
	}

	cfg, err := config.LoadWithParams(viper.GetString("config"), blocName)
	if err != nil {
		log.Warnf("Cannot load bloc config for env preparation: %v", err)

		return map[string]string{}
	}

	provider := cfg.Provider
	if provider == "" {
		provider = cfg.IaaS
	}

	init, err := newBastionInitializer(provider, cfg)
	if err != nil {
		log.Warnf("Cannot create bastion initializer for env preparation: %v", err)

		return map[string]string{}
	}

	return init.PrepareEnvironment()
}

// newBastionInitializer returns the provider-specific bastion initializer for the given
// provider name and bloc config.  Mirrors the switch in bastion.Manager.getProviderInitializer.
//
//nolint:ireturn // returning interface type is intentional for provider pluggability
func newBastionInitializer(provider string, cfg *config.Config) (bastionproviders.BastionInitializer, error) {
	switch strings.ToLower(provider) {
	case "pve":
		return bastionproviders.NewPVEBastionInit(cfg), nil
	case "aws":
		return bastionproviders.NewAWSBastionInit(cfg), nil
	case "stackit":
		return bastionproviders.NewStackitBastionInit(cfg), nil
	case "azure":
		return bastionproviders.NewAzureBastionInit(cfg), nil
	case "gcp":
		return bastionproviders.NewGCPBastionInit(cfg), nil
	case "openstack":
		return bastionproviders.NewOpenStackBastionInit(cfg), nil
	case "vmware", "vsphere":
		return bastionproviders.NewVMwareBastionInit(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported provider for bastion init: %q", provider)
	}
}

// BastionContext holds the connection details for a bastion host.
type BastionContext struct {
	IP           string
	User         string
	SSHOptions   string
	SSHKeyOption string
}

// GetBastionContext discovers or constructs the bastion host connection details for the current bloc.
func GetBastionContext(_cmd *cobra.Command, log logger.Logger) (*BastionContext, error) {
	user := viper.GetString("ssh.user")
	key := viper.GetString("ssh.key")
	blocName := viper.GetString("bloc")

	// Attempt discovery when bloc/provider are available
	if blocName != "" {
		ctx, err := tryDiscoverBastionContext(blocName, key, user)
		if err == nil && ctx != nil {
			return ctx, nil
		}
	}

	// Fallback placeholder context to match existing tests and allow dry-runs
	if log != nil {
		log.Warnf("Bastion context discovery not fully available - using placeholder values")
	}

	return buildPlaceholderContext(user, key), nil
}

func tryDiscoverBastionContext(blocName, key, user string) (*BastionContext, error) {
	cfg, err := config.LoadWithParams(viper.GetString("config"), blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Provider == "" && cfg.IaaS == "" {
		return nil, ErrNoProviderConfigured
	}

	ctx := context.Background()

	provider, err := initializeBastionProvider(ctx, cfg)
	if err != nil {
		return nil, err
	}

	bastionIP, err := getBastionIP(ctx, provider, blocName)
	if err != nil || bastionIP == "" {
		return nil, err
	}

	// Find key if not specified
	if key == "" {
		k, kerr := findSSHKey(blocName, cfg)
		if kerr == nil {
			key = k
		}
	}

	return &BastionContext{
		IP:           bastionIP,
		User:         user,
		SSHOptions:   "-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR",
		SSHKeyOption: strings.TrimSpace(key),
	}, nil
}

//nolint:ireturn
func initializeBastionProvider(ctx context.Context, cfg *config.Config) (cpi.Provider, error) {
	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to get provider: %w", err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize provider: %w", err)
	}

	return provider, nil
}

func buildPlaceholderContext(user, key string) *BastionContext {
	return &BastionContext{
		IP:           "placeholder-ip",
		User:         user,
		SSHOptions:   "-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR",
		SSHKeyOption: strings.TrimSpace(key),
	}
}

// FindProvisionScript locates a provisioning script by name in standard search paths.
func FindProvisionScript(scriptName string) (string, error) {
	// Get the directory where the binary is located
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
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
		_, err := os.Stat(path) //nolint:gosec // path components are from trusted config
		if err == nil {
			return path, nil
		}
	}

	return "", ErrScriptNotFound(scriptName)
}

func copyAndExecuteScript(ctx *BastionContext, scriptPath, remoteScript, operationName, logPath string, configEnv map[string]string, log logger.Logger) error {
	// Copy the script to bastion
	err := copyScriptToBastion(ctx, scriptPath, remoteScript, log)
	if err != nil {
		return err
	}

	// Merge config-derived env (from PrepareEnvironment) with the operator
	// environment.  Operator-exported values take precedence: configEnv
	// entries are only used when the named var is absent from os.Getenv.
	envString := BuildEnvironmentVariables(configEnv, log)

	// Execute the script on bastion
	err = executeRemoteScript(ctx, remoteScript, envString, operationName, logPath, log)
	if err != nil {
		return err
	}

	// Cleanup remote script (best-effort)
	cleanupRemoteScript(ctx, remoteScript, log)

	return nil
}

func copyScriptToBastion(ctx *BastionContext, scriptPath, remoteScript string, log logger.Logger) error {
	// Build destination in scp format
	dest := fmt.Sprintf("%s@%s:%s", ctx.User, ctx.IP, remoteScript)

	// Reuse SCP builder/executor
	scpCmd := buildSCPCommand(scriptPath, dest, ctx.SSHKeyOption, false, "")
	log.Debugf("Copying script via: %s", strings.Join(scpCmd, " "))

	execCtx := context.Background()

	return executeSCP(execCtx, scpCmd)
}

func executeRemoteScript(ctx *BastionContext, remoteScript, envString, operationName, logPath string, log logger.Logger) error {
	// B-3 fix: avoid nested single-quote quoting by base64-encoding the env
	// block.  The remote side decodes and evals it before invoking the script.
	// This sidesteps the shell-quoting depth problem entirely regardless of
	// what characters appear in env values (apostrophes, spaces, newlines, etc.).
	//
	// Security note: the base64 blob is still visible in `ps aux` on the local
	// machine while the SSH process runs, and is trivially decoded.  For
	// stronger isolation use SendEnv with a matching AcceptEnv on the sshd
	// side (requires sshd config change on the bastion).  The base64 path is
	// chosen here because it requires no sshd reconfiguration.
	envB64 := base64.StdEncoding.EncodeToString([]byte(envString))

	// The remote command: decode the base64 blob into a file, source it, then
	// run the Perl script.  Using a temp file for the env avoids `eval` on
	// arbitrary shell text and keeps PVE_TOKEN_SECRET off the ps command line
	// of the Perl process itself.
	remote := fmt.Sprintf(
		"bash -lc 'set -euo pipefail; _e=$(mktemp); echo %s | base64 -d > \"$_e\"; . \"$_e\"; rm -f \"$_e\"; perl %s | tee %s'",
		envB64, remoteScript, logPath,
	)

	sshCmd := buildSSHCommand(ctx.IP, ctx.User, ctx.SSHKeyOption, "", []string{}, []string{})
	sshCmd = append(sshCmd, remote)

	log.Infof("Executing %s on bastion", operationName)
	// Log without the base64 payload to prevent token secrets appearing in
	// debug output.  Only log the operation name and remote script path.
	log.Debugf("SSH exec: ssh %s@%s perl %s | tee %s (env vars encoded)", ctx.User, ctx.IP, remoteScript, logPath)

	execCtx := context.Background()

	return executeSSH(execCtx, sshCmd)
}

func cleanupRemoteScript(ctx *BastionContext, remoteScript string, _ logger.Logger) {
	sshCmd := buildSSHCommand(ctx.IP, ctx.User, ctx.SSHKeyOption, "", []string{}, []string{})
	sshCmd = append(sshCmd, "rm -f "+remoteScript)
	_ = executeSSH(context.Background(), sshCmd) // best effort
}

// BuildEnvironmentVariables constructs shell assignment statements for remote bastion execution.
// configEnv holds values from the bloc's PrepareEnvironment call.  Operator-exported values
// (os.Getenv) take precedence: a configEnv entry is only used when the named var is not already
// set in the process environment.
//
// The resulting string is NOT interpolated directly into a shell command line;
// executeRemoteScript base64-encodes it and decodes+sources it on the remote side, which
// eliminates all shell-quoting depth issues (B-3 fix).
func BuildEnvironmentVariables(configEnv map[string]string, _log logger.Logger) string {
	var envVars []string

	// Ordered list of every var name forwarded to the bastion.
	// Operator env takes precedence; configEnv fills in what the operator left unset.
	varNames := []string{
		// OCFP common vars - always forwarded regardless of provider.
		"OCFP_BLOC",
		"OCFP_PROVIDER",

		// STACKIT provider vars.
		"STACKIT_PROJECT_ID",
		"STACKIT_ORG_ID",
		"STACKIT_REGION",

		// PVE provider vars.
		// PVE_HOST, PVE_NODE, PVE_TOKEN_ID, PVE_TOKEN_SECRET are read by the
		// provision/bastion Perl script via $ENV{...} for environment validation
		// logging and any PVE API calls it makes.
		// PVE_BRIDGE, PVE_STORAGE_POOL, and PVE_ISO_STORAGE allow the script
		// to receive operator-specific storage/network topology without
		// hard-coding defaults.
		"PVE_HOST",
		"PVE_NODE",
		"PVE_TOKEN_ID",
		"PVE_TOKEN_SECRET",
		"PVE_BRIDGE",
		"PVE_STORAGE_POOL",
		"PVE_ISO_STORAGE",
	}

	for _, name := range varNames {
		appendEnvVar(&envVars, name, configEnv)
	}

	return strings.Join(envVars, "\n")
}

// appendEnvVar resolves a value for name: operator env (os.Getenv) wins;
// configEnv supplies the fallback from the bloc PrepareEnvironment map.
// When the resolved value is non-empty, a shell assignment line
// (NAME='value') is appended to *vars.  Single quotes inside the value are
// escaped with the standard shell idiom so the sourced file is safe even
// when values contain apostrophes.
func appendEnvVar(vars *[]string, name string, configEnv map[string]string) {
	// Operator-exported value takes precedence.
	val := os.Getenv(name)
	if val == "" {
		// Fall back to the bloc config-derived value when set.
		val = configEnv[name]
	}

	if val == "" {
		return
	}

	// Escape single quotes inside the value: end quote, escaped quote, reopen quote.
	escaped := strings.ReplaceAll(val, "'", `'\''`)
	*vars = append(*vars, fmt.Sprintf("%s='%s'", name, escaped))
}
