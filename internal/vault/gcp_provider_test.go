package vault

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gcpMockSafe tracks SetMultiple calls for GCP provider tests.
type gcpMockSafe struct {
	setMultipleCalls []gcpSetCall
	failOnPath       string // if non-empty, SetMultiple returns error for this path
}

type gcpSetCall struct {
	path string
	data map[string]interface{}
}

func (m *gcpMockSafe) SetMultiple(path string, data map[string]interface{}) error {
	if m.failOnPath != "" && path == m.failOnPath {
		return &mockSafeError{path: path}
	}
	m.setMultipleCalls = append(m.setMultipleCalls, gcpSetCall{path: path, data: data})
	return nil
}

func (m *gcpMockSafe) Set(_, _ string, _ interface{}) error            { return nil }
func (m *gcpMockSafe) Get(_, _ string) (interface{}, error)            { return "", nil }
func (m *gcpMockSafe) GetAll(_ string) (map[string]interface{}, error) { return nil, nil }
func (m *gcpMockSafe) Exists(_ string) (bool, error)                   { return false, nil }
func (m *gcpMockSafe) Delete(_, _ string) error                        { return nil }
func (m *gcpMockSafe) List(_ string) ([]string, error)                 { return nil, nil }
func (m *gcpMockSafe) Export(_ string) (map[string]interface{}, error) { return nil, nil }
func (m *gcpMockSafe) Import(_ string, _ map[string]interface{}) error { return nil }
func (m *gcpMockSafe) GetEngineInfo(_ string) (*EngineInfo, error)     { return nil, nil }
func (m *gcpMockSafe) MustGet(_, _ string) interface{}                 { return "" }
func (m *gcpMockSafe) GetString(_, _ string) (string, error)           { return "", nil }
func (m *gcpMockSafe) GetJSON(_, _ string) ([]byte, error)             { return nil, nil }

// findCall returns the first SetMultiple call for path, or nil.
func (m *gcpMockSafe) findCall(path string) *gcpSetCall {
	for i := range m.setMultipleCalls {
		if m.setMultipleCalls[i].path == path {
			return &m.setMultipleCalls[i]
		}
	}
	return nil
}

// mockSafeError is a sentinel error for injected failures.
type mockSafeError struct{ path string }

func (e *mockSafeError) Error() string { return "mock safe error at " + e.path }

// recordingReporter records all progress calls — no-op by default, used to
// verify Configure drives all phases without panic.
type recordingReporter struct {
	starts    []string
	completes []string
	subtasks  int
	summaries int
}

func (r *recordingReporter) ReportPhaseStart(phase string, _, _ int) {
	r.starts = append(r.starts, phase)
}
func (r *recordingReporter) ReportPhaseComplete(phase string, _ time.Duration) {
	r.completes = append(r.completes, phase)
}
func (r *recordingReporter) ReportSubtaskProgress(_ string, _, _ int, _ string) {
	r.subtasks++
}
func (r *recordingReporter) ReportError(_ string, _ error, _, _, _, _ int) {}
func (r *recordingReporter) ReportFinalSummary(_ bool, _ time.Duration, _, _ int) {
	r.summaries++
}

// newTestGCPProvider builds a provider wired to mock + given config/bloc.
func newTestGCPProvider(cfg *config.Config, mock *gcpMockSafe, blocName string) *GCPVaultProvider {
	if blocName == "" {
		blocName = "test-bloc"
	}
	return &GCPVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              mock,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
	}
}

// gcpTestConfig returns a minimal GCP config sufficient for all provider operations.
func gcpTestConfig() *config.Config {
	return &config.Config{
		ProjectID:          "my-project",
		Subnets:            []config.Subnet{{Name: "workload-a", CIDR: "10.80.4.0/22", Type: "ocfp", AvailabilityZone: "us-central1-a"}, {Name: "workload-b", CIDR: "10.80.8.0/22", Type: "ocfp", AvailabilityZone: "us-central1-b"}, {Name: "workload-c", CIDR: "10.80.12.0/22", Type: "ocfp", AvailabilityZone: "us-central1-c"}},
		Region:             "us-central1-a",
		ServiceAccountJSON: `{"type":"service_account"}`,
		FQDNs: &config.FQDNConfig{
			Base: "example.com",
		},
	}
}

