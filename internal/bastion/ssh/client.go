package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	// sshRetryBaseInterval is the initial delay between SSH connection retries.
	sshRetryBaseInterval = 3 * time.Second
	// sshRetryMaxInterval is the maximum delay between SSH connection retries.
	sshRetryMaxInterval = 30 * time.Second
	// sshRetryBackoffMultiplier is the multiplier for exponential backoff.
	sshRetryBackoffMultiplier = 1.5
	// sshpassArgCount is the number of fixed sshpass arguments (before appending ssh args).
	// With -e mode the only fixed arg is the subcommand name (e.g. "ssh"); password
	// is injected via SSHPASS env, never in argv.
	sshpassArgCount = 1
)

// validateCommand validates that only safe commands are executed.
func validateCommand(cmdSlice []string) error {
	if len(cmdSlice) == 0 {
		return ErrEmptyCommand
	}

	allowedCommands := map[string]bool{
		"sshpass": true,
		"ssh":     true,
	}

	if !allowedCommands[cmdSlice[0]] {
		return ErrCommandNotAllowed(cmdSlice[0])
	}

	// Check for shell metacharacters in arguments
	for _, arg := range cmdSlice[1:] {
		if strings.Contains(arg, ";") || strings.Contains(arg, "|") || strings.Contains(arg, "&") || strings.Contains(arg, "`") {
			return ErrShellMetacharacters(arg)
		}
	}

	return nil
}

// Client implements the SSHClient interface using Go's native SSH library.
type Client struct {
	config                 *ConnectionDetails
	options                *ProvisioningOptions
	client                 *ssh.Client
	log                    logger.Logger
	connected              bool
	keyManager             *KeyManager
	agentConn              net.Conn // Connection to SSH agent - must stay alive for forwarding
	agentForwardingEnabled bool     // Track if agent forwarding channel handler is set up
}

// NewClient creates a new SSH client.
func NewClient(config *ConnectionDetails, options *ProvisioningOptions) *Client {
	return &Client{
		config:     config,
		options:    options,
		client:     nil,
		log:        logger.Get(),
		connected:  false,
		keyManager: NewKeyManager(),
	}
}

// Connect establishes an SSH connection to the bastion host with retry logic.
//
//nolint:funlen // retry loop with multiple connection strategies requires this length
func (c *Client) Connect(ctx context.Context) error {
	if c.connected {
		return nil
	}

	c.log.Infow("Connecting to bastion host",
		"host", c.config.Host,
		"user", c.config.User)

	// Retry configuration
	maxRetries := 5
	retryInterval := sshRetryBaseInterval
	maxRetryInterval := sshRetryMaxInterval

	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Prepare SSH client configuration
		sshConfig, err := c.prepareSSHConfig()
		if err != nil {
			return fmt.Errorf("failed to prepare SSH config: %w", err)
		}

		// Try multiple connection strategies
		strategies := []func(context.Context, *ssh.ClientConfig) (*ssh.Client, error){
			c.connectWithNativeClient,
			c.connectWithExternalSSH,
		}

		for index, strategy := range strategies {
			c.log.Debugw("Attempting connection strategy",
				"strategy", index+1,
				"attempt", attempt,
				"max_attempts", maxRetries)

			client, err := strategy(ctx, sshConfig)
			if err != nil {
				lastErr = err
				c.log.Debugw("Connection strategy failed",
					"strategy", index+1,
					"attempt", attempt,
					"error", err.Error())

				continue
			}

			c.client = client
			c.connected = true

			// Set up SSH agent forwarding if available
			c.setupAgentForwarding(ctx)

			c.log.Infof("Successfully connected to bastion host on attempt %d", attempt)

			return nil
		}

		// All strategies failed for this attempt
		if attempt < maxRetries {
			c.log.Warnw("Connection attempt failed, retrying",
				"attempt", attempt,
				"max_attempts", maxRetries,
				"retry_delay", retryInterval,
				"error", lastErr.Error())

			// Wait before next attempt, respecting context cancellation
			select {
			case <-ctx.Done():
				return fmt.Errorf("SSH connection cancelled: %w", ctx.Err())
			case <-time.After(retryInterval):
				// Exponential backoff
				retryInterval = time.Duration(float64(retryInterval) * sshRetryBackoffMultiplier)
				if retryInterval > maxRetryInterval {
					retryInterval = maxRetryInterval
				}
			}
		}
	}

	return fmt.Errorf("failed to connect after %d attempts, last error: %w", maxRetries, lastErr)
}

