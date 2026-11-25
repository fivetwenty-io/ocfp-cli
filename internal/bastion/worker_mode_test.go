package bastion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
)

// freshWorkerSteps are the steps a worker install takes on a bastion that
// has no worker yet.
var freshWorkerSteps = []string{
	"probe",
	"directory",
	"transfer .join.json.upload",
	"join file",
	"transfer .bloc-worker.upload",
	"binary",
	"loopback",
	"prepare",
	"record",
	"unit",
	"restart",
}

func TestNarrowMode_SelectsTheWorkerMode(t *testing.T) {
	t.Parallel()

	for _, onBastion := range []bool{true, false} {
		m := newMinimalManager(newBaseConfig("bloc1", "aws"))
		m.options = &ProvisioningOptions{WorkerOnly: true}

		name, run := m.narrowMode(onBastion)
		assert.Equal(t, narrowModeWorker, name)
		assert.NotNil(t, run)
	}
}

// A bastion that a full init has already provisioned skips every phase,
// worker_service among them, so --worker from a workstation must run the
// worker install without stopping at the provisioned marker, and must not
// touch the marker either.
func TestRunModeOrPhases_WorkerRunsOnAProvisionedBastion(t *testing.T) {
	t.Parallel()

	bundle, _ := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), false)
	h.m.options.WorkerOnly = true
	h.m.config.Bastion.Worker.Bundle = bundle

	require.NoError(t, h.m.runModeOrPhases(context.Background()))
	assert.Equal(t, freshWorkerSteps, h.mock.steps, "the worker install and nothing else")

	for _, cmd := range h.mock.commands {
		assert.NotContains(t, cmd, ProvisionedMarkerPath, "a narrow mode must not consult or change the provisioned marker")
	}

	assert.Positive(t, h.logs.FilterMessageSnippet("Bloc worker installed and started").Len())
	assertNoSeed(t, h, nil)
}

// On the bastion itself, --worker runs the same worker install and nothing
// else.
func TestLocalExecutor_WorkerOnlyRunsOnlyThatPhase(t *testing.T) {
	t.Parallel()

	bundle, _ := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), false)
	h.m.options.WorkerOnly = true
	h.m.config.Bastion.Worker.Bundle = bundle
	h.m.progress = &ProvisioningProgress{Checkpoints: map[string]bool{}}

	le := &LocalExecutor{config: h.m.config, options: h.m.options, log: newTestLogger()}

	require.NoError(t, le.run(context.Background(), h.m))
	assert.Equal(t, freshWorkerSteps, h.mock.steps)
	assertNoSeed(t, h, nil)
}

// The full phase list skips the worker when no bundle is named, but
// --worker asks for the worker and nothing else, so it refuses and names
// the setting instead of finishing without doing anything.
func TestRunModeOrPhases_WorkerWithNoBundleRefuses(t *testing.T) {
	t.Parallel()

	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), false)
	h.m.options.WorkerOnly = true

	err := h.m.runModeOrPhases(context.Background())
	require.ErrorIs(t, err, ErrWorkerModeNoBundle)
	assert.Contains(t, err.Error(), "bastion.worker.bundle")
	assert.Contains(t, err.Error(), config.EnvWorkerBundle)
	assert.Empty(t, h.mock.steps, "a refused --worker reaches nothing on the bastion")
}

// A dry run of --worker checks the bundle on this machine and reaches
// nothing on the bastion, and it still refuses a bundle the real run would.
func TestRunModeOrPhases_WorkerDryRunChangesNothing(t *testing.T) {
	t.Parallel()

	bundle, _ := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), true)
	h.m.options.WorkerOnly = true
	h.m.config.Bastion.Worker.Bundle = bundle

	require.NoError(t, h.m.runModeOrPhases(context.Background()))
	assert.Empty(t, h.mock.steps, "a dry run reaches nothing on the bastion")
	assert.Positive(t, h.logs.FilterMessageSnippet("DRY RUN: Would install and start the bloc worker").Len())
	assert.Positive(t, h.logs.FilterMessageSnippet(dryRunFinishedMessage).Len())
	assert.Zero(t, h.logs.FilterMessageSnippet("installed and started").Len(), "a dry run never claims the work is done")

	join := workerJoinDoc()
	join["bloc"] = "ocfp-other-bloc"
	other, _ := writeWorkerBundle(t, join, workerBinary)
	h.m.config.Bastion.Worker.Bundle = other

	require.ErrorIs(t, h.m.runModeOrPhases(context.Background()), errWorkerBundleBloc)
}
