package vault

import (
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestCloudParityAWSNetworkIdentity(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.SetOutput("network_id", "vpc-real"))
	require.NoError(t, sm.Save())
	p := newTestAWSProvider(&config.Config{}, &awsMockSafe{})
	require.Equal(t, "vpc-real", p.getVPCID())
}
func TestCloudParityAWSPrivateKey(t *testing.T) {
	mock := &awsMockSafe{}
	p := newTestAWSProvider(&config.Config{Keys: map[string]string{"test-bloc-bastion": "PRIVATE MATERIAL"}}, mock)
	require.NoError(t, p.configureKeys("mgmt"))
	require.Equal(t, "PRIVATE MATERIAL", mock.findSetMultipleCall(p.PathBuilder.GetBOSHKeyPath("mgmt")).data["private"])
}
func TestCloudParityAWSDoesNotInventDatabase(t *testing.T) {
	p := newTestAWSProvider(&config.Config{}, &awsMockSafe{})
	require.Empty(t, p.getDatabasesForEnv("mgmt"))
}
func TestCloudParityGCPTopology(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.SetOutput("network_id", "projects/project/global/networks/real"))
	require.NoError(t, sm.AddResource(&state.Resource{Type: "subnet", Name: "custom", ID: "real-subnet", Properties: map[string]any{"cidr": "10.80.4.0/22", "availability_zone": "us-central1-b", "type": "ocfp", "role": "ocfp", "workload_index": 0}}))
	require.NoError(t, sm.Save())
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(&config.Config{Provider: "gcp", Region: "us-central1-b", Network: config.NetworkConfig{CIDR: "10.80.0.0/20"}}, mock, "test-bloc")
	require.NoError(t, p.configureSubnets("unused", "mgmt", nil, 1, 1))
	call := mock.findCall(p.PathBuilder.GetSubnetPath("mgmt", "ocfp", 0))
	require.NotNil(t, call)
	require.Equal(t, "10.80.4.0/22", call.data["cidr_block"])
	require.Equal(t, "real-subnet", call.data["id"])
	require.NotNil(t, mock.findCall(p.PathBuilder.GetReservedIPsPath("mgmt", "ocfp", 0)))
}
func TestCloudParityGCPCredentialsFile(t *testing.T) {
	key := filepath.Join(t.TempDir(), "key.json")
	const content = `{"type":"service_account","project_id":"project"}`
	require.NoError(t, os.WriteFile(key, []byte(content), 0600))
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(&config.Config{ProjectID: "project", Region: "us-central1-b", ServiceAccountKeyPath: key}, mock, "test-bloc")
	require.NoError(t, p.ConfigureIAAS("", "mgmt", nil, nil, 1))
	require.JSONEq(t, content, mock.findCall("secret/config/test-bloc/mgmt/cpi/gcp").data["json_key"].(string))
}
func TestCloudParityGCPFQDN(t *testing.T) {
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(&config.Config{FQDNs: &config.FQDNConfig{Base: "example.net", Mgmt: map[string]string{"vault": "custom.example.net"}}}, mock, "test-bloc")
	require.NoError(t, p.ConfigureFQDNs("ignored", "mgmt", nil, 1, 1))
	call := mock.findCall(p.PathBuilder.GetFQDNsPath("mgmt"))
	require.NotNil(t, call)
	require.Equal(t, "custom.example.net", call.data["vault"])
}

