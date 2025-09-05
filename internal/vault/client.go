package vault

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/api/auth/approle"
	"github.com/hashicorp/vault/api/auth/userpass"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"
)

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
	Password  string
	RoleID    string
	SecretID  string
	CACert    string
	TLSSkip   bool
}

// NewClient creates a new vault client with the specified configuration.
func NewClient(cfg *Config) (*Client, error) {
	log := logger.Get()

	// Create vault config
	vaultConfig := &api.Config{
		Address: cfg.Address,
	}

	// Configure TLS
	tlsConfig := &api.TLSConfig{
		Insecure: cfg.TLSSkip,
	}
	if cfg.CACert != "" {
		tlsConfig.CACert = cfg.CACert
	}

	if err := vaultConfig.ConfigureTLS(tlsConfig); err != nil {
		return nil, fmt.Errorf("failed to configure TLS: %w", err)
	}

	// Create client
	client, err := api.NewClient(vaultConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create vault client: %w", err)
	}

	// Set namespace if specified
	if cfg.Namespace != "" {
		client.SetNamespace(cfg.Namespace)
	}

	// Set token if provided directly
	if cfg.Token != "" {
		client.SetToken(cfg.Token)
	}

	vaultClient := &Client{
		client:    client,
		logical:   client.Logical(),
		namespace: cfg.Namespace,
		logger:    log,
	}

	// Authenticate if needed
	if cfg.Token == "" {
		err := vaultClient.authenticate(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to authenticate: %w", err)
		}
	}

	return vaultClient, nil
}

// NewClientFromEnv creates a vault client using environment variables.
func NewClientFromEnv() (*Client, error) {
	cfg := &Config{
		Address:   getEnvOrDefault("VAULT_ADDR", "https://127.0.0.1:8200"),
		Token:     os.Getenv("VAULT_TOKEN"),
		Namespace: os.Getenv("VAULT_NAMESPACE"),
		AuthType:  getEnvOrDefault("VAULT_AUTH_TYPE", "token"),
		Username:  os.Getenv("VAULT_USERNAME"),
		Password:  os.Getenv("VAULT_PASSWORD"),
		RoleID:    os.Getenv("VAULT_ROLE_ID"),
		SecretID:  os.Getenv("VAULT_SECRET_ID"),
		CACert:    os.Getenv("VAULT_CACERT"),
		TLSSkip:   os.Getenv("VAULT_SKIP_VERIFY") == "true",
	}

	return NewClient(cfg)
}

// NewClientFromConfig creates a vault client from OCFP config.
func NewClientFromConfig(ocfpCfg *config.Config) (*Client, error) {
	// Extract vault configuration from OCFP config
	// This would be expanded based on how vault config is stored in OCFP
	cfg := &Config{
		Address:   getEnvOrDefault("VAULT_ADDR", "https://127.0.0.1:8200"),
		Token:     os.Getenv("VAULT_TOKEN"),
		Namespace: os.Getenv("VAULT_NAMESPACE"),
		AuthType:  getEnvOrDefault("VAULT_AUTH_TYPE", "token"),
		TLSSkip:   os.Getenv("VAULT_SKIP_VERIFY") == "true",
	}

	return NewClient(cfg)
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
			return errors.New("token authentication requires VAULT_TOKEN to be set")
		}

		return nil
	default:
		return fmt.Errorf("unsupported auth type: %s", cfg.AuthType)
	}
}

// authenticateUserPass authenticates using username/password.
func (c *Client) authenticateUserPass(ctx context.Context, cfg *Config) error {
	if cfg.Username == "" || cfg.Password == "" {
		return errors.New("username and password required for userpass auth")
	}

	userpassAuth, err := userpass.NewUserpassAuth(cfg.Username, &userpass.Password{FromString: cfg.Password})
	if err != nil {
		return fmt.Errorf("failed to create userpass auth: %w", err)
	}

	authInfo, err := c.client.Auth().Login(ctx, userpassAuth)
	if err != nil {
		return fmt.Errorf("failed to login with userpass: %w", err)
	}

	if authInfo == nil || authInfo.Auth == nil {
		return errors.New("no auth info returned")
	}

	c.logger.Debug("Authenticated with userpass", "lease_duration", authInfo.Auth.LeaseDuration)

	return nil
}

// authenticateAppRole authenticates using AppRole.
func (c *Client) authenticateAppRole(ctx context.Context, cfg *Config) error {
	if cfg.RoleID == "" || cfg.SecretID == "" {
		return errors.New("role_id and secret_id required for approle auth")
	}

	approleAuth, err := approle.NewAppRoleAuth(cfg.RoleID, &approle.SecretID{FromString: cfg.SecretID})
	if err != nil {
		return fmt.Errorf("failed to create approle auth: %w", err)
	}

	authInfo, err := c.client.Auth().Login(ctx, approleAuth)
	if err != nil {
		return fmt.Errorf("failed to login with approle: %w", err)
	}

	if authInfo == nil || authInfo.Auth == nil {
		return errors.New("no auth info returned")
	}

	c.logger.Debug("Authenticated with approle", "lease_duration", authInfo.Auth.LeaseDuration)

	return nil
}

// ValidateConnection tests the vault connection and authentication.
func (c *Client) ValidateConnection() error {
	// Try to read own token info to validate connection
	secret, err := c.client.Auth().Token().LookupSelf()
	if err != nil {
		return fmt.Errorf("failed to validate connection: %w", err)
	}

	if secret == nil || secret.Data == nil {
		return errors.New("invalid token response")
	}

	c.logger.Debug("Vault connection validated", "token_policies", secret.Data["policies"])

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
