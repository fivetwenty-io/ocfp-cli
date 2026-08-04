package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAWSReservedIPs_EmptyStrategyDefaultsToSpanning proves AWS's
// configureSubnetReservedIPs for a /22 ocfp subnet at idx 0/1/2, with an
// empty network.strategy AWS config (Provider set to "aws" the way the
// real AWS flow constructs it — never hand-set on Network.Strategy),
// resolves through netlayout.DefaultNameFor("aws", "") to spanning and
// writes byte-identical values to Task 7's spanning golden
// (TestSpanningReservedIPs_PerSubnetIndex / spanning_golden_test.go). The
// legacy hand-rolled calculateSystemIPs table (cf_router_*/diego_cell_*
// offsets) is gone: this test also locks that those legacy keys never
// appear, since CF router/Diego addressing now lives in the
// public-ips/load-balancer paths, not subnet reserved-ips.
func TestAWSReservedIPs_EmptyStrategyDefaultsToSpanning(t *testing.T) {
	cfg := &config.Config{Provider: "aws"}
	safe := newMockFullSafe()
	provider := NewAWSVaultProvider(cfg, safe, "prod")

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
		assert.Equal(t, "10.4.4.4", mgmt0["ip"], "bosh_ip aliases to ip, same as PVE's writeTieredReservedIPs")
		assert.Equal(t, "10.4.4.4", mgmt0["director_ip"], "bosh_ip aliases to director_ip, same as PVE's writeTieredReservedIPs")
		assert.Equal(t, "10.4.4.5", mgmt0["vault_ip"])
		assert.Equal(t, "10.4.4.32", mgmt0["available_0"])
		assert.Equal(t, "10.4.4.63", mgmt0["available_1"])
		assert.Equal(t, "10.4.4.0", mgmt0["reserved_0"])
		assert.Equal(t, "10.4.4.31", mgmt0["reserved_1"])
		assert.Equal(t, "10.4.4.64", mgmt0["reserved_2"])
		assert.Equal(t, "10.4.7.254", mgmt0["reserved_3"])

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
		assert.Equal(t, "10.4.4.64", ocf0["ip"], "bosh_ip aliases to ip")
		assert.Equal(t, "10.4.4.64", ocf0["director_ip"], "bosh_ip aliases to director_ip")
		assert.Equal(t, "10.4.4.97", ocf0["haproxy_ip"])
		assert.Equal(t, "10.4.4.96", ocf0["available_0"])

		assert.NotContains(t, ocf0, "blacksmith_ip", "blacksmith is pinned to subnet 1")
	})

	t.Run("Idx1OCF", func(t *testing.T) {
		assert.Equal(t, "10.4.8.67", ocf1["blacksmith_ip"])

		assert.NotContains(t, ocf1, "bosh_ip", "bosh is pinned to subnet 0")
	})

	t.Run("LegacyOffsetTableKeysGone", func(t *testing.T) {
		for _, table := range []map[string]interface{}{mgmt0, mgmt1, mgmt2, ocf0, ocf1} {
			assert.NotContains(t, table, "cf_router_0_ip")
			assert.NotContains(t, table, "cf_router_1_ip")
			assert.NotContains(t, table, "diego_cell_0_ip")
			assert.NotContains(t, table, "diego_cell_1_ip")
		}
	})
}