// ---- Construction ----

func TestGCP_NewGCPVaultProvider_FieldsSet(t *testing.T) {
	t.Parallel()

	cfg := gcpTestConfig()
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(cfg, mock, "my-bloc")

	require.NotNil(t, p)
	assert.Same(t, mock, p.Safe)
	assert.NotNil(t, p.PathBuilder)
	assert.Equal(t, "my-bloc", p.BlocName)
	assert.Same(t, cfg, p.Config)
}

func TestGCP_NewGCPVaultProvider_Constructor(t *testing.T) {
	t.Parallel()

	cfg := gcpTestConfig()
	mock := &gcpMockSafe{}
	p := NewGCPVaultProvider(cfg, mock, "ctor-bloc")

	require.NotNil(t, p)
	assert.Equal(t, "ctor-bloc", p.BlocName)
	assert.Same(t, cfg, p.Config)
	assert.NotNil(t, p.PathBuilder)
	assert.NotNil(t, p.logger)
}

// ---- GetProviderName ----

func TestGCP_GetProviderName(t *testing.T) {
	t.Parallel()

	p := newTestGCPProvider(gcpTestConfig(), &gcpMockSafe{}, "")
	assert.Equal(t, "gcp", p.GetProviderName())
}

// ---- SaveConfigToVault ----

func TestGCP_SaveConfigToVault_WritesCorrectPath(t *testing.T) {
	t.Parallel()

	cfg := gcpTestConfig()
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(cfg, mock, "test-bloc")

	err := p.SaveConfigToVault(nil, 0, 1)
	require.NoError(t, err)

	expectedPath := "secret/config/test-bloc/ocfp"
	call := mock.findCall(expectedPath)
	require.NotNil(t, call, "must write to %s", expectedPath)

	assert.Equal(t, "gcp", call.data["provider"])
	assert.Equal(t, "test-bloc", call.data["bloc"])
	assert.NotEmpty(t, call.data["config"], "config field must be non-empty")
}

func TestGCP_SaveConfigToVault_ConfigIsValidGzipBase64(t *testing.T) {
	t.Parallel()

	cfg := gcpTestConfig()
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(cfg, mock, "test-bloc")

	err := p.SaveConfigToVault(nil, 0, 1)
	require.NoError(t, err)

	call := mock.findCall("secret/config/test-bloc/ocfp")
	require.NotNil(t, call)

	encoded, ok := call.data["config"].(string)
	require.True(t, ok, "config must be a string")

	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err, "config must be valid base64")

	gr, err := gzip.NewReader(io.NopCloser(
		// gzip.NewReader needs an io.Reader; wrap raw bytes
		func() io.Reader {
			return &byteReader{data: raw}
		}(),
	))
	require.NoError(t, err, "decoded bytes must be valid gzip")
	defer gr.Close()

	jsonBytes, err := io.ReadAll(gr)
	require.NoError(t, err)

	var decoded config.Config
	require.NoError(t, json.Unmarshal(jsonBytes, &decoded), "gzip payload must be valid JSON config")
}

// byteReader wraps a []byte to satisfy io.Reader for gzip.NewReader.
type byteReader struct {
	data []byte
	pos  int
}

func (b *byteReader) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}

func TestGCP_SaveConfigToVault_ReporterCalled(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	rep := &recordingReporter{}

	err := p.SaveConfigToVault(rep, 0, 5)
	require.NoError(t, err)

	assert.Contains(t, rep.starts, PhaseConfig)
	assert.Contains(t, rep.completes, PhaseConfig)
}

// ---- ConfigureIAAS ----

