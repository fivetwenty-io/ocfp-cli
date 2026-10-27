package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"golang.org/x/term"
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
- Finds the bloc's SSH key in standard locations
- Uses that key to establish the connection, or your own SSH setup when the bloc key is absent

The artifacts hop runs as an explicit ProxyCommand through the bastion with the same key and user,
so a rebuilt bastion's new host key never blocks the connection. Pass --no-proxy-jump to connect to
the artifacts VM directly when you are already on its network, for example on the bastion itself.

You can execute remote commands, use SSH port forwarding, and pass SSH-specific options.

SSH keys are searched in the following order:
1. ~/.local/share/ocfp/{bloc}/ssh/id_ed25519 (preferred, or $XDG_DATA_HOME/ocfp/{bloc}/ssh/id_ed25519 if set)
2. ~/.local/share/ocfp/{bloc}/ssh/id_rsa (fallback)
3. ~/.ocfp/{bloc}/ssh/id_ed25519 or id_rsa (legacy, used only if the above are absent)

Set OCFP_HOME to force the legacy ~/.ocfp layout for all three lookups.

--key names a private key to use instead, and wins over the bloc key. When neither --key nor
a bloc key is present, ssh runs without -i and without IdentitiesOnly, so your ssh-agent, your
default keys, and your ssh config decide how to log in (for example your own key on the
bastion, or Tailscale SSH). A one-line notice on stderr says when that happens. The hop through
the bastion to the artifacts VM may then prompt for a passphrase or password only in an
interactive run; when stdin is not a terminal, or the ssh options set BatchMode=yes, it runs in
batch mode and fails instead of waiting.`,
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

	keyPath, err := resolveOperatorSSHKey(sshConfig.KeyPath, sshConfig.BlocName, cfg, cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	err = verifyResolvedSSHKey(keyPath)
	if err != nil {
		return err
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

	logResolvedSSHKey(log, keyPath)

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

// resolveOperatorSSHKey picks the private key that ssh, scp, rsync, and
// ssh config hand to ssh. An explicit key (--key) wins. Otherwise the bloc
// key is used when it exists. When neither id_ed25519 nor id_rsa exists in the
// bloc's key directory, which is the normal case for anyone but the operator
// who bootstrapped it, the result is the empty path: the caller then runs ssh without -i and without
// IdentitiesOnly, so the user's ssh-agent, default keys, and ssh config decide
// how to log in. One line on notice says so. If either candidate exists but
// is unusable, such as an empty or unreadable file, or stat fails on it for
// any reason other than its absence, and no other candidate is sound, the
// result is an error, because falling back would hide a broken key.
func resolveOperatorSSHKey(explicit, blocName string, cfg *config.Config, notice io.Writer) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	keyPath, err := findSSHKey(blocName, cfg)
	if err == nil {
		return keyPath, nil
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("failed to find SSH key: %w", err)
	}

	_, _ = fmt.Fprintf(notice, "ocfp: bloc SSH key not found in %s; using your own SSH keys and config\n",
		config.OcfpSSHKeyDir(blocName))

	return "", nil
}

// verifyResolvedSSHKey checks a key resolveOperatorSSHKey returned. The empty
// path means the user's own ssh setup, so there is nothing to check.
func verifyResolvedSSHKey(keyPath string) error {
	if keyPath == "" {
		return nil
	}

	err := verifySSHKey(keyPath)
	if err != nil {
		return fmt.Errorf("SSH key verification failed: %w", err)
	}

	return nil
}

// logResolvedSSHKey records which key a command uses at debug level.
func logResolvedSSHKey(log logger.Logger, keyPath string) {
	if keyPath == "" {
		log.Debugf("Using the user's own SSH keys and config")

		return
	}

	log.Debugf("Using SSH key: %s", keyPath)
}

// getBastionIP retrieves the bastion host's public IP address.
func getBastionIP(ctx context.Context, provider cpi.Provider, blocName string) (string, error) {
	// Deprecated: kept for compatibility in tests; delegate to shared helper
	return findBastionIP(ctx, provider, blocName)
}

// findSSHKey locates the SSH private key for the bastion. It tries
// id_ed25519, then id_rsa, in the bloc's key directory and returns the
// first that is a non-empty regular file this process can open. The error
// wraps fs.ErrNotExist only when every candidate is absent. A candidate that
// is empty, unreadable, not a regular file, or that stat fails on for any
// other reason is reported instead, so a broken bloc key is never mistaken
// for a missing one.
//
//nolint:unparam // cfg reserved for future use in provider-specific key resolution
func findSSHKey(blocName string, _cfg *config.Config) (string, error) {
	log := logger.WithOperation("findSSHKey")
	dir := config.OcfpSSHKeyDir(blocName)

	var (
		paths    []string
		problems []error
	)

	for _, name := range []string{"id_ed25519", "id_rsa"} {
		keyPath := filepath.Join(dir, name)
		paths = append(paths, keyPath)

		err := checkSSHKeyCandidate(keyPath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			problems = append(problems, err)

			continue
		}

		log.Debugf("Found SSH key at: %s", keyPath)

		return keyPath, nil
	}

	if len(problems) == 0 {
		return "", fmt.Errorf("SSH key not found at %s: %w", strings.Join(paths, " or "), fs.ErrNotExist)
	}

	return "", errors.Join(problems...)
}

// openSSHKeyFile opens a candidate key to prove this process can read it.
// Tests replace it to fail after the candidate has been found.
var openSSHKeyFile = os.Open

// checkSSHKeyCandidate returns nil for a non-empty regular file this process
// can open, an error wrapping fs.ErrNotExist only when no directory entry
// exists at keyPath, and an error that never wraps it for anything else. A
// dangling symlink is an entry, so it is broken rather than absent.
func checkSSHKeyCandidate(keyPath string) error {
	_, err := os.Lstat(keyPath) // #nosec -- path components are from trusted config
	if errors.Is(err, fs.ErrNotExist) {
		return err
	}

	if err != nil {
		return fmt.Errorf("SSH key at %s cannot be inspected: %w", keyPath, err)
	}

	info, err := os.Stat(keyPath) // #nosec -- path components are from trusted config
	if err != nil {
		return unusableSSHKey("SSH key at %s cannot be inspected", keyPath, err)
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrSSHKeyUnusable, keyPath)
	}

	if info.Size() == 0 {
		return fmt.Errorf("%w: %s exists but is empty (0 bytes)", ErrSSHKeyUnusable, keyPath)
	}

	file, err := openSSHKeyFile(keyPath) // #nosec -- path components are from trusted config
	if err != nil {
		return unusableSSHKey("SSH key at %s cannot be read", keyPath, err)
	}

	_ = file.Close()

	return nil
}

// unusableSSHKey reports a failure on a candidate that was found. It marks
// the key unusable and keeps the cause's text, but drops fs.ErrNotExist from
// the chain so a vanished or dangling key is never read as an absent one.
func unusableSSHKey(format, keyPath string, cause error) error {
	if errors.Is(cause, fs.ErrNotExist) {
		return fmt.Errorf("%w: "+format+": %v", ErrSSHKeyUnusable, keyPath, cause)
	}

	return fmt.Errorf("%w: "+format+": %w", ErrSSHKeyUnusable, keyPath, cause)
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

// sshKeepaliveOptions keeps an idle session open. A keepalive every 30
// seconds stops a firewall or NAT idle timeout from dropping a quiet
// shell, and six missed replies (three minutes) end a dead connection.
var sshKeepaliveOptions = []string{
	"-o", "ServerAliveInterval=30",
	"-o", "ServerAliveCountMax=6",
}

// addSSHStandardOptions adds standard SSH options to the command. The
// host-key options are the same with or without a key. With a key, auth is
// pinned to it by -i and IdentitiesOnly; without one, both are left out so
// the user's ssh-agent, default keys, and ssh config apply.
func addSSHStandardOptions(cmd []string, keyPath string) []string {
	cmd = append(cmd, "-o", "UserKnownHostsFile=/dev/null")
	cmd = append(cmd, "-o", "StrictHostKeyChecking=no")
	cmd = append(cmd, "-o", "LogLevel=ERROR")

	if keyPath != "" {
		cmd = append(cmd, "-o", "IdentitiesOnly=yes")
		cmd = append(cmd, "-i", keyPath)
	}

	// Forward SSH agent to remote host when available.
	// This is independent of which key is used for authentication —
	// with a key, IdentitiesOnly=yes already constrains auth to it.
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

// buildSSHCommandForRoute is buildSSHCommandForRouteWith for this process,
// which is interactive when stdin is a terminal.
func buildSSHCommandForRoute(route sshRoute, user, keyPath, extraOptions string, sshArgs, command []string) []string {
	return buildSSHCommandForRouteWith(route, user, keyPath, extraOptions, sshArgs, command, stdinIsTerminal())
}

// stdinIsTerminal reports whether this process's stdin is a terminal.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) // #nosec G115 -- a file descriptor fits in int
}

// buildSSHCommandForRouteWith constructs the SSH command for a resolved route.
// A direct route is exactly buildSSHCommand. A route through the bastion adds
// the explicit ProxyCommand hop that bootstrap uses to reach the artifacts VM,
// with the same key and user on both hops, and keeps every final-hop option
// (agent forwarding, --ssh-options, the filtered flags, and the bash -lc
// command wrapping) unchanged.
//
// Without a key the jump hop is a separate ssh that could prompt for a
// passphrase or password. It gets BatchMode=yes when the outer options turn
// batch mode on or when stdinTerminal is false, so it prompts only in an
// interactive run.
func buildSSHCommandForRouteWith(route sshRoute, user, keyPath, extraOptions string, sshArgs, command []string, stdinTerminal bool) []string {
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

	batchMode := !stdinTerminal || sshOptionsSetBatchMode(filterSSHOptions(extraOptions), filterSSHArgs(sshArgs))
	hop := []string{"-o", "ProxyCommand=" + bootstrap.BastionProxyCommand(keyPath, user, route.Bastion, batchMode)}

	return assembleSSHCommand(route.Host, user, keyPath, hop, extraOptions, sshArgs, command)
}

// sshOptionsSetBatchMode reports whether the first BatchMode in the given
// option lists, read in the order ssh sees them, turns batch mode on. ssh
// keeps the first value it sees for an option, and so does this.
func sshOptionsSetBatchMode(lists ...[]string) bool {
	for _, args := range lists {
		for i := 0; i+1 < len(args); i++ {
			if args[i] != "-o" {
				continue
			}

			name, value, found := strings.Cut(args[i+1], "=")
			if !found {
				name, value, _ = strings.Cut(args[i+1], " ")
			}

			if strings.EqualFold(strings.TrimSpace(name), "BatchMode") {
				return strings.EqualFold(strings.TrimSpace(value), "yes")
			}
		}
	}

	return false
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

	// Add keepalives after the caller's options, because ssh keeps the
	// first value it sees and an operator's own setting should win
	cmd = append(cmd, sshKeepaliveOptions...)

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
