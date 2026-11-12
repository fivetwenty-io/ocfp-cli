package commands

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findProxyCommand returns the ProxyCommand option in argv, or "" when
// there is none.
func findProxyCommand(argv []string) string {
	for _, arg := range argv {
		if command, found := strings.CutPrefix(arg, "ProxyCommand="); found {
			return command
		}
	}

	return ""
}

func TestArtifactsRemoteScriptChecksTheBastionHop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logPath := fakeRecordingCommands(t)

	err := runArtifactsRemoteScript("ubuntu", "10.0.0.9", "/tmp/id_ed25519", "10.61.148.10",
		"/tmp/provision.sh", "A=1", logger.WithOperation("test"))
	require.NoError(t, err)

	runs := recordedRuns(t, logPath)
	require.Len(t, runs, 3)

	hop := mustProxyCommand(t, "/tmp/id_ed25519", "ubuntu", "10.61.148.10", true)
	require.Contains(t, hop, "-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="+
		filepath.Join(home, ".ssh", "known_hosts"))

	for i, name := range []string{"scp", "ssh", "ssh"} {
		require.GreaterOrEqual(t, len(runs[i]), 5, "run %d: %q", i, runs[i])
		assert.Equal(t, []string{name, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"},
			runs[i][:5], "run %d keeps the artifacts VM relaxed", i)
		assert.Equal(t, hop, findProxyCommand(runs[i]), "run %d", i)

		for _, arg := range runs[i] {
			assert.NotContains(t, arg, "ProxyJump", "run %d", i)
		}
	}
}

func TestArtifactsRemoteScriptWithoutABastionConnectsDirectly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	logPath := fakeRecordingCommands(t)

	err := runArtifactsRemoteScript("ubuntu", "10.0.0.9", "/tmp/id_ed25519", "",
		"/tmp/provision.sh", "A=1", logger.WithOperation("test"))
	require.NoError(t, err)

	runs := recordedRuns(t, logPath)
	require.Len(t, runs, 3)

	for i, run := range runs {
		assert.Empty(t, findProxyCommand(run), "run %d", i)
		assert.Contains(t, run, "StrictHostKeyChecking=no", "run %d", i)
	}
}
