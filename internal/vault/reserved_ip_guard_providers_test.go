package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The guard is a SafeInterface decorator rather than a change inside each
// provider, so PVE, AWS, and STACKIT are covered by one seam. These tests
// drive each provider's real reserved-IP writer through it to prove that,
// including the write sites (fallback subnets, compatibility aliases) that
// a per-provider fix would be easy to miss.

const guardTestBloc = "test-bloc"

// TestReservedIPGuardCoversPVEProvider drives the state-driven workload
// subnet writer, which is the exact path the 2026-07-28 incident ran.
func TestReservedIPGuardCoversPVEProvider(t *testing.T) {
	const (
		cidr       = "10.0.0.0/22"
		subnetPath = "secret/config/test-bloc/ocf/net/subnets/ocfp-0"
	)

	reservedPath := subnetPath + "/reserved-ips"

	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(reservedPath, map[string]interface{}{
		"bosh_ip":     "10.0.0.4",
		"director_ip": "10.0.0.4",
		"vault_ip":    "10.0.0.5",
	}))

	under.mutations = 0

	guard := newReservedIPGuard(under, false, logger.Get())
	provider := NewPVEVaultProvider(&config.Config{Provider: "pve"}, guard, guardTestBloc)

	require.NoError(t, provider.writeTieredReservedIPs(cidr, OCFEnvType, 0, "ocfp-0", subnetPath))

	assert.Zero(t, under.mutations)
	assert.Equal(t, "10.0.0.4", under.data[reservedPath]["bosh_ip"])
	assert.Equal(t, "10.0.0.4", under.data[reservedPath]["director_ip"])
	assert.Equal(t, "10.0.0.5", under.data[reservedPath]["vault_ip"])

	// The compatibility aliases are derived from bosh_ip, so all three must
	// be reported — an alias silently moving is the same defect.
	drifted := driftKeys(guard.Report())
	assert.Contains(t, drifted, "bosh_ip")
	assert.Contains(t, drifted, "director_ip")
	assert.Contains(t, drifted, "vault_ip")

	// Roles the bloc never had still arrive.
	assert.Contains(t, under.data[reservedPath], "haproxy_ip")
}

func TestReservedIPGuardCoversAWSProvider(t *testing.T) {
	cfg := &config.Config{Region: "us-east-1"}
	pathBuilder := NewPathBuilder(cfg, guardTestBloc)
	reservedPath := pathBuilder.GetReservedIPsPath(MgmtEnvType, "ocfp", 0)

	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(reservedPath, map[string]interface{}{
		"bosh_ip": "10.10.0.99",
	}))

	under.mutations = 0

	guard := newReservedIPGuard(under, false, logger.Get())
	provider := NewAWSVaultProvider(cfg, guard, guardTestBloc)

	require.NoError(t, provider.configureSubnetReservedIPs("10.10.0.0/24", "ocfp", 0, MgmtEnvType))

	assert.Zero(t, under.mutations)
	assert.Equal(t, "10.10.0.99", under.data[reservedPath]["bosh_ip"])
	assert.Contains(t, driftKeys(guard.Report()), "bosh_ip")
}

func TestReservedIPGuardCoversStackitProvider(t *testing.T) {
	cfg := &config.Config{Region: "eu01"}
	pathBuilder := NewPathBuilder(cfg, guardTestBloc)
	reservedPath := pathBuilder.GetReservedIPsPath(MgmtEnvType, DefaultSubnetType, 0)

	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(reservedPath, map[string]interface{}{
		"bosh_ip": "10.20.0.99",
	}))

	under.mutations = 0

	guard := newReservedIPGuard(under, false, logger.Get())
	provider := NewStackitVaultProvider(cfg, guard, guardTestBloc)

	require.NoError(t, provider.configureSubnetReservedIPs("10.20.0.0/24", DefaultSubnetType, 0, MgmtEnvType))

	assert.Zero(t, under.mutations)
	assert.Equal(t, "10.20.0.99", under.data[reservedPath]["bosh_ip"])
	assert.Contains(t, driftKeys(guard.Report()), "bosh_ip")
}

// TestReservedIPGuardCoversPVEFallbackSubnets covers the stateless-fallback
// writer, which reaches vault by a different path than the state-driven one.
func TestReservedIPGuardCoversPVEFallbackSubnets(t *testing.T) {
	cfg := &config.Config{
		Provider: "pve",
		Network: config.NetworkConfig{
			CIDR: "10.30.0.0/22",
			Name: "vmbr0",
		},
	}
	pathBuilder := NewPathBuilder(cfg, guardTestBloc)
	reservedPath := pathBuilder.GetReservedIPsPath(MgmtEnvType, "ocfp", 0)

	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(reservedPath, map[string]interface{}{
		"bosh_ip": "10.30.0.99",
	}))

	under.mutations = 0

	guard := newReservedIPGuard(under, false, logger.Get())
	provider := NewPVEVaultProvider(cfg, guard, guardTestBloc)

	require.NoError(t, provider.writeFallbackSubnet(MgmtEnvType))

	assert.Zero(t, under.mutations)
	assert.Equal(t, "10.30.0.99", under.data[reservedPath]["bosh_ip"])
	assert.Contains(t, driftKeys(guard.Report()), "bosh_ip")
}

// driftKeys collects the reported drift keys for easy assertion.
func driftKeys(report ReservedIPReport) []string {
	keys := make([]string, 0, len(report.Drifts))
	for _, drift := range report.Drifts {
		keys = append(keys, drift.Key)
	}

	return keys
}