func TestCloudParityAWSSecurityGroups(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.AddResource(&state.Resource{Type: "security_group", Name: "test-bloc-ocfp", ID: "sg-real"}))
	require.NoError(t, sm.Save())
	mock := &awsMockSafe{}
	p := newTestAWSProvider(&config.Config{}, mock)
	require.NoError(t, p.configureSecurityGroups("mgmt"))
	require.Equal(t, "sg-real", mock.findSetMultipleCall(p.PathBuilder.GetSecurityGroupPath("mgmt", "ocfp")).data["id"])
	require.Nil(t, mock.findSetMultipleCall(p.PathBuilder.GetSecurityGroupPath("mgmt", "default")))
}
func TestCloudParityAWSUsesStateTopology(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.AddResource(&state.Resource{Type: "subnet", Name: "custom", ID: "subnet-real", Properties: map[string]any{"cidr": "10.80.4.0/22", "availability_zone": "us-east-1b", "type": "ocfp", "workload_index": 0}}))
	require.NoError(t, sm.Save())
	mock := &awsMockSafe{}
	p := newTestAWSProvider(&config.Config{Provider: "aws", Network: config.NetworkConfig{CIDR: "10.80.0.0/20", Strategy: "wide"}}, mock)
	require.NoError(t, p.configureSubnets("mgmt"))
	call := mock.findSetMultipleCall(p.PathBuilder.GetSubnetPath("mgmt", "ocfp", 0))
	require.NotNil(t, call)
	require.Equal(t, "10.80.4.0/22", call.data["cidr_block"])
	require.NotNil(t, mock.findSetMultipleCall(p.PathBuilder.GetAZPath("mgmt", "us-east-1b")))
}
func TestCloudParityGCPNoFakeServices(t *testing.T) {
	seedPVEState(t, "test-bloc")
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(&config.Config{}, mock, "test-bloc")
	require.NoError(t, p.ConfigureDatabases("unused", "mgmt", nil, 1, 1))
	require.NoError(t, p.ConfigureLoadBalancers("unused", "mgmt", nil, 1, 1))
	require.NoError(t, p.ConfigurePublicIPs(nil, 1, 1))
	require.Empty(t, mock.setMultipleCalls)
}
func TestCloudParityInvalidCIDR(t *testing.T) {
	p := newTestAWSProvider(&config.Config{}, &awsMockSafe{})
	for _, cidr := range []string{"10.1.2.0/nope", "10.1.2.0/33", "999.1.2.0/22", "::/64"} {
		require.NotPanics(t, func() { _, err := p.parseSubnetCIDR(cidr); require.Error(t, err) })
	}
}

func TestCloudParityScopedCapabilities(t *testing.T) {
	cfg := &config.Config{Subnets: []config.Subnet{{Name: "ocfp-0", Type: "ocfp", CIDR: "10.80.4.0/22"}}, FQDNs: &config.FQDNConfig{Base: "example.net"}}
	for _, provider := range []any{NewAWSVaultProvider(cfg, &awsMockSafe{}, "test-bloc"), NewGCPVaultProvider(cfg, &gcpMockSafe{}, "test-bloc")} {
		_, ok := provider.(interface {
			ConfigureReservedIPs(providers.ProgressReporter, int, int) error
		})
		require.True(t, ok)
		_, ok = provider.(interface {
			PlanFQDNs(bool, FQDNFilter) ([]FQDNPlaneChanges, error)
		})
		require.True(t, ok)
	}
}

func TestCloudParityAWSNoSyntheticCredentials(t *testing.T) {
	mock := &awsMockSafe{}
	p := newTestAWSProvider(&config.Config{}, mock)
	require.NoError(t, p.configureShield("mgmt"))
	require.Empty(t, mock.setMultipleCalls)
}

type parityTransport struct{ calls int }

func (p *parityTransport) RoundTrip(*http.Request) (*http.Response, error) {
	p.calls++
	return nil, fmt.Errorf("network forbidden")
}
func TestCloudParityNoImplicitCAFetch(t *testing.T) {
	tr := &parityTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	p := newTestAWSProvider(&config.Config{}, &awsMockSafe{})
	require.NoError(t, p.ConfigureDatabases("", "mgmt", nil, 1, 1))
	require.Zero(t, tr.calls)
}
func TestCloudParitySSHKeyDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "id_ed25519"), []byte("PRIVATE KEY"), 0600))
	mock := &awsMockSafe{}
	p := newTestAWSProvider(&config.Config{Bastion: config.Bastion{SSHKeyDir: dir}}, mock)
	require.NoError(t, p.configureKeys("mgmt"))
	require.Equal(t, "PRIVATE KEY", mock.findSetMultipleCall(p.PathBuilder.GetBOSHKeyPath("mgmt")).data["private"])
}

