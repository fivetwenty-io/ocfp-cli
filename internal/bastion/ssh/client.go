package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// validateCommand validates that only safe commands are executed.
func validateCommand(cmdSlice []string) error {
	if len(cmdSlice) == 0 {
		return errors.New("empty command")
	}

	allowedCommands := map[string]bool{
		"sshpass": true,
		"ssh":     true,
	}

	if !allowedCommands[cmdSlice[0]] {
		return fmt.Errorf("command not allowed: %s", cmdSlice[0])
	}

	// Check for shell metacharacters in arguments
	for _, arg := range cmdSlice[1:] {
		if strings.Contains(arg, ";") || strings.Contains(arg, "|") || strings.Contains(arg, "&") || strings.Contains(arg, "`") {
			return fmt.Errorf("argument contains shell metacharacters: %s", arg)
		}
	}

	return nil
}

// Client implements the SSHClient interface using Go's native SSH library.
type Client struct {
	config     *ConnectionDetails
	options    *ProvisioningOptions
	client     *ssh.Client
	log        logger.Logger
	connected  bool
	keyManager *KeyManager
}

// NewClient creates a new SSH client.
func NewClient(config *ConnectionDetails, options *ProvisioningOptions) *Client {
	return &Client{
		config:     config,
		options:    options,
		log:        logger.Get(),
		keyManager: NewKeyManager(),
	}
}

// Connect establishes an SSH connection to the bastion host.
func (c *Client) Connect(ctx context.Context) error {
	if c.connected {
		return nil
	}

	c.log.Info("Connecting to bastion host",
		"host", c.config.Host,
		"user", c.config.User)

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

	var lastErr error

	for index, strategy := range strategies {
		c.log.Debug("Attempting connection strategy", "strategy", index+1)

		client, err := strategy(ctx, sshConfig)
		if err != nil {
			lastErr = err
			c.log.Warn("Connection strategy failed",
				"strategy", index+1,
				"error", err.Error())

			continue
		}

		c.client = client
		c.connected = true
		c.log.Info("Successfully connected to bastion host")

		return nil
	}

	return fmt.Errorf("all connection strategies failed, last error: %w", lastErr)
}

// ExecuteCommand executes a command on the remote host.
func (c *Client) ExecuteCommand(ctx context.Context, cmd string) (*CommandResult, error) {
	if !c.connected {
		return nil, errors.New("SSH client not connected")
	}

	startTime := time.Now()

	c.log.Debug("Executing remote command", "command", cmd)

	// Create session
	session, err := c.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	defer func() {
		err := session.Close()
		if err != nil {
			// Session close errors are typically not critical
			_ = err
		}
	}()

	// Set up output capture
	var stdout, stderr strings.Builder

	session.Stdout = &stdout
	session.Stderr = &stderr

	// Execute command with context
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

		return nil, ctx.Err()
	case err := <-done:
		duration := time.Since(startTime)

		result := &CommandResult{
			Command:  cmd,
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			Duration: duration,
		}

		if err != nil {
			exitErr := &ssh.ExitError{}
			if errors.As(err, &exitErr) {
				result.ExitCode = exitErr.ExitStatus()
			} else {
				result.ExitCode = 1
			}

			c.log.Debug("Command failed",
				"command", cmd,
				"exit_code", result.ExitCode,
				"stderr", result.Stderr)

			return result, fmt.Errorf("command failed with exit code %d: %w",
				result.ExitCode, err)
		}

		result.ExitCode = 0

		c.log.Debug("Command completed successfully",
			"command", cmd,
			"duration", duration.String())

		return result, nil
	}
}

// TransferFile transfers a file between local and remote systems.
func (c *Client) TransferFile(ctx context.Context, local, remote string, opts TransferOptions) error {
	if !c.connected {
		return errors.New("SSH client not connected")
	}

	c.log.Info("Transferring file", "local", local, "remote", remote)

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
		return errors.New("SSH client not connected")
	}

	c.log.Info("Creating SSH tunnel",
		"local_port", localPort,
		"remote_port", remotePort)

	// Listen on local port
	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", localPort))
	if err != nil {
		return fmt.Errorf("failed to listen on local port %d: %w", localPort, err)
	}

	// Handle tunnel connections
	go func() {
		defer func() {
			err := listener.Close()
			if err != nil {
				c.log.Warn("Failed to close listener", "error", err)
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			default:
				conn, err := listener.Accept()
				if err != nil {
					c.log.Error("Failed to accept connection", "error", err)

					continue
				}

				go c.handleTunnelConnection(ctx, conn, remotePort)
			}
		}
	}()

	return nil
}