func TestGCP_ConfigureIAAS_WritesCPIAndNetwork(t *testing.T) {
	t.Parallel()

	cfg := gcpTestConfig()
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(cfg, mock, "test-bloc")

	phaseIdx := 1
	err := p.ConfigureIAAS("", MgmtEnvType, nil, &phaseIdx, 10)
	require.NoError(t, err)

	// CPI path
	cpiPath := "secret/config/test-bloc/mgmt/cpi/gcp"
	cpiCall := mock.findCall(cpiPath)
	require.NotNil(t, cpiCall, "must write CPI config at %s", cpiPath)
	assert.Equal(t, "my-project", cpiCall.data["project"])
	assert.Equal(t, "us-central1-a", cpiCall.data["zone"])
	assert.Equal(t, `{"type":"service_account"}`, cpiCall.data["json_key"])
	assert.Equal(t, "test-bloc-bastion", cpiCall.data["default_key_name"])

	// Network path
	netPath := p.PathBuilder.GetNetPath(MgmtEnvType)
	netCall := mock.findCall(netPath)
	require.NotNil(t, netCall, "must write network config at %s", netPath)
	assert.Equal(t, "us-central1", netCall.data["region"], "region must be derived from zone")
	assert.Equal(t, "us-central1-a", netCall.data["zone"])
}

func TestGCP_ConfigureIAAS_OCFEnv(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")

	phaseIdx := 0
	err := p.ConfigureIAAS("", OCFEnvType, nil, &phaseIdx, 5)
	require.NoError(t, err)

	cpiPath := "secret/config/test-bloc/ocf/cpi/gcp"
	assert.NotNil(t, mock.findCall(cpiPath), "must write CPI config for ocf env")
}

func TestGCP_ConfigureIAAS_ReporterCalled(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	rep := &recordingReporter{}
	phaseIdx := 2

	err := p.ConfigureIAAS("", MgmtEnvType, rep, &phaseIdx, 10)
	require.NoError(t, err)

	assert.Contains(t, rep.starts, "networks-mgmt")
	assert.Contains(t, rep.completes, "networks-mgmt")
}

func TestGCP_ConfigureIAAS_NilPhaseNum(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")

	// phaseNum nil must not panic
	err := p.ConfigureIAAS("", MgmtEnvType, nil, nil, 5)
	require.NoError(t, err)
}

// ---- ConfigureBlobstores ----

func TestGCP_ConfigureBlobstores_MgmtHasBOSHOnly(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")

	err := p.ConfigureBlobstores("", MgmtEnvType, nil, 1, 10)
	require.NoError(t, err)

	// mgmt: only bosh/blobstores/artifacts
	artifactsPath := p.PathBuilder.GetSystemBlobstorePath(MgmtEnvType, "bosh", "artifacts")
	call := mock.findCall(artifactsPath)
	require.NotNil(t, call, "must write bosh artifacts blobstore at %s", artifactsPath)
	assert.Equal(t, "test-bloc-mgmt-artifacts", call.data["bucket_name"])
	assert.Equal(t, "us-central1", call.data["region"])
	assert.Equal(t, "STANDARD", call.data["storage_class"])
}

func TestGCP_ConfigureBlobstores_OCFHasCFBlobstores(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")

	err := p.ConfigureBlobstores("", OCFEnvType, nil, 1, 10)
	require.NoError(t, err)

	cfSystems := []string{"buildpacks", "droplets", "packages", "resources"}
	for _, name := range cfSystems {
		bPath := p.PathBuilder.GetSystemBlobstorePath(OCFEnvType, "cf", name)
		call := mock.findCall(bPath)
		require.NotNil(t, call, "must write cf/%s blobstore for ocf env", name)
		bucket := name
		if name == "resources" {
			bucket = "resource-pool"
		}
		assert.Equal(t, "test-bloc-ocf-cf-"+bucket, call.data["bucket_name"])
	}
}

