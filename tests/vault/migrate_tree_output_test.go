package vault_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/output"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
)

// TestVaultMigrateTreeOutput_Integration is an integration test for tree-based output.
// Note: This requires a working vault setup and is skipped in CI unless VAULT_INTEGRATION_TEST is set.
func TestVaultMigrateTreeOutput_Integration(t *testing.T) {
	if os.Getenv("VAULT_INTEGRATION_TEST") == "" {
		t.Skip("Skipping integration test. Set VAULT_INTEGRATION_TEST=1 to run.")
	}

	// This test would require:
	// 1. Setting up inception vault
	// 2. Setting up production vault
	// 3. Populating inception with test data
	// 4. Running migration
	// 5. Capturing output
	// 6. Verifying tree structure

	// TODO: Implement full integration test when vault test infrastructure is available
	t.Log("Integration test placeholder - requires vault test infrastructure")
}

// TestTreeRenderer_OutputFormats tests different output formats work correctly.
func TestTreeRenderer_OutputFormats(t *testing.T) {
	tests := []struct {
		name           string
		mode           output.Mode
		expectUnicode  bool
		expectColor    bool
		verifyFunction func(t *testing.T, output string)
	}{
		{
			name:          "Interactive mode",
			mode:          output.ModeInteractive,
			expectUnicode: true,
			expectColor:   true,
			verifyFunction: func(t *testing.T, out string) {
				if !strings.Contains(out, "\033[") {
					t.Error("Interactive output should contain color codes")
				}
			},
		},
		{
			name:          "Concise mode",
			mode:          output.ModeConcise,
			expectUnicode: true,
			expectColor:   false,
			verifyFunction: func(t *testing.T, out string) {
				if strings.Contains(out, "\033[") {
					t.Error("Concise output should not contain color codes")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			renderer := vault.NewTreeRenderer(tt.mode)

			// Render a simple tree structure
			renderer.StartDirectory("config", false)
			renderer.RenderKeyValidation("password", "abc123", "abc123", nil, false)
			renderer.RenderKeyValidation("token", "def456", "def456", nil, true)
			renderer.EndDirectory()

			// Restore stdout and capture output
			w.Close()
			os.Stdout = oldStdout
			var buf bytes.Buffer
			buf.ReadFrom(r)
			output := buf.String()

			// Run verification function
			if tt.verifyFunction != nil {
				tt.verifyFunction(t, output)
			}

			// Verify basic structure
			if !strings.Contains(output, "config/") {
				t.Error("Output should contain directory name")
			}
			if !strings.Contains(output, ":password") {
				t.Error("Output should contain key names")
			}
		})
	}
}

// TestStructuredOutput_JSONFormat tests JSON output format.
func TestStructuredOutput_JSONFormat(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	writer := vault.NewStructuredOutputWriter(output.ModeJSON)

	// Write multiple entries
	entries := []vault.ValidationEntry{
		{
			Path:               "secret/config/db",
			Key:                "username",
			FullPath:           "secret/config/db:username",
			Depth:              2,
			ParentPath:         "secret/config",
			IsLastSibling:      false,
			InceptionChecksum:  "abc123",
			ProductionChecksum: "abc123",
			Status:             "ok",
		},
		{
			Path:               "secret/config/db",
			Key:                "password",
			FullPath:           "secret/config/db:password",
			Depth:              2,
			ParentPath:         "secret/config",
			IsLastSibling:      true,
			InceptionChecksum:  "def456",
			ProductionChecksum: "xyz789",
			Status:             "mismatch",
			ErrorMessage:       "checksum mismatch",
		},
	}

	for _, entry := range entries {
		if err := writer.WriteValidation(entry); err != nil {
			t.Fatalf("WriteValidation failed: %v", err)
		}
	}

	// Restore stdout and capture output
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	buf.ReadFrom(r)
	jsonOutput := buf.String()

	// Parse each line as JSON
	lines := strings.Split(strings.TrimSpace(jsonOutput), "\n")
	if len(lines) != 2 {
		t.Fatalf("Expected 2 JSON lines, got %d", len(lines))
	}

	// Verify first entry
	var entry1 vault.ValidationEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry1); err != nil {
		t.Fatalf("Failed to parse first JSON entry: %v", err)
	}
	if entry1.Status != "ok" {
		t.Errorf("First entry status = %q, want 'ok'", entry1.Status)
	}
	if entry1.Key != "username" {
		t.Errorf("First entry key = %q, want 'username'", entry1.Key)
	}

	// Verify second entry
	var entry2 vault.ValidationEntry
	if err := json.Unmarshal([]byte(lines[1]), &entry2); err != nil {
		t.Fatalf("Failed to parse second JSON entry: %v", err)
	}
	if entry2.Status != "mismatch" {
		t.Errorf("Second entry status = %q, want 'mismatch'", entry2.Status)
	}
	if entry2.ErrorMessage != "checksum mismatch" {
		t.Errorf("Second entry error = %q, want 'checksum mismatch'", entry2.ErrorMessage)
	}
}

