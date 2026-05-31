package pve

import (
	"context"
	"errors"
	"strings"
	"testing"

	ocfpconfig "github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// TestRegister_RegistersProvider verifies Register() installs the "pve" factory
// in the global registry. The global registry persists across tests in a
// single binary run, so a second call returns ErrProviderAlreadyRegistered —
// both outcomes prove the provider is present.
func TestRegister_RegistersProvider(t *testing.T) {
	err := Register()
	if err != nil {
		// Acceptable if already registered by a prior test run within the same binary.
		if !strings.Contains(err.Error(), "already registered") {
			t.Fatalf("Register() unexpected error: %v", err)
		}
	}

	factory, err := cpi.Get("pve")
	if err != nil {
		t.Fatalf("cpi.Get(\"pve\") returned error after Register(): %v", err)
	}

	if factory == nil {
		t.Fatal("cpi.Get(\"pve\") returned nil factory")
	}
}

// TestNewProvider_ValidConfig covers all minimal valid auth paths:
// API token (token_id + token_secret) and username/password.
func TestNewProvider_ValidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config *Config
	}{
		{
			name: "api token auth",
			config: &Config{
				Host:        "https://pve.example.com:8006",
				TokenID:     "root@pam!mytoken",
				TokenSecret: "secret-uuid",
				Node:        "pve1",
			},
		},
		{
			name: "username password auth",
			config: &Config{
				Host:     "https://pve.example.com:8006",
				Username: "root",
				Password: "s3cr3t",
				Node:     "pve1",
			},
		},
		{
			name: "api token auth without node",
			config: &Config{
				Host:        "https://pve.example.com:8006",
				TokenID:     "root@pam!ci",
				TokenSecret: "tok-secret",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewProvider(tt.config)
			if err != nil {
				t.Fatalf("NewProvider(%q) unexpected error: %v", tt.name, err)
			}

			if provider == nil {
				t.Fatal("NewProvider returned nil provider")
			}
		})
	}
}

// TestNewProvider_ValidMapConfig verifies the map[string]interface{} path.
func TestNewProvider_ValidMapConfig(t *testing.T) {
	t.Parallel()

	config := map[string]interface{}{
		"host":         "https://pve.example.com:8006",
		"token_id":     "root@pam!mytoken",
		"token_secret": "secret-uuid",
		"node":         "pve1",
	}

	provider, err := NewProvider(config)
	if err != nil {
		t.Fatalf("NewProvider(map) unexpected error: %v", err)
	}

	if provider == nil {
		t.Fatal("NewProvider(map) returned nil provider")
	}
}

// TestNewProvider_NilConfig verifies nil config returns an uninitialized
// client without error (deferred initialization via Initialize).
func TestNewProvider_NilConfig(t *testing.T) {
	t.Parallel()

	provider, err := NewProvider(nil)
	if err != nil {
		t.Fatalf("NewProvider(nil) unexpected error: %v", err)
	}

	if provider == nil {
		t.Fatal("NewProvider(nil) returned nil provider")
	}
}

// TestNewProvider_MissingHost verifies that an empty Host field causes an error
// that references "host".
func TestNewProvider_MissingHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config interface{}
	}{
		{
			name: "struct empty host",
			config: &Config{
				TokenID:     "root@pam!tok",
				TokenSecret: "secret",
			},
		},
		{
			name: "map empty host",
			config: map[string]interface{}{
				"token_id":     "root@pam!tok",
				"token_secret": "secret",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewProvider(tt.config)
			if err == nil {
				t.Fatal("expected error for missing host, got nil")
			}

			if !errors.Is(err, ErrHostRequired) && !strings.Contains(strings.ToLower(err.Error()), "host") {
				t.Errorf("expected error mentioning 'host', got: %v", err)
			}
		})
	}
}

