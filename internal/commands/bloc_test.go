package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	pveapierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
	"github.com/ocfp/ocfp-cli-go/internal/blocinv"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
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

// fakeComputeReader stands in for the provider's compute manager. It holds
// only the two read calls bloc show may make.
type fakeComputeReader struct {
	instances map[string]*cpi.Instance
	getErrs   map[string]error
	listed    []*cpi.Instance
	listErr   error
	gets      []string
	lists     int
}

func (f *fakeComputeReader) GetInstance(_ context.Context, id string) (*cpi.Instance, error) {
	f.gets = append(f.gets, id)

	if err := f.getErrs[id]; err != nil {
		return nil, err
	}

	inst, ok := f.instances[id]
	if !ok {
		return nil, vmNotFound(id)
	}

	return inst, nil
}

// ListInstances honors the filters the way a provider does: "name" must equal
// the VM's name and every other key must be a tag with that value.
func (f *fakeComputeReader) ListInstances(_ context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	f.lists++

	if f.listErr != nil {
		return nil, f.listErr
	}

	var matched []*cpi.Instance

	for _, inst := range f.listed {
		if instanceMatches(inst, filters) {
			matched = append(matched, inst)
		}
	}

	return matched, nil
}

func instanceMatches(inst *cpi.Instance, filters map[string]string) bool {
	for key, want := range filters {
		if key == "name" {
			if inst.Name != want {
				return false
			}

			continue
		}

		got, ok := inst.Tags[key]
		if !ok || got != want {
			return false
		}
	}

	return true
}

// vmNotFound is the error PVE's GetInstance returns for a VM id that does not
// exist.
func vmNotFound(id string) error {
	vmid, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("%w: %s", pve.ErrInvalidVMID, id)
	}

	return pve.ErrVMIDNotFound(vmid)
}

// useBlocLive swaps the live reader factory for one that returns reader, and
// reports how many times the factory ran.
func useBlocLive(t *testing.T, reader blocComputeReader, factoryErr error) *int {
	t.Helper()

	calls := 0
	original := newBlocLiveSource
	newBlocLiveSource = func(_ context.Context, _, _ string) (blocLiveSource, error) {
		calls++

		if factoryErr != nil {
			return blocLiveSource{}, factoryErr
		}

		return blocLiveSource{Reader: reader, ArtifactsEnabled: true, Close: func() {}}, nil
	}

	t.Cleanup(func() { newBlocLiveSource = original })

	return &calls
}

const liveState = `{
  "version": "1.0",
  "provider": "pve",
  "region": "lab-region",
  "updatedAt": "2026-10-25T18:02:11Z",
  "resources": {
    "compute_instance.lab-bastion": {"id": "100", "type": "compute_instance", "name": "lab-bastion"},
    "artifacts.lab-artifacts": {"id": "101", "type": "artifacts", "name": "lab-artifacts", "properties": {
      "vm_id": "101", "endpoint": "https://10.20.0.7:9000", "tls_mode": "internal-ca",
      "access_key": "FAKE-TOKEN-DO-NOT-USE-AK", "secret_key": "FAKE-TOKEN-DO-NOT-USE-SK"}}
  },
  "outputs": {"bastion_id": "100", "bastion_private_ip": "10.20.0.5"}
}`

func seedLiveBloc(t *testing.T) {
	t.Helper()

	writeBlocsConfig(t, showConfig)
	writeBlocState(t, "lab", liveState)
}

func liveJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()

	out, err := runBloc(t, append([]string{"show", "lab", "--json", "--live"}, args...)...)
	require.NoError(t, err)
	assert.NotContains(t, out, "FAKE-TOKEN-DO-NOT-USE")

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)

	return got
}

