package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T14: coerceVMID accepts a valid positive integer string.
func TestCoerceVMID_Valid(t *testing.T) {
	cases := []struct {
		raw      string
		expected int
	}{
		{"12345", 12345},
		{"1", 1},
		{"999999", 999999},
		{" 100 ", 100}, // leading/trailing whitespace tolerated
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := coerceVMID(tc.raw)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, got)
		})
	}
}

// T15: coerceVMID rejects non-integer strings, including injection attempts.
func TestCoerceVMID_RejectsNonInteger(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"injection attempt", "abc; rm -rf /"},
		{"empty string", ""},
		{"float", "123.4"},
		{"alphanumeric", "vm-100"},
		{"hex", "0x1F"},
		{"uuid", "abc-123-def"},
		{"whitespace only", "   "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := coerceVMID(tc.raw)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrVMIDNonInteger),
				"expected ErrVMIDNonInteger for %q; got: %v", tc.raw, err)
		})
	}
}

// T16: coerceVMID rejects zero and negative values.
func TestCoerceVMID_RejectsNonPositive(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"zero", "0"},
		{"negative one", "-1"},
		{"large negative", "-9999"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := coerceVMID(tc.raw)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrVMIDNonPositive),
				"expected ErrVMIDNonPositive for %q; got: %v", tc.raw, err)
		})
	}
}

// T17: Command returns an error when no instance argument is supplied.
// Cobra's ExactArgs(1) rejects zero args and prints usage.
func TestCommand_NoArgs_PrintsUsage(t *testing.T) {
	cmd := NewPVEUnstickCmd()
	cmd.SetArgs([]string{})

	// Capture any output; we just want the error.
	var sb strings.Builder
	cmd.SetOut(&sb)
	cmd.SetErr(&sb)

	err := cmd.Execute()
	require.Error(t, err, "executing unstick with no args must return an error")
}

// TestMatchesInstanceRef validates the instance-ref matching logic.
// Not part of the numbered test plan but guards the matching logic against regressions.
func TestMatchesInstanceRef(t *testing.T) {
	cases := []struct {
		boshInstance string
		ref          string
		want         bool
	}{
		// Exact matches.
		{"uaa/0", "uaa/0", true},
		{"diego-cell/abc-uuid", "diego-cell/abc-uuid", true},
		// Bare job name matches any index.
		{"uaa/0", "uaa", true},
		{"uaa/1", "uaa", true},
		{"diego-cell/0", "diego-cell", true},
		// Mismatches.
		{"uaa/0", "router", false},
		{"uaa/0", "uaa/1", false},
		{"diego-cell/0", "diego-cell/1", false},
		// Instance without separator.
		{"noslash", "noslash", true},
		{"noslash", "other", false},
	}

	for _, tc := range cases {
		t.Run(tc.boshInstance+"~"+tc.ref, func(t *testing.T) {
			got := matchesInstanceRef(tc.boshInstance, tc.ref)
			assert.Equal(t, tc.want, got,
				"matchesInstanceRef(%q, %q)", tc.boshInstance, tc.ref)
		})
	}
}

// TestNewPVECmd verifies the parent pve command is created with unstick as a subcommand.
func TestNewPVECmd(t *testing.T) {
	cmd := NewPVECmd()
	require.NotNil(t, cmd)
	assert.Equal(t, "pve", cmd.Use)

	var found bool
	for _, sub := range cmd.Commands() {
		if strings.HasPrefix(sub.Use, "unstick") {
			found = true
			break
		}
	}
	assert.True(t, found, "pve command must have 'unstick' subcommand")
}

