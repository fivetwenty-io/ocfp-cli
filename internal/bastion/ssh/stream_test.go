package ssh

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// streamGuard bounds how long a test waits for Stream before failing. It is a
// safety net for a hung call, never a pass condition.
const streamGuard = 30 * time.Second

// handshakeCancelBound is how soon Stream must return after a cancel that
// lands during the handshake.
const handshakeCancelBound = 5 * time.Second

// sharedRSAHostKey is generated once because RSA generation is slow under
// the race detector.
var sharedRSAHostKey = sync.OnceValues(func() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2048)
})

func newEd25519Signer(t *testing.T) xssh.Signer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	signer, err := xssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ed25519 signer: %v", err)
	}

	return signer
}

func newECDSASigner(t *testing.T) xssh.Signer {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa key: %v", err)
	}

	signer, err := xssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ecdsa signer: %v", err)
	}

	return signer
}

func newRSASigner(t *testing.T) xssh.Signer {
	t.Helper()

	priv, err := sharedRSAHostKey()
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}

	signer, err := xssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("rsa signer: %v", err)
	}

	return signer
}

// streamHandler runs one exec request on the test server. It owns the channel
// until it returns, and the server closes the channel afterwards.
type streamHandler func(ch xssh.Channel, signals <-chan string, closed <-chan struct{})

// testSSHServer is an in-process ssh server on 127.0.0.1 that answers exec
// requests from a fixed table of handlers.
type testSSHServer struct {
	addr      string
	clientKey xssh.PublicKey
	handlers  map[string]streamHandler

	mu          sync.Mutex
	authAttempt int
	execs       []string
	signalsSeen []string
	conns       []net.Conn
	finished    chan string
}

func sendExitStatus(ch xssh.Channel, status uint32) {
	payload := xssh.Marshal(struct{ Status uint32 }{status})
	_, _ = ch.SendRequest("exit-status", false, payload)
}

func defaultStreamHandlers() map[string]streamHandler {
	return map[string]streamHandler{
		"echo-stdin": func(ch xssh.Channel, _ <-chan string, _ <-chan struct{}) {
			_, _ = io.Copy(ch, ch)
			_, _ = ch.Stderr().Write([]byte("err"))
			sendExitStatus(ch, 3)
		},
		"say-ok": func(ch xssh.Channel, _ <-chan string, _ <-chan struct{}) {
			_, _ = ch.Write([]byte("ok\n"))
			sendExitStatus(ch, 0)
		},
		"block": func(ch xssh.Channel, signals <-chan string, closed <-chan struct{}) {
			_, _ = ch.Write([]byte("started\n"))
			select {
			case <-signals:
			case <-closed:
			}
		},
		"late-output": func(ch xssh.Channel, _ <-chan string, _ <-chan struct{}) {
			_, _ = ch.Write([]byte("early\n"))
			sendExitStatus(ch, 0)
			_, _ = ch.Write([]byte("late\n"))
			_, _ = ch.Stderr().Write([]byte("late-err\n"))
		},
		"no-exit-status": func(ch xssh.Channel, _ <-chan string, _ <-chan struct{}) {
			_, _ = ch.Write([]byte("partial\n"))
		},
		// run-on-eof stands for a shell reading a script from stdin. It acts
		// only once its input ends cleanly.
		"run-on-eof": func(ch xssh.Channel, _ <-chan string, _ <-chan struct{}) {
			_, _ = io.Copy(io.Discard, ch)
			_, _ = ch.Write([]byte("ran\n"))
			sendExitStatus(ch, 0)
		},
	}
}

// startTestSSHServer listens on 127.0.0.1:0 with the given host keys and
// accepts only clientKey.
func startTestSSHServer(t *testing.T, clientKey xssh.PublicKey, hostKeys ...xssh.Signer) *testSSHServer {
	t.Helper()

	srv := &testSSHServer{
		clientKey: clientKey,
		handlers:  defaultStreamHandlers(),
		finished:  make(chan string, 16),
	}

	cfg := &xssh.ServerConfig{
		PublicKeyCallback: func(_ xssh.ConnMetadata, key xssh.PublicKey) (*xssh.Permissions, error) {
			srv.mu.Lock()
			srv.authAttempt++
			srv.mu.Unlock()

			if bytes.Equal(key.Marshal(), srv.clientKey.Marshal()) {
				return &xssh.Permissions{}, nil
			}

			return nil, errors.New("unknown client key")
		},
	}
	for _, hk := range hostKeys {
		cfg.AddHostKey(hk)
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv.addr = ln.Addr().String()

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			srv.mu.Lock()
			srv.conns = append(srv.conns, conn)
			srv.mu.Unlock()

			wg.Add(1)

			go func() {
				defer wg.Done()
				srv.serveConn(conn, cfg, &wg)
			}()
		}
	}()

	t.Cleanup(func() {
		_ = ln.Close()

		srv.mu.Lock()
		for _, c := range srv.conns {
			_ = c.Close()
		}
		srv.mu.Unlock()

		wg.Wait()
	})

	return srv
}

