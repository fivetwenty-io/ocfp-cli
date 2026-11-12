package commands

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
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

// treeOf lists every path under each root with its size, so a test can
// tell whether a run wrote, removed, or changed anything there.
func treeOf(t *testing.T, roots ...string) map[string]int64 {
	t.Helper()

	tree := map[string]int64{}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			info, err := entry.Info()
			if err != nil {
				return err
			}

			tree[path] = info.Size()

			return nil
		})
		require.NoError(t, err)
	}

	return tree
}

// fakeDeployTools puts bosh and cf on PATH as scripts that record their
// arguments in the log at logPath.
func fakeDeployTools(t *testing.T, logPath string) {
	t.Helper()

	bin := t.TempDir()
	for _, tool := range []string{"bosh", "cf"} {
		writeFakeExecutableFile(t, filepath.Join(bin, tool),
			"#!/bin/sh\necho "+tool+" \"$@\" >> '"+logPath+"'\n")
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// stubBastionInit replaces the full bastion init that init all runs first,
// and records the dry-run setting each call got.
func stubBastionInit(t *testing.T) *[]bool {
	t.Helper()

	var calls []bool

	saved := runBastionInitFn
	runBastionInitFn = func(_ context.Context, _ *config.Config, mode bastionInitMode, params bastionRunParams) error {
		assert.Equal(t, bastionModeFull, mode)

		calls = append(calls, params.dryRun)

		return nil
	}

	t.Cleanup(func() { runBastionInitFn = saved })

	return &calls
}

// A dry run of an init component other than bastion changes nothing. It
// doesn't deploy with bosh or log in with cf, which can reach the bastion
// through a proxy, and it doesn't write env files, manifests, or the
// current bloc on the workstation. Init all still runs the bastion dry run.
func TestInitDryRunRunsNoComponent(t *testing.T) {
	cases := []struct {
		component string
		bloc      string
	}{
		{"aws", "ocfp-aws-dry"},
		{"gcp", "ocfp-gcp-dry"},
		{"pve", "ocfp-pve-dc1"},
		{"pg", ""},
		{"bosh", ""},
		{"cf", ""},
		{KeywordAll, ""},
	}

	for _, tc := range cases {
		t.Run(tc.component, func(t *testing.T) {
			cfg, sshLog, home := connectivityCheckFixture(t, "", 0)
			t.Setenv("OCFP_BLOC", tc.bloc)

			ocfpHome, stateHome, configHome := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("OCFP_HOME", ocfpHome)
			t.Setenv("XDG_STATE_HOME", stateHome)
			t.Setenv("XDG_CONFIG_HOME", configHome)

			toolLog := filepath.Join(t.TempDir(), "tools.log")
			fakeDeployTools(t, toolLog)

			bastionCalls := stubBastionInit(t)
			roots := []string{home, ocfpHome, config.DataHome(), stateHome, configHome}
			before := treeOf(t, roots...)

			f := &initFlags{dryRun: true}
			require.NoError(t, f.executeInitialization(context.Background(), makeCmdNoBloc(t), cfg, tc.component))

			for _, log := range []string{sshLog, toolLog} {
				_, err := os.Stat(log)
				assert.True(t, os.IsNotExist(err), "a dry run must not run ssh, bosh, or cf")
			}

			assert.Equal(t, before, treeOf(t, roots...), "a dry run must not write anything")

			if tc.component == KeywordAll {
				assert.Equal(t, []bool{true}, *bastionCalls, "init all runs the bastion dry run")
			} else {
				assert.Empty(t, *bastionCalls)
			}
		})
	}
}

// A dry run still refuses a bloc name that the real run would refuse.
func TestInitDryRunStillChecksTheBlocName(t *testing.T) {
	for _, component := range []string{"aws", "gcp", "pve"} {
		t.Run(component, func(t *testing.T) {
			cfg, _, _ := connectivityCheckFixture(t, "", 0)
			t.Setenv("OCFP_BLOC", "")

			f := &initFlags{dryRun: true}
			err := f.executeInitialization(context.Background(), makeCmdNoBloc(t), cfg, component)
			require.ErrorIs(t, err, ErrBlocMissing)
		})
	}
}

// A real init all still runs every component after the bastion. Here the
// BOSH step stops because the bloc has no bootstrap state.
func TestInitAllRealRunStillRunsEachComponent(t *testing.T) {
	cfg, _, _ := connectivityCheckFixture(t, "", 0)

	bastionCalls := stubBastionInit(t)

	f := &initFlags{}
	err := f.executeInitialization(context.Background(), makeCmdNoBloc(t), cfg, KeywordAll)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BOSH initialization failed")
	assert.Equal(t, []bool{false}, *bastionCalls)
}
