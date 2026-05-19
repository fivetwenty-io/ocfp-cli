package vault

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/ocfp/ocfp-cli-go/internal/state"
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
// writes endpoint + region + path_style on cf/blobstores/main. For mgmt env,
// the BOSH director blobstore path is also written so genesis BOSH kit can
// consume the same S3-compatible endpoint.
func TestPVEVaultProvider_ConfigureBlobstores_ExternalEndpointOnly(t *testing.T) {
	const endpoint = "https://s3.pve.example.com:9000"

	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)
	provider.BlobstoreEndpoint = endpoint

	err := provider.ConfigureBlobstores("", MgmtEnvType, nil, 0, 1)
	require.NoError(t, err)

	require.Len(t, mock.setMultipleCalls, 2, "mgmt external mode without creds writes cf + bosh config entries")

	cfPath := provider.PathBuilder.GetSystemBlobstorePath(MgmtEnvType, "cf", "main")
	boshPath := provider.PathBuilder.GetSystemBlobstorePath(MgmtEnvType, "bosh", "bosh")

	cfCall := mock.findSetMultipleCall(cfPath)
	require.NotNil(t, cfCall, "CF blobstore config must be written")
	assert.Equal(t, "external", cfCall.data["mode"])
	assert.Equal(t, endpoint, cfCall.data["endpoint"])
	assert.Equal(t, "us-east-1", cfCall.data["region"])
	assert.Equal(t, true, cfCall.data["path_style"])
	assert.Equal(t, "configured", cfCall.data["status"])

	boshCall := mock.findSetMultipleCall(boshPath)
	require.NotNil(t, boshCall, "BOSH director blobstore config must be written for mgmt env")
	assert.Equal(t, "external", boshCall.data["mode"])
	assert.Equal(t, endpoint, boshCall.data["endpoint"])
	assert.Equal(t, true, boshCall.data["path_style"])
}

// TestPVEVaultProvider_ConfigureBlobstores_ExternalWithCreds — full external
// configuration with access/secret on mgmt env produces four SetMultiple
// calls: CF config + CF creds, BOSH director config + BOSH director creds.
// Credentials always live under /creds children so the parent config paths
// stay secret-free.
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

	require.Len(t, mock.setMultipleCalls, 4, "mgmt external mode with creds writes cf config+creds and bosh config+creds")

	cfPath := provider.PathBuilder.GetSystemBlobstorePath(MgmtEnvType, "cf", "main")
	boshPath := provider.PathBuilder.GetSystemBlobstorePath(MgmtEnvType, "bosh", "bosh")

	cfConfig := mock.findSetMultipleCall(cfPath)
	require.NotNil(t, cfConfig)
	assert.Equal(t, "external", cfConfig.data["mode"])
	assert.Equal(t, "eu-west-1", cfConfig.data["region"])
	assert.NotContains(t, cfConfig.data, "access_key", "cf config path must not carry secrets")

	cfCreds := mock.findSetMultipleCall(cfPath + "/creds")
	require.NotNil(t, cfCreds)
	assert.Equal(t, "AKIA-test", cfCreds.data["access_key"])
	assert.Equal(t, "secret-test", cfCreds.data["secret_key"])

	boshConfig := mock.findSetMultipleCall(boshPath)
	require.NotNil(t, boshConfig)
	assert.Equal(t, "external", boshConfig.data["mode"])
	assert.Equal(t, "eu-west-1", boshConfig.data["region"])
	assert.NotContains(t, boshConfig.data, "access_key", "bosh config path must not carry secrets")

	boshCreds := mock.findSetMultipleCall(boshPath + "/creds")
	require.NotNil(t, boshCreds)
	assert.Equal(t, "AKIA-test", boshCreds.data["access_key"])
	assert.Equal(t, "secret-test", boshCreds.data["secret_key"])
}

