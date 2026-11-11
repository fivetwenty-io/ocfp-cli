package ssh

import (
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
