package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPVEConfig returns a fully populated PVE config for use in tests.
// Uses API token auth: AuthToken = token_id, TokenSecret = token_secret.
// Password is NOT set because it is reserved for username/password auth mode only.
func newPVEConfig() *config.Config {
	return &config.Config{
		Name:        "my-bloc",
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!mytoken",
		TokenSecret: "tok-secret-uuid",
		Region:      "pve-node-01",
		BastionIP:   "10.0.1.50",
		Bastion: config.Bastion{
			SSHUser: "ubuntu",
			Genesis: config.Genesis{
				Enabled: true,
			},
		},
	}
}

// TestPVEBastionInit_Validate_MissingHost verifies that an empty APIEndpoint returns
// ErrPVEHostRequired.
func TestPVEBastionInit_Validate_MissingHost(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:        "my-bloc",
		AuthToken:   "root@pam!mytoken",
		TokenSecret: "tok-secret-uuid",
	}

	provider := NewPVEBastionInit(cfg)

	err := provider.Validate()
	require.Error(t, err, "expected error for empty APIEndpoint, got nil")
	require.ErrorIs(t, err, ErrPVEHostRequired)
}

// TestPVEBastionInit_Validate_MissingAuth verifies that when no auth credentials
// are supplied but a host is present, Validate returns ErrPVEAuthRequired.
// This mirrors the AWS fail-fast pattern (ErrAWSAccessKeyRequired / ErrAWSSecretKeyRequired).
func TestPVEBastionInit_Validate_MissingAuth(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:        "my-bloc",
		APIEndpoint: "https://pve.example.com:8006",
		// AuthToken, Username, and Password all empty — must fail
	}

	provider := NewPVEBastionInit(cfg)

	err := provider.Validate()
	require.Error(t, err, "expected ErrPVEAuthRequired for missing auth, got nil")
	require.ErrorIs(t, err, ErrPVEAuthRequired)
}

// TestPVEBastionInit_Validate_Valid verifies that a fully configured provider passes
// validation without error.
func TestPVEBastionInit_Validate_Valid(t *testing.T) {
	t.Parallel()

	provider := NewPVEBastionInit(newPVEConfig())

	require.NoError(t, provider.Validate(), "expected no error for valid config")
}

// TestPVEBastionInit_PrepareEnvironment verifies the environment map returned by
// PrepareEnvironment contains required keys and does NOT include the deprecated
// GENESIS_ENV bare key.
func TestPVEBastionInit_PrepareEnvironment(t *testing.T) {
	t.Parallel()

	provider := NewPVEBastionInit(newPVEConfig())
	env := provider.PrepareEnvironment()

	// OCFP_PROVIDER must be "pve"
	got, ok := env["OCFP_PROVIDER"]
	require.True(t, ok, "OCFP_PROVIDER key missing from PrepareEnvironment result")
	assert.Equal(t, "pve", got, "OCFP_PROVIDER must equal pve")

	// GENESIS_ENVIRONMENT must be set to the bloc name (Genesis v3.2+ requirement)
	got, ok = env["GENESIS_ENVIRONMENT"]
	require.True(t, ok, "GENESIS_ENVIRONMENT key missing from PrepareEnvironment result")
	assert.Equal(t, "my-bloc", got, "GENESIS_ENVIRONMENT must equal my-bloc")

	// GENESIS_ENV (bare / deprecated) must NOT be present
	_, ok = env["GENESIS_ENV"]
	assert.False(t, ok, "deprecated GENESIS_ENV key must not be present in PrepareEnvironment result")
}

// TestPVEBastionInit_PrepareEnvironment_GenesisDisabled verifies that when Genesis
// is disabled, GENESIS_SKIP_INSTALL is set and GENESIS_ENVIRONMENT is still present.
func TestPVEBastionInit_PrepareEnvironment_GenesisDisabled(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:        "my-bloc",
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!mytoken",
		TokenSecret: "tok-secret-uuid",
		Bastion: config.Bastion{
			Genesis: config.Genesis{
				Enabled: false,
			},
		},
	}

	provider := NewPVEBastionInit(cfg)
	env := provider.PrepareEnvironment()

	got, ok := env["GENESIS_ENVIRONMENT"]
	require.True(t, ok, "GENESIS_ENVIRONMENT key missing when Genesis disabled")
	assert.Equal(t, "my-bloc", got, "GENESIS_ENVIRONMENT must equal my-bloc")

	got, ok = env["GENESIS_SKIP_INSTALL"]
	require.True(t, ok, "GENESIS_SKIP_INSTALL missing when Genesis disabled")
	assert.Equal(t, "1", got, "GENESIS_SKIP_INSTALL must equal 1")

	_, ok = env["GENESIS_ENV"]
	assert.False(t, ok, "deprecated GENESIS_ENV key must not be present")
}