func TestBlocShowLive_Success(t *testing.T) {
	isolateBlocEnv(t)
	seedLiveBloc(t)

	reader := &fakeComputeReader{instances: map[string]*cpi.Instance{
		"100": {ID: "100", State: cpi.ResourceState("running")},
		"101": {ID: "101", State: cpi.ResourceState("stopped")},
	}}
	useBlocLive(t, reader, nil)

	got := liveJSON(t)

	live, ok := got["live"].(map[string]any)
	require.True(t, ok, "live object")
	assert.Equal(t,
		map[string]any{"vm_id": "100", "power": "running", "error": ""},
		live["bastion"])
	assert.Equal(t,
		map[string]any{"vm_id": "101", "power": "stopped", "endpoint": "https://10.20.0.7:9000", "tls_mode": "internal-ca", "error": ""},
		live["artifacts"])
	assert.ElementsMatch(t, []string{"100", "101"}, reader.gets)
	assert.Zero(t, reader.lists, "the state already names the artifacts VM")

	text, err := runBloc(t, "show", "lab", "--live")
	require.NoError(t, err)
	assert.Contains(t, text, "Live")
	assert.Contains(t, text, "running")
	assert.Contains(t, text, "stopped")
	assert.NotContains(t, text, "FAKE-TOKEN-DO-NOT-USE")
}

func TestBlocShowLive_NotCalledWithoutFlag(t *testing.T) {
	isolateBlocEnv(t)
	seedLiveBloc(t)

	calls := useBlocLive(t, &fakeComputeReader{}, nil)

	out, err := runBloc(t, "show", "lab", "--json")
	require.NoError(t, err)
	assert.Zero(t, *calls)
	assert.NotContains(t, out, `"live"`)
}

func TestBlocShowLive_BastionFailureKeepsTheRest(t *testing.T) {
	isolateBlocEnv(t)
	seedLiveBloc(t)

	reader := &fakeComputeReader{
		getErrs:   map[string]error{"100": connectionRefused()},
		listErr:   connectionRefused(),
		instances: map[string]*cpi.Instance{"101": {ID: "101", State: cpi.ResourceState("running")}},
	}
	useBlocLive(t, reader, nil)

	got := liveJSON(t)
	live := got["live"].(map[string]any)

	bastion := live["bastion"].(map[string]any)
	assert.Equal(t, "100", bastion["vm_id"])
	assert.Equal(t, "", bastion["power"])
	assert.Equal(t, "provider connection failed (VM 100)", bastion["error"])

	artifacts := live["artifacts"].(map[string]any)
	assert.Equal(t, "running", artifacts["power"])
	assert.Equal(t, "", artifacts["error"])

	assert.Contains(t, got, "config", "the local part is intact")
	assert.Contains(t, got, "state", "the local part is intact")
}

func TestBlocShowLive_ArtifactsDisabledIsALiveError(t *testing.T) {
	isolateBlocEnv(t)
	seedLiveBloc(t)

	reader := &fakeComputeReader{instances: map[string]*cpi.Instance{"100": {ID: "100", State: cpi.ResourceState("running")}}}

	original := newBlocLiveSource
	newBlocLiveSource = func(_ context.Context, _, _ string) (blocLiveSource, error) {
		return blocLiveSource{Reader: reader, ArtifactsEnabled: false, Close: func() {}}, nil
	}

	t.Cleanup(func() { newBlocLiveSource = original })

	got := liveJSON(t)
	live := got["live"].(map[string]any)

	assert.Equal(t, "running", live["bastion"].(map[string]any)["power"], "the bastion is still read")

	artifacts := live["artifacts"].(map[string]any)
	assert.Contains(t, artifacts["error"], "disabled")
	assert.Equal(t, "", artifacts["power"])
	assert.Equal(t, []string{"100"}, reader.gets, "the artifacts VM is not read")
}

func TestBlocShowLive_ProviderSetupFailure(t *testing.T) {
	isolateBlocEnv(t)
	seedLiveBloc(t)
	useBlocLive(t, nil, classifyProviderError(errors.New("bad endpoint FAKE-TOKEN-DO-NOT-USE"), blocinv.KindProviderSetup, ""))

	got := liveJSON(t)
	live := got["live"].(map[string]any)

	for _, part := range []string{"bastion", "artifacts"} {
		assert.Equal(t, "provider setup failed", live[part].(map[string]any)["error"], part)
	}

	assert.Contains(t, got, "state")
}

