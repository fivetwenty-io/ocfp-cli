package providers

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
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
		return errors.New("STACKIT project ID is required")
	}

	if s.config.Region == "" {
		return errors.New("STACKIT region is required")
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
	env["OCFP_BLOC_NAME"] = s.config.Name
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
	if s.config.Bastion.Genesis.Enabled {
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
	} else {
		env["GENESIS_SKIP_INSTALL"] = "1"
	}

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

	// In a real implementation, this would query the STACKIT API to get the bastion IP
	// For now, we'll use configuration or make assumptions

	bastionIP, err := s.getBastionIP()
	if err != nil {
		return nil, fmt.Errorf("failed to get bastion IP: %w", err)
	}

	// Get SSH user
	sshUser := s.config.Bastion.SSHUser
	if sshUser == "" {
		sshUser = "ubuntu" // Default for STACKIT
	}

	// Find SSH private key
	keyManager := ssh.NewKeyManager()

	privateKeyPath, err := keyManager.FindPrivateKey(s.config.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to find SSH private key: %w", err)
	}

	// Check if key is password protected
	isEncrypted, err := keyManager.IsKeyPasswordProtected(privateKeyPath)
	if err != nil {
		s.log.Warn("Failed to check if key is encrypted", "error", err.Error())
	}

	// Prepare SSH options
	sshOptions := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=30",
	}

	// Add custom SSH options if configured
	if s.config.Bastion.SSHOptions != "" {
		// Parse custom options (this is a simplified implementation)
		// In practice, you'd want to properly parse the SSH options string
		customOptions := []string{s.config.Bastion.SSHOptions}
		sshOptions = append(sshOptions, customOptions...)
	}

	details := &ConnectionDetails{
		Host:           bastionIP,
		Port:           22,
		User:           sshUser,
		PrivateKeyPath: privateKeyPath,
		SSHOptions:     sshOptions,
	}

	// Set password if key is encrypted (use bloc name as password)
	if isEncrypted {
		details.Password = s.config.Name
		details.UseSSHPass = true

		// Check if sshpass is available
		if _, err := exec.LookPath("sshpass"); err != nil {
			s.log.Warn("SSH key is encrypted but sshpass is not available")

			details.UseSSHPass = false
		}
	}

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

// getBastionIP retrieves the bastion host IP address.
func (s *StackitBastionInit) getBastionIP() (string, error) {
	// Strategy 1: Check if IP is already configured
	if s.config.BastionIP != "" {
		s.log.Debug("Using configured bastion IP", "ip", s.config.BastionIP)

		return s.config.BastionIP, nil
	}

	// Strategy 2: Try to get from STACKIT API (would need to implement STACKIT client)
	if ip, err := s.getBastionIPFromAPI(); err == nil && ip != "" {
		s.log.Debug("Retrieved bastion IP from STACKIT API", "ip", ip)

		return ip, nil
	}

	// Strategy 3: Try to find in terraform state or other sources
	if ip, err := s.getBastionIPFromState(); err == nil && ip != "" {
		s.log.Debug("Retrieved bastion IP from state", "ip", ip)

		return ip, nil
	}

	// Strategy 4: Check environment variable
	if ip := os.Getenv("STACKIT_BASTION_IP"); ip != "" {
		s.log.Debug("Using bastion IP from environment", "ip", ip)

		return ip, nil
	}

	return "", errors.New("could not determine bastion IP address")
}

// getBastionIPFromAPI retrieves bastion IP from STACKIT API.
func (s *StackitBastionInit) getBastionIPFromAPI() (string, error) {
	// This would implement calls to STACKIT API to find the bastion server
	// For now, return an error to fall back to other methods
	return "", errors.New("STACKIT API integration not implemented")
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
		if _, err := os.Stat(stateFile); err == nil {
			// Parse state file to extract bastion IP
			// This is a placeholder - would need actual terraform state parsing
			s.log.Debug("Found terraform state file", "file", stateFile)
			// return s.parseStateFile(stateFile)
		}
	}

	return "", errors.New("no state file found")
}

// (Removed unused helper stubs: validateStackitConnection, setupStackitCredentials)
