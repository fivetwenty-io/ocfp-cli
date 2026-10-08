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
