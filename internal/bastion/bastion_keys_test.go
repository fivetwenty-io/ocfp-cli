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

// Security-key public keys built for tests: no private key exists for
// either one.
const (
	testSKEd25519Key = "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gAAAABHNzaDo= yubikey"
	testSKECDSAKey   = "sk-ecdsa-sha2-nistp256@openssh.com AAAAInNrLWVjZHNhLXNoYTItbmlzdHAyNTZAb3BlbnNzaC5jb20AAAAIbmlzdHAyNTYAAABBBGsX0fLhLEJH+Lzm5WOkQPJ3A32BLeszoPShOUXYmMKWT+NC4v4af5uO5+tKfA+eFivOM1drMV7Oy7ZAaDe/UfUAAAAEc3NoOg=="
)

// Security-key entries reach authorized_keys on the bastion rather than
// being skipped.
func TestConfigureBastionKeysInstallsSecurityKeys(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "pve")
	cfg.Bastion.Keys = map[string]string{
		"yubi-ed": testSKEd25519Key,
		"yubi-ec": testSKECDSAKey,
	}

	mockSSH := &caTrustMockSSHClient{}
	m := newMinimalManager(cfg)
	m.sshClient = mockSSH

	require.NoError(t, m.configureBastionKeys(context.Background()))
	require.Len(t, mockSSH.commands, 2, "read authorized_keys, then append")
	assert.Contains(t, mockSSH.commands[1], testSKEd25519Key)
	assert.Contains(t, mockSSH.commands[1], testSKECDSAKey)
}

// A malformed literal key fails before the bastion is touched.
func TestConfigureBastionKeysFailsOnAMalformedLiteralKey(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "pve")
	cfg.Bastion.Keys = map[string]string{"bad": "ssh-ed25519 not-a-key"}

	mockSSH := &caTrustMockSSHClient{}
	m := newMinimalManager(cfg)
	m.sshClient = mockSSH

	err := m.configureBastionKeys(context.Background())
	require.ErrorIs(t, err, config.ErrBastionKeySpecInvalid)
	assert.Contains(t, err.Error(), "keys.bad")
	assert.Empty(t, mockSSH.commands)
}