func TestBlocShowLive_ArtifactsFoundByTagWhenStateLacksThem(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocsConfig(t, showConfig)
	writeBlocState(t, "lab", showState)

	reader := &fakeComputeReader{
		instances: map[string]*cpi.Instance{
			"100": {ID: "100", State: cpi.ResourceState("running")},
			"555": {ID: "555", State: cpi.ResourceState("running")},
		},
		listed: []*cpi.Instance{{ID: "555", Name: "lab-artifacts", Tags: map[string]string{"ocfp:role": "artifacts", "ocfp:bloc": "lab"}}},
	}
	useBlocLive(t, reader, nil)

	artifacts := liveJSON(t)["live"].(map[string]any)["artifacts"].(map[string]any)
	assert.Equal(t, "555", artifacts["vm_id"])
	assert.Equal(t, "running", artifacts["power"])
	assert.Equal(t, 1, reader.lists)
}

func TestBlocShowLive_NothingToRead(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocsConfig(t, showConfig)

	reader := &fakeComputeReader{}
	useBlocLive(t, reader, nil)

	live := liveJSON(t)["live"].(map[string]any)
	assert.Contains(t, live["bastion"].(map[string]any)["error"], "no bastion")
	assert.Contains(t, live["artifacts"].(map[string]any)["error"], "no ocfp-artifacts VM")
	assert.Empty(t, reader.gets)
}

func TestBlocShowLive_NoConfigEntry(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocState(t, "lab", liveState)

	calls := useBlocLive(t, &fakeComputeReader{}, nil)

	live := liveJSON(t)["live"].(map[string]any)
	assert.Contains(t, live["bastion"].(map[string]any)["error"], "no config defines this bloc")
	assert.Zero(t, *calls, "no provider can be built without a config entry")
}

// malformedYAMLWithToken is invalid YAML whose offending line holds a fake secret.
const malformedYAMLWithToken = "blocs:\n  lab:\n    provider: pve\n    auth_token: [FAKE-TOKEN-DO-NOT-USE\n    region: lab\n"

// liveOutputs renders a live report as JSON and as the text section.
func liveOutputs(t *testing.T, report *blocLiveReport) (string, string) {
	t.Helper()

	data, err := json.Marshal(report)
	require.NoError(t, err)

	var text bytes.Buffer
	require.NoError(t, writeBlocLiveText(&text, report))

	return string(data), text.String()
}

func TestBlocLive_MalformedConfigNeverLeaksIntoTheLiveSection(t *testing.T) {
	isolateBlocEnv(t)

	path := writeBlocsConfig(t, malformedYAMLWithToken)
	entry := blocinv.Entry{Name: "lab", HasConfig: true, ConfigFile: path}

	report := collectBlocLive(context.Background(), entry, blocinv.Summary{})

	asJSON, asText := liveOutputs(t, report)
	for _, rendered := range []string{asJSON, asText} {
		assert.NotContains(t, rendered, "FAKE-TOKEN-DO-NOT-USE")
		assert.Contains(t, rendered, "cannot be parsed")
	}
}

func TestBlocLive_MalformedStateNeverLeaksIntoTheLiveSection(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocsConfig(t, showConfig)

	statePath := writeBlocState(t, "lab", `{"provider": "FAKE-TOKEN-DO-NOT-USE",,, [unterminated`)
	useBlocLive(t, &fakeComputeReader{}, nil)

	entry := blocinv.Entry{Name: "lab", HasConfig: true, HasState: true, StatePath: statePath}

	report := collectBlocLive(context.Background(), entry, blocinv.Summary{})

	asJSON, asText := liveOutputs(t, report)
	for _, rendered := range []string{asJSON, asText} {
		assert.NotContains(t, rendered, "FAKE-TOKEN-DO-NOT-USE")
	}

	assert.Contains(t, report.Artifacts.Error, "cannot be parsed")
}

// liveLeakConfig is a config whose bastion data disk names a filesystem the
// validator rejects. The validator quotes the rejected value in its error.
const liveLeakConfig = `blocs:
  lab:
    provider: pve
    region: lab-region
    auth_token: FAKE-TOKEN-DO-NOT-USE-AUTH
    token_secret: FAKE-TOKEN-DO-NOT-USE-SECRET
    api_endpoint: https://pve.invalid:8006
    bastion:
      data:
        enabled: true
        filesystem: FAKE-TOKEN-DO-NOT-USE-FS
        mountpoint: /data
        disk_size_gib: 10
`

