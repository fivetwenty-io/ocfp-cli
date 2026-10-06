package commands

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// SSHKeyFileMode is the file permission mode for SSH private key files.
	SSHKeyFileMode = 0600

	// sshTargetNameBastion and sshTargetNameArtifacts are the bloc VMs that
	// `ocfp ssh` resolves by name.
	sshTargetNameBastion   = "bastion"
	sshTargetNameArtifacts = "artifacts"
)

// sshTargetKind says how `ocfp ssh` reaches a target.
type sshTargetKind int

const (
	// sshTargetBastion is the bloc bastion, reached directly.
	sshTargetBastion sshTargetKind = iota
	// sshTargetArtifacts is the bloc artifacts VM, reached through the bastion.
	sshTargetArtifacts
	// sshTargetAddress is an IP address or dotted hostname handed to ssh as is.
	sshTargetAddress
)

var (
	sshValidHostPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-.])*[a-zA-Z0-9]$`)
	sshValidUserPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-_])*[a-zA-Z0-9]$`)
	sshValidPathPattern = regexp.MustCompile(`^[a-zA-Z0-9/._-]+$`)
)

// NewSSHCmd creates the SSH command.
//
//nolint:funlen // cobra command setup with examples and flags is inherently verbose
func NewSSHCmd() *cobra.Command {
	var (
		user       string
		key        string
		sshOptions string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "ssh [target] [command...]",
		Short: "Connect to the bastion, the artifacts VM, or another host",
		Long: `SSH connects to a VM in the OCFP environment.

The target is one of:
- bastion (the default): the bloc bastion, at its bastion_ip override or its discovered address
- artifacts: the bloc artifacts VM, at the private IP recorded in state (or found by provider tags),
  reached by hopping through the bastion
- an IP address or a dotted hostname, which is passed to ssh unchanged

Any other bare word is rejected rather than handed to DNS.

The command automatically:
- Locates the target's address and, for artifacts, the bastion to hop through
- Finds the SSH key in standard locations
- Uses the correct private key to establish connection

The artifacts hop runs as an explicit ProxyCommand through the bastion with the same key and user,
so a rebuilt bastion's new host key never blocks the connection. Pass --no-proxy-jump to connect to
the artifacts VM directly when you are already on its network, for example on the bastion itself.

You can execute remote commands, use SSH port forwarding, and pass SSH-specific options.

SSH keys are searched in the following order:
1. ~/.local/share/ocfp/{bloc}/ssh/id_ed25519 (preferred, or $XDG_DATA_HOME/ocfp/{bloc}/ssh/id_ed25519 if set)
2. ~/.local/share/ocfp/{bloc}/ssh/id_rsa (fallback)
3. ~/.ocfp/{bloc}/ssh/id_ed25519 or id_rsa (legacy, used only if the above are absent)

Set OCFP_HOME to force the legacy ~/.ocfp layout for all three lookups.`,
		Example: `  # Connect to bastion host (interactive session)
  ocfp ssh --bloc production

  # Execute a single command on bastion (name the target explicitly)
  ocfp ssh --bloc production bastion 'hostname'

  # Execute multiple commands
  ocfp ssh --bloc production bastion 'ls /tmp; hostname; echo $OCFP_BLOC'

  # Connect to the artifacts VM through the bastion
  ocfp ssh --bloc production artifacts

  # Execute a command on the artifacts VM
  ocfp ssh --bloc production artifacts 'chronyc tracking'

  # Connect to the artifacts VM directly (from the bastion or the SDN)
  ocfp ssh --bloc production --no-proxy-jump artifacts

  # Connect to any other host by IP address or dotted hostname
  ocfp ssh --bloc production 10.0.1.25

  # Port forwarding (local)
  ocfp ssh --bloc production -L 8080:localhost:80

  # Dynamic port forwarding (SOCKS proxy)
  ocfp ssh --bloc production -D 1080

  # Remote port forwarding
  ocfp ssh --bloc production -R 9090:localhost:8080

  # Connect as specific user
  ocfp ssh --bloc production --user admin

  # Use specific SSH key
  ocfp ssh --bloc production --key /path/to/key.pem

  # Pass additional SSH options
  ocfp ssh --bloc production --ssh-options "-o StrictHostKeyChecking=no"`,
		Args: cobra.MinimumNArgs(0),
		RunE: runSSH,
	}

	cmd.SilenceUsage = true

	cmd.AddCommand(newSSHConfigCmd())

	// Command-specific flags
	cmd.Flags().StringVar(&user, "user", "ubuntu", "username for SSH login")
	cmd.Flags().StringVar(&key, "key", "", "path to SSH private key")
	cmd.Flags().StringVar(&sshOptions, "ssh-options", "", "additional SSH options")
	cmd.Flags().Bool("no-proxy-jump", false, "Connect directly to the artifacts VM (use when running on the bastion or otherwise on the SDN)")

	// Bind flags to viper
	bindFlagsOnRun(cmd, map[string]string{
		"ssh.user": "user",
		"ssh.key":  "key",
	})
	_ = viper.BindPFlag("ssh.options", cmd.Flags().Lookup("ssh-options"))

	return cmd
}

