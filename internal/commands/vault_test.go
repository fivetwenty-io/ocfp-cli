package commands

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestGetVaultInceptionPaths_PortIsPerBloc guards concurrent bootstraps: two
// blocs on one workstation must not resolve to the same listener port, or the
// second `safe local` fails with EADDRINUSE and the winner's data directory
// silently accumulates both blocs' secrets.
func TestGetVaultInceptionPaths_PortIsPerBloc(t *testing.T) {
	t.Parallel()

	first := mustInceptionPaths(t, "ocfp-lab-drgao", false)
	second := mustInceptionPaths(t, "ocfp-lab-drhu", false)

	if first["port"] == second["port"] {
		t.Errorf("blocs share inception port %q", first["port"])
	}

	if first["port"] == "8234" || second["port"] == "8234" {
		t.Errorf("bloc-scoped inception must not use the shared legacy port 8234")
	}
}

// TestGetVaultInceptionPaths_LogFileIsPerBloc guards readiness detection: the
// log file is tee'd into by `safe local` and read back to decide whether the
// vault came up. A shared file makes one bloc read another bloc's startup
// output, reporting a bogus failure — or a bogus success.
func TestGetVaultInceptionPaths_LogFileIsPerBloc(t *testing.T) {
	t.Parallel()

	first := mustInceptionPaths(t, "ocfp-lab-drgao", false)
	second := mustInceptionPaths(t, "ocfp-lab-drhu", false)

	if first["logFile"] == second["logFile"] {
		t.Errorf("blocs share inception log file %q", first["logFile"])
	}

	if filepath.Dir(first["logFile"]) != first["logDir"] {
		t.Errorf("logFile %q is not inside logDir %q", first["logFile"], first["logDir"])
	}
}

func TestGetVaultInceptionPaths(t *testing.T) {
	t.Parallel()

	homeDir := os.Getenv("HOME")

	tests := []struct {
		name     string
		blocName string
		testMode bool
		checks   map[string]string
	}{
		{
			name:     "no bloc",
			blocName: "",
			testMode: false,
			checks: map[string]string{
				"tmuxSession": "inception-vault",
				"vaultName":   "inception",
				"port":        "8234",
				"clusterPort": "9234",
				"vaultDir":    filepath.Join(homeDir, ".vault"),
			},
		},
		{
			name:     "with bloc",
			blocName: "520-aws-wayne",
			testMode: false,
			checks: map[string]string{
				"tmuxSession": "520-aws-wayne-inception-vault",
				"vaultName":   "520-aws-wayne-inception",
				"port":        strconv.Itoa(config.InceptionVaultPort("520-aws-wayne")),
				"vaultDir":    filepath.Join(mustBlocDir(t, "520-aws-wayne"), "vault", "data"),
				"rootKeyFile": filepath.Join(mustBlocDir(t, "520-aws-wayne"), "vault", "root.key"),
				"clusterPort": strconv.Itoa(config.InceptionVaultPort("520-aws-wayne") + config.InceptionVaultClusterPortOffset),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			paths := mustInceptionPaths(t, tc.blocName, tc.testMode)

			for key, expected := range tc.checks {
				if paths[key] != expected {
					t.Errorf("paths[%q] = %q, want %q", key, paths[key], expected)
				}
			}
		})
	}
}

func TestGetVaultInceptionPaths_TestMode(t *testing.T) {
	t.Parallel()

	homeDir := os.Getenv("HOME")
	paths := mustInceptionPaths(t, "any-bloc", true)

	// Test mode should override bloc-specific paths
	if paths["tmuxSession"] != "test-inception-vault" {
		t.Errorf("tmuxSession = %q, want %q", paths["tmuxSession"], "test-inception-vault")
	}

	if paths["vaultName"] != "test-inception" {
		t.Errorf("vaultName = %q, want %q", paths["vaultName"], "test-inception")
	}

	if paths["port"] != "8235" {
		t.Errorf("port = %q, want %q", paths["port"], "8235")
	}

	if paths["clusterPort"] != "9235" {
		t.Errorf("clusterPort = %q, want %q", paths["clusterPort"], "9235")
	}

	if paths["vaultDir"] != filepath.Join(homeDir, ".test-vault") {
		t.Errorf("vaultDir = %q, want %q", paths["vaultDir"], filepath.Join(homeDir, ".test-vault"))
	}
}