func TestGCP_ConfigureBlobstores_ReporterCalled(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	rep := &recordingReporter{}

	err := p.ConfigureBlobstores("", MgmtEnvType, rep, 3, 10)
	require.NoError(t, err)

	assert.Contains(t, rep.starts, "blobstores-mgmt")
	assert.Contains(t, rep.completes, "blobstores-mgmt")
	assert.Greater(t, rep.subtasks, 0)
}

// ---- getBlobstoresForSystem ----

func TestGCP_GetBlobstoresForSystem_Bosh(t *testing.T) {
	t.Parallel()

	p := newTestGCPProvider(gcpTestConfig(), &gcpMockSafe{}, "test-bloc")
	result := p.getBlobstoresForSystem("bosh", MgmtEnvType)

	require.Contains(t, result, "artifacts")
	assert.Equal(t, "test-bloc-mgmt-artifacts", result["artifacts"]["bucket_name"])
	assert.Equal(t, "STANDARD", result["artifacts"]["storage_class"])
}

func TestGCP_GetBlobstoresForSystem_CF(t *testing.T) {
	t.Parallel()

	p := newTestGCPProvider(gcpTestConfig(), &gcpMockSafe{}, "test-bloc")
	result := p.getBlobstoresForSystem("cf", OCFEnvType)

	for _, name := range []string{"buildpacks", "droplets", "packages", "resources"} {
		require.Contains(t, result, name, "cf blobstores must contain %s", name)
	}
}

func TestGCP_GetBlobstoresForSystem_UnknownSystem(t *testing.T) {
	t.Parallel()

	p := newTestGCPProvider(gcpTestConfig(), &gcpMockSafe{}, "test-bloc")
	result := p.getBlobstoresForSystem("unknown", MgmtEnvType)
	assert.Empty(t, result, "unknown system must return empty map")
}

// ---- ConfigureDatabases ----

func TestGCP_ConfigureDatabases_WritesPostgres(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.AddResource(&state.Resource{Type: "database", Name: "postgres", ID: "sql-real", Properties: map[string]any{"environment": "mgmt", "hostname": "10.90.0.5", "port": 5432, "type": "postgres"}}))
	require.NoError(t, sm.Save())
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	require.NoError(t, p.ConfigureDatabases("ignored", "mgmt", nil, 1, 1))
	call := mock.findCall(p.PathBuilder.GetDatabasePath("mgmt", "postgres"))
	require.NotNil(t, call)
	assert.Equal(t, "10.90.0.5", call.data["hostname"])

}

func TestGCP_ConfigureDatabases_HostnameContainsBlocAndEnv(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.AddResource(&state.Resource{Type: "database", Name: "postgres", ID: "sql-real", Properties: map[string]any{"environment": "ocf", "hostname": "10.90.0.5", "port": 5432, "type": "postgres"}}))
	require.NoError(t, sm.Save())
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	require.NoError(t, p.ConfigureDatabases("ignored", "ocf", nil, 1, 1))
	call := mock.findCall(p.PathBuilder.GetDatabasePath("ocf", "postgres"))
	require.NotNil(t, call)
	assert.Equal(t, "10.90.0.5", call.data["hostname"])

}

func TestGCP_ConfigureDatabases_ReporterCalled(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	rep := &recordingReporter{}
	envPath := p.PathBuilder.GetEnvironmentPath(MgmtEnvType)

	err := p.ConfigureDatabases(envPath, MgmtEnvType, rep, 4, 10)
	require.NoError(t, err)

	assert.Contains(t, rep.starts, "databases-mgmt")
	assert.Contains(t, rep.completes, "databases-mgmt")
}

// ---- ConfigureLoadBalancers ----

func TestGCP_ConfigureLoadBalancers_MgmtLBs(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.AddResource(&state.Resource{Type: "load_balancer", Name: "actual", ID: "lb-real", Properties: map[string]any{"environment": "mgmt", "type": "tcp", "port": 443}}))
	require.NoError(t, sm.Save())
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	require.NoError(t, p.ConfigureLoadBalancers("ignored", "mgmt", nil, 1, 1))
	call := mock.findCall(p.PathBuilder.GetLoadBalancerPath("mgmt", "actual"))
	require.NotNil(t, call)
	assert.Equal(t, "lb-real", call.data["id"])

}

