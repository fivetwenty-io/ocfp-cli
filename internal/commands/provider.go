package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	ocfpexec "github.com/ocfp/ocfp-cli-go/internal/exec"
	"github.com/ocfp/ocfp-cli-go/internal/security"
)

const (
	// VaultTimeoutSeconds is the timeout duration in seconds for vault operations.
	VaultTimeoutSeconds = 10

	// StackitTimeoutSeconds is the timeout duration in seconds for STACKIT operations.
	StackitTimeoutSeconds = 30
)

var (
	validPathPattern = regexp.MustCompile(`^[a-zA-Z0-9/._:-]+$`)
)

const (
	authTypeToken = "token"
	authTypeJSON  = "json"
)

// NewProviderCmd creates the provider command.
func NewProviderCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "provider <action>",
		Short: "Manage cloud provider operations",
		Long: `Manage cloud provider operations including login and credential management.

Available actions:
  login    Login to the specified cloud provider

Provider is normally derived from the bloc configuration. Use --iaas only when
you want to override or operate without a bloc.

Examples:
  ocfp provider login --bloc my-bloc        # provider read from bloc config
  ocfp provider login --iaas stackit --bloc my-bloc  # explicit override
  ocfp provider login --iaas aws
  ocfp provider login --iaas gcp`,
		Args:         cobra.MinimumNArgs(1),
		RunE:         runProviderCmd,
		SilenceUsage: true,
	}

	cmd.Flags().String("iaas", "", "Cloud provider type (stackit, aws, openstack, gcp, azure) - optional if specified in bloc config")
	cmd.Flags().String("bloc", "", "Bloc name for configuration (required for STACKIT, can be used for provider discovery)")

	return cmd
}

func runProviderCmd(cmd *cobra.Command, args []string) error {
	log := zap.L()
	action := args[0]

	switch action {
	case "login":
		return handleProviderLogin(cmd, log)
	default:
		return ErrUnknownProviderAction(action)
	}
}

func handleProviderLogin(cmd *cobra.Command, log *zap.Logger) error {
	// Get provider name from flag, environment, or config
	providerName, _ := cmd.Flags().GetString("iaas")
	if providerName == "" {
		providerName = os.Getenv("OCFP_PROVIDER")
	}

	// If not specified, try to get from config using bloc name
	if providerName == "" {
		blocName := getBlocName(cmd)

		if blocName != "" {
			cfg, err := config.LoadWithParams("", blocName)
			if err == nil && cfg.Provider != "" {
				providerName = cfg.Provider
			}
		}
	}

	if providerName == "" {
		return ErrProviderNotSpecified
	}

	providerName = strings.ToLower(providerName)

	log.Info("Logging into provider", zap.String("provider", providerName))

	switch providerName {
	case ProviderStackit:
		return loginSTACKIT(cmd, log)
	case "aws":
		return loginAWS(cmd, log)
	case "openstack":
		return loginOpenStack(log)
	case "gcp":
		return loginGCP(log)
	case "azure":
		return loginAzure(log)
	default:
		return ErrUnsupportedProvider(providerName)
	}
}

// getBlocName retrieves the bloc name from various sources in priority order.
// It also strips the "-bastion" suffix if present, as users often append it
// when referring to bastion operations but the actual bloc name in config
// doesn't include this suffix.
func getBlocName(cmd *cobra.Command) string {
	var blocName string

	// Try local flag first
	blocName, _ = cmd.Flags().GetString("bloc")
	if blocName != "" {
		return stripBastionSuffix(blocName)
	}

	// Try global/parent flags (for commands where --bloc is defined on root)
	if cmd.Parent() != nil {
		blocName, _ = cmd.Parent().PersistentFlags().GetString("bloc")
		if blocName != "" {
			return stripBastionSuffix(blocName)
		}
	}

	// Try viper (supports various sources including config files)
	blocName = viper.GetString("bloc")
	if blocName != "" {
		return stripBastionSuffix(blocName)
	}

	// Try environment variable
	blocName = os.Getenv("OCFP_BLOC")
	if blocName != "" {
		return stripBastionSuffix(blocName)
	}

	return ""
}

// stripBastionSuffix removes the "-bastion" suffix from a bloc name if present.
// This allows users to use "my-bloc-bastion" as a shorthand when the actual
// bloc name in the config is "my-bloc".
func stripBastionSuffix(blocName string) string {
	return strings.TrimSuffix(blocName, "-bastion")
}