// ExecuteCommand executes a command on the remote host.
func (c *Client) ExecuteCommand(ctx context.Context, cmd string) (*CommandResult, error) {
	if !c.connected {
		return nil, ErrSSHClientNotConnected
	}

	startTime := time.Now()

	c.log.Debugw("Executing remote command", "command", logger.RedactSecrets(cmd))

	session, err := c.createSession()
	if err != nil {
		return nil, err
	}
	defer c.closeSession(session)

	stdout, stderr := c.setupOutputCapture(session)

	return c.executeWithContext(ctx, session, cmd, startTime, stdout, stderr)
}

// TransferFile transfers a file between local and remote systems.
func (c *Client) TransferFile(ctx context.Context, local, remote string, opts TransferOptions) error {
	if !c.connected {
		return ErrSSHClientNotConnected
	}

	c.log.Infow("Transferring file", "local", local, "remote", remote)

	// Create transfer manager
	transferMgr := NewTransferManager(c, opts)
	// Assume uploading from local filesystem to remote bastion unless caller
	// already specified direction via prefix. Prefix remote with bastion: for upload.
	// If caller already provided a bastion: prefix, pass-through.
	bastionRemote := remote
	if !strings.HasPrefix(remote, "bastion:") && !strings.HasPrefix(local, "bastion:") {
		bastionRemote = "bastion:" + remote
	}

	return transferMgr.Transfer(ctx, local, bastionRemote)
}

// CreateTunnel creates an SSH tunnel for port forwarding.
func (c *Client) CreateTunnel(ctx context.Context, localPort, remotePort int) error {
	if !c.connected {
		return ErrSSHClientNotConnected
	}

	c.log.Infow("Creating SSH tunnel",
		"local_port", localPort,
		"remote_port", remotePort)

	// Listen on local port with context
	listenConfig := &net.ListenConfig{
		Control:   nil,
		KeepAlive: 0,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   false,
			Idle:     0,
			Interval: 0,
			Count:    0,
		},
	}

	listener, err := listenConfig.Listen(ctx, "tcp", fmt.Sprintf("localhost:%d", localPort))
	if err != nil {
		return fmt.Errorf("failed to listen on local port %d: %w", localPort, err)
	}

	// Close the listener when the context is cancelled so the blocking
	// Accept below unblocks and the accept goroutine can exit promptly.
	go func() {
		<-ctx.Done()

		err := listener.Close()
		if err != nil {
			c.log.Debugw("Closing tunnel listener on context cancel", "error", err)
		}
	}()

	// Handle tunnel connections
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				// A failed Accept means the listener is closed (context
				// cancelled) or otherwise unusable. Exit instead of
				// busy-spinning on a permanent error.
				if ctx.Err() == nil {
					c.log.Errorw("Stopping tunnel: accept failed", "error", err)
				}

				return
			}

			go c.handleTunnelConnection(ctx, conn, remotePort)
		}
	}()

	return nil
}

