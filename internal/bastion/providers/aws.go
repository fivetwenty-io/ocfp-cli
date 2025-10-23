package providers

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

const (
	// SSH connection defaults.
	defaultSSHPort = 22
)

// AWS provider errors.
var (
	ErrAWSAccessKeyRequired       = errors.New("AWS access key ID is required")
	ErrAWSSecretKeyRequired       = errors.New("AWS secret access key is required")
	ErrAWSRegionRequired          = errors.New("AWS region is required")
	ErrCouldNotDetermineBastionIP = errors.New("could not determine bastion IP address")
)

// AWSBastionInit implements bastion initialization for AWS.
type AWSBastionInit struct {
	config *config.Config
	log    logger.Logger
}

// NewAWSBastionInit creates a new AWS bastion initializer.
func NewAWSBastionInit(cfg *config.Config) *AWSBastionInit {
	return &AWSBastionInit{
		config: cfg,
		log:    logger.Get(),
	}
}

// Validate validates the AWS configuration.
func (a *AWSBastionInit) Validate() error {
	a.log.Debug("Validating AWS configuration")

	if a.config.AccessKeyID == "" {
		return ErrAWSAccessKeyRequired
	}

	if a.config.SecretAccessKey == "" {
		return ErrAWSSecretKeyRequired
	}

	if a.config.Region == "" {
		return ErrAWSRegionRequired
	}

	return nil
}

// PrepareEnvironment prepares AWS-specific environment variables.
func (a *AWSBastionInit) PrepareEnvironment() map[string]string {
	env := make(map[string]string)

	// Add OCFP-specific variables
	env["OCFP_BLOC"] = a.config.Name
	env["OCFP_PROVIDER"] = "aws"

	// Add AWS-specific variables
	if a.config.AccessKeyID != "" {
		env["AWS_ACCESS_KEY_ID"] = a.config.AccessKeyID
	}

	if a.config.SecretAccessKey != "" {
		env["AWS_SECRET_ACCESS_KEY"] = a.config.SecretAccessKey
	}

	if a.config.Region != "" {
		env["AWS_DEFAULT_REGION"] = a.config.Region
	}

	if a.config.SessionToken != "" {
		env["AWS_SESSION_TOKEN"] = a.config.SessionToken
	}

	// Add Genesis-specific variables if configured
	a.addGenesisEnv(env)

	// Add bastion git configuration
	if a.config.Bastion.Git.User.Name != "" {
		env["OCFP_BASTION_GIT_USER_NAME"] = a.config.Bastion.Git.User.Name
	}

	if a.config.Bastion.Git.User.Email != "" {
		env["OCFP_BASTION_GIT_USER_EMAIL"] = a.config.Bastion.Git.User.Email
	}

	return env
}

