package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateKeyDirs points every ocfp data root at a temp directory, so key
// lookups never touch the developer's real home, and returns the bloc's
// SSH key directory under it.
func isolateKeyDirs(t *testing.T, blocName string) string {
	t.Helper()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	return filepath.Join(config.DataHome(), blocName, "ssh")
}

func TestResolveOperatorSSHKeyUsesBlocKeyWhenPresent(t *testing.T) { //nolint:paralleltest // t.Setenv
	dir := isolateKeyDirs(t, "ocfp-lab-x")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	want := filepath.Join(dir, "id_ed25519")
	writeTestEd25519Key(t, want)

	var notice bytes.Buffer

	got, err := resolveOperatorSSHKey("", "ocfp-lab-x", nil, &notice)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Empty(t, notice.String(), "no notice when the bloc key is used")
}

func TestResolveOperatorSSHKeyFallsBackWhenBlocKeyAbsent(t *testing.T) { //nolint:paralleltest // t.Setenv
	isolateKeyDirs(t, "ocfp-lab-x")

	var notice bytes.Buffer

	got, err := resolveOperatorSSHKey("", "ocfp-lab-x", nil, &notice)
	require.NoError(t, err)
	assert.Empty(t, got, "no key path means plain ssh")

	line := notice.String()
	assert.Equal(t, 1, strings.Count(line, "\n"), "exactly one line: %q", line)
	assert.Contains(t, line, "bloc SSH key not found")
	assert.Contains(t, line, "your own SSH keys and config")
}

func TestResolveOperatorSSHKeyExplicitKeyWins(t *testing.T) { //nolint:paralleltest // t.Setenv
	dir := isolateKeyDirs(t, "ocfp-lab-x")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	writeTestEd25519Key(t, filepath.Join(dir, "id_ed25519"))

	var notice bytes.Buffer

	got, err := resolveOperatorSSHKey("/keys/mine", "ocfp-lab-x", nil, &notice)
	require.NoError(t, err)
	assert.Equal(t, "/keys/mine", got)
	assert.Empty(t, notice.String())

	// With no bloc key at all the explicit key still wins, silently.
	isolateKeyDirs(t, "ocfp-lab-y")

	got, err = resolveOperatorSSHKey("/keys/mine", "ocfp-lab-y", nil, &notice)
	require.NoError(t, err)
	assert.Equal(t, "/keys/mine", got)
	assert.Empty(t, notice.String())
}

func TestResolveOperatorSSHKeyEmptyBlocKeyStaysAnError(t *testing.T) { //nolint:paralleltest // t.Setenv
	dir := isolateKeyDirs(t, "ocfp-lab-x")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "id_ed25519"), nil, 0o600))

	var notice bytes.Buffer

	_, err := resolveOperatorSSHKey("", "ocfp-lab-x", nil, &notice)
	require.Error(t, err, "a zero-byte bloc key is broken, not absent")
	assert.Empty(t, notice.String())
}

func TestBuildSSHCommandWithoutKeyLeavesAuthToTheUser(t *testing.T) { //nolint:paralleltest // t.Setenv
	t.Setenv("SSH_AUTH_SOCK", "")

	got := buildSSHCommand("100.109.226.53", "ubuntu", "", "", []string{}, nil)
	joined := strings.Join(got, " ")

	assert.NotContains(t, got, "-i")
	assert.NotContains(t, joined, "IdentitiesOnly")
	// Host-key handling is exactly what the keyed path sends.
	assert.Contains(t, joined, "-o UserKnownHostsFile=/dev/null")
	assert.Contains(t, joined, "-o StrictHostKeyChecking=no")
	assert.Equal(t, "ubuntu@100.109.226.53", got[len(got)-1])
}

func TestBuildSSHCommandForRouteWithoutKeyLeavesBothHopsToTheUser(t *testing.T) { //nolint:paralleltest // t.Setenv
	t.Setenv("SSH_AUTH_SOCK", "")

	route := sshRoute{Host: "10.64.64.11", Bastion: "100.109.226.53"}
	got := buildSSHCommandForRoute(route, "ubuntu", "", "", []string{}, nil)
	joined := strings.Join(got, " ")

	require.NotEqual(t, []string{"ssh", "--help"}, got)
	assert.NotContains(t, got, "-i")
	assert.NotContains(t, joined, "IdentitiesOnly")
	assert.Contains(t, got, "ProxyCommand="+bootstrap.BastionProxyCommand("", "ubuntu", "100.109.226.53"))
	assert.Equal(t, "ubuntu@10.64.64.11", got[len(got)-1])
}

func TestGenerateSSHConfigWithoutKeyOmitsIdentity(t *testing.T) {
	t.Parallel()

	out := generateSSHConfig("100.109.226.53", "lab", "ubuntu", "", map[string]string{"bosh": "10.64.64.4"})

	assert.NotContains(t, out, "IdentityFile")
	assert.NotContains(t, out, "IdentitiesOnly")
	assert.Contains(t, out, "ProxyJump lab-bastion")
	assert.Equal(t, 2, strings.Count(out, "StrictHostKeyChecking no"))
	assert.Equal(t, 2, strings.Count(out, "UserKnownHostsFile /dev/null"))
}