func (s *testSSHServer) serveConn(conn net.Conn, cfg *xssh.ServerConfig, wg *sync.WaitGroup) {
	sconn, chans, reqs, err := xssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()

		return
	}

	defer func() { _ = sconn.Close() }()

	go xssh.DiscardRequests(reqs)

	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(xssh.UnknownChannelType, "only sessions")

			continue
		}

		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}

		wg.Add(1)

		go func() {
			defer wg.Done()
			s.serveSession(ch, chReqs)
		}()
	}
}

func (s *testSSHServer) serveSession(ch xssh.Channel, reqs <-chan *xssh.Request) {
	signals := make(chan string, 4)
	closed := make(chan struct{})
	execs := make(chan string, 1)

	go func() {
		defer close(closed)

		for req := range reqs {
			switch req.Type {
			case "exec":
				var body struct{ Command string }
				if err := xssh.Unmarshal(req.Payload, &body); err != nil {
					_ = req.Reply(false, nil)

					continue
				}

				_ = req.Reply(true, nil)
				execs <- body.Command
			case "signal":
				var body struct{ Signal string }
				if err := xssh.Unmarshal(req.Payload, &body); err == nil {
					s.mu.Lock()
					s.signalsSeen = append(s.signalsSeen, body.Signal)
					s.mu.Unlock()

					select {
					case signals <- body.Signal:
					default:
					}
				}
			default:
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
			}
		}
	}()

	var cmd string
	select {
	case cmd = <-execs:
	case <-closed:
		_ = ch.Close()

		return
	}

	s.mu.Lock()
	s.execs = append(s.execs, cmd)
	s.mu.Unlock()

	handler, ok := s.handlers[cmd]
	if !ok {
		_, _ = ch.Stderr().Write([]byte("no such command\n"))
		sendExitStatus(ch, 127)
	} else {
		handler(ch, signals, closed)
	}

	_ = ch.CloseWrite()
	_ = ch.Close()

	// Wait for the request loop to drain, so every signal the client sent
	// is recorded before the session counts as finished.
	<-closed
	s.finished <- cmd
}

func (s *testSSHServer) signals() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.signalsSeen...)
}

// waitFinished waits for the server to finish running cmd.
func (s *testSSHServer) waitFinished(t *testing.T, cmd string) {
	t.Helper()

	select {
	case got := <-s.finished:
		if got != cmd {
			t.Fatalf("server finished %q, want %q", got, cmd)
		}
	case <-time.After(streamGuard):
		t.Fatalf("server never finished %q", cmd)
	}
}

// caLine renders an @cert-authority entry for the server's address.
func (s *testSSHServer) caLine(ca xssh.PublicKey) string {
	return "@cert-authority " + knownhosts.Normalize(s.addr) + " " +
		strings.TrimSpace(string(xssh.MarshalAuthorizedKey(ca))) + "\n"
}

// newHostCertSigner signs host's key as a host certificate from ca.
func newHostCertSigner(t *testing.T, ca, host xssh.Signer, principal string) xssh.Signer {
	t.Helper()

	cert := &xssh.Certificate{
		Key:             host.PublicKey(),
		CertType:        xssh.HostCert,
		KeyId:           "test host",
		ValidPrincipals: []string{principal},
		ValidBefore:     xssh.CertTimeInfinity,
	}

	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatalf("sign host cert: %v", err)
	}

	signer, err := xssh.NewCertSigner(cert, host)
	if err != nil {
		t.Fatalf("cert signer: %v", err)
	}

	return signer
}

func (s *testSSHServer) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.authAttempt, len(s.execs)
}

// knownHostsLine renders a known_hosts entry for the server's address.
func (s *testSSHServer) knownHostsLine(key xssh.PublicKey) string {
	return knownhosts.Line([]string{knownhosts.Normalize(s.addr)}, key) + "\n"
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// streamFixture holds a client key on disk and its public half.
type streamFixture struct {
	dir     string
	keyPath string
	pub     xssh.PublicKey
}

func newStreamFixture(t *testing.T) streamFixture {
	t.Helper()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	writeTestEd25519Key(t, keyPath)

	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}

	signer, err := xssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}

	return streamFixture{dir: dir, keyPath: keyPath, pub: signer.PublicKey()}
}

