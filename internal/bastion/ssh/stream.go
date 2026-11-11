package ssh

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Host key refusals from Stream. The error text starts with the code, so a
// caller that only sees the message can still act on it.
var (
	// ErrHostKeyUnknown reports a host whose key the known_hosts file does
	// not list, including when the file does not exist.
	ErrHostKeyUnknown = errors.New("HOST_KEY_UNKNOWN")

	// ErrHostKeyMismatch reports a host that presented a key other than the
	// one the known_hosts file lists for it, or a key marked @revoked.
	ErrHostKeyMismatch = errors.New("HOST_KEY_MISMATCH")

	// ErrExitStatusMissing reports a remote command that ended without
	// sending an exit status or an exit signal.
	ErrExitStatusMissing = errors.New("remote command ended without an exit status")
)

// streamHandshakeTimeout bounds the TCP connect and, separately, the ssh
// handshake. The caller's context ends either one sooner when it is
// cancelled or its deadline comes first.
const streamHandshakeTimeout = mediumTimeout

// certAlgoSuffix marks a host certificate algorithm name.
const certAlgoSuffix = "-cert-v01@openssh.com"

// Stream runs cmd on the host over a connection of its own and returns the
// command's exit status.
//
// The host key must already be in the known_hosts file named by
// ConnectionDetails.KnownHostsPath, or $HOME/.ssh/known_hosts when that is
// empty. An unknown host fails with an error wrapping ErrHostKeyUnknown, and a
// changed or revoked key with one wrapping ErrHostKeyMismatch. Stream never
// writes the known_hosts file, never prompts, and never reads the terminal.
// It authenticates only with the key at PrivateKeyPath, which must not be
// encrypted, and ignores Password, UseSSHPass, and SSHOptions. It does not
// touch the connection that Connect manages.
//
// Stream copies stdin to the command and closes the command's input when a
// read from stdin returns io.EOF. A nil stdin sends EOF at once. If a read
// fails with any other error, including one that only wraps io.EOF, Stream
// does not close the command's input. It asks the server to kill the command
// with SIGKILL, drops the connection, and returns -1 with an error wrapping
// the read error. A server may ignore that signal, as OpenSSH before 7.9
// does and later releases do for a forced command or a subsystem, and it may
// then give the command a clean end of input when the connection drops. A
// caller that sends a script on stdin must send it in a form that does
// nothing when cut short, such as one that defines a function and calls it
// only on its last line.
//
// Stream stops reading stdin before it returns only when stdin has a
// SetReadDeadline method and that call succeeds, as it does for the read end
// of an os.Pipe on unix. It then clears the deadline it set. os.Stdin, as Go
// opens it, does not accept read deadlines, and neither does an io.Pipe. For
// such a reader, if the command exits while a read is blocked, that read
// stays blocked after Stream returns, and whatever it eventually yields is
// discarded. A caller that reads its own input again after Stream returns
// must pass a pipe that it owns and feeds, not os.Stdin.
//
// Output is written to stdout and stderr as it arrives, from separate
// goroutines, with writes serialized so one writer may serve both. A nil
// writer discards its stream. No writes happen after Stream returns.
//
// A non-zero exit status is not an error, so Stream returns it with a nil
// error. It returns -1 with an error when the command did not run to an exit
// status. Cancelling ctx closes the connection without sending a signal, so a
// caller can leave a remote job running, and what the job does after its
// connection drops is up to the server. Stream then returns -1 with an error
// wrapping ctx.Err(), unless the exit status had already arrived, in which
// case it returns that status. A cancel waits for a write already in progress
// to return, so stdout and stderr must not block indefinitely.
func (c *Client) Stream(ctx context.Context, cmd string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	err := ctx.Err()
	if err != nil {
		return -1, fmt.Errorf("stream to %s: %w", c.config.Host, err)
	}

	client, err := c.dialStrict(ctx)
	if err != nil {
		return -1, err
	}

	defer func() { _ = client.Close() }()

	return runStreamSession(ctx, client, cmd, stdin, stdout, stderr)
}

