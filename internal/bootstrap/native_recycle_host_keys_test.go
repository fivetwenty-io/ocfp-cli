package bootstrap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/cpi"
	"github.com/fivetwenty-io/ocfp-cli/internal/recycle"
	cryptossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// guestServer is an in-process SSH server standing in for a recycled
// bastion. It presents temporary for the first temporaryConns connections
// and restored after that, and answers every exec request with exit 0.
type guestServer struct {
	port  int
	conns atomic.Int32
}

func startGuestServer(t *testing.T, temporaryConns int32, temporary, restored cryptossh.Signer) *guestServer {
	t.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	srv := &guestServer{port: listener.Addr().(*net.TCPAddr).Port}

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			cfg := &cryptossh.ServerConfig{
				PublicKeyCallback: func(cryptossh.ConnMetadata, cryptossh.PublicKey) (*cryptossh.Permissions, error) {
					return &cryptossh.Permissions{}, nil
				},
			}

			if srv.conns.Add(1) <= temporaryConns {
				cfg.AddHostKey(temporary)
			} else {
				cfg.AddHostKey(restored)
			}

			go serveGuestConn(conn, cfg)
		}
	}()

	return srv
}

func serveGuestConn(conn net.Conn, cfg *cryptossh.ServerConfig) {
	sconn, chans, reqs, err := cryptossh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()

		return
	}

	defer func() { _ = sconn.Close() }()

	go cryptossh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(cryptossh.UnknownChannelType, "session only")

			continue
		}

		channel, requests, acceptErr := newChannel.Accept()
		if acceptErr != nil {
			return
		}

		go func() {
			for req := range requests {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)

					continue
				}

				_ = req.Reply(true, nil)
				_, _ = channel.SendRequest("exit-status", false, cryptossh.Marshal(struct{ Status uint32 }{0}))
				_ = channel.Close()
			}
		}()
	}
}

func newGuestSigner(t *testing.T) cryptossh.Signer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := cryptossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	return signer
}

// writeClientKey writes an unencrypted OpenSSH private key for the client.
func writeClientKey(t *testing.T, dir string) string {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	block, err := cryptossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "id_ed25519")

	err = os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

// isolateGuestSSH points HOME, and with it the known_hosts file both the
// bastion SSH client and this package use, at a fresh directory, and keeps
// any ssh agent and system ssh binary out of reach.
func isolateGuestSSH(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("PATH", t.TempDir())

	path := filepath.Join(home, ".ssh", "known_hosts")

	previous := knownHostsFile
	knownHostsFile = func() (string, error) { return path, nil }

	t.Cleanup(func() { knownHostsFile = previous })

	err := os.MkdirAll(filepath.Dir(path), 0o700)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

func authorizedLine(key cryptossh.PublicKey) string {
	return strings.TrimSpace(string(cryptossh.MarshalAuthorizedKey(key)))
}

func writeKnownHostsLines(t *testing.T, path string, lines ...string) {
	t.Helper()

	err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
}

func readKnownHosts(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path) // #nosec G304 -- test temp path
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	return string(content)
}

func newGuestJournal(t *testing.T) *recycle.NativeJournal {
	t.Helper()

	return &recycle.NativeJournal{Version: 1, Token: "token", SSHUser: "ubuntu", SSHKeyPath: writeClientKey(t, t.TempDir()), VerificationCommand: "true"}
}

// A native recycle reconnects to a bastion whose new root disk first
// presents a host key it generated, before the bastion restores its own from
// the data disk. The check before the recycle stores in the journal the key
// the original bastion presented, and the check after the recycle waits for
// that key, records it under the address it dials, and never records the
// temporary key.
func TestNativeRecycleVerifiesTheBastionsPreviousHostKey(t *testing.T) {
	knownHosts := isolateGuestSSH(t)

	previous, temporary := newGuestSigner(t), newGuestSigner(t)
	original := startGuestServer(t, 0, previous, previous)
	srv := startGuestServer(t, 1, temporary, previous)

	c := nativeTestCluster(t)
	c.guestPort = original.port

	bastion := &cpi.Instance{ID: "vm", PrivateIP: "127.0.0.1"}
	j := newGuestJournal(t)

	err := c.checkGuest(t.Context(), bastion, j, false)
	if err != nil {
		t.Fatalf("guest check before the recycle: %v", err)
	}

	if got, want := j.ExpectedHostKeys, []string{authorizedLine(previous.PublicKey())}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("journal expected host keys = %q, want %q", got, want)
	}

	err = c.Save(t.Context(), j)
	if err != nil {
		t.Fatal(err)
	}

	j, err = c.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// After the recycle it answers on an address with no entry.
	c.guestPort = srv.port

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	err = c.checkGuest(ctx, bastion, j, true)
	if err != nil {
		t.Fatalf("guest check after the recycle: %v", err)
	}

	if got := srv.conns.Load(); got < 2 {
		t.Errorf("server saw %d connections, want a retry after the temporary key", got)
	}

	content := readKnownHosts(t, knownHosts)
	if strings.Contains(content, authorizedLine(temporary.PublicKey())) {
		t.Errorf("known_hosts recorded the temporary key:\n%s", content)
	}

	recorded := knownhosts.Line([]string{net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.port))}, previous.PublicKey())
	if !strings.Contains(content, recorded) {
		t.Errorf("known_hosts does not record the previous key under the new address:\n%s", content)
	}
}

