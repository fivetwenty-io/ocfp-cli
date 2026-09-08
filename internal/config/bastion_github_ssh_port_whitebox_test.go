package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidate_RejectsBadGitHubSSHPort asserts that the top-level validate()
// surfaces a bad bastion.githubSshPort rather than letting it reach the
// bastion as a broken ssh config.
func TestValidate_RejectsBadGitHubSSHPort(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfig("root@pam!ocfp-bosh=abc123", "secret-uuid", "", "")
	cfg.Bastion.GitHubSSHPort = 8443

	err := validate(cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "githubSshPort must be 22 or 443, got 8443")
}

// TestValidate_AcceptsGitHubSSHPort443 asserts that 443 passes validation.
func TestValidate_AcceptsGitHubSSHPort443(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfig("root@pam!ocfp-bosh=abc123", "secret-uuid", "", "")
	cfg.Bastion.GitHubSSHPort = 443

	require.NoError(t, validate(cfg))
}
