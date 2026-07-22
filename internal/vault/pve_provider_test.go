package vault

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
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
		bucketEnsurer:     &recordingBucketEnsurer{},
	}
}

// recordingBucketEnsurer captures the buckets ensureBlobstoreBucket asks for
// without hitting a live S3 endpoint. Optionally returns a forced error to
// exercise the fatal-on-failure path.
type recordingBucketEnsurer struct {
	buckets []string
	err     error
}

func (r *recordingBucketEnsurer) EnsureBuckets(_ context.Context, _ artifacts.Endpoint, _ artifacts.Credentials, buckets []artifacts.BucketSpec) error {
	for _, b := range buckets {
		r.buckets = append(r.buckets, b.Name)
	}

	return r.err
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

	assert.Equal(t, "pve.example.com", call.data["host"], "host stored as bare hostname for CPI client compatibility")
	assert.Equal(t, "root@pam!mytoken", call.data["token_id"])
	assert.Equal(t, "supersecret", call.data["token_secret"])
	assert.Equal(t, "pve-node1", call.data["node"])
	assert.Equal(t, "configured", call.data["status"])

	// API token mode must NOT write username (user/pass-only key).
	assert.Nil(t, call.data["username"], "username key must be absent in API token mode")
	// API token mode must NOT write a password placeholder: the kit's PVE auth
	// wiring references only the active auth key, so the inactive password key
	// is absent (an empty value cannot be entombed into the director's CredHub).
	assert.Nil(t, call.data["password"], "password key must be absent in API token mode")
	// api_token is rendered in bosh-pve-cpi-release format
	// "user@realm!tokenid=secret" — the CPI's PVE client requires it joined.
	assert.Equal(t, "root@pam!mytoken=supersecret", call.data["api_token"], "api_token must combine token_id and token_secret as user@realm!tokenid=secret")
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

	assert.Equal(t, "pve.example.com", call.data["host"], "host stored as bare hostname for CPI client compatibility")
	assert.Equal(t, "root@pam", call.data["username"])
	assert.Equal(t, "s3cr3t", call.data["password"])
	assert.Equal(t, "pve-node1", call.data["node"])
	assert.Equal(t, "configured", call.data["status"])

	// User/pass mode must NOT write token_id or token_secret keys.
	assert.Nil(t, call.data["token_id"], "token_id key must be absent in user/pass mode")
	assert.Nil(t, call.data["token_secret"], "token_secret key must be absent in user/pass mode")
	// User/pass mode must NOT write an api_token placeholder: only the active
	// auth key is written, so the inactive api_token key is absent (an empty
	// value cannot be entombed into the director's CredHub).
	assert.Nil(t, call.data["api_token"], "api_token key must be absent in user/pass mode")
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
// AZs are keyed by workload zone (pvea/pveb/pvec), all backed by the single
// node. This matches the ocfp-{0,1,2} subnet az assignment so Genesis'
// _set_network_azs can resolve each subnet's az.
func TestPVEVaultProvider_ConfigureAZs_SingleNode(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigureAZs(MgmtEnvType)
	require.NoError(t, err)

	require.Len(t, mock.setMultipleCalls, pveWorkloadAZCount,
		"single-node config must write one AZ per workload zone")

	for z := range pveWorkloadAZCount {
		zone := pveAZKeyPrefix + string(rune('a'+z))
		path := provider.PathBuilder.GetAZPath(MgmtEnvType, zone)
		call := mock.findSetMultipleCall(path)
		require.NotNil(t, call, "AZ entry for zone %s must be written", zone)
		// All zones are backed by the single node...
		assert.Equal(t, "pve-node1", call.data["node_name"])
		// ...but each carries a distinct 1-based index so Genesis derives
		// pvea->"<env>-z1", pveb->z2, pvec->z3 (name = "<env>-z" . index).
		assert.Equal(t, z+1, call.data["index"], "zone %s must carry 1-based index", zone)
	}
}

// TestPVEVaultProvider_ConfigureAZs_MultiNode — Nodes slice spreads the workload
// zones across nodes round-robin; AZ keys remain the zone names (pvea/pveb/pvec)
// with 1-based indices so Genesis derives <env>-z1, <env>-z2, <env>-z3.
func TestPVEVaultProvider_ConfigureAZs_MultiNode(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Nodes: []string{"pve-a", "pve-b", "pve-c"}}
	provider := newTestPVEProvider(cfg, mock)

	require.NoError(t, provider.ConfigureAZs(MgmtEnvType))
	require.Len(t, mock.setMultipleCalls, pveWorkloadAZCount, "one AZ write per workload zone")

	for z := range pveWorkloadAZCount {
		zone := pveAZKeyPrefix + string(rune('a'+z))
		path := provider.PathBuilder.GetAZPath(MgmtEnvType, zone)
		call := mock.findSetMultipleCall(path)
		require.NotNil(t, call, "AZ entry for zone %s must be written", zone)
		// 3 zones across 3 nodes -> 1:1 mapping (pvea->pve-a, pveb->pve-b, ...).
		assert.Equal(t, cfg.Nodes[z%len(cfg.Nodes)], call.data["node_name"])
		assert.Equal(t, z+1, call.data["index"], "zone %s must carry 1-based index", zone)
	}
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

// TestPVEVaultProvider_ConfigureFQDNs_CloudflareScopesSystemServices — with the
// Cloudflare tunnel fronting the bloc, infra-UI services (concourse/shield/...)
// derive under the *.system wildcard cert as {svc}.system.{base}, while
// non-UI services (bosh) stay flat. Prevents the login-redirect SSL mismatch
// where atc's external_url pointed at a hostname with no edge cert.
func TestPVEVaultProvider_ConfigureFQDNs_CloudflareScopesSystemServices(t *testing.T) {
	enabled := true
	mock := &awsMockSafe{}
	cfg := &config.Config{
		FQDNs:      &config.FQDNConfig{Base: "ocf.example.io"},
		Cloudflare: &config.CloudflareConfig{Enabled: &enabled},
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigureFQDNs("", MgmtEnvType, nil, 1, 1)
	require.NoError(t, err)

	call := mock.findSetMultipleCall(provider.PathBuilder.GetFQDNsPath(MgmtEnvType))
	require.NotNil(t, call, "per-service FQDNs must be written for the env")

	assert.Equal(t, "concourse.system.ocf.example.io", call.data["concourse"],
		"concourse must derive under *.system (the only edge-cert host)")
	assert.Equal(t, "shield.system.ocf.example.io", call.data["shield"])
	assert.Equal(t, "prometheus.system.ocf.example.io", call.data["prometheus"])
	assert.Equal(t, "bosh.ocf.example.io", call.data["bosh"],
		"non-UI services stay flat (not behind the *.system wildcard)")
	assert.Equal(t, "ocf.example.io", call.data["base"])
	assert.Equal(t, MgmtEnvType, call.data["env_type"])

	// Base FQDN is also stored at the shared path (mgmt pass).
	basePath := provider.PathBuilder.GetBaseFQDNPath()
	var baseStored bool
	for _, s := range mock.setSingleCalls {
		if s.path == basePath && s.key == "value" && s.value == "ocf.example.io" {
			baseStored = true
		}
	}
	assert.True(t, baseStored, "base FQDN must be stored at the shared path")
}

// TestPVEVaultProvider_ConfigureFQDNs_FlatWhenCloudflareDisabled — without the
// Cloudflare edge wildcard, infra UIs keep the flat {svc}.{base} form (e.g. an
// external LB terminating per-host certs).
func TestPVEVaultProvider_ConfigureFQDNs_FlatWhenCloudflareDisabled(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		FQDNs: &config.FQDNConfig{Base: "ocf.example.io"},
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigureFQDNs("", MgmtEnvType, nil, 1, 1)
	require.NoError(t, err)

	call := mock.findSetMultipleCall(provider.PathBuilder.GetFQDNsPath(MgmtEnvType))
	require.NotNil(t, call)

	assert.Equal(t, "concourse.ocf.example.io", call.data["concourse"],
		"no Cloudflare edge: infra UIs stay flat")
}

// TestPVEVaultProvider_ConfigureBlobstores_LocalModeWritesMarker — no
// BlobstoreMode and no BlobstoreEndpoint defaults to local mode.  Two writes
// land on mgmt env: a cf/blobstores/main marker (mode=local) plus a
// non-functional bosh/blobstores/bosh placeholder so the bosh manifest hook's
// `:name` / `:region` lookups can resolve.  Neither write carries credentials
// or an endpoint.
func TestPVEVaultProvider_ConfigureBlobstores_LocalModeWritesMarker(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigureBlobstores("", MgmtEnvType, nil, 0, 1)
	require.NoError(t, err)

	cfPath := provider.PathBuilder.GetSystemBlobstorePath(MgmtEnvType, "cf", "main")
	boshPath := provider.PathBuilder.GetSystemBlobstorePath(MgmtEnvType, "bosh", "bosh")

	cfCall := mock.findSetMultipleCall(cfPath)
	require.NotNil(t, cfCall, "cf blobstore marker must be written in local mode")
	assert.Equal(t, "local", cfCall.data["mode"])
	assert.Equal(t, "configured", cfCall.data["status"])
	assert.NotContains(t, cfCall.data, "endpoint", "local mode must not write endpoint")
	assert.NotContains(t, cfCall.data, "access_key", "local mode must not write credentials")

	boshCall := mock.findSetMultipleCall(boshPath)
	require.NotNil(t, boshCall, "bosh blobstore meta placeholder must be written in local mode")
	assert.Equal(t, "local", boshCall.data["mode"])
	assert.NotContains(t, boshCall.data, "endpoint", "local mode bosh placeholder must not write endpoint")
	assert.NotContains(t, boshCall.data, "access_key", "local mode bosh placeholder must not write credentials")
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

// TestPVEVaultProvider_ConfigureBlobstores_ExternalOcfEnvWritesBOSH — the
// env-BOSH director (deployed for the ocf scope) needs its own ocf-scoped BOSH
// blobstore so it can store compiled releases. ocf external mode therefore
// writes cf config+creds AND the ocf-scoped bosh config+creds (4 calls).
func TestPVEVaultProvider_ConfigureBlobstores_ExternalOcfEnvWritesBOSH(t *testing.T) {
	const blocName = "test-bloc"

	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)
	provider.BlobstoreMode = "external"
	provider.BlobstoreEndpoint = "https://s3.example.com"
	provider.BlobstoreAccessKey = "AKIA-test"
	provider.BlobstoreSecretKey = "secret-test"

	err := provider.ConfigureBlobstores("", "ocf", nil, 0, 1)
	require.NoError(t, err)

	require.Len(t, mock.setMultipleCalls, 4, "ocf external mode writes cf config+creds and ocf bosh config+creds")

	boshPath := provider.PathBuilder.GetSystemBlobstorePath("ocf", "bosh", "bosh")
	boshCall := mock.findSetMultipleCall(boshPath)
	require.NotNil(t, boshCall, "ocf env must write the ocf-scoped BOSH blobstore")
	assert.Equal(t, "external", boshCall.data["mode"])
	assert.Equal(t, blocName+"-ocf-bosh", boshCall.data["name"], "ocf bosh bucket follows <bloc>-ocf-bosh")

	boshCreds := mock.findSetMultipleCall(boshPath + "/creds")
	require.NotNil(t, boshCreds, "ocf bosh creds must be written")
	assert.Equal(t, "AKIA-test", boshCreds.data["access_key"])
}

// TestPVEVaultProvider_ConfigureBlobstores_EnsuresBuckets — external mode must
// CREATE the buckets it writes secrets for, not just write the secret. The ocf
// env writes a CF blobstore secret (<bloc>-ocf-cf) and a BOSH director blobstore
// secret (<bloc>-ocf-bosh); both buckets must be ensured. The missing
// <bloc>-ocf-bosh bucket is what silently broke CF deploy (NoSuchUpload).
func TestPVEVaultProvider_ConfigureBlobstores_EnsuresBuckets(t *testing.T) {
	const blocName = "test-bloc"

	mock := &awsMockSafe{}
	ensurer := &recordingBucketEnsurer{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)
	provider.bucketEnsurer = ensurer
	provider.BlobstoreMode = "external"
	provider.BlobstoreEndpoint = "https://10.64.64.11:9000"
	provider.BlobstoreAccessKey = "AKIA-test"
	provider.BlobstoreSecretKey = "secret-test"

	err := provider.ConfigureBlobstores("", "ocf", nil, 0, 1)
	require.NoError(t, err)

	assert.Contains(t, ensurer.buckets, blocName+"-ocf-cf", "CF blobstore bucket must be created")
	assert.Contains(t, ensurer.buckets, blocName+"-ocf-bosh", "BOSH director blobstore bucket must be created")
}

// TestPVEVaultProvider_ConfigureBlobstores_BucketFailureIsFatal — a written
// secret pointing at a bucket that could not be created is worse than a loud
// failure, so bucket-creation errors abort ConfigureBlobstores.
func TestPVEVaultProvider_ConfigureBlobstores_BucketFailureIsFatal(t *testing.T) {
	mock := &awsMockSafe{}
	ensurer := &recordingBucketEnsurer{err: assert.AnError}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)
	provider.bucketEnsurer = ensurer
	provider.BlobstoreMode = "external"
	provider.BlobstoreEndpoint = "https://10.64.64.11:9000"
	provider.BlobstoreAccessKey = "AKIA-test"
	provider.BlobstoreSecretKey = "secret-test"

	err := provider.ConfigureBlobstores("", "ocf", nil, 0, 1)
	require.Error(t, err, "bucket-creation failure must abort blobstore configuration")
}

// TestPVEVaultProvider_ConfigureBlobstores_NoCredsSkipsBucketCreation — without
// credentials the ensurer cannot authenticate, so bucket creation is skipped
// (endpoint-only mode still writes the config marker).
func TestPVEVaultProvider_ConfigureBlobstores_NoCredsSkipsBucketCreation(t *testing.T) {
	mock := &awsMockSafe{}
	ensurer := &recordingBucketEnsurer{err: assert.AnError}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)
	provider.bucketEnsurer = ensurer
	provider.BlobstoreMode = "external"
	provider.BlobstoreEndpoint = "https://10.64.64.11:9000"

	err := provider.ConfigureBlobstores("", "ocf", nil, 0, 1)
	require.NoError(t, err, "no creds → skip bucket creation, do not surface the forced error")
	assert.Empty(t, ensurer.buckets)
}

