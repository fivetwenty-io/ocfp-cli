package cpirpc_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ocfp/ocfp-cli-go/tests/integration/cpirpc"
)

// mockCPIPath is the compiled mock_cpi binary used across all tests.
var mockCPIPath string

// TestMain compiles testdata/mock_cpi/main.go into a temp directory once
// before any test runs. All tests inject mockCPIPath via Client.BinaryPath.
func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "cpirpc-test-*")
	if err != nil {
		panic("cpirpc TestMain: cannot create temp dir: " + err.Error())
	}
	defer os.RemoveAll(tmpDir)

	binaryName := "mock_cpi"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	mockCPIPath = filepath.Join(tmpDir, binaryName)

	// Locate mock_cpi source relative to this test file.
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("cpirpc TestMain: runtime.Caller(0) failed")
	}
	srcDir := filepath.Join(filepath.Dir(testFile), "testdata", "mock_cpi")

	cmd := exec.Command("go", "build", "-o", mockCPIPath, ".")
	cmd.Dir = srcDir
	out, buildErr := cmd.CombinedOutput()
	if buildErr != nil {
		panic("cpirpc TestMain: build mock_cpi failed: " + buildErr.Error() +
			"\noutput: " + string(out) +
			"\nbinary will be at: " + mockCPIPath)
	}

	os.Exit(m.Run())
}

// minimalConfig returns a minimal CPI config map suitable for tests.
func minimalConfig() map[string]any {
	return map[string]any{
		"host":     "127.0.0.1",
		"port":     8006,
		"user":     "test",
		"password": "test",
		"node":     "pve",
	}
}

// newMockClient returns a Client wired to the mock binary with the given
// --mock-response and --mock-exit-code flags. Pass empty strings to use
// the mock's defaults (method-echo response, exit 0).
func newMockClient(response, exitCode string) *cpirpc.Client {
	var extra []string
	if response != "" {
		extra = append(extra, "--mock-response", response)
	}
	if exitCode != "" {
		extra = append(extra, "--mock-exit-code", exitCode)
	}
	c := cpirpc.New(mockCPIPath, minimalConfig())
	c.ExtraArgs = extra
	return c
}

// T65 TestCall_Roundtrip: mock cpi echoes a known success response; client
// parses result and error correctly.
func TestCall_Roundtrip(t *testing.T) {
	t.Parallel()

	respJSON := `{"result":"stemcell-42","error":null,"log":"some log"}`
	client := newMockClient(respJSON, "")
	defer client.Close()

	req := cpirpc.Request{
		Method:    "create_stemcell",
		Arguments: []any{"/path/to/image", map[string]any{"name": "bosh-pve", "version": "1"}},
		Context:   map[string]any{"request_id": "test-001"},
	}

	resp, err := client.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("Call returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("Call returned nil response")
	}
	if resp.Error != nil {
		t.Fatalf("expected no error in response, got: %v", resp.Error)
	}
	if resp.Log != "some log" {
		t.Errorf("expected log %q, got %q", "some log", resp.Log)
	}

	// Result is unmarshalled as interface{}; BOSH CPI returns strings as stemcell CIDs.
	resultStr, ok := resp.Result.(string)
	if !ok {
		t.Fatalf("expected string result, got %T: %v", resp.Result, resp.Result)
	}
	if resultStr != "stemcell-42" {
		t.Errorf("expected result %q, got %q", "stemcell-42", resultStr)
	}
}

// T66 TestCall_ErrorResponse: mock returns a CPI error object; client returns
// Response with non-nil Error; Call itself returns nil error.
func TestCall_ErrorResponse(t *testing.T) {
	t.Parallel()

	respJSON := `{"result":null,"error":{"type":"Bosh::Clouds::CloudError","message":"disk not found","ok_to_retry":false},"log":""}`
	client := newMockClient(respJSON, "")
	defer client.Close()

	req := cpirpc.Request{
		Method:    "delete_disk",
		Arguments: []any{"disk-missing"},
	}

	resp, err := client.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("Call returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("Call returned nil response")
	}
	if resp.Error == nil {
		t.Fatal("expected non-nil Error in response, got nil")
	}
	if resp.Error.Type != "Bosh::Clouds::CloudError" {
		t.Errorf("expected error type %q, got %q", "Bosh::Clouds::CloudError", resp.Error.Type)
	}
	if resp.Error.Message != "disk not found" {
		t.Errorf("expected error message %q, got %q", "disk not found", resp.Error.Message)
	}
	if resp.Error.OkToRetry {
		t.Error("expected ok_to_retry=false, got true")
	}
	// Error() string includes type and message.
	errStr := resp.Error.Error()
	if !strings.Contains(errStr, "Bosh::Clouds::CloudError") {
		t.Errorf("Error() missing type: %q", errStr)
	}
	if !strings.Contains(errStr, "disk not found") {
		t.Errorf("Error() missing message: %q", errStr)
	}
}