func (f streamFixture) client(t *testing.T, srv *testSSHServer, knownHostsPath string) *Client {
	t.Helper()

	host, portStr, err := net.SplitHostPort(srv.addr)
	if err != nil {
		t.Fatalf("split %s: %v", srv.addr, err)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %s: %v", portStr, err)
	}

	return NewClient(&ConnectionDetails{
		Host:           host,
		Port:           port,
		User:           "tester",
		PrivateKeyPath: f.keyPath,
		KnownHostsPath: knownHostsPath,
	}, &ProvisioningOptions{})
}

type streamResult struct {
	code int
	err  error
}

// runStream calls Stream in a goroutine and fails the test if it does not
// return within streamGuard.
func runStream(t *testing.T, ctx context.Context, c *Client, cmd string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	t.Helper()

	done := make(chan streamResult, 1)

	go func() {
		code, err := c.Stream(ctx, cmd, stdin, stdout, stderr)
		done <- streamResult{code, err}
	}()

	select {
	case r := <-done:
		return r.code, r.err
	case <-time.After(streamGuard):
		t.Fatalf("Stream(%q) did not return within %s", cmd, streamGuard)

		return 0, nil
	}
}

func requireCodePrefix(t *testing.T, err error, code string, sentinel error) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected an error starting with %s, got nil", code)
	}

	if !strings.HasPrefix(err.Error(), code) {
		t.Fatalf("error %q does not start with %s", err.Error(), code)
	}

	if !errors.Is(err, sentinel) {
		t.Fatalf("error %q does not wrap the %s sentinel", err.Error(), code)
	}
}

func TestStreamRunsCommandWithStdin(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)

	var stdout, stderr bytes.Buffer

	code, err := runStream(t, context.Background(), c, "echo-stdin", strings.NewReader("hello stdin\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}

	if got := stdout.String(); got != "hello stdin\n" {
		t.Fatalf("stdout = %q, want %q", got, "hello stdin\n")
	}

	if got := stderr.String(); got != "err" {
		t.Fatalf("stderr = %q, want %q", got, "err")
	}

	if c.connected || c.client != nil {
		t.Fatal("Stream must not use or set the client's shared connection")
	}
}

func TestStreamRefusesUnknownHost(t *testing.T) {
	// Not parallel: it swaps os.Stdin to prove nothing reads the terminal.
	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, "")

	before, err := os.Stat(kh)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	fakeTTY, ttyWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	defer func() { _ = fakeTTY.Close() }()

	const canary = "yes\n"
	if _, err := ttyWriter.Write([]byte(canary)); err != nil {
		t.Fatalf("write canary: %v", err)
	}

	_ = ttyWriter.Close()

	realStdin := os.Stdin
	os.Stdin = fakeTTY

	t.Cleanup(func() { os.Stdin = realStdin })

	c := fx.client(t, srv, kh)

	var stdout, stderr bytes.Buffer

	stdin := &readCounter{r: strings.NewReader("payload")}

	_, err = runStream(t, context.Background(), c, "say-ok", stdin, &stdout, &stderr)
	requireCodePrefix(t, err, "HOST_KEY_UNKNOWN", ErrHostKeyUnknown)

	if !strings.Contains(err.Error(), xssh.FingerprintSHA256(hostKey.PublicKey())) {
		t.Fatalf("error %q should name the presented key's fingerprint", err.Error())
	}

	os.Stdin = realStdin

	left, err := io.ReadAll(fakeTTY)
	if err != nil {
		t.Fatalf("read fake terminal: %v", err)
	}

	if string(left) != canary {
		t.Fatalf("something read the terminal: %q left of %q", left, canary)
	}

	if stdout.Len() != 0 || stderr.Len() != 0 || stdin.reads() != 0 {
		t.Fatalf("refused stream touched its streams: stdout=%q stderr=%q stdin reads=%d", stdout.String(), stderr.String(), stdin.reads())
	}

	after, err := os.Stat(kh)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}

	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("known_hosts changed")
	}

	if entries, _ := os.ReadDir(fx.dir); len(entries) != 2 {
		t.Fatalf("Stream created files next to known_hosts: %v", entries)
	}

	if auths, execs := srv.counts(); auths != 0 || execs != 0 {
		t.Fatalf("refused host saw auth=%d exec=%d, want 0 and 0", auths, execs)
	}
}