// TestQGAWaitFromEnv covers the OCFP_QGA_WAIT parsing helper. Invalid values
// must fall back to the default rather than fail the operator's recovery flow.
func TestQGAWaitFromEnv(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"unset", "", 30 * time.Second},
		{"valid 60", "60", 60 * time.Second},
		{"valid 5", "5", 5 * time.Second},
		{"invalid garbage falls back to default", "abc", 30 * time.Second},
		{"empty string falls back to default", "", 30 * time.Second},
		{"zero falls back to default", "0", 30 * time.Second},
		{"negative falls back to default", "-10", 30 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OCFP_QGA_WAIT", tc.env)
			got := qgaWaitFromEnv()
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestQGAPingUntil_SuccessFirstTry verifies the bounded poll returns
// immediately when the prober reports the agent reachable on the first probe.
func TestQGAPingUntil_SuccessFirstTry(t *testing.T) {
	calls := 0
	prober := func(_ context.Context, _ []string, _ string, _ int) (bool, string) {
		calls++
		return true, ""
	}

	start := time.Now()
	ok, diag := qgaPingUntil(context.Background(), prober, nil, "pve.example", 100, 5*time.Second)
	elapsed := time.Since(start)

	assert.True(t, ok, "expected probe success on first try")
	assert.Empty(t, diag)
	assert.Equal(t, 1, calls, "prober must be called exactly once on first-try success")
	assert.Less(t, elapsed, 500*time.Millisecond, "first-try success must not sleep")
}

// TestQGAPingUntil_TimeoutReturnsLastDiag verifies a stubborn QGA returns the
// last underlying diagnostic when the deadline expires.
func TestQGAPingUntil_TimeoutReturnsLastDiag(t *testing.T) {
	prober := func(_ context.Context, _ []string, _ string, _ int) (bool, string) {
		return false, "QEMU guest agent is not running"
	}

	ok, diag := qgaPingUntil(context.Background(), prober, nil, "pve.example", 100, 50*time.Millisecond)

	assert.False(t, ok, "expected probe timeout")
	assert.Contains(t, diag, "QEMU guest agent is not running")
}

// TestQGAPingUntil_CancelledContextReturnsImmediately verifies the loop honors
// caller cancellation rather than running to the wall-clock deadline.
func TestQGAPingUntil_CancelledContextReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	prober := func(_ context.Context, _ []string, _ string, _ int) (bool, string) {
		return false, "not reachable"
	}

	start := time.Now()
	ok, _ := qgaPingUntil(ctx, prober, nil, "pve.example", 100, 10*time.Second)
	elapsed := time.Since(start)

	assert.False(t, ok)
	assert.Less(t, elapsed, time.Second, "cancelled context must short-circuit the wait")
}

// TestFormatQGAUnreachableError verifies the actionable error message contains
// every recovery hint the runbook depends on.
func TestFormatQGAUnreachableError(t *testing.T) {
	err := formatQGAUnreachableError("uaa/0", "cf", 123, 30*time.Second, "guest agent is not running")
	require.Error(t, err)

	msg := err.Error()
	for _, want := range []string{
		"vmid 123",
		"30s",
		"guest agent is not running",
		"OCFP_QGA_WAIT",
		"bosh -d cf recreate uaa/0",
		"/var/vcap/sys/log/pve-guest-agent/install.log",
		"installed.flag",
	} {
		assert.Contains(t, msg, want, "actionable error missing %q", want)
	}
}

// TestNewPVEUnstickCmd_RequiredFlags verifies that missing required flags produce errors.
func TestNewPVEUnstickCmd_RequiredFlags(t *testing.T) {
	t.Run("missing all flags", func(t *testing.T) {
		cmd := NewPVEUnstickCmd()
		cmd.SetArgs([]string{"uaa/0"})
		var sb strings.Builder
		cmd.SetErr(&sb)
		err := cmd.Execute()
		require.Error(t, err)
	})

	t.Run("missing vars-file", func(t *testing.T) {
		cmd := NewPVEUnstickCmd()
		cmd.SetArgs([]string{"uaa/0", "-e", "my-bosh", "-d", "cf"})
		var sb strings.Builder
		cmd.SetErr(&sb)
		err := cmd.Execute()
		require.Error(t, err)
	})
}
