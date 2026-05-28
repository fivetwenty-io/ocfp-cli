package commands

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// commandRunner abstracts subprocess execution for testing.
// Production code uses osCommandRunner which wraps os/exec verbatim.
// Tests inject a fake implementation via the pkg-level runner var.
type commandRunner interface {
	// Output runs name with args and returns stdout only (analogous to cmd.Output()).
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Run runs name with args and returns combined stdout+stderr (analogous to cmd.CombinedOutput()).
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// RunSplit runs name with args and returns stdout and stderr separately.
	// Callers that only need one stream may discard the other.
	RunSplit(ctx context.Context, name string, args ...string) (stdout []byte, stderr []byte, err error)
	// LookPath checks whether name is available on PATH (analogous to exec.LookPath).
	LookPath(name string) error
}

// osCommandRunner is the production commandRunner. It delegates directly to
// os/exec with no additional logic so production behaviour is unchanged.
type osCommandRunner struct{}

// Output executes name with args and returns stdout only.
// Inputs: ctx must be non-nil; name must be a valid executable.
// Failure modes: exec failure, non-zero exit, context cancellation.
func (osCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output() //nolint:gosec // caller is responsible for safe arg construction
}

// Run executes name with args and returns combined stdout+stderr.
// Inputs: ctx must be non-nil; name must be a valid executable.
// Failure modes: exec failure, non-zero exit, context cancellation.
func (osCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // caller is responsible for safe arg construction
}

// RunSplit executes name with args, returning stdout and stderr as separate byte slices.
// Inputs: ctx must be non-nil; name must be a valid executable.
// Failure modes: exec failure, non-zero exit, context cancellation. Both buffers
// are populated up to the point of failure before error is returned.
func (osCommandRunner) RunSplit(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	var stdoutBuf, stderrBuf bytes.Buffer

	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // caller is responsible for safe arg construction
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()

	return stdoutBuf.Bytes(), stderrBuf.Bytes(), err
}

// LookPath reports whether name is available on PATH.
// Failure modes: executable not found returns non-nil error (same as exec.LookPath).
func (osCommandRunner) LookPath(name string) error {
	_, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("lookpath %s: %w", name, err)
	}

	return nil
}

// runner is the package-level commandRunner used by all provider functions.
// Tests replace this with a fakeRunner to avoid spawning real processes.
var runner commandRunner = osCommandRunner{} //nolint:gochecknoglobals // intentional seam for testing