// TestGetVaultInceptionPaths_TestModeKeyFilesAreSeparate guards the check
// that a running vault's keys are saved: it needs the root token and the
// unseal key in two files, so a test-mode vault that shares one file for both
// can never pass it.
func TestGetVaultInceptionPaths_TestModeKeyFilesAreSeparate(t *testing.T) {
	home := t.TempDir()

	original := homeDirFn
	homeDirFn = func() (string, error) { return home, nil }

	t.Cleanup(func() { homeDirFn = original })

	paths := mustInceptionPaths(t, "any-bloc", true)

	assert.Equal(t, filepath.Join(home, "test-vault.root.key"), paths["rootKeyFile"])
	assert.Equal(t, filepath.Join(home, "test-vault.unseal.keys"), paths["unsealKeysFile"])
	assert.NotEqual(t, paths["rootKeyFile"], paths["unsealKeysFile"])
	assert.Equal(t, filepath.Join(home, "test-vault.key"), paths["vaultKeyFile"], "nothing else reads vaultKeyFile")

	// A vault that holds both keys in those files counts as saved.
	saved, err := inceptionKeysSaved(paths)
	require.NoError(t, err)
	assert.False(t, saved, "no keys are written yet")

	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("hvs.testmodetoken0123456789\n"), 0o600))
	require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte(testSealKey+"\n"), 0o600))

	saved, err = inceptionKeysSaved(paths)
	require.NoError(t, err)
	assert.True(t, saved, "a test-mode vault with both keys written must count as saved")
}

// Without a bloc, the root token and the unseal key each get their own file
// in the home directory. Older releases wrote both to ~/vault.key, where the
// second write replaced the first, so that file is no longer read or written.
func TestGetVaultInceptionPaths_NoBlocKeyFilesAreSeparate(t *testing.T) {
	home := t.TempDir()

	original := homeDirFn
	homeDirFn = func() (string, error) { return home, nil }

	t.Cleanup(func() { homeDirFn = original })

	paths := mustInceptionPaths(t, "", false)

	assert.Equal(t, filepath.Join(home, "vault.root.key"), paths["rootKeyFile"])
	assert.Equal(t, filepath.Join(home, "vault.unseal.keys"), paths["unsealKeysFile"])
	assert.Equal(t, filepath.Join(home, ".vault"), paths["vaultDir"])

	oldKeyFile := filepath.Join(home, "vault.key")
	for _, key := range []string{"rootKeyFile", "unsealKeysFile"} {
		assert.NotEqual(t, oldKeyFile, paths[key], "%s must not be the shared file older releases wrote", key)
	}

	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("hvs.nobloctoken0123456789\n"), 0o600))
	require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte(testSealKey+"\n"), 0o600))

	saved, err := inceptionKeysSaved(paths)
	require.NoError(t, err)
	assert.True(t, saved, "a vault with both keys written must count as saved")
}

// An archive without a bloc moves the data and both key files aside, and
// never renames, replaces, or deletes the ~/vault.key an older release left.
func TestArchiveAndForgetVault_NoBlocLeavesTheOldKeyFile(t *testing.T) {
	home := t.TempDir()

	original := homeDirFn
	homeDirFn = func() (string, error) { return home, nil }

	t.Cleanup(func() { homeDirFn = original })

	paths := mustInceptionPaths(t, "", false)
	oldKeyFile := filepath.Join(home, "vault.key")
	suffix := "20261006-120000"

	require.NoError(t, os.MkdirAll(filepath.Join(paths["vaultDir"], "raft"), 0o700))
	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("root\n"), 0o600))
	require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte("unseal\n"), 0o600))
	require.NoError(t, os.WriteFile(oldKeyFile, []byte("older-release-key\n"), 0o600))

	archived, err := archiveAndForgetVault(paths, suffix, zap.NewNop().Sugar())
	require.NoError(t, err)

	assert.Equal(t, paths["vaultDir"]+VaultArchiveSuffix+suffix, archived)
	assert.FileExists(t, paths["rootKeyFile"]+VaultArchiveSuffix+suffix)
	assert.FileExists(t, paths["unsealKeysFile"]+VaultArchiveSuffix+suffix)

	kept, err := os.ReadFile(oldKeyFile) // #nosec G304 -- the test's own file
	require.NoError(t, err)
	assert.Equal(t, "older-release-key\n", string(kept))
	assert.NoFileExists(t, oldKeyFile+VaultArchiveSuffix+suffix)
}

// TestGetVaultInceptionPaths_NoBlocLogDirUnderStateHomeNotOcfpHome verifies
// the no-bloc inception log directory resolves under the XDG state-class
// root (config.GetLogDir()) rather than a hardcoded config.OcfpHome() join,
// when neither the new nor the legacy log directory pre-exists.
func TestGetVaultInceptionPaths_NoBlocLogDirUnderStateHomeNotOcfpHome(t *testing.T) {
	legacyHome := t.TempDir()
	xdgStateBase := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", legacyHome)
	t.Setenv("XDG_STATE_HOME", xdgStateBase)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	wantLogDir := filepath.Join(config.GetLogDir(), "vault")

	paths := mustInceptionPaths(t, "", false)

	if paths["logDir"] != wantLogDir {
		t.Errorf("logDir = %q, want %q", paths["logDir"], wantLogDir)
	}

	if strings.Contains(paths["logDir"], legacyHome) {
		t.Errorf("logDir = %q, must not resolve under legacy HOME %q", paths["logDir"], legacyHome)
	}
}