// captureBlocLog points the global logger at a temp directory at debug level
// and returns a func that reads back everything it has written.
func captureBlocLog(t *testing.T) func() string {
	t.Helper()

	dir := t.TempDir()

	require.NoError(t, logger.Initialize(logger.Config{
		Level: "debug", Debug: true, LogDir: dir, BlocName: "logcheck", Command: "test",
	}))
	t.Cleanup(func() { _ = logger.Initialize(logger.Config{NoLog: true}) })

	return func() string {
		_ = logger.Sync()

		var all strings.Builder

		walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}

			all.Write(data)

			return nil
		})
		require.NoError(t, walkErr)

		return all.String()
	}
}

// showLiveEverywhere runs show --live as JSON and as text and returns both
// outputs and the log they wrote.
func showLiveEverywhere(t *testing.T) (string, string, string) {
	t.Helper()

	readLog := captureBlocLog(t)

	asJSON, err := runBloc(t, "show", "lab", "--live", "--json")
	require.NoError(t, err)

	asText, err := runBloc(t, "show", "lab", "--live")
	require.NoError(t, err)

	return asJSON, asText, readLog()
}

func TestBlocShowLive_ConfigValidationErrorNeverLeaks(t *testing.T) {
	isolateBlocEnv(t)

	path := writeBlocsConfig(t, liveLeakConfig)

	_, err := config.LoadWithParams(path, "lab")
	require.Error(t, err)
	require.Contains(t, err.Error(), "FAKE-TOKEN-DO-NOT-USE-FS", "the loader's own error quotes the value")

	asJSON, asText, logged := showLiveEverywhere(t)

	for name, rendered := range map[string]string{"json": asJSON, "text": asText, "log": logged} {
		assert.NotContains(t, rendered, "FAKE-TOKEN-DO-NOT-USE", name)
	}

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(asJSON), &got), asJSON)

	live := got["live"].(map[string]any)
	for _, part := range []string{"bastion", "artifacts"} {
		assert.Equal(t, "config is invalid ("+path+")", live[part].(map[string]any)["error"], part)
	}

	assert.Contains(t, asText, "config is invalid")
}

func TestBlocShowLive_ProviderErrorTextNeverLeaks(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocsConfig(t, showConfig)
	writeBlocState(t, "lab", showState)

	reader := &fakeComputeReader{
		getErrs: map[string]error{"100": errors.New("FAKE-TOKEN-DO-NOT-USE rejected\ncaused by: dial tcp")},
		listErr: errors.New("FAKE-TOKEN-DO-NOT-USE listing failed"),
	}
	useBlocLive(t, reader, nil)

	asJSON, asText, logged := showLiveEverywhere(t)

	for name, rendered := range map[string]string{"json": asJSON, "text": asText, "log": logged} {
		assert.NotContains(t, rendered, "FAKE-TOKEN-DO-NOT-USE", name)
		assert.NotContains(t, rendered, "caused by", name)
	}

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(asJSON), &got), asJSON)

	live := got["live"].(map[string]any)
	assert.Equal(t, "provider request failed (VM 100)", live["bastion"].(map[string]any)["error"])
	assert.Equal(t, "provider request failed", live["artifacts"].(map[string]any)["error"])
}

func TestClassifyProviderError_KeepsOnlySafeFields(t *testing.T) {
	body := []byte(`{"message": "FAKE-TOKEN-DO-NOT-USE lacks VM.Audit"}`)

	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"forbidden":    {pveapierrors.ParseAPIError(403, body), "permission denied (VM 100, HTTP 403)"},
		"unauthorized": {pveapierrors.ParseAPIError(401, body), "provider authentication failed (VM 100, HTTP 401)"},
		"server error": {pveapierrors.ParseAPIError(500, body), "provider request failed (VM 100, HTTP 500)"},
		"not found":    {pveapierrors.ParseAPIError(404, body), "VM not found (VM 100, HTTP 404)"},
		"connection":   {connectionRefused(), "provider connection failed (VM 100)"},
		"timeout":      {fmt.Errorf("FAKE-TOKEN-DO-NOT-USE: %w", context.DeadlineExceeded), "timed out (VM 100)"},
		"unknown":      {errors.New("FAKE-TOKEN-DO-NOT-USE"), "provider request failed (VM 100)"},
	} {
		got := blocinv.SafeMessage(classifyProviderError(tc.err, blocinv.KindProviderRequest, "100"), "")
		assert.Equal(t, tc.want, got, name)
	}
}