// STACKIT Login Implementation.
func loginSTACKIT(cmd *cobra.Command, log *zap.Logger) error {
	blocName := getBlocName(cmd)

	if blocName == "" {
		return ErrBlocFlagOrEnvVarRequired
	}

	// Load config to get project ID; a missing config is non-fatal
	cfg, _ := config.LoadWithParams("", blocName)

	// Get credentials (either JSON or token)
	authType, credentials, err := getSTACKITCredentials(blocName, log)
	if err != nil {
		return fmt.Errorf("could not retrieve STACKIT service account credentials: %w", err)
	}

	if credentials == "" {
		return ErrCouldNotRetrieveStackitCredentials
	}

	// Authenticate with service account
	if authType == authTypeToken {
		err = authenticateSTACKITToken(credentials, log)
	} else {
		err = authenticateSTACKIT(credentials, log)
	}

	if err != nil {
		return err
	}

	// Configure project ID if available
	if cfg != nil && cfg.ProjectID != "" {
		return configureSTACKITProject(cfg.ProjectID, log)
	}

	log.Warn("No project ID found in config - STACKIT CLI may require explicit project ID in commands")

	return nil
}

func getSTACKITCredentials(blocName string, log *zap.Logger) (string, string, error) {
	// Try config file first
	authType, credentials, err := getSTACKITCredentialsFromConfig(blocName, log)
	if err != nil {
		return "", "", err
	}

	if credentials != "" {
		return authType, credentials, nil
	}

	// If not found in config, try vault
	return getSTACKITCredentialsFromVault(blocName, log)
}

func getSTACKITCredentialsFromConfig(blocName string, log *zap.Logger) (string, string, error) {
	cfg, err := config.LoadWithParams("", blocName)
	if err != nil {
		log.Debug("Failed to load config", zap.Error(err))

		return "", "", nil
	}

	log.Debug("Attempting to get credentials from config file", zap.String("bloc", blocName))

	// Check if config has service account token
	if cfg.ServiceAccountToken != "" {
		log.Info("Retrieved STACKIT service account token from config file")

		return authTypeToken, cfg.ServiceAccountToken, nil
	}

	// Check if config has service account JSON
	if cfg.ServiceAccountJSON != "" {
		log.Info("Retrieved STACKIT service account credentials from config file")

		return authTypeJSON, cfg.ServiceAccountJSON, nil
	}

	// Check if config has service account key path
	if cfg.ServiceAccountKeyPath != "" {
		_, err := os.Stat(cfg.ServiceAccountKeyPath)
		if err == nil {
			content, err := os.ReadFile(cfg.ServiceAccountKeyPath)
			if err != nil {
				return "", "", fmt.Errorf("cannot read service account key file: %w", err)
			}

			log.Info("Retrieved STACKIT service account credentials from file", zap.String("path", cfg.ServiceAccountKeyPath))

			return authTypeJSON, string(content), nil
		}
	}

	return "", "", nil
}

func getSTACKITCredentialsFromVault(blocName string, log *zap.Logger) (string, string, error) {
	// Check if safe command is available
	_, err := exec.LookPath("safe")
	if err != nil {
		log.Debug("Safe command not available, skipping vault lookup")

		return "", "", nil
	}

	// Try to get token first
	tokenPath := fmt.Sprintf("secret/config/%s/mgmt/cpi/stackit:service_account_token", blocName)
	log.Debug("Attempting to retrieve STACKIT service account token from vault", zap.String("path", tokenPath))

	ctx, cancel := context.WithTimeout(context.Background(), VaultTimeoutSeconds*time.Second)
	defer cancel()

	err = security.ValidateInput(tokenPath, validPathPattern)
	if err != nil {
		return "", "", fmt.Errorf("invalid token path: %w", err)
	}

	cmd := exec.CommandContext(ctx, "safe", "get", tokenPath) // #nosec G204 - input validated above

	output, err := cmd.Output()
	if err == nil {
		token := strings.TrimSpace(string(output))
		if token != "" {
			log.Info("Retrieved STACKIT service account token from vault")

			return authTypeToken, token, nil
		}
	}

	// If no token, try JSON
	jsonPath := fmt.Sprintf("secret/config/%s/mgmt/cpi/stackit:service_account_json", blocName)
	log.Debug("Attempting to retrieve STACKIT service account JSON from vault", zap.String("path", jsonPath))

	err = security.ValidateInput(jsonPath, validPathPattern)
	if err != nil {
		return "", "", fmt.Errorf("invalid JSON path: %w", err)
	}

	cmd = exec.CommandContext(ctx, "safe", "get", jsonPath) // #nosec G204 - input validated above

	output, err = cmd.Output()
	if err == nil {
		jsonCreds := strings.TrimSpace(string(output))
		if jsonCreds != "" {
			log.Info("Retrieved STACKIT service account JSON from vault")

			return authTypeJSON, jsonCreds, nil
		}
	}

	log.Debug("Vault retrieval failed or returned empty")

	return "", "", nil
}

