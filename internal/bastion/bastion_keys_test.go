package bastion

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// configureBastionKeys must fail, naming the entry, rather than bring a
// bastion up with only the bloc key when an entry cannot be read. It fails
// before it touches the bastion.
func TestConfigureBastionKeysFailsOnAnUnreadableEntry(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "pve")
	cfg.Bastion.Keys = map[string]string{"wayne": "bitbucket.org/wayne"}

	mockSSH := &caTrustMockSSHClient{}
	m := newMinimalManager(cfg)
	m.sshClient = mockSSH

	err := m.configureBastionKeys(context.Background())
	require.Error(t, err)
	require.ErrorIs(t, err, config.ErrBastionKeySpecInvalid)
	assert.Contains(t, err.Error(), "wayne")
	assert.Empty(t, mockSSH.commands, "no SSH commands run for a config that cannot be read")
}

// A bare name loads with a warning, but ocfp init bastion still refuses to
// apply keys from it, before it touches the bastion.
func TestConfigureBastionKeysFailsOnABareName(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "pve")
	cfg.Bastion.Keys = map[string]string{"wayne": "wayne"}
	require.NoError(t, cfg.Bastion.Validate(), "the entry does not stop the bloc loading")

	mockSSH := &caTrustMockSSHClient{}
	m := newMinimalManager(cfg)
	m.sshClient = mockSSH

	err := m.configureBastionKeys(context.Background())
	require.ErrorIs(t, err, config.ErrBastionKeySpecInvalid)
	assert.Contains(t, err.Error(), "keys.wayne")
	assert.Contains(t, err.Error(), "names no key source")
	assert.Empty(t, mockSSH.commands, "no SSH commands run")
}
