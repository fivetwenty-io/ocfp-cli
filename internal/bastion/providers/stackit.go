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

// STACKIT provider errors.
var (
	ErrStackitProjectIDRequired            = errors.New("STACKIT project ID is required")
	ErrStackitRegionRequired               = errors.New("STACKIT region is required")
	ErrStackitAPIIntegrationNotImplemented = errors.New("STACKIT API integration not implemented")
	ErrNoStateFileFound                    = errors.New("no state file found")
	ErrBastionInstanceNoPublicIP           = errors.New("bastion instance has no public IP")
)

// StackitBastionInit implements bastion initialization for STACKIT.
type StackitBastionInit struct {
	config *config.Config
	log    logger.Logger
}

// NewStackitBastionInit creates a new STACKIT bastion initializer.
func NewStackitBastionInit(cfg *config.Config) *StackitBastionInit {
	return &StackitBastionInit{
		config: cfg,
		log:    logger.Get(),
	}
}

// Validate validates the STACKIT configuration.
func (s *StackitBastionInit) Validate() error {
	s.log.Debug("Validating STACKIT configuration")

	// Check required configuration
	if s.config.ProjectID == "" {
		return ErrStackitProjectIDRequired
	}

	if s.config.Region == "" {
		return ErrStackitRegionRequired
	}

	// Check for service account JSON or other auth method
	if s.config.ServiceAccountJSON == "" {
		s.log.Warn("No STACKIT service account JSON configured, authentication may fail")
	}

	return nil
}

// PrepareEnvironment prepares STACKIT-specific environment variables.
func (s *StackitBastionInit) PrepareEnvironment() map[string]string {
	env := make(map[string]string)

	// Add OCFP-specific variables
	env["OCFP_BLOC"] = s.config.Name
	env["OCFP_PROVIDER"] = "stackit"

	// Add STACKIT-specific variables
	if s.config.ProjectID != "" {
		env["STACKIT_PROJECT_ID"] = s.config.ProjectID
	}

	if s.config.OrgID != "" {
		env["STACKIT_ORG_ID"] = s.config.OrgID
	}

	if s.config.Region != "" {
		env["STACKIT_REGION"] = s.config.Region
	}

	// Encode service account JSON if present
	if s.config.ServiceAccountJSON != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(s.config.ServiceAccountJSON))
		env["STACKIT_SERVICE_ACCOUNT_JSON_BASE64"] = encoded
	}

	// Add Genesis-specific variables if configured
	s.addGenesisEnv(env)

	// Add bastion git configuration
	if s.config.Bastion.Git.User.Name != "" {
		env["OCFP_BASTION_GIT_USER_NAME"] = s.config.Bastion.Git.User.Name
	}

	if s.config.Bastion.Git.User.Email != "" {
		env["OCFP_BASTION_GIT_USER_EMAIL"] = s.config.Bastion.Git.User.Email
	}

	return env
}

// GetConnectionDetails returns SSH connection details for the bastion.
func (s *StackitBastionInit) GetConnectionDetails() (*ConnectionDetails, error) {
	s.log.Debug("Getting STACKIT bastion connection details")

	bastionIP, err := s.getBastionIP()
	if err != nil {
		return nil, fmt.Errorf("failed to get bastion IP: %w", err)
	}

	sshUser := s.getSSHUser()

	privateKeyPath, err := s.findSSHPrivateKey()
	if err != nil {
		return nil, err
	}

	isEncrypted := s.checkKeyEncryption(privateKeyPath)
	sshOptions := s.buildSSHOptions()

	details := &ConnectionDetails{
		Host:           bastionIP,
		Port:           defaultSSHPort,
		User:           sshUser,
		PrivateKeyPath: privateKeyPath,
		Password:       "",
		SSHOptions:     sshOptions,
		UseSSHPass:     false,
	}

	s.configurePasswordIfEncrypted(details, isEncrypted)

	return details, nil
}

// Initialize performs the actual bastion initialization.
func (s *StackitBastionInit) Initialize(ctx context.Context) error {
	s.log.Info("Initializing STACKIT bastion")

	// This method coordinates the initialization process
	// The actual work is done by the Manager class, but this method
	// can perform STACKIT-specific setup if needed

	return nil
}

