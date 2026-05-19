package vault

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestPVEProvider builds a PVEVaultProvider wired to a mock safe.
// Uses awsMockSafe (defined in aws_provider_test.go) — same package, same
// SafeInterface, no redefinition needed.
func newTestPVEProvider(cfg *config.Config, mock *awsMockSafe) *PVEVaultProvider {
	const blocName = "test-bloc"

	return &PVEVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              mock,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
	}
}

// TestPVEVaultProvider_configureCPI_WritesPath_APITokenMode — API token auth happy
// path: AuthToken + TokenSecret set; mock captures one SetMultiple with token_id,
// token_secret, and no username/password keys.
func TestPVEVaultProvider_configureCPI_WritesPath_APITokenMode(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!mytoken",
		TokenSecret: "supersecret",
		Region:      "pve-node1",
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)

	require.Len(t, mock.setMultipleCalls, 1, "expected exactly one SetMultiple call")

	call := mock.setMultipleCalls[0]

	// Path must end with /cpi/pve under the mgmt environment.
	expectedSuffix := "mgmt/cpi/pve"
	assert.True(t, strings.HasSuffix(call.path, expectedSuffix),
		"CPI path %q should end with %q", call.path, expectedSuffix)

	assert.Equal(t, "https://pve.example.com:8006", call.data["host"])
	assert.Equal(t, "root@pam!mytoken", call.data["token_id"])
	assert.Equal(t, "supersecret", call.data["token_secret"])
	assert.Equal(t, "pve-node1", call.data["node"])
	assert.Equal(t, "configured", call.data["status"])

	// API token mode must NOT write username or password keys.
	assert.Nil(t, call.data["username"], "username key must be absent in API token mode")
	assert.Nil(t, call.data["password"], "password key must be absent in API token mode")
}

// TestPVEVaultProvider_configureCPI_WritesPath_UserPassMode — username/password auth
// happy path: Username + Password set (AuthToken empty); mock captures one SetMultiple
// with username, password, and no token_id/token_secret keys.
func TestPVEVaultProvider_configureCPI_WritesPath_UserPassMode(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		Username:    "root@pam",
		Password:    "s3cr3t",
		Region:      "pve-node1",
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)

	require.Len(t, mock.setMultipleCalls, 1, "expected exactly one SetMultiple call")

	call := mock.setMultipleCalls[0]

	expectedSuffix := "mgmt/cpi/pve"
	assert.True(t, strings.HasSuffix(call.path, expectedSuffix),
		"CPI path %q should end with %q", call.path, expectedSuffix)

	assert.Equal(t, "https://pve.example.com:8006", call.data["host"])
	assert.Equal(t, "root@pam", call.data["username"])
	assert.Equal(t, "s3cr3t", call.data["password"])
	assert.Equal(t, "pve-node1", call.data["node"])
	assert.Equal(t, "configured", call.data["status"])

	// User/pass mode must NOT write token_id or token_secret keys.
	assert.Nil(t, call.data["token_id"], "token_id key must be absent in user/pass mode")
	assert.Nil(t, call.data["token_secret"], "token_secret key must be absent in user/pass mode")
}

// TestPVEVaultProvider_configureCPI_MissingHostAndAuth — empty APIEndpoint produces
// an error whose message references "host". Verified before auth mode detection.
func TestPVEVaultProvider_configureCPI_MissingHost(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "",
		AuthToken:   "root@pam!mytoken",
		TokenSecret: "supersecret",
		Region:      "pve-node1",
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host",
		"error for missing api_endpoint must reference 'host'")

	assert.Empty(t, mock.setMultipleCalls)
}

// TestPVEVaultProvider_configureCPI_NoAuth — no auth credentials at all produces
// an error whose message references the expected auth modes.
func TestPVEVaultProvider_configureCPI_NoAuth(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		Region:      "pve-node1",
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth",
		"error for missing auth must reference 'auth'")

	assert.Empty(t, mock.setMultipleCalls)
}

// TestPVEVaultProvider_ConfigureAZs_SingleNode — Region set, no Nodes slice:
// expect exactly one SetMultiple at the AZ path for that region.
func TestPVEVaultProvider_ConfigureAZs_SingleNode(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigureAZs(MgmtEnvType)
	require.NoError(t, err)

	require.Len(t, mock.setMultipleCalls, 1, "single-node config must produce exactly one AZ write")

	expectedPath := provider.PathBuilder.GetAZPath(MgmtEnvType, "pve-node1")
	assert.Equal(t, expectedPath, mock.setMultipleCalls[0].path)
	assert.Equal(t, "pve-node1", mock.setMultipleCalls[0].data["node_name"])
}

// TestPVEVaultProvider_ConfigureAZs_MultiNode — Nodes []string slice not yet
// present in config.Config (tracked as N3 in adversarial review). Skip until
// Config.Nodes is added.
func TestPVEVaultProvider_ConfigureAZs_MultiNode(t *testing.T) {
	t.Skip("Config.Nodes []string not yet in config.Config (N3); re-enable when field is added")
}

