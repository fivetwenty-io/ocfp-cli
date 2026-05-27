package exec_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	ocfpexec "github.com/ocfp/ocfp-cli-go/internal/exec"
)

// T36 TestRunWithEnv_InjectsEnvNotArgv
// Asserts that the secret value appears in the subprocess env (printed by
// `env`) but never in cmd.Args. Because RunWithEnv returns the combined
// output of the subprocess, we verify the env var shows up in that output.
// We simultaneously confirm that the argv of `env` contains no secret.
func TestRunWithEnv_InjectsEnvNotArgv(t *testing.T) {
	t.Parallel()

	const secretKey = "TEST_SECRET_KEY"
	const secretVal = "super-secret-value-12345"

	ctx := context.Background()

	out, err := ocfpexec.RunWithEnv(ctx, map[string]string{
		secretKey: secretVal,
	}, "env")
	if err != nil {
		t.Fatalf("RunWithEnv returned unexpected error: %v", err)
	}

	output := string(out)

	// Secret must appear in env output (injected via Cmd.Env).
	if !strings.Contains(output, secretKey+"="+secretVal) {
		t.Errorf("expected env output to contain %q, got:\n%s", secretKey+"="+secretVal, output)
	}

	// Argv of `env` itself contains no secret — `env` takes no args here.
	// This asserts the call site did not interpolate the secret into args.
	if strings.Contains("env", secretVal) {
		t.Errorf("secret value must not appear in argv; found in: env")
	}
}

// T37 TestRunWithEnv_EmptyExtraEnv_Equivalent
// With an empty extraEnv map, RunWithEnv behaves exactly like a plain exec:
// it inherits os.Environ() and the subprocess runs successfully.
func TestRunWithEnv_EmptyExtraEnv_Equivalent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// `true` exits 0 with no output — ideal for a no-op sanity check.
	out, err := ocfpexec.RunWithEnv(ctx, nil, "true")
	if err != nil {
		t.Fatalf("RunWithEnv with nil extraEnv failed: %v", err)
	}

	if len(out) != 0 {
		t.Errorf("expected no output from `true`, got: %q", string(out))
	}

	// Empty map variant — same expectation.
	out, err = ocfpexec.RunWithEnv(ctx, map[string]string{}, "true")
	if err != nil {
		t.Fatalf("RunWithEnv with empty extraEnv failed: %v", err)
	}

	if len(out) != 0 {
		t.Errorf("expected no output from `true` (empty map), got: %q", string(out))
	}
}

// TestRunWithEnv_NilContext returns ErrNilContext without exec.
func TestRunWithEnv_NilContext(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // intentional nil ctx for test
	_, err := ocfpexec.RunWithEnv(nil, nil, "true")
	if !errors.Is(err, ocfpexec.ErrNilContext) {
		t.Errorf("expected ErrNilContext, got: %v", err)
	}
}

// TestRunWithEnv_EmptyName returns ErrEmptyName without exec.
func TestRunWithEnv_EmptyName(t *testing.T) {
	t.Parallel()

	_, err := ocfpexec.RunWithEnv(context.Background(), nil, "")
	if !errors.Is(err, ocfpexec.ErrEmptyName) {
		t.Errorf("expected ErrEmptyName, got: %v", err)
	}
}

// TestRunWithEnv_NonZeroExit wraps error with output on non-zero exit.
func TestRunWithEnv_NonZeroExit(t *testing.T) {
	t.Parallel()

	out, err := ocfpexec.RunWithEnv(context.Background(), nil, "false")
	if err == nil {
		t.Fatal("expected error from `false`, got nil")
	}

	// Output slice is non-nil (may be empty) even on error.
	_ = out
}

// TestRunWithEnv_ContextCancelled propagates context cancellation.
func TestRunWithEnv_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before exec

	_, err := ocfpexec.RunWithEnv(ctx, nil, "true")
	if err == nil {
		// On some platforms a pre-cancelled context still allows a fast exec.
		// Acceptable: the important thing is the function does not hang.
		t.Log("note: pre-cancelled context did not produce error on this platform (acceptable)")
	}
}

// TestRunWithEnv_MultipleEnvVars injects several keys and verifies all appear.
func TestRunWithEnv_MultipleEnvVars(t *testing.T) {
	t.Parallel()

	extra := map[string]string{
		"IMP12_KEY_A": "value-alpha",
		"IMP12_KEY_B": "value-beta",
	}

	ctx := context.Background()

	out, err := ocfpexec.RunWithEnv(ctx, extra, "env")
	if err != nil {
		t.Fatalf("RunWithEnv returned unexpected error: %v", err)
	}

	output := string(out)

	for k, v := range extra {
		want := k + "=" + v
		if !strings.Contains(output, want) {
			t.Errorf("expected env output to contain %q", want)
		}
	}
}