// dialStrict opens an ssh connection that verifies the host key strictly.
func (c *Client) dialStrict(ctx context.Context) (*ssh.Client, error) {
	knownHostsPath, err := c.streamKnownHostsPath()
	if err != nil {
		return nil, err
	}

	known, err := loadKnownHosts(knownHostsPath)
	if err != nil {
		return nil, err
	}

	auth, err := c.streamAuth()
	if err != nil {
		return nil, err
	}

	address := streamAddress(c.config.Host, c.config.Port)
	entries := known.entriesFor(address)
	verdict := &hostKeyVerdict{mu: sync.Mutex{}, err: nil}

	config := &ssh.ClientConfig{
		User:              c.config.User,
		Auth:              []ssh.AuthMethod{auth},
		HostKeyCallback:   strictHostKeyCallback(known, entries, verdict),
		BannerCallback:    nil,
		ClientVersion:     "",
		HostKeyAlgorithms: preferredHostKeyAlgorithms(entries),
		Config: ssh.Config{
			Rand:           nil,
			RekeyThreshold: 0,
			KeyExchanges:   nil,
			Ciphers:        nil,
			MACs:           nil,
		},
	}

	return handshake(ctx, c.createNetDialer(), address, config, verdict)
}

// handshake dials address and runs the ssh handshake, closing the socket if
// ctx is cancelled before the handshake finishes.
func handshake(ctx context.Context, dialer net.Dialer, address string, config *ssh.ClientConfig, verdict *hostKeyVerdict) (*ssh.Client, error) {
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("stream to %s: %w", address, ctx.Err())
		}

		return nil, fmt.Errorf("dial %s: %w", address, err)
	}

	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })

	_ = conn.SetDeadline(time.Now().Add(streamHandshakeTimeout))

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, address, config)
	stopped := stop()

	if err != nil {
		_ = conn.Close()

		refusal := verdict.get()
		if refusal != nil {
			return nil, refusal
		}

		if ctx.Err() != nil {
			return nil, fmt.Errorf("stream to %s: %w", address, ctx.Err())
		}

		return nil, fmt.Errorf("ssh handshake with %s: %w", address, err)
	}

	// The handshake can finish just as ctx is cancelled, after the cancel
	// has already closed the socket under it. The connection is then dead,
	// so report the cancel rather than hand back a broken client.
	if !stopped {
		_ = sshConn.Close()

		return nil, fmt.Errorf("stream to %s: %w", address, ctx.Err())
	}

	_ = conn.SetDeadline(time.Time{})

	return ssh.NewClient(sshConn, chans, reqs), nil
}

// runStreamSession runs cmd in a new session on client.
func runStreamSession(ctx context.Context, client *ssh.Client, cmd string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	// Closing the client unblocks every step below, so it is the one
	// cancellation path for the whole session.
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()

	session, err := client.NewSession()
	if err != nil {
		return -1, streamFailure(ctx, "open session", err)
	}

	defer func() { _ = session.Close() }()

	var mu sync.Mutex

	session.Stdout = &lockedWriter{mu: &mu, w: orDiscard(stdout)}
	session.Stderr = &lockedWriter{mu: &mu, w: orDiscard(stderr)}

	remoteStdin, err := session.StdinPipe()
	if err != nil {
		return -1, streamFailure(ctx, "open stdin", err)
	}

	err = session.Start(cmd)
	if err != nil {
		return -1, streamFailure(ctx, "start command", err)
	}

	copier := &stdinCopier{
		dst: remoteStdin,
		src: stdin,
		abort: func() {
			_ = session.Signal(ssh.SIGKILL)
			_ = client.Close()
		},
		stopping: atomic.Bool{},
		aborted:  atomic.Bool{},
		done:     make(chan struct{}),
		readErr:  nil,
	}

	go copier.run()

	waitErr := session.Wait()
	notCancelled := stop()

	// Every byte of output has been delivered by now. Closing the client
	// fails any write the copier has in flight, so stopping it cannot hang.
	_ = client.Close()

	copier.stop()

	if copier.aborted.Load() {
		return -1, fmt.Errorf("read stdin for remote command: %w", copier.readErr)
	}

	code, err := exitResult(waitErr)
	if err != nil && !notCancelled {
		return -1, fmt.Errorf("stream: %w", ctx.Err())
	}

	return code, err
}

