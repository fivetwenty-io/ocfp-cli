package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
)

// testHome points HOME at a temporary directory, so the bastion hop names a
// known_hosts file the test controls, and returns that file's path.
func testHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	return filepath.Join(home, ".ssh", "known_hosts")
}

// mustProvisionArgs is buildArtifactsProvisionSSHArgs for a test, which
// fails on an error.
func mustProvisionArgs(t *testing.T, c artifactsProvisionConn) []string {
	t.Helper()

	args, err := buildArtifactsProvisionSSHArgs(c)
	if err != nil {
		t.Fatalf("buildArtifactsProvisionSSHArgs() error = %v", err)
	}

	return args
}

// mustProxyCommand is BastionProxyCommand for a test, which fails on an
// error.
func mustProxyCommand(t *testing.T, keyPath, user, bastionHost string, batchMode bool) string {
	t.Helper()

	command, err := BastionProxyCommand(keyPath, user, bastionHost, batchMode)
	if err != nil {
		t.Fatalf("BastionProxyCommand() error = %v", err)
	}

	return command
}

func testProvisionConn() artifactsProvisionConn {
	return artifactsProvisionConn{
		KeyPath:       "/home/op/.ocfp/ocfp-lab-wayne/ssh/id_ed25519",
		User:          "ubuntu",
		BastionHost:   "100.109.226.53",
		ArtifactsHost: "10.64.64.11",
	}
}

func TestBuildArtifactsProvisionSSHArgs_ProxyJumpThroughBastion(t *testing.T) {
	knownHosts := testHome(t)

	args := mustProvisionArgs(t, testProvisionConn())
	joined := strings.Join(args, " ")

	// The bastion hop is an explicit ProxyCommand (not -o ProxyJump), because
	// options on the command line apply only to the destination. It must
	// still route to the bastion and relay to the artifacts host (-W %h:%p).
	if !strings.Contains(joined, "ProxyCommand=ssh ") {
		t.Errorf("args must reach the bastion via an explicit ProxyCommand, got: %s", joined)
	}

	if !strings.Contains(joined, "-W %h:%p ubuntu@100.109.226.53") {
		t.Errorf("ProxyCommand must netcat through the bastion, got: %s", joined)
	}

	// The jump hop checks the bastion's host key against the real
	// known_hosts, so a changed bastion key stops provisioning.
	if !strings.Contains(joined, "ProxyCommand=ssh -i /home/op/.ocfp/ocfp-lab-wayne/ssh/id_ed25519 -o BatchMode=yes"+
		" -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="+knownHosts) {
		t.Errorf("ProxyCommand must check the bastion's host key strictly, got: %s", joined)
	}

	if !strings.Contains(joined, "ubuntu@10.64.64.11") {
		t.Errorf("args must target the artifacts host, got: %s", joined)
	}
}

// The artifacts VM sits behind the bastion and is rebuilt with each
// bootstrap, so the final hop to it keeps the relaxed host-key options.
func TestBuildArtifactsProvisionSSHArgs_ArtifactsHopStaysRelaxed(t *testing.T) {
	testHome(t)

	args := mustProvisionArgs(t, testProvisionConn())
	joined := strings.Join(args, " ")

	if !strings.HasPrefix(joined, "-i /home/op/.ocfp/ocfp-lab-wayne/ssh/id_ed25519 -o BatchMode=yes"+
		" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null") {
		t.Errorf("the artifacts hop must keep relaxed host-key options, got: %s", joined)
	}
}

func TestBuildArtifactsProvisionSSHArgs_UsesKeyAndBatchMode(t *testing.T) {
	testHome(t)

	args := mustProvisionArgs(t, testProvisionConn())
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-i /home/op/.ocfp/ocfp-lab-wayne/ssh/id_ed25519") {
		t.Errorf("args must pass the identity key, got: %s", joined)
	}

	// Non-interactive: must not hang waiting for a password or host-key prompt.
	if !strings.Contains(joined, "BatchMode=yes") {
		t.Errorf("args must set BatchMode=yes, got: %s", joined)
	}

	if !strings.Contains(joined, "StrictHostKeyChecking") {
		t.Errorf("args must set a StrictHostKeyChecking policy, got: %s", joined)
	}
}

func TestBuildArtifactsProvisionSSHArgs_RemoteCommandLast(t *testing.T) {
	testHome(t)

	args := mustProvisionArgs(t, testProvisionConn())

	// The remote command must be the final argument so the script can be
	// piped to it over stdin (sudo bash -s).
	last := args[len(args)-1]
	if !strings.Contains(last, "sudo bash -s") {
		t.Errorf("final arg must be the remote command 'sudo bash -s', got: %q", last)
	}

	// The destination must come before the remote command.
	destIdx, cmdIdx := -1, len(args)-1
	for i, a := range args {
		if a == "ubuntu@10.64.64.11" {
			destIdx = i
		}
	}

	if destIdx == -1 || destIdx >= cmdIdx {
		t.Errorf("destination must precede the remote command; args: %v", args)
	}
}

