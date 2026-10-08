package bastion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNarrowMode_SelectsTheRequestedMode covers the choice between one narrow
// mode and the full phase list, on the bastion and from a workstation. A
// narrow flag that is not consulted widens the work it was asked to narrow:
// `ocfp init bastion --genesis` run on a bastion used to accept the flag and
// then run every local phase, and --ocfp and --config still did. Options are
// nil for callers taking every default, so that case must not panic.
func TestNarrowMode_SelectsTheRequestedMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      *ProvisioningOptions
		onBastion bool
		want      string
	}{
		{"nil options take the full path on the bastion", nil, true, ""},
		{"nil options take the full path remotely", nil, false, ""},
		{"no flag takes the full path on the bastion", &ProvisioningOptions{}, true, ""},
		{"no flag takes the full path remotely", &ProvisioningOptions{}, false, ""},
		{"--genesis on the bastion", &ProvisioningOptions{GenesisOnly: true}, true, narrowModeGenesis},
		{"--genesis remotely", &ProvisioningOptions{GenesisOnly: true}, false, narrowModeGenesis},
		{"--ocfp on the bastion", &ProvisioningOptions{OCFPOnly: true}, true, narrowModeOCFP},
		{"--ocfp remotely", &ProvisioningOptions{OCFPOnly: true}, false, narrowModeOCFP},
		{"--config on the bastion", &ProvisioningOptions{ConfigOnly: true}, true, narrowModeConfig},
		{"--config remotely", &ProvisioningOptions{ConfigOnly: true}, false, narrowModeConfig},
		{"--vault-boot-unit on the bastion", &ProvisioningOptions{VaultBootUnitOnly: true}, true, narrowModeVaultBootUnit},
		{"--vault-boot-unit remotely", &ProvisioningOptions{VaultBootUnitOnly: true}, false, narrowModeVaultBootUnit},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := newMinimalManager(newBaseConfig("bloc1", "aws"))
			m.options = tc.opts

			name, run := m.narrowMode(tc.onBastion)
			assert.Equal(t, tc.want, name)
			assert.Equal(t, tc.want != "", run != nil, "a named mode must come with something to run, and only then")
		})
	}
}

// localModeManager builds the manager a bastion-side run hands its narrow
// modes, with a mock in place of the local command executor.
func localModeManager(opts *ProvisioningOptions, mock SSHClient) (*LocalExecutor, *Manager) {
	cfg := newBaseConfig("bloc1", "aws")
	m := newMinimalManager(cfg)
	m.sshClient = mock
	m.options = opts
	m.progress = &ProvisioningProgress{Checkpoints: map[string]bool{}}

	return &LocalExecutor{config: cfg, options: opts, log: newTestLogger()}, m
}

// On the bastion, --ocfp used to fall through to the full local phase list,
// so a run asked to refresh one binary re-ran vault inception, configure,
// and everything else. It must issue exactly the commands the ocfp CLI setup
// issues on its own.
func TestLocalExecutor_OCFPOnlyRunsOnlyTheCLISetup(t *testing.T) {
	t.Setenv("OCFP_CLI_SOURCE", "")
	t.Setenv("OCFP_BINARY_PATH", "")
	t.Setenv("OCFP_CLI_VERSION", "0.1.0")

	opts := &ProvisioningOptions{OCFPOnly: true}

	alone := &ocfpCLIMockSSHClient{}
	_, reference := localModeManager(opts, alone)
	require.NoError(t, reference.setupOCFPCLI(context.Background()))

	mock := &ocfpCLIMockSSHClient{}
	le, m := localModeManager(opts, mock)
	require.NoError(t, le.run(context.Background(), m))

	// The scripts carry generated names, so the runs are compared by their
	// shape: the same number of commands, each doing the same job.
	require.Len(t, mock.commands, len(alone.commands),
		"--ocfp on the bastion must run the ocfp CLI setup and nothing else")
	assert.Contains(t, mock.commands[0], "releases/download/", "the release install comes first")
	assert.Contains(t, mock.commands[1], "completion bash", "then the completions")
	assert.Empty(t, mock.transfers)
}