func authenticateSTACKIT(serviceAccountJSON string, log *zap.Logger) error {
	// Create temporary file for service account JSON
	tempFile, err := os.CreateTemp("", "stackit-service-account-*.json")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}

	defer func() { _ = os.Remove(tempFile.Name()) }() //nolint:gosec // path from os.CreateTemp is trusted

	_, err = tempFile.WriteString(serviceAccountJSON)
	if err != nil {
		_ = tempFile.Close()

		return fmt.Errorf("failed to write service account JSON: %w", err)
	}

	_ = tempFile.Close()

	// Execute stackit auth command
	log.Info("Authenticating with STACKIT...")
	log.Debug("Executing stackit auth activate-service-account", zap.String("keyPath", tempFile.Name()))

	ctx, cancel := context.WithTimeout(context.Background(), StackitTimeoutSeconds*time.Second)
	defer cancel()

	err = security.ValidateInput(tempFile.Name(), validPathPattern)
	if err != nil {
		return fmt.Errorf("invalid temp file path: %w", err)
	}

	cmd := exec.CommandContext(ctx, "stackit", "auth", "activate-service-account", "--service-account-key-path", tempFile.Name()) //nolint:gosec // command args are validated above

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		log.Error("Failed to login to STACKIT provider", zap.Error(err), zap.String("stderr", stderr.String()))

		return fmt.Errorf("STACKIT authentication failed: %w", err)
	}

	log.Info("Successfully logged into STACKIT provider")

	if stdout.Len() > 0 {
		_, _ = fmt.Fprint(os.Stdout, stdout.String())
	}

	return nil
}

func authenticateSTACKITToken(serviceAccountToken string, log *zap.Logger) error {
	// Pass the token via env var so it never appears in the process table (ps).
	// The STACKIT CLI reads STACKIT_SERVICE_ACCOUNT_TOKEN when the flag is absent.
	log.Info("Authenticating with STACKIT token...")
	log.Debug("Executing stackit auth activate-service-account (token via env)")

	ctx, cancel := context.WithTimeout(context.Background(), StackitTimeoutSeconds*time.Second)
	defer cancel()

	out, err := ocfpexec.RunWithEnv(ctx,
		map[string]string{"STACKIT_SERVICE_ACCOUNT_TOKEN": serviceAccountToken},
		"stackit", "auth", "activate-service-account",
	)
	if err != nil {
		log.Error("Failed to login to STACKIT provider", zap.Error(err), zap.String("output", string(out)))

		return fmt.Errorf("STACKIT authentication failed: %w", err)
	}

	log.Info("Successfully logged into STACKIT provider")

	if len(out) > 0 {
		_, _ = fmt.Fprint(os.Stdout, string(out))
	}

	return nil
}

func configureSTACKITProject(projectID string, log *zap.Logger) error {
	log.Info("Configuring STACKIT CLI project", zap.String("projectID", projectID))

	ctx, cancel := context.WithTimeout(context.Background(), StackitTimeoutSeconds*time.Second)
	defer cancel()

	err := security.ValidateInput(projectID, validPathPattern)
	if err != nil {
		return fmt.Errorf("invalid project ID: %w", err)
	}

	cmd := exec.CommandContext(ctx, "stackit", "config", "set", "--project-id", projectID) // #nosec G204 - input validated above

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		log.Error("Failed to configure STACKIT project", zap.Error(err), zap.String("stderr", stderr.String()))

		return fmt.Errorf("STACKIT project configuration failed: %w", err)
	}

	log.Info("Successfully configured STACKIT project", zap.String("projectID", projectID))

	if stdout.Len() > 0 {
		_, _ = fmt.Fprint(os.Stdout, stdout.String())
	}

	return nil
}

// AWS Login Implementation.
func loginAWS(cmd *cobra.Command, log *zap.Logger) error {
	blocName := getBlocName(cmd)

	if blocName == "" {
		return ErrBlocFlagOrEnvVarRequired
	}

	// Get credentials from config or vault
	credentials, err := getAWSCredentials(blocName, log)
	if err != nil {
		return fmt.Errorf("could not retrieve AWS credentials: %w", err)
	}

	if credentials == nil || credentials.AccessKeyID == "" || credentials.SecretAccessKey == "" {
		return fmt.Errorf("%w for bloc: %s", ErrAWSCredentialsNotFound, blocName)
	}

	// Configure AWS CLI profile
	return configureAWSProfile(blocName, credentials, log)
}