func TestCloudParityScopedManager(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("OCFP_HOME", t.TempDir())
			cfg := &config.Config{Provider: provider, Network: config.NetworkConfig{Strategy: "wide"}, Subnets: []config.Subnet{{Name: "ocfp-0", CIDR: "10.80.4.0/22", Type: "ocfp"}}, FQDNs: &config.FQDNConfig{Base: "example.net"}}
			safe := newFakeSafe()
			mgr := newReservedIPsScopeTestManager(cfg, safe)
			require.NoError(t, mgr.populateReservedIPsPhase(nil, &PopulateOptions{}, io.Discard))
			var paths []string
			for path := range safe.data {
				paths = append(paths, path)
			}
			assertOnlyReservedIPPaths(t, paths)
			require.NoError(t, mgr.populateFQDNsPhase(&PopulateOptions{FQDNPlanes: []string{"mgmt"}}, io.Discard))
		})
	}
}

func TestCloudParityGCPInvalidCredentialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0600))
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(&config.Config{ServiceAccountKeyPath: path}, mock, "test-bloc")
	require.Error(t, p.ConfigureIAAS("", "mgmt", nil, nil, 1))
	require.Empty(t, mock.setMultipleCalls)
}
func TestCloudParityNoEmptyCredentials(t *testing.T) {
	am := &awsMockSafe{}
	a := newTestAWSProvider(&config.Config{Region: "us-east-1"}, am)
	require.NoError(t, a.configureCPI("mgmt"))
	call := am.findSetMultipleCall("secret/config/test-bloc/mgmt/cpi/aws")
	require.NotNil(t, call)
	_, exists := call.data["access_key_id"]
	require.False(t, exists)
	gm := &gcpMockSafe{}
	g := newTestGCPProvider(&config.Config{}, gm, "test-bloc")
	require.NoError(t, g.ConfigureIAAS("", "mgmt", nil, nil, 1))
	callg := gm.findCall("secret/config/test-bloc/mgmt/cpi/gcp")
	_, exists = callg.data["json_key"]
	require.False(t, exists)
}

func TestCloudParityAWSBastionReservationUsesObservedIP(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.SetOutput("bastion_private_ip", "10.80.4.25"))
	require.NoError(t, sm.Save())
	mock := &awsMockSafe{}
	cfg := &config.Config{Provider: "aws", Network: config.NetworkConfig{Strategy: "wide"}}
	p := newTestAWSProvider(cfg, mock)
	require.NoError(t, p.configureSubnetReservedIPs("10.80.4.0/22", "ocfp", 0, "mgmt"))
	call := mock.findSetMultipleCall(p.PathBuilder.GetReservedIPsPath("mgmt", "ocfp", 0))
	require.Equal(t, "10.80.4.25", call.data["bastion_ip"])
	mock = &awsMockSafe{}
	p = newTestAWSProvider(cfg, mock)
	require.NoError(t, p.configureSubnetReservedIPs("10.80.8.0/22", "ocfp", 1, "mgmt"))
	call = mock.findSetMultipleCall(p.PathBuilder.GetReservedIPsPath("mgmt", "ocfp", 1))
	require.Equal(t, "10.80.11.254", call.data["bastion_ip"])
}