// newTestPVEProviderWithFakeSafe builds a PVEVaultProvider wired to fakeSafe
// (defined in ca_test.go), which — unlike awsMockSafe — supports seeded
// GetAll reads. Needed to exercise blobstoreS3Target's vault CA recovery.
func newTestPVEProviderWithFakeSafe(cfg *config.Config, safe *fakeSafe) *PVEVaultProvider {
	const blocName = "test-bloc"

	return &PVEVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              safe,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
		bucketEnsurer:     &recordingBucketEnsurer{},
	}
}

// TestPVEVaultProvider_blobstoreS3Target_NoEndpoint_NotOK — endpoint/creds
// absent is the "external mode not configured" case: ok=false, err=nil, no
// vault access attempted.
func TestPVEVaultProvider_blobstoreS3Target_NoEndpoint_NotOK(t *testing.T) {
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProviderWithFakeSafe(cfg, newFakeSafe())

	_, _, ok, err := provider.blobstoreS3Target()
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestPVEVaultProvider_blobstoreS3Target_CACertAlreadySet_PinsWithoutVaultCall
// — when state already carried a CA cert, blobstoreS3Target must pin to it
// and never touch vault at all (proven by a Safe configured to error on any
// GetAll).
func TestPVEVaultProvider_blobstoreS3Target_CACertAlreadySet_PinsWithoutVaultCall(t *testing.T) {
	safe := newFakeSafe()
	safe.failOnRead = assert.AnError

	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProviderWithFakeSafe(cfg, safe)
	provider.BlobstoreEndpoint = "https://10.64.64.11:9000"
	provider.BlobstoreAccessKey = "ak"
	provider.BlobstoreSecretKey = "sk"
	provider.blobstoreTLSMode = config.ArtifactsTLSModeInternalCA
	provider.blobstoreCACert = "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"

	ep, creds, ok, err := provider.blobstoreS3Target()
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, provider.blobstoreCACert, ep.CACert)
	assert.False(t, ep.SkipTLSVerify)
	assert.Equal(t, "ak", creds.AccessKey)
}