// GetConnectionDetails returns SSH connection details for the bastion.
func (a *AWSBastionInit) GetConnectionDetails() (*ConnectionDetails, error) {
	a.log.Debug("Getting AWS bastion connection details")

	bastionIP, err := a.getBastionIP()
	if err != nil {
		return nil, fmt.Errorf("failed to get bastion IP: %w", err)
	}

	// Get SSH user (EC2 default is usually ec2-user or ubuntu)
	sshUser := a.config.Bastion.SSHUser
	if sshUser == "" {
		sshUser = "ec2-user" // Default for AWS EC2
	}

	// Find SSH private key
	keyManager := ssh.NewKeyManager()

	privateKeyPath, err := keyManager.FindPrivateKey(a.config.Name)
	if err != nil {
		// Try to restore key from config if it exists
		keypairName := a.config.Name + "-bastion"
		if configKey, exists := a.config.Keys[keypairName]; exists && configKey != "" {
			restoredPath, restoreErr := keyManager.RestoreKeyFromConfig(a.config.Name, configKey)
			if restoreErr == nil && restoredPath != "" {
				a.log.Info("Restored SSH key from config", "path", restoredPath)
				privateKeyPath = restoredPath
			} else {
				a.log.Warn("Failed to restore key from config", "error", restoreErr)

				return nil, fmt.Errorf("failed to find SSH private key: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to find SSH private key: %w", err)
		}
	}

	// Check if key is password protected
	isEncrypted, err := keyManager.IsKeyPasswordProtected(privateKeyPath)
	if err != nil {
		a.log.Warn("Failed to check if key is encrypted", "error", err.Error())
	}

	// Prepare SSH options (just the option values, not the -o flag)
	sshOptions := []string{
		"StrictHostKeyChecking=no",
		"UserKnownHostsFile=/dev/null",
		"LogLevel=ERROR",
		"ConnectTimeout=30",
		"ForwardAgent=yes",
	}

	details := &ConnectionDetails{
		Host:           bastionIP,
		Port:           defaultSSHPort,
		User:           sshUser,
		PrivateKeyPath: privateKeyPath,
		Password:       "",
		SSHOptions:     sshOptions,
		UseSSHPass:     false,
	}

	// Set password if key is encrypted
	if isEncrypted {
		details.Password = a.config.Name
		details.UseSSHPass = true
	}

	return details, nil
}

// Initialize performs the actual bastion initialization.
func (a *AWSBastionInit) Initialize(ctx context.Context) error {
	a.log.Info("Initializing AWS bastion")

	return nil
}

// addGenesisEnv adds Genesis-specific environment variables to the provided map.
func (a *AWSBastionInit) addGenesisEnv(env map[string]string) {
	if !a.config.Bastion.Genesis.Enabled {
		env["GENESIS_SKIP_INSTALL"] = "1"

		return
	}

	if a.config.Bastion.Genesis.Branch != "" {
		env["GENESIS_BRANCH"] = a.config.Bastion.Genesis.Branch
	}

	if a.config.Bastion.Genesis.Commit != "" {
		env["GENESIS_COMMIT"] = a.config.Bastion.Genesis.Commit
	}

	if a.config.Bastion.Genesis.VersionPrefix != "" {
		env["GENESIS_VERSION_PREFIX"] = a.config.Bastion.Genesis.VersionPrefix
	}

	if a.config.Bastion.Genesis.Repo != "" {
		env["GENESIS_REPO"] = a.config.Bastion.Genesis.Repo
	}
}

// getBastionIP retrieves the bastion host IP address.
func (a *AWSBastionInit) getBastionIP() (string, error) {
	if a.config.BastionIP != "" {
		return a.config.BastionIP, nil
	}

	// Try environment variable
	if ip := os.Getenv("AWS_BASTION_IP"); ip != "" {
		return ip, nil
	}

	// Try to get from state file
	ip, err := a.getBastionIPFromState()
	if err == nil {
		return ip, nil
	}

	// Try to get from AWS API (would need AWS SDK integration)
	// For now, return an error
	return "", ErrCouldNotDetermineBastionIP
}

// getBastionIPFromState retrieves the bastion IP from the state file.
func (a *AWSBastionInit) getBastionIPFromState() (string, error) {
	// Get standard state directory for this bloc
	stateDir, err := state.GetStateDir(a.config.Name)
	if err != nil {
		return "", fmt.Errorf("failed to determine state directory: %w", err)
	}

	stateMgr, err := state.NewManager(stateDir)
	if err != nil {
		return "", fmt.Errorf("failed to create state manager: %w", err)
	}

	// Load the state for this bloc
	_, err = stateMgr.Load(a.config.Name)
	if err != nil {
		return "", fmt.Errorf("failed to load state: %w", err)
	}

	// Try to get bastion IP from outputs (same as commands/bastion_lookup.go)
	bastionIPOutput, err := stateMgr.GetOutput("bastion_public_ip")
	if err == nil {
		if publicIP, ok := bastionIPOutput.(string); ok && publicIP != "" {
			a.log.Debugw("Found bastion IP in state outputs", "ip", publicIP)

			return publicIP, nil
		}
	}

	// Fallback: try to get from instance resource
	bastionName := a.config.Name + "-bastion"

	resource, err := stateMgr.GetResource("instance", bastionName)
	if err != nil {
		return "", fmt.Errorf("failed to get bastion resource: %w", err)
	}

	if resource == nil {
		return "", ErrCouldNotDetermineBastionIP
	}

	publicIP, ok := resource.Properties["public_ip"].(string)
	if !ok || publicIP == "" {
		return "", ErrCouldNotDetermineBastionIP
	}

	a.log.Debugw("Found bastion IP in state resource", "ip", publicIP)

	return publicIP, nil
}