// AWSCredentials holds AWS authentication details.
type AWSCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string //nolint:gosec // field name is descriptive, not a hardcoded secret
	Region          string
}

func getAWSCredentials(blocName string, log *zap.Logger) (*AWSCredentials, error) {
	// Try config file first
	credentials := getAWSCredentialsFromConfig(blocName, log)

	if credentials != nil && credentials.AccessKeyID != "" {
		return credentials, nil
	}

	// If not found in config, try vault
	return getAWSCredentialsFromVault(blocName, log)
}

func getAWSCredentialsFromConfig(blocName string, log *zap.Logger) *AWSCredentials {
	cfg, err := config.LoadWithParams("", blocName)
	if err != nil {
		log.Debug("Failed to load config", zap.Error(err))

		return nil
	}

	log.Debug("Attempting to get AWS credentials from config file", zap.String("bloc", blocName))

	// Check if config has AWS credentials
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		log.Info("Retrieved AWS credentials from config file")

		return &AWSCredentials{
			AccessKeyID:     cfg.AccessKeyID,
			SecretAccessKey: cfg.SecretAccessKey,
			SessionToken:    cfg.SessionToken,
			Region:          cfg.Region,
		}
	}

	return nil
}

func getAWSCredentialsFromVault(blocName string, log *zap.Logger) (*AWSCredentials, error) {
	// Check if safe command is available
	_, err := exec.LookPath("safe")
	if err != nil {
		log.Debug("Safe command not available, skipping vault lookup")

		return nil, nil
	}

	log.Debug("Attempting to retrieve AWS credentials from vault", zap.String("bloc", blocName))

	ctx, cancel := context.WithTimeout(context.Background(), VaultTimeoutSeconds*time.Second)
	defer cancel()

	credentials := &AWSCredentials{}

	err = retrieveAWSAccessKeyFromVault(ctx, blocName, credentials)
	if err != nil {
		return nil, err
	}

	err = retrieveAWSSecretKeyFromVault(ctx, blocName, credentials)
	if err != nil {
		return nil, err
	}

	err = retrieveAWSSessionTokenFromVault(ctx, blocName, credentials)
	if err != nil {
		return nil, err
	}

	err = retrieveAWSRegionFromVault(ctx, blocName, credentials)
	if err != nil {
		return nil, err
	}

	if credentials.AccessKeyID != "" && credentials.SecretAccessKey != "" {
		log.Info("Retrieved AWS credentials from vault")

		return credentials, nil
	}

	log.Debug("Vault retrieval failed or returned empty")

	return nil, nil
}

// retrieveAWSAccessKeyFromVault retrieves the AWS access key from vault.
func retrieveAWSAccessKeyFromVault(ctx context.Context, blocName string, credentials *AWSCredentials) error {
	accessKeyPath := fmt.Sprintf("secret/config/%s/aws:access_key_id", blocName)

	err := security.ValidateInput(accessKeyPath, validPathPattern)
	if err != nil {
		return fmt.Errorf("invalid access key path: %w", err)
	}

	cmd := exec.CommandContext(ctx, "safe", "get", accessKeyPath) // #nosec G204 - input validated above

	output, err := cmd.Output()
	if err == nil {
		credentials.AccessKeyID = strings.TrimSpace(string(output))
	}

	return nil
}

// retrieveAWSSecretKeyFromVault retrieves the AWS secret access key from vault.
func retrieveAWSSecretKeyFromVault(ctx context.Context, blocName string, credentials *AWSCredentials) error {
	secretKeyPath := fmt.Sprintf("secret/config/%s/aws:secret_access_key", blocName)

	err := security.ValidateInput(secretKeyPath, validPathPattern)
	if err != nil {
		return fmt.Errorf("invalid secret key path: %w", err)
	}

	cmd := exec.CommandContext(ctx, "safe", "get", secretKeyPath) // #nosec G204 - input validated above

	output, err := cmd.Output()
	if err == nil {
		credentials.SecretAccessKey = strings.TrimSpace(string(output))
	}

	return nil
}