func TestCloudParityCloudDynamicBounds(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		for _, strategy := range []string{"wide", "compact"} {
			t.Run(provider+strategy, func(t *testing.T) {
				t.Setenv("OCFP_HOME", t.TempDir())
				mock := &awsMockSafe{}
				p := newTestAWSProvider(&config.Config{Provider: provider, Network: config.NetworkConfig{Strategy: strategy}}, mock)
				require.NoError(t, p.configureSubnetReservedIPs("10.80.4.0/25", "ocfp", 0, "ocf"))
				data := mock.findSetMultipleCall(p.PathBuilder.GetReservedIPsPath("ocf", "ocfp", 0)).data
				require.Equal(t, "10.80.4.125", data["available_1"])
				require.Equal(t, "10.80.4.126", data["reserved_2"])
			})
		}
	}
}

func TestCloudParityObservedBastionExcludedFromOCFBand(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.SetOutput("bastion_private_ip", "10.80.4.110"))
	require.NoError(t, sm.Save())
	mock := &awsMockSafe{}
	p := newTestAWSProvider(&config.Config{Provider: "aws", Network: config.NetworkConfig{Strategy: "wide"}}, mock)
	require.NoError(t, p.configureSubnetReservedIPs("10.80.4.0/25", "ocfp", 0, "ocf"))
	data := mock.findSetMultipleCall(p.PathBuilder.GetReservedIPsPath("ocf", "ocfp", 0)).data
	require.Equal(t, "10.80.4.109", data["available_1"])
	require.Equal(t, "10.80.4.110", data["reserved_2"])
	require.Equal(t, "10.80.4.64", data["bosh_ip"])
}

func TestCloudParityScopedReservationProtection(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("OCFP_HOME", t.TempDir())
			cfg := &config.Config{Provider: provider, Network: config.NetworkConfig{Strategy: "wide"}, Subnets: []config.Subnet{{Name: "ocfp-0", CIDR: "10.80.4.0/22", Type: "ocfp"}}}
			safe := newFakeSafe()
			mgr := newReservedIPsScopeTestManager(cfg, safe)
			require.NoError(t, mgr.populateDryRun(&PopulateOptions{Subcommand: PhaseReservedIPs}, safe, "test", io.Discard))
			require.Empty(t, safe.data)
			_, err := mgr.reservedIPs(&ReservedIPOptions{Apply: false})
			require.NoError(t, err)
			require.Empty(t, safe.data)
			require.NoError(t, mgr.populateReservedIPsPhase(nil, &PopulateOptions{}, io.Discard))
			path := NewAWSVaultProvider(cfg, safe, "test-bloc").PathBuilder.GetReservedIPsPath("mgmt", "ocfp", 0)
			safe.data[path]["bosh_ip"] = "10.80.4.200"
			require.NoError(t, mgr.populateReservedIPsPhase(nil, &PopulateOptions{}, io.Discard))
			require.Equal(t, "10.80.4.200", safe.data[path]["bosh_ip"])
			require.NoError(t, mgr.populateReservedIPsPhase(nil, &PopulateOptions{ForceReallocate: true}, io.Discard))
			require.Equal(t, "10.80.4.4", safe.data[path]["bosh_ip"])
			require.NoError(t, mgr.populateReservedIPsPhase(nil, &PopulateOptions{}, io.Discard))
			require.Equal(t, "10.80.4.4", safe.data[path]["bosh_ip"])
		})
	}
}

func TestCloudParityRejectsBastionStaticCollision(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.SetOutput("bastion_private_ip", "10.80.4.4"))
	require.NoError(t, sm.Save())
	mock := &awsMockSafe{}
	p := newTestAWSProvider(&config.Config{Provider: "aws", Network: config.NetworkConfig{Strategy: "wide"}}, mock)
	require.ErrorContains(t, p.configureSubnetReservedIPs("10.80.4.0/22", "ocfp", 0, "mgmt"), "collides")
	require.Empty(t, mock.setMultipleCalls)
}

