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

// startTestPaths returns a bloc's paths moved into a temp directory, with key
// files holding recognisable values so a test can prove they never reach the
// command line typed into tmux.
func startTestPaths(t *testing.T, dataDir string) map[string]string {
	t.Helper()

	paths := getVaultInceptionPaths("ocfp-lab-drgao", false)
	vaultRoot := filepath.Dir(dataDir)

	paths["vaultDir"] = dataDir
	paths["rootKeyFile"] = filepath.Join(vaultRoot, "root.key")
	paths["unsealKeysFile"] = filepath.Join(vaultRoot, "unseal.keys")
	paths["logFile"] = filepath.Join(t.TempDir(), "vault-inception.log")

	require.NoError(t, os.MkdirAll(vaultRoot, 0o700))
	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("s.ROOT-TOKEN-SENTINEL\n"), 0o600))
	require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte("UNSEAL-KEY-SENTINEL\n"), 0o600))

	return paths
}

func testEngine() inceptionEngine {
	return inceptionEngine{name: "bao", path: "/opt/engines/bin/bao"}
}

func TestBuildSafeLocalCommand_FreshStartUsesRaft(t *testing.T) {
	paths := startTestPaths(t, filepath.Join(t.TempDir(), "vault", "data"))

	cmd := buildSafeLocalCommand(paths, testEngine(), safeLocalFresh)

	assert.True(t, strings.HasPrefix(cmd, `PATH='/opt/engines/bin':"$PATH" safe local `), cmd)
	assert.Contains(t, cmd, "--raft '"+paths["vaultDir"]+"'")
	assert.Contains(t, cmd, "--as '"+paths["vaultName"]+"'")
	assert.Contains(t, cmd, "--port "+paths["port"]+" ")
	assert.Contains(t, cmd, "--cluster-port "+paths["clusterPort"]+" ")
	assert.Contains(t, cmd, "--engine bao")
	assert.Contains(t, cmd, "< /dev/null 2>&1 | tee '"+paths["logFile"]+"'")
	assert.NotContains(t, cmd, "--file")
	assert.NotContains(t, cmd, "--root-token-file")
	assert.NotContains(t, cmd, "unseal.keys")
}

func TestBuildSafeLocalCommand_RestartReopensWithSavedKeys(t *testing.T) {
	paths := startTestPaths(t, filepath.Join(t.TempDir(), "vault", "data"))

	cmd := buildSafeLocalCommand(paths, testEngine(), safeLocalRestart)

	assert.Contains(t, cmd, "--raft '"+paths["vaultDir"]+"'")
	assert.Contains(t, cmd, "--cluster-port "+paths["clusterPort"]+" ")
	assert.Contains(t, cmd, "--root-token-file '"+paths["rootKeyFile"]+"'")
	assert.Contains(t, cmd, "< '"+paths["unsealKeysFile"]+"' 2>&1 | tee '"+paths["logFile"]+"'")
	assert.NotContains(t, cmd, "/dev/null")
	assert.NotContains(t, cmd, "--file")
}

func TestBuildSafeLocalCommand_NeverCarriesKeyValues(t *testing.T) {
	paths := startTestPaths(t, filepath.Join(t.TempDir(), "vault", "data"))

	for _, mode := range []safeLocalMode{safeLocalFresh, safeLocalRestart} {
		cmd := buildSafeLocalCommand(paths, testEngine(), mode)

		assert.NotContains(t, cmd, "ROOT-TOKEN-SENTINEL")
		assert.NotContains(t, cmd, "UNSEAL-KEY-SENTINEL")
	}
}

// A shell must hand safe the exact paths, even ones holding a space and a
// single quote. The command is run through sh with a stub safe that prints
// its arguments, so the test checks real shell parsing, not string shapes.
func TestBuildSafeLocalCommand_QuotingSurvivesTheShell(t *testing.T) {
	_, err := exec.LookPath("sh")
	require.NoError(t, err)

	base := filepath.Join(t.TempDir(), "it's a bloc")
	paths := startTestPaths(t, filepath.Join(base, "vault", "data"))
	paths["logFile"] = filepath.Join(base, "log's here.log")

	stubDir := filepath.Join(t.TempDir(), "engine dir's bin")
	require.NoError(t, os.MkdirAll(stubDir, 0o700))
	writeFakeExecutable(t, stubDir, "safe", `for a in "$@"; do printf '%s\n' "$a"; done`)

	engine := inceptionEngine{name: "vault", path: filepath.Join(stubDir, "vault")}
	cmd := buildSafeLocalCommand(paths, engine, safeLocalRestart)

	out, err := exec.Command("sh", "-c", cmd).CombinedOutput() // #nosec G204 -- test runs the command it just built against a stub
	require.NoError(t, err, string(out))

	args := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	assert.Equal(t, []string{
		"local",
		"--raft", paths["vaultDir"],
		"--as", paths["vaultName"],
		"--port", paths["port"],
		"--cluster-port", paths["clusterPort"],
		"--engine", "vault",
		"--root-token-file", paths["rootKeyFile"],
	}, args)

	logged, err := os.ReadFile(paths["logFile"])
	require.NoError(t, err)
	assert.Equal(t, string(out), string(logged), "tee must write to the quoted log path")
}

func TestShellQuote(t *testing.T) {
	assert.Equal(t, `'plain'`, shellQuote("plain"))
	assert.Equal(t, `'it'\''s'`, shellQuote("it's"))
	assert.Equal(t, `''`, shellQuote(""))
}
