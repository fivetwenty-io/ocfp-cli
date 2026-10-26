package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateBlocEnv gives a bloc test an empty home, an empty working directory,
// no OCFP_BLOC, and a clean viper, so no real config or state is ever read.
func isolateBlocEnv(t *testing.T) {
	t.Helper()

	isolateXDGHome(t)
	t.Setenv("OCFP_BLOC", "")
	t.Chdir(t.TempDir())
	viper.Reset()
	t.Cleanup(viper.Reset)
}

// writeBlocsConfig writes the XDG config.yml with the given blocs: map body.
func writeBlocsConfig(t *testing.T, body string) string {
	t.Helper()

	require.NoError(t, os.MkdirAll(config.ConfigHome(), 0o750))

	path := filepath.Join(config.ConfigHome(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

// writeBlocState writes a state file for bloc under the XDG state home.
func writeBlocState(t *testing.T, bloc, content string) string {
	t.Helper()

	dir := filepath.Join(config.StateHome(), bloc, "state")
	require.NoError(t, os.MkdirAll(dir, 0o750))

	path := filepath.Join(dir, bloc+".json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

// runBloc runs `ocfp bloc <args>` and returns what it wrote to stdout.
func runBloc(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer

	cmd := NewBlocCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)

	err := cmd.Execute()

	return out.String(), err
}

const labConfig = `blocs:
  ocfp-lab-wayneeseguin:
    provider: pve
    region: lab
  other-bloc:
    provider: pve
    region: lab
`

const labState = `{"version":"1.0","provider":"pve","region":"lab","updatedAt":"2026-10-25T18:02:11Z"}`

func TestBlocList_Table(t *testing.T) {
	isolateBlocEnv(t)
	t.Setenv("OCFP_BLOC", "ocfp-lab-wayneeseguin")

	cfgPath := writeBlocsConfig(t, labConfig)
	writeBlocState(t, "ocfp-lab-wayneeseguin", labState)
	writeBlocState(t, "old-bloc", `{"provider":"aws","region":"us-east-1","updatedAt":"2026-08-01T09:00:00Z"}`)
	writeBlocState(t, "bad-bloc", "{{{ not state")

	out, err := runBloc(t, "list")
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 5, out)

	assert.Equal(t, []string{"NAME", "PROVIDER", "REGION", "SOURCE", "CONFIG", "UPDATED", "CURRENT"}, strings.Fields(lines[0]))

	rows := map[string][]string{}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		rows[fields[0]] = fields
	}

	assert.Equal(t,
		[]string{"bad-bloc", "-", "-", "broken", "-", "-"},
		rows["bad-bloc"])
	assert.Equal(t,
		[]string{"ocfp-lab-wayneeseguin", "pve", "lab", "both", cfgPath, "2026-10-25T18:02:11Z", "*"},
		rows["ocfp-lab-wayneeseguin"])
	assert.Equal(t,
		[]string{"old-bloc", "aws", "us-east-1", "state", "-", "2026-08-01T09:00:00Z"},
		rows["old-bloc"])
	assert.Equal(t,
		[]string{"other-bloc", "pve", "lab", "config", cfgPath, "-"},
		rows["other-bloc"])

	assert.Less(t, strings.Index(out, "bad-bloc"), strings.Index(out, "ocfp-lab-wayneeseguin"), "rows are sorted by name")
}

func TestBlocList_JSON(t *testing.T) {
	isolateBlocEnv(t)
	t.Setenv("OCFP_BLOC", "ocfp-lab-wayneeseguin")

	cfgPath := writeBlocsConfig(t, labConfig)
	statePath := writeBlocState(t, "ocfp-lab-wayneeseguin", labState)

	out, err := runBloc(t, "list", "--json")
	require.NoError(t, err)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows), out)
	require.Len(t, rows, 2)

	lab := rows[0]
	assert.Equal(t, "ocfp-lab-wayneeseguin", lab["name"])
	assert.Equal(t, "pve", lab["provider"])
	assert.Equal(t, "lab", lab["region"])
	assert.Equal(t, "both", lab["source"])
	assert.Equal(t, true, lab["has_config"])
	assert.Equal(t, true, lab["has_state"])
	assert.Equal(t, false, lab["broken"])
	assert.Equal(t, cfgPath, lab["config_file"])
	assert.Equal(t, []any{cfgPath}, lab["config_files"])
	assert.Equal(t, statePath, lab["state_path"])
	assert.Equal(t, true, lab["current"])
	assert.Equal(t, "2026-10-25T18:02:11Z", lab["updated_at"])

	other := rows[1]
	assert.Equal(t, "other-bloc", other["name"])
	assert.Equal(t, "config", other["source"])
	assert.Equal(t, false, other["current"])
	assert.Nil(t, other["updated_at"], "a bloc with no state has a null updated_at")
	assert.Contains(t, other, "updated_at")
}

func TestBlocList_Empty(t *testing.T) {
	isolateBlocEnv(t)

	out, err := runBloc(t, "list")
	require.NoError(t, err)
	assert.Equal(t, "No blocs found\n", out)

	out, err = runBloc(t, "ls", "--json")
	require.NoError(t, err)
	assert.JSONEq(t, "[]", out)
}

func TestBlocList_ExplicitConfigFile(t *testing.T) {
	isolateBlocEnv(t)

	path := filepath.Join(t.TempDir(), "elsewhere.yml")
	require.NoError(t, os.WriteFile(path, []byte("blocs:\n  remote:\n    provider: aws\n"), 0o600))
	viper.Set("config", path)

	out, err := runBloc(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "remote")
}