// TestPVEBastionInit_GetConnectionDetails verifies that GetConnectionDetails returns
// the expected user, host, and port from config when BastionIP is pre-configured
// (short-circuits all fallback strategies).
func TestPVEBastionInit_GetConnectionDetails(t *testing.T) {
	t.Parallel()

	cfg := newPVEConfig()
	// BastionIP set in newPVEConfig() — getBastionIP uses strategy 1 (config field).
	// SSHUser set to "ubuntu" in newPVEConfig().
	// SSH key lookup will fail since no real key exists; we only test that the host/port
	// values returned are derived from config, not that a key is present.
	// GetConnectionDetails calls findSSHPrivateKey which may fail — so we exercise the
	// error path and confirm it wraps from the correct cause, not from getBastionIP.

	provider := NewPVEBastionInit(cfg)

	details, err := provider.GetConnectionDetails(context.Background())
	if err != nil {
		// Expected when no SSH key exists in test environment.
		// Confirm the error is NOT from IP resolution — i.e. bastionIP was found.
		assert.False(t, errors.Is(err, ErrCouldNotDetermineBastionIP),
			"GetConnectionDetails failed on IP lookup despite BastionIP being set: %v", err)
		// SSH key not found is acceptable in a unit test environment.
		t.Logf("GetConnectionDetails returned expected key-lookup error: %v", err)

		return
	}

	assert.Equal(t, "10.0.1.50", details.Host)
	assert.Equal(t, defaultSSHPort, details.Port)
	assert.Equal(t, "ubuntu", details.User)
}

// TestPVEBastionInit_GetConnectionDetails_BastionIPFromConfig verifies that when
// BastionIP is explicitly set, getBastionIP resolves it without any external calls.
func TestPVEBastionInit_GetConnectionDetails_BastionIPFromConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:        "prod-bloc",
		APIEndpoint: "https://pve.prod.example.com:8006",
		BastionIP:   "192.168.10.5",
		Bastion: config.Bastion{
			SSHUser: "ubuntu",
		},
	}

	provider := NewPVEBastionInit(cfg)

	ip, err := provider.getBastionIP()
	require.NoError(t, err, "expected no error for BastionIP in config")
	assert.Equal(t, "192.168.10.5", ip)
}

// TestPVEBastionInit_GetConnectionDetails_NoBastionIP verifies that when no IP is
// available from any strategy, getBastionIP returns ErrCouldNotDetermineBastionIP.
func TestPVEBastionInit_GetConnectionDetails_NoBastionIP(t *testing.T) {
	t.Parallel()

	// No BastionIP, no env var, no state dir, no real API available.
	cfg := &config.Config{
		Name:        "no-ip-bloc",
		APIEndpoint: "https://pve.example.com:8006",
	}

	provider := NewPVEBastionInit(cfg)

	_, err := provider.getBastionIP()
	require.Error(t, err, "expected error when no bastion IP available, got nil")
	require.ErrorIs(t, err, ErrCouldNotDetermineBastionIP)
}