func runSSH(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	log := logger.WithOperation("ssh")

	sshConfig, err := getSSHConfig(args)
	if err != nil {
		return err
	}

	noProxyJump, _ := cmd.Flags().GetBool("no-proxy-jump")

	cfg, provider, err := setupSSHProvider(ctx, sshConfig)
	if err != nil {
		return err
	}

	route, err := resolveSSHRoute(ctx, sshConfig.Kind, sshConfig.Target, noProxyJump, sshRouteResolver{
		bastionIP: func(ctx context.Context) (string, error) {
			return findBastionIP(ctx, provider, cfg.Name)
		},
		artifactsIP: func(ctx context.Context) (string, error) {
			return lookupArtifactsIPForSSH(ctx, provider, cfg.Name)
		},
	})
	if err != nil {
		return err
	}

	keyPath, err := resolveSSHKeyForSSH(sshConfig, cfg)
	if err != nil {
		return err
	}

	err = verifySSHKey(keyPath)
	if err != nil {
		return fmt.Errorf("SSH key verification failed: %w", err)
	}

	sshCmd := buildSSHCommandForRoute(route, sshConfig.User, keyPath, sshConfig.Options, sshConfig.SSHArgs, sshConfig.Command)

	where := route.Host
	if route.Bastion != "" {
		where = fmt.Sprintf("%s via bastion %s", route.Host, route.Bastion)
	}

	if len(sshConfig.Command) > 0 {
		log.Infof("Executing command on %s at %s as %s: %s", sshConfig.Target, where, sshConfig.User, strings.Join(sshConfig.Command, " "))
	} else {
		log.Infof("Connecting to %s at %s as %s", sshConfig.Target, where, sshConfig.User)
	}

	log.Debugf("Using SSH key: %s", keyPath)

	return executeSSH(ctx, sshCmd)
}

type sshConfig struct {
	BlocName string
	User     string
	KeyPath  string
	Options  string
	Target   string
	Kind     sshTargetKind
	SSHArgs  []string // SSH-specific flags like -L, -R, -D
	Command  []string // Remote command to execute
}

