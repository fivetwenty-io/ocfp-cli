package commands

import (
	"context"
	"os"
	"path/filepath"
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

// initComponents lists every component init accepts. Each one reaches the
// prerequisite check, which ssh's into the bastion in a real run.
var initComponents = []string{"aws", "gcp", "pve", "pg", "cf", "bosh", RoleBastion, KeywordAll}

// A dry run of any init component skips the bastion connectivity check.
// The check runs ssh with accept-new, which writes the bastion's host key
// to our known_hosts on first contact and leaves a login record on the
// bastion. The prerequisite step also leaves the deployment directory
// alone, because a dry run changes nothing on the workstation either.
func TestInitDryRunSkipsTheConnectivityCheckForEveryComponent(t *testing.T) {
	for _, component := range initComponents {
		t.Run(component, func(t *testing.T) {
			cfg, logPath, home := connectivityCheckFixture(t, "", 0)

			f := &initFlags{dryRun: true}
			require.NoError(t, f.validatePrerequisitesIfNeeded(context.Background(), cfg, component))

			_, err := os.Stat(logPath)
			assert.True(t, os.IsNotExist(err), "a dry run must not run ssh")

			_, err = os.Stat(filepath.Join(home, "deployments", cfg.Name))
			assert.True(t, os.IsNotExist(err), "a dry run must not create the deployment directory")
		})
	}
}

// A real run of every component other than bastion still checks
// connectivity and creates the deployment directory it needs.
func TestInitRealRunChecksConnectivityForEveryComponent(t *testing.T) {
	for _, component := range initComponents {
		if component == RoleBastion {
			continue
		}

		t.Run(component, func(t *testing.T) {
			cfg, logPath, home := connectivityCheckFixture(t, "", 0)

			f := &initFlags{}
			require.NoError(t, f.validatePrerequisitesIfNeeded(context.Background(), cfg, component))

			_, err := os.Stat(logPath)
			require.NoError(t, err, "a real run must run the ssh check")

			info, err := os.Stat(filepath.Join(home, "deployments", cfg.Name))
			require.NoError(t, err, "a real run creates the deployment directory")
			assert.True(t, info.IsDir())
		})
	}
}

// A real run of a component other than bastion still fails when the
// bastion can't be reached.
func TestInitRealRunStopsOnAnUnreachableBastion(t *testing.T) {
	cfg, _, _ := connectivityCheckFixture(t, "ssh: connect to host 10.61.148.10 port 22: Connection refused\n", 255)

	f := &initFlags{}
	err := f.validatePrerequisitesIfNeeded(context.Background(), cfg, "bosh")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bastion check failed")
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