func TestBastionProxyCommand_StrictJumpHop(t *testing.T) {
	knownHosts := testHome(t)

	got := mustProxyCommand(t, "/home/op/.ocfp/ocfp-lab-wayne/ssh/id_ed25519", "ubuntu", "100.109.226.53", false)

	want := "ssh -i /home/op/.ocfp/ocfp-lab-wayne/ssh/id_ed25519" +
		" -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + knownHosts +
		" -o IdentitiesOnly=yes -o LogLevel=ERROR -o ConnectTimeout=15" +
		" -W %h:%p ubuntu@100.109.226.53"

	if got != want {
		t.Errorf("BastionProxyCommand:\n got: %s\nwant: %s", got, want)
	}
}

func TestBastionProxyCommand_WithoutKeyLeavesAuthToTheUser(t *testing.T) {
	knownHosts := testHome(t)

	got := mustProxyCommand(t, "", "ubuntu", "100.109.226.53", false)

	want := "ssh -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + knownHosts +
		" -o LogLevel=ERROR -o ConnectTimeout=15" +
		" -W %h:%p ubuntu@100.109.226.53"

	if got != want {
		t.Errorf("BastionProxyCommand:\n got: %s\nwant: %s", got, want)
	}
}

// Without a key, a non-interactive run must not let the jump hop prompt for
// a passphrase or password, so it gets BatchMode=yes.
func TestBastionProxyCommand_WithoutKeyInBatchModeNeverPrompts(t *testing.T) {
	knownHosts := testHome(t)

	got := mustProxyCommand(t, "", "ubuntu", "100.109.226.53", true)

	want := "ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + knownHosts +
		" -o LogLevel=ERROR -o ConnectTimeout=15" +
		" -W %h:%p ubuntu@100.109.226.53"

	if got != want {
		t.Errorf("BastionProxyCommand:\n got: %s\nwant: %s", got, want)
	}
}

// With a key the jump hop is always in batch mode, whatever the caller says.
func TestBastionProxyCommand_WithKeyIsAlwaysBatchMode(t *testing.T) {
	testHome(t)

	key := "/home/op/.ocfp/ocfp-lab-wayne/ssh/id_ed25519"
	if mustProxyCommand(t, key, "ubuntu", "100.109.226.53", false) != mustProxyCommand(t, key, "ubuntu", "100.109.226.53", true) {
		t.Error("the keyed jump hop must not depend on batchMode")
	}
}

func TestBuildArtifactsProvisionSSHArgs_SharesBastionProxyCommand(t *testing.T) {
	testHome(t)

	c := testProvisionConn()
	args := mustProvisionArgs(t, c)

	want := "ProxyCommand=" + mustProxyCommand(t, c.KeyPath, c.User, c.BastionHost, true)

	found := false

	for _, a := range args {
		if a == want {
			found = true
		}
	}

	if !found {
		t.Errorf("provision args must hop through BastionProxyCommand %q; args: %v", want, args)
	}
}

// fakeHostKeySSH puts an ssh on PATH that records each argv to a log and,
// for any remote command other than the readiness probe "true" (or for every
// command when refuseProbe is set), prints OpenSSH's changed-key refusal
// and exits 255. It returns the log's path.
func fakeHostKeySSH(t *testing.T, refuseProbe bool) string {
	t.Helper()

	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")

	probe := `[ "$last" = true ] && exit 0`
	if refuseProbe {
		probe = ":"
	}

	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + argvLog + "'\n" +
		"for last; do :; done\n" +
		probe + "\n" +
		"echo '@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @' >&2\n" +
		"echo 'Host key for 100.109.226.53 has changed and you have requested strict checking.' >&2\n" +
		"echo 'Host key verification failed.' >&2\n" +
		"exit 255\n"

	err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o700) //nolint:gosec // test executable
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir)

	return argvLog
}

func argvLines(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// A refused bastion key cannot change by waiting, so the readiness loop
// stops at once with an error that matches ErrHostKeyMismatch.
func TestWaitArtifactsSSHReadyStopsOnABastionHostKeyRefusal(t *testing.T) {
	knownHosts := testHome(t)
	argvLog := fakeHostKeySSH(t, true)

	m := &Manager{}

	err := m.waitArtifactsSSHReady(context.Background(), testProvisionConn())
	if !errors.Is(err, ssh.ErrHostKeyMismatch) {
		t.Fatalf("waitArtifactsSSHReady() = %v, want an error matching ErrHostKeyMismatch", err)
	}

	if !strings.Contains(err.Error(), "ssh-keygen -R 100.109.226.53 -f "+knownHosts) {
		t.Errorf("error %q does not name the bastion's known_hosts entry", err)
	}

	lines := argvLines(t, argvLog)
	if len(lines) != 1 {
		t.Fatalf("ssh ran %d times, want once: %q", len(lines), lines)
	}

	if !strings.Contains(lines[0], "-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="+knownHosts+" ") {
		t.Errorf("ssh was not given the strict bastion hop: %s", lines[0])
	}
}

func TestProvisionArtifactsOverSSHReportsABastionHostKeyRefusal(t *testing.T) {
	testHome(t)
	fakeHostKeySSH(t, false)

	m := &Manager{}

	err := m.provisionArtifactsOverSSH(context.Background(), testProvisionConn(), "echo provisioned\n")
	if !errors.Is(err, ssh.ErrHostKeyMismatch) {
		t.Fatalf("provisionArtifactsOverSSH() = %v, want an error matching ErrHostKeyMismatch", err)
	}
}
