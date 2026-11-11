package ssh

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A host can hold several key types. known_hosts often records only the one
// OpenSSH chose, ed25519, while Go's client would otherwise ask for ECDSA
// first. The client must ask for the recorded type so a correct host is not
// mistaken for a changed one.
func TestConnectPrefersRecordedHostKeyType(t *testing.T) {
	knownHosts := isolateSSHEnv(t)

	ed := newEd25519Signer(t)
	srv := startHostKeyTestServer(t, newECDSASigner(t), ed)
	writeKnownHosts(t, knownHosts, srv.address(), ed.PublicKey())

	before := readFile(t, knownHosts)

	err := connectWithin(t, newLoopbackClient(srv), 5*time.Second)
	if err != nil {
		t.Fatalf("connect to a host whose recorded ed25519 key is unchanged: %v", err)
	}

	if after := readFile(t, knownHosts); after != before {
		t.Errorf("known_hosts changed on a verified connection:\nbefore: %q\nafter:  %q", before, after)
	}
}

// A changed host key must end the connection at once. Connect may not retry
// it or hand it to the external ssh fallback, and the error must name the
// host and the file and say how to clear an entry left by a planned rebuild.
func TestConnectStopsOnChangedHostKey(t *testing.T) {
	knownHosts := isolateSSHEnv(t)

	srv := startHostKeyTestServer(t, newEd25519Signer(t))
	writeKnownHosts(t, knownHosts, srv.address(), newEd25519Signer(t).PublicKey())

	before := readFile(t, knownHosts)

	err := connectWithin(t, newLoopbackClient(srv), 5*time.Second)
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("Connect error = %v, want ErrHostKeyMismatch", err)
	}

	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Connect error %v does not carry a *HostKeyMismatchError", err)
	}

	host := "[127.0.0.1]:" + strconv.Itoa(srv.port)
	for _, want := range []string{host, knownHosts, "ssh-keygen -R '" + host + "'", "wildcard"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}

	if got := srv.conns.Load(); got != 1 {
		t.Errorf("server saw %d connections, want exactly 1 (no retry, no fallback)", got)
	}

	if after := readFile(t, knownHosts); after != before {
		t.Errorf("known_hosts changed after a mismatch:\nbefore: %q\nafter:  %q", before, after)
	}
}
