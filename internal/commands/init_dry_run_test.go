package commands

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A dry run of init bastion must not connect to the bastion, so it skips
// the SSH connectivity check that a real run makes first. The check runs
// ssh, which can write the bastion's host key to known_hosts and leaves a
// login record on the bastion.
func TestInitBastionDryRunSkipsTheConnectivityCheck(t *testing.T) {
	cfg, logPath, _ := connectivityCheckFixture(t, "", 0)

	f := &initFlags{dryRun: true}
	require.NoError(t, f.validatePrerequisitesIfNeeded(context.Background(), cfg, RoleBastion))

	_, err := os.Stat(logPath)
	assert.True(t, os.IsNotExist(err), "a dry run must not run ssh")
}

// A real run of init bastion still checks connectivity first.
func TestInitBastionRealRunChecksConnectivity(t *testing.T) {
	cfg, logPath, _ := connectivityCheckFixture(t, "", 0)

	f := &initFlags{}
	require.NoError(t, f.validatePrerequisitesIfNeeded(context.Background(), cfg, RoleBastion))

	_, err := os.Stat(logPath)
	assert.NoError(t, err, "a real run must run the ssh check")
}

// A dry run's closing line says the dry run finished, not that the work it
// skipped was done. A real run's line is unchanged.
func TestBastionInitDoneLine(t *testing.T) {
	t.Parallel()

	for _, mode := range []bastionInitMode{
		bastionModeFull, bastionModeGenesis, bastionModeOCFP, bastionModeConfig, bastionModeVaultBootUnit,
	} {
		text := mode.text()
		assert.Equal(t, text.done, text.doneLine(false), mode.String())
		assert.Equal(t, "Dry run of the "+text.failed+" finished without changing anything",
			text.doneLine(true), mode.String())
	}
}