// TestNewProvider_MissingAuth verifies that a valid host with no auth credentials
// causes an error referencing "token" or "auth".
func TestNewProvider_MissingAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config interface{}
	}{
		{
			name: "struct no auth",
			config: &Config{
				Host: "https://pve.example.com:8006",
			},
		},
		{
			name: "struct partial token — id only",
			config: &Config{
				Host:    "https://pve.example.com:8006",
				TokenID: "root@pam!tok",
				// TokenSecret deliberately empty
			},
		},
		{
			name: "struct partial token — secret only",
			config: &Config{
				Host:        "https://pve.example.com:8006",
				TokenSecret: "secret",
				// TokenID deliberately empty
			},
		},
		{
			name: "map no auth",
			config: map[string]interface{}{
				"host": "https://pve.example.com:8006",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewProvider(tt.config)
			if err == nil {
				t.Fatal("expected error for missing auth, got nil")
			}

			if !errors.Is(err, ErrAPITokenRequired) &&
				!strings.Contains(strings.ToLower(err.Error()), "token") &&
				!strings.Contains(strings.ToLower(err.Error()), "auth") {
				t.Errorf("expected error mentioning 'token' or 'auth', got: %v", err)
			}
		})
	}
}

// TestNewProvider_InvalidConfigType verifies that an unsupported config type
// returns an error.
func TestNewProvider_InvalidConfigType(t *testing.T) {
	t.Parallel()

	_, err := NewProvider("not-a-valid-config")
	if err == nil {
		t.Fatal("expected error for invalid config type, got nil")
	}
}

// TestProvider_Name verifies that a created provider returns "pve" from Name().
func TestProvider_Name(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config interface{}
	}{
		{
			name:   "nil config",
			config: nil,
		},
		{
			name: "valid struct config",
			config: &Config{
				Host:        "https://pve.example.com:8006",
				TokenID:     "root@pam!tok",
				TokenSecret: "secret",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewProvider(tt.config)
			if err != nil {
				t.Fatalf("NewProvider(%q) unexpected error: %v", tt.name, err)
			}

			if provider == nil {
				t.Fatal("NewProvider returned nil provider")
			}

			if provider.Name() != "pve" {
				t.Errorf("Name() = %q, want %q", provider.Name(), "pve")
			}
		})
	}
}

// TestGetString_MapHelper covers the getString helper used during map config parsing.
func TestGetString_MapHelper(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		expected string
	}{
		{
			name:     "existing string value",
			m:        map[string]interface{}{"host": "https://pve.example.com:8006"},
			key:      "host",
			expected: "https://pve.example.com:8006",
		},
		{
			name:     "missing key returns empty string",
			m:        map[string]interface{}{},
			key:      "host",
			expected: "",
		},
		{
			name:     "non-string value returns empty string",
			m:        map[string]interface{}{"port": 8006},
			key:      "port",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getString(tt.m, tt.key)
			if got != tt.expected {
				t.Errorf("getString(%q) = %q, want %q", tt.key, got, tt.expected)
			}
		})
	}
}

