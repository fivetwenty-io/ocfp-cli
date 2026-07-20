package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeBlocConfig writes a minimal ~/.ocfp/config.yml into a temp OCFP_HOME
// and points the process at it for the duration of the test.
func writeBlocConfig(t *testing.T, body string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("OCFP_HOME", home)

	err := os.WriteFile(filepath.Join(home, "config.yml"), []byte(body), 0o600)
	require.NoError(t, err)
}

func TestInceptionVaultPort_DistinctPerBloc(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())
	t.Setenv(config.InceptionVaultPortEnvVar, "")

	blocs := []string{
		"ocfp-lab-dbell",
		"ocfp-lab-drgao",
		"ocfp-lab-drhu",
		"ocfp-lab-itsouvalas",
		"ocfp-lab-krutten",
		"ocfp-lab-nabramovitz",
		"ocfp-lab-wayne",
	}

	seen := make(map[int]string, len(blocs))

	for _, bloc := range blocs {
		port := config.InceptionVaultPort(bloc)

		other, collides := seen[port]
		assert.False(t, collides, "blocs %q and %q both resolved to port %d", bloc, other, port)

		seen[port] = bloc
	}
}

func TestInceptionVaultPort_StableAcrossCalls(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())
	t.Setenv(config.InceptionVaultPortEnvVar, "")

	first := config.InceptionVaultPort("ocfp-lab-drgao")
	second := config.InceptionVaultPort("ocfp-lab-drgao")

	assert.Equal(t, first, second, "per-bloc port must be rediscoverable by later commands")
}

func TestInceptionVaultPort_InRange(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())
	t.Setenv(config.InceptionVaultPortEnvVar, "")

	port := config.InceptionVaultPort("ocfp-lab-krutten")

	assert.GreaterOrEqual(t, port, config.InceptionVaultPortRangeStart)
	assert.Less(t, port, config.InceptionVaultPortRangeStart+config.InceptionVaultPortRangeSize)
}

func TestInceptionVaultPort_EmptyBlocUsesLegacyPort(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())
	t.Setenv(config.InceptionVaultPortEnvVar, "")

	assert.Equal(t, config.LegacyInceptionVaultPort, config.InceptionVaultPort(""))
}

func TestInceptionVaultPort_EnvVarWins(t *testing.T) {
	writeBlocConfig(t, "blocs:\n  ocfp-lab-drgao:\n    name: ocfp-lab-drgao\n    vault_inception_port: 18999\n")
	t.Setenv(config.InceptionVaultPortEnvVar, "18500")

	assert.Equal(t, 18500, config.InceptionVaultPort("ocfp-lab-drgao"))
}

func TestInceptionVaultPort_ConfigFieldWinsOverDefault(t *testing.T) {
	writeBlocConfig(t, "blocs:\n  ocfp-lab-drgao:\n    name: ocfp-lab-drgao\n    vault_inception_port: 18999\n")
	t.Setenv(config.InceptionVaultPortEnvVar, "")

	assert.Equal(t, 18999, config.InceptionVaultPort("ocfp-lab-drgao"))
}

func TestInceptionVaultPort_ConfigFieldOnlyAppliesToOwnBloc(t *testing.T) {
	writeBlocConfig(t, "blocs:\n  ocfp-lab-drgao:\n    name: ocfp-lab-drgao\n    vault_inception_port: 18999\n")
	t.Setenv(config.InceptionVaultPortEnvVar, "")

	assert.NotEqual(t, 18999, config.InceptionVaultPort("ocfp-lab-drhu"))
}

func TestInceptionVaultPort_IgnoresInvalidEnvVar(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	for _, bad := range []string{"not-a-port", "0", "-1", "70000"} {
		t.Setenv(config.InceptionVaultPortEnvVar, bad)

		assert.Equal(t, config.DeterministicInceptionVaultPort("ocfp-lab-drhu"),
			config.InceptionVaultPort("ocfp-lab-drhu"),
			"invalid %s=%q must fall through to the per-bloc default", config.InceptionVaultPortEnvVar, bad)
	}
}

func TestDeterministicInceptionVaultPort_NoConfigDependency(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	// The deterministic default must not depend on env or config: two agents on
	// the same workstation must compute the same port for the same bloc.
	assert.Equal(t, config.DeterministicInceptionVaultPort("ocfp-lab-dbell"),
		config.DeterministicInceptionVaultPort("ocfp-lab-dbell"))
	assert.NotEqual(t, config.DeterministicInceptionVaultPort("ocfp-lab-dbell"),
		config.DeterministicInceptionVaultPort("ocfp-lab-drhu"))
}
