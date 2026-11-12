package ssh

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// changedKeyOutput is what OpenSSH prints at LogLevel=ERROR when the key a
// host presents differs from the one known_hosts records for it, with the
// carriage returns ssh puts on most lines.
func changedKeyOutput(knownHosts, host string) string {
	return "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\n" +
		"@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\n" +
		"@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\n" +
		"IT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!\r\n" +
		"Add correct host key in " + knownHosts + " to get rid of this message.\r\n" +
		"Offending ED25519 key in " + knownHosts + ":1\r\n" +
		"Host key for " + host + " has changed and you have requested strict checking.\r\n" +
		"Host key verification failed.\r\n"
}

func TestSystemSSHHostKeyErrorMatchesErrHostKeyMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	knownHosts := filepath.Join(home, ".ssh", "known_hosts")
	cause := &exec.ExitError{}

	err := SystemSSHHostKeyFailure(changedKeyOutput(knownHosts, "10.0.0.4"), "10.0.0.4", cause)
	if err == nil {
		t.Fatal("SystemSSHHostKeyFailure() = nil for a changed key")
	}

	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("errors.Is(%v, ErrHostKeyMismatch) = false", err)
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error %v does not wrap the ssh exit error", err)
	}

	if !strings.Contains(err.Error(), "ssh-keygen -R 10.0.0.4 -f "+knownHosts) {
		t.Fatalf("error %q does not say how to clear the stale entry", err)
	}
}

// The hint names the entry the way ssh printed it, so it is right for a
// non-default port and for a name other than the one we dialled.
func TestSystemSSHHostKeyFailureNamesTheEntrySSHPrinted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	knownHosts := filepath.Join(home, ".ssh", "known_hosts")

	tests := []struct {
		name    string
		printed string
		bastion string
		want    string
	}{
		{"NonDefaultPort", "[10.0.0.4]:2222", "[10.0.0.4]:2222", "ssh-keygen -R '[10.0.0.4]:2222' -f " + knownHosts},
		{"IPv6", "[fd00::10]:2222", "[FD00::0:10]:2222", "ssh-keygen -R '[fd00::10]:2222' -f " + knownHosts},
		{"IPv6OnPort22", "fd00::10", "fd00:0::10", "ssh-keygen -R fd00::10 -f " + knownHosts},
		{"Alias", "lab-jump", "100.109.226.53", "ssh-keygen -R lab-jump -f " + knownHosts},
	}

	for _, tt := range tests {
		err := SystemSSHHostKeyFailure(changedKeyOutput(knownHosts, tt.printed), tt.bastion, nil)
		if !errors.Is(err, ErrHostKeyMismatch) {
			t.Errorf("%s: got %v, want an error matching ErrHostKeyMismatch", tt.name, err)

			continue
		}

		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error %q does not contain %q", tt.name, err, tt.want)
		}
	}
}

func TestSystemSSHHostKeyFailureReportsARevokedKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output := "@       WARNING: REVOKED HOST KEY DETECTED!               @\r\n" +
		"The ED25519 host key for 10.0.0.4 is marked as revoked.\r\n" +
		"ED25519 host key for 10.0.0.4 was revoked and you have requested strict checking.\r\n" +
		"Host key verification failed.\r\n"

	err := SystemSSHHostKeyFailure(output, "10.0.0.4", nil)
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("got %v, want an error matching ErrHostKeyMismatch", err)
	}

	if !strings.Contains(err.Error(), "marks as revoked") {
		t.Errorf("error %q does not say the key is revoked", err)
	}

	if !strings.Contains(err.Error(), "ssh-keygen -R 10.0.0.4 -f "+filepath.Join(home, ".ssh", "known_hosts")) {
		t.Errorf("error %q does not say how to clear the entry", err)
	}
}

// Only ssh's own lines about the bastion count. The same words from a
// command the bastion ran, such as an ssh it started to another host, are
// that command's output.
func TestSystemSSHHostKeyFailureIgnoresOtherFailures(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ourKnownHosts := filepath.Join(home, ".ssh", "known_hosts")

	for name, output := range map[string]string{
		"Empty":              "",
		"ConnectionRefused":  "ssh: connect to host 10.0.0.4 port 22: Connection refused\n",
		"PermissionDenied":   "ubuntu@10.0.0.4: Permission denied (publickey).\n",
		"BareMarkers":        "@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\nHost key verification failed.\n",
		"RemoteNestedSSH":    changedKeyOutput("/home/ubuntu/.ssh/known_hosts", "10.64.64.20"),
		"RemoteSameHomePath": changedKeyOutput(ourKnownHosts, "10.64.64.20"),
		"RemoteRevoked":      "ED25519 host key for 10.64.64.20 was revoked and you have requested strict checking.\n",
	} {
		err := SystemSSHHostKeyFailure(output, "10.0.0.4", nil)
		if err != nil {
			t.Errorf("%s: SystemSSHHostKeyFailure() = %v, want nil", name, err)
		}
	}
}

// A known_hosts entry is for a host and a port. ssh prints a host on port
// 22 bare and any other port as "[host]:port", and a bastion spelled
// without a port is on 22, so a refusal of the bastion's address on
// another port is not the bastion's.
func TestSystemSSHHostKeyFailureComparesThePort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	knownHosts := filepath.Join(home, ".ssh", "known_hosts")

	tests := []struct {
		name    string
		printed string
		bastion string
		want    bool
	}{
		{"BarePrintedBareDialled", "127.0.0.1", "127.0.0.1", true},
		{"BracketedDialledOn22", "127.0.0.1", "[127.0.0.1]:22", true},
		{"BracketedWithoutAPort", "127.0.0.1", "[127.0.0.1]", true},
		{"SamePort", "[127.0.0.1]:2222", "[127.0.0.1]:2222", true},
		{"OtherPortPrinted", "[127.0.0.1]:9", "127.0.0.1", false},
		{"OtherPortDialled", "127.0.0.1", "[127.0.0.1]:2222", false},
		{"DifferentPorts", "[127.0.0.1]:9", "[127.0.0.1]:2222", false},
		{"IPv6OtherPort", "[fd00::10]:9", "fd00::10", false},
	}

	for _, tt := range tests {
		err := SystemSSHHostKeyFailure(changedKeyOutput(knownHosts, tt.printed), tt.bastion, nil)
		if got := errors.Is(err, ErrHostKeyMismatch); got != tt.want {
			t.Errorf("%s: printed %s, dialled %s: got %v, want a mismatch %v", tt.name, tt.printed, tt.bastion, err, tt.want)
		}
	}
}
