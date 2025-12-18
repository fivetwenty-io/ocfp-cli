package providers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// Proxmox provider errors.
var (
	ErrProxmoxHostRequired = errors.New("Proxmox host URL is required")
	ErrProxmoxAuthRequired = errors.New("Proxmox API token or username/password is required")
)

// ProxmoxBastionInit implements bastion initialization for Proxmox.
type ProxmoxBastionInit struct {
	config *config.Config
	log    logger.Logger
}

// NewProxmoxBastionInit creates a new Proxmox bastion initializer.
func NewProxmoxBastionInit(cfg *config.Config) *ProxmoxBastionInit {
	return &ProxmoxBastionInit{
		config: cfg,
		log:    logger.Get(),
	}
}

// Validate validates the Proxmox configuration.
func (p *ProxmoxBastionInit) Validate() error {
	p.log.Debug("Validating Proxmox configuration")

	// Check required configuration
	if p.config.APIEndpoint == "" {
		return ErrProxmoxHostRequired
	}

	// Check for authentication (API token or username/password)
	hasAPIToken := p.config.AuthToken != "" && p.config.Password != ""
	hasUserPass := p.config.Username != "" && p.config.Password != ""

	if !hasAPIToken && !hasUserPass {
		p.log.Warn("No Proxmox authentication configured, authentication may fail")
	}

	return nil
}

// PrepareEnvironment prepares Proxmox-specific environment variables.
func (p *ProxmoxBastionInit) PrepareEnvironment() map[string]string {
	env := make(map[string]string)

	// Add OCFP-specific variables
	env["OCFP_BLOC"] = p.config.Name
	env["OCFP_PROVIDER"] = "proxmox"

	// Add Proxmox-specific variables
	if p.config.APIEndpoint != "" {
		env["PROXMOX_HOST"] = p.config.APIEndpoint
	}

	if p.config.Region != "" {
		env["PROXMOX_NODE"] = p.config.Region
	}

	// API Token authentication
	if p.config.AuthToken != "" {
		env["PROXMOX_TOKEN_ID"] = p.config.AuthToken
	}

	if p.config.Password != "" && p.config.AuthToken != "" {
		// Token secret
		env["PROXMOX_TOKEN_SECRET"] = p.config.Password
	}

	// Username/password authentication (base64 encoded for security)
	if p.config.Username != "" && p.config.Password != "" && p.config.AuthToken == "" {
		env["PROXMOX_USERNAME"] = p.config.Username
		encoded := base64.StdEncoding.EncodeToString([]byte(p.config.Password))
		env["PROXMOX_PASSWORD_BASE64"] = encoded
	}

	// Add Genesis-specific variables if configured
	p.addGenesisEnv(env)

	// Add bastion git configuration
	if p.config.Bastion.Git.User.Name != "" {
		env["OCFP_BASTION_GIT_USER_NAME"] = p.config.Bastion.Git.User.Name
	}

	if p.config.Bastion.Git.User.Email != "" {
		env["OCFP_BASTION_GIT_USER_EMAIL"] = p.config.Bastion.Git.User.Email
	}

	return env
}

// GetConnectionDetails returns SSH connection details for the bastion.
func (p *ProxmoxBastionInit) GetConnectionDetails() (*ConnectionDetails, error) {
	p.log.Debug("Getting Proxmox bastion connection details")

	bastionIP, err := p.getBastionIP()
	if err != nil {
		return nil, fmt.Errorf("failed to get bastion IP: %w", err)
	}

	sshUser := p.getSSHUser()

	privateKeyPath, err := p.findSSHPrivateKey()
	if err != nil {
		return nil, err
	}

	isEncrypted := p.checkKeyEncryption(privateKeyPath)
	sshOptions := p.buildSSHOptions()

	details := &ConnectionDetails{
		Host:           bastionIP,
		Port:           defaultSSHPort,
		User:           sshUser,
		PrivateKeyPath: privateKeyPath,
		Password:       "",
		SSHOptions:     sshOptions,
		UseSSHPass:     false,
	}

	p.configurePasswordIfEncrypted(details, isEncrypted)

	return details, nil
}

// Initialize performs the actual bastion initialization.
func (p *ProxmoxBastionInit) Initialize(ctx context.Context) error {
	p.log.Info("Initializing Proxmox bastion")

	// This method coordinates the initialization process
	// The actual work is done by the Manager class, but this method
	// can perform Proxmox-specific setup if needed

	return nil
}