// getSSHUser returns the SSH user for bastion connection.
func (s *StackitBastionInit) getSSHUser() string {
	if s.config.Bastion.SSHUser != "" {
		return s.config.Bastion.SSHUser
	}

	return defaultSSHUser // Default for STACKIT
}

// findSSHPrivateKey locates the SSH private key, restoring from config if needed.
func (s *StackitBastionInit) findSSHPrivateKey() (string, error) {
	keyManager := ssh.NewKeyManager()

	privateKeyPath, err := keyManager.FindPrivateKey(s.config.Name)
	if err == nil {
		return privateKeyPath, nil
	}

	// Try to restore key from config if it exists
	keypairName := s.config.Name + "-keypair"

	configKey, exists := s.config.Keys[keypairName]
	if !exists || configKey == "" {
		return "", fmt.Errorf("failed to find SSH private key: %w", err)
	}

	restoredPath, restoreErr := keyManager.RestoreKeyFromConfig(s.config.Name, configKey)
	if restoreErr != nil || restoredPath == "" {
		s.log.Warn("Failed to restore key from config", "error", restoreErr)

		return "", fmt.Errorf("failed to find SSH private key: %w", err)
	}

	s.log.Info("Restored SSH key from config", "path", restoredPath)

	return restoredPath, nil
}

// checkKeyEncryption checks if the SSH key is password protected.
func (s *StackitBastionInit) checkKeyEncryption(privateKeyPath string) bool {
	keyManager := ssh.NewKeyManager()

	isEncrypted, err := keyManager.IsKeyPasswordProtected(privateKeyPath)
	if err != nil {
		s.log.Warn("Failed to check if key is encrypted", "error", err.Error())

		return false
	}

	return isEncrypted
}

// buildSSHOptions constructs the SSH options list.
func (s *StackitBastionInit) buildSSHOptions() []string {
	sshOptions := []string{
		"StrictHostKeyChecking=no",
		"UserKnownHostsFile=/dev/null",
		"LogLevel=ERROR",
		"ConnectTimeout=30",
		"ForwardAgent=yes",
	}

	if s.config.Bastion.SSHOptions != "" {
		customOptions := []string{s.config.Bastion.SSHOptions}
		sshOptions = append(sshOptions, customOptions...)
	}

	return sshOptions
}

// configurePasswordIfEncrypted sets up password authentication if key is encrypted.
func (s *StackitBastionInit) configurePasswordIfEncrypted(details *ConnectionDetails, isEncrypted bool) {
	if !isEncrypted {
		return
	}

	details.Password = s.config.Name

	details.UseSSHPass = true

	_, err := exec.LookPath("sshpass")
	if err != nil {
		s.log.Warn("SSH key is encrypted but sshpass is not available")

		details.UseSSHPass = false
	}
}

// addGenesisEnv adds Genesis-specific environment variables to the provided map.
func (s *StackitBastionInit) addGenesisEnv(env map[string]string) {
	if !s.config.Bastion.Genesis.Enabled {
		env["GENESIS_SKIP_INSTALL"] = "1"

		return
	}

	if s.config.Bastion.Genesis.Branch != "" {
		env["GENESIS_BRANCH"] = s.config.Bastion.Genesis.Branch
	}

	if s.config.Bastion.Genesis.Commit != "" {
		env["GENESIS_COMMIT"] = s.config.Bastion.Genesis.Commit
	}

	if s.config.Bastion.Genesis.VersionPrefix != "" {
		env["GENESIS_VERSION_PREFIX"] = s.config.Bastion.Genesis.VersionPrefix
	}

	if s.config.Bastion.Genesis.Repo != "" {
		env["GENESIS_REPO"] = s.config.Bastion.Genesis.Repo
	}
}

