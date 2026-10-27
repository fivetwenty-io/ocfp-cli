package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runRoot wires the root command the way Execute does, without the os.Exit
// and the provider registration, and runs it with args.
func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	require.NoError(t, viper.BindEnv("bloc", "OCFP_BLOC"))

	flags := setupFlags()
	root := createRootCommand()
	defineFlags(root, flags)
	bindFlagsToViper(root)

	lock := &lockInfo{}
	root.PersistentPreRun = createPreRunHandler(flags.blocName, lock)
	root.PersistentPostRun = createPostRunHandler(lock)
	RegisterCommands(root)

	var out bytes.Buffer

	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(args)

	err := root.Execute()
	releaseCommandLock(lock)

	return out.String(), err
}

func writeBlocConfig(t *testing.T) {
	t.Helper()

	require.NoError(t, os.MkdirAll(config.ConfigHome(), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(config.ConfigHome(), "config.yml"), []byte(
		"blocs:\n  foo:\n    provider: pve\n    region: lab\n  env-bloc:\n    provider: pve\n    region: lab\n  saved-bloc:\n    provider: pve\n    region: lab\n"), 0o600))
}

func TestBlocCommand_BlocFlagBeatsEnvAndSavedBloc(t *testing.T) {
	isolateXDGHome(t)
	t.Chdir(t.TempDir())
	t.Setenv("OCFP_BLOC", "env-bloc")
	writeBlocConfig(t)
	require.NoError(t, config.SetCurrentBloc("saved-bloc", filepath.Join(config.ConfigHome(), "config.yml")))

	out, err := runRoot(t, "--bloc", "foo", "bloc", "show", "--json")
	require.NoError(t, err)

	var shown map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &shown), out)
	assert.Equal(t, "foo", shown["name"], "show with no argument follows --bloc")
	assert.Equal(t, true, shown["current"])

	out, err = runRoot(t, "--bloc", "foo", "bloc", "list", "--json")
	require.NoError(t, err)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows), out)

	current := map[string]bool{}
	for _, row := range rows {
		current[row["name"].(string)], _ = row["current"].(bool)
	}

	assert.Equal(t, map[string]bool{"foo": true, "env-bloc": false, "saved-bloc": false}, current)
}

func TestBlocCommand_EnvBeatsSavedBlocWithoutTheFlag(t *testing.T) {
	isolateXDGHome(t)
	t.Chdir(t.TempDir())
	t.Setenv("OCFP_BLOC", "env-bloc")
	writeBlocConfig(t)
	require.NoError(t, config.SetCurrentBloc("saved-bloc", filepath.Join(config.ConfigHome(), "config.yml")))

	out, err := runRoot(t, "bloc", "show", "--json")
	require.NoError(t, err)

	var shown map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &shown), out)
	assert.Equal(t, "env-bloc", shown["name"])
}

func TestEnvList_MarksTheSameBlocAsBlocList(t *testing.T) {
	isolateXDGHome(t)
	t.Chdir(t.TempDir())
	t.Setenv("OCFP_BLOC", "env-bloc")
	writeBlocConfig(t)

	out, err := runRoot(t, "--bloc", "foo", "env", "list")
	require.NoError(t, err)

	active := map[string]bool{}

	for _, line := range bytes.Split([]byte(out), []byte("\n")) {
		fields := bytes.Fields(line)
		if len(fields) > 0 {
			active[string(fields[0])] = bytes.Contains(line, []byte("ACTIVE"))
		}
	}

	assert.True(t, active["foo"], out)
	assert.False(t, active["env-bloc"], out)
	assert.False(t, active["saved-bloc"], out)
}
