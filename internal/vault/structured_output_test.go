package vault

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/output"
	"github.com/goccy/go-yaml"
)

func TestNewStructuredOutputWriter(t *testing.T) {
	tests := []struct {
		name string
		mode output.Mode
	}{
		{"JSON mode", output.ModeJSON},
		{"YAML mode", output.ModeYAML},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := NewStructuredOutputWriter(tt.mode)
			if writer == nil {
				t.Fatal("NewStructuredOutputWriter returned nil")
			}
			if writer.mode != tt.mode {
				t.Errorf("mode = %v, want %v", writer.mode, tt.mode)
			}
		})
	}
}

func TestStructuredOutputWriter_WriteValidation_JSON(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	writer := NewStructuredOutputWriter(output.ModeJSON)

	entry := ValidationEntry{
		Path:               "secret/config/bosh",
		Key:                "admin_password",
		FullPath:           "secret/config/bosh:admin_password",
		Depth:              2,
		ParentPath:         "secret/config",
		IsLastSibling:      true,
		InceptionChecksum:  "abc123def456",
		ProductionChecksum: "abc123def456",
		Status:             "ok",
	}

	err := writer.WriteValidation(entry)
	if err != nil {
		t.Fatalf("WriteValidation failed: %v", err)
	}

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	io.Copy(&buf, r)
	jsonOutput := buf.String()

	// Parse JSON to verify structure
	var parsed ValidationEntry
	if err := json.Unmarshal([]byte(jsonOutput), &parsed); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, jsonOutput)
	}

	// Verify fields
	if parsed.Path != entry.Path {
		t.Errorf("Path = %q, want %q", parsed.Path, entry.Path)
	}
	if parsed.Key != entry.Key {
		t.Errorf("Key = %q, want %q", parsed.Key, entry.Key)
	}
	if parsed.FullPath != entry.FullPath {
		t.Errorf("FullPath = %q, want %q", parsed.FullPath, entry.FullPath)
	}
	if parsed.Depth != entry.Depth {
		t.Errorf("Depth = %d, want %d", parsed.Depth, entry.Depth)
	}
	if parsed.Status != entry.Status {
		t.Errorf("Status = %q, want %q", parsed.Status, entry.Status)
	}
	if parsed.InceptionChecksum != entry.InceptionChecksum {
		t.Errorf("InceptionChecksum = %q, want %q", parsed.InceptionChecksum, entry.InceptionChecksum)
	}
	if parsed.ProductionChecksum != entry.ProductionChecksum {
		t.Errorf("ProductionChecksum = %q, want %q", parsed.ProductionChecksum, entry.ProductionChecksum)
	}

	// Verify timestamp was set
	if parsed.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
}

func TestStructuredOutputWriter_WriteValidation_YAML(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	writer := NewStructuredOutputWriter(output.ModeYAML)

	entry := ValidationEntry{
		Path:               "secret/database/postgres",
		Key:                "password",
		FullPath:           "secret/database/postgres:password",
		Depth:              2,
		ParentPath:         "secret/database",
		IsLastSibling:      false,
		InceptionChecksum:  "xyz789abc123",
		ProductionChecksum: "xyz789abc123",
		Status:             "ok",
	}

	err := writer.WriteValidation(entry)
	if err != nil {
		t.Fatalf("WriteValidation failed: %v", err)
	}

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	io.Copy(&buf, r)
	yamlOutput := buf.String()

	// Parse YAML to verify structure
	var parsed ValidationEntry
	if err := yaml.Unmarshal([]byte(yamlOutput), &parsed); err != nil {
		t.Fatalf("Failed to parse YAML output: %v\nOutput: %s", err, yamlOutput)
	}

	// Verify fields
	if parsed.Path != entry.Path {
		t.Errorf("Path = %q, want %q", parsed.Path, entry.Path)
	}
	if parsed.Key != entry.Key {
		t.Errorf("Key = %q, want %q", parsed.Key, entry.Key)
	}
	if parsed.Status != entry.Status {
		t.Errorf("Status = %q, want %q", parsed.Status, entry.Status)
	}
}

