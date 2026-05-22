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

// PVE provider errors.
var (
	ErrPVEHostRequired         = errors.New("pve host URL is required")
	ErrPVEAuthRequired         = errors.New("pve API token or username/password is required")
	ErrBastionInstanceNoIP     = errors.New("bastion instance has no IP")
	ErrBastionInstanceNotFound = errors.New("bastion instance not found")
)

// PVEBastionInit implements bastion initialization for Proxmox VE.
type PVEBastionInit struct {
	config *config.Config
	log    logger.Logger
}

// NewPVEBastionInit creates a new Proxmox VE bastion initializer.
func NewPVEBastionInit(cfg *config.Config) *PVEBastionInit {
	return &PVEBastionInit{
		config: cfg,
		log:    logger.Get(),
	}
}

// Validate validates the Proxmox VE configuration.
//
// Auth mode logic:
//   - API token auth: AuthToken + TokenSecret both non-empty.
//   - User/pass auth: Username + Password both non-empty.
//   - Password alone (with AuthToken) is NOT treated as the token secret;
//     TokenSecret must be set explicitly for API token auth.
func (p *PVEBastionInit) Validate() error {
	p.log.Debug("Validating Proxmox VE configuration")

	if p.config.APIEndpoint == "" {
		return ErrPVEHostRequired
	}

	// API token auth requires both AuthToken (token_id) and TokenSecret (token_secret).
	// Username/password auth requires both Username and Password.
	hasAPIToken := p.config.AuthToken != "" && p.config.TokenSecret != ""
	hasUserPass := p.config.Username != "" && p.config.Password != ""

	if !hasAPIToken && !hasUserPass {
		return ErrPVEAuthRequired
	}

	return nil
}

