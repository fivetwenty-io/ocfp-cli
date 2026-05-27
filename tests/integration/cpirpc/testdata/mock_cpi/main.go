// mock_cpi simulates a BOSH CPI binary for unit testing.
//
// It reads a single JSON-RPC request from stdin and writes a JSON-RPC response
// to stdout, keyed on the "method" field. Behaviour is controlled by two
// command-line flags injected by the test harness via Client wrapper helpers:
//
//	--mock-response  Raw JSON to emit as the response body. When absent the
//	                 default response echoes the method name as the result.
//	--mock-exit-code Integer exit code to use (default 0). When non-zero the
//	                 process writes to stderr and exits without printing to stdout.
//
// The required BOSH CPI flag --config <path> is accepted (and silently ignored
// beyond parsing) so the client under test can pass it without error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
)

func main() {
	// Required BOSH CPI flag; accepted but not used by the mock.
	configPath := flag.String("config", "", "path to CPI JSON config (ignored by mock)")

	// Test-control flags injected by cpirpc.Client helpers.
	mockResponse := flag.String("mock-response", "", "raw JSON response to emit")
	mockExitCode := flag.String("mock-exit-code", "0", "exit code to return")

	flag.Parse()
	_ = configPath // accepted, not needed

	exitCode, err := strconv.Atoi(*mockExitCode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock_cpi: bad --mock-exit-code %q: %v\n", *mockExitCode, err)
		os.Exit(1)
	}

	if exitCode != 0 {
		fmt.Fprintf(os.Stderr, "mock_cpi: simulated failure (exit %d)\n", exitCode)
		os.Exit(exitCode)
	}

	// Consume stdin so the parent does not get SIGPIPE.
	rawIn, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock_cpi: read stdin: %v\n", err)
		os.Exit(1)
	}

	// Decode method name for default response.
	var req struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(rawIn, &req)

	if *mockResponse != "" {
		fmt.Println(*mockResponse)
		return
	}

	// Default: result is "<method>-result".
	resp := map[string]any{
		"result": req.Method + "-result",
		"error":  nil,
		"log":    "",
	}
	if encErr := json.NewEncoder(os.Stdout).Encode(resp); encErr != nil {
		fmt.Fprintf(os.Stderr, "mock_cpi: encode response: %v\n", encErr)
		os.Exit(1)
	}
}