// streamFailure prefers the context's error when ctx caused the failure.
func streamFailure(ctx context.Context, step string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("stream: %s: %w", step, ctx.Err())
	}

	return fmt.Errorf("stream: %s: %w", step, err)
}

// exitResult turns the session's wait error into an exit status.
func exitResult(waitErr error) (int, error) {
	if waitErr == nil {
		return 0, nil
	}

	var exitErr *ssh.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitStatus(), nil
	}

	var missing *ssh.ExitMissingError
	if errors.As(waitErr, &missing) {
		return -1, ErrExitStatusMissing
	}

	return -1, fmt.Errorf("stream: %w", waitErr)
}

// stdinCopyBuffer is the size of each read from the caller's stdin.
const stdinCopyBuffer = 32 * 1024

// stdinCopier copies the caller's stdin to the remote command's input.
type stdinCopier struct {
	dst   io.WriteCloser
	src   io.Reader
	abort func()

	// stopping marks a read error as one that stop caused on purpose.
	stopping atomic.Bool

	// aborted is set after readErr is written, so a reader that sees it set
	// may read readErr.
	aborted atomic.Bool

	done    chan struct{}
	readErr error
}

// run copies until src ends, a write fails, or a read fails. It closes dst
// only at a clean EOF. A read error that stop did not cause is recorded and
// handed to abort instead, so the command never sees a clean end of input.
func (s *stdinCopier) run() {
	defer close(s.done)

	if s.src == nil {
		_ = s.dst.Close()

		return
	}

	buf := make([]byte, stdinCopyBuffer)

	for {
		n, err := s.src.Read(buf)
		if n > 0 {
			_, werr := s.dst.Write(buf[:n])
			if werr != nil {
				return
			}
		}

		switch {
		case err == nil:
			continue
		// io.Reader returns io.EOF itself at a clean end, as io.Copy assumes.
		// An error that only wraps io.EOF reports a failure, and treating it
		// as the end would run a script that was cut short.
		case err == io.EOF: //nolint:errorlint // a wrapped EOF is a failure, not the end
			_ = s.dst.Close()
		case s.stopping.Load():
		default:
			s.readErr = err
			s.aborted.Store(true)
			s.abort()
		}

		return
	}
}

// readDeadliner is a reader whose blocked Read can be interrupted.
type readDeadliner interface {
	SetReadDeadline(t time.Time) error
}

// stop ends the copy and waits for it when src supports read deadlines, then
// clears the deadline so the caller can keep reading src. Any other src
// cannot be interrupted, so stop returns without waiting.
func (s *stdinCopier) stop() {
	select {
	case <-s.done:
		return
	default:
	}

	src, ok := s.src.(readDeadliner)
	if !ok {
		return
	}

	s.stopping.Store(true)

	if src.SetReadDeadline(time.Now()) != nil {
		return
	}

	<-s.done

	_ = src.SetReadDeadline(time.Time{})
}

// lockedWriter serializes writes to w across goroutines that share mu.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.w.Write(p)
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}

	return w
}

// streamAddress joins host and port, defaulting port 0 to the ssh port.
func streamAddress(host string, port int) string {
	if port == 0 {
		port = DefaultSSHPort
	}

	return net.JoinHostPort(host, strconv.Itoa(port))
}