// PrepareEnvironment prepares Proxmox VE-specific environment variables.
func (p *PVEBastionInit) PrepareEnvironment() map[string]string {
	env := make(map[string]string)

	// Add OCFP-specific variables
	env["OCFP_BLOC"] = p.config.Name
	env["OCFP_PROVIDER"] = "pve"

	// Add PVE-specific variables
	if p.config.APIEndpoint != "" {
		env["PVE_HOST"] = p.config.APIEndpoint
	}

	if p.config.Region != "" {
		env["PVE_NODE"] = p.config.Region
	}

	// API token authentication: AuthToken is the token_id; TokenSecret is the token_secret.
	// These two fields are independent of Username/Password — do not conflate them.
	if p.config.AuthToken != "" && p.config.TokenSecret != "" {
		env["PVE_TOKEN_ID"] = p.config.AuthToken
		env["PVE_TOKEN_SECRET"] = p.config.TokenSecret
	}

	// Username/password authentication (base64 encoded for security).
	// Only emitted when NOT in API token mode to avoid leaking the user password
	// into an env var that consumers might misread as a token secret.
	if p.config.Username != "" && p.config.Password != "" && p.config.AuthToken == "" {
		env["PVE_USERNAME"] = p.config.Username
		encoded := base64.StdEncoding.EncodeToString([]byte(p.config.Password))
		env["PVE_PASSWORD_BASE64"] = encoded
	}

	// PVE network/storage topology vars consumed by the provision/bastion Perl script.
	// Config field mapping:
	//   PVE_BRIDGE       → config.Network.Name  (set by bootstrap as default_bridge)
	//   PVE_STORAGE_POOL → config.Artifacts.Data.StoragePool (VM root-disk pool)
	//   PVE_ISO_STORAGE  → config.IsoStorage    (cloud-init ISO / snippet pool)
	if p.config.Network.Name != "" {
		env["PVE_BRIDGE"] = p.config.Network.Name
	}

	if p.config.Artifacts.Data.StoragePool != "" {
		env["PVE_STORAGE_POOL"] = p.config.Artifacts.Data.StoragePool
	}

	if p.config.IsoStorage != "" {
		env["PVE_ISO_STORAGE"] = p.config.IsoStorage
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
func (p *PVEBastionInit) GetConnectionDetails() (*ConnectionDetails, error) {
	p.log.Debug("Getting Proxmox VE bastion connection details")

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

// Initialize validates PVE configuration and probes API connectivity.
//
// Validation always runs: missing host or auth credentials return an error
// immediately so the caller gets a clear diagnostic before any network I/O.
//
// Connectivity probe: Initialize attempts to authenticate with the PVE API
// via the registered CPI provider (which calls GET /version under the hood).
// A probe failure is treated as a non-fatal warning — the bastion host may be
// reachable by SSH even when the PVE API is temporarily unavailable or the
// operator plans to provide connectivity later. This matches the staged-init
// contract used by the AWS provider.
func (p *PVEBastionInit) Initialize(ctx context.Context) error {
	p.log.Infow("Initializing Proxmox VE bastion", "bloc", p.config.Name, "host", p.config.APIEndpoint)

	// Validate required configuration fields before attempting any I/O.
	if err := p.Validate(); err != nil {
		return fmt.Errorf("pve bastion configuration invalid: %w", err)
	}

	// Probe PVE API connectivity. Use the CPI provider so we exercise the same
	// auth path (token or user/pass) that the rest of the CLI uses at runtime.
	provider, err := cpi.GetProvider("pve")
	if err != nil {
		p.log.Warnw("PVE CPI provider not registered; skipping connectivity probe", "error", err)

		return nil
	}

	if err := provider.Initialize(ctx, p.config); err != nil {
		p.log.Warnw("PVE API connectivity probe failed; continuing with staged initialization",
			"host", p.config.APIEndpoint,
			"error", err,
		)

		return nil
	}

	p.log.Infow("PVE API connectivity confirmed", "host", p.config.APIEndpoint)

	return nil
}

// getSSHUser returns the SSH user for bastion connection.
func (p *PVEBastionInit) getSSHUser() string {
	if p.config.Bastion.SSHUser != "" {
		return p.config.Bastion.SSHUser
	}

	return defaultSSHUser // Default for cloud images on Proxmox VE
}

// findSSHPrivateKey locates the SSH private key, restoring from config if needed.
func (p *PVEBastionInit) findSSHPrivateKey() (string, error) {
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
func (p *PVEBastionInit) checkKeyEncryption(privateKeyPath string) bool {
	keyManager := ssh.NewKeyManager()

	isEncrypted, err := keyManager.IsKeyPasswordProtected(privateKeyPath)
	if err != nil {
		p.log.Warn("Failed to check if key is encrypted", "error", err.Error())

		return false
	}

	return isEncrypted
}

// buildSSHOptions constructs the SSH options list.
func (p *PVEBastionInit) buildSSHOptions() []string {
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
func (p *PVEBastionInit) configurePasswordIfEncrypted(details *ConnectionDetails, isEncrypted bool) {
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
func (p *PVEBastionInit) addGenesisEnv(env map[string]string) {
	// GENESIS_ENVIRONMENT is required by Genesis v3.2+ kit hooks.
	env["GENESIS_ENVIRONMENT"] = p.config.Name

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
//
//nolint:dupl // intentionally similar CPI implementation
func (p *PVEBastionInit) getBastionIP() (string, error) {
	// Strategy 1: Check if IP is already configured
	if p.config.BastionIP != "" {
		p.log.Debugw("Using configured bastion IP", "ip", p.config.BastionIP)

		return p.config.BastionIP, nil
	}

	// Strategy 2: Check state cache first (fast path)
	stateDir, err := state.GetStateDir(p.config.Name)
	if err == nil { //nolint:nestif // state cache lookup requires nested checks
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

	// Strategy 3: Try to get from Proxmox VE API
	bastionIP, err := p.getBastionIPFromAPI()
	if err == nil && bastionIP != "" {
		p.log.Debugw("Retrieved bastion IP from Proxmox VE API", "ip", bastionIP)

		return bastionIP, nil
	}

	// Strategy 4: Try to find in terraform state or other sources
	ip, err := p.getBastionIPFromState()
	if err == nil && ip != "" {
		p.log.Debugw("Retrieved bastion IP from state", "ip", ip)

		return ip, nil
	}

	// Strategy 5: Check environment variable
	if ip := os.Getenv("PVE_BASTION_IP"); ip != "" {
		p.log.Debugw("Using bastion IP from environment", "ip", ip)

		return ip, nil
	}

	return "", ErrCouldNotDetermineBastionIP
}

// getBastionIPFromAPI retrieves bastion IP from Proxmox VE API.
func (p *PVEBastionInit) getBastionIPFromAPI() (string, error) {
	p.log.Debug("Attempting to retrieve bastion IP from Proxmox VE API")

	// Get the PVE provider instance
	provider, err := cpi.GetProvider("pve")
	if err != nil {
		p.log.Debugw("Failed to get PVE provider", "error", err)

		return "", fmt.Errorf("failed to get provider: %w", err)
	}

	// Initialize the provider with our config
	err = provider.Initialize(context.Background(), p.config)
	if err != nil {
		p.log.Debugw("Failed to initialize PVE provider", "error", err)

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

			return "", fmt.Errorf("%w: %s", ErrBastionInstanceNoIP, bastionName)
		}
	}

	p.log.Debugw("No bastion instance found", "name", bastionName)

	return "", fmt.Errorf("%w: %s", ErrBastionInstanceNotFound, bastionName)
}

// getBastionIPFromState retrieves bastion IP from terraform state or similar.
func (p *PVEBastionInit) getBastionIPFromState() (string, error) {
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
