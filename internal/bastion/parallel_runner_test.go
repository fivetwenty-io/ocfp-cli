package bastion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
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

	err := m.runPhasesParallel(context.Background(), parallelTestPhases(phaseCount, failing), 0)
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

// stepRecorder builds steps that record each run, and it fails the one
// named failing while fail is set.
type stepRecorder struct {
	mu      sync.Mutex
	runs    map[string]int
	failing string
	fail    bool
}

func (r *stepRecorder) steps(names ...string) []struct {
	name string
	fn   func(context.Context) error
} {
	steps := make([]struct {
		name string
		fn   func(context.Context) error
	}, 0, len(names))

	for _, name := range names {
		steps = append(steps, struct {
			name string
			fn   func(context.Context) error
		}{name, func(context.Context) error {
			r.mu.Lock()
			defer r.mu.Unlock()

			r.runs[name]++

			if r.fail && name == r.failing {
				return errTestPhaseMisconfigured
			}

			return nil
		}})
	}

	return steps
}

// A parallel run that fails after the batch must leave a checkpoint that
// --resume can load, and the resumed run must skip what already finished.
// The workers used to report every batch step as step 0 of 0, which set
// the checkpoint's total to 0, so Load threw the checkpoint away.
func TestParallelRunCheckpointResumes(t *testing.T) {
	cfg := dryRunBloc(t)
	t.Setenv("OCFP_HOME", t.TempDir())

	recorder := &stepRecorder{runs: map[string]int{}, failing: "post_2", fail: true}
	pre := recorder.steps("pre_0", "pre_1", "pre_2")
	par := recorder.steps("par_0", "par_1", "par_2")
	post := recorder.steps("post_0", "post_1", "post_2", "post_3")

	var out bytes.Buffer

	m, _, _ := dryRunManager(t, cfg, &ProvisioningOptions{Parallel: true, ProgressOut: &out}, &unconnectedSSHClient{})
	m.progress.StartTime = time.Now()
	m.reporter = NewProgressReporter(&out, output.ModeConcise, m.progress)

	require.ErrorIs(t, m.runParallelLists(context.Background(), pre, par, post), errTestPhaseMisconfigured)

	checkpoint, err := m.checkpointManager.Load()
	require.NoError(t, err)
	require.NotNil(t, checkpoint, "the checkpoint of a failed parallel run must load")
	assert.Equal(t, 10, checkpoint.TotalSteps)

	recorder.fail = false

	resumed, _, _ := dryRunManager(t, cfg,
		&ProvisioningOptions{Parallel: true, Resume: true, ProgressOut: &out}, &unconnectedSSHClient{})
	resumed.progress = resumed.checkpointManager.RestoreProgress(checkpoint)
	resumed.reporter = NewProgressReporter(&out, output.ModeConcise, resumed.progress)

	require.NoError(t, resumed.runParallelLists(context.Background(), pre, par, post))

	for _, name := range []string{"pre_0", "pre_1", "pre_2", "par_0", "par_1", "par_2", "post_0", "post_1", "post_3"} {
		assert.Equal(t, 1, recorder.runs[name], "%s finished in the first run, so the resumed run skips it", name)
	}

	assert.Equal(t, 2, recorder.runs["post_2"], "the step that failed runs again")
}

// phaseInfoRecorder is a renderer that keeps what it was asked to show.
type phaseInfoRecorder struct {
	mu        sync.Mutex
	started   map[string]output.PhaseInfo
	completed map[string]output.PhaseInfo
}

func (r *phaseInfoRecorder) PhaseStart(info output.PhaseInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.started[info.ID] = info

	return nil
}

func (r *phaseInfoRecorder) PhaseComplete(info output.PhaseInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.completed[info.ID] = info

	return nil
}

func (r *phaseInfoRecorder) PhaseProgress(output.ProgressInfo) error     { return nil }
func (r *phaseInfoRecorder) PhaseFailed(output.PhaseInfo, error) error   { return nil }
func (r *phaseInfoRecorder) PhaseSkipped(output.PhaseInfo, string) error { return nil }
func (r *phaseInfoRecorder) Finalize(output.Summary) error               { return nil }
func (r *phaseInfoRecorder) Close() error                                { return nil }

// Parallel workers start steps that overlap, so the reporter must time and
// number each step by its own start. It kept one start time and one
// number, so a step that finished after another had started took the
// other's start time and number.
func TestReporterTimesOverlappingStepsApart(t *testing.T) {
	t.Parallel()

	recorder := &phaseInfoRecorder{started: map[string]output.PhaseInfo{}, completed: map[string]output.PhaseInfo{}}
	reporter := &ProgressReporter{renderer: recorder, progress: &ProvisioningProgress{}, log: zap.NewNop().Sugar()}

	reporter.ReportPhaseStart("first", 0, 3)
	reporter.ReportPhaseStart("second", 1, 3)
	reporter.ReportPhaseComplete("first", 0)
	reporter.ReportPhaseComplete("second", 0)

	for _, name := range []string{"first", "second"} {
		started, completed := recorder.started[name], recorder.completed[name]
		assert.Equal(t, started.Number, completed.Number, name)
		assert.Equal(t, 3, completed.Total, name)
		assert.Equal(t, started.StartTime, completed.StartTime, name)
	}
}
