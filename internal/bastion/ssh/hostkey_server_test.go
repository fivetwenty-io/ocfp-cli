package ssh

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyTestServer is an in-process SSH server on the loopback interface. It
// accepts any client without authentication and refuses every channel, which
// is enough to exercise host key verification during the handshake.
type hostKeyTestServer struct {
	port  int
	conns atomic.Int32
}

// startHostKeyTestServer serves SSH on 127.0.0.1 with the given host keys
// until the test ends.
func startHostKeyTestServer(t *testing.T, signers ...gossh.Signer) *hostKeyTestServer {
	t.Helper()

	cfg := &gossh.ServerConfig{NoClientAuth: true}
	for _, signer := range signers {
		cfg.AddHostKey(signer)
	}

	var lc net.ListenConfig

	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", listener.Addr())
	}

	srv := &hostKeyTestServer{port: tcpAddr.Port}

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			srv.conns.Add(1)

			go serveHostKeyTestConn(conn, cfg)
		}
	}()

	return srv
}

func serveHostKeyTestConn(conn net.Conn, cfg *gossh.ServerConfig) {
	sconn, chans, reqs, err := gossh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()

		return
	}

	go gossh.DiscardRequests(reqs)

	go func() {
		for ch := range chans {
			_ = ch.Reject(gossh.Prohibited, "test server")
		}
	}()

	_ = sconn.Wait()
}

// address is the host:port the client dials, as known_hosts lookups see it.
func (s *hostKeyTestServer) address() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(s.port))
}

// isolateSSHEnv points HOME at a fresh directory, hides any ssh agent, and
// empties PATH so no system ssh binary can run. It returns the known_hosts
// path the client will use.
func isolateSSHEnv(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("PATH", t.TempDir())

	return filepath.Join(home, ".ssh", "known_hosts")
}

// writeKnownHosts writes one known_hosts line per key for address.
func writeKnownHosts(t *testing.T, path, address string, keys ...gossh.PublicKey) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), sshDirectoryMode)
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var content string
	for _, key := range keys {
		content += knownhosts.Line([]string{address}, key) + "\n"
	}

	err = os.WriteFile(path, []byte(content), privateKeyMode)
	if err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

// newLoopbackClient builds a Client for the test server. The password only
// satisfies prepareAuthMethods; the server never asks for it.
func newLoopbackClient(srv *hostKeyTestServer) *Client {
	c := NewClient(&ConnectionDetails{
		Host:     "127.0.0.1",
		Port:     srv.port,
		User:     "tester",
		Password: "unused",
	}, &ProvisioningOptions{})
	c.log = zap.NewNop().Sugar()

	return c
}

func connectWithin(t *testing.T, c *Client, timeout time.Duration) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	t.Cleanup(func() { _ = c.Close() })

	return c.Connect(ctx)
}