func TestStreamRefusesChangedHostKey(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	content := srv.knownHostsLine(newEd25519Signer(t).PublicKey())
	writeFile(t, kh, content)

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	requireCodePrefix(t, err, "HOST_KEY_MISMATCH", ErrHostKeyMismatch)

	if got, _ := os.ReadFile(kh); string(got) != content {
		t.Fatal("known_hosts changed")
	}

	if auths, execs := srv.counts(); auths != 0 || execs != 0 {
		t.Fatalf("refused host saw auth=%d exec=%d, want 0 and 0", auths, execs)
	}
}

// startSignal closes started on the first write that contains "started".
type startSignal struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	started chan struct{}
	once    sync.Once
}

func (s *startSignal) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n, err := s.buf.Write(p)
	if strings.Contains(s.buf.String(), "started") {
		s.once.Do(func() { close(s.started) })
	}

	return n, err
}

func TestStreamCancelsWithContext(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)
	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	out := &startSignal{started: make(chan struct{})}
	done := make(chan streamResult, 1)

	go func() {
		code, err := c.Stream(ctx, "block", nil, out, io.Discard)
		done <- streamResult{code, err}
	}()

	select {
	case <-out.started:
	case r := <-done:
		t.Fatalf("Stream returned before the command started: %d %v", r.code, r.err)
	case <-time.After(streamGuard):
		t.Fatal("remote command never started")
	}

	cancel()

	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", r.err)
		}
	case <-time.After(streamGuard):
		t.Fatal("Stream did not return after cancel")
	}

	select {
	case cmd := <-srv.finished:
		if cmd != "block" {
			t.Fatalf("finished %q, want block", cmd)
		}
	case <-time.After(streamGuard):
		t.Fatal("remote command kept running after cancel")
	}
}

func TestStreamRefusesRevokedHostKey(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	revoked := "@revoked * " + strings.TrimSpace(string(xssh.MarshalAuthorizedKey(hostKey.PublicKey()))) + "\n"
	writeFile(t, kh, revoked+srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	requireCodePrefix(t, err, "HOST_KEY_MISMATCH", ErrHostKeyMismatch)

	if !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("error %q should say the key is revoked", err.Error())
	}

	if _, execs := srv.counts(); execs != 0 {
		t.Fatalf("revoked host ran %d commands", execs)
	}
}

func TestStreamAcceptsHashedHostEntry(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	hashed := knownhosts.HashHostname(knownhosts.Normalize(srv.addr))
	writeFile(t, kh, knownhosts.Line([]string{hashed}, hostKey.PublicKey())+"\n")

	c := fx.client(t, srv, kh)

	var stdout bytes.Buffer

	code, err := runStream(t, context.Background(), c, "say-ok", nil, &stdout, io.Discard)
	if err != nil || code != 0 || stdout.String() != "ok\n" {
		t.Fatalf("Stream = %d, %v, stdout %q; want 0, nil, %q", code, err, stdout.String(), "ok\n")
	}
}

func TestStreamRefusesChangedHashedHostEntry(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	hashed := knownhosts.HashHostname(knownhosts.Normalize(srv.addr))
	writeFile(t, kh, knownhosts.Line([]string{hashed}, newEd25519Signer(t).PublicKey())+"\n")

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	requireCodePrefix(t, err, "HOST_KEY_MISMATCH", ErrHostKeyMismatch)
}

func TestStreamPrefersTheKnownKeyTypeWhenHostOffersSeveral(t *testing.T) {
	t.Parallel()

	ed := newEd25519Signer(t)
	ec := newECDSASigner(t)
	rs := newRSASigner(t)

	for _, tc := range []struct {
		name  string
		known xssh.Signer
	}{
		{"ed25519", ed},
		{"ecdsa", ec},
		{"rsa", rs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fx := newStreamFixture(t)
			srv := startTestSSHServer(t, fx.pub, ec, rs, ed)

			kh := filepath.Join(fx.dir, "known_hosts")
			writeFile(t, kh, srv.knownHostsLine(tc.known.PublicKey()))

			c := fx.client(t, srv, kh)

			code, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
			if err != nil || code != 0 {
				t.Fatalf("Stream = %d, %v; want 0, nil", code, err)
			}
		})
	}
}

func TestStreamRefusesChangedKeyWhenHostOffersSeveral(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	srv := startTestSSHServer(t, fx.pub, newECDSASigner(t), newRSASigner(t), newEd25519Signer(t))

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(newEd25519Signer(t).PublicKey()))

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	requireCodePrefix(t, err, "HOST_KEY_MISMATCH", ErrHostKeyMismatch)
}