func TestCloudParityRejectsCorruptStateBeforeExports(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OCFP_HOME", home)
	sm := seedPVEState(t, "test-bloc")
	_ = sm
	dir, err := state.GetStateDir("test-bloc")
	require.NoError(t, err)
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, file := range files {
		if !file.IsDir() {
			require.NoError(t, os.WriteFile(filepath.Join(dir, file.Name()), []byte("[ invalid state"), 0600))
		}
	}
	for _, provider := range []string{"aws", "gcp"} {
		t.Run(provider, func(t *testing.T) {
			safe := newFakeSafe()
			cfg := &config.Config{Provider: provider}
			var p providers.VaultProvider
			if provider == "aws" {
				p = NewAWSVaultProvider(cfg, safe, "test-bloc")
			} else {
				p = NewGCPVaultProvider(cfg, safe, "test-bloc")
			}
			require.Error(t, p.Configure(nil))
			require.Empty(t, safe.data)
			require.Error(t, p.ConfigureIAAS("", "mgmt", nil, nil, 1))
			require.Empty(t, safe.data)
			require.Error(t, p.ConfigureDatabases("", "mgmt", nil, 1, 1))
			require.Empty(t, safe.data)
			require.Error(t, p.(reservedIPProvider).ConfigureReservedIPs(nil, 1, 1))
			require.Empty(t, safe.data)
		})
	}
}

func TestGCPCanonicalExportsUseObservedTopology(t *testing.T) {
	sm := seedPVEState(t, "test-bloc")
	require.NoError(t, sm.SetOutput("network_id", "projects/host-project/global/networks/actual"))
	require.NoError(t, sm.AddResource(&state.Resource{Type: "subnet", ID: "actual-subnet", Name: "custom", Properties: map[string]any{"cidr": "10.80.4.0/22", "availability_zone": "us-central1-b", "type": "ocfp", "workload_index": 0}}))
	require.NoError(t, sm.AddResource(&state.Resource{Type: "security_group", ID: "sg-id", Name: "custom-tag"}))
	require.NoError(t, sm.Save())
	cfg := &config.Config{Provider: "gcp", ProjectID: "service-project", Region: "us-central1", Network: config.NetworkConfig{Strategy: "wide", DNS: []string{"10.80.0.2"}}, Keys: map[string]string{"test-bloc-bastion": "PRIVATE KEY"}}
	mock := &gcpMockSafe{}
	p := newTestGCPProvider(cfg, mock, "test-bloc")
	require.NoError(t, p.ConfigureIAAS("", "mgmt", nil, nil, 1))
	cpi := mock.findCall("secret/config/test-bloc/mgmt/cpi/gcp").data
	require.Equal(t, "us-central1-b", cpi["zone"])
	require.JSONEq(t, `["custom-tag"]`, cpi["default_security_groups"].(string))
	require.Equal(t, "host-project", mock.findCall(p.PathBuilder.GetNetPath("mgmt")).data["project"])
	require.Equal(t, "10.80.0.2", mock.findCall(p.PathBuilder.GetNetPath("mgmt")).data["dns"])
	require.Equal(t, "PRIVATE KEY", mock.findCall(p.PathBuilder.GetBOSHKeyPath("mgmt")).data["private"])
	require.NoError(t, p.configureSubnets("", "mgmt", nil, 1, 1))
	require.Equal(t, "10.80.0.2", mock.findCall(p.PathBuilder.GetSubnetPath("mgmt", "ocfp", 0)).data["dns"])
	require.Equal(t, "actual-subnet", mock.findCall(p.PathBuilder.GetSubnetPath("mgmt", "ocfp", 0)).data["cloud_name"])
	require.NoError(t, p.ConfigureBlobstores("", "mgmt", nil, 1, 1))
	blob := mock.findCall(p.PathBuilder.GetSystemBlobstorePath("mgmt", "bosh", "bosh")).data
	require.Equal(t, blob["bucket_name"], blob["name"])
	require.NotContains(t, cpi, "access_key_id")
}