// TestPVEVaultProvider_blobstoreS3Target_InternalCA_RecoversFromVault — CA
// missing from state but present in vault: blobstoreS3Target recovers it via
// LoadBlocCA, uses it for the endpoint, and repairs p.blobstoreCACert so
// subsequent config writes (configureExternalBlobstore, ...ForScope) also
// pick it up.
func TestPVEVaultProvider_blobstoreS3Target_InternalCA_RecoversFromVault(t *testing.T) {
	const bloc = "test-bloc"

	safe := newFakeSafe()

	caCert := "-----BEGIN CERTIFICATE-----\nrecovered\n-----END CERTIFICATE-----\n"
	require.NoError(t, safe.SetMultiple(blocCAPath(bloc), map[string]any{
		"cert":        caCert,
		"key":         "-----BEGIN EC PRIVATE KEY-----\nkey\n-----END EC PRIVATE KEY-----\n",
		"fingerprint": "deadbeef",
	}))

	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProviderWithFakeSafe(cfg, safe)
	provider.BlobstoreEndpoint = "https://10.64.64.11:9000"
	provider.BlobstoreAccessKey = "ak"
	provider.BlobstoreSecretKey = "sk"
	provider.blobstoreTLSMode = config.ArtifactsTLSModeInternalCA

	ep, _, ok, err := provider.blobstoreS3Target()
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, caCert, ep.CACert)
	assert.False(t, ep.SkipTLSVerify)
	assert.Equal(t, caCert, provider.blobstoreCACert, "recovered CA must be cached for later config writes")
}

// TestPVEVaultProvider_blobstoreS3Target_InternalCA_UnrecoverableCA_Errors —
// tls.mode=internal-ca, no CA in state, no CA in vault either: this must be a
// loud error, never a silent SkipTLSVerify fallback.
func TestPVEVaultProvider_blobstoreS3Target_InternalCA_UnrecoverableCA_Errors(t *testing.T) {
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProviderWithFakeSafe(cfg, newFakeSafe())
	provider.BlobstoreEndpoint = "https://10.64.64.11:9000"
	provider.BlobstoreAccessKey = "ak"
	provider.BlobstoreSecretKey = "sk"
	provider.blobstoreTLSMode = config.ArtifactsTLSModeInternalCA

	_, _, ok, err := provider.blobstoreS3Target()
	require.Error(t, err)
	assert.True(t, ok, "endpoint/creds ARE configured; ok distinguishes that from the TLS-trust error")
	assert.ErrorIs(t, err, ErrBlocCANotFound)
}

// TestPVEVaultProvider_blobstoreS3Target_SelfSigned_NoCACert_SkipVerifyWithWarning
// — self-signed with no CA is the one case allowed to fall back to
// SkipTLSVerify (this provider has no operator-facing --insecure flag to gate
// an explicit opt-in on; the warning log is the acknowledgment).
func TestPVEVaultProvider_blobstoreS3Target_SelfSigned_NoCACert_SkipVerifyWithWarning(t *testing.T) {
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProviderWithFakeSafe(cfg, newFakeSafe())
	provider.BlobstoreEndpoint = "https://10.64.64.11:9000"
	provider.BlobstoreAccessKey = "ak"
	provider.BlobstoreSecretKey = "sk"
	provider.blobstoreTLSMode = config.ArtifactsTLSModeSelfSigned

	ep, _, ok, err := provider.blobstoreS3Target()
	require.NoError(t, err)
	assert.True(t, ok)
	assert.True(t, ep.SkipTLSVerify)
	assert.Empty(t, ep.CACert)
}

// TestPVEVaultProvider_blobstoreS3Target_UnknownMode_NoCACert_SkipVerifyWithWarning
// — a manual --blobstore-endpoint override with no corresponding artifacts
// state carries no tls.mode at all ("") ; treated the same as self-signed
// rather than erroring as an internal-ca state inconsistency, since there is
// no state to be inconsistent with.
func TestPVEVaultProvider_blobstoreS3Target_UnknownMode_NoCACert_SkipVerifyWithWarning(t *testing.T) {
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProviderWithFakeSafe(cfg, newFakeSafe())
	provider.BlobstoreEndpoint = "https://10.64.64.11:9000"
	provider.BlobstoreAccessKey = "ak"
	provider.BlobstoreSecretKey = "sk"
	// provider.blobstoreTLSMode left at its zero value "".

	ep, _, ok, err := provider.blobstoreS3Target()
	require.NoError(t, err)
	assert.True(t, ok)
	assert.True(t, ep.SkipTLSVerify)
}

// TestPVEVaultProvider_blobstoreS3Target_HTTPEndpoint_NeverTouchesVault — an
// http:// endpoint needs no TLS trust material at all, even when tls.mode is
// internal-ca (inconsistent state); blobstoreS3Target must not attempt vault
// CA recovery in that case, proven by a Safe configured to error on any read.
func TestPVEVaultProvider_blobstoreS3Target_HTTPEndpoint_NeverTouchesVault(t *testing.T) {
	safe := newFakeSafe()
	safe.failOnRead = assert.AnError

	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProviderWithFakeSafe(cfg, safe)
	provider.BlobstoreEndpoint = "http://10.64.64.11:9000"
	provider.BlobstoreAccessKey = "ak"
	provider.BlobstoreSecretKey = "sk"
	provider.blobstoreTLSMode = config.ArtifactsTLSModeInternalCA

	ep, _, ok, err := provider.blobstoreS3Target()
	require.NoError(t, err)
	assert.True(t, ok)
	assert.False(t, ep.SkipTLSVerify)
	assert.Empty(t, ep.CACert)
}

// TestPVEVaultProvider_ensureBlobstoreBucket_PropagatesTLSTrustError —
// ensureBlobstoreBucket must surface blobstoreS3Target's TLS-trust error
// (internal-ca, unrecoverable CA) as a fatal error, not skip bucket creation
// silently the way the "no creds" case does.
func TestPVEVaultProvider_ensureBlobstoreBucket_PropagatesTLSTrustError(t *testing.T) {
	ensurer := &recordingBucketEnsurer{}

	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProviderWithFakeSafe(cfg, newFakeSafe())
	provider.bucketEnsurer = ensurer
	provider.BlobstoreEndpoint = "https://10.64.64.11:9000"
	provider.BlobstoreAccessKey = "ak"
	provider.BlobstoreSecretKey = "sk"
	provider.blobstoreTLSMode = config.ArtifactsTLSModeInternalCA

	err := provider.ensureBlobstoreBucket("test-bloc-ocf-cf")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlocCANotFound)
	assert.Empty(t, ensurer.buckets, "bucket creation must not be attempted when TLS trust is unresolved")
}

