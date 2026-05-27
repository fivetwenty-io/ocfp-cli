// Package exec provides subprocess helpers that keep secrets out of argv.
package exec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// RunWithEnv runs name with args, injecting extraEnv into the subprocess
// environment. Credentials passed via extraEnv never appear in the process
// table (argv is the only public part of a process visible to `ps`).
//
// The subprocess inherits the current process environment (os.Environ()) with
// each key in extraEnv added or overridden. Callers can therefore observe
// exactly which variables are being injected: they are listed in extraEnv.
//
// Returns combined stdout+stderr and the first error encountered (context
// cancellation, exec failure, or non-zero exit).
//
// Inputs:
//   - ctx: cancellation/deadline; non-nil required.
//   - extraEnv: map of KEY -> VALUE to inject; nil or empty map is valid.
//   - name: executable name or absolute path; must be non-empty.
//   - args: arguments passed to name; no credential values should appear here.
//
// Failure modes:
//   - ctx is nil: returns ErrNilContext immediately (no exec).
//   - name is empty: returns ErrEmptyName immediately (no exec).
//   - executable not found: exec.CommandContext returns the error from os/exec.
//   - non-zero exit: error wraps the combined output for diagnosis.
//   - context cancelled/timed out: error reflects the context state.
func RunWithEnv(ctx context.Context, extraEnv map[string]string, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}

	if name == "" {
		return nil, ErrEmptyName
	}

	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // caller is responsible for safe arg construction

	// Build env: inherit current process, then overlay caller-supplied keys.
	// Explicit construction (not append) ensures extraEnv is fully visible.
	base := os.Environ()
	env := make([]string, 0, len(base)+len(extraEnv))
	env = append(env, base...)

	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}

	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("runwithenv: %s: %w (output: %s)", name, err, string(out))
	}

	return out, nil
}