// retrieveAWSSessionTokenFromVault retrieves the AWS session token from vault.
func retrieveAWSSessionTokenFromVault(ctx context.Context, blocName string, credentials *AWSCredentials) error {
	sessionTokenPath := fmt.Sprintf("secret/config/%s/aws:session_token", blocName)

	err := security.ValidateInput(sessionTokenPath, validPathPattern)
	if err != nil {
		return fmt.Errorf("invalid session token path: %w", err)
	}

	cmd := exec.CommandContext(ctx, "safe", "get", sessionTokenPath) // #nosec G204 - input validated above

	output, err := cmd.Output()
	if err == nil {
		credentials.SessionToken = strings.TrimSpace(string(output))
	}

	return nil
}

// retrieveAWSRegionFromVault retrieves the AWS region from vault.
func retrieveAWSRegionFromVault(ctx context.Context, blocName string, credentials *AWSCredentials) error {
	regionPath := fmt.Sprintf("secret/config/%s/aws:region", blocName)

	err := security.ValidateInput(regionPath, validPathPattern)
	if err != nil {
		return fmt.Errorf("invalid region path: %w", err)
	}

	cmd := exec.CommandContext(ctx, "safe", "get", regionPath) // #nosec G204 - input validated above

	output, err := cmd.Output()
	if err == nil {
		credentials.Region = strings.TrimSpace(string(output))
	}

	return nil
}

func configureAWSProfile(profileName string, credentials *AWSCredentials, log *zap.Logger) error {
	log.Info("Configuring AWS CLI profile", zap.String("profile", profileName))

	// Validate profile name
	err := security.ValidateInput(profileName, validPathPattern)
	if err != nil {
		return fmt.Errorf("invalid profile name: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), VaultTimeoutSeconds*time.Second)
	defer cancel()

	err = setAWSAccessKeyID(ctx, profileName, credentials.AccessKeyID, log)
	if err != nil {
		return err
	}

	err = setAWSSecretAccessKey(ctx, profileName, credentials.SecretAccessKey, log)
	if err != nil {
		return err
	}

	if credentials.Region != "" {
		err = setAWSRegion(ctx, profileName, credentials.Region, log)
		if err != nil {
			return err
		}
	}

	if credentials.SessionToken != "" {
		err = setAWSSessionToken(ctx, profileName, credentials.SessionToken, log)
		if err != nil {
			return err
		}
	}

	// Validate credentials by calling AWS STS
	log.Info("Validating AWS credentials...")

	return validateAWSCredentials(profileName, log)
}

// setAWSAccessKeyID configures the AWS access key ID for a profile.
func setAWSAccessKeyID(ctx context.Context, profileName, accessKeyID string, log *zap.Logger) error {
	log.Debug("Setting AWS access key ID")

	cmd := exec.CommandContext(ctx, "aws", "configure", "set", "aws_access_key_id", accessKeyID, "--profile", profileName) // #nosec G204 - input validated above

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		log.Error("Failed to configure AWS access key ID", zap.Error(err), zap.String("stderr", stderr.String()))

		return fmt.Errorf("failed to configure AWS access key ID: %w", err)
	}

	return nil
}

// setAWSSecretAccessKey configures the AWS secret access key for a profile.
// The secret value is written directly to the credentials file rather than
// passed via `aws configure set` argv, which would expose it in the process
// table. writeAWSCredentialsKey handles the file operation.
func setAWSSecretAccessKey(ctx context.Context, profileName, secretAccessKey string, log *zap.Logger) error {
	log.Debug("Setting AWS secret access key")

	if err := writeAWSCredentialsKey(profileName, "aws_secret_access_key", secretAccessKey); err != nil {
		log.Error("Failed to configure AWS secret access key", zap.Error(err))

		return fmt.Errorf("failed to configure AWS secret access key: %w", err)
	}

	// Suppress unused ctx warning: retained in signature for API consistency.
	_ = ctx

	return nil
}

// setAWSRegion configures the AWS region for a profile.
func setAWSRegion(ctx context.Context, profileName, region string, log *zap.Logger) error {
	log.Debug("Setting AWS region", zap.String("region", region))

	cmd := exec.CommandContext(ctx, "aws", "configure", "set", "region", region, "--profile", profileName) // #nosec G204 - input validated above

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		log.Error("Failed to configure AWS region", zap.Error(err), zap.String("stderr", stderr.String()))

		return fmt.Errorf("failed to configure AWS region: %w", err)
	}

	return nil
}