// TestPVEVaultProvider_ConfigureBlobstores_InternalCARecoveryReachesCFAndBOSH
// is a regression test for an ordering bug: configureExternalBlobstore used
// to write the CF blobstore's ca_cert field BEFORE ensureBlobstoreBucket (via
// blobstoreS3Target) ever ran the internal-ca vault recovery that populates
// p.blobstoreCACert, so on exactly the scenario the recovery exists for
// (internal-ca, state ca_cert empty, CA recoverable from vault) the CF entry
// was written with no ca_cert while the BOSH entry — written after bucket
// creation had already triggered recovery — got it. resolveBlobstoreTLSTrust
// must now run (once) before either config write, so both entries agree.
func TestPVEVaultProvider_ConfigureBlobstores_InternalCARecoveryReachesCFAndBOSH(t *testing.T) {
	const blocName = "test-bloc"

	safe := newFakeSafe()

	caCert := "-----BEGIN CERTIFICATE-----\nrecovered-for-cf-and-bosh\n-----END CERTIFICATE-----\n"
	require.NoError(t, safe.SetMultiple(blocCAPath(blocName), map[string]any{
		"cert":        caCert,
		"key":         "-----BEGIN EC PRIVATE KEY-----\nkey\n-----END EC PRIVATE KEY-----\n",
		"fingerprint": "deadbeef",
	}))

	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProviderWithFakeSafe(cfg, safe)
	provider.BlobstoreMode = "external"
	provider.BlobstoreEndpoint = "https://10.64.64.11:9000"
	provider.BlobstoreAccessKey = "ak"
	provider.BlobstoreSecretKey = "sk"
	provider.blobstoreTLSMode = config.ArtifactsTLSModeInternalCA
	// blobstoreCACert deliberately left empty: state's copy is missing, vault
	// has it — the exact scenario the recovery heal targets.

	err := provider.ConfigureBlobstores("", MgmtEnvType, nil, 0, 1)
	require.NoError(t, err)

	cfPath := provider.PathBuilder.GetSystemBlobstorePath(MgmtEnvType, "cf", "main")
	boshPath := provider.PathBuilder.GetSystemBlobstorePath(MgmtEnvType, "bosh", "bosh")

	cfCACert, err := safe.Get(cfPath, "ca_cert")
	require.NoError(t, err)
	assert.Equal(t, caCert, cfCACert, "CF blobstore entry must carry the vault-recovered CA")

	boshCACert, err := safe.Get(boshPath, "ca_cert")
	require.NoError(t, err)
	assert.Equal(t, caCert, boshCACert, "BOSH blobstore entry must carry the vault-recovered CA")

	assert.Equal(t, caCert, provider.blobstoreCACert, "recovered CA must be cached on the provider")
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
		Properties: map[string]any{
			"cidr":              "10.64.64.0/22",
			"availability_zone": "",
			"gateway":           "10.64.64.1",
		},
	}))
	require.NoError(t, sm.AddResource(&state.Resource{
		ID:   "subnet-ocfp-0",
		Type: "subnet",
		Name: blocName + "-ocfp-0",
		Properties: map[string]any{
			"cidr":              "10.64.68.0/22",
			"availability_zone": "pvea",
			// Bootstrap records the PARENT /18 gateway here
			// (network.go addVirtualSubnetToState -> CIDRGatewayIP(parentCIDR)).
			// ConfigureSubnets must override it with the subnet's OWN /22 gateway
			// so BOSH's "gateway must be inside range" check passes and the PVE
			// SDN's per-/22 gateway (.68.1) is used.
			"gateway": "10.64.64.1",
		},
	}))
	require.NoError(t, sm.Save())

	mock := &awsMockSafe{}
	cfg := &config.Config{VPCCIDRBlock: "10.64.64.0/19", DNS: []string{"10.64.64.1"}}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.ConfigureSubnets("", MgmtEnvType, nil, 0, 1)
	require.NoError(t, err)

	subnetsPath := provider.PathBuilder.GetSubnetsPath(MgmtEnvType)

	// Genesis consumes bloc-relative names; the provider strips the bloc prefix.
	infraPath := filepath.Join(subnetsPath, "infra")
	infraCall := mock.findSetMultipleCall(infraPath)
	require.NotNil(t, infraCall, "infra subnet must be written at %s", infraPath)
	assert.Equal(t, "10.64.64.0/22", infraCall.data["cidr"])
	assert.Equal(t, "10.64.64.1", infraCall.data["gateway"])
	assert.Equal(t, "", infraCall.data["az"])
	// Per-subnet DNS must be written so genesis's dynamic-subnet cloud-config
	// builder does not emit dns: [null]. With config DNS set, every subnet uses it.
	assert.Equal(t, "10.64.64.1", infraCall.data["dns"])

	ocfp0Path := filepath.Join(subnetsPath, "ocfp-0")
	ocfp0Call := mock.findSetMultipleCall(ocfp0Path)
	require.NotNil(t, ocfp0Call, "ocfp-0 subnet must be written at %s", ocfp0Path)
	assert.Equal(t, "10.64.68.0/22", ocfp0Call.data["cidr"])
	assert.Equal(t, "pvea", ocfp0Call.data["az"])
	// Overridden from the parent .64.1 in state to the subnet's own /22 gateway.
	assert.Equal(t, "10.64.68.1", ocfp0Call.data["gateway"])
	assert.Equal(t, "10.64.64.1", ocfp0Call.data["dns"])

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

	// The fallback per-subnet entries must carry gateway and dns derived from
	// the CIDR, so genesis builds a valid subnet (no dns: [null], correct gw).
	ocfp0Path := provider.PathBuilder.GetSubnetPath(MgmtEnvType, "ocfp", 0)
	ocfp0 := mock.findSetMultipleCall(ocfp0Path)
	require.NotNil(t, ocfp0, "fallback ocfp-0 subnet must be written at %s", ocfp0Path)
	assert.Equal(t, "10.64.64.1", ocfp0.data["gateway"])
	assert.Equal(t, "10.64.64.1", ocfp0.data["dns"])
}