func TestStructuredOutputWriter_WriteValidation_WithError(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	writer := NewStructuredOutputWriter(output.ModeJSON)

	entry := ValidationEntry{
		Path:               "secret/config/app",
		Key:                "api_key",
		FullPath:           "secret/config/app:api_key",
		Depth:              2,
		ParentPath:         "secret/config",
		IsLastSibling:      true,
		InceptionChecksum:  "abc123",
		ProductionChecksum: "xyz789",
		Status:             "mismatch",
		ErrorMessage:       "checksum mismatch",
	}

	err := writer.WriteValidation(entry)
	if err != nil {
		t.Fatalf("WriteValidation failed: %v", err)
	}

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	io.Copy(&buf, r)
	jsonOutput := buf.String()

	// Parse JSON
	var parsed ValidationEntry
	if err := json.Unmarshal([]byte(jsonOutput), &parsed); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	// Verify error fields
	if parsed.Status != "mismatch" {
		t.Errorf("Status = %q, want 'mismatch'", parsed.Status)
	}
	if parsed.ErrorMessage != entry.ErrorMessage {
		t.Errorf("ErrorMessage = %q, want %q", parsed.ErrorMessage, entry.ErrorMessage)
	}
	if parsed.InceptionChecksum != "abc123" {
		t.Errorf("InceptionChecksum = %q, want 'abc123'", parsed.InceptionChecksum)
	}
	if parsed.ProductionChecksum != "xyz789" {
		t.Errorf("ProductionChecksum = %q, want 'xyz789'", parsed.ProductionChecksum)
	}
}

func TestStructuredOutputWriter_MultipleEntries(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	writer := NewStructuredOutputWriter(output.ModeJSON)

	entries := []ValidationEntry{
		{
			Path:               "secret/config/db",
			Key:                "username",
			FullPath:           "secret/config/db:username",
			Depth:              2,
			InceptionChecksum:  "aaa111",
			ProductionChecksum: "aaa111",
			Status:             "ok",
		},
		{
			Path:               "secret/config/db",
			Key:                "password",
			FullPath:           "secret/config/db:password",
			Depth:              2,
			InceptionChecksum:  "bbb222",
			ProductionChecksum: "bbb222",
			Status:             "ok",
		},
		{
			Path:               "secret/config/app",
			Key:                "token",
			FullPath:           "secret/config/app:token",
			Depth:              2,
			InceptionChecksum:  "ccc333",
			ProductionChecksum: "ddd444",
			Status:             "mismatch",
			ErrorMessage:       "checksum mismatch",
		},
	}

	for _, entry := range entries {
		if err := writer.WriteValidation(entry); err != nil {
			t.Fatalf("WriteValidation failed: %v", err)
		}
	}

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	io.Copy(&buf, r)
	jsonOutput := buf.String()

	// Split by newlines (each entry is on its own line)
	lines := strings.Split(strings.TrimSpace(jsonOutput), "\n")
	if len(lines) != len(entries) {
		t.Errorf("Got %d lines, want %d", len(lines), len(entries))
	}

	// Verify each line is valid JSON
	for i, line := range lines {
		var parsed ValidationEntry
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Errorf("Line %d: failed to parse JSON: %v\nLine: %s", i, err, line)
		}
	}
}

