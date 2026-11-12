package bastion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errTestPhaseMisconfigured fails a phase without a retry, because the
// error handler treats an invalid setting as one that a retry won't fix.
var errTestPhaseMisconfigured = errors.New("invalid phase setting")

// parallelTestPhases returns count phases that succeed at once, except
// the one named failing, which fails with errTestPhaseMisconfigured.
func parallelTestPhases(count int, failing string) []struct {
	name string
	fn   func(context.Context) error
} {
	phases := make([]struct {
		name string
		fn   func(context.Context) error
	}, 0, count)

	for index := range count {
		name := fmt.Sprintf("phase_%02d", index)
		fn := func(context.Context) error { return nil }

		if name == failing {
			fn = func(context.Context) error { return errTestPhaseMisconfigured }
		}

		phases = append(phases, struct {
			name string
			fn   func(context.Context) error
		}{name, fn})
	}

	return phases
}

// The parallel runner's workers share the run's progress, the checkpoint
// file, and the progress reporter. Under -race this test fails if any of
// them is read or written without a lock, and the checkpoint the workers
// leave behind must list every phase that finished, which interleaved
// writes to the file would lose.
func TestParallelRunnerSharesProgressSafely(t *testing.T) {
	cfg := dryRunBloc(t)
	t.Setenv("OCFP_HOME", t.TempDir())

	var out bytes.Buffer

	const (
		phaseCount = 24
		failing    = "phase_17"
		resumed    = "phase_03"
	)

	m, _, _ := dryRunManager(t, cfg,
		&ProvisioningOptions{Parallel: true, MaxWorkers: 8, Resume: true, ProgressOut: &out},
		&unconnectedSSHClient{})
	m.progress.Checkpoints[resumed] = true
	m.progress.TotalSteps = phaseCount
	m.progress.StartTime = time.Now()
	m.reporter = NewProgressReporter(&out, output.ModeConcise, m.progress)

	err := m.runPhasesParallel(context.Background(), parallelTestPhases(phaseCount, failing))
	require.ErrorIs(t, err, errTestPhaseMisconfigured)
	assert.Len(t, m.progress.Errors, 1)

	data, err := os.ReadFile(filepath.Join(checkpointsDir(), "bastion-drybloc-pve.json"))
	require.NoError(t, err)

	var saved CheckpointData
	require.NoError(t, json.Unmarshal(data, &saved), "the workers must leave one whole checkpoint")

	for index := range phaseCount {
		name := fmt.Sprintf("phase_%02d", index)
		if name == failing {
			assert.False(t, saved.CompletedPhases[name], name)

			continue
		}

		assert.True(t, saved.CompletedPhases[name], "%s finished, so the checkpoint must list it", name)
	}
}

// Two saves must land in the order they read the progress. The first save
// here is held after it reads the progress, and a step finishes before the
// second save starts. Without the save lock the second save reads and
// writes the newer progress, and the held one then overwrites it with the
// older copy. With the lock the second save waits, and the file ends up
// with the newer progress.
func TestCheckpointSavesLandInOrder(t *testing.T) {
	cfg := dryRunBloc(t)
	t.Setenv("OCFP_HOME", t.TempDir())

	cm := NewCheckpointManager(cfg)
	progress := &ProvisioningProgress{TotalSteps: 2, Checkpoints: map[string]bool{"first": true}}

	var calls atomic.Int32

	firstHeld := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondRead := make(chan struct{})

	cm.afterSnapshot = func() {
		switch calls.Add(1) {
		case 1:
			close(firstHeld)
			<-releaseFirst
		case 2:
			close(secondRead)
		}
	}

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)

	go func() { firstDone <- cm.Save(progress, nil) }()

	<-firstHeld
	cm.MarkPhaseCompleted(progress, "second")

	go func() { secondDone <- cm.Save(progress, nil) }()

	// The second save can only read the progress while the first is held
	// if nothing makes it wait. Give it time to, and when it does, let it
	// write before the held save goes on.
	secondWaited := false

	select {
	case <-secondRead:
		require.NoError(t, <-secondDone)

		secondWaited = true
	case <-time.After(500 * time.Millisecond):
	}

	close(releaseFirst)
	require.NoError(t, <-firstDone)

	if !secondWaited {
		require.NoError(t, <-secondDone)
	}

	checkpoint, err := cm.Load()
	require.NoError(t, err)
	require.NotNil(t, checkpoint)
	assert.True(t, checkpoint.CompletedPhases["second"], "the newer progress must be the one on disk")
}
