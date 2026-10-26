package blocinv

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secretBlocConfig = `blocs:
  lab:
    name: lab
    provider: pve
    iaas: proxmox
    region: lab-region
    project_id: proj-1
    org_id: org-1
    api_endpoint: https://FAKE-UNLISTED-VALUE.example
    auth_token: FAKE-TOKEN-DO-NOT-USE-AUTH
    token_secret: FAKE-TOKEN-DO-NOT-USE-SECRET
    password: FAKE-TOKEN-DO-NOT-USE-PASSWORD
    service_account_json: FAKE-TOKEN-DO-NOT-USE-SA
    access_key_id: FAKE-TOKEN-DO-NOT-USE-AKID
    secret_access_key: FAKE-TOKEN-DO-NOT-USE-SAK
    network:
      cidr: 10.20.0.0/20
    artifacts:
      enabled: true
`

const richState = `{
  "version": "1.0",
  "provider": "pve",
  "region": "lab-region",
  "createdAt": "2026-10-01T00:00:00Z",
  "updatedAt": "2026-10-25T18:02:11Z",
  "resources": {
    "network.lab-net": {"id": "vnet1", "type": "network", "name": "lab-net", "properties": {"cidr": "10.20.0.0/20"}},
    "compute_instance.lab-bastion": {"id": "100", "type": "compute_instance", "name": "lab-bastion"},
    "compute_instance.lab-artifacts": {"id": "101", "type": "compute_instance", "name": "lab-artifacts"},
    "block_volume.lab-data": {"id": "vol1", "type": "block_volume", "name": "lab-data"}
  },
  "outputs": {
    "bastion_id": "100",
    "bastion_private_ip": "10.20.0.5",
    "bastion_public_ip": "203.0.113.9",
    "bastion_ssh_host": "203.0.113.9",
    "keypair_public_key": "ssh-ed25519 FAKE-TOKEN-DO-NOT-USE-KEY",
    "reserved_bosh_ip": "10.20.0.4",
    "reserved_vault_ip": "10.20.0.6",
    "reserved_concourse_ip": "10.20.0.7",
    "reserved_credhub_ip": "10.20.0.8",
    "reserved_uaa_ip": "10.20.0.9",
    "reserved_zeta_ip": "10.20.0.10",
    "reserved_api_token": "FAKE-TOKEN-DO-NOT-USE-RESERVED"
  }
}`

func collectOne(t *testing.T, cfgBody, stateBody string) Entry {
	t.Helper()

	opts := Options{}

	if cfgBody != "" {
		opts.ConfigDirs = []string{t.TempDir()}
		writeFile(t, opts.ConfigDirs[0], "config.yml", cfgBody)
	}

	if stateBody != "" {
		opts.StateHomes = []string{t.TempDir()}
		writeState(t, opts.StateHomes[0], "lab", stateBody)
	}

	entries, err := Collect(opts)
	require.NoError(t, err)

	return byName(t, entries, "lab")
}

func TestSummarize_ConfigAllowlist(t *testing.T) {
	entry := collectOne(t, secretBlocConfig, "")
	require.Equal(t, "FAKE-TOKEN-DO-NOT-USE-AUTH", entry.cfg.AuthToken, "the fixture must reach the parsed config")

	summary, err := Summarize(entry)
	require.NoError(t, err)
	require.NotNil(t, summary.Config)
	assert.Nil(t, summary.State)

	assert.Equal(t, ConfigSummary{
		File:             entry.ConfigFile,
		Provider:         "pve",
		IaaS:             "proxmox",
		Region:           "lab-region",
		ProjectID:        "proj-1",
		OrgID:            "org-1",
		NetworkCIDR:      "10.20.0.0/20",
		ArtifactsEnabled: true,
	}, *summary.Config)
}

func TestSummarize_NeverLeaksSecretOrUnlistedFields(t *testing.T) {
	entry := collectOne(t, secretBlocConfig, richState)

	summary, err := Summarize(entry)
	require.NoError(t, err)

	encoded, err := json.Marshal(summary)
	require.NoError(t, err)

	rendered := string(encoded) + fmt.Sprintf("%+v %#v", summary, summary)

	for _, forbidden := range []string{
		"FAKE-TOKEN-DO-NOT-USE",
		"FAKE-UNLISTED-VALUE",
		"auth_token",
		"token_secret",
		"keypair_public_key",
		"reserved_api_token",
	} {
		assert.NotContains(t, rendered, forbidden)
	}
}

func TestSummarize_State(t *testing.T) {
	entry := collectOne(t, secretBlocConfig, richState)

	summary, err := Summarize(entry)
	require.NoError(t, err)
	require.NotNil(t, summary.State)

	st := summary.State
	assert.Equal(t, "pve", st.Provider)
	assert.Equal(t, "lab-region", st.Region)
	assert.True(t, st.CreatedAt.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)))
	assert.True(t, st.UpdatedAt.Equal(time.Date(2026, 10, 25, 18, 2, 11, 0, time.UTC)))

	assert.Equal(t, BastionSummary{ID: "100", PrivateIP: "10.20.0.5", PublicIP: "203.0.113.9", SSHHost: "203.0.113.9"}, st.Bastion)
	assert.Equal(t, NetworkSummary{Name: "lab-net", CIDR: "10.20.0.0/20"}, st.Network)
	assert.Equal(t, map[string]int{"network": 1, "compute_instance": 2, "block_volume": 1}, st.Resources)

	assert.Equal(t, 12, st.OutputCount)
	assert.Equal(t, 7, st.ReservedOutputCount, "the count includes every reserved_ output")
	assert.Equal(t,
		[]string{"reserved_bosh_ip", "reserved_concourse_ip", "reserved_credhub_ip", "reserved_uaa_ip", "reserved_vault_ip"},
		st.ReservedOutputSample,
		"the sample is the first few sorted names and leaves out sensitive-looking ones")
}

func TestSummarize_BastionIDFallsBackToInstanceRecord(t *testing.T) {
	entry := collectOne(t, "", `{
  "resources": {"compute_instance.lab-bastion": {"id": "777", "type": "compute_instance", "name": "lab-bastion"}},
  "outputs": {}
}`)

	summary, err := Summarize(entry)
	require.NoError(t, err)
	require.NotNil(t, summary.State)
	assert.Equal(t, "777", summary.State.Bastion.ID)
}

func TestSummarize_NoConfigNoState(t *testing.T) {
	summary, err := Summarize(Entry{Name: "ghost"})
	require.NoError(t, err)
	assert.Nil(t, summary.Config)
	assert.Nil(t, summary.State)
}

func TestSummarize_BrokenStateHasNoStateSummary(t *testing.T) {
	entry := collectOne(t, secretBlocConfig, "{{{ not state")
	require.True(t, entry.Broken)

	summary, err := Summarize(entry)
	require.NoError(t, err)
	assert.NotNil(t, summary.Config)
	assert.Nil(t, summary.State)
}

func TestSummarize_StateFileGoneIsAnError(t *testing.T) {
	entry := collectOne(t, "", richState)
	require.NoError(t, os.Remove(entry.StatePath))

	_, err := Summarize(entry)
	require.Error(t, err)
}