func TestStreamTreatsMissingKnownHostsAsUnknown(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	sshDir := filepath.Join(fx.dir, "nested", ".ssh")
	kh := filepath.Join(sshDir, "known_hosts")

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	requireCodePrefix(t, err, "HOST_KEY_UNKNOWN", ErrHostKeyUnknown)

	if _, statErr := os.Stat(filepath.Join(fx.dir, "nested")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Stream created the known_hosts directory: %v", statErr)
	}
}

func TestStreamTreatsDanglingKnownHostsSymlinkAsUnknown(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	target := filepath.Join(fx.dir, "absent")

	if err := os.Symlink(target, kh); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	requireCodePrefix(t, err, "HOST_KEY_UNKNOWN", ErrHostKeyUnknown)

	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Stream created the symlink target: %v", statErr)
	}
}

func TestStreamReadsKnownHostsThroughSymlink(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	target := filepath.Join(fx.dir, "real_known_hosts")
	content := srv.knownHostsLine(hostKey.PublicKey())
	writeFile(t, target, content)

	kh := filepath.Join(fx.dir, "known_hosts")
	if err := os.Symlink(target, kh); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	c := fx.client(t, srv, kh)

	code, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("Stream = %d, %v; want 0, nil", code, err)
	}

	if got, _ := os.ReadFile(target); string(got) != content {
		t.Fatal("symlink target changed")
	}

	if fi, _ := os.Lstat(kh); fi == nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("known_hosts symlink was replaced")
	}
}

func TestStreamFailsOnUnreadableKnownHosts(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("file modes do not deny reads on windows")
	}

	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of mode")
	}

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	if err := os.Chmod(kh, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("Stream succeeded with an unreadable known_hosts")
	}

	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v, want a permission error", err)
	}

	if _, execs := srv.counts(); execs != 0 {
		t.Fatalf("ran %d commands with an unreadable known_hosts", execs)
	}
}

func TestStreamFailsOnMalformedKnownHosts(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey())+"garbage-line-without-a-key\n")

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("Stream succeeded with a malformed known_hosts")
	}

	if errors.Is(err, ErrHostKeyUnknown) || errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("a file that does not parse must not read as a host key verdict: %v", err)
	}

	if !strings.Contains(err.Error(), kh+":2") {
		t.Fatalf("error %q should name %s line 2", err.Error(), kh)
	}

	if _, execs := srv.counts(); execs != 0 {
		t.Fatalf("ran %d commands with a malformed known_hosts", execs)
	}
}

func TestStreamDefaultsToHomeKnownHosts(t *testing.T) {
	// Not parallel: it sets HOME.
	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeFile(t, filepath.Join(sshDir, "known_hosts"), srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, "")

	code, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("Stream = %d, %v; want 0, nil", code, err)
	}
}

func TestStreamDefaultMissingHomeKnownHostsIsUnknown(t *testing.T) {
	// Not parallel: it sets HOME.
	fx := newStreamFixture(t)
	srv := startTestSSHServer(t, fx.pub, newEd25519Signer(t))

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	c := fx.client(t, srv, "")

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	requireCodePrefix(t, err, "HOST_KEY_UNKNOWN", ErrHostKeyUnknown)

	if _, statErr := os.Stat(filepath.Join(home, ".ssh")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Stream created ~/.ssh: %v", statErr)
	}
}

func TestStreamDeliversOutputSentAfterExitStatus(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)

	var stdout, stderr bytes.Buffer

	code, err := runStream(t, context.Background(), c, "late-output", nil, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("Stream = %d, %v; want 0, nil", code, err)
	}

	if stdout.String() != "early\nlate\n" || stderr.String() != "late-err\n" {
		t.Fatalf("stdout %q stderr %q; want %q and %q", stdout.String(), stderr.String(), "early\nlate\n", "late-err\n")
	}
}

func TestStreamReportsMissingExitStatus(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)

	var stdout bytes.Buffer

	code, err := runStream(t, context.Background(), c, "no-exit-status", nil, &stdout, io.Discard)
	if err == nil {
		t.Fatal("Stream succeeded without an exit status")
	}

	if code != -1 {
		t.Fatalf("exit code = %d, want -1", code)
	}

	if stdout.String() != "partial\n" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "partial\n")
	}
}

func TestStreamSharesOneWriterSafely(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)

	var both bytes.Buffer

	code, err := runStream(t, context.Background(), c, "echo-stdin", strings.NewReader("in"), &both, &both)
	if err != nil || code != 3 {
		t.Fatalf("Stream = %d, %v; want 3, nil", code, err)
	}

	// The two streams travel separately, so only their content is fixed,
	// not their order.
	if got := both.String(); got != "inerr" && got != "errin" {
		t.Fatalf("combined output = %q, want %q in either order", got, "in"+"err")
	}
}