// Close closes the SSH connection.
func (c *Client) Close() error {
	var errs []error

	// Close agent connection first if it exists
	if c.agentConn != nil {
		err := c.agentConn.Close()
		if err != nil {
			c.log.Debug("Failed to close agent connection", "error", err.Error())

			errs = append(errs, fmt.Errorf("failed to close agent connection: %w", err))
		}

		c.agentConn = nil
		c.agentForwardingEnabled = false
	}

	// Close SSH client connection
	if c.client != nil {
		c.connected = false

		err := c.client.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to close SSH client: %w", err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// createHostKeyCallback creates a secure host key callback.
func (c *Client) createHostKeyCallback() (ssh.HostKeyCallback, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	knownHostsPath := filepath.Join(homeDir, ".ssh", "known_hosts")

	// Try to use known_hosts file for verification
	_, err = os.Stat(knownHostsPath)
	if err == nil {
		hostKeyCallback, err := knownhosts.New(knownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load known_hosts file: %w", err)
		}

		// Wrap the callback to handle new hosts
		return c.wrapHostKeyCallback(hostKeyCallback, knownHostsPath), nil
	}

	// Create known_hosts file if it doesn't exist
	err = os.MkdirAll(filepath.Dir(knownHostsPath), sshDirectoryMode)
	if err != nil {
		return nil, fmt.Errorf("failed to create .ssh directory: %w", err)
	}
	// #nosec G304 - knownHostsPath is constructed from safe paths (user home directory)
	_, err = os.Create(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create known_hosts file: %w", err)
	}

	// Now load the empty known_hosts file
	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load newly created known_hosts file: %w", err)
	}

	return c.wrapHostKeyCallback(hostKeyCallback, knownHostsPath), nil
}

// wrapHostKeyCallback wraps the known hosts callback to handle new hosts.
func (c *Client) wrapHostKeyCallback(knownHostsCallback ssh.HostKeyCallback, knownHostsPath string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := knownHostsCallback(hostname, remote, key)
		if err != nil {
			errStr := err.Error()

			// Check if this is a host key mismatch error
			if strings.Contains(errStr, "mismatch") || strings.Contains(errStr, "differs") {
				c.log.Errorw("Host key has changed! Possible security breach detected",
					"host", hostname,
					"error", err.Error())

				return fmt.Errorf("host key verification failed - host key has changed: %w", err)
			}

			// Check if this is an unknown host error
			if strings.Contains(errStr, "not in known_hosts") || strings.Contains(errStr, "unknown") {
				c.log.Warnw("Unknown host, automatically adding to known_hosts",
					"host", hostname,
					"fingerprint", ssh.FingerprintSHA256(key))

				// Auto-add the host key (bastion provisioning context)
				addErr := c.addHostKey(knownHostsPath, hostname, key)
				if addErr != nil {
					c.log.Errorw("Failed to add host key", "error", addErr.Error())

					return fmt.Errorf("failed to add host key: %w", addErr)
				}

				c.log.Infow("Host key added to known_hosts", "host", hostname)

				return nil
			}

			// Other verification errors
			return fmt.Errorf("host key verification failed: %w", err)
		}

		// Host key verified successfully
		return nil
	}
}

// addHostKey adds a host key to the known_hosts file.
func (c *Client) addHostKey(knownHostsPath, hostname string, key ssh.PublicKey) error {
	// Ensure SSH directory exists
	sshDir := filepath.Dir(knownHostsPath)

	err := os.MkdirAll(sshDir, sshDirectoryMode)
	if err != nil {
		return fmt.Errorf("failed to create SSH directory: %w", err)
	}

	// Open known_hosts file for appending
	file, err := os.OpenFile(knownHostsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, privateKeyMode) // #nosec G304 - safe path construction
	if err != nil {
		return fmt.Errorf("failed to open known_hosts file: %w", err)
	}

	defer func() {
		err := file.Close()
		if err != nil {
			c.log.Warn("Failed to close known_hosts file", "error", err.Error())
		}
	}()

	// Format the host key entry
	keyLine := knownhosts.Line([]string{hostname}, key)

	// Write the key entry
	_, err = file.WriteString(keyLine + "\n")
	if err != nil {
		return fmt.Errorf("failed to write host key: %w", err)
	}

	return nil
}

// createFallbackHostKeyCallback creates a host key callback that adds unknown hosts.
func (c *Client) createFallbackHostKeyCallback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}

		knownHostsPath := filepath.Join(homeDir, ".ssh", "known_hosts")

		// Try to verify against existing known_hosts
		_, err = os.Stat(knownHostsPath)
		if err == nil {
			hostKeyCallback, err := knownhosts.New(knownHostsPath)
			if err == nil {
				err := hostKeyCallback(hostname, remote, key)
				if err == nil {
					return nil // Host key is known and valid
				}
			}
		}

		// Host is unknown, add it to known_hosts
		c.log.Infow("Adding unknown host to known_hosts", "host", hostname)

		err = c.addHostKey(knownHostsPath, hostname, key)
		if err != nil {
			return fmt.Errorf("failed to add host key: %w", err)
		}

		return nil
	}
}

// prepareSSHConfig creates the SSH client configuration.
func (c *Client) prepareSSHConfig() (*ssh.ClientConfig, error) {
	// Create host key callback
	hostKeyCallback, err := c.createHostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("failed to create host key callback: %w", err)
	}

	config := &ssh.ClientConfig{
		User:              c.config.User,
		Auth:              nil,
		HostKeyCallback:   hostKeyCallback,
		BannerCallback:    nil,
		ClientVersion:     "",
		HostKeyAlgorithms: nil,
		Timeout:           defaultCommandTimeout,
		Config: ssh.Config{
			Rand:           nil,
			RekeyThreshold: 0,
			KeyExchanges:   nil,
			Ciphers:        nil,
			MACs:           nil,
		},
	}

	// Add authentication methods
	authMethods, err := c.prepareAuthMethods()
	if err != nil {
		return nil, fmt.Errorf("failed to prepare auth methods: %w", err)
	}

	config.Auth = authMethods

	return config, nil
}