// TestPVEVaultProvider_ConfigureSubnets_DerivesGatewayAndDNSWhenAbsent — when a
// state subnet omits gateway and no config DNS is set, ConfigureSubnets derives
// both from the subnet CIDR (gateway = first host; dns = gateway) so the
// genesis cloud-config builder always has a usable gateway and resolver.
func TestPVEVaultProvider_ConfigureSubnets_DerivesGatewayAndDNSWhenAbsent(t *testing.T) {
	const blocName = "test-bloc"

	sm := seedPVEState(t, blocName)
	require.NoError(t, sm.AddResource(&state.Resource{
		ID:   "subnet-ocfp-0",
		Type: "subnet",
		Name: blocName + "-ocfp-0",
		Properties: map[string]any{
			"cidr":              "10.64.68.0/22",
			"availability_zone": "pvea",
			"gateway":           "",
		},
	}))
	require.NoError(t, sm.Save())

	mock := &awsMockSafe{}
	cfg := &config.Config{VPCCIDRBlock: "10.64.64.0/19"}
	provider := newTestPVEProvider(cfg, mock)

	require.NoError(t, provider.ConfigureSubnets("", MgmtEnvType, nil, 0, 1))

	subnetsPath := provider.PathBuilder.GetSubnetsPath(MgmtEnvType)
	ocfp0 := mock.findSetMultipleCall(filepath.Join(subnetsPath, "ocfp-0"))
	require.NotNil(t, ocfp0, "ocfp-0 subnet must be written")
	assert.Equal(t, "10.64.68.1", ocfp0.data["gateway"], "gateway derived from CIDR")
	assert.Equal(t, "10.64.68.1", ocfp0.data["dns"], "dns derived from subnet gateway")
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
		Properties: map[string]any{
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
		"infra",
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

	// In addition to the per-role sub-paths, the role IPs must also be written
	// as `{role}_ip` KEYS in the reserved-ips secret itself — this is the
	// convention the genesis kits read (e.g. doomsday reads
	// reserved-ips:doomsday_ip, vault reads reserved-ips:vault_ip). Without the
	// key form, only AWS/STACKIT (which write keys via network.go) work, and the
	// kits break on PVE.
	reservedKeysPath := filepath.Join(subnetPath, "reserved-ips")
	reservedKeysCall := mock.findSetMultipleCall(reservedKeysPath)
	require.NotNil(t, reservedKeysCall,
		"role IPs must also be written as keys at %s", reservedKeysPath)
	assert.Equal(t, "10.64.64.10", reservedKeysCall.data["bastion_ip"])
	assert.Equal(t, "10.64.64.11", reservedKeysCall.data["jumpbox_ip"])
	// Empty-valued outputs must not surface as keys.
	_, hasEmpty := reservedKeysCall.data["empty_ip"]
	assert.False(t, hasEmpty, "empty-valued reserved IP must not produce a key")
}

// TestPVEVaultProvider_ConfigureSubnets_AvailableBandDefaults — the fallback
// subnet write must emit available_0/available_1 reserved-ips keys so Genesis'
// cloud-config IPAM (_get_subnet_ranges) confines kit-generated networks to a
// band that clears the infra IPs. With no explicit config the band defaults to
// the mgmt tier's map offsets (32-63, see pve_reserved_ips.go) and is split
// into three DISJOINT per-subnet slices so compilation (ocfp-2) never
// overlaps workload (ocfp-0/1).
func TestPVEVaultProvider_ConfigureSubnets_AvailableBandDefaults(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OCFP_HOME", tmp)

	mock := &awsMockSafe{}
	cfg := &config.Config{VPCCIDRBlock: "10.64.64.0/19"}
	provider := newTestPVEProvider(cfg, mock)

	require.NoError(t, provider.ConfigureSubnets("", MgmtEnvType, nil, 0, 1))

	// Default mgmt band .32-.63 (32 IPs) splits into 10-IP contiguous slices.
	band0 := mock.findSetMultipleCall(provider.PathBuilder.GetReservedIPsPath(MgmtEnvType, "ocfp", 0))
	band1 := mock.findSetMultipleCall(provider.PathBuilder.GetReservedIPsPath(MgmtEnvType, "ocfp", 1))
	band2 := mock.findSetMultipleCall(provider.PathBuilder.GetReservedIPsPath(MgmtEnvType, "ocfp", 2))
	require.NotNil(t, band0)
	require.NotNil(t, band1)
	require.NotNil(t, band2)

	assert.Equal(t, "10.64.64.32", band0.data["available_0"], "first slice starts at band start")
	assert.Equal(t, "10.64.64.41", band0.data["available_1"])
	assert.Equal(t, "10.64.64.42", band1.data["available_0"], "second slice begins after the first")
	assert.Equal(t, "10.64.64.51", band1.data["available_1"])
	assert.Equal(t, "10.64.64.52", band2.data["available_0"], "compilation slice begins after the second")
	assert.Equal(t, "10.64.64.63", band2.data["available_1"], "last slice absorbs the remainder to band end")
}

// TestPVEVaultProvider_ConfigureSubnets_AvailableBandDefaults_OCFTier — the
// same fallback write for the ocf tier must use ocf's disjoint default band
// (96-> the /19's last usable host), never mgmt's 32-63.
func TestPVEVaultProvider_ConfigureSubnets_AvailableBandDefaults_OCFTier(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OCFP_HOME", tmp)

	mock := &awsMockSafe{}
	cfg := &config.Config{VPCCIDRBlock: "10.64.64.0/19"}
	provider := newTestPVEProvider(cfg, mock)

	require.NoError(t, provider.ConfigureSubnets("", "ocf", nil, 0, 1))

	band0 := mock.findSetMultipleCall(provider.PathBuilder.GetReservedIPsPath("ocf", "ocfp", 0))
	require.NotNil(t, band0)
	assert.Equal(t, "10.64.64.96", band0.data["available_0"], "ocf band starts at offset 96, disjoint from mgmt's 32-63")
}

// TestPVEVaultProvider_ConfigureSubnets_AvailableBandExplicit — explicit
// network.availableIpStart/End override the derived band, still split into
// disjoint per-subnet slices.
func TestPVEVaultProvider_ConfigureSubnets_AvailableBandExplicit(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OCFP_HOME", tmp)

	mock := &awsMockSafe{}
	cfg := &config.Config{
		VPCCIDRBlock: "10.64.64.0/19",
		Network: config.NetworkConfig{
			AvailableIPStart: "10.64.64.20",
			AvailableIPEnd:   "10.64.64.49",
		},
	}
	provider := newTestPVEProvider(cfg, mock)

	require.NoError(t, provider.ConfigureSubnets("", MgmtEnvType, nil, 0, 1))

	// Band .20-.49 (30 IPs) -> 10-IP slices: ocfp-0 .20-.29, ocfp-2 .40-.49.
	band0 := mock.findSetMultipleCall(provider.PathBuilder.GetReservedIPsPath(MgmtEnvType, "ocfp", 0))
	band2 := mock.findSetMultipleCall(provider.PathBuilder.GetReservedIPsPath(MgmtEnvType, "ocfp", 2))
	require.NotNil(t, band0)
	require.NotNil(t, band2)
	assert.Equal(t, "10.64.64.20", band0.data["available_0"])
	assert.Equal(t, "10.64.64.29", band0.data["available_1"])
	assert.Equal(t, "10.64.64.40", band2.data["available_0"])
	assert.Equal(t, "10.64.64.49", band2.data["available_1"])
}

// TestPVEVaultProvider_ConfigureSubnets_OCFTierComputedFromCIDR — a
// state-backed ocfp-* subnet must write its genesis-consumed reserved-ips
// block at the bloc-stripped path, with available_0/available_1 and every
// named static computed from the subnet's OWN /22 CIDR plus the ocf-tier
// assignment table (pve_reserved_ips.go), NOT from bootstrap's tier-blind
// reserved_<name>_* state outputs — those are left in state here specifically
// to prove they are now ignored (the whole point of the tiered-map change:
// mgmt and ocf must never again derive the same physical IP from one shared
// bootstrap computation).
func TestPVEVaultProvider_ConfigureSubnets_OCFTierComputedFromCIDR(t *testing.T) {
	const blocName = "test-bloc"

	sm := seedPVEState(t, blocName)
	require.NoError(t, sm.AddResource(&state.Resource{
		ID:   "subnet-ocfp-2",
		Type: "subnet",
		Name: blocName + "-ocfp-2",
		Properties: map[string]any{
			"cidr":              "10.64.76.0/22",
			"availability_zone": "pvec",
			"gateway":           "10.64.64.1",
			"network_id":        "lvnet001",
		},
	}))
	// Stale tier-blind state outputs from the pre-tiered-map layout: must be
	// ignored by the new architecture, not echoed into vault.
	require.NoError(t, sm.SetOutput("reserved_"+blocName+"-ocfp-2_available_a", "10.64.76.12"))
	require.NoError(t, sm.SetOutput("reserved_"+blocName+"-ocfp-2_available_b", "10.64.76.29"))
	require.NoError(t, sm.SetOutput("reserved_"+blocName+"-ocfp-2_bosh_ip", "10.64.76.4"))
	require.NoError(t, sm.SetOutput("reserved_"+blocName+"-ocfp-2_jumpbox_ip", "10.64.76.6"))
	require.NoError(t, sm.Save())

	mock := &awsMockSafe{}
	cfg := &config.Config{VPCCIDRBlock: "10.64.64.0/19"}
	provider := newTestPVEProvider(cfg, mock)

	require.NoError(t, provider.ConfigureSubnets("", "ocf", nil, 0, 1))

	subnetsPath := provider.PathBuilder.GetSubnetsPath("ocf")

	// Subnet entry written at the bloc-stripped genesis name with the distinct /22.
	subnet := mock.findSetMultipleCall(filepath.Join(subnetsPath, "ocfp-2"))
	require.NotNil(t, subnet, "ocfp-2 subnet entry must be written at the stripped path")
	assert.Equal(t, "10.64.76.0/22", subnet.data["cidr"])
	assert.Equal(t, "10.64.76.0/22", subnet.data["cidr_block"])
	assert.Equal(t, "lvnet001", subnet.data["id"])

	// Reserved band computed from the subnet's own CIDR + the ocf-tier table,
	// NOT from the stale state outputs seeded above.
	band := mock.findSetMultipleCall(filepath.Join(subnetsPath, "ocfp-2", "reserved-ips"))
	require.NotNil(t, band, "ocfp-2 reserved-ips band must be written")
	assert.Equal(t, "10.64.76.96", band.data["available_0"], "ocf available band starts at offset 96, not the stale state value")
	assert.Equal(t, "10.64.79.254", band.data["available_1"], "ocf available band runs open-ended to the /22's last usable host")
	assert.Equal(t, "10.64.76.64", band.data["bosh_ip"], "bosh_ip at the ocf-tier offset, not the stale state value")
	assert.Equal(t, "10.64.76.64", band.data["director_ip"])
	assert.Equal(t, "10.64.76.66", band.data["jumpbox_ip"], "jumpbox_ip at the ocf-tier offset (66), not mgmt's offset (6)")
	assert.Equal(t, "10.64.76.68", band.data["haproxy_ip"], "haproxy_ip at the fixed ocf-tier offset")
}

// TestPVEVaultProvider_ConfigureSubnets_MgmtOcfDisjointOnSharedSubnet is the
// integration-level acceptance test for the plan's root-cause fix: given the
// SAME physical subnet CIDR, ConfigureSubnets must write DIFFERENT,
// non-overlapping reserved-ips into the mgmt and ocf vault trees, so Genesis'
// per-director cloud-config claims ledgers can never collide on a shared
// subnet (plans/pve-tiered-reserved-ip-map.md).
func TestPVEVaultProvider_ConfigureSubnets_MgmtOcfDisjointOnSharedSubnet(t *testing.T) {
	const blocName = "test-bloc"

	sm := seedPVEState(t, blocName)
	require.NoError(t, sm.AddResource(&state.Resource{
		ID:   "subnet-ocfp-0",
		Type: "subnet",
		Name: blocName + "-ocfp-0",
		Properties: map[string]any{
			"cidr":              "10.64.68.0/22",
			"availability_zone": "pvea",
			"network_id":        "lvnet001",
		},
	}))
	require.NoError(t, sm.Save())

	mgmtMock := &awsMockSafe{}
	provider := newTestPVEProvider(&config.Config{VPCCIDRBlock: "10.64.64.0/19"}, mgmtMock)
	require.NoError(t, provider.ConfigureSubnets("", MgmtEnvType, nil, 0, 1))

	ocfMock := &awsMockSafe{}
	provider = newTestPVEProvider(&config.Config{VPCCIDRBlock: "10.64.64.0/19"}, ocfMock)
	require.NoError(t, provider.ConfigureSubnets("", "ocf", nil, 0, 1))

	mgmtBand := mgmtMock.findSetMultipleCall(filepath.Join(provider.PathBuilder.GetSubnetsPath(MgmtEnvType), "ocfp-0", "reserved-ips"))
	ocfBand := ocfMock.findSetMultipleCall(filepath.Join(provider.PathBuilder.GetSubnetsPath("ocf"), "ocfp-0", "reserved-ips"))
	require.NotNil(t, mgmtBand)
	require.NotNil(t, ocfBand)

	assert.NotEqual(t, mgmtBand.data["bosh_ip"], ocfBand.data["bosh_ip"])
	assert.Equal(t, "10.64.68.4", mgmtBand.data["bosh_ip"])
	assert.Equal(t, "10.64.68.64", ocfBand.data["bosh_ip"])

	assert.NotEqual(t, mgmtBand.data["available_0"], ocfBand.data["available_0"])
	assert.Equal(t, "10.64.68.32", mgmtBand.data["available_0"])
	assert.Equal(t, "10.64.68.96", ocfBand.data["available_0"])
}

// TestPVEVaultProvider_ConfigureBlobstores_AutoSourcesFromArtifactsState — with
// no blobstore flags but an artifacts resource in bootstrap state,
// ConfigureBlobstores promotes to external mode and writes the full CF + BOSH
// blobstore secrets (endpoint, region, ca_cert, bucket, creds) sourced from
// state. Secrets are written to vault but never logged.
func TestPVEVaultProvider_ConfigureBlobstores_AutoSourcesFromArtifactsState(t *testing.T) {
	const blocName = "test-bloc"

	sm := seedPVEState(t, blocName)
	require.NoError(t, sm.AddResource(&state.Resource{
		ID:   "artifacts",
		Type: "artifacts",
		Name: blocName + "-artifacts",
		Properties: map[string]any{
			"endpoint":   "https://10.64.68.11:9000",
			"private_ip": "10.64.68.11",
			"access_key": "AKIA-state",
			"secret_key": "secret-state",
			"ca_cert":    "-----BEGIN CERTIFICATE-----\nMII...\n-----END CERTIFICATE-----",
			"tls_mode":   "internal-ca",
		},
	}))
	require.NoError(t, sm.Save())

	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)

	// No --blobstore-* flags set: must auto-source from artifacts state.
	require.NoError(t, provider.ConfigureBlobstores("", "ocf", nil, 0, 1))

	cfPath := provider.PathBuilder.GetSystemBlobstorePath("ocf", "cf", "main")
	cfCall := mock.findSetMultipleCall(cfPath)
	require.NotNil(t, cfCall, "CF blobstore config must be written from state")
	assert.Equal(t, "external", cfCall.data["mode"])
	assert.Equal(t, "https://10.64.68.11:9000", cfCall.data["endpoint"])
	assert.Equal(t, "10.64.68.11", cfCall.data["host"])
	assert.Equal(t, "us-east-1", cfCall.data["region"])
	assert.Equal(t, blocName+"-ocf-cf", cfCall.data["bucket"])
	assert.Contains(t, cfCall.data, "ca_cert", "ca_cert must be written from state")
	assert.NotContains(t, cfCall.data, "access_key", "config path must stay secret-free")

	cfCreds := mock.findSetMultipleCall(cfPath + "/creds")
	require.NotNil(t, cfCreds, "CF blobstore creds must be written")
	assert.Equal(t, "AKIA-state", cfCreds.data["access_key"])
	assert.Equal(t, "secret-state", cfCreds.data["secret_key"])

	// ocf scope must also write the ocf-scoped BOSH blobstore for env-BOSH.
	boshPath := provider.PathBuilder.GetSystemBlobstorePath("ocf", "bosh", "bosh")
	boshCall := mock.findSetMultipleCall(boshPath)
	require.NotNil(t, boshCall, "ocf BOSH blobstore must be written")
	assert.Equal(t, blocName+"-ocf-bosh", boshCall.data["name"])
	assert.Equal(t, "10.64.68.11", boshCall.data["host"])
}

