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

func testTools() inceptionTools {
	return inceptionTools{
		safe:   "/opt/safe/bin/safe",
		engine: inceptionEngine{name: "bao", path: "/opt/engines/bin/bao"},
	}
}

func TestBuildSafeLocalCommand_FreshStartUsesRaft(t *testing.T) {
	paths := startTestPaths(t, filepath.Join(t.TempDir(), "vault", "data"))

	cmd := buildSafeLocalCommand(paths, testTools(), safeLocalFresh)

	assert.True(t, strings.HasPrefix(cmd, `PATH='/opt/engines/bin':"$PATH" '/opt/safe/bin/safe' local `), cmd)
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

	cmd := buildSafeLocalCommand(paths, testTools(), safeLocalRestart)

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
		cmd := buildSafeLocalCommand(paths, testTools(), mode)

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

	stubDir := filepath.Join(t.TempDir(), "safe dir's bin")
	require.NoError(t, os.MkdirAll(stubDir, 0o700))

	tools := inceptionTools{
		safe:   writeFakeExecutable(t, stubDir, "safe", `for a in "$@"; do printf '%s\n' "$a"; done`),
		engine: inceptionEngine{name: "vault", path: filepath.Join(t.TempDir(), "engine dir's bin", "vault")},
	}
	cmd := buildSafeLocalCommand(paths, tools, safeLocalRestart)

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

// The pane must run the safe whose version ocfp checked. Putting the engine's
// directory first on PATH would let a different safe in that directory answer
// instead, so the line names the checked safe by its absolute path.
func TestBuildSafeLocalCommand_RunsTheCheckedSafe(t *testing.T) {
	paths := startTestPaths(t, filepath.Join(t.TempDir(), "vault", "data"))

	engineDir := t.TempDir()
	writeFakeExecutable(t, engineDir, "safe", "echo WRONG-SAFE\n")

	tools := inceptionTools{
		safe:   writeFakeExecutable(t, t.TempDir(), "safe", "echo CHECKED-SAFE\n"),
		engine: inceptionEngine{name: "bao", path: filepath.Join(engineDir, "bao")},
	}

	out, err := exec.Command("sh", "-c", buildSafeLocalCommand(paths, tools, safeLocalFresh)).CombinedOutput() // #nosec G204 -- test runs the command it just built against a stub
	require.NoError(t, err, string(out))
	assert.Equal(t, "CHECKED-SAFE\n", string(out))
}

// longStartTestPaths returns paths whose every directory is near the longest
// name a filesystem allows, so the full safe command is far past what tmux
// send-keys will type in one go.
func longStartTestPaths(t *testing.T) map[string]string {
	t.Helper()

	long := strings.Repeat("d", 200)
	base := filepath.Join(t.TempDir(), long, long)

	paths := startTestPaths(t, filepath.Join(base, long, "vault", "data"))
	paths["logDir"] = filepath.Join(base, "logs", long)
	paths["logFile"] = filepath.Join(paths["logDir"], "vault-inception.log")
	paths["vaultName"] = "ocfp-lab-" + strings.Repeat("b", 60) + "-inception"

	require.NoError(t, os.MkdirAll(paths["logDir"], 0o700))

	return paths
}

// tmux send-keys cuts typed input off at about 1024 bytes, which would run a
// truncated safe command. The pane is handed a short line that runs a
// launcher script instead, however long the paths are.
func TestSafeLocalLauncher_TypedLineStaysShort(t *testing.T) {
	paths := longStartTestPaths(t)
	tools := testTools()

	full := buildSafeLocalCommand(paths, tools, safeLocalRestart)
	require.Greater(t, len(full), tmuxSendKeysLimit, "the fixture must be long enough to matter")

	script, err := writeSafeLocalLauncher(paths, tools, safeLocalRestart)
	require.NoError(t, err)

	line, err := safeLocalLauncherLine(script)
	require.NoError(t, err)
	assert.Equal(t, "sh "+shellQuote(script), line)
	assert.Less(t, len(line), tmuxSendKeysLimit)
	assert.NotContains(t, line, paths["vaultDir"])
}

// A launcher path too long to type safely is refused rather than typed
// truncated into the pane.
func TestSafeLocalLauncherLine_RefusesAnOverlongPath(t *testing.T) {
	_, err := safeLocalLauncherLine("/" + strings.Repeat("x", tmuxSendKeysLimit))
	require.ErrorIs(t, err, ErrSafeLocalLineTooLong)
}

// The launcher holds the same command the pane used to be typed, in a file
// only the user can read, and never the key values.
func TestSafeLocalLauncher_HoldsTheCommandAndNoKeys(t *testing.T) {
	paths := longStartTestPaths(t)
	tools := testTools()

	for _, mode := range []safeLocalMode{safeLocalFresh, safeLocalRestart} {
		script, err := writeSafeLocalLauncher(paths, tools, mode)
		require.NoError(t, err)
		assert.Equal(t, paths["logDir"], filepath.Dir(script))

		info, err := os.Stat(script)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

		body, err := os.ReadFile(script) // #nosec G304 -- test reads the launcher it just wrote
		require.NoError(t, err)
		assert.Equal(t, "#!/bin/sh\n"+buildSafeLocalCommand(paths, tools, mode)+"\n", string(body))
		assert.NotContains(t, string(body), "ROOT-TOKEN-SENTINEL")
		assert.NotContains(t, string(body), "UNSEAL-KEY-SENTINEL")
	}
}

// A launcher left from an earlier start, even one another user could read,
// is replaced whole with a private file.
func TestSafeLocalLauncher_ReplacesAnOldLauncher(t *testing.T) {
	paths := longStartTestPaths(t)
	tools := testTools()

	script, err := writeSafeLocalLauncher(paths, tools, safeLocalFresh)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(script, []byte("echo stale\n"), 0o600))
	require.NoError(t, os.Chmod(script, 0o644)) // #nosec G302 -- the test makes the old launcher readable on purpose

	again, err := writeSafeLocalLauncher(paths, tools, safeLocalRestart)
	require.NoError(t, err)
	assert.Equal(t, script, again)

	info, err := os.Stat(again)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	body, err := os.ReadFile(again) // #nosec G304 -- test reads the launcher it just wrote
	require.NoError(t, err)
	assert.Contains(t, string(body), "--root-token-file")
}

// The pane's shell runs the launcher through sh, and safe gets exactly the
// arguments the full command would have given it.
func TestSafeLocalLauncher_RunsThroughTheShell(t *testing.T) {
	paths := longStartTestPaths(t)
	tools := inceptionTools{
		safe:   writeFakeExecutable(t, t.TempDir(), "safe", `for a in "$@"; do printf '%s\n' "$a"; done`),
		engine: inceptionEngine{name: "bao", path: "/opt/engines/bin/bao"},
	}

	script, err := writeSafeLocalLauncher(paths, tools, safeLocalFresh)
	require.NoError(t, err)

	line, err := safeLocalLauncherLine(script)
	require.NoError(t, err)

	out, err := exec.Command("sh", "-c", line).CombinedOutput() // #nosec G204 -- test runs the line it just built against a stub
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "--raft\n"+paths["vaultDir"]+"\n")
}