func TestGCP_ConfigureLoadBalancers_OCFLBs(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.AddResource(&state.Resource{Type: "load_balancer", Name: "actual", ID: "lb-real", Properties: map[string]any{"environment": "ocf", "type": "tcp", "port": 443}}))
	require.NoError(t, sm.Save())
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	require.NoError(t, p.ConfigureLoadBalancers("ignored", "ocf", nil, 1, 1))
	call := mock.findCall(p.PathBuilder.GetLoadBalancerPath("ocf", "actual"))
	require.NotNil(t, call)
	assert.Equal(t, "lb-real", call.data["id"])

}

func TestGCP_ConfigureLoadBalancers_ReporterCalled(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	rep := &recordingReporter{}
	envPath := p.PathBuilder.GetEnvironmentPath(MgmtEnvType)

	err := p.ConfigureLoadBalancers(envPath, MgmtEnvType, rep, 5, 10)
	require.NoError(t, err)

	assert.Contains(t, rep.starts, "load-balancers-mgmt")
	assert.Contains(t, rep.completes, "load-balancers-mgmt")
}

// ---- getLoadBalancersForEnv ----

func TestGCP_GetLoadBalancersForEnv_MgmtHasThree(t *testing.T) {
	t.Parallel()

	p := newTestGCPProvider(gcpTestConfig(), &gcpMockSafe{}, "")
	lbs := p.getLoadBalancersForEnv(MgmtEnvType)
	assert.Len(t, lbs, 3)

	names := make([]string, len(lbs))
	for i, lb := range lbs {
		names[i] = lb.name
	}
	assert.Contains(t, names, "concourse")
	assert.Contains(t, names, "vault")
	assert.Contains(t, names, "prometheus")
}

func TestGCP_GetLoadBalancersForEnv_OCFHasThree(t *testing.T) {
	t.Parallel()

	p := newTestGCPProvider(gcpTestConfig(), &gcpMockSafe{}, "")
	lbs := p.getLoadBalancersForEnv(OCFEnvType)
	assert.Len(t, lbs, 3)
}

// ---- ConfigureFQDNs ----

func TestGCP_ConfigureFQDNs_MgmtFQDNs(t *testing.T) {
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	require.NoError(t, p.ConfigureFQDNs("ignored", "mgmt", nil, 1, 1))
	call := mock.findCall(p.PathBuilder.GetFQDNsPath("mgmt"))
	require.NotNil(t, call)
	assert.NotEmpty(t, call.data)

}

func TestGCP_ConfigureFQDNs_OCFFQDNs(t *testing.T) {
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	require.NoError(t, p.ConfigureFQDNs("ignored", "ocf", nil, 1, 1))
	call := mock.findCall(p.PathBuilder.GetFQDNsPath("ocf"))
	require.NotNil(t, call)
	assert.NotEmpty(t, call.data)

}

func TestGCP_ConfigureFQDNs_FQDNUsesBaseDomain(t *testing.T) {
	t.Parallel()

	cfg := gcpTestConfig()
	cfg.FQDNs = &config.FQDNConfig{Base: "myco.io"}
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(cfg, mock, "test-bloc")
	envPath := p.PathBuilder.GetEnvironmentPath(MgmtEnvType)

	err := p.ConfigureFQDNs(envPath, MgmtEnvType, nil, 1, 10)
	require.NoError(t, err)

	call := mock.findCall(envPath + "/fqdns")
	require.NotNil(t, call)
	assert.Equal(t, "concourse.myco.io", call.data["concourse"])
}