// connectionRefused is the error the PVE client returns when nothing listens
// on the API port. Its text names the host and repeats the cause.
func connectionRefused() error {
	return &pveapierrors.ConnectionError{
		Host: "FAKE-TOKEN-DO-NOT-USE.invalid", Port: 8006, Message: "dial failed",
		Cause: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
	}
}

func TestBlocList_SymlinkedConfigIsReportedUnderItsOwnName(t *testing.T) {
	isolateBlocEnv(t)

	require.NoError(t, os.MkdirAll(config.ConfigHome(), 0o750))

	target := filepath.Join(config.ConfigHome(), "config.pve.yml")
	require.NoError(t, os.WriteFile(target, []byte("blocs:\n  lab:\n    provider: pve\n    region: lab\n"), 0o600))

	link := filepath.Join(config.ConfigHome(), "config.yml")
	require.NoError(t, os.Symlink(target, link))

	out, err := runBloc(t, "list", "--json")
	require.NoError(t, err)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows), out)
	require.Len(t, rows, 1)
	assert.Equal(t, link, rows[0]["config_file"])
	assert.Equal(t, []any{link}, rows[0]["config_files"])
}

func TestBlocList_PrimaryConfigWinsOverAnAlternateThatSortsFirst(t *testing.T) {
	isolateBlocEnv(t)

	require.NoError(t, os.MkdirAll(config.ConfigHome(), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(config.ConfigHome(), "config.all.yml"),
		[]byte("blocs:\n  lab:\n    provider: aws\n    region: elsewhere\n  extra:\n    provider: aws\n"), 0o600))

	primary := writeBlocsConfig(t, "blocs:\n  lab:\n    provider: pve\n    region: lab\n")

	out, err := runBloc(t, "list", "--json")
	require.NoError(t, err)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows), out)

	files := map[string]any{}
	for _, row := range rows {
		files[row["name"].(string)] = row["config_file"]
	}

	assert.Equal(t, primary, files["lab"])
	assert.Equal(t, filepath.Join(config.ConfigHome(), "config.all.yml"), files["extra"], "the alternate file's own blocs stay listed")
}

func TestBlocShowLive_LoadsTheConfigFileThatDefinesTheBloc(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocsConfig(t, "blocs:\n  other:\n    provider: pve\n")

	alternate := filepath.Join(config.ConfigHome(), "config.all.yml")
	require.NoError(t, os.WriteFile(alternate, []byte("blocs:\n  extra:\n    provider: pve\n    region: lab\n"), 0o600))

	var gotName, gotFile string

	original := newBlocLiveSource
	newBlocLiveSource = func(_ context.Context, name, configFile string) (blocLiveSource, error) {
		gotName, gotFile = name, configFile

		return blocLiveSource{Reader: &fakeComputeReader{}, ArtifactsEnabled: true, Close: func() {}}, nil
	}

	t.Cleanup(func() { newBlocLiveSource = original })

	_, err := runBloc(t, "show", "extra", "--live", "--json")
	require.NoError(t, err)
	assert.Equal(t, "extra", gotName)
	assert.Equal(t, alternate, gotFile, "the bloc's own config file is loaded, not only the primary one")
}

func TestOpenBlocLiveSource_ReadsTheNamedConfigFile(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocsConfig(t, "blocs:\n  other:\n    provider: pve\n")

	alternate := filepath.Join(config.ConfigHome(), "config.all.yml")
	require.NoError(t, os.WriteFile(alternate, []byte("blocs:\n  extra:\n    provider: nonesuch\n"), 0o600))

	_, err := openBlocLiveSource(context.Background(), "extra", alternate)
	require.Error(t, err)

	var classified *blocinv.ClassifiedError
	require.ErrorAs(t, err, &classified)
	assert.Equal(t, blocinv.KindConfigInvalid, classified.Kind, "the bloc is found in its own file, and its provider setting is what failed")
	assert.Equal(t, alternate, classified.Path)
}