// TestPVEVaultProvider_ConfigureBlobstores_ExternalOcfEnvSkipsBOSH — ocf env
// must NOT write BOSH director blobstore paths (those belong to mgmt only).
func TestPVEVaultProvider_ConfigureBlobstores_ExternalOcfEnvSkipsBOSH(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)
	provider.BlobstoreMode = "external"
	provider.BlobstoreEndpoint = "https://s3.example.com"
	provider.BlobstoreAccessKey = "AKIA-test"
	provider.BlobstoreSecretKey = "secret-test"

	err := provider.ConfigureBlobstores("", "ocf", nil, 0, 1)
	require.NoError(t, err)

	require.Len(t, mock.setMultipleCalls, 2, "ocf external mode writes only cf config + creds")

	boshPath := provider.PathBuilder.GetSystemBlobstorePath("ocf", "bosh", "bosh")
	assert.Nil(t, mock.findSetMultipleCall(boshPath), "ocf env must not write BOSH blobstore")
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

// seedPVEState creates a fresh bloc state on disk under a temp OCFP_HOME so
// the provider's loadStateManager() can read it back. Returns the loaded
// manager so callers can append resources/outputs before invoking the SUT.
func seedPVEState(t *testing.T, blocName string) *state.Manager {
	t.Helper()

	tmp := t.TempDir()
	t.Setenv("OCFP_HOME", tmp)

	stateDir, err := state.GetStateDir(blocName)
	require.NoError(t, err)

	sm, err := state.NewManager(stateDir)
	require.NoError(t, err)

	_, err = sm.Load(blocName)
	require.NoError(t, err)

	// Persist the (empty) state so subsequent Load calls in the SUT succeed
	// against a real file rather than the fresh in-memory state.
	require.NoError(t, sm.Save())

	return sm
}

// TestPVEVaultProvider_ConfigureSubnets_WritesPerSubnetEntries — given two
// subnet resources in bootstrap state, ConfigureSubnets must write one vault
// entry per subnet under .../net/subnets/{name} with cidr, az, and gateway.
func TestPVEVaultProvider_ConfigureSubnets_WritesPerSubnetEntries(t *testing.T) {
	const blocName = "test-bloc"

	sm := seedPVEState(t, blocName)

	require.NoError(t, sm.AddResource(&state.Resource{
		ID:   "subnet-infra",
		Type: "subnet",
		Name: blocName + "-infra",
		Properties: map[string]interface{}{
			"cidr":              "10.64.64.0/22",
			"availability_zone": "",
			"gateway":           "10.64.64.1",
		},
	}))
	require.NoError(t, sm.AddResource(&state.Resource{
		ID:   "subnet-ocfp-0",
		Type: "subnet",
		Name: blocName + "-ocfp-0",
		Properties: map[string]interface{}{
			"cidr":              "10.64.68.0/22",
			"availability_zone": "pvea",
			"gateway":           "10.64.68.1",
		},
	}))
	require.NoError(t, sm.Save())

	mock := &awsMockSafe{}
	cfg := &config.Config{VPCCIDRBlock: "10.64.64.0/19"}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigureSubnets("", MgmtEnvType, nil, 0, 1)
	require.NoError(t, err)

	subnetsPath := provider.PathBuilder.GetSubnetsPath(MgmtEnvType)

	infraPath := filepath.Join(subnetsPath, blocName+"-infra")
	infraCall := mock.findSetMultipleCall(infraPath)
	require.NotNil(t, infraCall, "infra subnet must be written at %s", infraPath)
	assert.Equal(t, "10.64.64.0/22", infraCall.data["cidr"])
	assert.Equal(t, "10.64.64.1", infraCall.data["gateway"])
	assert.Equal(t, "", infraCall.data["az"])

	ocfp0Path := filepath.Join(subnetsPath, blocName+"-ocfp-0")
	ocfp0Call := mock.findSetMultipleCall(ocfp0Path)
	require.NotNil(t, ocfp0Call, "ocfp-0 subnet must be written at %s", ocfp0Path)
	assert.Equal(t, "10.64.68.0/22", ocfp0Call.data["cidr"])
	assert.Equal(t, "pvea", ocfp0Call.data["az"])
	assert.Equal(t, "10.64.68.1", ocfp0Call.data["gateway"])

	// Fallback blob path must NOT be written when state-driven entries exist.
	fallback := mock.findSetMultipleCall(subnetsPath)
	assert.Nil(t, fallback, "with state present, no fallback blob should be written at %s", subnetsPath)
}

