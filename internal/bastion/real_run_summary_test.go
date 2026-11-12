package bastion

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A real run's closing summary totals the steps of the list the run used.
// The parallel list is longer than the sequential one, so a parallel run
// that reported the sequential length understated its own steps.
//
// The checkpoint marks every step as done, so the resumed run skips them
// all and reaches the summary without sending anything to a bastion.
func TestRealRunSummaryTotalsTheStepListTheRunUsed(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(fmt.Sprintf("parallel=%t", parallel), func(t *testing.T) {
			cfg := dryRunBloc(t)
			dryRunWorkstation(t, cfg)

			var out bytes.Buffer

			m, _, _ := dryRunManager(t, cfg,
				&ProvisioningOptions{Resume: true, Parallel: parallel}, &unconnectedSSHClient{})

			steps := m.getInitializationPhases()
			if parallel {
				steps = nil
				steps = append(steps, m.parallelPrePhaseList()...)
				steps = append(steps, m.parallelParPhaseList()...)
				steps = append(steps, m.parallelPostPhaseList()...)
			}

			for _, step := range steps {
				m.progress.Checkpoints[step.name] = true
			}

			m.reporter = NewProgressReporter(&out, output.ModeJSON, m.progress)

			require.NoError(t, m.executeInitializationPhases(context.Background()))

			event := summaryEvent(t, out.String())
			assert.Equal(t, true, event["success"])
			assert.InDelta(t, len(steps), event["total_phases"], 0)
			assert.InDelta(t, len(steps), event["completed_phases"], 0)
			assert.InDelta(t, 0, event["failed_phases"], 0)
		})
	}

	// The test compares two different lists, so it fails if they ever
	// become the same length.
	m, _, _ := dryRunManager(t, dryRunBloc(t), &ProvisioningOptions{}, &unconnectedSSHClient{})
	parallelLen := len(m.parallelPrePhaseList()) + len(m.parallelParPhaseList()) + len(m.parallelPostPhaseList())
	assert.NotEqual(t, len(m.getInitializationPhases()), parallelLen)
}