// prepareAuthMethods prepares SSH authentication methods.
func (c *Client) prepareAuthMethods() ([]ssh.AuthMethod, error) {
	var authMethods []ssh.AuthMethod

	// Password authentication
	if c.config.Password != "" {
		authMethods = append(authMethods, ssh.Password(c.config.Password))
	}

	// Public key authentication
	if c.config.PrivateKeyPath != "" {
		keyAuth, err := c.keyManager.CreatePublicKeyAuth(c.config.PrivateKeyPath, c.config.Password)
		if err != nil {
			c.log.Warnw("Failed to create public key auth",
				"key_path", c.config.PrivateKeyPath,
				"error", err.Error())
		} else {
			authMethods = append(authMethods, keyAuth)
		}
	}

	if len(authMethods) == 0 {
		return nil, ErrNoAuthMethodsAvailable
	}

	return authMethods, nil
}

// connectWithNativeClient attempts connection using Go's native SSH client.
func (c *Client) connectWithNativeClient(ctx context.Context, sshConfig *ssh.ClientConfig) (*ssh.Client, error) {
	address := fmt.Sprintf("%s:%d", c.config.Host, c.config.Port)

	// Create dialer with context
	dialer := net.Dialer{
		Timeout:       shortTimeout,
		Deadline:      time.Time{},
		LocalAddr:     nil,
		DualStack:     false,
		FallbackDelay: 0,
		KeepAlive:     0,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   false,
			Idle:     0,
			Interval: 0,
			Count:    0,
		},
		Resolver:       nil,
		Cancel:         nil,
		ControlContext: nil,
		Control:        nil,
	}

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %w", err)
	}

	// Perform SSH handshake
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, address, sshConfig)
	if err != nil {
		err := conn.Close()
		if err != nil {
			// Connection close errors are typically not critical
			_ = err
		}

		return nil, fmt.Errorf("SSH handshake failed: %w", err)
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}

// connectWithExternalSSH attempts connection using external SSH command as fallback.
func (c *Client) connectWithExternalSSH(ctx context.Context, sshConfig *ssh.ClientConfig) (*ssh.Client, error) {
	c.log.Debug("Attempting external SSH connection fallback")

	err := c.validateExternalSSHConnectivity(ctx)
	if err != nil {
		return nil, fmt.Errorf("external SSH validation failed: %w", err)
	}

	return c.connectWithRelaxedConfig(ctx, sshConfig)
}

// validateExternalSSHConnectivity tests connectivity using external SSH command.
func (c *Client) validateExternalSSHConnectivity(ctx context.Context) error {
	// Check if SSH command is available
	_, err := exec.LookPath("ssh")
	if err != nil {
		return ErrExternalSSHNotAvailable
	}

	// Build SSH command arguments
	args := []string{
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}

	// Add port if not default
	if c.config.Port != DefaultSSHPort {
		args = append(args, "-p", strconv.Itoa(c.config.Port))
	}

	// Add private key if specified
	if c.config.PrivateKeyPath != "" {
		args = append(args, "-i", c.config.PrivateKeyPath)
	}

	// Add custom SSH options
	for _, opt := range c.config.SSHOptions {
		args = append(args, "-o", opt)
	}

	// Add host and simple test command
	args = append(args, fmt.Sprintf("%s@%s", c.config.User, c.config.Host), "echo", "connection-test")

	// Execute SSH command
	cmd := exec.CommandContext(ctx, "ssh", args...)

	// Set up environment if password authentication is needed.
	// Password is injected via SSHPASS env var; -e tells sshpass to read it
	// from the environment instead of argv, keeping the secret out of ps output.
	if c.config.UseSSHPass && c.config.Password != "" {
		_, err := exec.LookPath("sshpass")
		if err == nil {
			sshpassArgs := make([]string, 0, sshpassArgCount+len(args))
			sshpassArgs = append(sshpassArgs, "-e", "ssh")
			sshpassArgs = append(sshpassArgs, args...)

			err := validateCommand(append([]string{"sshpass"}, sshpassArgs...))
			if err != nil {
				return fmt.Errorf("invalid sshpass command: %w", err)
			}

			cmd = exec.CommandContext(ctx, "sshpass", sshpassArgs...) // #nosec G204 - command is validated above

			cmd.Env = append(os.Environ(), "SSHPASS="+c.config.Password)
		} else {
			c.log.Warn("sshpass not available for password authentication")
		}
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("external SSH test failed: %w (output: %s)", err, string(output))
	}

	expectedOutput := "connection-test"
	if !strings.Contains(string(output), expectedOutput) {
		return ErrUnexpectedSSHOutput(string(output))
	}

	c.log.Debug("External SSH connectivity validated")

	return nil
}