// classifySSHArguments separates command-line arguments into target, SSH flags, and remote command.
// Arguments are parsed left-to-right:
// - SSH flags (starting with -) are collected into sshArgs
// - First non-flag argument becomes the target (defaults to "bastion" if not provided)
// - All remaining arguments after target become the remote command
//
//nolint:nonamedreturns // Named returns improve readability for this parsing function
func classifySSHArguments(args []string) (target string, sshArgs []string, command []string) {
	target = "bastion" // default target
	sshArgs = []string{}
	command = []string{}

	if len(args) == 0 {
		return target, sshArgs, command
	}

	argIndex := 0 //nolint:varnamelen // 'i' is standard for loop indices

	// Parse SSH flags and find target
	for argIndex < len(args) {
		arg := args[argIndex]

		// Check if this is an SSH flag
		if strings.HasPrefix(arg, "-") {
			// Collect SSH flag
			sshArgs = append(sshArgs, arg)

			// Some SSH flags require a value (like -L, -R, -D, -p, -o)
			// Check if next argument is not a flag and collect it as the flag's value
			if argIndex+1 < len(args) {
				nextArg := args[argIndex+1]
				// For flags that take arguments, collect the next arg if it doesn't start with -
				if (arg == "-L" || arg == "-R" || arg == "-D" || arg == "-p" || arg == "-o") &&
					!strings.HasPrefix(nextArg, "-") {
					argIndex++
					sshArgs = append(sshArgs, args[argIndex])
				}
			}

			argIndex++
		} else {
			// First non-flag argument is the target
			target = arg
			argIndex++

			break
		}
	}

	// All remaining arguments are the remote command
	if argIndex < len(args) {
		command = args[argIndex:]
	}

	return target, sshArgs, command
}

func getSSHConfig(args []string) (*sshConfig, error) {
	blocName := viper.GetString("bloc")
	if blocName == "" {
		return nil, ErrBlocIsRequired
	}

	// Classify arguments into target, SSH flags, and remote command
	target, sshArgs, command := classifySSHArguments(args)

	kind, err := classifySSHTarget(target)
	if err != nil {
		return nil, err
	}

	return &sshConfig{
		BlocName: blocName,
		User:     viper.GetString("ssh.user"),
		KeyPath:  viper.GetString("ssh.key"),
		Options:  viper.GetString("ssh.options"),
		Target:   target,
		Kind:     kind,
		SSHArgs:  sshArgs,
		Command:  command,
	}, nil
}

// classifySSHTarget decides how a target is reached. The named targets are
// resolved from the bloc; an IP address or anything containing a dot is taken
// as an address and handed to ssh unchanged. Any other bare word is rejected
// here, because ssh would only pass it to DNS and fail with "Could not resolve
// hostname", which hides the real mistake.
func classifySSHTarget(target string) (sshTargetKind, error) {
	switch target {
	case sshTargetNameBastion:
		return sshTargetBastion, nil
	case sshTargetNameArtifacts:
		return sshTargetArtifacts, nil
	}

	if net.ParseIP(target) != nil || strings.Contains(target, ".") {
		return sshTargetAddress, nil
	}

	return sshTargetBastion, fmt.Errorf(
		"%w %q: the named targets are %s, %s; pass an IP address or a dotted hostname for any other host, "+
			"and name the target before a remote command (ocfp ssh --bloc <bloc> bastion '<command>')",
		ErrUnknownSSHTarget, target, sshTargetNameBastion, sshTargetNameArtifacts)
}

//nolint:ireturn // Returns interface by design for provider abstraction
func setupSSHProvider(ctx context.Context, sshCfg *sshConfig) (*config.Config, cpi.Provider, error) {
	cfg, err := config.LoadWithParams(viper.GetString("config.file"), sshCfg.BlocName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	if cfg.Provider == "" && cfg.IaaS == "" {
		return nil, nil, ErrProviderMustBeSpecifiedInBlocConfig(sshCfg.BlocName)
	}

	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get provider %s: %w", cfg.Provider, err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize provider %s: %w", cfg.Provider, err)
	}

	return cfg, provider, nil
}

// sshRoute is where the final ssh hop connects. Host is the address it
// dials, and Bastion, when set, is the bastion address that hop is proxied
// through.
type sshRoute struct {
	Host    string
	Bastion string
}

// sshRouteResolver supplies the bloc lookups resolveSSHRoute needs, so the
// routing decision can be exercised without a provider or state.
type sshRouteResolver struct {
	bastionIP   func(ctx context.Context) (string, error)
	artifactsIP func(ctx context.Context) (string, error)
}