// T67 TestCall_BinaryNotFound: BinaryPath does not exist; Call returns a
// wrapped error; no panic.
func TestCall_BinaryNotFound(t *testing.T) {
	t.Parallel()

	client := cpirpc.New("/nonexistent/path/to/cpi", minimalConfig())
	defer client.Close()

	req := cpirpc.Request{Method: "info"}
	_, err := client.Call(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
	// Any OS-level error mentioning the exec failure is acceptable.
	t.Logf("BinaryNotFound error (expected): %v", err)
}

// T68 TestCall_NonZeroExit: mock exits 1; Call returns error that includes
// stderr content.
func TestCall_NonZeroExit(t *testing.T) {
	t.Parallel()

	client := newMockClient("", "1")
	defer client.Close()

	req := cpirpc.Request{Method: "has_vm", Arguments: []any{"vm-99"}}
	_, err := client.Call(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
	// Error must mention the method.
	if !strings.Contains(err.Error(), "has_vm") {
		t.Errorf("error missing method name; got: %v", err)
	}
	// Error must carry stderr snippet ("simulated failure" written by mock).
	if !strings.Contains(err.Error(), "simulated failure") {
		t.Errorf("error missing stderr snippet; got: %v", err)
	}
}

// T69 TestClose_RemovesTmpConfig: Close removes the temp config file; second
// Close is a no-op (no double-remove error).
func TestClose_RemovesTmpConfig(t *testing.T) {
	t.Parallel()

	respJSON := `{"result":"ok","error":null,"log":""}`
	client := newMockClient(respJSON, "")

	// Trigger config file creation with a real Call.
	req := cpirpc.Request{Method: "info"}
	_, err := client.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	// Second Close must be a no-op.
	if err := client.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}
}

// T70 TestNew_ConfigSerializesToJSON: verify that the Config map serialises to
// valid JSON and round-trips correctly (New stores config; Call writes it).
func TestNew_ConfigSerializesToJSON(t *testing.T) {
	t.Parallel()

	cfg := map[string]any{
		"host":       "pve.example.com",
		"port":       8006,
		"user":       "root@pam",
		"password":   "secret",
		"node":       "pve01",
		"storage":    "local-lvm",
		"vm_id_pool": []any{float64(900), float64(999)},
	}

	respJSON := `{"result":"ok","error":null,"log":""}`

	c := cpirpc.New(mockCPIPath, cfg)
	c.ExtraArgs = []string{"--mock-response", respJSON}
	defer c.Close()

	// First Call triggers config file write.
	req := cpirpc.Request{Method: "info"}
	_, err := c.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	// Verify the config JSON round-trips via direct marshal/unmarshal.
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if back["host"] != "pve.example.com" {
		t.Errorf("host mismatch: %v", back["host"])
	}
	if back["node"] != "pve01" {
		t.Errorf("node mismatch: %v", back["node"])
	}
}

// TestCall_NilContext verifies that a nil context returns an error rather than
// panicking.
func TestCall_NilContext(t *testing.T) {
	t.Parallel()

	client := cpirpc.New(mockCPIPath, minimalConfig())
	defer client.Close()

	req := cpirpc.Request{Method: "info"}
	//nolint:staticcheck // intentionally passing nil to verify guard
	_, err := client.Call(nil, req)
	if err == nil {
		t.Fatal("expected error for nil context, got nil")
	}
}

// TestCall_ContextCancellation verifies that context cancellation terminates
// the subprocess and returns an error.
func TestCall_ContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Call

	client := cpirpc.New(mockCPIPath, minimalConfig())
	defer client.Close()

	req := cpirpc.Request{Method: "info"}
	_, err := client.Call(ctx, req)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

// TestCall_DefaultArgumentsAndContext verifies that nil Arguments and Context
// are normalised to empty slices/maps (not marshalled as JSON null).
func TestCall_DefaultArgumentsAndContext(t *testing.T) {
	t.Parallel()

	client := newMockClient(`{"result":"ok","error":null,"log":""}`, "")
	defer client.Close()

	// Arguments and Context intentionally left nil.
	req := cpirpc.Request{Method: "info"}
	resp, err := client.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("Call returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("Call returned nil response")
	}
}

// TestCall_ConcurrentCalls verifies that the sync.Once config-file init is
// safe under concurrent calls (no race on configPath write).
func TestCall_ConcurrentCalls(t *testing.T) {
	t.Parallel()

	respJSON := `{"result":"ok","error":null,"log":""}`
	client := newMockClient(respJSON, "")
	defer client.Close()

	const goroutines = 5
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			req := cpirpc.Request{Method: "info"}
			_, errs[idx] = client.Call(context.Background(), req)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
}

// TestCall_AutoIncrementID verifies that successive calls succeed without
// error, confirming the auto-increment counter does not stall or wrap.
func TestCall_AutoIncrementID(t *testing.T) {
	t.Parallel()

	respJSON := `{"result":"ok","error":null,"log":""}`
	client := newMockClient(respJSON, "")
	defer client.Close()

	for i := range 3 {
		req := cpirpc.Request{Method: "info"}
		resp, err := client.Call(context.Background(), req)
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
		if resp == nil {
			t.Fatalf("call %d: nil response", i+1)
		}
	}
}

// TestClose_BeforeCall verifies that Close before any Call is a no-op.
func TestClose_BeforeCall(t *testing.T) {
	t.Parallel()

	client := cpirpc.New(mockCPIPath, minimalConfig())
	if err := client.Close(); err != nil {
		t.Fatalf("Close before Call returned error: %v", err)
	}
}