// failingReader returns its data and then a read error.
type failingReader struct {
	data []byte
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}

	n := copy(p, f.data)
	f.data = f.data[n:]

	return n, nil
}

// TestStreamAbortsOnStdinReadError checks what the client does on a failed
// read: it returns the error and asks the server to kill the command. What
// the remote command does after the connection drops is up to the server,
// so the test checks the signal, not what the command ran.
func TestStreamAbortsOnStdinReadError(t *testing.T) {
	t.Parallel()

	for name, readErr := range map[string]error{
		"plain error": errors.New("disk went away"),
		// io.Reader returns io.EOF itself at a clean end, so an error that
		// only wraps it is a failure.
		"wrapped EOF": fmt.Errorf("chunk 3 truncated: %w", io.EOF),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fx := newStreamFixture(t)
			hostKey := newEd25519Signer(t)
			srv := startTestSSHServer(t, fx.pub, hostKey)

			kh := filepath.Join(fx.dir, "known_hosts")
			writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

			c := fx.client(t, srv, kh)

			var stdout bytes.Buffer

			code, err := runStream(t, context.Background(), c, "run-on-eof",
				&failingReader{data: []byte("rm -rf /tmp/x/"), err: readErr}, &stdout, io.Discard)
			if !errors.Is(err, readErr) {
				t.Fatalf("err = %v, want the stdin read error", err)
			}

			if code != -1 {
				t.Fatalf("exit code = %d, want -1", code)
			}

			if strings.Contains(stdout.String(), "ran") {
				t.Fatal("the client received output from a command that saw a clean end of input")
			}

			srv.waitFinished(t, "run-on-eof")

			if got := srv.signals(); !slices.Contains(got, "KILL") {
				t.Fatalf("server saw signals %v, want KILL", got)
			}
		})
	}
}

func TestStreamStopsStdinCopierThatSupportsDeadlines(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("pipes have no read deadlines on windows")
	}

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	defer func() { _ = r.Close() }()

	code, err := runStream(t, context.Background(), c, "say-ok", r, io.Discard, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("Stream = %d, %v; want 0, nil", code, err)
	}

	// The command never read its input. Stream must have stopped its copier
	// and cleared the deadline it used, so the caller gets the next line.
	if _, err := w.Write([]byte("y\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = w.Close()

	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read after Stream: %v", err)
	}

	if string(rest) != "y\n" {
		t.Fatalf("caller read %q after Stream, want %q", rest, "y\n")
	}
}

func TestStreamLeavesBlockedStdinReadBehind(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)

	pr, pw := io.Pipe()

	t.Cleanup(func() { _ = pw.Close() })

	code, err := runStream(t, context.Background(), c, "say-ok", pr, io.Discard, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("Stream = %d, %v; want 0, nil", code, err)
	}

	// An io.Pipe read cannot be interrupted, so the copier is still parked
	// in Read. It takes the next write and discards it, as documented.
	wrote := make(chan error, 1)

	go func() {
		_, err := pw.Write([]byte("y\n"))
		wrote <- err
	}()

	select {
	case err := <-wrote:
		if err != nil {
			t.Fatalf("write: %v", err)
		}
	case <-time.After(streamGuard):
		t.Fatal("nothing read stdin after Stream returned")
	}
}

// noDeadlineReader stands for os.Stdin, whose blocking descriptor refuses
// read deadlines.
type noDeadlineReader struct {
	*io.PipeReader

	attempts atomic.Int32
}

func (r *noDeadlineReader) SetReadDeadline(time.Time) error {
	r.attempts.Add(1)

	return os.ErrNoDeadline
}

func TestStreamLeavesReadBehindWhenDeadlineFails(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)

	pr, pw := io.Pipe()

	t.Cleanup(func() { _ = pw.Close() })

	stdin := &noDeadlineReader{PipeReader: pr}

	// runStream fails the test if Stream waits for a copier it cannot stop.
	code, err := runStream(t, context.Background(), c, "say-ok", stdin, io.Discard, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("Stream = %d, %v; want 0, nil", code, err)
	}

	if stdin.attempts.Load() != 1 {
		t.Fatalf("Stream tried to set a read deadline %d times, want 1", stdin.attempts.Load())
	}

	// The copier is still parked in Read, so it takes the next write and
	// discards it, as the doc comment says.
	wrote := make(chan error, 1)

	go func() {
		_, err := pw.Write([]byte("y\n"))
		wrote <- err
	}()

	select {
	case err := <-wrote:
		if err != nil {
			t.Fatalf("write: %v", err)
		}
	case <-time.After(streamGuard):
		t.Fatal("nothing read stdin after Stream returned")
	}
}

