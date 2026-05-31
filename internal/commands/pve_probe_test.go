package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/pve/probes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProbe is a test double for probes.Probe. It returns a fixed Result on Run.
type fakeProbe struct {
	name   string
	result probes.Result
}

func (f *fakeProbe) Name() string { return f.name }

func (f *fakeProbe) Run(_ context.Context) probes.Result { return f.result }

// fakeBuilder returns a probeBuilder closure that always yields the given probes.
func fakeBuilder(ps ...probes.Probe) probeBuilder {
	return func(_, _, _ string) []probes.Probe {
		return ps
	}
}

// TestPVEProbe_NoArgs_Usage verifies that running `pve probe` with zero positional
// args returns an error (cobra.ExactArgs(1) enforcement).
func TestPVEProbe_NoArgs_Usage(t *testing.T) {
	cmd := newPVEProbeCmdWithBuilder(fakeBuilder())
	cmd.SetArgs([]string{"--bosh-env", "test", "--director-ip", "1.2.3.4"})

	var sb strings.Builder
	cmd.SetOut(&sb)
	cmd.SetErr(&sb)

	err := cmd.Execute()
	require.Error(t, err, "executing probe with no bloc arg must return an error")
}

// TestPVEProbe_MissingRequiredFlags verifies that omitting required flags returns errors.
func TestPVEProbe_MissingRequiredFlags(t *testing.T) {
	t.Run("missing bosh-env", func(t *testing.T) {
		cmd := newPVEProbeCmdWithBuilder(fakeBuilder())
		cmd.SetArgs([]string{"mybloc", "--director-ip", "1.2.3.4"})
		var sb strings.Builder
		cmd.SetErr(&sb)
		err := cmd.Execute()
		require.Error(t, err)
	})

	t.Run("missing director-ip", func(t *testing.T) {
		cmd := newPVEProbeCmdWithBuilder(fakeBuilder())
		cmd.SetArgs([]string{"mybloc", "--bosh-env", "lab"})
		var sb strings.Builder
		cmd.SetErr(&sb)
		err := cmd.Execute()
		require.Error(t, err)
	})
}

// TestPVEProbe_AllOK_ZeroExit verifies that when all probes return OK, the command
// returns nil (exit 0 semantics) and prints an OK line to stdout.
func TestPVEProbe_AllOK_ZeroExit(t *testing.T) {
	okProbe := &fakeProbe{
		name:   "fake-ok",
		result: probes.Result{OK: true, Detail: "all-good"},
	}

	cmd := newPVEProbeCmdWithBuilder(fakeBuilder(okProbe))
	cmd.SetArgs([]string{
		"mybloc",
		"--bosh-env", "lab",
		"--director-ip", "10.0.0.1",
	})

	var stdout, stderr strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	require.NoError(t, err, "all-OK probes must produce nil error (exit 0)")

	outStr := stdout.String()
	assert.Contains(t, outStr, "OK", "stdout must contain OK token")
	assert.Contains(t, outStr, "mybloc", "stdout must reference the bloc name")
	assert.Empty(t, stderr.String(), "stderr must be empty on full success")
}

// TestPVEProbe_ProbeFails_NonZeroExit verifies that a failing probe causes the
// command to return a non-nil error (exit 1 semantics), print FAIL to stderr,
// and include the remediation text when provided.
func TestPVEProbe_ProbeFails_NonZeroExit(t *testing.T) {
	failProbe := &fakeProbe{
		name: "fake-fail",
		result: probes.Result{
			OK:          false,
			Detail:      "FAILED_ROWS=3",
			Remediation: "Run: bosh recreate uaa",
		},
	}

	cmd := newPVEProbeCmdWithBuilder(fakeBuilder(failProbe))
	cmd.SetArgs([]string{
		"prodbloc",
		"--bosh-env", "prod",
		"--director-ip", "10.1.2.3",
	})

	var stdout, stderr strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	require.Error(t, err, "failing probe must produce non-nil error (exit 1)")

	errStr := stderr.String()
	assert.Contains(t, errStr, "FAIL", "stderr must contain FAIL token")
	assert.Contains(t, errStr, "prodbloc", "stderr must reference the bloc name")
	assert.Contains(t, errStr, "FAILED_ROWS=3", "stderr must contain detail from probe result")
	assert.Contains(t, errStr, "bosh recreate uaa", "stderr must contain remediation text")
	assert.Empty(t, stdout.String(), "stdout must be empty on failure")
}

// TestPVEProbe_FirstFailStops verifies that RunAll stops at the first failing
// probe and does not report detail from subsequent probes.
func TestPVEProbe_FirstFailStops(t *testing.T) {
	first := &fakeProbe{
		name:   "first-fail",
		result: probes.Result{OK: false, Detail: "first-detail", Remediation: "fix first"},
	}
	second := &fakeProbe{
		name:   "second-ok",
		result: probes.Result{OK: true, Detail: "second-detail"},
	}

	cmd := newPVEProbeCmdWithBuilder(fakeBuilder(first, second))
	cmd.SetArgs([]string{
		"bloc",
		"--bosh-env", "e",
		"--director-ip", "1.2.3.4",
	})

	var stdout, stderr strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	require.Error(t, err)

	errStr := stderr.String()
	assert.Contains(t, errStr, "first-detail", "stderr must show first failure detail")
	assert.NotContains(t, errStr, "second-detail", "stderr must not show second probe detail when first fails")
}

// TestPVEProbe_NoRemediation verifies FAIL output when remediation is empty:
// error returned, FAIL printed, no trailing blank+remediation block.
func TestPVEProbe_NoRemediation(t *testing.T) {
	failProbe := &fakeProbe{
		name:   "no-remed",
		result: probes.Result{OK: false, Detail: "tcp-timeout"},
	}

	cmd := newPVEProbeCmdWithBuilder(fakeBuilder(failProbe))
	cmd.SetArgs([]string{
		"bloc",
		"--bosh-env", "e",
		"--director-ip", "1.2.3.4",
	})

	var stdout, stderr strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	require.Error(t, err)

	errStr := stderr.String()
	assert.Contains(t, errStr, "FAIL")
	assert.Contains(t, errStr, "tcp-timeout")
}

// TestNewPVECmd_HasProbeSubcommand verifies that NewPVECmd registers the probe subcommand.
func TestNewPVECmd_HasProbeSubcommand(t *testing.T) {
	cmd := NewPVECmd()
	require.NotNil(t, cmd)

	var found bool

	for _, sub := range cmd.Commands() {
		if strings.HasPrefix(sub.Use, "probe") {
			found = true

			break
		}
	}

	assert.True(t, found, "pve command must have 'probe' subcommand after AddCommand")
}