// Close closes the SSH connection.
func (c *Client) Close() error {
	if c.client != nil {
		c.connected = false

		return c.client.Close()
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
	if _, err := os.Stat(knownHostsPath); err == nil {
		hostKeyCallback, err := knownhosts.New(knownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load known_hosts file: %w", err)
		}

		// Wrap the callback to handle new hosts
		return c.wrapHostKeyCallback(hostKeyCallback, knownHostsPath), nil
	}

	// Create known_hosts file if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0700); err != nil {
		return nil, fmt.Errorf("failed to create .ssh directory: %w", err)
	}
	// #nosec G304 - knownHostsPath is constructed from safe paths (user home directory)
	if _, err := os.Create(knownHostsPath); err != nil {
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
				c.log.Error("Host key has changed! Possible security breach detected",
					"host", hostname,
					"error", err.Error())

				return fmt.Errorf("host key verification failed - host key has changed: %w", err)
			}

			// Check if this is an unknown host error
			if strings.Contains(errStr, "not in known_hosts") || strings.Contains(errStr, "unknown") {
				c.log.Warn("Unknown host, automatically adding to known_hosts",
					"host", hostname,
					"fingerprint", ssh.FingerprintSHA256(key))

				// Auto-add the host key (bastion provisioning context)
				addErr := c.addHostKey(knownHostsPath, hostname, key)
				if addErr != nil {
					c.log.Error("Failed to add host key", "error", addErr.Error())

					return fmt.Errorf("failed to add host key: %w", addErr)
				}

				c.log.Info("Host key added to known_hosts", "host", hostname)

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
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("failed to create SSH directory: %w", err)
	}

	// Open known_hosts file for appending
	file, err := os.OpenFile(knownHostsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600) // #nosec G304 - safe path construction
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
	if _, err := file.WriteString(keyLine + "\n"); err != nil {
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
		if _, err := os.Stat(knownHostsPath); err == nil {
			hostKeyCallback, err := knownhosts.New(knownHostsPath)
			if err == nil {
				err := hostKeyCallback(hostname, remote, key)
				if err == nil {
					return nil // Host key is known and valid
				}
			}
		}

		// Host is unknown, add it to known_hosts
		c.log.Info("Adding unknown host to known_hosts", "host", hostname)

		if err := c.addHostKey(knownHostsPath, hostname, key); err != nil {
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
		User:            c.config.User,
		HostKeyCallback: hostKeyCallback,
		Timeout:         30 * time.Second,
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
			c.log.Warn("Failed to create public key auth",
				"key_path", c.config.PrivateKeyPath,
				"error", err.Error())
		} else {
			authMethods = append(authMethods, keyAuth)
		}
	}

	if len(authMethods) == 0 {
		return nil, errors.New("no authentication methods available")
	}

	return authMethods, nil
}

// connectWithNativeClient attempts connection using Go's native SSH client.
func (c *Client) connectWithNativeClient(ctx context.Context, sshConfig *ssh.ClientConfig) (*ssh.Client, error) {
	address := fmt.Sprintf("%s:%d", c.config.Host, c.config.Port)

	// Create dialer with context
	dialer := net.Dialer{
		Timeout: 10 * time.Second,
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

	// First validate connectivity using external SSH
	if err := c.validateExternalSSHConnectivity(ctx); err != nil {
		return nil, fmt.Errorf("external SSH validation failed: %w", err)
	}

	// If external SSH works, try native connection again with relaxed settings
	// This can help with certain SSH server configurations that are picky
	relaxedConfig := &ssh.ClientConfig{
		User:            sshConfig.User,
		Auth:            sshConfig.Auth,
		HostKeyCallback: c.createFallbackHostKeyCallback(), // Fallback uses host key verification with auto-add
		Timeout:         45 * time.Second,                  // Longer timeout
		ClientVersion:   "SSH-2.0-OCFP-Go-Client",          // Custom client version
	}

	// Try native connection with relaxed config
	address := fmt.Sprintf("%s:%d", c.config.Host, c.config.Port)

	dialer := net.Dialer{
		Timeout: 15 * time.Second,
	}

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to dial with relaxed settings: %w", err)
	}

	// Perform SSH handshake with relaxed config
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, address, relaxedConfig)
	if err != nil {
		err := conn.Close()
		if err != nil {
			// Connection close errors are typically not critical
			_ = err
		}

		return nil, fmt.Errorf("SSH handshake failed with relaxed settings: %w", err)
	}

	c.log.Info("External SSH fallback successful - connected with relaxed settings")

	return ssh.NewClient(sshConn, chans, reqs), nil
}

// validateExternalSSHConnectivity tests connectivity using external SSH command.
func (c *Client) validateExternalSSHConnectivity(ctx context.Context) error {
	// Check if SSH command is available
	if _, err := exec.LookPath("ssh"); err != nil {
		return errors.New("external SSH command not available")
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
	if c.config.Port != 22 {
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

	// Set up environment if password authentication is needed
	if c.config.UseSSHPass && c.config.Password != "" {
		if _, err := exec.LookPath("sshpass"); err == nil {
			// Use sshpass for password authentication
			sshpassArgs := []string{"-p", c.config.Password, "ssh"}

			sshpassArgs = append(sshpassArgs, args...)
			err := validateCommand(append([]string{"sshpass"}, sshpassArgs...))
			if err != nil {
				return fmt.Errorf("invalid sshpass command: %w", err)
			}

			cmd = exec.CommandContext(ctx, "sshpass", sshpassArgs...) // #nosec G204 - command is validated above
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
		return fmt.Errorf("external SSH test returned unexpected output: %s", string(output))
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
		c.log.Error("Failed to dial remote address",
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
	done := make(chan struct{}, 2)

	go func() {
		if _, err := io.Copy(remoteConn, localConn); err != nil {
			c.log.Debug("Tunnel copy error", "direction", "local->remote", "error", err)
		}

		done <- struct{}{}
	}()

	go func() {
		if _, err := io.Copy(localConn, remoteConn); err != nil {
			c.log.Debug("Tunnel copy error", "direction", "remote->local", "error", err)
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
