package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSpanningReservedIPs_PerSubnetIndex proves the spanning strategy's
// per-static subnet pinning reaches the vault layer through the full
// pveReservedIPsForSubnet path: each of a bloc's three physical /22
// workload subnets gets its OWN address space (10.4.4.0/22, 10.4.8.0/22,
// 10.4.12.0/22 — the state-driven per-index CIDR pattern), and a static
// pinned to one subnet index appears ONLY in that index's table, never the
// others, while an unpinned static (e.g. vault) appears identically on
// every index. This is the first strategy where subnetNum actually gates
// which roles appear — TestPVEReservedIPs_SubnetIndexDoesNotAffectRoleOffsets
// locks the OPPOSITE property for wide/compact, which have no pinning at
// all.
func TestSpanningReservedIPs_PerSubnetIndex(t *testing.T) {
	cfg := &config.Config{Network: config.NetworkConfig{Strategy: "spanning"}}

	const (
		cidr0 = "10.4.4.0/22"
		cidr1 = "10.4.8.0/22"
		cidr2 = "10.4.12.0/22"
	)

	mgmt0, err := pveReservedIPsForSubnet(cidr0, MgmtEnvType, 0, cfg, logger.Get())
	require.NoError(t, err)

	mgmt1, err := pveReservedIPsForSubnet(cidr1, MgmtEnvType, 1, cfg, logger.Get())
	require.NoError(t, err)

	mgmt2, err := pveReservedIPsForSubnet(cidr2, MgmtEnvType, 2, cfg, logger.Get())
	require.NoError(t, err)

	ocf0, err := pveReservedIPsForSubnet(cidr0, OCFEnvType, 0, cfg, logger.Get())
	require.NoError(t, err)

	ocf1, err := pveReservedIPsForSubnet(cidr1, OCFEnvType, 1, cfg, logger.Get())
	require.NoError(t, err)

	t.Run("Idx0Mgmt", func(t *testing.T) {
		assert.Equal(t, "10.4.4.3", mgmt0["bastion_ip"])
		assert.Equal(t, "10.4.4.4", mgmt0["bosh_ip"])
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
		assert.Equal(t, "10.4.4.97", ocf0["haproxy_ip"])
		assert.Equal(t, "10.4.4.96", ocf0["available_0"])

		assert.NotContains(t, ocf0, "blacksmith_ip", "blacksmith is pinned to subnet 1")
	})

	t.Run("Idx1OCF", func(t *testing.T) {
		assert.Equal(t, "10.4.8.67", ocf1["blacksmith_ip"])

		assert.NotContains(t, ocf1, "bosh_ip", "bosh is pinned to subnet 0")
	})
}