// On the bastion there is no workstation config to copy, so --config must
// fail with a message that says so, and must not run any phase instead.
func TestLocalExecutor_ConfigOnlyRefusesOnTheBastion(t *testing.T) {
	t.Parallel()

	mock := &ocfpCLIMockSSHClient{}
	le, m := localModeManager(&ProvisioningOptions{ConfigOnly: true}, mock)

	err := le.run(context.Background(), m)
	require.ErrorIs(t, err, ErrConfigModeOnBastion)
	assert.Contains(t, err.Error(), "workstation")
	assert.Empty(t, mock.commands, "a refused --config must not run anything on the bastion")
	assert.Empty(t, mock.transfers)
}

// --vault-boot-unit on the bastion installs and enables the boot unit and
// does nothing else: one support check, one probe for the operator, and one
// install.
func TestLocalExecutor_VaultBootUnitOnlyRunsOnlyThatPhase(t *testing.T) {
	t.Parallel()

	mock := &bootUnitMockSSHClient{whoami: "ubuntu\n/home/ubuntu\n"}
	le, m := localModeManager(&ProvisioningOptions{VaultBootUnitOnly: true}, mock)
	m.config.Name = "ocfp-lab-drgao"

	require.NoError(t, le.run(context.Background(), m))
	require.Len(t, mock.commands, 3, "a support check, an operator probe, and the install, and nothing else")
	assert.Contains(t, mock.commands[0], "vault start --check-support")
	assert.Contains(t, mock.commands[2], "ocfp-vault@ocfp-lab-drgao.service")
}

// A bastion that a full init has already provisioned is exactly the one
// that needs the boot unit added later, so --vault-boot-unit from a
// workstation must not stop at the provisioned marker. The mock answers the
// marker check as present.
func TestRunModeOrPhases_VaultBootUnitRunsOnAProvisionedBastion(t *testing.T) {
	t.Parallel()

	mock := &bootUnitMockSSHClient{whoami: "ubuntu\n/home/ubuntu\n"}
	m := bootUnitManager(mock, false)
	m.options.VaultBootUnitOnly = true

	require.NoError(t, m.runModeOrPhases(context.Background()))
	require.Len(t, mock.commands, 3, "a support check, an operator probe, and the install, and nothing else")
	assert.Contains(t, mock.commands[2], "ocfp-vault@ocfp-lab-drgao.service")

	for _, cmd := range mock.commands {
		assert.NotContains(t, cmd, ProvisionedMarkerPath, "a narrow mode must not consult the provisioned marker")
	}
}

// A full init from a workstation still stops at the provisioned marker, so
// the narrow mode above is what reaches a provisioned bastion.
func TestRunModeOrPhases_FullInitStopsAtTheProvisionedMarker(t *testing.T) {
	t.Parallel()

	mock := &bootUnitMockSSHClient{whoami: "ubuntu\n/home/ubuntu\n"}
	m := bootUnitManager(mock, false)

	require.NoError(t, m.runModeOrPhases(context.Background()))
	require.Len(t, mock.commands, 1, "only the marker check runs on a provisioned bastion")
	assert.Contains(t, mock.commands[0], ProvisionedMarkerPath)
}

// A dry run of --vault-boot-unit reports the unit and touches nothing.
func TestLocalExecutor_VaultBootUnitOnlyDryRunChangesNothing(t *testing.T) {
	t.Parallel()

	mock := &bootUnitMockSSHClient{whoami: "ubuntu\n/home/ubuntu\n"}
	le, m := localModeManager(&ProvisioningOptions{VaultBootUnitOnly: true, DryRun: true}, mock)

	require.NoError(t, le.run(context.Background(), m))
	assert.Empty(t, mock.commands)
}