// getSSHUser returns the SSH user for bastion connection.
func (p *ProxmoxBastionInit) getSSHUser() string {
	if p.config.Bastion.SSHUser != "" {
		return p.config.Bastion.SSHUser
	}

	return "ubuntu" // Default for cloud images on Proxmox
}

// findSSHPrivateKey locates the SSH private key, restoring from config if needed.
func (p *ProxmoxBastionInit) findSSHPrivateKey() (string, error) {
	keyManager := ssh.NewKeyManager()

	privateKeyPath, err := keyManager.FindPrivateKey(p.config.Name)
	if err == nil {
		return privateKeyPath, nil
	}

	// Try to restore key from config if it exists
	keypairName := p.config.Name + "-keypair"

	configKey, exists := p.config.Keys[keypairName]
	if !exists || configKey == "" {
		return "", fmt.Errorf("failed to find SSH private key: %w", err)
	}

	restoredPath, restoreErr := keyManager.RestoreKeyFromConfig(p.config.Name, configKey)
	if restoreErr != nil || restoredPath == "" {
		p.log.Warn("Failed to restore key from config", "error", restoreErr)

		return "", fmt.Errorf("failed to find SSH private key: %w", err)
	}

	p.log.Info("Restored SSH key from config", "path", restoredPath)

	return restoredPath, nil
}

// checkKeyEncryption checks if the SSH key is password protected.
func (p *ProxmoxBastionInit) checkKeyEncryption(privateKeyPath string) bool {
	keyManager := ssh.NewKeyManager()

	isEncrypted, err := keyManager.IsKeyPasswordProtected(privateKeyPath)
	if err != nil {
		p.log.Warn("Failed to check if key is encrypted", "error", err.Error())

		return false
	}

	return isEncrypted
}

// buildSSHOptions constructs the SSH options list.
func (p *ProxmoxBastionInit) buildSSHOptions() []string {
	sshOptions := []string{
		"StrictHostKeyChecking=no",
		"UserKnownHostsFile=/dev/null",
		"LogLevel=ERROR",
		"ConnectTimeout=30",
		"ForwardAgent=yes",
	}

	if p.config.Bastion.SSHOptions != "" {
		customOptions := []string{p.config.Bastion.SSHOptions}
		sshOptions = append(sshOptions, customOptions...)
	}

	return sshOptions
}

// configurePasswordIfEncrypted sets up password authentication if key is encrypted.
func (p *ProxmoxBastionInit) configurePasswordIfEncrypted(details *ConnectionDetails, isEncrypted bool) {
	if !isEncrypted {
		return
	}

	details.Password = p.config.Name

	details.UseSSHPass = true

	_, err := exec.LookPath("sshpass")
	if err != nil {
		p.log.Warn("SSH key is encrypted but sshpass is not available")

		details.UseSSHPass = false
	}
}

// addGenesisEnv adds Genesis-specific environment variables to the provided map.
func (p *ProxmoxBastionInit) addGenesisEnv(env map[string]string) {
	if !p.config.Bastion.Genesis.Enabled {
		env["GENESIS_SKIP_INSTALL"] = "1"

		return
	}

	if p.config.Bastion.Genesis.Branch != "" {
		env["GENESIS_BRANCH"] = p.config.Bastion.Genesis.Branch
	}

	if p.config.Bastion.Genesis.Commit != "" {
		env["GENESIS_COMMIT"] = p.config.Bastion.Genesis.Commit
	}

	if p.config.Bastion.Genesis.VersionPrefix != "" {
		env["GENESIS_VERSION_PREFIX"] = p.config.Bastion.Genesis.VersionPrefix
	}

	if p.config.Bastion.Genesis.Repo != "" {
		env["GENESIS_REPO"] = p.config.Bastion.Genesis.Repo
	}
}