// TestParsePVEConfig_AuthModes exercises parsePVEConfig on *ocfpconfig.Config for
// all auth combinations: API token mode, user/pass mode, mixed/partial mode, and
// neither. It tests the disambiguation logic that was previously aliasing Password
// as TokenSecret unconditionally.
func TestParsePVEConfig_AuthModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         *ocfpconfig.Config
		wantErr     bool
		errIs       error
		wantTokenID string
		wantSecret  string
		wantUser    string
		wantPass    string
	}{
		{
			name: "api token mode — both AuthToken and TokenSecret set",
			cfg: &ocfpconfig.Config{
				APIEndpoint: "https://pve.example.com:8006",
				AuthToken:   "root@pam!ci",
				TokenSecret: "tok-secret-uuid",
			},
			wantTokenID: "root@pam!ci",
			wantSecret:  "tok-secret-uuid",
			wantUser:    "",
			wantPass:    "",
		},
		{
			name: "user/pass mode — Username and Password, no AuthToken",
			cfg: &ocfpconfig.Config{
				APIEndpoint: "https://pve.example.com:8006",
				Username:    "root",
				Password:    "s3cr3t",
			},
			wantTokenID: "",
			wantSecret:  "",
			wantUser:    "root",
			wantPass:    "s3cr3t",
		},
		{
			name: "neither mode — all auth fields empty",
			cfg: &ocfpconfig.Config{
				APIEndpoint: "https://pve.example.com:8006",
			},
			wantTokenID: "",
			wantSecret:  "",
			wantUser:    "",
			wantPass:    "",
		},
		{
			name: "mixed mode — both API token and user/pass fully set",
			cfg: &ocfpconfig.Config{
				APIEndpoint: "https://pve.example.com:8006",
				AuthToken:   "root@pam!ci",
				TokenSecret: "tok-secret-uuid",
				Username:    "root",
				Password:    "s3cr3t",
			},
			wantErr: true,
			errIs:   ErrMixedAuthConfig,
		},
		{
			name: "partial token — AuthToken set but TokenSecret empty (old alias pattern)",
			cfg: &ocfpconfig.Config{
				APIEndpoint: "https://pve.example.com:8006",
				AuthToken:   "root@pam!ci",
				// TokenSecret empty, Password set — old wrong alias usage
				Password: "old-aliased-password",
			},
			wantErr: true,
			errIs:   ErrMixedAuthConfig,
		},
		{
			name: "partial token — TokenSecret set but AuthToken empty",
			cfg: &ocfpconfig.Config{
				APIEndpoint: "https://pve.example.com:8006",
				TokenSecret: "tok-secret-uuid",
			},
			wantErr: true,
			errIs:   ErrMixedAuthConfig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, _ := NewClient(nil)

			got, err := c.parsePVEConfig(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.errIs != nil && !errors.Is(err, tt.errIs) {
					t.Errorf("expected errors.Is(err, %v), got: %v", tt.errIs, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got == nil {
				t.Fatal("parsePVEConfig returned nil config without error")
			}

			if got.TokenID != tt.wantTokenID {
				t.Errorf("TokenID = %q, want %q", got.TokenID, tt.wantTokenID)
			}

			if got.TokenSecret != tt.wantSecret {
				t.Errorf("TokenSecret = %q, want %q", got.TokenSecret, tt.wantSecret)
			}

			if got.Username != tt.wantUser {
				t.Errorf("Username = %q, want %q", got.Username, tt.wantUser)
			}

			if got.Password != tt.wantPass {
				t.Errorf("Password = %q, want %q", got.Password, tt.wantPass)
			}

			// Verify Password is never aliased as TokenSecret in API token mode.
			if tt.wantTokenID != "" && got.Password != "" {
				t.Errorf("Password must be empty in API token mode, got %q", got.Password)
			}
		})
	}
}

// TestParsePVEConfig_MixedMode_ViaInitialize confirms that Initialize returns an
// error for mixed-mode configs (both API token and user/pass set). This exercises
// the production call path (Initialize → parsePVEConfig).
func TestParsePVEConfig_MixedMode_ViaInitialize(t *testing.T) {
	t.Parallel()

	cfg := &ocfpconfig.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!ci",
		TokenSecret: "tok-secret-uuid",
		Username:    "root",
		Password:    "s3cr3t",
	}

	c, _ := NewClient(nil)
	err := c.Initialize(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for mixed auth config, got nil")
	}

	if !errors.Is(err, ErrMixedAuthConfig) {
		t.Errorf("expected ErrMixedAuthConfig, got: %v", err)
	}
}

// TestGetBool_MapHelper covers the getBool helper used during map config parsing.
func TestGetBool_MapHelper(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		expected bool
	}{
		{
			name:     "existing true",
			m:        map[string]interface{}{"verify_ssl": true},
			key:      "verify_ssl",
			expected: true,
		},
		{
			name:     "existing false",
			m:        map[string]interface{}{"verify_ssl": false},
			key:      "verify_ssl",
			expected: false,
		},
		{
			name:     "missing key returns false",
			m:        map[string]interface{}{},
			key:      "verify_ssl",
			expected: false,
		},
		{
			name:     "non-bool value returns false",
			m:        map[string]interface{}{"verify_ssl": "true"},
			key:      "verify_ssl",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getBool(tt.m, tt.key)
			if got != tt.expected {
				t.Errorf("getBool(%q) = %v, want %v", tt.key, got, tt.expected)
			}
		})
	}
}
