// Package cpirpc provides a Go client for the BOSH CPI JSON-RPC protocol.
// Each Call launches one cpi binary process, writes a JSON-RPC request to
// stdin, and parses the JSON-RPC response from stdout. The binary flag
// convention matches scripts/lifecycle: `cpi --config <path>`.
package cpirpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

// Client invokes the CPI binary once per Call. Config is written to a temp
// file on first use (sync.Once) and removed by Close.
type Client struct {
	BinaryPath string         // path to compiled cpi binary, e.g. ./bin/cpi
	Config     map[string]any // CPI JSON config (host, port, user, ...)

	// ExtraArgs are appended to the binary invocation after --config <path>.
	// Used by tests to inject mock-control flags (e.g. --mock-response).
	// Production callers leave this nil.
	ExtraArgs []string

	configPath string
	once       sync.Once
	callID     atomic.Uint64
}

// Request is a BOSH CPI JSON-RPC 2.0 request envelope.
type Request struct {
	Method    string         `json:"method"`
	Arguments []any          `json:"arguments"`
	Context   map[string]any `json:"context"`
}

// Response is a BOSH CPI JSON-RPC 2.0 response envelope.
type Response struct {
	Result any       `json:"result"`
	Error  *RPCError `json:"error,omitempty"`
	Log    string    `json:"log,omitempty"`
}

// RPCError is the structured error object in a CPI response.
type RPCError struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	OkToRetry bool   `json:"ok_to_retry"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("cpi error [%s]: %s", e.Type, e.Message)
}

// New returns a Client configured with the given binary path and CPI config.
// The config is not written to disk until the first Call.
func New(binaryPath string, config map[string]any) *Client {
	return &Client{
		BinaryPath: binaryPath,
		Config:     config,
	}
}

// initConfigFile writes Config as JSON to a temp file exactly once.
// Returns the path on success, or an error.
func (c *Client) initConfigFile() (string, error) {
	var initErr error

	c.once.Do(func() {
		f, err := os.CreateTemp("", "cpirpc-config-*.json")
		if err != nil {
			initErr = fmt.Errorf("cpirpc: create temp config: %w", err)

			return
		}

		enc := json.NewEncoder(f)
		if err := enc.Encode(c.Config); err != nil {
			f.Close()
			os.Remove(f.Name())

			initErr = fmt.Errorf("cpirpc: encode config: %w", err)

			return
		}

		if err := f.Close(); err != nil {
			os.Remove(f.Name())

			initErr = fmt.Errorf("cpirpc: close temp config: %w", err)

			return
		}

		c.configPath = f.Name()
	})

	if initErr != nil {
		// Reset once so a retry after a transient failure can re-attempt.
		c.once = sync.Once{}

		return "", initErr
	}

	return c.configPath, nil
}

// Call invokes the CPI binary with the given request. It:
//  1. Lazily writes the config JSON to a temp file.
//  2. Launches `<BinaryPath> --config <configPath> [ExtraArgs...]` via exec.CommandContext.
//  3. Writes the JSON-RPC request (with auto-incremented id) to stdin.
//  4. Waits for the process to exit; returns an error if non-zero.
//  5. Parses the stdout JSON-RPC response.
//  6. Returns the Response, which may have a non-nil Error field.
//
// Inputs validated:
//   - ctx: must be non-nil; cancellation propagates SIGKILL to the cpi process.
//   - BinaryPath: checked by the OS at exec time; missing binary returns a wrapped error.
//   - Config: serialization errors surface at first Call.
//   - Response: a non-empty error field sets Response.Error; caller must check.
//
// Failure modes:
//   - BinaryPath missing or not executable: wrapped *exec.Error / path error.
//   - Config JSON serialization error: fmt-wrapped error.
//   - Process exits non-zero: error includes stderr (up to 2 KiB).
//   - Empty stdout: error indicating empty response.
//   - stdout not valid JSON: json.Unmarshal error wrapped.
//   - Response.Error non-nil: returned in Response; Call itself returns nil error.
func (c *Client) Call(ctx context.Context, req Request) (*Response, error) {
	if ctx == nil {
		return nil, errors.New("cpirpc: nil context")
	}

	cfgPath, err := c.initConfigFile()
	if err != nil {
		return nil, err
	}

	// Auto-increment call ID (1-based).
	id := c.callID.Add(1)

	// Build the wire request. id is appended for traceability; BOSH CPI
	// servers ignore the id field but downstream tooling may log it.
	wire := struct {
		Method    string         `json:"method"`
		Arguments []any          `json:"arguments"`
		Context   map[string]any `json:"context"`
		ID        uint64         `json:"id"`
	}{
		Method:    req.Method,
		Arguments: req.Arguments,
		Context:   req.Context,
		ID:        id,
	}
	if wire.Arguments == nil {
		wire.Arguments = []any{}
	}

	if wire.Context == nil {
		wire.Context = map[string]any{}
	}

	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("cpirpc: marshal request: %w", err)
	}

	body = append(body, '\n')

	// Build args: --config <path> [ExtraArgs...]
	args := make([]string, 0, 2+len(c.ExtraArgs))
	args = append(args, "--config", cfgPath)
	args = append(args, c.ExtraArgs...)

	cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
	cmd.Stdin = bytes.NewReader(body)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrSnip := stderr.String()

		const maxSnip = 2048
		if len(stderrSnip) > maxSnip {
			stderrSnip = "..." + stderrSnip[len(stderrSnip)-maxSnip:]
		}

		if stderrSnip != "" {
			return nil, fmt.Errorf("cpirpc: cpi binary exited with error for method %q: %w\nstderr: %s",
				req.Method, err, stderrSnip)
		}

		return nil, fmt.Errorf("cpirpc: cpi binary exited with error for method %q: %w",
			req.Method, err)
	}

	raw := stdout.Bytes()
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("cpirpc: empty response from cpi binary for method %q", req.Method)
	}

	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("cpirpc: parse response for method %q: %w", req.Method, err)
	}

	return &resp, nil
}

// Close removes the temp config file written by the first Call.
// Safe to call before any Call (no-op). Safe to call multiple times.
func (c *Client) Close() error {
	if c.configPath == "" {
		return nil
	}

	path := c.configPath
	c.configPath = ""

	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cpirpc: remove temp config %q: %w", path, err)
	}

	return nil
}