// getBastionIP retrieves the bastion host IP address.
//nolint:dupl // intentionally similar CPI implementation
func (s *StackitBastionInit) getBastionIP() (string, error) {
	// Strategy 1: Check if IP is already configured
	if s.config.BastionIP != "" {
		s.log.Debugw("Using configured bastion IP", "ip", s.config.BastionIP)

		return s.config.BastionIP, nil
	}

	// Strategy 2: Check state cache first (fast path)
	stateDir, err := state.GetStateDir(s.config.Name)
	if err == nil { //nolint:nestif // state cache lookup requires nested checks
		stateManager, err := state.NewManager(stateDir)
		if err == nil {
			_, err := stateManager.Load(s.config.Name)
			if err == nil {
				v, err := stateManager.GetOutput("bastion_public_ip")
				if err == nil {
					if bastionIP, ok := v.(string); ok && bastionIP != "" {
						s.log.Debugw("Retrieved bastion IP from state cache", "ip", bastionIP)

						return bastionIP, nil
					}
				}
			}
		}
	}

	// Strategy 3: Try to get from STACKIT API
	bastionIP, err := s.getBastionIPFromAPI()
	if err == nil && bastionIP != "" {
		s.log.Debugw("Retrieved bastion IP from STACKIT API", "ip", bastionIP)

		return bastionIP, nil
	}

	// Strategy 4: Try to find in terraform state or other sources
	ip, err := s.getBastionIPFromState()
	if err == nil && ip != "" {
		s.log.Debugw("Retrieved bastion IP from state", "ip", ip)

		return ip, nil
	}

	// Strategy 5: Check environment variable
	if ip := os.Getenv("STACKIT_BASTION_IP"); ip != "" {
		s.log.Debugw("Using bastion IP from environment", "ip", ip)

		return ip, nil
	}

	return "", ErrCouldNotDetermineBastionIP
}

// getBastionIPFromAPI retrieves bastion IP from STACKIT API.
func (s *StackitBastionInit) getBastionIPFromAPI() (string, error) {
	s.log.Debug("Attempting to retrieve bastion IP from STACKIT API")

	// Get the STACKIT provider instance
	provider, err := cpi.GetProvider("stackit")
	if err != nil {
		s.log.Debugw("Failed to get STACKIT provider", "error", err)

		return "", fmt.Errorf("failed to get provider: %w", err)
	}

	// Initialize the provider with our config
	err = provider.Initialize(context.Background(), s.config)
	if err != nil {
		s.log.Debugw("Failed to initialize STACKIT provider", "error", err)

		return "", fmt.Errorf("failed to initialize provider: %w", err)
	}

	// Search for bastion instance by name pattern
	bastionName := s.config.Name + "-bastion"
	filters := map[string]string{
		"name": bastionName,
	}

	s.log.Debugw("Searching for bastion instance", "name", bastionName)

	instances, err := provider.Compute().ListInstances(context.Background(), filters)
	if err != nil {
		s.log.Debugw("Failed to list instances", "error", err)

		return "", fmt.Errorf("failed to list instances: %w", err)
	}

	// Find the bastion instance
	for _, inst := range instances {
		if inst.Name == bastionName {
			// Prefer public IP, fall back to floating IP
			if inst.PublicIP != "" {
				s.log.Debugw("Found bastion with public IP", "name", inst.Name, "ip", inst.PublicIP)

				return inst.PublicIP, nil
			}

			if inst.FloatingIP != "" {
				s.log.Debugw("Found bastion with floating IP", "name", inst.Name, "ip", inst.FloatingIP)

				return inst.FloatingIP, nil
			}

			s.log.Debugw("Found bastion but no public IP assigned", "name", inst.Name)

			return "", fmt.Errorf("%w: %s", ErrBastionInstanceNoPublicIP, bastionName)
		}
	}

	s.log.Debugw("No bastion instance found", "name", bastionName)

	return "", fmt.Errorf("%w: %s", ErrBastionInstanceNotFound, bastionName)
}

// getBastionIPFromState retrieves bastion IP from terraform state or similar.
func (s *StackitBastionInit) getBastionIPFromState() (string, error) {
	// Look for terraform state file or other state sources
	stateFiles := []string{
		"terraform.tfstate",
		fmt.Sprintf("terraform-%s.tfstate", s.config.Name),
		filepath.Join("state", s.config.Name+".tfstate"),
	}

	for _, stateFile := range stateFiles {
		_, err := os.Stat(stateFile)
		if err == nil {
			// Parse state file to extract bastion IP
			// This is a placeholder - would need actual terraform state parsing
			s.log.Debugw("Found terraform state file", "file", stateFile)
			// return s.parseStateFile(stateFile)
		}
	}

	return "", ErrNoStateFileFound
}