// TestPVEVaultProvider_ConfigureBlobstores_AutoSourceWritesArtifactsMeta is a
// regression test: `ocfp vault populate` runs ON the bastion against the
// bastion's own inception vault, which WriteArtifacts (the workstation-side
// writer of secret/ocfp/{bloc}/artifacts) never touches. Without this write,
// scripts/blobstores' `safe get secret/ocfp/<bloc>/artifacts:endpoint` finds
// nothing on the bastion and the script exits silently even though the
// blobstore config itself is fully populated.
func TestPVEVaultProvider_ConfigureBlobstores_AutoSourceWritesArtifactsMeta(t *testing.T) {
	const blocName = "test-bloc"

	sm := seedPVEState(t, blocName)
	require.NoError(t, sm.AddResource(&state.Resource{
		ID:   "artifacts",
		Type: "artifacts",
		Name: blocName + "-artifacts",
		Properties: map[string]any{
			"endpoint":               "https://10.64.68.11:9000",
			"private_ip":             "10.64.68.11",
			"access_key":             "AKIA-state",
			"secret_key":             "secret-state",
			"ca_cert":                "-----BEGIN CERTIFICATE-----\nMII...\n-----END CERTIFICATE-----",
			"tls_mode":               "internal-ca",
			"tls_fingerprint_sha256": "deadbeefcafe",
			"tls_leaf_not_after":     "2027-06-01T00:00:00Z",
		},
	}))
	require.NoError(t, sm.Save())

	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)

	require.NoError(t, provider.ConfigureBlobstores("", "ocf", nil, 0, 1))

	metaCall := mock.findSetMultipleCall(artifactsMetaPath(blocName))
	require.NotNil(t, metaCall, "artifacts meta must be written at %s on the auto-source path", artifactsMetaPath(blocName))
	assert.Equal(t, "https://10.64.68.11:9000", metaCall.data["endpoint"])
	assert.Equal(t, "10.64.68.11", metaCall.data["host"])
	assert.Equal(t, 9000, metaCall.data["port"])
	assert.Equal(t, "internal-ca", metaCall.data["tls_mode"])
	assert.Equal(t, "deadbeefcafe", metaCall.data["tls_fingerprint_sha256"])
	assert.Equal(t, "2027-06-01T00:00:00Z", metaCall.data["tls_leaf_not_after"])
}

// TestPVEVaultProvider_ConfigureBlobstores_AutoSourceOmitsEmptyFingerprintAndNotAfter
// mirrors WriteArtifacts' omit-when-empty contract for the two operator/status
// fields: a state resource that predates leaf-expiry tracking (no
// tls_fingerprint_sha256/tls_leaf_not_after properties) must not get those
// keys written as empty strings.
func TestPVEVaultProvider_ConfigureBlobstores_AutoSourceOmitsEmptyFingerprintAndNotAfter(t *testing.T) {
	const blocName = "test-bloc"

	sm := seedPVEState(t, blocName)
	require.NoError(t, sm.AddResource(&state.Resource{
		ID:   "artifacts",
		Type: "artifacts",
		Name: blocName + "-artifacts",
		Properties: map[string]any{
			"endpoint":   "https://10.64.68.11:9000",
			"private_ip": "10.64.68.11",
			"access_key": "AKIA-state",
			"secret_key": "secret-state",
			"ca_cert":    "-----BEGIN CERTIFICATE-----\nMII...\n-----END CERTIFICATE-----",
			"tls_mode":   "internal-ca",
			// No tls_fingerprint_sha256 / tls_leaf_not_after.
		},
	}))
	require.NoError(t, sm.Save())

	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)

	require.NoError(t, provider.ConfigureBlobstores("", "ocf", nil, 0, 1))

	metaCall := mock.findSetMultipleCall(artifactsMetaPath(blocName))
	require.NotNil(t, metaCall, "artifacts meta must still be written")
	assert.Equal(t, "https://10.64.68.11:9000", metaCall.data["endpoint"])
	assert.NotContains(t, metaCall.data, "tls_fingerprint_sha256", "empty fingerprint must be omitted, not written as \"\"")
	assert.NotContains(t, metaCall.data, "tls_leaf_not_after", "empty leaf-expiry must be omitted, not written as \"\"")
}

