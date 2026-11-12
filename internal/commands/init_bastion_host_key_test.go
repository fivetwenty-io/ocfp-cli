package commands

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	bastionssh "github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const changedBastionKeyBanner = `@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
Host key verification failed.
`

// connectivityCheckFixture gives a bloc a key under a temporary data home
// and puts a fake ssh first on PATH. The fake records its arguments, one per
// line, to the returned log file, writes stderr to its own stderr, and exits
// with exitCode. It returns the bloc config and the log path.
func connectivityCheckFixture(t *testing.T, stderr string, exitCode int) (*config.Config, string, string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	blocName := "ocfp-lab-check"
	keyPath := filepath.Join(config.DataHome(), blocName, "ssh", "id_ed25519")
	require.NoError(t, os.MkdirAll(filepath.Dir(keyPath), 0o700))
	writeTestEd25519Key(t, keyPath)

	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "argv.log")
	stderrPath := filepath.Join(t.TempDir(), "stderr.txt")
	require.NoError(t, os.WriteFile(stderrPath, []byte(stderr), 0o600))

	writeFakeExecutableFile(t, filepath.Join(bin, "ssh"),
		"#!/bin/sh\nprintf '%s\\n' \"$@\" >> '"+logPath+"'\ncat '"+stderrPath+"' >&2\nexit "+
			strconv.Itoa(exitCode)+"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{ //nolint:exhaustruct // Only the fields the check reads are set.
		Name:      blocName,
		BastionIP: "10.61.148.10",
		Bastion:   config.Bastion{SSHUser: "ubuntu"}, //nolint:exhaustruct // Only the user is read.
	}

	return cfg, logPath, home
}

func TestCheckBastionConnectivityChecksTheHostKey(t *testing.T) {
	cfg, logPath, home := connectivityCheckFixture(t, "", 0)

	require.NoError(t, checkBastionConnectivity(context.Background(), cfg))

	data, err := os.ReadFile(logPath) // #nosec G304 -- path is a test temp file
	require.NoError(t, err)

	argv := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	assert.Equal(t, strictBastionOptions(home), argv[:4])
	assert.NotContains(t, argv, "UserKnownHostsFile=/dev/null")
	assert.NotContains(t, argv, "StrictHostKeyChecking=no")
	assert.Equal(t, []string{"ubuntu@10.61.148.10", "echo", "connected"}, argv[len(argv)-3:])
}

func TestCheckBastionConnectivityReportsAChangedHostKey(t *testing.T) {
	cfg, _, home := connectivityCheckFixture(t, changedBastionKeyBanner, 255)

	err := checkBastionConnectivity(context.Background(), cfg)

	require.ErrorIs(t, err, bastionssh.ErrHostKeyMismatch)
	assert.Contains(t, err.Error(),
		"ssh-keygen -R 10.61.148.10 -f "+filepath.Join(home, ".ssh", "known_hosts"))
}

func TestCheckBastionConnectivityKeepsOtherFailuresGeneric(t *testing.T) {
	cfg, _, _ := connectivityCheckFixture(t, "ssh: connect to host 10.61.148.10 port 22: Connection refused\n", 255)

	err := checkBastionConnectivity(context.Background(), cfg)

	require.Error(t, err)
	assert.NotErrorIs(t, err, bastionssh.ErrHostKeyMismatch)
	assert.Contains(t, err.Error(), "bastion not accessible")
}

func TestValidatePrerequisitesStopsBastionInitOnAChangedHostKey(t *testing.T) {
	cfg, _, _ := connectivityCheckFixture(t, changedBastionKeyBanner, 255)

	err := validatePrerequisites(context.Background(), cfg, RoleBastion)

	require.ErrorIs(t, err, bastionssh.ErrHostKeyMismatch)
}

func TestValidatePrerequisitesStillWarnsOnAnUnreachableBastion(t *testing.T) {
	cfg, _, _ := connectivityCheckFixture(t, "ssh: connect to host 10.61.148.10 port 22: Connection refused\n", 255)

	require.NoError(t, validatePrerequisites(context.Background(), cfg, RoleBastion))
}