// TestPVEBastionInit_PrepareEnvironment_APITokenMode verifies that API token auth
// emits PVE_TOKEN_ID and PVE_TOKEN_SECRET and does NOT emit PVE_USERNAME or
// PVE_PASSWORD_BASE64.
func TestPVEBastionInit_PrepareEnvironment_APITokenMode(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:        "my-bloc",
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!ci",
		TokenSecret: "tok-secret-uuid",
	}

	provider := NewPVEBastionInit(cfg)
	env := provider.PrepareEnvironment()

	got, ok := env["PVE_TOKEN_ID"]
	require.True(t, ok, "PVE_TOKEN_ID missing for API token auth")
	assert.Equal(t, "root@pam!ci", got, "PVE_TOKEN_ID must equal root@pam!ci")

	got, ok = env["PVE_TOKEN_SECRET"]
	require.True(t, ok, "PVE_TOKEN_SECRET missing for API token auth")
	assert.Equal(t, "tok-secret-uuid", got, "PVE_TOKEN_SECRET must equal tok-secret-uuid")

	_, ok = env["PVE_USERNAME"]
	assert.False(t, ok, "PVE_USERNAME must not be present in API token auth mode")

	_, ok = env["PVE_PASSWORD_BASE64"]
	assert.False(t, ok, "PVE_PASSWORD_BASE64 must not be present in API token auth mode")
}

// TestPVEBastionInit_PrepareEnvironment_UserPassMode verifies that username/password
// auth emits PVE_USERNAME and PVE_PASSWORD_BASE64 and does NOT emit PVE_TOKEN_ID or
// PVE_TOKEN_SECRET.
func TestPVEBastionInit_PrepareEnvironment_UserPassMode(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:        "my-bloc",
		APIEndpoint: "https://pve.example.com:8006",
		Username:    "root",
		Password:    "s3cr3t",
	}

	provider := NewPVEBastionInit(cfg)
	env := provider.PrepareEnvironment()

	_, ok := env["PVE_USERNAME"]
	assert.True(t, ok, "PVE_USERNAME missing for username/password auth mode")

	_, ok = env["PVE_PASSWORD_BASE64"]
	assert.True(t, ok, "PVE_PASSWORD_BASE64 missing for username/password auth mode")

	_, ok = env["PVE_TOKEN_ID"]
	assert.False(t, ok, "PVE_TOKEN_ID must not be present in username/password auth mode")

	_, ok = env["PVE_TOKEN_SECRET"]
	assert.False(t, ok, "PVE_TOKEN_SECRET must not be present in username/password auth mode")
}

// TestPVEBastionInit_Validate_APITokenMode verifies that AuthToken + TokenSecret
// (without Password) passes Validate.
func TestPVEBastionInit_Validate_APITokenMode(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:        "my-bloc",
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!ci",
		TokenSecret: "tok-secret-uuid",
	}

	provider := NewPVEBastionInit(cfg)

	require.NoError(t, provider.Validate(), "Validate() unexpected error for API token auth")
}

// TestPVEBastionInit_Validate_UserPassMode verifies that Username + Password
// (without AuthToken/TokenSecret) passes Validate.
func TestPVEBastionInit_Validate_UserPassMode(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:        "my-bloc",
		APIEndpoint: "https://pve.example.com:8006",
		Username:    "root",
		Password:    "s3cr3t",
	}

	provider := NewPVEBastionInit(cfg)

	require.NoError(t, provider.Validate(), "Validate() unexpected error for user/password auth")
}

// TestPVEBastionInit_Validate_PartialTokenAuth verifies that AuthToken without
// TokenSecret (old aliased-Password pattern) fails Validate with ErrPVEAuthRequired.
func TestPVEBastionInit_Validate_PartialTokenAuth(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:        "my-bloc",
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!ci",
		// TokenSecret intentionally empty — old pattern that must now fail.
		// Password set to simulate the old (wrong) alias usage.
		Password: "old-aliased-password",
	}

	provider := NewPVEBastionInit(cfg)

	// hasAPIToken = AuthToken!="" && TokenSecret=="" → false
	// hasUserPass = Username=="" → false
	// Neither mode complete → ErrPVEAuthRequired
	err := provider.Validate()
	require.Error(t, err, "expected ErrPVEAuthRequired when TokenSecret is empty and AuthToken is set, got nil")
	require.ErrorIs(t, err, ErrPVEAuthRequired)
}

// NOTE: No TestPVE_getBastionIPFromAPI_* tests are included.
// pve.go's getBastionIPFromAPI calls cpi.GetProvider("pve") directly with no
// injectable client field (unlike AWSBastionInit.ec2Client). Mocking the Proxmox
// API at the unit-test level requires refactoring pve.go to accept an injectable
// CPI provider; that change is out of scope for this task.
