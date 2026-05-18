package providers

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

const (
	// SSH connection defaults.
	defaultSSHPort = 22
)

// EC2DescribeInstancesAPI is the subset of the EC2 client used for bastion IP lookup.
// Defined as an interface to allow injection of fakes in tests.
type EC2DescribeInstancesAPI interface {
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

// AWSBastionInit implements bastion initialization for AWS.
type AWSBastionInit struct {
	config    *config.Config
	log       logger.Logger
	ec2Client EC2DescribeInstancesAPI // nil until first use; injectable for tests
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
		restoredPath, restoreErr := a.tryRestoreKeyFromConfig(keyManager)
		if restoreErr != nil {
			return nil, fmt.Errorf("failed to find SSH private key: %w", err)
		}

		privateKeyPath = restoredPath
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
func (a *AWSBastionInit) Initialize(_ctx context.Context) error {
	a.log.Info("Initializing AWS bastion")

	return nil
}

// addGenesisEnv adds Genesis-specific environment variables to the provided map.
func (a *AWSBastionInit) addGenesisEnv(env map[string]string) {
	// GENESIS_ENVIRONMENT is required by Genesis v3.2+ kit hooks.
	env["GENESIS_ENVIRONMENT"] = a.config.Name

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
// Resolution order:
//  1. Explicit config field (BastionIP)
//  2. Environment variable AWS_BASTION_IP
//  3. State file (bastion_public_ip output or instance resource)
//  4. EC2 DescribeInstances filtered by tag:Name={bloc}-bastion + instance-state-name=running
func (a *AWSBastionInit) getBastionIP() (string, error) {
	if a.config.BastionIP != "" {
		return a.config.BastionIP, nil
	}

	if ip := os.Getenv("AWS_BASTION_IP"); ip != "" {
		return ip, nil
	}

	ip, err := a.getBastionIPFromState()
	if err == nil {
		return ip, nil
	}

	a.log.Debugw("State lookup failed, falling back to EC2 API", "error", err.Error())

	return a.getBastionIPFromEC2(context.Background())
}

// getBastionIPFromEC2 queries EC2 for a running instance tagged Name={bloc}-bastion
// and returns its public IP address.
func (a *AWSBastionInit) getBastionIPFromEC2(ctx context.Context) (string, error) {
	client, err := a.getOrBuildEC2Client(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to build EC2 client for bastion lookup: %w", err)
	}

	bastionName := a.config.Name + "-bastion"

	input := &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []string{bastionName},
			},
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running"},
			},
		},
	}

	result, err := client.DescribeInstances(ctx, input)
	if err != nil {
		return "", fmt.Errorf("EC2 DescribeInstances failed for %s: %w", bastionName, err)
	}

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.PublicIpAddress != nil && *instance.PublicIpAddress != "" {
				ip := *instance.PublicIpAddress

				a.log.Debugw("Found bastion IP via EC2 API", "instance", aws.ToString(instance.InstanceId), "ip", ip)

				return ip, nil
			}
		}
	}

	return "", fmt.Errorf("%w: no running EC2 instance found with tag Name=%s", ErrCouldNotDetermineBastionIP, bastionName)
}

// getOrBuildEC2Client returns the injected EC2 client (tests) or builds one from config.
func (a *AWSBastionInit) getOrBuildEC2Client(ctx context.Context) (EC2DescribeInstancesAPI, error) {
	if a.ec2Client != nil {
		return a.ec2Client, nil
	}

	var opts []func(*awsconfig.LoadOptions) error

	if a.config.Region != "" {
		opts = append(opts, awsconfig.WithRegion(a.config.Region))
	}

	if a.config.AccessKeyID != "" && a.config.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				a.config.AccessKeyID,
				a.config.SecretAccessKey,
				a.config.SessionToken,
			),
		))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	a.ec2Client = ec2.NewFromConfig(cfg)

	return a.ec2Client, nil
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

// tryRestoreKeyFromConfig attempts to restore an SSH key from the configuration.
func (a *AWSBastionInit) tryRestoreKeyFromConfig(keyManager *ssh.KeyManager) (string, error) {
	keypairName := a.config.Name + "-bastion"
	configKey, exists := a.config.Keys[keypairName]

	if !exists || configKey == "" {
		return "", ErrNoKeyFoundInConfig(keypairName)
	}

	restoredPath, err := keyManager.RestoreKeyFromConfig(a.config.Name, configKey)
	if err != nil {
		a.log.Warn("Failed to restore key from config", "error", err)

		return "", fmt.Errorf("failed to restore key from config: %w", err)
	}

	if restoredPath == "" {
		return "", ErrRestoredPathEmpty
	}

	a.log.Info("Restored SSH key from config", "path", restoredPath)

	return restoredPath, nil
}
