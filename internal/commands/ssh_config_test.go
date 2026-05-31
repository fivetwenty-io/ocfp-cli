package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSSHConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		bastionIP     string
		blocName      string
		user          string
		keyPath       string
		internalHosts map[string]string
		checkContains []string
		checkAbsent   []string
	}{
		{
			name:          "BastionOnly",
			bastionIP:     "203.0.113.10",
			blocName:      "prod",
			user:          "ubuntu",
			keyPath:       "/home/user/.ocfp/prod/ssh/id_ed25519",
			internalHosts: map[string]string{},
			checkContains: []string{
				"Host prod-bastion",
				"HostName 203.0.113.10",
				"User ubuntu",
				"IdentityFile /home/user/.ocfp/prod/ssh/id_ed25519",
				"StrictHostKeyChecking no",
				"UserKnownHostsFile /dev/null",
				"LogLevel ERROR",
				"IdentitiesOnly yes",
			},
			checkAbsent: []string{
				"ProxyJump",
			},
		},
		{
			name:      "BastionWithInternalHosts",
			bastionIP: "203.0.113.10",
			blocName:  "prod",
			user:      "ubuntu",
			keyPath:   "/home/user/.ocfp/prod/ssh/id_ed25519",
			internalHosts: map[string]string{
				"bosh":  "10.0.0.5",
				"vault": "10.0.0.6",
			},
			checkContains: []string{
				"Host prod-bastion",
				"HostName 203.0.113.10",
				"Host prod-bosh",
				"HostName 10.0.0.5",
				"ProxyJump prod-bastion",
				"Host prod-vault",
				"HostName 10.0.0.6",
			},
		},
		{
			name:      "InternalHostsHaveProxyJump",
			bastionIP: "198.51.100.1",
			blocName:  "staging",
			user:      "admin",
			keyPath:   "/keys/id_rsa",
			internalHosts: map[string]string{
				"doomsday": "10.1.0.7",
			},
			checkContains: []string{
				"Host staging-bastion",
				"Host staging-doomsday",
				"HostName 10.1.0.7",
				"ProxyJump staging-bastion",
				"User admin",
				"IdentityFile /keys/id_rsa",
			},
		},
		{
			name:          "BastionBlockHasNoProxyJump",
			bastionIP:     "198.51.100.1",
			blocName:      "dev",
			user:          "ubuntu",
			keyPath:       "/keys/id_ed25519",
			internalHosts: map[string]string{},
			checkAbsent: []string{
				"ProxyJump",
			},
		},
		{
			name:      "MultipleInternalHosts",
			bastionIP: "203.0.113.50",
			blocName:  "lab",
			user:      "ubuntu",
			keyPath:   "/keys/id_ed25519",
			internalHosts: map[string]string{
				"bosh":       "10.0.0.5",
				"vault":      "10.0.0.6",
				"doomsday":   "10.0.0.7",
				"prometheus": "10.0.0.8",
				"shield":     "10.0.0.9",
			},
			checkContains: []string{
				"Host lab-bastion",
				"Host lab-bosh",
				"Host lab-vault",
				"Host lab-doomsday",
				"Host lab-prometheus",
				"Host lab-shield",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := generateSSHConfig(tt.bastionIP, tt.blocName, tt.user, tt.keyPath, tt.internalHosts)
			require.NotEmpty(t, result)

			for _, expected := range tt.checkContains {
				assert.Contains(t, result, expected, "Expected config to contain: %s", expected)
			}

			for _, absent := range tt.checkAbsent {
				assert.NotContains(t, result, absent, "Expected config to NOT contain: %s", absent)
			}
		})
	}
}

func TestGenerateSSHConfigStructure(t *testing.T) {
	t.Parallel()

	result := generateSSHConfig(
		"203.0.113.10",
		"prod",
		"ubuntu",
		"/keys/id_ed25519",
		map[string]string{"bosh": "10.0.0.5"},
	)

	blocks := strings.Split(strings.TrimSpace(result), "\n\n")
	require.Len(t, blocks, 2, "Expected 2 Host blocks (bastion + bosh)")

	// First block should be bastion
	assert.True(t, strings.HasPrefix(blocks[0], "Host prod-bastion"), "First block should be bastion")

	// Second block should be internal host
	assert.True(t, strings.HasPrefix(blocks[1], "Host prod-bosh"), "Second block should be bosh")
}

func TestExtractInternalHosts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		outputs  map[string]interface{}
		blocName string
		expected map[string]string
	}{
		{
			name: "ExtractsBoshAndVault",
			outputs: map[string]interface{}{
				"reserved_prod-ocfp-0_bosh_ip":  "10.0.0.5",
				"reserved_prod-ocfp-0_vault_ip": "10.0.0.6",
				"bastion_public_ip":             "203.0.113.10",
			},
			blocName: "prod",
			expected: map[string]string{
				"bosh":  "10.0.0.5",
				"vault": "10.0.0.6",
			},
		},
		{
			name: "SkipsBastionAndAvailableEntries",
			outputs: map[string]interface{}{
				"reserved_prod-ocfp-0_bosh_ip":    "10.0.0.5",
				"bastion_public_ip":               "203.0.113.10",
				"available_prod-ocfp-0_ip":        "10.0.0.99",
				"reserved_prod-ocfp-0_bastion_ip": "10.0.0.1",
			},
			blocName: "prod",
			expected: map[string]string{
				"bosh": "10.0.0.5",
			},
		},
		{
			name:     "EmptyOutputs",
			outputs:  map[string]interface{}{},
			blocName: "prod",
			expected: map[string]string{},
		},
		{
			name: "NonStringValuesSkipped",
			outputs: map[string]interface{}{
				"reserved_prod-ocfp-0_bosh_ip": 12345,
			},
			blocName: "prod",
			expected: map[string]string{},
		},
		{
			name: "MultipleSlotsSameComponent",
			outputs: map[string]interface{}{
				"reserved_prod-ocfp-0_bosh_ip": "10.0.0.5",
				"reserved_prod-ocfp-1_bosh_ip": "10.0.0.15",
			},
			blocName: "prod",
			expected: map[string]string{
				"bosh": "10.0.0.5",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := extractInternalHosts(tt.outputs, tt.blocName)
			assert.Equal(t, tt.expected, result)
		})
	}
}
