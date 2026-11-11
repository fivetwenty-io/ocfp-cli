package ssh

import (
	"errors"
	"net"
	"os"
	"slices"
	"strings"
	"testing"

	"go.uber.org/zap"
	gossh "golang.org/x/crypto/ssh"
)

var fallbackRemote = &net.TCPAddr{IP: net.IPv4(10, 0, 0, 5), Port: DefaultSSHPort}

func newFallbackTestClient() *Client {
	c := NewClient(&ConnectionDetails{Host: "10.0.0.5", Port: DefaultSSHPort, User: "tester"}, &ProvisioningOptions{})
	c.log = zap.NewNop().Sugar()

	return c
}

func TestFallbackHostKeyCallbackRefusesChangedKey(t *testing.T) {
	knownHosts := isolateSSHEnv(t)
	writeKnownHosts(t, knownHosts, "10.0.0.5:22", newEd25519Signer(t).PublicKey())

	before := readFile(t, knownHosts)

	err := newFallbackTestClient().createFallbackHostKeyCallback()("10.0.0.5:22", fallbackRemote, newEd25519Signer(t).PublicKey())
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("fallback callback error = %v, want ErrHostKeyMismatch", err)
	}

	if after := readFile(t, knownHosts); after != before {
		t.Errorf("fallback callback rewrote known_hosts on a mismatch:\nbefore: %q\nafter:  %q", before, after)
	}
}

func TestFallbackHostKeyCallbackRecordsUnknownHost(t *testing.T) {
	knownHosts := isolateSSHEnv(t)
	key := newEd25519Signer(t).PublicKey()

	err := newFallbackTestClient().createFallbackHostKeyCallback()("10.0.0.5:22", fallbackRemote, key)
	if err != nil {
		t.Fatalf("fallback callback refused a host with no entry: %v", err)
	}

	want := "10.0.0.5 " + strings.TrimSpace(string(gossh.MarshalAuthorizedKey(key))) + "\n"
	if got := readFile(t, knownHosts); got != want {
		t.Errorf("known_hosts = %q, want %q", got, want)
	}
}

func TestFallbackHostKeyCallbackAcceptsRecordedKey(t *testing.T) {
	knownHosts := isolateSSHEnv(t)
	key := newEd25519Signer(t).PublicKey()
	writeKnownHosts(t, knownHosts, "10.0.0.5:22", key)

	before := readFile(t, knownHosts)

	err := newFallbackTestClient().createFallbackHostKeyCallback()("10.0.0.5:22", fallbackRemote, key)
	if err != nil {
		t.Fatalf("fallback callback refused the recorded key: %v", err)
	}

	if after := readFile(t, knownHosts); after != before {
		t.Errorf("known_hosts changed for a recorded key:\nbefore: %q\nafter:  %q", before, after)
	}
}

func TestFallbackHostKeyCallbackRefusesUnreadableKnownHosts(t *testing.T) {
	knownHosts := isolateSSHEnv(t)
	writeKnownHosts(t, knownHosts, "10.0.0.5:22")

	err := os.WriteFile(knownHosts, []byte("10.0.0.5 ssh-ed25519 not-base64!\n"), privateKeyMode)
	if err != nil {
		t.Fatal(err)
	}

	err = newFallbackTestClient().createFallbackHostKeyCallback()("10.0.0.5:22", fallbackRemote, newEd25519Signer(t).PublicKey())
	if err == nil {
		t.Fatal("fallback callback trusted a key it could not check against a corrupt known_hosts")
	}
}

// The external probe must check the host against the same known_hosts file
// the Go client uses, accepting a new host but never a changed one.
func TestExternalProbeChecksKnownHosts(t *testing.T) {
	t.Parallel()

	c := newTestClient("")
	args := c.externalProbeArgs("/home/op/.ssh/known_hosts")

	for _, want := range []string{"StrictHostKeyChecking=accept-new", "UserKnownHostsFile=/home/op/.ssh/known_hosts"} {
		if !slices.Contains(args, want) {
			t.Errorf("probe args %v lack %q", args, want)
		}
	}

	for _, banned := range []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null"} {
		if slices.Contains(args, banned) {
			t.Errorf("probe args %v contain %q", args, banned)
		}
	}
}

// File transfers to the bastion must check its host key against the same
// known_hosts file as the SSH client.
func TestSCPArgsCheckKnownHosts(t *testing.T) {
	knownHosts := isolateSSHEnv(t)

	args := newTestTransferManager("").buildSCPArgs("local", "/remote", true)

	for _, want := range []string{"StrictHostKeyChecking=accept-new", "UserKnownHostsFile=" + knownHosts} {
		if !slices.Contains(args, want) {
			t.Errorf("scp args %v lack %q", args, want)
		}
	}

	for _, banned := range []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null"} {
		if slices.Contains(args, banned) {
			t.Errorf("scp args %v contain %q", args, banned)
		}
	}
}
