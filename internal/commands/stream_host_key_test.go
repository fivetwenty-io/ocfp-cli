package commands

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	bastionssh "github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
)

// refusalBanner prints what ssh prints when it refuses the bastion's key.
const refusalBanner = "echo '@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @' >&2\n" +
	"echo 'Host key for 100.109.226.53 has changed and you have requested strict checking.' >&2\n" +
	"echo 'Host key verification failed.' >&2\n"

// nestedRefusal prints what an ssh the bastion runs prints when it refuses
// another host's key, which reaches us as the remote command's stderr.
const nestedRefusal = "echo '@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @' >&2\n" +
	"echo 'Offending ECDSA key in /home/ubuntu/.ssh/known_hosts:3' >&2\n" +
	"echo 'Host key for 10.64.64.20 has changed and you have requested strict checking.' >&2\n" +
	"echo 'Host key verification failed.' >&2\n"

// fakeOnPath puts an executable called name first on PATH that runs body.
func fakeOnPath(t *testing.T, name, body string) {
	t.Helper()

	dir := t.TempDir()
	writeFakeExecutableFile(t, filepath.Join(dir, name), "#!/bin/sh\n"+body)
	t.Setenv("PATH", dir+":/usr/bin:/bin")
}

// The streaming commands show ssh's stderr as it comes, and still turn a
// refusal of the bastion's host key into the mismatch error with its hint,
// whatever non-zero status the command exits with. rsync can exit 12 when
// ssh refuses the key, and a wrapper can pick its own status.
func TestStreamingCommandsReportABastionHostKeyRefusal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hint := "ssh-keygen -R 100.109.226.53 -f " + filepath.Join(home, ".ssh", "known_hosts")

	runs := []struct {
		name string
		exit string
		run  func() error
	}{
		{"ssh", "255", func() error { return executeSSH(context.Background(), []string{"ssh", "x"}, "100.109.226.53") }},
		{"scp", "255", func() error { return executeSCP(context.Background(), []string{"scp", "a", "b"}, "100.109.226.53") }},
		{"legacy scp", "1", func() error { return executeSCP(context.Background(), []string{"scp", "a", "b"}, "100.109.226.53") }},
		{"rsync", "255", func() error { return executeRSync(context.Background(), []string{"rsync", "a", "b"}, "100.109.226.53") }},
		{"rsync", "12", func() error { return executeRSync(context.Background(), []string{"rsync", "a", "b"}, "100.109.226.53") }},
		{"ssh", "1", func() error { return executeSSH(context.Background(), []string{"ssh", "x"}, "100.109.226.53") }},
	}

	for _, tt := range runs {
		fakeOnPath(t, strings.Fields(tt.name)[len(strings.Fields(tt.name))-1], refusalBanner+"exit "+tt.exit+"\n")

		err := tt.run()
		if !errors.Is(err, bastionssh.ErrHostKeyMismatch) {
			t.Errorf("%s: got %v, want an error matching ErrHostKeyMismatch", tt.name, err)

			continue
		}

		if !strings.Contains(err.Error(), hint) {
			t.Errorf("%s: error %q does not say how to clear the entry", tt.name, err)
		}
	}
}

// Only a refusal of the bastion's own key counts. An ssh the remote end
// runs can refuse another host's key, whatever status it exits with, and
// a failure without the words, or one that never reached the bastion, is
// an ordinary failure.
func TestStreamingSSHReportsOnlyItsOwnRefusal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	runs := []struct {
		name    string
		body    string
		bastion string
	}{
		{"RemoteSSHRefusedAnotherHost", nestedRefusal + "exit 255\n", "100.109.226.53"},
		{"RemoteSSHRefusedAnotherHostWithItsOwnStatus", nestedRefusal + "exit 1\n", "100.109.226.53"},
		{"BareMarkersOnly", "echo 'Host key verification failed.' >&2\nexit 255\n", "100.109.226.53"},
		{"ExitWithoutTheWords", "echo 'Connection refused' >&2\nexit 255\n", "100.109.226.53"},
		{"NoBastionOnTheRoute", refusalBanner + "exit 255\n", ""},
	}

	for _, tt := range runs {
		fakeOnPath(t, "ssh", tt.body)

		err := executeSSH(context.Background(), []string{"ssh", "x"}, tt.bastion)
		if err == nil || errors.Is(err, bastionssh.ErrHostKeyMismatch) {
			t.Errorf("%s: got %v, want an ordinary failure", tt.name, err)
		}
	}
}

// stderr reaches the terminal whole while the tail kept for the check
// stays small, and stdout is left alone.
func TestRunShowingStderrTeesOnlyStderr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	writeFakeExecutableFile(t, filepath.Join(dir, "noisy"), "#!/bin/sh\n"+
		"echo out\n"+
		"i=0; while [ $i -lt 2000 ]; do echo 'progress line that fills the buffer' >&2; i=$((i+1)); done\n"+
		refusalBanner+"exit 255\n")

	var stdout, terminal bytes.Buffer

	cmd := exec.Command(filepath.Join(dir, "noisy")) //nolint:gosec // test-owned path
	cmd.Stdout = &stdout

	err := runShowingStderr(cmd, &terminal, "100.109.226.53")
	if !errors.Is(err, bastionssh.ErrHostKeyMismatch) {
		t.Fatalf("got %v, want an error matching ErrHostKeyMismatch", err)
	}

	if stdout.String() != "out\n" {
		t.Errorf("stdout = %q, want it untouched", stdout.String())
	}

	if got := strings.Count(terminal.String(), "progress line"); got != 2000 {
		t.Errorf("the terminal saw %d progress lines, want all 2000", got)
	}

	if !strings.HasSuffix(terminal.String(), "Host key verification failed.\n") {
		t.Error("the terminal did not see ssh's refusal")
	}
}

func TestStderrTailIsBounded(t *testing.T) {
	t.Parallel()

	tail := newStderrTail()

	for range 100 {
		_, _ = tail.Write(bytes.Repeat([]byte("x"), 1000))
	}

	_, _ = tail.Write([]byte("end"))

	if got := tail.String(); len(got) > stderrTailLimit || !strings.HasSuffix(got, "end") {
		t.Errorf("tail holds %d bytes ending %q, want at most %d ending in the last write", len(got), got[len(got)-3:], stderrTailLimit)
	}
}
