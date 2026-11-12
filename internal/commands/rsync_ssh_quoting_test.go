package commands

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rsync splits the -e command into words itself, with rules of its own
// rather than a shell's. Running the real rsync against a fake ssh shows
// the words it hands ssh, and ssh -G shows what ssh makes of them.
func TestRSyncHandsSSHTheKnownHostsAndKeyPathsWhole(t *testing.T) {
	rsyncPath, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not on PATH")
	}

	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh is not on PATH")
	}

	home := filepath.Join(t.TempDir(), `o p%d 'q' "r" \s`)
	t.Setenv("HOME", home)

	keyPath := filepath.Join(t.TempDir(), `my key's "dir"`, "id_ed25519")

	argv := mustRSyncCommand(t, &rsyncConfig{}, //nolint:exhaustruct // defaults suffice
		t.TempDir()+"/", "ubuntu@10.61.148.10:/remote/dir/", keyPath)

	bin := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "argv")
	fake := "#!/bin/sh\nfor a; do printf '%s\\0' \"$a\"; done > '" + argvLog + "'\nexit 1\n"

	err = os.WriteFile(filepath.Join(bin, "ssh"), []byte(fake), 0o700) //nolint:gosec // test executable
	if err != nil {
		t.Fatal(err)
	}

	rsync := exec.Command(rsyncPath, argv[1:]...) //nolint:gosec // test runs rsync with test-built arguments
	rsync.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")

	out, _ := rsync.CombinedOutput() // The fake ssh exits at once, so rsync fails.

	data, err := os.ReadFile(argvLog) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatalf("rsync never ran ssh: %v\n%s", err, out)
	}

	sshArgs := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")

	at := indexOf(sshArgs, "-i")
	if at == -1 || at+1 >= len(sshArgs) || sshArgs[at+1] != keyPath {
		t.Errorf("ssh got %q, want -i %q", sshArgs, keyPath)
	}

	// ssh -G resolves the options up to the host; the remote command rsync
	// appends after it does not matter here.
	host := indexOf(sshArgs, "10.61.148.10")
	if host == -1 {
		host = indexOf(sshArgs, "ubuntu@10.61.148.10")
	}

	if host == -1 {
		t.Fatalf("ssh got no host: %q", sshArgs)
	}

	var stdout bytes.Buffer

	resolve := exec.Command(sshPath, //nolint:gosec // test runs the system ssh with test-built arguments
		append([]string{"-F", "/dev/null", "-G"}, sshArgs[:host+1]...)...)
	resolve.Stdout = &stdout

	err = resolve.Run()
	if err != nil {
		t.Fatalf("ssh -G %q: %v", sshArgs[:host+1], err)
	}

	want := "userknownhostsfile " + filepath.Join(home, ".ssh", "known_hosts")
	if !strings.Contains(stdout.String(), want+"\n") {
		t.Errorf("ssh resolved the known_hosts file differently, want %q in:\n%s", want, stdout.String())
	}
}
