package commands

import (
	"os"
	"os/exec"
	"path/filepath"
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
				"StrictHostKeyChecking accept-new",
				"LogLevel ERROR",
				"IdentitiesOnly yes",
			},
			checkAbsent: []string{
				"ProxyJump",
				"StrictHostKeyChecking no",
				"UserKnownHostsFile",
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

			result := sshConfigHostBlocks(t, generateSSHConfig(tt.bastionIP, tt.blocName, tt.user, tt.keyPath, tt.internalHosts))
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
	require.Len(t, blocks, 3, "Expected the header comment and 2 Host blocks (bastion + bosh)")

	assert.True(t, strings.HasPrefix(blocks[0], "# "), "First part should be the header comment")

	// Then the bastion block
	assert.True(t, strings.HasPrefix(blocks[1], "Host prod-bastion"), "Second part should be bastion")

	// Then the internal host
	assert.True(t, strings.HasPrefix(blocks[2], "Host prod-bosh"), "Third part should be bosh")
}

// ssh keeps the first value it finds for an option, so an earlier "Host *"
// block that turns host key checking off wins over the bastion block. The
// output says where the blocks have to go, and so does the help.
func TestSSHConfigSaysTheBlocksGoBeforeHostStar(t *testing.T) {
	t.Parallel()

	result := generateSSHConfig("203.0.113.10", "prod", "ubuntu", "", nil)
	header := strings.Split(result, "\n\n")[0]

	for _, want := range []string{`"Host *"`, "Include ~/.ssh/config.d/ocfp-prod", "near the top of\n# ~/.ssh/config"} {
		assert.Contains(t, header, want)
	}

	for _, line := range strings.Split(header, "\n") {
		assert.True(t, strings.HasPrefix(line, "#"), "header line %q is not a comment", line)
	}

	help := newSSHConfigCmd()
	for _, want := range []string{`"Host *"`, "Include ~/.ssh/config.d/ocfp-<bloc>"} {
		assert.Contains(t, help.Long, want)
	}

	assert.NotContains(t, help.Example, ">> ~/.ssh/config\n", "the example should not append after any Host * block")
}

// TestGenerateSSHConfigChecksOnlyTheBastionHostKey proves the bastion block
// checks its host key against the default known_hosts, which the internal
// blocks also use for their ProxyJump hop, while the internal hosts behind
// it stay unchecked.
func TestGenerateSSHConfigChecksOnlyTheBastionHostKey(t *testing.T) {
	t.Parallel()

	result := generateSSHConfig(
		"203.0.113.10",
		"prod",
		"ubuntu",
		"/keys/id_ed25519",
		map[string]string{"bosh": "10.0.0.5"},
	)

	blocks := strings.Split(strings.TrimSpace(sshConfigHostBlocks(t, result)), "\n\n")
	require.Len(t, blocks, 2)

	assert.Contains(t, blocks[0], "    StrictHostKeyChecking accept-new\n")
	assert.NotContains(t, blocks[0], "UserKnownHostsFile")
	assert.NotContains(t, blocks[0], "StrictHostKeyChecking no")

	assert.Contains(t, blocks[1], "    ProxyJump prod-bastion\n")
	assert.Contains(t, blocks[1], "    StrictHostKeyChecking no\n")
	assert.Contains(t, blocks[1], "    UserKnownHostsFile /dev/null\n")
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
			name: "CustomSubnetNamesResolveViaSubnetOutputs",
			outputs: map[string]interface{}{
				"subnet_east-workload-a_cidr":         "10.4.4.0/22",
				"reserved_east-workload-a_bosh_ip":    "10.4.4.4",
				"reserved_east-workload-a_bastion_ip": "10.4.4.3",
			},
			blocName: "prod",
			expected: map[string]string{
				"bosh": "10.4.4.4",
			},
		},
		{
			name: "CustomNameWithUnderscoresKeepsComponentBoundary",
			outputs: map[string]interface{}{
				"subnet_east_zone_a_id":           "subnet-123",
				"reserved_east_zone_a_ocfp_ui_ip": "10.4.4.17",
			},
			blocName: "prod",
			expected: map[string]string{
				"ocfp_ui": "10.4.4.17",
			},
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

// sshConfigHostBlocks returns the generated ssh config without the header
// comment that opens it.
func sshConfigHostBlocks(t *testing.T, result string) string {
	t.Helper()

	header, rest, found := strings.Cut(result, "\n\n")
	require.True(t, found, "the output has no header comment")
	require.True(t, strings.HasPrefix(header, "# "), "the output does not open with a comment")

	return rest
}

// ssh splits a config value on spaces and expands % in IdentityFile, so a
// key path holding either is written so that ssh reads back exactly that
// path.
func TestGenerateSSHConfigQuotesTheKeyPath(t *testing.T) {
	t.Parallel()

	result := generateSSHConfig("203.0.113.10", "prod", "ubuntu", `/Users/o p/100%/"k"`, map[string]string{"bosh": "10.0.0.5"})

	assert.Equal(t, 2, strings.Count(result, `    IdentityFile "/Users/o p/100%%/\"k\""`+"\n"), result)
}

func TestGenerateSSHConfigKeyPathSurvivesSSHParsing(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "o p", "id_ed25519")
	configPath := filepath.Join(dir, "config")

	result := generateSSHConfig("203.0.113.10", "prod", "ubuntu", keyPath, map[string]string{})
	require.NoError(t, os.WriteFile(configPath, []byte(result), 0o600))

	out, err := exec.Command("ssh", "-F", configPath, "-G", "prod-bastion").CombinedOutput() //nolint:gosec // test-owned path
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "\nidentityfile "+keyPath+"\n")
}