// TestPVEVaultProvider_ConfigureBlobstores_FlagDrivenExternalMode_NoMetaWrite
// — an operator-supplied --blobstore-endpoint has no backing artifacts state
// to source metadata from, so the auto-source path (and its meta write) must
// not run; writing guessed/partial metadata would be worse than writing none.
func TestPVEVaultProvider_ConfigureBlobstores_FlagDrivenExternalMode_NoMetaWrite(t *testing.T) {
	const blocName = "test-bloc"

	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)
	provider.BlobstoreMode = "external"
	provider.BlobstoreEndpoint = "https://s3.example.com"
	provider.BlobstoreAccessKey = "AKIA-test"
	provider.BlobstoreSecretKey = "secret-test"

	require.NoError(t, provider.ConfigureBlobstores("", MgmtEnvType, nil, 0, 1))

	metaCall := mock.findSetMultipleCall(artifactsMetaPath(blocName))
	assert.Nil(t, metaCall, "flag-driven external mode must not write artifacts meta")
}

// TestPVEVaultProvider_ConfigureBlobstores_LocalMode_NoMetaWrite — local mode
// (no endpoint, no state to auto-source from) writes only the local-mode
// marker; it must not write artifacts meta either.
func TestPVEVaultProvider_ConfigureBlobstores_LocalMode_NoMetaWrite(t *testing.T) {
	const blocName = "test-bloc"

	// Empty OCFP_HOME: no bootstrap state, so the auto-source lookup finds
	// nothing and ConfigureBlobstores stays in local mode.
	tmp := t.TempDir()
	t.Setenv("OCFP_HOME", tmp)

	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "pve-node1"}
	provider := newTestPVEProvider(cfg, mock)

	require.NoError(t, provider.ConfigureBlobstores("", MgmtEnvType, nil, 0, 1))

	metaCall := mock.findSetMultipleCall(artifactsMetaPath(blocName))
	assert.Nil(t, metaCall, "local mode must not write artifacts meta")
}

// ---------------------------------------------------------------------------
// IMP-10: Storage backend classification + VMStorage/DiskStorage Config fields
// ---------------------------------------------------------------------------

// T29 TestPVEStorageBackend_LVMThin_ReturnsBlock verifies lvmthin classifies
// as "block" storage backend.
func TestPVEStorageBackend_LVMThin_ReturnsBlock(t *testing.T) {
	t.Parallel()

	got := pveStorageBackend("lvmthin")
	assert.Equal(t, "block", got)
}

// T29b TestPVEStorageBackend_ZFSPool_ReturnsBlock verifies zfspool classifies
// as "block" storage backend.
func TestPVEStorageBackend_ZFSPool_ReturnsBlock(t *testing.T) {
	t.Parallel()

	got := pveStorageBackend("zfspool")
	assert.Equal(t, "block", got)
}

// T30 TestPVEStorageBackend_NFS_ReturnsShared verifies nfs classifies as
// "shared" storage backend.
func TestPVEStorageBackend_NFS_ReturnsShared(t *testing.T) {
	t.Parallel()

	got := pveStorageBackend("nfs")
	assert.Equal(t, "shared", got)
}

// TestPVEStorageBackend_SharedTypes verifies all shared types return "shared".
func TestPVEStorageBackend_SharedTypes(t *testing.T) {
	t.Parallel()

	shared := []string{"rbd", "cephfs", "nfs", "cifs", "glusterfs", "pbs"}
	for _, s := range shared {
		s := s
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, "shared", pveStorageBackend(s), "storage type %q should be shared", s)
		})
	}
}

// T29c TestPVEStorageBackend_ZFSPool_DiskFormatRaw verifies zfspool requires
// raw disk format (qcow2 not supported on block devices).
func TestPVEStorageBackend_ZFSPool_DiskFormatRaw(t *testing.T) {
	t.Parallel()

	got := pveDiskFormat("zfspool")
	assert.Equal(t, "raw", got, "zfspool block devices require disk_format: raw")
}

// T29d TestPVEStorageBackend_Unknown_DefaultsToBlock verifies unknown pool
// names default to "block" (conservative safe default).
func TestPVEStorageBackend_Unknown_DefaultsToBlock(t *testing.T) {
	t.Parallel()

	got := pveStorageBackend("some-unknown-pool")
	assert.Equal(t, "block", got, "unknown pool type must default to block (conservative)")
}

// T29e TestConfigureCPI_VMStorageField_WritesVmStorageKey verifies that when
// Config.VMStorage is set, configureCPI writes that value to the vm_storage
// vault key.
func TestConfigureCPI_VMStorageField_WritesVmStorageKey(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!tok",
		TokenSecret: "secret",
		Region:      "pve01",
		VMStorage:   "data",
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data
	assert.Equal(t, "data", data["vm_storage"], "vm_storage must reflect Config.VMStorage")
}

// T29f TestConfigureCPI_VMStorageEmpty_FallsBackToBlobstorePool verifies that
// when Config.VMStorage is empty and Artifacts.Data.StoragePool is set,
// configureCPI falls back to Artifacts.Data.StoragePool for vm_storage.
func TestConfigureCPI_VMStorageEmpty_FallsBackToBlobstorePool(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!tok",
		TokenSecret: "secret",
		Region:      "pve01",
		// VMStorage empty — should fall back to Artifacts.Data.StoragePool.
		Artifacts: config.ArtifactsConfig{
			Data: config.ArtifactsDataConfig{
				StoragePool: "local-zfs",
			},
		},
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data
	assert.Equal(t, "local-zfs", data["vm_storage"], "vm_storage must fall back to Artifacts.Data.StoragePool when VMStorage is empty")
}

// TestConfigureCPI_DiskStorageEmpty_FallsBackToBlobstorePool verifies that
// when Config.DiskStorage is empty and Artifacts.Data.StoragePool is set,
// configureCPI falls back to Artifacts.Data.StoragePool for disk_storage.
func TestConfigureCPI_DiskStorageEmpty_FallsBackToBlobstorePool(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!tok",
		TokenSecret: "secret",
		Region:      "pve01",
		// DiskStorage empty — should fall back to Artifacts.Data.StoragePool.
		Artifacts: config.ArtifactsConfig{
			Data: config.ArtifactsDataConfig{
				StoragePool: "local-zfs",
			},
		},
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data
	assert.Equal(t, "local-zfs", data["disk_storage"], "disk_storage must fall back to Artifacts.Data.StoragePool when DiskStorage is empty")
}

// TestConfigureCPI_DiskStorageField_WritesStorageBackendAndFormat verifies that
// when Config.DiskStorage is set, the storage_backend and disk_format keys
// reflect the classification of that storage type.
func TestConfigureCPI_DiskStorageField_WritesStorageBackendAndFormat(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!tok",
		TokenSecret: "secret",
		Region:      "pve01",
		DiskStorage: "zfs-1",
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data
	assert.Equal(t, "zfs-1", data["disk_storage"])
	// zfs-1 is an alias for zfspool storage; storage_backend defaults to "block"
	// for unknown pool names (conservative safe default).
	assert.Equal(t, "block", data["storage_backend"], "unknown pool name zfs-1 must default storage_backend to block")
	// zfs-1 contains the "zfs" marker, so the substring heuristic classifies
	// it as a block backend requiring raw format (zfspool rejects qcow2).
	assert.Equal(t, "raw", data["disk_format"], "pool names containing zfs must map to disk_format: raw")
}

// TestConfigureCPI_DiskStorageZfspool_WritesRawFormat verifies that when
// DiskStorage is set to the literal "zfspool" type keyword, disk_format is "raw"
// and storage_backend is "block".
func TestConfigureCPI_DiskStorageZfspool_WritesRawFormat(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!tok",
		TokenSecret: "secret",
		Region:      "pve01",
		DiskStorage: "zfspool",
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data
	assert.Equal(t, "block", data["storage_backend"])
	assert.Equal(t, "raw", data["disk_format"], "zfspool type requires disk_format: raw")
}