// setAWSSessionToken configures the AWS session token for a profile.
// The token value is written directly to the credentials file rather than
// passed via `aws configure set` argv, which would expose it in the process
// table. writeAWSCredentialsKey handles the file operation.
func setAWSSessionToken(ctx context.Context, profileName, sessionToken string, log *zap.Logger) error {
	log.Debug("Setting AWS session token")

	if err := writeAWSCredentialsKey(profileName, "aws_session_token", sessionToken); err != nil {
		log.Error("Failed to configure AWS session token", zap.Error(err))

		return fmt.Errorf("failed to configure AWS session token: %w", err)
	}

	// Suppress unused ctx warning: retained in signature for API consistency.
	_ = ctx

	return nil
}

// writeAWSCredentialsKey writes a single key=value into the AWS credentials
// file (~/.aws/credentials) for the named profile. It reads the existing file,
// updates or inserts the key under the matching [profile] section, and rewrites
// the file. This avoids passing secret values as subprocess argv.
//
// File format follows the standard INI profile layout used by the AWS CLI.
// Permissions on the file are preserved when the file already exists; new
// files are created with mode 0600.
func writeAWSCredentialsKey(profileName, key, value string) error {
	const credentialsFileMode = 0600

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	credFile := home + "/.aws/credentials"

	// Ensure .aws directory exists.
	if mkErr := os.MkdirAll(home+"/.aws", 0700); mkErr != nil { //nolint:mnd // 0700 = owner rwx
		return fmt.Errorf("cannot create .aws directory: %w", mkErr)
	}

	// Read existing content (empty slice if file absent).
	existing, err := os.ReadFile(credFile) //nolint:gosec // path is user home directory
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot read credentials file: %w", err)
	}

	lines := strings.Split(string(existing), "\n")
	sectionHeader := "[" + profileName + "]"
	prefix := key + " = "

	inSection := false
	keyFound := false
	insertAfter := -1 // index of section header, used if key not found

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == sectionHeader {
			inSection = true
			insertAfter = i

			continue
		}

		if inSection {
			// New section starts — stop searching.
			if strings.HasPrefix(trimmed, "[") {
				break
			}

			if strings.HasPrefix(trimmed, key+" =") || strings.HasPrefix(trimmed, key+"=") {
				lines[i] = prefix + value
				keyFound = true

				break
			}

			// Track last line in section for insertion.
			if trimmed != "" {
				insertAfter = i
			}
		}
	}

	if !inSection {
		// Section not present — append it.
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}

		lines = append(lines, sectionHeader, prefix+value)
	} else if !keyFound {
		// Section exists but key missing — insert after last populated line.
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:insertAfter+1]...)
		newLines = append(newLines, prefix+value)
		newLines = append(newLines, lines[insertAfter+1:]...)
		lines = newLines
	}

	content := strings.Join(lines, "\n")

	if err := os.WriteFile(credFile, []byte(content), credentialsFileMode); err != nil { //nolint:gosec // path is user home directory
		return fmt.Errorf("cannot write credentials file: %w", err)
	}

	return nil
}

func validateAWSCredentials(profileName string, log *zap.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), VaultTimeoutSeconds*time.Second)
	defer cancel()

	// Validate profile name
	err := security.ValidateInput(profileName, validPathPattern)
	if err != nil {
		return fmt.Errorf("invalid profile name: %w", err)
	}

	cmd := exec.CommandContext(ctx, "aws", "sts", "get-caller-identity", "--profile", profileName) // #nosec G204 - input validated above

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		stderrStr := stderr.String()

		// Check if it's a network connectivity issue vs credential issue
		if strings.Contains(stderrStr, "Could not connect to the endpoint") {
			log.Warn("AWS profile configured but validation skipped due to network connectivity",
				zap.String("profile", profileName),
				zap.String("error", stderrStr))

			_, _ = fmt.Fprintf(os.Stdout, "Successfully configured AWS profile: %s\n", profileName)
			_, _ = fmt.Fprintf(os.Stdout, "\n⚠️  Warning: Could not validate credentials due to network connectivity.\n")
			_, _ = fmt.Fprintf(os.Stdout, "The profile has been configured with credentials from your config.\n")
			_, _ = fmt.Fprintf(os.Stdout, "\nTo use this profile, run commands with: --profile %s\n", profileName)
			_, _ = fmt.Fprintf(os.Stdout, "Or set environment variable: export AWS_PROFILE=%s\n", profileName)

			return nil
		}

		log.Error("Failed to validate AWS credentials", zap.Error(err), zap.String("stderr", stderrStr))

		return fmt.Errorf("AWS credential validation failed: %w", err)
	}

	log.Info("Successfully logged into AWS provider", zap.String("profile", profileName))

	_, _ = fmt.Fprintf(os.Stdout, "Successfully configured AWS profile: %s\n", profileName)
	_, _ = fmt.Fprintf(os.Stdout, "\nCaller Identity:\n%s\n", stdout.String())
	_, _ = fmt.Fprintf(os.Stdout, "\nTo use this profile, run commands with: --profile %s\n", profileName)
	_, _ = fmt.Fprintf(os.Stdout, "Or set environment variable: export AWS_PROFILE=%s\n", profileName)

	return nil
}

