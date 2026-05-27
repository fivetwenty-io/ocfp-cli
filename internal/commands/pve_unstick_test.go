package commands

import (
	"errors"
	"strings"
	"testing"

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
