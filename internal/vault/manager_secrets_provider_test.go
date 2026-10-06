package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests replace PATH with a directory holding a fake genesis, so none
// of them may call t.Parallel(): PATH is process-wide state.

// fakeGenesisScript records each invocation as "<physical cwd>\t<args>" in
// $FAKE_GENESIS_LOG. Asked for `envs`, it reports the dead-vault skip and
// still exits 0, as the real genesis does once the inception vault is gone.
// It exits non-zero when the basename of its cwd is listed in
// $FAKE_GENESIS_FAIL.
const fakeGenesisScript = `#!/bin/sh
dir="$(pwd -P)"
printf '%s\t%s\n' "$dir" "$*" >> "$FAKE_GENESIS_LOG"
if [ "$1" = "envs" ]; then
	echo "Skipping bosh: Could not connect to vault ..."
	exit 0
fi
name="$(basename "$dir")"
for f in $FAKE_GENESIS_FAIL; do
	if [ "$f" = "$name" ]; then
		echo "fake genesis failure in $name"
		exit 1
	fi
done
exit 0
`

// genesisCall is one recorded invocation of the fake genesis.
type genesisCall struct {
	dir  string
	args string
}

// installFakeGenesis puts the fake genesis first on PATH and returns the
// path of its invocation log.
func installFakeGenesis(t *testing.T, failRepos ...string) string {
	t.Helper()

	binDir := t.TempDir()
	script := filepath.Join(binDir, "genesis")
	//nolint:gosec // the fake must be executable to stand in for genesis
	require.NoError(t, os.WriteFile(script, []byte(fakeGenesisScript), 0o755))

	logPath := filepath.Join(t.TempDir(), "genesis.log")

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GENESIS_LOG", logPath)
	t.Setenv("FAKE_GENESIS_FAIL", strings.Join(failRepos, " "))

	return logPath
}

// readGenesisCalls parses the fake genesis log. A missing log means genesis
// was never run.
func readGenesisCalls(t *testing.T, logPath string) []genesisCall {
	t.Helper()

	data, err := os.ReadFile(logPath) //nolint:gosec // test-owned temp file
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	require.NoError(t, err)

	var calls []genesisCall

	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		dir, args, ok := strings.Cut(line, "\t")
		require.True(t, ok, "malformed log line %q", line)

		calls = append(calls, genesisCall{dir: dir, args: args})
	}

	return calls
}

// makeGenesisRepo creates dir with a .genesis/config file in it.
func makeGenesisRepo(t *testing.T, dir string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".genesis"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".genesis", "config"), []byte("---\n"), 0o600))
}

// physical resolves symlinks so expected paths match `pwd -P` on systems
// whose temp dir sits behind a symlink, such as /var on macOS.
func physical(t *testing.T, dir string) string {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	return resolved
}

func TestUpdateEnvironmentSecrets_RepointsEachRepo(t *testing.T) {
	logPath := installFakeGenesis(t)

	deployments := t.TempDir()
	makeGenesisRepo(t, filepath.Join(deployments, "concourse"))
	makeGenesisRepo(t, filepath.Join(deployments, "bosh"))
	require.NoError(t, os.MkdirAll(filepath.Join(deployments, "certs"), 0o750))
	t.Setenv("DEPLOYMENTS_DIR", deployments)

	err := newTargetTestManager("mybloc").updateEnvironmentSecrets()
	require.NoError(t, err)

	root := physical(t, deployments)
	assert.Equal(t, []genesisCall{
		{dir: filepath.Join(root, "bosh"), args: "secrets-provider mybloc-mgmt"},
		{dir: filepath.Join(root, "concourse"), args: "secrets-provider mybloc-mgmt"},
	}, readGenesisCalls(t, logPath))
}

func TestUpdateEnvironmentSecrets_NoReposIsAnError(t *testing.T) {
	logPath := installFakeGenesis(t)

	deployments := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(deployments, "certs"), 0o750))
	t.Setenv("DEPLOYMENTS_DIR", deployments)

	err := newTargetTestManager("mybloc").updateEnvironmentSecrets()
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoGenesisRepos)
	assert.Contains(t, err.Error(), deployments)
	assert.Empty(t, readGenesisCalls(t, logPath))
}

func TestUpdateEnvironmentSecrets_ContinuesPastFailure(t *testing.T) {
	logPath := installFakeGenesis(t, "bosh")

	deployments := t.TempDir()
	makeGenesisRepo(t, filepath.Join(deployments, "bosh"))
	makeGenesisRepo(t, filepath.Join(deployments, "concourse"))
	t.Setenv("DEPLOYMENTS_DIR", deployments)

	err := newTargetTestManager("mybloc").updateEnvironmentSecrets()
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEnvironmentUpdate)

	root := physical(t, deployments)
	assert.Equal(t, []genesisCall{
		{dir: filepath.Join(root, "bosh"), args: "secrets-provider mybloc-mgmt"},
		{dir: filepath.Join(root, "concourse"), args: "secrets-provider mybloc-mgmt"},
	}, readGenesisCalls(t, logPath))
}

func TestUpdateEnvironmentSecrets_SingleRepoDir(t *testing.T) {
	logPath := installFakeGenesis(t)

	repo := filepath.Join(t.TempDir(), "bosh")
	makeGenesisRepo(t, repo)
	t.Setenv("DEPLOYMENTS_DIR", repo)

	err := newTargetTestManager("mybloc").updateEnvironmentSecrets()
	require.NoError(t, err)

	assert.Equal(t, []genesisCall{
		{dir: physical(t, repo), args: "secrets-provider mybloc-mgmt"},
	}, readGenesisCalls(t, logPath))
}