func loginOpenStack(log *zap.Logger) error {
	log.Warn("OpenStack provider login not implemented yet")

	_, _ = fmt.Fprintln(os.Stdout, "OpenStack provider login not implemented yet")
	_, _ = fmt.Fprintln(os.Stdout, "\nOpenStack authentication typically uses:")
	_, _ = fmt.Fprintln(os.Stdout, "  - OpenStack RC file: source openrc.sh")
	_, _ = fmt.Fprintln(os.Stdout, "  - Environment variables: OS_AUTH_URL, OS_USERNAME, OS_PASSWORD, etc.")

	return nil
}

func loginGCP(log *zap.Logger) error {
	log.Info("Setting up GCP authentication")

	// Check for service account credentials
	credPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if credPath == "" {
		resolved, err := resolveGCPCredPath(log)
		if err != nil {
			return err
		}

		credPath = resolved
	}

	if credPath != "" {
		// Set environment variable for GCP SDK
		_ = os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credPath)
		log.Info("Using service account credentials", zap.String("path", credPath))

		// Try to activate using gcloud if available
		_, err := exec.LookPath("gcloud")
		if err == nil {
			cmd := exec.CommandContext(context.Background(), "gcloud", "auth", "activate-service-account", "--key-file", credPath) //nolint:gosec // command args are from trusted config

			output, err := cmd.CombinedOutput()
			if err != nil {
				log.Warn("gcloud auth failed (may not be required)", zap.Error(err), zap.String("output", string(output)))
			} else {
				log.Info("Activated service account with gcloud")

				_, _ = fmt.Fprintln(os.Stdout, "GCP service account activated successfully")
			}
		}

		_, _ = fmt.Fprintln(os.Stdout, "GCP credentials configured")
		_, _ = fmt.Fprintf(os.Stdout, "  GOOGLE_APPLICATION_CREDENTIALS=%s\n", credPath) //nolint:gosec // output to stdout, not web context

		return nil
	}

	// No service account found, provide guidance
	_, _ = fmt.Fprintln(os.Stdout, "GCP authentication not configured")
	_, _ = fmt.Fprintln(os.Stdout, "\nTo configure GCP authentication:")
	_, _ = fmt.Fprintln(os.Stdout, "  1. Set GOOGLE_APPLICATION_CREDENTIALS to your service account key file")
	_, _ = fmt.Fprintln(os.Stdout, "  2. Or configure service_account_key_path in your OCFP config")
	_, _ = fmt.Fprintln(os.Stdout, "  3. Or run: gcloud auth application-default login")

	return nil
}

// resolveGCPCredPath resolves the GCP credential path from config.
func resolveGCPCredPath(log *zap.Logger) (string, error) {
	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return "", err //nolint:wrapcheck // error context handled by caller
	}

	if cfg == nil {
		return "", nil
	}

	if cfg.ServiceAccountKeyPath != "" {
		return cfg.ServiceAccountKeyPath, nil
	}

	if cfg.ServiceAccountJSON == "" {
		return "", nil
	}

	// Write inline JSON to temp file
	tmpFile, err := os.CreateTemp("", "gcp-credentials-*.json")
	if err != nil {
		return "", fmt.Errorf("failed to create temp credentials file: %w", err)
	}

	_, err = tmpFile.WriteString(cfg.ServiceAccountJSON)
	if err != nil {
		return "", fmt.Errorf("failed to write credentials file: %w", err)
	}

	_ = tmpFile.Close()
	log.Info("Created temporary credentials file", zap.String("path", tmpFile.Name()))

	return tmpFile.Name(), nil
}