// TestStructuredOutput_YAMLFormat tests YAML output format.
func TestStructuredOutput_YAMLFormat(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	writer := vault.NewStructuredOutputWriter(output.ModeYAML)

	entry := vault.ValidationEntry{
		Path:               "secret/app",
		Key:                "api_key",
		FullPath:           "secret/app:api_key",
		Depth:              1,
		ParentPath:         "secret",
		IsLastSibling:      true,
		InceptionChecksum:  "aaa111",
		ProductionChecksum: "aaa111",
		Status:             "ok",
	}

	if err := writer.WriteValidation(entry); err != nil {
		t.Fatalf("WriteValidation failed: %v", err)
	}

	// Restore stdout and capture output
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	buf.ReadFrom(r)
	yamlOutput := buf.String()

	// Parse YAML
	var parsed vault.ValidationEntry
	if err := yaml.Unmarshal([]byte(yamlOutput), &parsed); err != nil {
		t.Fatalf("Failed to parse YAML: %v", err)
	}

	// Verify parsed data
	if parsed.Path != entry.Path {
		t.Errorf("Path = %q, want %q", parsed.Path, entry.Path)
	}
	if parsed.Key != entry.Key {
		t.Errorf("Key = %q, want %q", parsed.Key, entry.Key)
	}
	if parsed.Status != "ok" {
		t.Errorf("Status = %q, want 'ok'", parsed.Status)
	}

	// Verify YAML format
	if !strings.Contains(yamlOutput, "timestamp:") {
		t.Error("YAML should contain timestamp field")
	}
	if !strings.Contains(yamlOutput, "path:") {
		t.Error("YAML should contain path field")
	}
}

// TestTreeRenderer_FailureSummary tests the failure summary output.
func TestTreeRenderer_FailureSummary(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	renderer := vault.NewTreeRenderer(output.ModeConcise)

	// Add some failures
	renderer.RenderKeyValidation("key1", "abc", "xyz",
		&ValidationError{Message: "checksum mismatch"}, false)
	renderer.RenderKeyValidation("key2", "def", "",
		&ValidationError{Message: "production vault unreachable"}, false)
	renderer.RenderKeyValidation("key3", "ghi", "ghi", nil, true) // Success

	// Render failure summary
	renderer.RenderFailureSummary()

	// Restore stdout and capture output
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	// Verify summary appears
	if !strings.Contains(output, "Validation Failures") {
		t.Error("Output should contain failure summary header")
	}

	// Extract only the failure summary section (after "Validation Failures")
	summaryIdx := strings.Index(output, "Validation Failures")
	summarySection := output[summaryIdx:]

	// Verify failed keys are listed in summary
	if !strings.Contains(summarySection, "key1") {
		t.Error("Summary should list key1")
	}
	if !strings.Contains(summarySection, "key2") {
		t.Error("Summary should list key2")
	}

	// Verify successful key is not in failure summary
	if strings.Contains(summarySection, "key3") {
		t.Error("Summary should not list successful key3")
	}

	// Verify error messages
	if !strings.Contains(summarySection, "checksum mismatch") {
		t.Error("Summary should contain error message")
	}
}

// ValidationError is a simple error type for testing.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

// TestTreeRenderer_UnicodeVsASCII tests that tree output contains proper tree characters.
func TestTreeRenderer_UnicodeVsASCII(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	renderer := vault.NewTreeRenderer(output.ModeConcise)

	renderer.StartDirectory("dir1", false)
	renderer.StartDirectory("dir2", true)
	renderer.RenderKeyValidation("key", "abc", "abc", nil, true)
	renderer.EndDirectory()
	renderer.EndDirectory()

	// Restore stdout and capture output
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	buf.ReadFrom(r)
	outputStr := buf.String()

	// Verify tree structure characters are present (either Unicode or ASCII)
	hasTreeChars := strings.Contains(outputStr, "├") ||
		strings.Contains(outputStr, "└") ||
		strings.Contains(outputStr, "│") ||
		strings.Contains(outputStr, "|-") ||
		strings.Contains(outputStr, "\\-") ||
		strings.Contains(outputStr, "|")

	if !hasTreeChars {
		t.Errorf("Output should contain tree structure characters\nGot: %s", outputStr)
	}
}

// TestTreeRenderer_HierarchicalStructure tests proper tree hierarchy.
func TestTreeRenderer_HierarchicalStructure(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	renderer := vault.NewTreeRenderer(output.ModeConcise)

	// Build a 3-level hierarchy
	renderer.StartDirectory("level1", false)
	renderer.StartDirectory("level2", false)
	renderer.StartDirectory("level3", true)
	renderer.RenderKeyValidation("deep_key", "abc", "abc", nil, true)
	renderer.EndDirectory()
	renderer.EndDirectory()
	renderer.EndDirectory()

	// Restore stdout and capture output
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	buf.ReadFrom(r)
	outputStr := buf.String()

	// Verify hierarchical structure
	lines := strings.Split(outputStr, "\n")

	// Should have proper nesting indicated by indentation/tree characters
	foundLevel1 := false
	foundLevel2 := false
	foundLevel3 := false
	foundKey := false

	for _, line := range lines {
		if strings.Contains(line, "level1/") {
			foundLevel1 = true
		}
		if strings.Contains(line, "level2/") {
			foundLevel2 = true
		}
		if strings.Contains(line, "level3/") {
			foundLevel3 = true
		}
		if strings.Contains(line, ":deep_key") {
			foundKey = true
		}
	}

	if !foundLevel1 || !foundLevel2 || !foundLevel3 || !foundKey {
		t.Errorf("Not all hierarchy levels found in output. Got:\n%s", outputStr)
	}
}