// handleTunnelConnection handles a single tunnel connection.
func (c *Client) handleTunnelConnection(ctx context.Context, localConn net.Conn, remotePort int) {
	defer func() {
		err := localConn.Close()
		if err != nil {
			c.log.Debug("Failed to close local connection", "error", err.Error())
		}
	}()

	// Dial remote connection through SSH
	remoteAddr := fmt.Sprintf("localhost:%d", remotePort)

	remoteConn, err := c.client.Dial("tcp", remoteAddr)
	if err != nil {
		c.log.Errorw("Failed to dial remote address",
			"address", remoteAddr,
			"error", err)

		return
	}

	defer func() {
		err := remoteConn.Close()
		if err != nil {
			c.log.Debug("Failed to close remote connection", "error", err.Error())
		}
	}()

	// Copy data bidirectionally
	done := make(chan struct{}, defaultChannelBuffer)

	go func() {
		_, err := io.Copy(remoteConn, localConn)
		if err != nil {
			c.log.Debugw("Tunnel copy error", "direction", "local->remote", "error", err)
		}

		done <- struct{}{}
	}()

	go func() {
		_, err := io.Copy(localConn, remoteConn)
		if err != nil {
			c.log.Debugw("Tunnel copy error", "direction", "remote->local", "error", err)
		}

		done <- struct{}{}
	}()

	select {
	case <-done:
		// Connection closed by one side
	case <-ctx.Done():
		// Context cancelled
	}
}

func (c *Client) setupAgentForwarding(ctx context.Context) {
	// Check if SSH_AUTH_SOCK is set (agent is available)
	authSock := os.Getenv("SSH_AUTH_SOCK")
	if authSock == "" {
		c.log.Debug("SSH agent not available (SSH_AUTH_SOCK not set)")

		return
	}

	c.log.Debugw("Setting up SSH agent forwarding", "auth_sock", authSock)

	// Connect to local SSH agent with context
	agentCtx, cancel := context.WithTimeout(ctx, 5*time.Second) //nolint:mnd // reasonable timeout for agent connection
	defer cancel()

	var d net.Dialer

	conn, err := d.DialContext(agentCtx, "unix", authSock)
	if err != nil {
		c.log.Warn("Failed to connect to SSH agent", "error", err.Error(), "auth_sock", authSock)

		return
	}

	// Create agent client
	agentClient := agent.NewClient(conn)

	// Verify agent is working by listing keys
	keys, err := agentClient.List()
	if err != nil {
		c.log.Warn("Failed to list SSH agent keys", "error", err.Error())

		_ = conn.Close()

		return
	}

	c.log.Debugw("SSH agent has keys available", "key_count", len(keys))

	// Set up agent forwarding channel handler on the SSH client
	// This registers a handler for "auth-agent@openssh.com" channels from the server
	err = agent.ForwardToAgent(c.client, agentClient)
	if err != nil {
		c.log.Warn("Failed to set up agent forwarding handler", "error", err.Error())

		_ = conn.Close()

		return
	}

	// CRITICAL: Keep the agent connection alive for the SSH client's lifetime
	// If we close this, agent forwarding will fail on subsequent sessions
	c.agentConn = conn
	c.agentForwardingEnabled = true

	c.log.Infow("SSH agent forwarding channel handler established", "keys_available", len(keys))
}

func (c *Client) createSession() (*ssh.Session, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	// Agent forwarding is handled via the channel handler established in setupAgentForwarding
	// CRITICAL: Also request agent forwarding per-session for OpenSSH compatibility
	// Some SSH servers require BOTH the channel handler AND the session request
	if c.agentForwardingEnabled {
		err := agent.RequestAgentForwarding(session)
		if err != nil {
			// Non-fatal - channel handler is already active and will handle forwarding
			// Some servers deny the per-session request when channel handler is sufficient
			c.log.Debug("Per-session agent forwarding request denied (channel handler active)", "error", err.Error())
		} else {
			c.log.Debug("Session created with SSH agent forwarding enabled (channel handler + session request)")
		}
	}

	return session, nil
}