func TestOpenBlocLiveSource_ABlocMissingFromItsFileIsClassified(t *testing.T) {
	isolateBlocEnv(t)

	path := writeBlocsConfig(t, "blocs:\n  other:\n    provider: pve\n")

	_, err := openBlocLiveSource(context.Background(), "lab", path)
	require.ErrorIs(t, err, config.ErrBlocNotDefined)
	assert.Equal(t, "bloc not found in config ("+path+")", err.Error())
}

func TestBlocShowLive_NullBlocDefinitionIsAConfigError(t *testing.T) {
	isolateBlocEnv(t)

	path := writeBlocsConfig(t, "blocs:\n  bare: null\n  other:\n    provider: pve\n")

	text, err := runBloc(t, "show", "bare", "--live")
	require.NoError(t, err)
	assert.Contains(t, text, "Bloc\n")
	assert.Contains(t, text, "Config\n")
	assert.Contains(t, text, "State\n")
	assert.Contains(t, text, "Bastion error:       config is invalid ("+path+")")

	out, err := runBloc(t, "show", "bare", "--live", "--json")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	assert.Equal(t, "bare", got["name"])
	assert.Contains(t, got, "config", "the local part is intact")

	live := got["live"].(map[string]any)
	for _, part := range []string{"bastion", "artifacts"} {
		assert.Equal(t, "config is invalid ("+path+")", live[part].(map[string]any)["error"], part)
	}
}

func TestOpenBlocLiveSource_NoConfigFileIsAClearError(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocsConfig(t, showConfig)

	_, err := openBlocLiveSource(context.Background(), "lab", "")
	require.ErrorIs(t, err, ErrBlocLiveNoConfig, "the primary config is not consulted for a bloc it does not define")
}

func TestBlocShowLive_ReadsTheSameStateFileTheInventoryShowed(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocsConfig(t, showConfig)

	// The XDG state directory exists but holds no state file, and the legacy
	// home holds the bloc's state.
	require.NoError(t, os.MkdirAll(filepath.Join(config.StateHome(), "lab", "state"), 0o750))

	legacyDir := filepath.Join(config.OcfpHome(), "lab", "state")
	require.NoError(t, os.MkdirAll(legacyDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, "lab.json"), []byte(liveState), 0o600))

	reader := &fakeComputeReader{instances: map[string]*cpi.Instance{
		"100": {ID: "100", State: cpi.ResourceState("running")},
		"101": {ID: "101", State: cpi.ResourceState("stopped")},
	}}
	useBlocLive(t, reader, nil)

	artifacts := liveJSON(t)["live"].(map[string]any)["artifacts"].(map[string]any)
	assert.Equal(t, "101", artifacts["vm_id"], "the legacy state names the artifacts VM")
	assert.Equal(t, "stopped", artifacts["power"])
	assert.Zero(t, reader.lists, "no provider query is needed when the state names the VM")
}

func TestBlocAndEnvList_SurviveAStateHomeThatCannotBeListed(t *testing.T) {
	isolateBlocEnv(t)
	writeBlocsConfig(t, labConfig)
	writeBlocState(t, "lab-state", `{"provider":"aws","region":"us-east-1"}`)

	// The legacy home is a regular file, so listing it fails with ENOTDIR.
	require.NoError(t, os.WriteFile(config.OcfpHome(), []byte("not a directory\n"), 0o600))

	out, err := runBloc(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "lab-state")

	var envOut bytes.Buffer

	cmd := newEnvListCmd()
	cmd.SetOut(&envOut)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(nil)

	require.NoError(t, cmd.Execute())
	assert.Contains(t, envOut.String(), "lab-state")
}

// bastionTags are the tags the bootstrap puts on a bloc's VMs.
func bastionTags(bloc string) map[string]string {
	return map[string]string{"bloc": bloc, "managed-by": "ocfp"}
}