// getBastionIP retrieves the bastion host IP address.
func (p *ProxmoxBastionInit) getBastionIP() (string, error) {
	// Strategy 1: Check if IP is already configured
	if p.config.BastionIP != "" {
		p.log.Debugw("Using configured bastion IP", "ip", p.config.BastionIP)

		return p.config.BastionIP, nil
	}

	// Strategy 2: Check state cache first (fast path)
	stateDir, err := state.GetStateDir(p.config.Name)
	if err == nil {
		stateManager, err := state.NewManager(stateDir)
		if err == nil {
			_, err := stateManager.Load(p.config.Name)
			if err == nil {
				v, err := stateManager.GetOutput("bastion_public_ip")
				if err == nil {
					if bastionIP, ok := v.(string); ok && bastionIP != "" {
						p.log.Debugw("Retrieved bastion IP from state cache", "ip", bastionIP)

						return bastionIP, nil
					}
				}
			}
		}
	}

	// Strategy 3: Try to get from Proxmox API
	bastionIP, err := p.getBastionIPFromAPI()
	if err == nil && bastionIP != "" {
		p.log.Debugw("Retrieved bastion IP from Proxmox API", "ip", bastionIP)

		return bastionIP, nil
	}

	// Strategy 4: Try to find in terraform state or other sources
	ip, err := p.getBastionIPFromState()
	if err == nil && ip != "" {
		p.log.Debugw("Retrieved bastion IP from state", "ip", ip)

		return ip, nil
	}

	// Strategy 5: Check environment variable
	if ip := os.Getenv("PROXMOX_BASTION_IP"); ip != "" {
		p.log.Debugw("Using bastion IP from environment", "ip", ip)

		return ip, nil
	}

	return "", ErrCouldNotDetermineBastionIP
}

// getBastionIPFromAPI retrieves bastion IP from Proxmox API.
func (p *ProxmoxBastionInit) getBastionIPFromAPI() (string, error) {
	p.log.Debug("Attempting to retrieve bastion IP from Proxmox API")

	// Get the Proxmox provider instance
	provider, err := cpi.GetProvider("proxmox")
	if err != nil {
		p.log.Debugw("Failed to get Proxmox provider", "error", err)

		return "", fmt.Errorf("failed to get provider: %w", err)
	}

	// Initialize the provider with our config
	err = provider.Initialize(context.Background(), p.config)
	if err != nil {
		p.log.Debugw("Failed to initialize Proxmox provider", "error", err)

		return "", fmt.Errorf("failed to initialize provider: %w", err)
	}

	// Search for bastion instance by name pattern
	bastionName := p.config.Name + "-bastion"
	filters := map[string]string{
		"name": bastionName,
	}

	p.log.Debugw("Searching for bastion instance", "name", bastionName)

	instances, err := provider.Compute().ListInstances(context.Background(), filters)
	if err != nil {
		p.log.Debugw("Failed to list instances", "error", err)

		return "", fmt.Errorf("failed to list instances: %w", err)
	}

	// Find the bastion instance
	for _, inst := range instances {
		if inst.Name == bastionName {
			// Prefer public IP, fall back to private IP
			if inst.PublicIP != "" {
				p.log.Debugw("Found bastion with public IP", "name", inst.Name, "ip", inst.PublicIP)

				return inst.PublicIP, nil
			}

			if inst.PrivateIP != "" {
				p.log.Debugw("Found bastion with private IP", "name", inst.Name, "ip", inst.PrivateIP)

				return inst.PrivateIP, nil
			}

			p.log.Debugw("Found bastion but no IP assigned", "name", inst.Name)

			return "", fmt.Errorf("bastion instance %s has no IP", bastionName)
		}
	}

	p.log.Debugw("No bastion instance found", "name", bastionName)

	return "", fmt.Errorf("bastion instance not found: %s", bastionName)
}

// getBastionIPFromState retrieves bastion IP from terraform state or similar.
func (p *ProxmoxBastionInit) getBastionIPFromState() (string, error) {
	// Look for terraform state file or other state sources
	stateFiles := []string{
		"terraform.tfstate",
		fmt.Sprintf("terraform-%s.tfstate", p.config.Name),
		filepath.Join("state", p.config.Name+".tfstate"),
	}

	for _, stateFile := range stateFiles {
		_, err := os.Stat(stateFile)
		if err == nil {
			// Parse state file to extract bastion IP
			// This is a placeholder - would need actual terraform state parsing
			p.log.Debugw("Found terraform state file", "file", stateFile)
			// return p.parseStateFile(stateFile)
		}
	}

	return "", ErrNoStateFileFound
}
