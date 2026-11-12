package ssh

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const changedKeyOutput = `@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
IT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!
Host key for 10.0.0.4 has changed and you have requested strict checking.
Host key verification failed.
`

func TestSystemSSHHostKeyErrorMatchesErrHostKeyMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cause := &exec.ExitError{}

	err := SystemSSHHostKeyFailure(changedKeyOutput, "10.0.0.4", DefaultSSHPort, cause)
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

	knownHosts := filepath.Join(home, ".ssh", "known_hosts")
	if !strings.Contains(err.Error(), "ssh-keygen -R 10.0.0.4 -f "+knownHosts) {
		t.Fatalf("error %q does not say how to clear the stale entry", err)
	}
}

func TestSystemSSHHostKeyFailureNamesANonDefaultPortTheWayKnownHostsDoes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := SystemSSHHostKeyFailure("Host key verification failed.\r\n", "bastion.example", 2222, nil)
	if err == nil {
		t.Fatal("SystemSSHHostKeyFailure() = nil for a failed verification")
	}

	if !strings.Contains(err.Error(), "ssh-keygen -R '[bastion.example]:2222' -f ") {
		t.Fatalf("error %q does not name the [host]:port entry", err)
	}
}

func TestSystemSSHHostKeyFailureIgnoresOtherFailures(t *testing.T) {
	t.Parallel()

	for _, output := range []string{
		"",
		"ssh: connect to host 10.0.0.4 port 22: Connection refused\n",
		"ubuntu@10.0.0.4: Permission denied (publickey).\n",
	} {
		err := SystemSSHHostKeyFailure(output, "10.0.0.4", DefaultSSHPort, nil)
		if err != nil {
			t.Errorf("SystemSSHHostKeyFailure(%q) = %v, want nil", output, err)
		}
	}
}