// resolveSSHRoute turns a classified target into a route. The bastion is
// dialled directly at the address findBastionIP resolves (the bastion_ip
// override first). The artifacts VM sits on the SDN, so its private IP is
// reached through that same bastion unless noProxyJump says the operator is
// already on the SDN. Addresses pass through without any bloc lookup.
func resolveSSHRoute(ctx context.Context, kind sshTargetKind, target string, noProxyJump bool, resolver sshRouteResolver) (sshRoute, error) {
	switch kind {
	case sshTargetBastion:
		ip, err := resolver.bastionIP(ctx)
		if err != nil {
			return sshRoute{}, err
		}

		return sshRoute{Host: ip, Bastion: ""}, nil
	case sshTargetArtifacts:
		artifactsIP, err := resolver.artifactsIP(ctx)
		if err != nil {
			return sshRoute{}, err
		}

		if noProxyJump {
			return sshRoute{Host: artifactsIP, Bastion: ""}, nil
		}

		bastionIP, err := resolver.bastionIP(ctx)
		if err != nil {
			return sshRoute{}, fmt.Errorf("resolve bastion jump host: %w", err)
		}

		return sshRoute{Host: artifactsIP, Bastion: bastionIP}, nil
	case sshTargetAddress:
		return sshRoute{Host: target, Bastion: ""}, nil
	}

	return sshRoute{Host: target, Bastion: ""}, nil
}

// lookupArtifactsIPForSSH resolves the artifacts VM's private IP with
// artifacts.Lookup, which reads state first and falls back to the provider's
// role tags. A state load failure is not fatal, because the tag query can
// still find the VM.
func lookupArtifactsIPForSSH(ctx context.Context, provider cpi.Provider, blocName string) (string, error) {
	sm, err := createStateManager(blocName)
	if err != nil {
		return "", fmt.Errorf("creating state manager: %w", err)
	}

	_, err = sm.Load(blocName)
	if err != nil {
		logger.WithOperation("ssh").Debugf("Loading state for %s failed, falling back to provider tags: %v", blocName, err)
	}

	lr, err := artifacts.Lookup(ctx, sm, provider, blocName)
	if err != nil {
		return "", fmt.Errorf("looking up artifacts VM: %w", err)
	}

	if lr == nil {
		return "", fmt.Errorf("%w: %s", ErrArtifactsNotFound, blocName)
	}

	ip := strings.TrimSpace(lr.PrivateIP)
	if ip == "" {
		return "", fmt.Errorf("%w: %s; re-run bootstrap --artifacts", ErrArtifactsNoPrivateIP, lr.Name)
	}

	return ip, nil
}

func resolveSSHKeyForSSH(sshCfg *sshConfig, cfg *config.Config) (string, error) {
	if sshCfg.KeyPath != "" {
		return sshCfg.KeyPath, nil
	}

	keyPath, err := findSSHKey(sshCfg.BlocName, cfg)
	if err != nil {
		return "", fmt.Errorf("failed to find SSH key: %w", err)
	}

	return keyPath, nil
}

// getBastionIP retrieves the bastion host's public IP address.
func getBastionIP(ctx context.Context, provider cpi.Provider, blocName string) (string, error) {
	// Deprecated: kept for compatibility in tests; delegate to shared helper
	return findBastionIP(ctx, provider, blocName)
}

// findSSHKey locates the SSH private key for the bastion.
//
//nolint:unparam // cfg reserved for future use in provider-specific key resolution
func findSSHKey(blocName string, _cfg *config.Config) (string, error) {
	log := logger.WithOperation("findSSHKey")

	// Try Ed25519 key first (preferred)
	keyPath := filepath.Join(config.OcfpSSHKeyDir(blocName), "id_ed25519")

	info, err := os.Stat(keyPath) // #nosec -- path components are from trusted config
	if err == nil && info.Size() > 0 {
		log.Debugf("Found SSH key at: %s", keyPath)

		return keyPath, nil
	}

	// Fall back to RSA key
	rsaKeyPath := filepath.Join(config.OcfpSSHKeyDir(blocName), "id_rsa")

	rsaInfo, rsaErr := os.Stat(rsaKeyPath) // #nosec -- path components are from trusted config
	if rsaErr == nil && rsaInfo.Size() > 0 {
		log.Debugf("Found SSH key at: %s", rsaKeyPath)

		return rsaKeyPath, nil
	}

	// Distinguish between missing keys and empty keys for clearer diagnostics
	if err != nil {
		return "", fmt.Errorf("SSH key not found at %s or %s: %w", keyPath, rsaKeyPath, err)
	}

	return "", fmt.Errorf("SSH key at %s exists but is empty (0 bytes); no valid key found at %s either", keyPath, rsaKeyPath) //nolint:err113 // dynamic error with context
}