// streamKnownHostsPath resolves the known_hosts file Stream checks.
func (c *Client) streamKnownHostsPath() (string, error) {
	if c.config.KnownHostsPath != "" {
		return c.config.KnownHostsPath, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find known_hosts: %w", err)
	}

	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

// knownHostsFile is a parsed known_hosts file.
type knownHostsFile struct {
	path  string
	check ssh.HostKeyCallback

	// caLines maps the number of each @cert-authority line to its key.
	caLines map[int][]byte
}

// loadKnownHosts reads path into a host key check. A file that does not exist
// is an empty list, so every host is unknown. Any other failure is an error.
func loadKnownHosts(path string) (*knownHostsFile, error) {
	check, err := knownhosts.New(path)
	if errors.Is(err, fs.ErrNotExist) {
		empty, emptyErr := knownhosts.New()
		if emptyErr != nil {
			return nil, fmt.Errorf("read known_hosts %s: %w", path, emptyErr)
		}

		return &knownHostsFile{path: path, check: empty, caLines: nil}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read known_hosts %s: %w", path, err)
	}

	caLines, err := certAuthorityLines(path)
	if err != nil {
		return nil, fmt.Errorf("read known_hosts %s: %w", path, err)
	}

	return &knownHostsFile{path: path, check: check, caLines: caLines}, nil
}

// certAuthorityLines finds the @cert-authority lines in path, numbered from
// one and counting every line, as the knownhosts package numbers them.
func certAuthorityLines(path string) (map[int][]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the caller names its known_hosts file
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	lines := map[int][]byte{}
	scanner := bufio.NewScanner(bytes.NewReader(data))

	for n := 1; scanner.Scan(); n++ {
		marker, _, key, _, _, parseErr := ssh.ParseKnownHosts(scanner.Bytes())
		if parseErr == nil && marker == "cert-authority" {
			lines[n] = key.Marshal()
		}
	}

	err = scanner.Err()
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	return lines, nil
}

// isCertAuthority reports whether known came from an @cert-authority line.
// It compares the key as well as the line, in case the file changed between
// the two reads.
func (f *knownHostsFile) isCertAuthority(known knownhosts.KnownKey) bool {
	key, ok := f.caLines[known.Line]

	return ok && known.Filename == f.path && bytes.Equal(key, known.Key.Marshal())
}

// hostEntries is what a known_hosts file lists for one address.
type hostEntries struct {
	// plain holds the host keys listed for the address.
	plain []knownhosts.KnownKey

	// certAuthority is true when an @cert-authority line covers the address.
	certAuthority bool
}

func (e hostEntries) empty() bool {
	return len(e.plain) == 0 && !e.certAuthority
}

// split separates host keys from authority keys in a list of matches. The
// knownhosts package returns both kinds when a plain host key does not
// match, without saying which is which.
func (f *knownHostsFile) split(want []knownhosts.KnownKey) hostEntries {
	var entries hostEntries

	for _, known := range want {
		if f.isCertAuthority(known) {
			entries.certAuthority = true

			continue
		}

		entries.plain = append(entries.plain, known)
	}

	return entries
}

// entriesFor lists what the file holds for address.
func (f *knownHostsFile) entriesFor(address string) hostEntries {
	// An all-zero ed25519 key matches no real entry, so the check fails with
	// a KeyError that lists every key the file holds for this address.
	probe, err := ssh.NewPublicKey(ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)))
	if err != nil {
		return hostEntries{plain: nil, certAuthority: false}
	}

	var keyErr *knownhosts.KeyError
	if !errors.As(f.check(address, probeAddr(address), probe), &keyErr) {
		return hostEntries{plain: nil, certAuthority: false}
	}

	return f.split(keyErr.Want)
}

// streamAuth loads the unencrypted private key at PrivateKeyPath.
//
//nolint:ireturn // ssh.AuthMethod is the crypto/ssh interface
func (c *Client) streamAuth() (ssh.AuthMethod, error) {
	if c.config.PrivateKeyPath == "" {
		return nil, ErrNoAuthMethodsAvailable
	}

	keyManager := c.keyManager
	if keyManager == nil {
		keyManager = NewKeyManager()
	}

	auth, err := keyManager.CreatePublicKeyAuth(c.config.PrivateKeyPath, "")
	if err != nil {
		return nil, fmt.Errorf("load private key %s: %w", c.config.PrivateKeyPath, err)
	}

	return auth, nil
}

// hostKeyVerdict holds the refusal from the host key check, so the handshake
// can return it unwrapped with its code first.
type hostKeyVerdict struct {
	mu  sync.Mutex
	err error
}

func (v *hostKeyVerdict) set(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.err == nil {
		v.err = err
	}
}

func (v *hostKeyVerdict) get() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.err
}