func TestGCP_ConfigureFQDNs_FallbackBaseDomain(t *testing.T) {
	cfg := gcpTestConfig()
	cfg.FQDNs = nil
	cfg.DomainName = "custom.example.net"
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(cfg, mock, "test-bloc")
	require.NoError(t, p.ConfigureFQDNs("ignored", "mgmt", nil, 1, 1))
	call := mock.findCall(p.PathBuilder.GetFQDNsPath("mgmt"))
	require.NotNil(t, call)
	assert.Equal(t, "vault.custom.example.net", call.data["vault"])

}

// ---- getFQDNsForEnv ----

func TestGCP_GetFQDNsForEnv_MgmtHasThree(t *testing.T) {
	t.Parallel()

	p := newTestGCPProvider(gcpTestConfig(), &gcpMockSafe{}, "test-bloc")
	fqdns := p.getFQDNsForEnv(MgmtEnvType)
	assert.Len(t, fqdns, 3)
}

func TestGCP_GetFQDNsForEnv_OCFHasFive(t *testing.T) {
	t.Parallel()

	p := newTestGCPProvider(gcpTestConfig(), &gcpMockSafe{}, "test-bloc")
	fqdns := p.getFQDNsForEnv(OCFEnvType)
	assert.Len(t, fqdns, 5)
}

// ---- ConfigureCertificates ----

func TestGCP_ConfigureCertificates_WritesCertsPath(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")

	err := p.ConfigureCertificates("", "", nil, 1, 10)
	require.NoError(t, err)

	certsPath := p.PathBuilder.GetCertsPath()
	call := mock.findCall(certsPath)
	require.NotNil(t, call, "must write certs config at %s", certsPath)
	assert.Equal(t, "letsencrypt", call.data["provider"])
	assert.Equal(t, "us-central1", call.data["region"])
}

func TestGCP_ConfigureCertificates_ReporterCalled(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	rep := &recordingReporter{}

	err := p.ConfigureCertificates("", "", rep, 6, 10)
	require.NoError(t, err)

	assert.Contains(t, rep.starts, PhaseCertificates)
	assert.Contains(t, rep.completes, PhaseCertificates)
}

// ---- ConfigurePublicIPs ----

func TestGCP_ConfigurePublicIPs_WritesThreeJobs(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.AddResource(&state.Resource{Type: "public_ip", Name: "test-bloc-router-0", ID: "address-real", Properties: map[string]any{"ip_address": "198.51.100.10", "job": "router"}, Tags: map[string]string{"job": "router"}}))
	require.NoError(t, sm.Save())
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	require.NoError(t, p.ConfigurePublicIPs(nil, 1, 1))
	call := mock.findCall(p.PathBuilder.GetPublicIPsPath())
	require.NotNil(t, call)
	assert.Equal(t, "198.51.100.10", call.data["cf_router_0"])

}

func TestGCP_ConfigurePublicIPs_ReporterCalled(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	rep := &recordingReporter{}

	err := p.ConfigurePublicIPs(rep, 7, 10)
	require.NoError(t, err)

	assert.Contains(t, rep.starts, PhasePublicIPs)
	assert.Contains(t, rep.completes, PhasePublicIPs)
}

// ---- configureSubnets (via configureEnvironment) ----

func TestGCP_ConfigureSubnets_WritesThreeSubnets(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())
	mock := &gcpMockSafe{}
	cfg := gcpTestConfig()
	p := newTestGCPProvider(cfg, mock, "test-bloc")
	require.NoError(t, p.configureSubnets("ignored", "mgmt", nil, 1, 1))
	for i, sub := range cfg.Subnets {
		call := mock.findCall(p.PathBuilder.GetSubnetPath("mgmt", "ocfp", i))
		require.NotNil(t, call)
		assert.Equal(t, sub.Name, call.data["name"])
		assert.Equal(t, sub.CIDR, call.data["cidr_block"])
	}

}