// verifySSHKey checks if the SSH key exists and has correct permissions.
func verifySSHKey(keyPath string) error {
	info, err := os.Stat(keyPath)
	if err != nil {
		return ErrSSHKeyNotFound(keyPath)
	}

	// Check permissions (should be 600 or 400)
	mode := info.Mode()
	if mode.Perm()&0077 != 0 {
		// Try to fix permissions
		err := os.Chmod(keyPath, SSHKeyFileMode)
		if err != nil {
			return ErrSSHKeyIncorrectPermissions(keyPath)
		}

		logger.WithOperation("verifySSHKey").Warnf("Fixed SSH key permissions for: %s", keyPath)
	}

	return nil
}

// validateSSHInputs validates the host, user, and keyPath for SSH connections.
func validateSSHInputs(host, user, keyPath string) error {
	err := security.ValidateInput(host, sshValidHostPattern)
	if err != nil {
		logger.WithOperation("buildSSHCommand").Errorf("invalid host: %v", err)

		return fmt.Errorf("invalid SSH host: %w", err)
	}

	err = security.ValidateInput(user, sshValidUserPattern)
	if err != nil {
		logger.WithOperation("buildSSHCommand").Errorf("invalid user: %v", err)

		return fmt.Errorf("invalid SSH user: %w", err)
	}

	if keyPath != "" {
		err = security.ValidateInput(keyPath, sshValidPathPattern)
		if err != nil {
			logger.WithOperation("buildSSHCommand").Errorf("invalid key path: %v", err)

			return fmt.Errorf("invalid SSH key path: %w", err)
		}
	}

	return nil
}

// addSSHStandardOptions adds standard SSH options to the command.
func addSSHStandardOptions(cmd []string, keyPath string) []string {
	cmd = append(cmd, "-o", "UserKnownHostsFile=/dev/null")
	cmd = append(cmd, "-o", "StrictHostKeyChecking=no")
	cmd = append(cmd, "-o", "LogLevel=ERROR")
	cmd = append(cmd, "-o", "IdentitiesOnly=yes")

	if keyPath != "" {
		cmd = append(cmd, "-i", keyPath)
	}

	// Forward SSH agent to remote host when available.
	// This is independent of which key is used for authentication —
	// IdentitiesOnly=yes already constrains auth to the specified key.
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		cmd = append(cmd, "-A")
	}

	return cmd
}

// filterSSHOptions filters extra SSH options to only allow safe ones.
func filterSSHOptions(extraOptions string) []string {
	if extraOptions == "" {
		return []string{}
	}

	allowedOptions := map[string]bool{
		"-p": true, "-v": true, "-q": true, "-4": true, "-6": true,
		"-o": true, "-L": true, "-R": true, "-D": true,
	}

	result := []string{}
	options := strings.Fields(extraOptions)

	for _, opt := range options {
		if strings.HasPrefix(opt, "-") && !allowedOptions[opt] {
			logger.WithOperation("buildSSHCommand").Warnf("skipping unsafe SSH option: %s", opt)

			continue
		}

		result = append(result, opt)
	}

	return result
}

// filterSSHArgs filters SSH arguments to only allow safe flags.
func filterSSHArgs(sshArgs []string) []string {
	allowedSSHFlags := map[string]bool{
		"-p": true, "-v": true, "-vv": true, "-vvv": true,
		"-q": true, "-4": true, "-6": true, "-A": true,
		"-o": true, "-L": true, "-R": true, "-D": true,
		"-N": true, "-f": true, "-T": true, "-t": true,
	}

	result := []string{}

	for _, arg := range sshArgs {
		if strings.HasPrefix(arg, "-") {
			flag := arg
			if strings.Contains(arg, "=") {
				flag = strings.Split(arg, "=")[0]
			}

			if !allowedSSHFlags[flag] {
				logger.WithOperation("buildSSHCommand").Warnf("skipping unsafe SSH argument: %s", arg)

				continue
			}
		}

		result = append(result, arg)
	}

	return result
}

