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
	"github.com/ocfp/ocfp-cli-go/internal/security"
)

const (
	// Command timeout constants for provider operations.
	VaultTimeoutSeconds   = 10
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
		Args: cobra.MinimumNArgs(1),
		RunE: runProviderCmd,
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
		blocName, _ := cmd.Flags().GetString("bloc")
		if blocName == "" {
			// fallback to global viper value (supports --bloc-name alias)
			blocName = viper.GetString("bloc_name")
		}

		if blocName == "" {
			blocName = os.Getenv("OCFP_BLOC_NAME")
		}

		cfg, err := config.LoadWithParams("", blocName)
		if err == nil && cfg.Provider != "" {
			providerName = cfg.Provider
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
		return loginAWS(log)
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

// STACKIT Login Implementation.
func loginSTACKIT(cmd *cobra.Command, log *zap.Logger) error {
	blocName, _ := cmd.Flags().GetString("bloc")
	if blocName == "" {
		blocName = os.Getenv("OCFP_BLOC_NAME")
	}

	if blocName == "" {
		return ErrBlocFlagOrEnvVarRequired
	}

	// Get credentials (either JSON or token)
	authType, credentials, err := getSTACKITCredentials(blocName, log)
	if err != nil {
		return fmt.Errorf("could not retrieve STACKIT service account credentials: %w", err)
	}

	if credentials == "" {
		return ErrCouldNotRetrieveStackitCredentials
	}

	if authType == authTypeToken {
		return authenticateSTACKITToken(credentials, log)
	}

	return authenticateSTACKIT(credentials, log)
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

	defer func() { _ = os.Remove(tempFile.Name()) }()

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

	cmd := exec.CommandContext(ctx, "stackit", "auth", "activate-service-account", "--service-account-key-path", tempFile.Name()) // #nosec G204 - path is validated above

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
	// Execute stackit auth command with token
	log.Info("Authenticating with STACKIT token...")
	log.Debug("Executing stackit auth activate-service-account with token")

	ctx, cancel := context.WithTimeout(context.Background(), StackitTimeoutSeconds*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "stackit", "auth", "activate-service-account", "--service-account-token", serviceAccountToken)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
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

// Other Provider Login Implementations (Placeholder).
func loginAWS(log *zap.Logger) error {
	log.Warn("AWS provider login not implemented yet")
	log.Info("AWS authentication typically uses:")
	log.Info("  - AWS CLI profiles: aws configure")
	log.Info("  - Environment variables: AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY")
	log.Info("  - IAM roles for EC2 instances")

	return nil
}

func loginOpenStack(log *zap.Logger) error {
	log.Warn("OpenStack provider login not implemented yet")
	log.Info("OpenStack authentication typically uses:")
	log.Info("  - OpenStack RC file: source openrc.sh")
	log.Info("  - Environment variables: OS_AUTH_URL, OS_USERNAME, OS_PASSWORD, etc.")

	return nil
}

func loginGCP(log *zap.Logger) error {
	log.Warn("GCP provider login not implemented yet")
	log.Info("GCP authentication typically uses:")
	log.Info("  - gcloud auth login")
	log.Info("  - Service account key files")
	log.Info("  - Application default credentials")

	return nil
}

func loginAzure(log *zap.Logger) error {
	log.Warn("Azure provider login not implemented yet")
	log.Info("Azure authentication typically uses:")
	log.Info("  - az login")
	log.Info("  - Service principal credentials")
	log.Info("  - Managed identities")

	return nil
}