// TestPVEVaultProvider_ConfigureSubnets_NoStateFallsBack — without an OCFP_HOME
// pointing at real state, ConfigureSubnets must still produce a single
// fallback write so populate doesn't leave the path empty.
func TestPVEVaultProvider_ConfigureSubnets_NoStateFallsBack(t *testing.T) {
	// Point OCFP_HOME at an empty temp dir so state.Load creates a fresh
	// in-memory state with no subnet resources — exercising the empty-resources
	// fallback branch.
	tmp := t.TempDir()
	t.Setenv("OCFP_HOME", tmp)

	mock := &awsMockSafe{}
	cfg := &config.Config{VPCCIDRBlock: "10.64.64.0/19"}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigureSubnets("", MgmtEnvType, nil, 0, 1)
	require.NoError(t, err)

	subnetsPath := provider.PathBuilder.GetSubnetsPath(MgmtEnvType)
	call := mock.findSetMultipleCall(subnetsPath)
	require.NotNil(t, call, "fallback blob must be written at %s", subnetsPath)
	assert.Equal(t, "10.64.64.0/19", call.data["cidr"])
	assert.Contains(t, call.data["note"], "no bootstrap state",
		"fallback note must indicate state was absent")
}

// TestPVEVaultProvider_ConfigureSubnets_ReservedIPsPropagated — when state has
// a `reserved_{subnet}_{role}_ip` output, ConfigureSubnets writes the IP under
// .../subnets/{name}/reserved-ips/{role} so genesis kits can resolve roles
// without parsing CIDR math themselves.
func TestPVEVaultProvider_ConfigureSubnets_ReservedIPsPropagated(t *testing.T) {
	const blocName = "test-bloc"

	sm := seedPVEState(t, blocName)

	require.NoError(t, sm.AddResource(&state.Resource{
		ID:   "subnet-infra",
		Type: "subnet",
		Name: blocName + "-infra",
		Properties: map[string]interface{}{
			"cidr":    "10.64.64.0/22",
			"gateway": "10.64.64.1",
		},
	}))
	require.NoError(t, sm.SetOutput("reserved_"+blocName+"-infra_bastion_ip", "10.64.64.10"))
	require.NoError(t, sm.SetOutput("reserved_"+blocName+"-infra_jumpbox_ip", "10.64.64.11"))
	// Empty + unrelated outputs must be ignored.
	require.NoError(t, sm.SetOutput("reserved_"+blocName+"-infra_empty_ip", ""))
	require.NoError(t, sm.SetOutput("unrelated_output", "noise"))
	require.NoError(t, sm.Save())

	mock := &awsMockSafe{}
	cfg := &config.Config{VPCCIDRBlock: "10.64.64.0/19"}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigureSubnets("", MgmtEnvType, nil, 0, 1)
	require.NoError(t, err)

	subnetPath := filepath.Join(
		provider.PathBuilder.GetSubnetsPath(MgmtEnvType),
		blocName+"-infra",
	)

	bastionPath := filepath.Join(subnetPath, "reserved-ips", "bastion")
	bastionCall := mock.findSetMultipleCall(bastionPath)
	require.NotNil(t, bastionCall, "bastion reserved IP must be written at %s", bastionPath)
	assert.Equal(t, "10.64.64.10", bastionCall.data["ip"])

	jumpboxPath := filepath.Join(subnetPath, "reserved-ips", "jumpbox")
	jumpboxCall := mock.findSetMultipleCall(jumpboxPath)
	require.NotNil(t, jumpboxCall, "jumpbox reserved IP must be written at %s", jumpboxPath)
	assert.Equal(t, "10.64.64.11", jumpboxCall.data["ip"])

	// Empty-IP outputs must not produce a write.
	emptyPath := filepath.Join(subnetPath, "reserved-ips", "empty")
	assert.Nil(t, mock.findSetMultipleCall(emptyPath),
		"empty-valued reserved IP output must not be written")
}