// strictHostKeyCallback accepts only a key that the file accepts, and
// records why it refused any other.
//
// The knownhosts package also matches a plain host key against the keys on
// @cert-authority lines, so a host that presents a listed authority's own
// public key is accepted, where OpenSSH would refuse it. Presenting that key
// needs the authority's private key, which could sign a valid certificate
// anyway, so Stream leaves that behaviour alone.
func strictHostKeyCallback(file *knownHostsFile, entries hostEntries, verdict *hostKeyVerdict) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := file.check(hostname, remote, key)
		if err == nil {
			return nil
		}

		refusal := classifyHostKeyError(err, hostname, remote, file, entries, key)
		verdict.set(refusal)

		return refusal
	}
}

// classifyHostKeyError names a failed host key check with its code.
func classifyHostKeyError(err error, hostname string, remote net.Addr, file *knownHostsFile, entries hostEntries, key ssh.PublicKey) error {
	presented := key.Type() + " key " + ssh.FingerprintSHA256(key)

	var revoked *knownhosts.RevokedError
	if errors.As(err, &revoked) {
		return fmt.Errorf("%w: %s presented %s, which %s:%d marks @revoked",
			ErrHostKeyMismatch, hostname, presented, revoked.Revoked.Filename, revoked.Revoked.Line)
	}

	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		plain := file.split(keyErr.Want).plain
		if len(plain) == 0 {
			return fmt.Errorf("%w: %s presented %s, which %s does not list",
				ErrHostKeyUnknown, hostname, presented, file.path)
		}

		want := plain[0]

		return fmt.Errorf("%w: %s presented %s, but %s:%d lists %s key %s",
			ErrHostKeyMismatch, hostname, presented, want.Filename, want.Line,
			want.Key.Type(), ssh.FingerprintSHA256(want.Key))
	}

	if cert, ok := key.(*ssh.Certificate); ok {
		// A certificate that fails the authority check says nothing about
		// its own key, which @revoked can still name.
		if errors.As(file.check(hostname, remote, cert.Key), &revoked) {
			return fmt.Errorf("%w: %s presented a host certificate for %s key %s, which %s:%d marks @revoked",
				ErrHostKeyMismatch, hostname, cert.Key.Type(), ssh.FingerprintSHA256(cert.Key),
				revoked.Revoked.Filename, revoked.Revoked.Line)
		}

		signer := "a host certificate signed by " + cert.SignatureKey.Type() +
			" key " + ssh.FingerprintSHA256(cert.SignatureKey)

		if entries.empty() {
			return fmt.Errorf("%w: %s presented %s, and %s lists nothing for it",
				ErrHostKeyUnknown, hostname, signer, file.path)
		}

		return fmt.Errorf("%w: %s presented %s, which %s does not accept: %w",
			ErrHostKeyMismatch, hostname, signer, file.path, err)
	}

	return fmt.Errorf("host key check for %s failed: %w", hostname, err)
}

// probeAddr is the remote address handed to a host key check when no
// connection exists yet.
type probeAddr string

func (a probeAddr) Network() string { return "tcp" }
func (a probeAddr) String() string  { return string(a) }

// preferredHostKeyAlgorithms orders the host key algorithms so that a host
// offering several key types is asked for one the file can vouch for.
// Without this, the library's default order could pick a type the file does
// not list and report a false mismatch. Certificate algorithms come first
// only when an @cert-authority line covers the host, and last otherwise, so
// a host with both a certificate and a listed plain key presents the plain
// key. It returns nil, meaning the library default, when the file lists
// nothing for the host.
func preferredHostKeyAlgorithms(entries hostEntries) []string {
	if entries.empty() {
		return nil
	}

	supported := ssh.SupportedAlgorithms().HostKeys

	var certs, known, rest []string

	for _, want := range entries.plain {
		for _, algo := range algorithmsForKeyType(want.Key.Type()) {
			if slices.Contains(supported, algo) && !slices.Contains(known, algo) {
				known = append(known, algo)
			}
		}
	}

	for _, algo := range supported {
		switch {
		case strings.HasSuffix(algo, certAlgoSuffix):
			certs = append(certs, algo)
		case !slices.Contains(known, algo):
			rest = append(rest, algo)
		}
	}

	if entries.certAuthority {
		return slices.Concat(certs, known, rest)
	}

	return slices.Concat(known, rest, certs)
}

// algorithmsForKeyType lists the signature algorithms a host key of keyType
// can use.
func algorithmsForKeyType(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512}
	}

	return []string{keyType}
}