func TestBlocShowLive_StaleBastionIDFallsBackToTheBastionByName(t *testing.T) {
	isolateBlocEnv(t)
	seedLiveBloc(t)

	reader := &fakeComputeReader{
		getErrs: map[string]error{"100": vmNotFound("100")},
		instances: map[string]*cpi.Instance{
			"101": {ID: "101", State: cpi.ResourceState("running")},
		},
		listed: []*cpi.Instance{
			// Look-alikes that must never be picked.
			{ID: "200", Name: "other-bastion", State: cpi.ResourceState("running"), Tags: bastionTags("other")},
			{ID: "201", Name: "other-artifacts", State: cpi.ResourceState("running"), Tags: bastionTags("other")},
			{ID: "202", Name: "lab-artifacts", State: cpi.ResourceState("running"), Tags: bastionTags("lab")},
			{ID: "203", Name: "lab-bastion-old", State: cpi.ResourceState("running"), Tags: bastionTags("lab")},
			{ID: "204", Name: "xlab-bastion", State: cpi.ResourceState("running"), Tags: bastionTags("lab")},
			{ID: "205", Name: "lab-bastion", State: cpi.ResourceState("running"), Tags: bastionTags("other")},
			// The real one.
			{ID: "102", Name: "lab-bastion", State: cpi.ResourceState("stopped"), Tags: bastionTags("lab")},
		},
	}
	useBlocLive(t, reader, nil)

	bastion := liveJSON(t)["live"].(map[string]any)["bastion"].(map[string]any)
	assert.Equal(t, "102", bastion["vm_id"], "the live VM id, not the stale one in state")
	assert.Equal(t, "stopped", bastion["power"])
	assert.Equal(t, "", bastion["error"])
	assert.Contains(t, bastion["note"], "state records VM 100")
	assert.Contains(t, bastion["note"], "stale")

	text, err := runBloc(t, "show", "lab", "--live")
	require.NoError(t, err)
	assert.Contains(t, text, "Bastion note")
}

func TestBlocShowLive_StaleBastionIDWithNoMatchIsAShortError(t *testing.T) {
	isolateBlocEnv(t)
	seedLiveBloc(t)

	reader := &fakeComputeReader{
		getErrs:   map[string]error{"100": vmNotFound("100")},
		instances: map[string]*cpi.Instance{"101": {ID: "101", State: cpi.ResourceState("running")}},
		listed: []*cpi.Instance{
			{ID: "200", Name: "other-bastion", Tags: bastionTags("other")},
			{ID: "204", Name: "xlab-bastion", Tags: bastionTags("lab")},
			{ID: "205", Name: "lab-bastion", Tags: bastionTags("other")},
		},
	}
	useBlocLive(t, reader, nil)

	live := liveJSON(t)["live"].(map[string]any)

	bastion := live["bastion"].(map[string]any)
	assert.Equal(t, "VM 100 not found", bastion["error"])
	assert.Equal(t, "", bastion["power"])
	assert.NotContains(t, bastion, "note")

	assert.Equal(t, "running", live["artifacts"].(map[string]any)["power"], "the artifacts read is unaffected")
}

func TestBlocShowLive_TwoBastionsWithTheSameNameAreNotGuessedBetween(t *testing.T) {
	isolateBlocEnv(t)
	seedLiveBloc(t)

	reader := &fakeComputeReader{
		getErrs:   map[string]error{"100": vmNotFound("100")},
		instances: map[string]*cpi.Instance{"101": {ID: "101", State: cpi.ResourceState("running")}},
		listed: []*cpi.Instance{
			{ID: "102", Name: "lab-bastion", Tags: bastionTags("lab")},
			{ID: "103", Name: "lab-bastion", Tags: bastionTags("lab")},
		},
	}
	useBlocLive(t, reader, nil)

	bastion := liveJSON(t)["live"].(map[string]any)["bastion"].(map[string]any)
	assert.Equal(t, "100", bastion["vm_id"])
	assert.Contains(t, bastion["error"], "VM 100 not found")
	assert.Contains(t, bastion["error"], "more than one")
}

