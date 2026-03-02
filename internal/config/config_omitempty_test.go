package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestConfigOmitEmpty verifies that empty/default values are omitted from YAML output.
func TestConfigOmitEmpty(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		contains []string // Fields that should be in output
		omits    []string // Fields that should NOT be in output
	}{
		{
			name: "only non-empty values written",
			config: Config{
				Provider:        "aws",
				AccessKeyID:     "AKIATEST123",
				SecretAccessKey: "secretkey123",
				Region:          "us-east-1",
				VPCCIDRBlock:    "10.5.0.0/23",
				// All other fields left empty/zero
			},
			contains: []string{
				"provider: aws",
				"access_key_id: AKIATEST123",
				"secret_access_key: secretkey123",
				"region: us-east-1",
				"vpc_cidr_block: 10.5.0.0/23",
			},
			omits: []string{
				"name: \"\"",
				"iaas: \"\"",
				"project_id: \"\"",
				"org_id: \"\"",
				"auth_token: \"\"",
				"service_account_token: \"\"",
				"username: \"\"",
				"password: \"\"",
			},
		},
		{
			name: "empty nested structs omitted",
			config: Config{
				Provider: "aws",
				Region:   "us-east-1",
				// Network, Bastion, Genesis left as empty structs
			},
			contains: []string{
				"provider: aws",
				"region: us-east-1",
			},
			omits: []string{
				"network:",
				"bastion:",
				"genesis:",
			},
		},
		{
			name: "zero integer values omitted",
			config: Config{
				Provider:           "aws",
				RouterPublicIPs:    0,
				CFSSHPublicIPs:     0,
				JumpboxPublicIPs:   0,
				TCPRouterPublicIPs: 0,
			},
			contains: []string{
				"provider: aws",
			},
			omits: []string{
				"router_public_ips:",
				"cf_ssh_public_ips:",
				"jumpbox_public_ips:",
				"tcp_router_public_ips:",
			},
		},
		{
			name: "non-zero values preserved",
			config: Config{
				Provider:         "aws",
				Region:           "us-east-1",
				RouterPublicIPs:  2,
				JumpboxPublicIPs: 1,
			},
			contains: []string{
				"provider: aws",
				"region: us-east-1",
				"router_public_ips: 2",
				"jumpbox_public_ips: 1",
			},
			omits: []string{
				"name: \"\"",
				"iaas: \"\"",
			},
		},
		{
			name: "empty slices and maps omitted",
			config: Config{
				Provider: "aws",
				DNS:      []string{},          // Empty slice
				AZs:      nil,                 // Nil map
				Keys:     map[string]string{}, // Empty map
			},
			contains: []string{
				"provider: aws",
			},
			omits: []string{
				"dns:",
				"azs:",
				"keys:",
			},
		},
		{
			name: "populated slices and maps preserved",
			config: Config{
				Provider: "aws",
				DNS:      []string{"8.8.8.8", "8.8.4.4"},
				Keys:     map[string]string{"test": "value"},
			},
			contains: []string{
				"provider: aws",
				"dns:",
				"- 8.8.8.8",
				"- 8.8.4.4",
				"keys:",
				"test: value",
			},
			omits: []string{
				"name: \"\"",
				"network:",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal config to YAML
			data, err := yaml.Marshal(&tt.config)
			if err != nil {
				t.Fatalf("Failed to marshal config: %v", err)
			}

			output := string(data)
			t.Logf("YAML output:\n%s", output)

			// Check that expected fields are present
			for _, expected := range tt.contains {
				if !containsString(output, expected) {
					t.Errorf("Expected output to contain %q, but it doesn't", expected)
				}
			}

			// Check that unwanted fields are omitted
			for _, omitted := range tt.omits {
				if containsString(output, omitted) {
					t.Errorf("Expected output to omit %q, but it's present", omitted)
				}
			}
		})
	}
}

// TestNestedStructOmitEmpty verifies nested structs also omit empty values.
func TestNestedStructOmitEmpty(t *testing.T) {
	config := Config{
		Provider: "aws",
		Region:   "us-east-1",
		Network: NetworkConfig{
			ID:   "",            // Empty - should be omitted
			Name: "",            // Empty - should be omitted
			CIDR: "10.0.0.0/16", // Non-empty - should be present
		},
		Bastion: Bastion{
			Flavor: "t3.large", // Non-empty
			// Other fields empty
		},
	}

	data, err := yaml.Marshal(&config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	output := string(data)
	t.Logf("YAML output:\n%s", output)

	// Network should be present (has non-empty CIDR)
	if !containsString(output, "network:") {
		t.Error("Expected network section to be present")
	}
	if !containsString(output, "cidr: 10.0.0.0/16") {
		t.Error("Expected network CIDR to be present")
	}
	// Empty network fields should be omitted
	if containsString(output, "id: \"\"") {
		t.Error("Expected empty network ID to be omitted")
	}

	// Bastion should be present (has non-empty flavor)
	if !containsString(output, "bastion:") {
		t.Error("Expected bastion section to be present")
	}
	if !containsString(output, "flavor: t3.large") {
		t.Error("Expected bastion flavor to be present")
	}
}

// TestBooleanOmitEmpty verifies boolean false values are omitted correctly.
func TestBooleanOmitEmpty(t *testing.T) {
	config := Config{
		Provider: "aws",
		Genesis: Genesis{
			Enabled: false, // Should be omitted
			Repo:    "",    // Should be omitted
		},
		Blobstore: BlobstoreConfig{
			EnablePolicies: false, // Should be omitted
		},
	}

	data, err := yaml.Marshal(&config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	output := string(data)
	t.Logf("YAML output:\n%s", output)

	// Genesis and Blobstore should be omitted entirely (all fields empty/false)
	if containsString(output, "genesis:") {
		t.Error("Expected genesis section to be omitted when all fields are empty/false")
	}
	if containsString(output, "blobstore:") {
		t.Error("Expected blobstore section to be omitted when all fields are empty/false")
	}
}

// Helper function to check if a string contains a substring.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || containsString(s[1:], substr)))
}