// buildSSHCommand constructs the SSH command with all options.
func buildSSHCommand(host, user, keyPath, extraOptions string, sshArgs, command []string) []string {
	// Validate inputs
	err := validateSSHInputs(host, user, keyPath)
	if err != nil {
		return []string{"ssh", "--help"} // Return safe command
	}

	return assembleSSHCommand(host, user, keyPath, nil, extraOptions, sshArgs, command)
}

// buildSSHCommandForRoute constructs the SSH command for a resolved route.
// A direct route is exactly buildSSHCommand. A route through the bastion adds
// the explicit ProxyCommand hop that bootstrap uses to reach the artifacts VM,
// with the same key and user on both hops, and keeps every final-hop option
// (agent forwarding, --ssh-options, the filtered flags, and the bash -lc
// command wrapping) unchanged.
func buildSSHCommandForRoute(route sshRoute, user, keyPath, extraOptions string, sshArgs, command []string) []string {
	if route.Bastion == "" {
		return buildSSHCommand(route.Host, user, keyPath, extraOptions, sshArgs, command)
	}

	// The ProxyCommand string runs through a shell, so the bastion address,
	// user, and key path must all pass validation before they reach it.
	err := validateSSHInputs(route.Host, user, keyPath)
	if err == nil {
		err = validateSSHInputs(route.Bastion, user, keyPath)
	}

	if err != nil {
		return []string{"ssh", "--help"} // Return safe command
	}

	hop := []string{"-o", "ProxyCommand=" + bootstrap.BastionProxyCommand(keyPath, user, route.Bastion)}

	return assembleSSHCommand(route.Host, user, keyPath, hop, extraOptions, sshArgs, command)
}

// assembleSSHCommand builds the ssh argument vector from inputs the caller
// has already validated. hopOptions carries any bastion hop and sits after
// the standard options.
func assembleSSHCommand(host, user, keyPath string, hopOptions []string, extraOptions string, sshArgs, command []string) []string {
	cmd := []string{"ssh"}

	// Add standard options
	cmd = addSSHStandardOptions(cmd, keyPath)

	// Add the bastion hop, if any
	cmd = append(cmd, hopOptions...)

	// Add filtered extra options
	cmd = append(cmd, filterSSHOptions(extraOptions)...)

	// Add filtered SSH arguments
	cmd = append(cmd, filterSSHArgs(sshArgs)...)

	// Add user@host
	cmd = append(cmd, fmt.Sprintf("%s@%s", user, host))

	// Add remote command if specified
	if len(command) > 0 {
		cmd = append(cmd, wrapRemoteCommand(command)...)
	}

	return cmd
}

// wrapRemoteCommand wraps a remote command in a login shell so that
// /etc/profile.d/ocfp.sh (Linuxbrew PATH, OCFP env vars) applies to
// non-interactive command execution, matching interactive sessions.
func wrapRemoteCommand(command []string) []string {
	joined := strings.Join(command, " ")
	quoted := "'" + strings.ReplaceAll(joined, "'", `'\''`) + "'"

	return []string{"bash", "-lc", quoted}
}

// executeSSH executes the SSH command and attaches to stdin/stdout/stderr.
func executeSSH(ctx context.Context, sshCmd []string) error {
	log := logger.WithOperation("executeSSH")
	log.Debugf("Executing: %s", strings.Join(sshCmd, " "))

	// Validate that the command is ssh
	if len(sshCmd) == 0 || sshCmd[0] != "ssh" {
		return ErrInvalidSSHCommand
	}

	cmd := exec.CommandContext(ctx, sshCmd[0], sshCmd[1:]...) // #nosec G204 - command is validated above
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("ssh command failed: %w", err)
	}

	return nil
}
