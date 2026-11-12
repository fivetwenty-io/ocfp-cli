package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prerequisiteRefusal sets up one way the bastion connectivity check
// refuses before it runs ssh.
type prerequisiteRefusal struct {
	name  string
	setup func(t *testing.T, cfg *config.Config)
}

var prerequisiteRefusals = []prerequisiteRefusal{
	{"invalid SSH user", func(_ *testing.T, cfg *config.Config) {
		cfg.Bastion.SSHUser = "ubuntu;id"
	}},
	{"invalid bastion IP", func(_ *testing.T, cfg *config.Config) {
		cfg.BastionIP = "10.61.148.10;id"
	}},
	{"invalid key path", func(t *testing.T, cfg *config.Config) {
		t.Helper()

		dataHome := filepath.Join(t.TempDir(), "data home")
		t.Setenv("XDG_DATA_HOME", dataHome)

		keyPath := filepath.Join(config.DataHome(), cfg.Name, "ssh", "id_ed25519")
		require.NoError(t, os.MkdirAll(filepath.Dir(keyPath), 0o700))
		writeTestEd25519Key(t, keyPath)
	}},
	{"unusable known_hosts path", func(t *testing.T, _ *config.Config) {
		t.Helper()

		home := filepath.Join(t.TempDir(), "home${X}")
		require.NoError(t, os.MkdirAll(home, 0o700))
		t.Setenv("HOME", home)
	}},
}

// prerequisiteResult is what one prerequisite check returned and whether
// ssh has run.
type prerequisiteResult struct {
	err    string
	ranSSH bool
}

// runPrerequisites runs the prerequisite check for component in a dry or
// real run on cfg, and reports whether it ran ssh, which records its
// arguments in sshLog.
func runPrerequisites(t *testing.T, cfg *config.Config, sshLog, component string, dryRun bool) prerequisiteResult {
	t.Helper()

	f := &initFlags{dryRun: dryRun}
	err := f.validatePrerequisitesIfNeeded(context.Background(), cfg, component)

	result := prerequisiteResult{}
	if err != nil {
		result.err = err.Error()
	}

	_, statErr := os.Stat(sshLog)
	result.ranSSH = statErr == nil

	return result
}

// A dry run makes the checks of the bastion connectivity check that only
// read, so it refuses a bad SSH user, bastion IP, key path, or known_hosts
// path with the error the real run gives, and the real run refuses all of
// them before it runs ssh. Bastion init only warns about them, in both
// forms.
func TestPrerequisiteDryRunRefusesWhereTheRealRunRefuses(t *testing.T) {
	for _, refusal := range prerequisiteRefusals {
		for _, component := range initComponents {
			t.Run(refusal.name+"/"+component, func(t *testing.T) {
				cfg, sshLog, _ := connectivityCheckFixture(t, "", 0)
				refusal.setup(t, cfg)

				real := runPrerequisites(t, cfg, sshLog, component, false)
				dry := runPrerequisites(t, cfg, sshLog, component, true)

				assert.False(t, real.ranSSH, "the real run refuses before it runs ssh")
				assert.False(t, dry.ranSSH, "a dry run never runs ssh")
				assert.Equal(t, real.err, dry.err)

				if component == RoleBastion {
					assert.Empty(t, real.err, "bastion init only warns")

					return
				}

				assert.Contains(t, real.err, "prerequisite check failed: bastion check failed: ")
			})
		}
	}
}

// A dry run that passes every check it can make says it didn't check the
// connection or the host key, and never claims the bastion is reachable.
// It leaves known_hosts alone.
func TestPrerequisiteDryRunSaysWhatItDidNotCheck(t *testing.T) {
	for _, component := range initComponents {
		t.Run(component, func(t *testing.T) {
			logs := captureInitLog(t)
			cfg, sshLog, home := connectivityCheckFixture(t, "", 0)

			f := &initFlags{dryRun: true}
			require.NoError(t, f.validatePrerequisitesIfNeeded(context.Background(), cfg, component))

			assert.Equal(t, 1, logs.FilterMessage(
				"DRY RUN: Did not check SSH connectivity or the bastion's host key, because that connects to the bastion").Len())
			assert.Zero(t, logs.FilterMessage("Bastion connectivity verified").Len())
			assert.Zero(t, logs.FilterMessageSnippet("DRY RUN: Would check").Len())

			_, err := os.Stat(sshLog)
			assert.True(t, os.IsNotExist(err), "a dry run must not run ssh")

			_, err = os.Stat(filepath.Join(home, ".ssh", "known_hosts"))
			assert.True(t, os.IsNotExist(err), "a dry run must not write known_hosts")
		})
	}
}

// Without a bastion IP in the config, the real run probes the bastion at
// the address the state cache holds, asks the provider when that fails,
// and records the address it finds. A dry run does none of that, so it
// says it checked nothing of the connection, and it passes, because the
// real run skips the check when the lookup fails.
func TestPrerequisiteDryRunWithoutAConfiguredIP(t *testing.T) {
	logs := captureInitLog(t)
	cfg, sshLog, _ := connectivityCheckFixture(t, "", 0)
	cfg.BastionIP = ""
	cfg.Bastion.SSHUser = "ubuntu;id"

	f := &initFlags{dryRun: true}
	require.NoError(t, f.validatePrerequisitesIfNeeded(context.Background(), cfg, "bosh"))

	assert.Equal(t, 1, logs.FilterMessage(
		"DRY RUN: Did not check the bastion's IP, SSH key, connectivity, or host key, because finding its IP probes the bastion and records the address it finds").Len())
	assert.Zero(t, logs.FilterMessageSnippet("Could not resolve bastion IP").Len(), "a dry run never asks the provider")

	_, err := os.Stat(sshLog)
	assert.True(t, os.IsNotExist(err), "a dry run must not run ssh")
}

// Without a bloc SSH key, both forms warn and skip the connection check,
// so a dry run passes exactly where the real run does.
func TestPrerequisiteDryRunWithoutAnSSHKey(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		logs := captureInitLog(t)
		cfg, sshLog, _ := connectivityCheckFixture(t, "", 0)
		require.NoError(t, os.RemoveAll(filepath.Join(config.DataHome(), cfg.Name, "ssh")))

		f := &initFlags{dryRun: dryRun}
		require.NoError(t, f.validatePrerequisitesIfNeeded(context.Background(), cfg, "bosh"))

		assert.Equal(t, 1, logs.FilterMessage("Could not find SSH key for bastion, skipping connectivity check").Len())
		assert.Zero(t, logs.FilterMessageSnippet("DRY RUN: Did not check").Len(), "nothing was left unchecked")

		_, err := os.Stat(sshLog)
		assert.True(t, os.IsNotExist(err), "neither form runs ssh without a key")
	}
}