func TestStreamKeepsExitStatusWhenCancelledAfterExit(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)
	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	// late-output sends its exit status before "late", so by the time the
	// writer cancels, the status is already on its way to the client.
	out := &cancelOnWrite{marker: "late", cancel: cancel}

	code, err := runStream(t, ctx, c, "late-output", nil, out, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("Stream = %d, %v; want 0, nil", code, err)
	}
}

// cancelOnWrite cancels once the output it has seen contains marker.
type cancelOnWrite struct {
	buf    bytes.Buffer
	marker string
	cancel context.CancelFunc
}

func (c *cancelOnWrite) Write(p []byte) (int, error) {
	n, err := c.buf.Write(p)
	if strings.Contains(c.buf.String(), c.marker) {
		c.cancel()
	}

	return n, err
}

func TestStreamCAOnlyKnownHostsReportsPlainKeyAsUnknown(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	ca := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, newEd25519Signer(t))

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.caLine(ca.PublicKey()))

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	requireCodePrefix(t, err, "HOST_KEY_UNKNOWN", ErrHostKeyUnknown)

	if strings.Contains(err.Error(), xssh.FingerprintSHA256(ca.PublicKey())) {
		t.Fatalf("error %q names the CA key as the host's key", err.Error())
	}
}

func TestStreamUsesPlainKeyWhenHostAlsoOffersCertificate(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	cert := newHostCertSigner(t, newEd25519Signer(t), hostKey, "127.0.0.1")
	srv := startTestSSHServer(t, fx.pub, cert, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)

	code, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("Stream = %d, %v; want 0, nil", code, err)
	}
}

func TestStreamAcceptsCertificateFromListedAuthority(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	ca := newEd25519Signer(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, newHostCertSigner(t, ca, hostKey, "127.0.0.1"), hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.caLine(ca.PublicKey()))

	c := fx.client(t, srv, kh)

	code, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("Stream = %d, %v; want 0, nil", code, err)
	}
}

func TestStreamRefusesCertificateFromUnlistedAuthority(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, newHostCertSigner(t, newEd25519Signer(t), hostKey, "127.0.0.1"), hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.caLine(newEd25519Signer(t).PublicKey()))

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	requireCodePrefix(t, err, "HOST_KEY_MISMATCH", ErrHostKeyMismatch)

	if _, execs := srv.counts(); execs != 0 {
		t.Fatalf("ran %d commands", execs)
	}
}

func TestStreamReportsUnknownHostThatOffersCertificate(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, newHostCertSigner(t, newEd25519Signer(t), hostKey, "127.0.0.1"), hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, "")

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	requireCodePrefix(t, err, "HOST_KEY_UNKNOWN", ErrHostKeyUnknown)
}

func TestStreamReportsRevokedKeyBehindUntrustedCertificate(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, newHostCertSigner(t, newEd25519Signer(t), hostKey, "127.0.0.1"), hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, "@revoked * "+strings.TrimSpace(string(xssh.MarshalAuthorizedKey(hostKey.PublicKey())))+"\n")

	c := fx.client(t, srv, kh)

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	requireCodePrefix(t, err, "HOST_KEY_MISMATCH", ErrHostKeyMismatch)

	if !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("error %q does not say the key is revoked", err.Error())
	}
}

func TestStreamAddressDefaultsPort(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		host string
		port int
		want string
	}{
		{"bastion.example", 0, "bastion.example:22"},
		{"10.0.0.5", 2222, "10.0.0.5:2222"},
		{"::1", 0, "[::1]:22"},
	} {
		if got := streamAddress(tc.host, tc.port); got != tc.want {
			t.Errorf("streamAddress(%q, %d) = %q, want %q", tc.host, tc.port, got, tc.want)
		}
	}
}

func TestStreamRefusesEncryptedKeyWithoutPrompting(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	block, err := xssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("sesame"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	encPath := filepath.Join(fx.dir, "id_encrypted")
	writeFile(t, encPath, string(pem.EncodeToMemory(block)))

	c := fx.client(t, srv, kh)
	c.config.PrivateKeyPath = encPath
	c.config.Password = "sesame"

	_, err = runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	var missing *xssh.PassphraseMissingError
	if !errors.Is(err, ErrKeyEncryptedNoPassphrase) && !errors.As(err, &missing) {
		t.Fatalf("err = %v, want a missing passphrase refusal", err)
	}

	if _, execs := srv.counts(); execs != 0 {
		t.Fatalf("ran %d commands", execs)
	}
}

func TestStreamRequiresPrivateKey(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)
	c.config.PrivateKeyPath = ""
	c.config.Password = "hunter2"

	_, err := runStream(t, context.Background(), c, "say-ok", nil, io.Discard, io.Discard)
	if !errors.Is(err, ErrNoAuthMethodsAvailable) {
		t.Fatalf("err = %v, want ErrNoAuthMethodsAvailable", err)
	}

	if auths, _ := srv.counts(); auths != 0 {
		t.Fatalf("password-only stream reached auth %d times", auths)
	}
}