func (c *Client) closeSession(session *ssh.Session) {
	err := session.Close()
	if err != nil {
		// Session close errors are typically not critical
		_ = err
	}
}

func (c *Client) setupOutputCapture(session *ssh.Session) (*strings.Builder, *strings.Builder) {
	var stdout, stderr strings.Builder

	session.Stdout = &stdout
	session.Stderr = &stderr

	return &stdout, &stderr
}

func (c *Client) executeWithContext(ctx context.Context, session *ssh.Session, cmd string, startTime time.Time, stdout, stderr *strings.Builder) (*CommandResult, error) {
	done := make(chan error, 1)

	go func() {
		done <- session.Run(cmd)
	}()

	select {
	case <-ctx.Done():
		err := session.Signal(ssh.SIGTERM)
		if err != nil {
			// Signal errors are expected when session is already closing
			_ = err
		}

		return nil, fmt.Errorf("context cancelled: %w", ctx.Err())
	case err := <-done:
		return c.buildCommandResult(cmd, err, startTime, stdout, stderr)
	}
}

func (c *Client) buildCommandResult(cmd string, err error, startTime time.Time, stdout, stderr *strings.Builder) (*CommandResult, error) {
	duration := time.Since(startTime)

	result := &CommandResult{
		Command:  cmd,
		ExitCode: 0,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: duration,
	}

	if err != nil {
		exitErr := &ssh.ExitError{
			Waitmsg: ssh.Waitmsg{},
		}
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitStatus()
		} else {
			result.ExitCode = 1
		}

		c.log.Debugw("Command failed",
			"command", logger.RedactSecrets(cmd),
			"exit_code", result.ExitCode,
			"stderr", logger.RedactSecrets(result.Stderr))

		return result, fmt.Errorf("command failed with exit code %d: %w",
			result.ExitCode, err)
	}

	result.ExitCode = 0

	c.log.Debugw("Command completed successfully",
		"command", logger.RedactSecrets(cmd),
		"duration", duration.String())

	return result, nil
}

func (c *Client) connectWithRelaxedConfig(ctx context.Context, sshConfig *ssh.ClientConfig) (*ssh.Client, error) {
	relaxedConfig := c.createRelaxedSSHConfig(sshConfig)
	address := fmt.Sprintf("%s:%d", c.config.Host, c.config.Port)

	dialer := c.createNetDialer()

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to dial with relaxed settings: %w", err)
	}

	return c.performSSHHandshake(conn, address, relaxedConfig)
}

func (c *Client) createRelaxedSSHConfig(sshConfig *ssh.ClientConfig) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:              sshConfig.User,
		Auth:              sshConfig.Auth,
		HostKeyCallback:   c.createFallbackHostKeyCallback(),
		BannerCallback:    nil,
		ClientVersion:     "SSH-2.0-OCFP-Go-Client",
		HostKeyAlgorithms: nil,
		Timeout:           longTimeout,
		Config: ssh.Config{
			Rand:           nil,
			RekeyThreshold: 0,
			KeyExchanges:   nil,
			Ciphers:        nil,
			MACs:           nil,
		},
	}
}

func (c *Client) createNetDialer() net.Dialer {
	return net.Dialer{
		Timeout:       mediumTimeout,
		Deadline:      time.Time{},
		LocalAddr:     nil,
		DualStack:     false,
		FallbackDelay: 0,
		KeepAlive:     0,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   false,
			Idle:     0,
			Interval: 0,
			Count:    0,
		},
		Resolver:       nil,
		Cancel:         nil,
		ControlContext: nil,
		Control:        nil,
	}
}

func (c *Client) performSSHHandshake(conn net.Conn, address string, relaxedConfig *ssh.ClientConfig) (*ssh.Client, error) {
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, address, relaxedConfig)
	if err != nil {
		closeErr := conn.Close()
		if closeErr != nil {
			// Connection close errors are typically not critical
			_ = closeErr
		}

		return nil, fmt.Errorf("SSH handshake failed with relaxed settings: %w", err)
	}

	c.log.Info("External SSH fallback successful - connected with relaxed settings")

	return ssh.NewClient(sshConn, chans, reqs), nil
}
