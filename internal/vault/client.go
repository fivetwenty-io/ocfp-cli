package vault

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/api/auth/approle"
	"github.com/hashicorp/vault/api/auth/userpass"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"
)

// envValueTrue is the string representation of true for environment variable comparisons.
const envValueTrue = "true"

// Client wraps the HashiCorp Vault API client with OCFP-specific functionality.
type Client struct {
	client    *api.Client
	logical   *api.Logical
	namespace string
	logger    *zap.SugaredLogger
}

// Config holds vault client configuration.
type Config struct {
	Address   string
	Token     string
	Namespace string
	AuthType  string
	Username  string
	Password  string //nolint:gosec // field name is descriptive, not a hardcoded secret
	RoleID    string
	SecretID  string
	CACert    string
	TLSSkip   bool
}

// NewClient creates a new vault client with the specified configuration.
func NewClient(cfg *Config) (*Client, error) {
	vaultConfig := createVaultAPIConfig(cfg)

	client, err := createAndConfigureVaultClient(vaultConfig, cfg)
	if err != nil {
		return nil, err
	}

	vaultClient := buildVaultClient(client, cfg)

	if cfg.Token == "" {
		err := vaultClient.authenticate(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to authenticate: %w", err)
		}
	}

	return vaultClient, nil
}

func createVaultAPIConfig(cfg *Config) *api.Config {
	return &api.Config{
		Address:          cfg.Address,
		AgentAddress:     "",
		HttpClient:       nil,
		MinRetryWait:     time.Duration(0),
		MaxRetryWait:     time.Duration(0),
		MaxRetries:       0,
		Timeout:          time.Duration(0),
		Error:            nil,
		Backoff:          nil,
		CheckRetry:       nil,
		Logger:           nil,
		Limiter:          nil,
		OutputCurlString: false,
		OutputPolicy:     false,
		SRVLookup:        false,
		CloneHeaders:     false,
		CloneToken:       false,
		CloneTLSConfig:   false,
		ReadYourWrites:   false,
		DisableRedirects: false,
	}
}

func createAndConfigureVaultClient(vaultConfig *api.Config, cfg *Config) (*api.Client, error) {
	tlsConfig := &api.TLSConfig{
		Insecure:      cfg.TLSSkip,
		CACert:        "",
		CACertBytes:   nil,
		CAPath:        "",
		ClientCert:    "",
		ClientKey:     "",
		TLSServerName: "",
	}
	if cfg.CACert != "" {
		tlsConfig.CACert = cfg.CACert
	}

	err := vaultConfig.ConfigureTLS(tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to configure TLS: %w", err)
	}

	client, err := api.NewClient(vaultConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create vault client: %w", err)
	}

	if cfg.Namespace != "" {
		client.SetNamespace(cfg.Namespace)
	}

	if cfg.Token != "" {
		client.SetToken(cfg.Token)
	}

	return client, nil
}

func buildVaultClient(client *api.Client, cfg *Config) *Client {
	log := logger.Get()

	return &Client{
		client:    client,
		logical:   client.Logical(),
		namespace: cfg.Namespace,
		logger:    log,
	}
}

// NewClientFromEnv creates a vault client using environment variables.
// If VAULT_TOKEN is not set, it will try to read from ~/.saferc.
func NewClientFromEnv() (*Client, error) {
	// Try to get token and address from environment first
	token := os.Getenv("VAULT_TOKEN")
	address := os.Getenv("VAULT_ADDR")

	// Default skip_verify from environment
	skipVerify := os.Getenv("VAULT_SKIP_VERIFY") == envValueTrue

	// If token is not in environment, try reading from ~/.saferc
	if token == "" {
		safeAddr, safeToken, safeSkipVerify, err := readSafeConfig()
		if err == nil {
			token = safeToken
			skipVerify = safeSkipVerify
			// Also use safe's vault address if VAULT_ADDR not set
			if address == "" {
				address = safeAddr
			}
		}
	}

	// Default to localhost if still not set
	if address == "" {
		address = "https://127.0.0.1:8200"
	}

	cfg := &Config{
		Address:   address,
		Token:     token,
		Namespace: os.Getenv("VAULT_NAMESPACE"),
		AuthType:  getEnvOrDefault("VAULT_AUTH_TYPE", "token"),
		Username:  os.Getenv("VAULT_USERNAME"),
		Password:  os.Getenv("VAULT_PASSWORD"),
		RoleID:    os.Getenv("VAULT_ROLE_ID"),
		SecretID:  os.Getenv("VAULT_SECRET_ID"),
		CACert:    os.Getenv("VAULT_CACERT"),
		TLSSkip:   skipVerify,
	}

	return NewClient(cfg)
}

// NewClientFromConfig creates a vault client from OCFP config.
// If VAULT_TOKEN is not set, it will try to read from ~/.saferc.
func NewClientFromConfig(_ocfpCfg *config.Config) (*Client, error) {
	// Try to get token and address from environment first
	token := os.Getenv("VAULT_TOKEN")
	address := os.Getenv("VAULT_ADDR")

	// Default skip_verify from environment
	skipVerify := os.Getenv("VAULT_SKIP_VERIFY") == envValueTrue

	// If token is not in environment, try reading from ~/.saferc
	if token == "" {
		safeAddr, safeToken, safeSkipVerify, err := readSafeConfig()
		if err == nil {
			token = safeToken
			skipVerify = safeSkipVerify
			// Also use safe's vault address if VAULT_ADDR not set
			if address == "" {
				address = safeAddr
			}
		}
	}

	// Default to localhost if still not set
	if address == "" {
		address = "https://127.0.0.1:8200"
	}

	cfg := &Config{
		Address:   address,
		Token:     token,
		Namespace: os.Getenv("VAULT_NAMESPACE"),
		AuthType:  getEnvOrDefault("VAULT_AUTH_TYPE", "token"),
		Username:  "",
		Password:  "",
		RoleID:    "",
		SecretID:  "",
		CACert:    "",
		TLSSkip:   skipVerify,
	}

	return NewClient(cfg)
}

