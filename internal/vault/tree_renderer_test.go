package vault

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/output"
)

func TestNewTreeRenderer(t *testing.T) {
	tests := []struct {
		name string
		mode output.Mode
	}{
		{"Interactive mode", output.ModeInteractive},
		{"Concise mode", output.ModeConcise},
		{"JSON mode", output.ModeJSON},
		{"YAML mode", output.ModeYAML},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			renderer := NewTreeRenderer(tt.mode)
			if renderer == nil {
				t.Fatal("NewTreeRenderer returned nil")
			}
			if tt.mode == output.ModeInteractive && !renderer.useColor {
				t.Error("Interactive mode should enable color")
			}
			if tt.mode != output.ModeInteractive && renderer.useColor {
				t.Error("Non-interactive mode should disable color")
			}
		})
	}
}

func TestDetectUnicodeSupport(t *testing.T) {
	tests := []struct {
		name     string
		lang     string
		lcAll    string
		term     string
		expected bool
	}{
		{"UTF-8 in LANG", "en_US.UTF-8", "", "", true},
		{"UTF-8 in LC_ALL", "", "en_US.UTF-8", "", true},
		{"256color term", "", "", "xterm-256color", true},
		{"xterm", "", "", "xterm", true},
		{"No unicode", "C", "", "vt100", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set test env (t.Setenv restores originals on cleanup)
			t.Setenv("LANG", tt.lang)
			t.Setenv("LC_ALL", tt.lcAll)
			t.Setenv("TERM", tt.term)

			result := detectUnicodeSupport()
			if result != tt.expected {
				t.Errorf("detectUnicodeSupport() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestTreeRenderer_GetTreeChars(t *testing.T) {
	tests := []struct {
		name            string
		useUnicode      bool
		indentStack     []bool
		isLast          bool
		expectedPrefix  string
		expectedConnect string
	}{
		{
			name:            "Unicode middle child no indent",
			useUnicode:      true,
			indentStack:     []bool{},
			isLast:          false,
			expectedPrefix:  "",
			expectedConnect: "├─ ",
		},
		{
			name:            "Unicode last child no indent",
			useUnicode:      true,
			indentStack:     []bool{},
			isLast:          true,
			expectedPrefix:  "",
			expectedConnect: "└─ ",
		},
		{
			name:            "ASCII middle child no indent",
			useUnicode:      false,
			indentStack:     []bool{},
			isLast:          false,
			expectedPrefix:  "",
			expectedConnect: "|- ",
		},
		{
			name:            "ASCII last child no indent",
			useUnicode:      false,
			indentStack:     []bool{},
			isLast:          true,
			expectedPrefix:  "",
			expectedConnect: "\\- ",
		},
		{
			name:            "Unicode with parent continuation",
			useUnicode:      true,
			indentStack:     []bool{true},
			isLast:          false,
			expectedPrefix:  "│  ",
			expectedConnect: "├─ ",
		},
		{
			name:            "Unicode with parent no continuation",
			useUnicode:      true,
			indentStack:     []bool{false},
			isLast:          false,
			expectedPrefix:  "   ",
			expectedConnect: "├─ ",
		},
		{
			name:            "Unicode nested",
			useUnicode:      true,
			indentStack:     []bool{true, false, true},
			isLast:          true,
			expectedPrefix:  "│     │  ",
			expectedConnect: "└─ ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			renderer := &TreeRenderer{
				useUnicode:  tt.useUnicode,
				indentStack: tt.indentStack,
			}

			prefix, connector := renderer.getTreeChars(tt.isLast)

			if prefix != tt.expectedPrefix {
				t.Errorf("prefix = %q, want %q", prefix, tt.expectedPrefix)
			}
			if connector != tt.expectedConnect {
				t.Errorf("connector = %q, want %q", connector, tt.expectedConnect)
			}
		})
	}
}

func TestTreeRenderer_StartEndDirectory(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	renderer := NewTreeRenderer(output.ModeConcise) // No color for easier testing
	renderer.useUnicode = false                     // Use ASCII for predictable output

	renderer.StartDirectory("config", false)
	if len(renderer.indentStack) != 1 {
		t.Errorf("indentStack length = %d, want 1", len(renderer.indentStack))
	}
	if renderer.indentStack[0] != true {
		t.Error("indentStack[0] should be true for non-last directory")
	}

	renderer.StartDirectory("nested", true)
	if len(renderer.indentStack) != 2 {
		t.Errorf("indentStack length = %d, want 2", len(renderer.indentStack))
	}
	if renderer.indentStack[1] != false {
		t.Error("indentStack[1] should be false for last directory")
	}

	renderer.EndDirectory()
	if len(renderer.indentStack) != 1 {
		t.Errorf("After EndDirectory, indentStack length = %d, want 1", len(renderer.indentStack))
	}

	renderer.EndDirectory()
	if len(renderer.indentStack) != 0 {
		t.Errorf("After second EndDirectory, indentStack length = %d, want 0", len(renderer.indentStack))
	}

	// Restore stdout and read output
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Verify output contains directory names
	if !strings.Contains(output, "config/") {
		t.Error("Output should contain 'config/'")
	}
	if !strings.Contains(output, "nested/") {
		t.Error("Output should contain 'nested/'")
	}
}

func TestTreeRenderer_RenderKeyValidation(t *testing.T) {
	tests := []struct {
		name           string
		key            string
		inceptionHash  string
		productionHash string
		err            error
		expectSuccess  bool
	}{
		{
			name:           "Successful validation",
			key:            "password",
			inceptionHash:  "abc123def456",
			productionHash: "abc123def456",
			err:            nil,
			expectSuccess:  true,
		},
		{
			name:           "Checksum mismatch",
			key:            "secret",
			inceptionHash:  "abc123def456",
			productionHash: "xyz789uvw012",
			err:            fmt.Errorf("checksum mismatch"),
			expectSuccess:  false,
		},
		{
			name:           "Validation error",
			key:            "token",
			inceptionHash:  "abc123",
			productionHash: "",
			err:            fmt.Errorf("production checksum failed"),
			expectSuccess:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			renderer := NewTreeRenderer(output.ModeConcise)
			renderer.useUnicode = false

			initialValidated := renderer.totalValidated
			initialFailures := len(renderer.failures)

			renderer.RenderKeyValidation(tt.key, tt.inceptionHash, tt.productionHash, tt.err, true)

			// Restore stdout
			w.Close()
			os.Stdout = oldStdout
			var buf bytes.Buffer
			io.Copy(&buf, r)
			output := buf.String()

			// Check validation count
			if tt.expectSuccess {
				if renderer.totalValidated != initialValidated+1 {
					t.Errorf("totalValidated = %d, want %d", renderer.totalValidated, initialValidated+1)
				}
				if len(renderer.failures) != initialFailures {
					t.Errorf("failures count = %d, want %d", len(renderer.failures), initialFailures)
				}
			} else {
				if renderer.totalValidated != initialValidated {
					t.Errorf("totalValidated = %d, want %d (no change)", renderer.totalValidated, initialValidated)
				}
				if len(renderer.failures) != initialFailures+1 {
					t.Errorf("failures count = %d, want %d", len(renderer.failures), initialFailures+1)
				}
			}

			// Check output contains key name
			if !strings.Contains(output, ":"+tt.key) {
				t.Errorf("Output should contain key ':%s'", tt.key)
			}

			// Check output contains checksums (truncated)
			if tt.inceptionHash != "" && !strings.Contains(output, truncateHash(tt.inceptionHash)) {
				t.Error("Output should contain truncated inception hash")
			}
		})
	}
}

func TestTreeRenderer_RenderFailureSummary(t *testing.T) {
	tests := []struct {
		name     string
		failures []ValidationFailure
		wantErr  bool
	}{
		{
			name:     "No failures",
			failures: []ValidationFailure{},
			wantErr:  false,
		},
		{
			name: "Single failure",
			failures: []ValidationFailure{
				{
					Key:                "password",
					InceptionChecksum:  "abc123",
					ProductionChecksum: "xyz789",
					ErrorMessage:       "checksum mismatch",
				},
			},
			wantErr: false,
		},
		{
			name: "Multiple failures",
			failures: []ValidationFailure{
				{
					Key:                "password",
					InceptionChecksum:  "abc123",
					ProductionChecksum: "xyz789",
					ErrorMessage:       "checksum mismatch",
				},
				{
					Key:          "token",
					ErrorMessage: "production vault unreachable",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			renderer := NewTreeRenderer(output.ModeConcise)
			renderer.failures = tt.failures

			err := renderer.RenderFailureSummary()

			// Restore stdout
			w.Close()
			os.Stdout = oldStdout
			var buf bytes.Buffer
			io.Copy(&buf, r)
			output := buf.String()

			if (err != nil) != tt.wantErr {
				t.Errorf("RenderFailureSummary() error = %v, wantErr %v", err, tt.wantErr)
			}

			if len(tt.failures) == 0 {
				if output != "" {
					t.Error("No failures should produce no output")
				}
			} else {
				if !strings.Contains(output, "Validation Failures") {
					t.Error("Output should contain 'Validation Failures' header")
				}
				for _, failure := range tt.failures {
					if !strings.Contains(output, failure.Key) {
						t.Errorf("Output should contain failed key: %s", failure.Key)
					}
				}
			}
		})
	}
}

func TestTruncateHash(t *testing.T) {
	tests := []struct {
		name     string
		hash     string
		expected string
	}{
		{"Long hash", "abc123def456ghi789", "abc123de"},
		{"Exact 8 chars", "abc12345", "abc12345"},
		{"Short hash", "abc", "abc"},
		{"Empty hash", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateHash(tt.hash)
			if result != tt.expected {
				t.Errorf("truncateHash(%q) = %q, want %q", tt.hash, result, tt.expected)
			}
		})
	}
}

func TestTreeRenderer_IndentStackManagement(t *testing.T) {
	renderer := NewTreeRenderer(output.ModeConcise)

	// Test empty stack
	if len(renderer.indentStack) != 0 {
		t.Error("Initial indentStack should be empty")
	}

	// Test building stack
	renderer.StartDirectory("level1", false)
	renderer.StartDirectory("level2", false)
	renderer.StartDirectory("level3", true)

	if len(renderer.indentStack) != 3 {
		t.Errorf("indentStack length = %d, want 3", len(renderer.indentStack))
	}

	// Verify stack values
	if renderer.indentStack[0] != true {
		t.Error("indentStack[0] should be true (not last)")
	}
	if renderer.indentStack[1] != true {
		t.Error("indentStack[1] should be true (not last)")
	}
	if renderer.indentStack[2] != false {
		t.Error("indentStack[2] should be false (is last)")
	}

	// Test unwinding stack
	renderer.EndDirectory()
	if len(renderer.indentStack) != 2 {
		t.Errorf("After EndDirectory, length = %d, want 2", len(renderer.indentStack))
	}

	renderer.EndDirectory()
	renderer.EndDirectory()
	if len(renderer.indentStack) != 0 {
		t.Error("After all EndDirectory calls, stack should be empty")
	}

	// Test EndDirectory on empty stack (should not panic)
	renderer.EndDirectory()
	if len(renderer.indentStack) != 0 {
		t.Error("EndDirectory on empty stack should keep it empty")
	}
}

func TestTreeRenderer_ColorOutput(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Test with color enabled
	renderer := NewTreeRenderer(output.ModeInteractive)
	renderer.useUnicode = false

	renderer.StartDirectory("test", true)
	renderer.RenderKeyValidation("key1", "abc123", "abc123", nil, false)
	renderer.RenderKeyValidation("key2", "def456", "xyz789", fmt.Errorf("mismatch"), true)

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Check for ANSI color codes
	if !strings.Contains(output, "\033[") {
		t.Error("Interactive mode output should contain ANSI color codes")
	}
	if !strings.Contains(output, "\033[32m") {
		t.Error("Success output should contain green color code")
	}
	if !strings.Contains(output, "\033[31m") {
		t.Error("Failure output should contain red color code")
	}
}