// Blocs reuse private addresses, and a stale AWS_BASTION_IP can name some
// other bloc's host, so known_hosts may hold keys for the bastion's other
// addresses that this bastion never presented. Only the key the original
// bastion presented, and that passed the check, is expected after the
// recycle. A key recorded only for another of the bastion's addresses, or
// matched only through a wildcard line, is not, and a host presenting one is
// refused and never recorded.
func TestNativeRecycleExpectsOnlyTheKeyTheBastionPresented(t *testing.T) {
	knownHosts := isolateGuestSSH(t)

	own, other, wildcard := newGuestSigner(t), newGuestSigner(t), newGuestSigner(t)
	original := startGuestServer(t, 0, own, own)

	c := nativeTestCluster(t)
	c.guestPort = original.port

	t.Setenv("AWS_BASTION_IP", "10.4.0.4")

	port := strconv.Itoa(original.port)
	writeKnownHostsLines(t, knownHosts,
		knownhosts.Line([]string{net.JoinHostPort("10.4.0.4", port)}, other.PublicKey()),
		"[10.4.0.*]:"+port+" "+authorizedLine(wildcard.PublicKey()),
	)

	bastion := &cpi.Instance{ID: "vm", PrivateIP: "127.0.0.1"}
	if addresses := c.mgr.bastionAddresses(bastion, c.name()); !slices.Contains(addresses, "10.4.0.4") {
		t.Fatalf("bastion addresses %q do not include AWS_BASTION_IP", addresses)
	}

	j := newGuestJournal(t)

	err := c.checkGuest(t.Context(), bastion, j, false)
	if err != nil {
		t.Fatalf("guest check before the recycle: %v", err)
	}

	if got, want := j.ExpectedHostKeys, []string{authorizedLine(own.PublicKey())}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("journal expected host keys = %q, want only the key the bastion presented, %q", got, want)
	}

	// A host on the bastion's address after the recycle that presents the
	// other address's key is refused, however long it is given.
	impostor := startGuestServer(t, 0, other, other)
	c.guestPort = impostor.port

	ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()

	err = c.checkGuest(ctx, bastion, j, true)
	if err == nil {
		t.Fatal("guest check after the recycle accepted a key the bastion never presented")
	}

	if impostor.conns.Load() == 0 {
		t.Fatalf("the check never reached the host presenting the other key: %v", err)
	}

	impostorEntry := knownhosts.Line([]string{net.JoinHostPort("127.0.0.1", strconv.Itoa(impostor.port))}, other.PublicKey())
	if content := readKnownHosts(t, knownHosts); strings.Contains(content, impostorEntry) {
		t.Errorf("known_hosts recorded the other address's key for the recycled bastion:\n%s", content)
	}
}

// A journal written before the CLI recorded host keys, or one whose keys
// cannot be read, has nothing to check the recycled bastion against. The
// check after the recycle stops at once with an error that says so and how
// to go on, and never dials, records a key, or runs the ssh fallback.
func TestNativeRecycleWithNoPreviousHostKeyFailsClosed(t *testing.T) {
	for name, keys := range map[string][]string{
		"journal without the field": nil,
		"unreadable key":            {"not a key"},
	} {
		t.Run(name, func(t *testing.T) {
			knownHosts := isolateGuestSSH(t)

			signer := newGuestSigner(t)
			srv := startGuestServer(t, 0, signer, signer)

			c := nativeTestCluster(t)
			c.guestPort = srv.port

			j := newGuestJournal(t)
			j.ExpectedHostKeys = keys

			err := c.Save(t.Context(), j)
			if err != nil {
				t.Fatal(err)
			}

			if keys == nil {
				blob, marshalErr := json.Marshal(j)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}

				if strings.Contains(string(blob), "expected_host_keys") {
					t.Fatalf("journal %s carries expected_host_keys", blob)
				}
			}

			j, err = c.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			start := time.Now()
			err = c.checkGuest(ctx, &cpi.Instance{ID: "vm", PrivateIP: "127.0.0.1"}, j, true)

			if !errors.Is(err, ssh.ErrNoPreviousHostKey) {
				t.Fatalf("guest check error = %v, want ErrNoPreviousHostKey", err)
			}

			for _, want := range []string{"no previous host key is on record", "out of band", "ocfp bastion init", "recycle"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err.Error(), want)
				}
			}

			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("guest check took %s, want it to stop at once", elapsed)
			}

			if got := srv.conns.Load(); got != 0 {
				t.Errorf("server saw %d connections, want none", got)
			}

			if content := readKnownHosts(t, knownHosts); content != "" {
				t.Errorf("known_hosts was written:\n%s", content)
			}
		})
	}
}