func TestBlocShowLive_OnlyAVMNotFoundErrorStartsTheSearchByName(t *testing.T) {
	forbidden := pveapierrors.ParseAPIError(403, []byte(`{"message": "FAKE-TOKEN-DO-NOT-USE lacks VM.Audit"}`))

	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"permission denied": {forbidden, "permission denied (VM 100, HTTP 403)"},
		"timeout":           {fmt.Errorf("status: %w", context.DeadlineExceeded), "timed out (VM 100)"},
		"connection":        {connectionRefused(), "provider connection failed (VM 100)"},
		"unclassified":      {errors.New("VM 100 does not exist"), "provider request failed (VM 100)"},
	} {
		t.Run(name, func(t *testing.T) {
			isolateBlocEnv(t)
			seedLiveBloc(t)

			reader := &fakeComputeReader{
				getErrs:   map[string]error{"100": tc.err},
				instances: map[string]*cpi.Instance{"101": {ID: "101", State: cpi.ResourceState("running")}},
				// The listing would find the bastion under its recorded id.
				listed: []*cpi.Instance{{ID: "100", Name: "lab-bastion", State: cpi.ResourceState("running"), Tags: bastionTags("lab")}},
			}
			useBlocLive(t, reader, nil)

			bastion := liveJSON(t)["live"].(map[string]any)["bastion"].(map[string]any)
			assert.Equal(t, "100", bastion["vm_id"])
			assert.Equal(t, "", bastion["power"])
			assert.Equal(t, tc.want, bastion["error"])
			assert.NotContains(t, bastion, "note", "a failed read is not evidence that state is stale")
			assert.Zero(t, reader.lists, "no search by name without a not-found answer")
		})
	}
}

func TestBlocShowLive_SameIDFromTheSearchIsNotCalledStale(t *testing.T) {
	isolateBlocEnv(t)
	seedLiveBloc(t)

	reader := &fakeComputeReader{
		getErrs:   map[string]error{"100": vmNotFound("100")},
		instances: map[string]*cpi.Instance{"101": {ID: "101", State: cpi.ResourceState("running")}},
		listed:    []*cpi.Instance{{ID: "100", Name: "lab-bastion", State: cpi.ResourceState("running"), Tags: bastionTags("lab")}},
	}
	useBlocLive(t, reader, nil)

	bastion := liveJSON(t)["live"].(map[string]any)["bastion"].(map[string]any)
	assert.Equal(t, "100", bastion["vm_id"])
	assert.Equal(t, "running", bastion["power"])
	assert.Equal(t, "", bastion["error"])
	assert.NotContains(t, bastion["note"], "stale")
	assert.NotContains(t, bastion["note"], "does not exist")
	assert.Contains(t, bastion["note"], "VM list")
}

func TestBlocShowLive_FailedSearchAfterNotFoundSaysBoth(t *testing.T) {
	isolateBlocEnv(t)
	seedLiveBloc(t)

	reader := &fakeComputeReader{
		getErrs:   map[string]error{"100": vmNotFound("100")},
		listErr:   connectionRefused(),
		instances: map[string]*cpi.Instance{"101": {ID: "101", State: cpi.ResourceState("running")}},
	}
	useBlocLive(t, reader, nil)

	bastion := liveJSON(t)["live"].(map[string]any)["bastion"].(map[string]any)
	assert.Equal(t, "VM 100 not found, and the search by name failed: provider connection failed", bastion["error"])
	assert.NotContains(t, bastion, "note")
}

// hangingReader blocks every call until its context ends.
type hangingReader struct{}

func (hangingReader) GetInstance(ctx context.Context, _ string) (*cpi.Instance, error) {
	<-ctx.Done()

	return nil, ctx.Err()
}

func (hangingReader) ListInstances(ctx context.Context, _ map[string]string) ([]*cpi.Instance, error) {
	<-ctx.Done()

	return nil, ctx.Err()
}

func TestBlocShowLive_TimeoutBecomesALiveError(t *testing.T) {
	isolateBlocEnv(t)
	seedLiveBloc(t)
	useBlocLive(t, hangingReader{}, nil)

	original := blocLiveTimeout
	blocLiveTimeout = 50 * time.Millisecond

	t.Cleanup(func() { blocLiveTimeout = original })

	got := liveJSON(t)
	live := got["live"].(map[string]any)

	for _, part := range []string{"bastion", "artifacts"} {
		assert.Contains(t, live[part].(map[string]any)["error"], "timed out", part)
	}

	assert.Contains(t, got, "state", "the local part of the output is intact")
}