// TestConfigureCPI_WritesAllExpectedStorageKeys verifies every required storage
// key is present in the vault write for completeness (T29 range check).
func TestConfigureCPI_WritesAllExpectedStorageKeys(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!tok",
		TokenSecret: "secret",
		Region:      "pve01",
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data
	requiredKeys := []string{
		"disk_format",
		"disk_storage",
		"storage_backend",
		"vm_storage",
	}
	for _, k := range requiredKeys {
		_, ok := data[k]
		assert.True(t, ok, "cpiConfig must contain key %q", k)
	}
}

// TestConfigureCPI_WritesVmidRangeEnd (T05) — when VmidRangeEnd is set in
// Config, configureCPI writes the exact value to vmid_range_end in the vault
// payload.
func TestConfigureCPI_WritesVmidRangeEnd(t *testing.T) {
	t.Parallel()

	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint:  "https://pve.example.com:8006",
		AuthToken:    "root@pam!tok",
		TokenSecret:  "secret",
		Region:       "pve01",
		VmidRangeEnd: 4000,
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data
	assert.Equal(t, "4000", data["vmid_range_end"],
		"vmid_range_end must reflect Config.VmidRangeEnd when set")
}

// TestPVEVMIDRangeStart_UsesConfigValue (T06) — pveVMIDRangeStart returns the
// configured value when Config.VmidRangeStart is non-zero.
func TestPVEVMIDRangeStart_UsesConfigValue(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{VmidRangeStart: 500}
	got := pveVMIDRangeStart(cfg)
	assert.Equal(t, 500, got, "pveVMIDRangeStart must return Config.VmidRangeStart when non-zero")
}

// TestPVEVMIDRangeStart_FallsBackTo100 (T06b) — pveVMIDRangeStart returns 100
// when Config.VmidRangeStart is zero (unset).
func TestPVEVMIDRangeStart_FallsBackTo100(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{VmidRangeStart: 0}
	got := pveVMIDRangeStart(cfg)
	assert.Equal(t, 100, got, "pveVMIDRangeStart must return 100 when VmidRangeStart is zero")
}

// TestPVEVMIDRangeEnd_ReadsConfig (T05) — pveVMIDRangeEnd returns the
// configured value when Config.VmidRangeEnd is non-zero.
func TestPVEVMIDRangeEnd_ReadsConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{VmidRangeEnd: 4000}
	got := pveVMIDRangeEnd(cfg)
	assert.Equal(t, 4000, got, "pveVMIDRangeEnd must return Config.VmidRangeEnd when non-zero")
}

// TestPVEVMIDRangeEnd_Default (T06) — pveVMIDRangeEnd returns 5999 when
// Config.VmidRangeEnd is zero (unset).
func TestPVEVMIDRangeEnd_Default(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{VmidRangeEnd: 0}
	got := pveVMIDRangeEnd(cfg)
	assert.Equal(t, 5999, got, "pveVMIDRangeEnd must return 5999 when VmidRangeEnd is zero")
}

// TestPVEVMIDRangeEnd_NilConfig — pveVMIDRangeEnd returns 5999 when Config is nil.
func TestPVEVMIDRangeEnd_NilConfig(t *testing.T) {
	t.Parallel()

	got := pveVMIDRangeEnd(nil)
	assert.Equal(t, 5999, got, "pveVMIDRangeEnd must return 5999 for nil Config")
}

// TestPVEVMIDRangeStart_NilConfig — pveVMIDRangeStart returns 100 when Config is nil.
func TestPVEVMIDRangeStart_NilConfig(t *testing.T) {
	t.Parallel()

	got := pveVMIDRangeStart(nil)
	assert.Equal(t, 100, got, "pveVMIDRangeStart must return 100 for nil Config")
}

// TestConfigureCPI_VmidRangeStart_ReadsConfig (T05b) — when VmidRangeStart is
// set in Config, configureCPI writes the exact value to vmid_range_start.
func TestConfigureCPI_VmidRangeStart_ReadsConfig(t *testing.T) {
	t.Parallel()

	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint:    "https://pve.example.com:8006",
		AuthToken:      "root@pam!tok",
		TokenSecret:    "secret",
		Region:         "pve01",
		VmidRangeStart: 300,
		VmidRangeEnd:   5999,
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data
	assert.Equal(t, "300", data["vmid_range_start"],
		"vmid_range_start must reflect Config.VmidRangeStart when set")
}

// TestConfigureCPI_VmidRangeDefaults — when both VmidRangeStart and VmidRangeEnd
// are zero, configureCPI writes the defaults (100 / 5999).
func TestConfigureCPI_VmidRangeDefaults(t *testing.T) {
	t.Parallel()

	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!tok",
		TokenSecret: "secret",
		Region:      "pve01",
		// VmidRangeStart and VmidRangeEnd both zero (unset).
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data
	assert.Equal(t, "100", data["vmid_range_start"],
		"default vmid_range_start must be 100 when VmidRangeStart is zero")
	assert.Equal(t, "5999", data["vmid_range_end"],
		"default vmid_range_end must be 5999 when VmidRangeEnd is zero")
}

// ---------------------------------------------------------------------------
// IMP-08: cf_max_in_flight (T24, T25)
// ---------------------------------------------------------------------------

// T24 TestConfigureCPI_WritesCfMaxInFlight_FromConfig verifies that when
// Config.CfMaxInFlight > 0, configureCPI writes that value to the vault key
// cf_max_in_flight as a string.
func TestConfigureCPI_WritesCfMaxInFlight_FromConfig(t *testing.T) {
	t.Parallel()

	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint:   "https://pve.example.com:8006",
		AuthToken:     "root@pam!tok",
		TokenSecret:   "secret",
		Region:        "pve01",
		CfMaxInFlight: 8,
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data
	assert.Equal(t, "8", data["cf_max_in_flight"],
		"cf_max_in_flight must reflect Config.CfMaxInFlight when set")
}

// T25 TestConfigureCPI_WritesCfMaxInFlight_DefaultWhenUnset verifies that when
// Config.CfMaxInFlight == 0 (unset), configureCPI writes the default value
// "12" to the vault key cf_max_in_flight.
func TestConfigureCPI_WritesCfMaxInFlight_DefaultWhenUnset(t *testing.T) {
	t.Parallel()

	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!tok",
		TokenSecret: "secret",
		Region:      "pve01",
		// CfMaxInFlight deliberately omitted (zero).
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data
	assert.Equal(t, "12", data["cf_max_in_flight"],
		"cf_max_in_flight must be the default 12 when Config.CfMaxInFlight is zero")
}

// TestConfigureCPIWritesInfraKeys is a regression guard: the cpi/pve vault map
// must always contain disk_format, network_bridge, and vm_storage as non-empty
// strings. Missing or empty values cause the BOSH PVE CPI to reject the config
// at director deploy time (e.g. network_bridge="" fails the CPI's validation).
func TestConfigureCPIWritesInfraKeys(t *testing.T) {
	t.Parallel()

	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!tok",
		TokenSecret: "secret",
		Region:      "pve01",
		// No VMStorage, DiskStorage, or Network.Name set: must still produce
		// non-empty values via defaults so the CPI config is always valid.
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	data := mock.setMultipleCalls[0].data

	infraKeys := []string{"disk_format", "network_bridge", "vm_storage"}
	for _, k := range infraKeys {
		v, ok := data[k]
		assert.True(t, ok, "cpi/pve vault map must contain key %q", k)
		s, isStr := v.(string)
		assert.True(t, isStr, "cpi/pve key %q must be a string, got %T", k, v)
		assert.NotEmpty(t, s, "cpi/pve key %q must be non-empty; empty value causes CPI validation failure at deploy time", k)
	}
}

// TestConfigureCPI_CfMaxInFlight_KeyAlwaysPresent verifies cf_max_in_flight is
// always present in the vault payload regardless of other config values.
func TestConfigureCPI_CfMaxInFlight_KeyAlwaysPresent(t *testing.T) {
	t.Parallel()

	mock := &awsMockSafe{}
	cfg := &config.Config{
		APIEndpoint: "https://pve.example.com:8006",
		AuthToken:   "root@pam!tok",
		TokenSecret: "secret",
		Region:      "pve01",
	}
	provider := newTestPVEProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)
	require.Len(t, mock.setMultipleCalls, 1)

	_, ok := mock.setMultipleCalls[0].data["cf_max_in_flight"]
	assert.True(t, ok, "cf_max_in_flight key must always be present in the CPI vault payload")
}
