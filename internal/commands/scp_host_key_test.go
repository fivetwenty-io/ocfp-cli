package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strictBastionOptions returns the host key options a connection to the
// bastion must carry once HOME is home.
func strictBastionOptions(home string) []string {
	return []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + filepath.Join(home, ".ssh", "known_hosts"),
	}
}

// fakeRecordingCommands puts fake scp and ssh executables first on PATH.
// Each appends its name and arguments, one per line and followed by a
// blank line, to the returned log file and exits 0.
func fakeRecordingCommands(t *testing.T) string {
	t.Helper()

	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "argv.log")

	for _, name := range []string{"scp", "ssh"} {
		writeFakeExecutableFile(t, filepath.Join(bin, name),
			"#!/bin/sh\n{ echo "+name+"; printf '%s\\n' \"$@\"; echo; } >> '"+logPath+"'\nexit 0\n")
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	return logPath
}

// recordedRuns reads the log fakeRecordingCommands writes and returns the
// argv of each run, the command name first.
func recordedRuns(t *testing.T, logPath string) [][]string {
	t.Helper()

	data, err := os.ReadFile(logPath) // #nosec G304 -- path is a test temp file
	require.NoError(t, err)

	var runs [][]string

	for _, block := range strings.Split(strings.TrimRight(string(data), "\n"), "\n\n") {
		runs = append(runs, strings.Split(block, "\n"))
	}

	return runs
}

func TestPrepareSCPCommandChecksTheBastionHostKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &scpConfig{
		BlocName:    "ocfp-cf1-lab",
		User:        "ubuntu",
		KeyPath:     "/tmp/id_ed25519",
		Recursive:   false,
		Options:     "-o StrictHostKeyChecking=no",
		Source:      "./file.txt",
		Destination: "bastion:/tmp/file.txt",
	}

	got := mustSCPCommand(t, cfg, "10.61.148.10", "/tmp/id_ed25519")

	require.GreaterOrEqual(t, len(got), 5)
	assert.Equal(t, append([]string{"scp"}, strictBastionOptions(home)...), got[:5])
	assert.Equal(t, "ubuntu@10.61.148.10:/tmp/file.txt", got[len(got)-1])
	assert.NotContains(t, got, "UserKnownHostsFile=/dev/null")
}

func TestBastionScriptRunChecksTheBastionHostKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logPath := fakeRecordingCommands(t)

	bastion := &BastionContext{
		IP:           "10.61.148.10",
		User:         "ubuntu",
		SSHKeyOption: "/tmp/id_ed25519",
	}
	log := logger.WithOperation("test")

	require.NoError(t, copyScriptToBastion(bastion, "/tmp/script.pl", "/tmp/remote.pl", log))
	require.NoError(t, executeRemoteScript(bastion, "/tmp/remote.pl", "A=1", "test", "/tmp/run.log", log))
	cleanupRemoteScript(bastion, "/tmp/remote.pl", log)

	runs := recordedRuns(t, logPath)
	require.Len(t, runs, 3)

	for i, name := range []string{"scp", "ssh", "ssh"} {
		require.GreaterOrEqual(t, len(runs[i]), 5, "run %d: %q", i, runs[i])
		assert.Equal(t, append([]string{name}, strictBastionOptions(home)...), runs[i][:5], "run %d", i)
		assert.NotContains(t, runs[i], "UserKnownHostsFile=/dev/null", "run %d", i)
	}
}
