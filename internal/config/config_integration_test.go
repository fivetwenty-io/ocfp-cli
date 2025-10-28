package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveConfigOmitsEmptyValues is an integration test that verifies
// SaveConfig omits empty values when writing to disk.
func TestSaveConfigOmitsEmptyValues(t *testing.T) {
	// Create a temporary directory for test config
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yml")

	// Create a config with some values set and many empty
	testConfig := &Config{
		Provider:        "aws",
		Region:          "us-east-1",
		AccessKeyID:     "AKIA123456789",
		SecretAccessKey: "secretkey123",
		VPCCIDRBlock:    "10.5.0.0/23",
		// All other fields intentionally left empty/zero
	}

	// Save the config
	err := SaveConfig(configPath, "test-bloc", testConfig)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Read the saved file
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read saved config: %v", err)
	}

	output := string(data)
	t.Logf("Saved config file:\n%s", output)

	// Verify non-empty values are present
	expectedPresent := []string{
		"provider: aws",
		"region: us-east-1",
		"access_key_id: AKIA123456789",
		"secret_access_key: secretkey123",
		"vpc_cidr_block: 10.5.0.0/23",
	}
	for _, expected := range expectedPresent {
		if !strings.Contains(output, expected) {
			t.Errorf("Expected saved config to contain %q", expected)
		}
	}

	// Verify empty values are NOT present
	emptyFieldsToOmit := []string{
		"name: \"\"",
		"iaas: \"\"",
		"project_id: \"\"",
		"org_id: \"\"",
		"auth_token: \"\"",
		"service_account_token: \"\"",
		"service_account_json: \"\"",
		"username: \"\"",
		"password: \"\"",
		"project_name: \"\"",
		"domain_name: \"\"",
		"session_token: \"\"",
		"bastion_ip: \"\"",
	}
	for _, omitted := range emptyFieldsToOmit {
		if strings.Contains(output, omitted) {
			t.Errorf("Expected saved config to omit %q, but it's present", omitted)
		}
	}

	// Verify empty nested structs are omitted
	emptyStructsToOmit := []string{
		"network:",
		"bastion:",
		"genesis:",
	}
	for _, omitted := range emptyStructsToOmit {
		if strings.Contains(output, omitted+"\n  ") ||
			strings.Contains(output, omitted+" {}") ||
			(strings.Contains(output, omitted) && strings.Contains(output, "id: \"\"")) {
			t.Errorf("Expected saved config to omit empty %q section, but it's present", omitted)
		}
	}
}

// TestSaveConfigPreservesNonEmptyNestedStructs verifies that nested structs
// with actual values are saved correctly.
func TestSaveConfigPreservesNonEmptyNestedStructs(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yml")

	testConfig := &Config{
		Provider: "aws",
		Region:   "us-east-1",
		Bastion: Bastion{
			Flavor: "t3.large",
			Image:  "ubuntu-22.04",
		},
	}

	err := SaveConfig(configPath, "test-bloc", testConfig)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read saved config: %v", err)
	}

	output := string(data)
	t.Logf("Saved config file:\n%s", output)

	// Bastion should be present with its values
	if !strings.Contains(output, "bastion:") {
		t.Error("Expected bastion section to be present")
	}
	if !strings.Contains(output, "flavor: t3.large") {
		t.Error("Expected bastion flavor to be present")
	}
	if !strings.Contains(output, "image: ubuntu-22.04") {
		t.Error("Expected bastion image to be present")
	}

	// Empty bastion fields should still be omitted
	if strings.Contains(output, "os: \"\"") {
		t.Error("Expected empty bastion.os to be omitted")
	}
}

// TestRoundTripConfig verifies that loading and saving a config preserves data.
func TestRoundTripConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yml")

	original := &Config{
		Provider:        "aws",
		Region:          "us-east-1",
		AccessKeyID:     "AKIA123",
		SecretAccessKey: "secret",
		VPCCIDRBlock:    "10.5.0.0/23",
		DNS:             []string{"8.8.8.8"},
		RouterPublicIPs: 2,
	}

	// Save
	err := SaveConfig(configPath, "test-bloc", original)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Load
	testBlocConfig, err := LoadWithParams(configPath, "test-bloc")
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify values match
	if testBlocConfig.Provider != original.Provider {
		t.Errorf("Provider mismatch: got %q, want %q", testBlocConfig.Provider, original.Provider)
	}
	if testBlocConfig.Region != original.Region {
		t.Errorf("Region mismatch: got %q, want %q", testBlocConfig.Region, original.Region)
	}
	if testBlocConfig.AccessKeyID != original.AccessKeyID {
		t.Errorf("AccessKeyID mismatch: got %q, want %q", testBlocConfig.AccessKeyID, original.AccessKeyID)
	}
	if testBlocConfig.RouterPublicIPs != original.RouterPublicIPs {
		t.Errorf("RouterPublicIPs mismatch: got %d, want %d", testBlocConfig.RouterPublicIPs, original.RouterPublicIPs)
	}
	if len(testBlocConfig.DNS) != len(original.DNS) {
		t.Errorf("DNS length mismatch: got %d, want %d", len(testBlocConfig.DNS), len(original.DNS))
	}
}