func loginAzure(log *zap.Logger) error {
	log.Info("Setting up Azure authentication")

	if loginAzureFromServicePrincipal(log) {
		return nil
	}

	if loginAzureFromCLI(log) {
		return nil
	}

	if loginAzureFromManagedIdentity(log) {
		return nil
	}

	// No authentication found, provide guidance
	_, _ = fmt.Fprintln(os.Stdout, "Azure authentication not configured")
	_, _ = fmt.Fprintln(os.Stdout, "\nTo configure Azure authentication, choose one of:")
	_, _ = fmt.Fprintln(os.Stdout, "\n1. Service Principal (recommended for automation):")
	_, _ = fmt.Fprintln(os.Stdout, "   export AZURE_SUBSCRIPTION_ID=<subscription-id>")
	_, _ = fmt.Fprintln(os.Stdout, "   export AZURE_TENANT_ID=<tenant-id>")
	_, _ = fmt.Fprintln(os.Stdout, "   export AZURE_CLIENT_ID=<client-id>")
	_, _ = fmt.Fprintln(os.Stdout, "   export AZURE_CLIENT_SECRET=<client-secret>")
	_, _ = fmt.Fprintln(os.Stdout, "\n2. Azure CLI (for development):")
	_, _ = fmt.Fprintln(os.Stdout, "   az login")
	_, _ = fmt.Fprintln(os.Stdout, "\n3. Managed Identity (for Azure-hosted workloads):")
	_, _ = fmt.Fprintln(os.Stdout, "   export AZURE_USE_MANAGED_IDENTITY=true")

	return nil
}

// loginAzureFromServicePrincipal checks for service principal credentials in environment.
func loginAzureFromServicePrincipal(log *zap.Logger) bool {
	subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")
	clientID := os.Getenv("AZURE_CLIENT_ID")
	tenantID := os.Getenv("AZURE_TENANT_ID")
	clientSecret := os.Getenv("AZURE_CLIENT_SECRET")

	if subscriptionID == "" || clientID == "" || tenantID == "" || clientSecret == "" {
		return false
	}

	log.Info("Azure service principal credentials found in environment")

	_, _ = fmt.Fprintln(os.Stdout, "Azure credentials configured via environment variables:")
	_, _ = fmt.Fprintf(os.Stdout, "  AZURE_SUBSCRIPTION_ID=%s\n", subscriptionID)
	_, _ = fmt.Fprintf(os.Stdout, "  AZURE_TENANT_ID=%s\n", tenantID)
	_, _ = fmt.Fprintf(os.Stdout, "  AZURE_CLIENT_ID=%s\n", clientID)
	_, _ = fmt.Fprintln(os.Stdout, "  AZURE_CLIENT_SECRET=***")

	return true
}

// loginAzureFromCLI checks for Azure CLI authentication.
func loginAzureFromCLI(log *zap.Logger) bool {
	_, err := exec.LookPath("az")
	if err != nil {
		return false
	}

	log.Info("Azure CLI found, checking authentication status")

	ctx, cancel := context.WithTimeout(context.Background(), VaultTimeoutSeconds*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "az", "account", "show", "--output", "json")

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err == nil {
		log.Info("Already authenticated with Azure CLI")

		_, _ = fmt.Fprintln(os.Stdout, "Azure CLI authentication active")
		_, _ = fmt.Fprintf(os.Stdout, "\nAccount details:\n%s\n", stdout.String())

		return true
	}

	_, _ = fmt.Fprintln(os.Stdout, "Azure CLI is installed but not authenticated")
	_, _ = fmt.Fprintln(os.Stdout, "\nTo authenticate, run one of:")
	_, _ = fmt.Fprintln(os.Stdout, "  az login                        # Interactive browser login")
	_, _ = fmt.Fprintln(os.Stdout, "  az login --use-device-code      # Device code flow (for headless)")
	_, _ = fmt.Fprintln(os.Stdout, "  az login --service-principal -u <client-id> -p <secret> --tenant <tenant>")

	return true
}

// loginAzureFromManagedIdentity checks for Azure managed identity configuration.
func loginAzureFromManagedIdentity(log *zap.Logger) bool {
	useMI := os.Getenv("AZURE_USE_MANAGED_IDENTITY")
	if useMI != "true" && useMI != "1" {
		return false
	}

	log.Info("Managed identity mode enabled")

	_, _ = fmt.Fprintln(os.Stdout, "Azure Managed Identity authentication enabled")
	_, _ = fmt.Fprintln(os.Stdout, "  AZURE_USE_MANAGED_IDENTITY=true")

	if clientID := os.Getenv("AZURE_CLIENT_ID"); clientID != "" {
		_, _ = fmt.Fprintf(os.Stdout, "  AZURE_CLIENT_ID=%s (user-assigned)\n", clientID)
	} else {
		_, _ = fmt.Fprintln(os.Stdout, "  Using system-assigned managed identity")
	}

	return true
}
