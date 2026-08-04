package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAddOffsetToIP tests IP offset calculation. addOffsetToIP stays a
// production-reachable delegate to reservedip.AddOffsetToIP: AWS's
// calculateSystemIPs still calls it directly (see stackit_provider.go).
func TestAddOffsetToIP(t *testing.T) {
	tests := []struct {
		name   string
		baseIP string
		offset int
		want   string
	}{
		{
			name:   "simple offset within same subnet",
			baseIP: "10.10.1.0",
			offset: 5,
			want:   "10.10.1.5",
		},
		{
			name:   "offset to end of subnet",
			baseIP: "10.10.1.0",
			offset: 254,
			want:   "10.10.1.254",
		},
		{
			name:   "offset with overflow to next subnet",
			baseIP: "10.10.1.200",
			offset: 100,
			want:   "10.10.2.44",
		},
		{
			name:   "zero offset",
			baseIP: "192.168.1.0",
			offset: 0,
			want:   "192.168.1.0",
		},
		{
			name:   "large offset",
			baseIP: "172.16.0.0",
			offset: 300,
			want:   "172.16.1.44",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addOffsetToIP(tt.baseIP, tt.offset)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestStackitReservedIPs_TripleDefaultsToSpanning proves
// configureSubnetReservedIPs for a stackit-triple bloc (empty
// network.strategy, so netlayout.DefaultNameFor("stackit", "ocfp-triple")
// resolves the strategy) routes through the same netlayout engine Task 7
// pinned for PVE, writing byte-identical values to
// TestSpanningReservedIPs_PerSubnetIndex's spanning golden. No STACKIT
// blocs are deployed, so this reroute has no live consumer to break: the
// point of this test is that the legacy hand-rolled offset table
// (getDefaultReservedIPAssignments) is gone and the written data comes
// from the shared strategy engine instead.
func TestStackitReservedIPs_TripleDefaultsToSpanning(t *testing.T) {
	cfg := &config.Config{
		Provider: "stackit",
		Network:  config.NetworkConfig{SubnetStrategy: "ocfp-triple"},
	}
	safe := newMockFullSafe()
	provider := NewStackitVaultProvider(cfg, safe, "prod")

	const (
		cidr0 = "10.4.4.0/22"
		cidr1 = "10.4.8.0/22"
		cidr2 = "10.4.12.0/22"
	)

	require.NoError(t, provider.configureSubnetReservedIPs(cidr0, DefaultSubnetType, 0, MgmtEnvType))
	require.NoError(t, provider.configureSubnetReservedIPs(cidr1, DefaultSubnetType, 1, MgmtEnvType))
	require.NoError(t, provider.configureSubnetReservedIPs(cidr2, DefaultSubnetType, 2, MgmtEnvType))
	require.NoError(t, provider.configureSubnetReservedIPs(cidr0, DefaultSubnetType, 0, OCFEnvType))
	require.NoError(t, provider.configureSubnetReservedIPs(cidr1, DefaultSubnetType, 1, OCFEnvType))

	mgmt0, err := safe.GetAll(provider.PathBuilder.GetReservedIPsPath(MgmtEnvType, DefaultSubnetType, 0))
	require.NoError(t, err)

	mgmt1, err := safe.GetAll(provider.PathBuilder.GetReservedIPsPath(MgmtEnvType, DefaultSubnetType, 1))
	require.NoError(t, err)

	mgmt2, err := safe.GetAll(provider.PathBuilder.GetReservedIPsPath(MgmtEnvType, DefaultSubnetType, 2))
	require.NoError(t, err)

	ocf0, err := safe.GetAll(provider.PathBuilder.GetReservedIPsPath(OCFEnvType, DefaultSubnetType, 0))
	require.NoError(t, err)

	ocf1, err := safe.GetAll(provider.PathBuilder.GetReservedIPsPath(OCFEnvType, DefaultSubnetType, 1))
	require.NoError(t, err)

	t.Run("Idx0Mgmt", func(t *testing.T) {
		assert.Equal(t, "10.4.4.3", mgmt0["bastion_ip"])
		assert.Equal(t, "10.4.4.4", mgmt0["bosh_ip"])
		assert.Equal(t, "10.4.4.5", mgmt0["vault_ip"])
		assert.Equal(t, "10.4.4.32", mgmt0["available_0"])
		assert.Equal(t, "10.4.4.63", mgmt0["available_1"])

		assert.NotContains(t, mgmt0, "doomsday_ip", "doomsday is pinned to subnet 1")
		assert.NotContains(t, mgmt0, "shout_ip", "shout is pinned to subnet 1")
		assert.NotContains(t, mgmt0, "ocfp_ui_ip", "ocfp_ui is pinned to subnet 2")
	})

	t.Run("Idx1Mgmt", func(t *testing.T) {
		assert.Equal(t, "10.4.8.18", mgmt1["doomsday_ip"])
		assert.Equal(t, "10.4.8.19", mgmt1["shout_ip"])
		assert.Equal(t, "10.4.8.5", mgmt1["vault_ip"], "vault is unpinned, present on every index")

		assert.NotContains(t, mgmt1, "bastion_ip", "bastion is pinned to subnet 0")
	})

	t.Run("Idx2Mgmt", func(t *testing.T) {
		assert.Equal(t, "10.4.12.17", mgmt2["ocfp_ui_ip"])
		assert.Equal(t, "10.4.12.5", mgmt2["vault_ip"], "vault is unpinned, present on every index")

		assert.NotContains(t, mgmt2, "bastion_ip", "bastion is pinned to subnet 0")
		assert.NotContains(t, mgmt2, "doomsday_ip", "doomsday is pinned to subnet 1")
	})

	t.Run("Idx0OCF", func(t *testing.T) {
		assert.Equal(t, "10.4.4.64", ocf0["bosh_ip"])
		assert.Equal(t, "10.4.4.97", ocf0["haproxy_ip"])
		assert.Equal(t, "10.4.4.96", ocf0["available_0"])

		assert.NotContains(t, ocf0, "blacksmith_ip", "blacksmith is pinned to subnet 1")
	})

	t.Run("Idx1OCF", func(t *testing.T) {
		assert.Equal(t, "10.4.8.67", ocf1["blacksmith_ip"])

		assert.NotContains(t, ocf1, "bosh_ip", "bosh is pinned to subnet 0")
	})
}

// TestStackitReservedIPs_SingleSubnetWideDefault locks the key set
// configureSubnetReservedIPs writes for a stackit-single bloc's one
// workload subnet (subnetNum 0, the wide default strategy — every static
// is unpinned/colocated, so nothing is dropped). This is the STACKIT vault
// provider's own single-subnet path (Layer B): it always calls
// configureSubnetReservedIPs with a real index (0), unlike
// internal/bootstrap's createStackitSingleSubnet (Layer A), which resolves
// output keys through netlayout.Layout.LayerASlots with idx -1 — see
// internal/bootstrap/reserved_ip_layout_test.go's
// TestStackitSingleSubnet_WideDefault_LocksNegativeIndexKeySet and
// TestStackitSingleSubnet_SpanningDropsPinnedStatics for that idx -1
// contract, which this package's helper never receives.
func TestStackitReservedIPs_SingleSubnetWideDefault(t *testing.T) {
	cfg := &config.Config{Provider: "stackit"}
	safe := newMockFullSafe()
	provider := NewStackitVaultProvider(cfg, safe, "prod")

	const cidr = "10.4.0.0/22"

	require.NoError(t, provider.configureSubnetReservedIPs(cidr, DefaultSubnetType, 0, MgmtEnvType))

	mgmt, err := safe.GetAll(provider.PathBuilder.GetReservedIPsPath(MgmtEnvType, DefaultSubnetType, 0))
	require.NoError(t, err)

	wantNamedKeys := []string{
		"bastion_ip", "bosh_ip", "vault_ip", "jumpbox_ip", "concourse_ip",
		"prometheus_ip", "shield_ip", "blacksmith_ip", "artifacts_ip",
		"wireguard_ip", "ovpn_ip", "rustfs_ip", "proxycache_ip", "nfs_ip",
		"ocfp_ui_ip", "doomsday_ip", "shout_ip", "garage_ip",
		"rustfs_ip_smoke", "garage_ip_smoke",
	}

	for _, key := range wantNamedKeys {
		assert.Contains(t, mgmt, key, "wide's colocated mgmt statics all apply to the single subnet's index 0")
	}

	// available_*/reserved_* are the range-derived keys, not named statics;
	// counting only the named ones keeps this assertion resilient to a
	// future band-shape change.
	namedCount := 0

	for key := range mgmt {
		for _, want := range wantNamedKeys {
			if key == want {
				namedCount++

				break
			}
		}
	}

	assert.Equal(t, len(wantNamedKeys), namedCount, "no extra/missing named statics beyond wide's 20")
}
