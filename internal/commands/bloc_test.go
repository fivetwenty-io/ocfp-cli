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

const showConfig = `blocs:
  lab:
    provider: pve
    iaas: proxmox
    region: lab-region
    project_id: proj-1
    api_endpoint: https://FAKE-UNLISTED-VALUE.example
    auth_token: FAKE-TOKEN-DO-NOT-USE-AUTH
    password: FAKE-TOKEN-DO-NOT-USE-PASSWORD
    network:
      cidr: 10.20.0.0/20
    artifacts:
      enabled: true
  quiet:
    provider: pve
    region: lab-region
`

const showState = `{
  "version": "1.0",
  "provider": "pve",
  "region": "lab-region",
  "createdAt": "2026-10-01T00:00:00Z",
  "updatedAt": "2026-10-25T18:02:11Z",
  "resources": {
    "network.lab-net": {"id": "vnet1", "type": "network", "name": "lab-net", "properties": {"cidr": "10.20.0.0/20"}},
    "compute_instance.lab-bastion": {"id": "100", "type": "compute_instance", "name": "lab-bastion"}
  },
  "outputs": {
    "bastion_id": "100",
    "bastion_private_ip": "10.20.0.5",
    "bastion_ssh_host": "10.20.0.5",
    "keypair_public_key": "ssh-ed25519 FAKE-TOKEN-DO-NOT-USE-KEY",
    "reserved_bosh_ip": "10.20.0.4",
    "reserved_vault_ip": "10.20.0.6"
  }
}`

// seedShowBloc writes the lab bloc's config and state and returns the config path.
func seedShowBloc(t *testing.T) string {
	t.Helper()

	path := writeBlocsConfig(t, showConfig)
	writeBlocState(t, "lab", showState)

	return path
}

func TestBlocShow_NamedBlocText(t *testing.T) {
	isolateBlocEnv(t)
	cfgPath := seedShowBloc(t)

	out, err := runBloc(t, "show", "lab")
	require.NoError(t, err)

	for _, want := range []string{
		"Bloc", "lab", "both",
		"Config", cfgPath, "proxmox", "10.20.0.0/20", "proj-1",
		"State", "2026-10-25T18:02:11Z", "10.20.0.5", "lab-net",
		"compute_instance", "network",
		"reserved_bosh_ip", "reserved_vault_ip",
	} {
		assert.Contains(t, out, want)
	}

	assert.NotContains(t, out, "Live")
}

func TestBlocShow_NeverPrintsSecrets(t *testing.T) {
	isolateBlocEnv(t)
	seedShowBloc(t)

	for _, args := range [][]string{{"show", "lab"}, {"show", "lab", "--json"}} {
		out, err := runBloc(t, args...)
		require.NoError(t, err)

		for _, forbidden := range []string{
			"FAKE-TOKEN-DO-NOT-USE", "FAKE-UNLISTED-VALUE", "auth_token", "keypair_public_key",
		} {
			assert.NotContains(t, out, forbidden, "args %v", args)
		}
	}
}

func TestBlocShow_JSON(t *testing.T) {
	isolateBlocEnv(t)
	t.Setenv("OCFP_BLOC", "lab")

	cfgPath := seedShowBloc(t)

	out, err := runBloc(t, "show", "lab", "--json")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)

	assert.Equal(t, "lab", got["name"])
	assert.Equal(t, true, got["current"])
	assert.NotContains(t, got, "live")

	cfg, ok := got["config"].(map[string]any)
	require.True(t, ok, "config object")
	assert.Equal(t, cfgPath, cfg["file"])
	assert.Equal(t, "pve", cfg["provider"])
	assert.Equal(t, "lab-region", cfg["region"])
	assert.Equal(t, "10.20.0.0/20", cfg["network_cidr"])
	assert.Equal(t, true, cfg["artifacts_enabled"])

	st, ok := got["state"].(map[string]any)
	require.True(t, ok, "state object")
	assert.Equal(t, "2026-10-25T18:02:11Z", st["updated_at"])
	assert.EqualValues(t, 6, st["output_count"])
	assert.EqualValues(t, 2, st["reserved_output_count"])
	assert.Equal(t, []any{"reserved_bosh_ip", "reserved_vault_ip"}, st["reserved_output_sample"])
	assert.Equal(t, map[string]any{"id": "100", "private_ip": "10.20.0.5", "public_ip": "", "ssh_host": "10.20.0.5"}, st["bastion"])
	assert.Equal(t, map[string]any{"name": "lab-net", "cidr": "10.20.0.0/20"}, st["network"])
	assert.Equal(t, map[string]any{"network": float64(1), "compute_instance": float64(1)}, st["resources"])
}

func TestBlocShow_ConfigOnlyBlocOmitsState(t *testing.T) {
	isolateBlocEnv(t)
	seedShowBloc(t)

	out, err := runBloc(t, "show", "quiet", "--json")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Contains(t, got, "config")
	assert.NotContains(t, got, "state")

	text, err := runBloc(t, "show", "quiet")
	require.NoError(t, err)
	assert.Contains(t, text, "no state")
}

func TestBlocShow_BrokenState(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocState(t, "wrecked", "{{{ FAKE-TOKEN-DO-NOT-USE")

	out, err := runBloc(t, "show", "wrecked")
	require.NoError(t, err, "a broken bloc is shown, not failed")
	assert.Contains(t, out, "broken")
	assert.NotContains(t, out, "FAKE-TOKEN-DO-NOT-USE")

	jsonOut, err := runBloc(t, "show", "wrecked", "--json")
	require.NoError(t, err)
	assert.Contains(t, jsonOut, `"source": "broken"`)
	assert.NotContains(t, jsonOut, "FAKE-TOKEN-DO-NOT-USE")
}

func TestBlocShow_DefaultsToCurrentBloc(t *testing.T) {
	isolateBlocEnv(t)
	t.Setenv("OCFP_BLOC", "lab")
	seedShowBloc(t)

	out, err := runBloc(t, "show")
	require.NoError(t, err)
	assert.Contains(t, out, "lab-net")
}

func TestBlocShow_DefaultsToSoleConfiguredBloc(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocsConfig(t, "blocs:\n  only:\n    provider: pve\n    region: lab\n")

	out, err := runBloc(t, "show")
	require.NoError(t, err)
	assert.Contains(t, out, "only")
}

func TestBlocShow_UnknownBloc(t *testing.T) {
	isolateBlocEnv(t)
	seedShowBloc(t)

	_, err := runBloc(t, "show", "nope")
	require.ErrorIs(t, err, ErrBlocNotFound)
	assert.Contains(t, err.Error(), "nope")
	assert.Contains(t, err.Error(), "ocfp bloc list")
}

func TestBlocShow_NoCurrentBloc(t *testing.T) {
	isolateBlocEnv(t)
	seedShowBloc(t)

	_, err := runBloc(t, "show")
	require.ErrorIs(t, err, ErrNoCurrentBloc)
	assert.Contains(t, err.Error(), "ocfp bloc list")
}

func TestBlocShow_TooManyArgs(t *testing.T) {
	isolateBlocEnv(t)

	_, err := runBloc(t, "show", "a", "b")
	require.Error(t, err)
}
