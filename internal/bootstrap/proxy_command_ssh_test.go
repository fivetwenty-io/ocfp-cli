package bootstrap

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// realSSH returns the path of the system ssh, or skips the test when there
// is none.
func realSSH(t *testing.T) string {
	t.Helper()

	path, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh is not on PATH")
	}

	return path
}

// proxyCommandInnerArgv hands proxyCommand to the system ssh as its
// ProxyCommand and returns the arguments the inner ssh starts with, after
// the outer ssh has expanded its %-tokens and the shell has split it. A
// fake ssh first on PATH stands in for the inner one: it records its
// arguments and exits, so nothing is dialled. The outer ssh reads no ssh
// config and records no host key.
func proxyCommandInnerArgv(t *testing.T, proxyCommand string) []string {
	t.Helper()

	sshPath := realSSH(t)

	bin := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "argv")

	fake := "#!/bin/sh\nfor a; do printf '%s\\0' \"$a\"; done > '" + argvLog + "'\nexit 1\n"

	err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fake), 0o700) //nolint:gosec // test executable
	if err != nil {
		t.Fatal(err)
	}

	outer := exec.Command(sshPath, //nolint:gosec // test runs the system ssh with test-built arguments
		"-F", "/dev/null",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ProxyCommand="+proxyCommand,
		"-p", "2222",
		"ubuntu@artifacts.invalid", "true")
	outer.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "SHELL=/bin/sh")

	_ = outer.Run() // The fake inner ssh exits at once, so the outer one fails.

	data, err := os.ReadFile(argvLog) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatalf("the inner ssh never ran: %v", err)
	}

	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}

// resolvedSSHConfig runs ssh -G over args and returns what ssh prints for
// each of keys, after it has unquoted and expanded the values.
func resolvedSSHConfig(t *testing.T, args []string, keys ...string) map[string]string {
	t.Helper()

	full := append([]string{"-F", "/dev/null", "-G"}, args...)

	var stdout bytes.Buffer

	cmd := exec.Command(realSSH(t), full...) //nolint:gosec // test runs the system ssh with test-built arguments
	cmd.Stdout = &stdout

	err := cmd.Run()
	if err != nil {
		t.Fatalf("ssh -G %q: %v", full, err)
	}

	got := map[string]string{}

	for _, line := range strings.Split(stdout.String(), "\n") {
		for _, key := range keys {
			if value, found := strings.CutPrefix(line, key+" "); found {
				got[key] = value
			}
		}
	}

	return got
}

// The jump hop's known_hosts path passes through two rounds of %-token
// expansion, one in the outer ssh and one in the inner, and a shell in
// between. The inner ssh must still check the real file, whatever the home
// directory holds.
func TestBastionProxyCommandInnerSSHChecksTheRealKnownHosts(t *testing.T) {
	home := filepath.Join(t.TempDir(), `o p%d 'q' "r" \s`)
	t.Setenv("HOME", home)

	for _, keyPath := range []string{"", "/tmp/id_ed25519"} {
		argv := proxyCommandInnerArgv(t, mustProxyCommand(t, keyPath, "ubuntu", "100.109.226.53", true))

		if !strings.Contains(strings.Join(argv, "\n"), "-W\nartifacts.invalid:2222\nubuntu@100.109.226.53") {
			t.Errorf("key %q: the inner ssh does not relay to the target: %q", keyPath, argv)
		}

		got := resolvedSSHConfig(t, argv, "stricthostkeychecking", "userknownhostsfile", "user", "hostname")
		want := map[string]string{
			"stricthostkeychecking": "accept-new",
			"userknownhostsfile":    filepath.Join(home, ".ssh", "known_hosts"),
			"user":                  "ubuntu",
			"hostname":              "100.109.226.53",
		}

		for key, value := range want {
			if got[key] != value {
				t.Errorf("key %q: the inner ssh reads %s as %q, want %q", keyPath, key, got[key], value)
			}
		}
	}
}