func TestValidationEntry_TimestampHandling(t *testing.T) {
	entry := ValidationEntry{
		Path:   "secret/test",
		Key:    "key1",
		Status: "ok",
	}

	// Timestamp should initially be zero
	if !entry.Timestamp.IsZero() {
		t.Error("Initial timestamp should be zero")
	}

	// Set timestamp
	now := time.Now()
	entry.Timestamp = now

	// Marshal to JSON
	jsonData, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Unmarshal and verify timestamp is preserved
	var parsed ValidationEntry
	if err := json.Unmarshal(jsonData, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Compare timestamps (allowing for small differences due to precision)
	if parsed.Timestamp.Sub(now).Abs() > time.Second {
		t.Errorf("Timestamp difference too large: %v vs %v", parsed.Timestamp, now)
	}
}

func TestValidationEntry_AllStatuses(t *testing.T) {
	statuses := []string{"ok", "mismatch", "error"}

	for _, status := range statuses {
		t.Run("Status_"+status, func(t *testing.T) {
			entry := ValidationEntry{
				Path:   "secret/test",
				Key:    "key",
				Status: status,
			}

			// Marshal to JSON
			jsonData, err := json.Marshal(entry)
			if err != nil {
				t.Fatalf("Failed to marshal: %v", err)
			}

			// Verify status in JSON
			if !strings.Contains(string(jsonData), `"`+status+`"`) {
				t.Errorf("JSON should contain status %q", status)
			}

			// Unmarshal and verify
			var parsed ValidationEntry
			if err := json.Unmarshal(jsonData, &parsed); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}

			if parsed.Status != status {
				t.Errorf("Status = %q, want %q", parsed.Status, status)
			}
		})
	}
}

func TestValidationEntry_OmitEmptyErrorMessage(t *testing.T) {
	// Entry without error
	entryOK := ValidationEntry{
		Path:         "secret/test",
		Key:          "key",
		Status:       "ok",
		ErrorMessage: "", // Empty error message
	}

	jsonData, err := json.Marshal(entryOK)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Verify error_message is not in JSON when empty
	if strings.Contains(string(jsonData), "error_message") {
		t.Error("JSON should not contain 'error_message' field when empty")
	}

	// Entry with error
	entryError := ValidationEntry{
		Path:         "secret/test",
		Key:          "key",
		Status:       "error",
		ErrorMessage: "some error",
	}

	jsonData, err = json.Marshal(entryError)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Verify error_message is in JSON when present
	if !strings.Contains(string(jsonData), "error_message") {
		t.Error("JSON should contain 'error_message' field when set")
	}
	if !strings.Contains(string(jsonData), "some error") {
		t.Error("JSON should contain the error message text")
	}
}

func TestValidationEntry_HierarchyMetadata(t *testing.T) {
	entry := ValidationEntry{
		Path:          "secret/a/b/c",
		Key:           "key",
		FullPath:      "secret/a/b/c:key",
		Depth:         3,
		ParentPath:    "secret/a/b",
		IsLastSibling: true,
	}

	jsonData, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed ValidationEntry
	if err := json.Unmarshal(jsonData, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify hierarchy metadata
	if parsed.Depth != 3 {
		t.Errorf("Depth = %d, want 3", parsed.Depth)
	}
	if parsed.ParentPath != "secret/a/b" {
		t.Errorf("ParentPath = %q, want 'secret/a/b'", parsed.ParentPath)
	}
	if !parsed.IsLastSibling {
		t.Error("IsLastSibling should be true")
	}
}

func TestValidationEntry_YAMLFormat(t *testing.T) {
	entry := ValidationEntry{
		Timestamp:          time.Now(),
		Path:               "secret/config",
		Key:                "key1",
		FullPath:           "secret/config:key1",
		Depth:              1,
		ParentPath:         "secret",
		IsLastSibling:      true,
		InceptionChecksum:  "abc",
		ProductionChecksum: "abc",
		Status:             "ok",
	}

	yamlData, err := yaml.Marshal(entry)
	if err != nil {
		t.Fatalf("Failed to marshal to YAML: %v", err)
	}

	yamlStr := string(yamlData)

	// Verify YAML contains expected fields
	expectedFields := []string{
		"timestamp:",
		"path:",
		"key:",
		"full_path:",
		"depth:",
		"parent_path:",
		"is_last_sibling:",
		"inception_checksum:",
		"production_checksum:",
		"status:",
	}

	for _, field := range expectedFields {
		if !strings.Contains(yamlStr, field) {
			t.Errorf("YAML should contain field %q", field)
		}
	}

	// Unmarshal and verify
	var parsed ValidationEntry
	if err := yaml.Unmarshal(yamlData, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal YAML: %v", err)
	}

	if parsed.Status != entry.Status {
		t.Errorf("Status = %q, want %q", parsed.Status, entry.Status)
	}
}