// TestPVEVaultProvider_ConfigureAZs_EmptyBoth — empty Region means no write,
// no error.
func TestPVEVaultProvider_ConfigureAZs_EmptyBoth(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: ""}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigureAZs(MgmtEnvType)
	require.NoError(t, err)

	assert.Empty(t, mock.setMultipleCalls, "empty Region must produce no vault writes")
}

// TestPVEVaultProvider_ConfigureBlobstores_LocalModeWritesMarker — no
// BlobstoreMode and no BlobstoreEndpoint defaults to local mode, which writes
// a single `mode: local, status: configured` marker so kits can detect the
// configured-but-not-external state. Previously this produced zero writes,
// but downstream kit code now needs a positive signal.
func TestPVEVaultProvider_ConfigureBlobstores_LocalModeWritesMarker(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigureBlobstores("", MgmtEnvType, nil, 0, 1)
	require.NoError(t, err)

	require.Len(t, mock.setMultipleCalls, 1, "local mode must write a single marker")

	expectedPath := provider.PathBuilder.GetSystemBlobstorePath(MgmtEnvType, "cf", "main")
	call := mock.setMultipleCalls[0]
	assert.Equal(t, expectedPath, call.path)
	assert.Equal(t, "local", call.data["mode"])
	assert.Equal(t, "configured", call.data["status"])
	assert.NotContains(t, call.data, "endpoint", "local mode must not write endpoint")
	assert.NotContains(t, call.data, "access_key", "local mode must not write credentials")
}

// TestPVEVaultProvider_ConfigureBlobstores_ExternalEndpointOnly — a non-empty
// BlobstoreEndpoint with no explicit mode promotes the entry to external
// mode (backwards-compatible with --blobstore-endpoint-only setups) and
// writes endpoint + region + path_style on cf/blobstores/main.
func TestPVEVaultProvider_ConfigureBlobstores_ExternalEndpointOnly(t *testing.T) {
	const endpoint = "https://s3.pve.example.com:9000"

	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)
	provider.BlobstoreEndpoint = endpoint

	err := provider.ConfigureBlobstores("", MgmtEnvType, nil, 0, 1)
	require.NoError(t, err)

	require.Len(t, mock.setMultipleCalls, 1, "external mode without creds writes one entry")

	expectedPath := provider.PathBuilder.GetSystemBlobstorePath(MgmtEnvType, "cf", "main")
	call := mock.setMultipleCalls[0]
	assert.Equal(t, expectedPath, call.path)
	assert.Equal(t, "external", call.data["mode"])
	assert.Equal(t, endpoint, call.data["endpoint"])
	assert.Equal(t, "us-east-1", call.data["region"])
	assert.Equal(t, true, call.data["path_style"])
	assert.Equal(t, "configured", call.data["status"])
}

// TestPVEVaultProvider_ConfigureBlobstores_ExternalWithCreds — full external
// configuration with access/secret produces two SetMultiple calls: one to the
// blobstore config path, one to a /creds child so the config path stays
// secret-free.
func TestPVEVaultProvider_ConfigureBlobstores_ExternalWithCreds(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)
	provider.BlobstoreMode = "external"
	provider.BlobstoreEndpoint = "https://s3.example.com"
	provider.BlobstoreRegion = "eu-west-1"
	provider.BlobstoreAccessKey = "AKIA-test"
	provider.BlobstoreSecretKey = "secret-test"

	err := provider.ConfigureBlobstores("", MgmtEnvType, nil, 0, 1)
	require.NoError(t, err)

	require.Len(t, mock.setMultipleCalls, 2, "external mode with creds writes config + creds")

	configCall := mock.setMultipleCalls[0]
	assert.Equal(t, "external", configCall.data["mode"])
	assert.Equal(t, "eu-west-1", configCall.data["region"])
	assert.NotContains(t, configCall.data, "access_key", "config path must not carry secrets")

	credsCall := mock.setMultipleCalls[1]
	assert.Contains(t, credsCall.path, "/creds")
	assert.Equal(t, "AKIA-test", credsCall.data["access_key"])
	assert.Equal(t, "secret-test", credsCall.data["secret_key"])
}

// TestPVEVaultProvider_ConfigurePublicIPs_StatusPending — with no state manager
// (nil reporter, empty config) ConfigurePublicIPs must write status: "pending".
// PVE has no IaaS floating IPs; the pending marker mirrors the AWS shape so
// that downstream kit code can use a single status-field check.
func TestPVEVaultProvider_ConfigurePublicIPs_StatusPending(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigurePublicIPs(nil, 1, 1)
	require.NoError(t, err)

	publicIPsPath := provider.PathBuilder.GetPublicIPsPath()
	call := mock.findSetMultipleCall(publicIPsPath)
	require.NotNil(t, call, "ConfigurePublicIPs must write to public IPs path")

	assert.Equal(t, PublicIPStatusPending, call.data["status"],
		"PVE public IPs must carry status: %q", PublicIPStatusPending)
}

// TestPVEVaultProvider_GetProviderName — GetProviderName returns the canonical
// string "pve" used as the provider discriminator throughout the CLI.
func TestPVEVaultProvider_GetProviderName(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{}
	provider := newTestPVEProvider(cfg, mock)

	assert.Equal(t, "pve", provider.GetProviderName())
}
