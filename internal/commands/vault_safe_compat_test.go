package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// writeFakeExecutable writes a shell script into dir under name and returns
// its path. The inception vault tests use these in place of safe, tmux, and
// the vault engines so nothing real ever starts. It returns only once the
// script can be exec'd, so a parallel test's fork cannot leave it busy.
func writeFakeExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name)

	writeFakeExecutableFile(t, path, "#!/bin/sh\n"+body)

	return path
}

// fakeSafe writes a safe that answers --version with version and `help local`
// with helpText.
func fakeSafe(t *testing.T, dir, version, helpText string) string {
	t.Helper()

	return writeFakeExecutable(t, dir, "safe", `case "$1" in
  --version) printf '%s\n  commit 0000000\n' '`+version+`' ;;
  help) printf '%s\n' '`+helpText+`' ;;
esac
`)
}

// fakeSafeLocalHelpBase is what `safe help local` says about the options a
// raft inception vault needs, and fakeSafeLocalHelp adds --keep-current.
const (
	fakeSafeLocalHelpBase = "safe local (--memory|--file dir|--raft dir) [--cluster-port port] [--root-token-file path]"
	fakeSafeLocalHelp     = fakeSafeLocalHelpBase + " [--keep-current]"
)

func TestCheckSafeCompatibility_RefusesOldRelease(t *testing.T) {
	t.Parallel()

	safePath := fakeSafe(t, t.TempDir(), "safe v1.24.0", fakeSafeLocalHelp)

	err := checkSafeCompatibility(context.Background(), safePath)
	require.ErrorIs(t, err, ErrSafeTooOld)

	msg := err.Error()
	for _, want := range []string{
		"v1.24.0",
		"v1.25.0",
		"--raft",
		"--cluster-port",
		"--root-token-file",
		"brew upgrade cloudfoundry-community/cf/safe",
		"https://github.com/cloudfoundry-community/safe/releases",
	} {
		assert.Contains(t, msg, want)
	}
}

func TestCheckSafeCompatibility_AcceptsCurrentReleases(t *testing.T) {
	t.Parallel()

	for _, version := range []string{"safe v1.25.0", "safe v1.30.2", "safe v2.0.0"} {
		// The help text is deliberately silent about the options: a release
		// at or past the minimum is trusted on its version alone.
		safePath := fakeSafe(t, t.TempDir(), version, "safe local")

		err := checkSafeCompatibility(context.Background(), safePath)
		require.NoError(t, err, version)
	}
}

func TestCheckSafeCompatibility_DevelopmentBuildNeedsTheOptions(t *testing.T) {
	t.Parallel()

	capable := fakeSafe(t, t.TempDir(), "safe (development build)", fakeSafeLocalHelp)
	require.NoError(t, checkSafeCompatibility(context.Background(), capable))

	partial := fakeSafe(t, t.TempDir(), "safe (development build)",
		"safe local (--memory|--file dir|--raft dir) [--cluster-port port]")

	err := checkSafeCompatibility(context.Background(), partial)
	require.ErrorIs(t, err, ErrSafeTooOld)
	assert.Contains(t, err.Error(), "development build")
}

// A pre-release of the minimum version may predate the options, so it is
// judged by what its help text offers rather than by its number.
func TestCheckSafeCompatibility_PrereleaseOfMinimumNeedsTheOptions(t *testing.T) {
	t.Parallel()

	early := fakeSafe(t, t.TempDir(), "safe v1.25.0-rc.1", "safe local (--memory|--file dir|--raft dir)")
	require.ErrorIs(t, checkSafeCompatibility(context.Background(), early), ErrSafeTooOld)

	late := fakeSafe(t, t.TempDir(), "safe v1.25.0-rc.2", fakeSafeLocalHelp)
	require.NoError(t, checkSafeCompatibility(context.Background(), late))
}

func TestParseSafeVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		output     string
		version    [3]int
		prerelease bool
		ok         bool
	}{
		{output: "safe v1.24.0\n  commit fb91296\n", version: [3]int{1, 24, 0}, ok: true},
		{output: "safe v1.25.0-rc.1\n", version: [3]int{1, 25, 0}, prerelease: true, ok: true},
		{output: "safe v10.2.33\n", version: [3]int{10, 2, 33}, ok: true},
		{output: "safe (development build)\n", ok: false},
		{output: "", ok: false},
	}

	for _, tc := range tests {
		version, prerelease, ok := parseSafeVersion(tc.output)

		assert.Equal(t, tc.ok, ok, tc.output)

		if tc.ok {
			assert.Equal(t, tc.version, version, tc.output)
			assert.Equal(t, tc.prerelease, prerelease, tc.output)
		}
	}
}

// TestCheckVaultInceptionPrerequisites_RefusesOldSafe proves the version check
// runs before anything else in the inception flow can stop or move a vault.
func TestCheckVaultInceptionPrerequisites_RefusesOldSafe(t *testing.T) {
	bin := t.TempDir()
	fakeSafe(t, bin, "safe v1.24.0", fakeSafeLocalHelp)
	writeFakeExecutable(t, bin, "vault", "exit 0\n")
	writeFakeExecutable(t, bin, "tmux", "exit 0\n")
	writeFakeExecutable(t, bin, "script", "exit 0\n")
	isolateEngineLookup(t, bin)

	_, err := checkVaultInceptionPrerequisites(context.Background(), zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrSafeTooOld)
}

// A safe without --keep-current would make the bloc's target current when
// ocfp registers it, so the prerequisite check refuses it before anything
// runs that could change a target, and names the upgrade.
func TestCheckVaultInceptionPrerequisites_RefusesSafeWithoutKeepCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(fakeSafeNoKeepCurrentEnv, "1")

	bin := t.TempDir()
	_, safeLog := installFakeSafe(t, bin)
	writeFakeExecutable(t, bin, "bao", "exit 0\n")
	writeFakeExecutable(t, bin, "tmux", "exit 0\n")
	writeFakeExecutable(t, bin, "script", "exit 0\n")
	isolateEngineLookup(t, bin)

	require.NoError(t, writeFakeSafeRCFile(&fakeSafeRC{Version: 1, Current: "ops", Vaults: map[string]*fakeSafeVault{
		"ops": {URL: "https://ops.example:8200"},
	}}))

	before, err := os.ReadFile(fakeSafeRCPath())
	require.NoError(t, err)

	_, err = checkVaultInceptionPrerequisites(context.Background(), zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrSafeLacksKeepCurrent)

	for _, want := range []string{"--keep-current", "upgrade", safeReleasesURL} {
		assert.Contains(t, err.Error(), want)
	}

	after, err := os.ReadFile(fakeSafeRCPath())
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))

	for _, call := range readFakeSafeCalls(t, safeLog) {
		assert.Contains(t, []string{"--version", "help"}, call.Args[0], "only the checks ran: %v", call.Args)
	}
}

// Both commands ocfp registers a target with must offer --keep-current.
func TestCheckSafeKeepCurrent_NeedsTargetAndLocal(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"neither":     `printf 'safe %s\n' "$2"`,
		"target only": `case "$2" in target) echo 'safe target name url [--keep-current]' ;; *) echo 'safe local' ;; esac`,
		"local only":  `case "$2" in local) echo 'safe local [--keep-current]' ;; *) echo 'safe target name url' ;; esac`,
	} {
		safePath := writeFakeExecutable(t, t.TempDir(), "safe", body+"\n")
		require.ErrorIs(t, checkSafeKeepCurrent(context.Background(), safePath), ErrSafeLacksKeepCurrent, name)
	}

	both := writeFakeExecutable(t, t.TempDir(), "safe", "echo \"safe $2 [--keep-current]\"\n")
	require.NoError(t, checkSafeKeepCurrent(context.Background(), both))
}

// Both commands that register the bloc's target say in their help that the
// current target stays as it was.
func TestVaultHelp_SaysTheCurrentTargetStays(t *testing.T) {
	for name, cmd := range map[string]string{
		"inception": newVaultInceptionCmd().Long,
		"start":     newVaultStartCmd().Long,
	} {
		long := strings.Join(strings.Fields(cmd), " ")
		assert.Contains(t, long, "--keep-current", name)
		assert.NotContains(t, long, "puts back", name)
	}
}