func TestGCP_ConfigureSubnets_NameIncludesBlocAndEnv(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())
	mock := &gcpMockSafe{}
	cfg := gcpTestConfig()
	p := newTestGCPProvider(cfg, mock, "test-bloc")
	require.NoError(t, p.configureSubnets("ignored", "ocf", nil, 1, 1))
	for i, sub := range cfg.Subnets {
		call := mock.findCall(p.PathBuilder.GetSubnetPath("ocf", "ocfp", i))
		require.NotNil(t, call)
		assert.Equal(t, sub.Name, call.data["name"])
		assert.Equal(t, sub.CIDR, call.data["cidr_block"])
	}

}

// ---- configureSecurityGroups ----

func TestGCP_ConfigureSecurityGroups_MgmtHasThree(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	for _, name := range []string{"default", "ocfp", "bosh"} {
		require.NoError(t, sm.AddResource(&state.Resource{Type: "security_group", Name: "test-bloc-" + name, ID: "firewall-" + name}))
	}
	require.NoError(t, sm.Save())
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	require.NoError(t, p.configureSecurityGroups("ignored", "mgmt", nil, 1, 1))
	for _, name := range []string{"default", "ocfp", "bosh"} {
		call := mock.findCall(p.PathBuilder.GetSecurityGroupPath("mgmt", name))
		require.NotNil(t, call)
		assert.Equal(t, "test-bloc-"+name, call.data["network_tag"])
	}

}

func TestGCP_ConfigureSecurityGroups_OCFHasCFExtra(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	for _, name := range []string{"default", "ocfp", "bosh"} {
		require.NoError(t, sm.AddResource(&state.Resource{Type: "security_group", Name: "test-bloc-" + name, ID: "firewall-" + name}))
	}
	require.NoError(t, sm.Save())
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	require.NoError(t, p.configureSecurityGroups("ignored", "ocf", nil, 1, 1))
	for _, name := range []string{"default", "ocfp", "bosh"} {
		call := mock.findCall(p.PathBuilder.GetSecurityGroupPath("ocf", name))
		require.NotNil(t, call)
		assert.Equal(t, "test-bloc-"+name, call.data["network_tag"])
	}

}

func TestGCP_ConfigureSecurityGroups_ReporterCalled(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	rep := &recordingReporter{}
	envPath := p.PathBuilder.GetEnvironmentPath(MgmtEnvType)

	err := p.configureSecurityGroups(envPath, MgmtEnvType, rep, 2, 10)
	require.NoError(t, err)

	assert.Contains(t, rep.starts, "security-groups-mgmt")
	assert.Contains(t, rep.completes, "security-groups-mgmt")
}

// ---- Configure (full integration via mock) ----

func TestGCP_Configure_HappyPath(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")
	rep := &recordingReporter{}

	err := p.Configure(rep)
	require.NoError(t, err)

	// Config saved
	assert.NotNil(t, mock.findCall("secret/config/test-bloc/ocfp"), "config must be saved to vault")

	// Both env types configured — at minimum IAAS paths written
	mgmtCPI := "secret/config/test-bloc/mgmt/cpi/gcp"
	ocfCPI := "secret/config/test-bloc/ocf/cpi/gcp"
	assert.NotNil(t, mock.findCall(mgmtCPI), "mgmt CPI must be written")
	assert.NotNil(t, mock.findCall(ocfCPI), "ocf CPI must be written")

	// Shared: certs and public IPs
	assert.NotNil(t, mock.findCall(p.PathBuilder.GetCertsPath()), "certs must be written")

	// Reporter received final summary
	assert.Equal(t, 1, rep.summaries, "Configure must call ReportFinalSummary once")
}

func TestGCP_Configure_NilReporter_NoError(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")

	err := p.Configure(nil)
	require.NoError(t, err)
}

func TestGCP_Configure_SaveConfigFailurePropagates(t *testing.T) {
	t.Parallel()

	mock := &gcpMockSafe{failOnPath: "secret/config/test-bloc/ocfp"}
	p := newTestGCPProvider(gcpTestConfig(), mock, "test-bloc")

	err := p.Configure(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to save config to vault")
}
