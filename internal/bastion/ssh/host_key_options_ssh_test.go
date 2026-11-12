package ssh

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// awkwardHome returns a home directory under a temporary directory whose
// name holds the characters ssh treats specially in a file option: a
// %-token, whitespace, both quotes, and a backslash. "${" is left out,
// because ssh cannot be told to read it literally.
func awkwardHome(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), `o p%d 'q' "r" \s`)
}

// sshResolvedConfig runs ssh -G with args and returns the value of key in
// the configuration ssh prints, which is the value after ssh has unquoted
// and expanded it. It skips the test when ssh is not on PATH.
func sshResolvedConfig(t *testing.T, key string, args ...string) string {
	t.Helper()

	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh is not on PATH")
	}

	// -F /dev/null keeps the user's and the system's ssh config out of it.
	full := append([]string{"-F", "/dev/null", "-G"}, args...)
	full = append(full, "host.invalid")

	out, err := exec.Command(sshPath, full...).Output() // #nosec G204 -- test runs the system ssh with test-built arguments
	if err != nil {
		t.Fatalf("ssh -G %q: %v", full, err)
	}

	for _, line := range strings.Split(string(out), "\n") {
		if value, found := strings.CutPrefix(line, key+" "); found {
			return value
		}
	}

	t.Fatalf("ssh -G printed no %s:\n%s", key, out)

	return ""
}

func TestBastionHostKeyOptionsSurviveSSHParsing(t *testing.T) {
	home := awkwardHome(t)
	t.Setenv("HOME", home)

	options, err := BastionHostKeyOptions()
	if err != nil {
		t.Fatalf("BastionHostKeyOptions() error = %v", err)
	}

	want := filepath.Join(home, ".ssh", "known_hosts")

	if got := sshResolvedConfig(t, "userknownhostsfile", options...); got != want {
		t.Errorf("ssh reads UserKnownHostsFile as %q, want %q", got, want)
	}

	if got := sshResolvedConfig(t, "stricthostkeychecking", options...); got != "accept-new" {
		t.Errorf("ssh reads StrictHostKeyChecking as %q, want accept-new", got)
	}
}

func TestBastionHostKeyOptionsRefuseAnEnvironmentReference(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "a${HOME}b"))

	options, err := BastionHostKeyOptions()
	if !errors.Is(err, ErrKnownHostsPathUnusable) {
		t.Fatalf("BastionHostKeyOptions() = %q, %v; want ErrKnownHostsPathUnusable", options, err)
	}

	if !strings.Contains(err.Error(), "${") {
		t.Errorf("error %q does not say what is wrong with the path", err)
	}
}