// TestGetVaultInceptionPaths_BlocLogDirUnderStateHomeNotDataHome verifies
// the bloc-scoped inception log directory resolves under the XDG
// state-class root rather than mustBlocDir(t, ) (data-class), when
// neither the new nor the legacy log directory pre-exists.
func TestGetVaultInceptionPaths_BlocLogDirUnderStateHomeNotDataHome(t *testing.T) {
	legacyHome := t.TempDir()
	xdgStateBase := t.TempDir()
	xdgDataBase := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", legacyHome)
	t.Setenv("XDG_STATE_HOME", xdgStateBase)
	t.Setenv("XDG_DATA_HOME", xdgDataBase)
	t.Setenv("XDG_CONFIG_HOME", "")

	const blocName = "xdg-log-bloc"

	wantLogDir := filepath.Join(xdgStateBase, "ocfp", blocName, VaultInceptionLogDir)

	paths := mustInceptionPaths(t, blocName, false)

	if paths["logDir"] != wantLogDir {
		t.Errorf("logDir = %q, want %q", paths["logDir"], wantLogDir)
	}

	if strings.Contains(paths["logDir"], xdgDataBase) {
		t.Errorf("logDir = %q, must not resolve under XDG data home %q", paths["logDir"], xdgDataBase)
	}

	if strings.Contains(paths["logDir"], legacyHome) {
		t.Errorf("logDir = %q, must not resolve under legacy HOME %q", paths["logDir"], legacyHome)
	}
}

// TestRequireClusterPort_FailsBeforeAnyWork guards an operator override high
// enough that the cluster port would pass 65535: the error must surface while
// resolving paths, before anything is stopped or moved.
func TestRequireClusterPort_FailsBeforeAnyWork(t *testing.T) {
	t.Setenv(config.InceptionVaultPortEnvVar, "64600")

	paths := mustInceptionPaths(t, "ocfp-lab-drgao", false)

	if paths["clusterPort"] != "" {
		t.Fatalf("clusterPort = %q, want empty for an API port past 64535", paths["clusterPort"])
	}

	err := requireClusterPort(paths)
	if err == nil {
		t.Fatal("requireClusterPort accepted an API port with no room for its cluster port")
	}

	if !strings.Contains(err.Error(), "64600") || !strings.Contains(err.Error(), config.InceptionVaultPortEnvVar) {
		t.Errorf("error %q should name the API port and the override variable", err)
	}

	t.Setenv(config.InceptionVaultPortEnvVar, "18500")

	err = requireClusterPort(mustInceptionPaths(t, "ocfp-lab-drgao", false))
	if err != nil {
		t.Errorf("requireClusterPort rejected a usable API port: %v", err)
	}
}

// A boot unit or a script that runs a vault subcommand this binary does not
// have must fail, not print help and exit 0 as if it had worked.
func TestNewVaultCmd_UnknownSubcommandFails(t *testing.T) {
	root := &cobra.Command{Use: "ocfp"} //nolint:exhaustruct // a bare root is all the test needs
	root.AddCommand(NewVaultCmd())
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"vault", "no-such-subcommand"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "no-such-subcommand"`)
}

// 'ocfp vault' on its own still prints its help and succeeds.
func TestNewVaultCmd_BareCommandPrintsHelp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OCFP_HOME", filepath.Join(home, "ocfp"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))

	var out bytes.Buffer

	root := &cobra.Command{Use: "ocfp"} //nolint:exhaustruct // a bare root is all the test needs
	root.AddCommand(NewVaultCmd())
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"vault"})

	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), "Available Commands:")
	assert.Contains(t, out.String(), "start")
}

// mustInceptionPaths returns getVaultInceptionPaths for a bloc whose
// directory resolves, failing the test when it does not.
func mustInceptionPaths(t *testing.T, blocName string, testMode bool) map[string]string {
	t.Helper()

	paths, err := getVaultInceptionPaths(blocName, testMode)
	if err != nil {
		t.Fatalf("getVaultInceptionPaths(%q, %v) error = %v", blocName, testMode, err)
	}

	return paths
}

// mustBlocDir returns config.OcfpBlocDir for a bloc whose directory
// resolves, failing the test when it does not.
func mustBlocDir(t *testing.T, blocName string) string {
	t.Helper()

	dir, err := config.OcfpBlocDir(blocName)
	if err != nil {
		t.Fatalf("config.OcfpBlocDir(%q) error = %v", blocName, err)
	}

	return dir
}