func TestStreamReturnsAtOnceForCancelledContext(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)
	hostKey := newEd25519Signer(t)
	srv := startTestSSHServer(t, fx.pub, hostKey)

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, srv.knownHostsLine(hostKey.PublicKey()))

	c := fx.client(t, srv, kh)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runStream(t, ctx, c, "say-ok", nil, io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if _, execs := srv.counts(); execs != 0 {
		t.Fatalf("ran %d commands", execs)
	}
}

func TestStreamCancelsDuringHandshake(t *testing.T) {
	t.Parallel()

	fx := newStreamFixture(t)

	var lc net.ListenConfig

	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	accepted := make(chan net.Conn, 1)

	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	t.Cleanup(func() { _ = ln.Close() })

	kh := filepath.Join(fx.dir, "known_hosts")
	writeFile(t, kh, "")

	silent := &testSSHServer{addr: ln.Addr().String()}
	c := fx.client(t, silent, kh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan streamResult, 1)

	go func() {
		code, err := c.Stream(ctx, "say-ok", nil, io.Discard, io.Discard)
		done <- streamResult{code, err}
	}()

	var conn net.Conn
	select {
	case conn = <-accepted:
	case r := <-done:
		t.Fatalf("Stream returned before connecting: %d %v", r.code, r.err)
	case <-time.After(streamGuard):
		t.Fatal("client never connected")
	}

	t.Cleanup(func() { _ = conn.Close() })

	cancel()

	// The bound sits well below streamHandshakeTimeout, so only the cancel
	// itself, not the handshake deadline, can end the wait in time.
	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", r.err)
		}
	case <-time.After(handshakeCancelBound):
		t.Fatalf("Stream did not return within %s of a cancel during the handshake", handshakeCancelBound)
	}
}

// readCounter counts calls to Read.
type readCounter struct {
	mu sync.Mutex
	n  int
	r  io.Reader
}

func (r *readCounter) Read(p []byte) (int, error) {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()

	return r.r.Read(p)
}

func (r *readCounter) reads() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.n
}

// recordingInput is a remote input that records what reached it.
type recordingInput struct {
	bytes.Buffer
	closed bool
}

func (r *recordingInput) Close() error {
	r.closed = true

	return nil
}

func TestStdinCopierAbortsWithoutClosingInput(t *testing.T) {
	t.Parallel()

	for _, readErr := range []error{
		errors.New("disk went away"),
		fmt.Errorf("chunk 3 truncated: %w", io.EOF),
	} {
		checkCopierAborts(t, readErr)
	}
}

func checkCopierAborts(t *testing.T, readErr error) {
	t.Helper()

	dst := &recordingInput{}
	aborts := 0

	copier := &stdinCopier{
		dst:   dst,
		src:   &failingReader{data: []byte("partial"), err: readErr},
		abort: func() { aborts++ },
		done:  make(chan struct{}),
	}

	copier.run()

	if dst.closed {
		t.Fatalf("read error %q closed the remote input, which reads as a clean end of input", readErr)
	}

	if aborts != 1 {
		t.Fatalf("abort ran %d times, want 1", aborts)
	}

	if !copier.aborted.Load() || !errors.Is(copier.readErr, readErr) {
		t.Fatalf("aborted = %v, readErr = %v; want true and the read error", copier.aborted.Load(), copier.readErr)
	}

	if dst.String() != "partial" {
		t.Fatalf("remote input got %q, want %q", dst.String(), "partial")
	}
}

func TestStdinCopierClosesInputAtEOF(t *testing.T) {
	t.Parallel()

	for _, src := range []io.Reader{nil, strings.NewReader("whole script")} {
		dst := &recordingInput{}
		copier := &stdinCopier{
			dst:   dst,
			src:   src,
			abort: func() { t.Error("abort ran on a clean end of input") },
			done:  make(chan struct{}),
		}

		copier.run()

		if !dst.closed || copier.aborted.Load() {
			t.Fatalf("closed = %v, aborted = %v; want true and false", dst.closed, copier.aborted.Load())
		}
	}
}