// ValidateConnection tests the vault connection and authentication.
func (c *Client) ValidateConnection() error {
	// Try to read own token info to validate connection
	secret, err := c.client.Auth().Token().LookupSelf()
	if err != nil {
		return fmt.Errorf("failed to validate connection: %w", err)
	}

	if secret == nil || secret.Data == nil {
		return ErrInvalidTokenResponse
	}

	c.logger.Debugw("Vault connection validated", "token_policies", secret.Data["policies"])

	return nil
}

// Close cleans up the client connection.
func (c *Client) Close() error {
	// Nothing to close for the HTTP client
	return nil
}

// GetClient returns the underlying vault client for advanced operations.
func (c *Client) GetClient() *api.Client {
	return c.client
}

// GetLogical returns the logical client for secret operations.
func (c *Client) GetLogical() *api.Logical {
	return c.logical
}

// getEnvOrDefault returns environment variable value or default.
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return defaultValue
}

// authenticate handles various authentication methods.
func (c *Client) authenticate(cfg *Config) error {
	ctx := context.Background()

	switch strings.ToLower(cfg.AuthType) {
	case "userpass":
		return c.authenticateUserPass(ctx, cfg)
	case "approle":
		return c.authenticateAppRole(ctx, cfg)
	case "token":
		// Token should already be set
		if cfg.Token == "" {
			return ErrTokenAuthRequiresVaultToken
		}

		return nil
	default:
		return ErrUnsupportedAuthType(cfg.AuthType)
	}
}

// authenticateUserPass authenticates using username/password.
func (c *Client) authenticateUserPass(ctx context.Context, cfg *Config) error {
	if cfg.Username == "" || cfg.Password == "" {
		return ErrUsernameAndPasswordRequiredForUserpass
	}

	userpassAuth, err := userpass.NewUserpassAuth(cfg.Username, &userpass.Password{
		FromString: cfg.Password,
		FromFile:   "",
		FromEnv:    "",
	})
	if err != nil {
		return fmt.Errorf("failed to create userpass auth: %w", err)
	}

	authInfo, err := c.client.Auth().Login(ctx, userpassAuth)
	if err != nil {
		return fmt.Errorf("failed to login with userpass: %w", err)
	}

	if authInfo == nil || authInfo.Auth == nil {
		return ErrNoAuthInfoReturned
	}

	c.logger.Debugw("Authenticated with userpass", "lease_duration", authInfo.Auth.LeaseDuration)

	return nil
}

// authenticateAppRole authenticates using AppRole.
func (c *Client) authenticateAppRole(ctx context.Context, cfg *Config) error {
	if cfg.RoleID == "" || cfg.SecretID == "" {
		return ErrRoleIDAndSecretIDRequiredForApprole
	}

	approleAuth, err := approle.NewAppRoleAuth(cfg.RoleID, &approle.SecretID{
		FromString: cfg.SecretID,
		FromFile:   "",
		FromEnv:    "",
	})
	if err != nil {
		return fmt.Errorf("failed to create approle auth: %w", err)
	}

	authInfo, err := c.client.Auth().Login(ctx, approleAuth)
	if err != nil {
		return fmt.Errorf("failed to login with approle: %w", err)
	}

	if authInfo == nil || authInfo.Auth == nil {
		return ErrNoAuthInfoReturned
	}

	c.logger.Debugw("Authenticated with approle", "lease_duration", authInfo.Auth.LeaseDuration)

	return nil
}

// safeConfig represents the structure of ~/.saferc file.
type safeConfig struct {
	Version string `yaml:"version"`
	Current string `yaml:"current"`
	Vaults  map[string]struct {
		URL        string `yaml:"url"`
		Token      string `yaml:"token"`
		SkipVerify bool   `yaml:"skip_verify"`
	} `yaml:"vaults"`
}

// readSafeConfig reads the ~/.saferc file and returns the vault address, token, and skip_verify.
// Returns the URL, token, and skip_verify for the current vault target.
func readSafeConfig() (string, string, bool, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", "", false, fmt.Errorf("failed to get home directory: %w", err)
	}

	// Sanitize and validate the path to prevent directory traversal
	safeRcPath := filepath.Clean(filepath.Join(homeDir, ".saferc"))

	// Ensure the resolved path is still within the user's home directory
	cleanHomeDir := filepath.Clean(homeDir)
	if !strings.HasPrefix(safeRcPath, cleanHomeDir) {
		return "", "", false, ErrSafercMustBeInHomeDirectory
	}

	data, err := os.ReadFile(safeRcPath)
	if err != nil {
		return "", "", false, fmt.Errorf("failed to read ~/.saferc: %w", err)
	}

	var cfg safeConfig

	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return "", "", false, fmt.Errorf("failed to parse ~/.saferc: %w", err)
	}

	// Get the current vault
	if cfg.Current == "" {
		return "", "", false, ErrNoCurrentVaultSet()
	}

	vault, ok := cfg.Vaults[cfg.Current]
	if !ok {
		return "", "", false, ErrVaultNotFoundInSaferc(cfg.Current)
	}

	if vault.Token == "" {
		return "", "", false, ErrNoTokenFoundForVault(cfg.Current)
	}

	return vault.URL, vault.Token, vault.SkipVerify, nil
}
