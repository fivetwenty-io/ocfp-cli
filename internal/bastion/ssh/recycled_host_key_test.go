package ssh

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// startRecyclingServer serves temporary for the first `temporaryConns`
// connections, the way a recycled bastion presents the keys its fresh root
// disk generated until it restores its own from the data disk, and restored
// after that.
func startRecyclingServer(t *testing.T, temporaryConns int32, temporary, restored gossh.Signer) *hostKeyTestServer {
	t.Helper()

	return startHostKeyTestServerFunc(t, func(conn int32) []gossh.Signer {
		if conn <= temporaryConns {
			return []gossh.Signer{temporary}
		}

		return []gossh.Signer{restored}
	})
}

// A recycled bastion first presents a key its new root disk generated, then
// the key it held before the recycle once it restores it. Recycle mode must
// wait through the first, never record it, and accept the second, recording
// it under an address that has no entry yet.
func TestRecycledHostAcceptsItsPreviousKeyAfterATemporaryOne(t *testing.T) {
	for _, onRecord := range []bool{false, true} {
		name := "new address"
		if onRecord {
			name = "address on record"
		}

		t.Run(name, func(t *testing.T) {
			knownHosts := isolateSSHEnv(t)

			previous, temporary := newEd25519Signer(t), newEd25519Signer(t)
			srv := startRecyclingServer(t, 1, temporary, previous)

			recordedAs := "10.0.0.9"
			if onRecord {
				recordedAs = srv.address()
			}

			writeKnownHosts(t, knownHosts, recordedAs, previous.PublicKey())

			client := newLoopbackClient(srv)
			client.ExpectRecycledHostKeys([]gossh.PublicKey{previous.PublicKey()}, 30*time.Second)

			err := connectWithin(t, client, 30*time.Second)
			if err != nil {
				t.Fatalf("connect to a recycled host that restored its previous key: %v", err)
			}

			if got := srv.conns.Load(); got < 2 {
				t.Errorf("server saw %d connections, want a retry after the temporary key", got)
			}

			content := readFile(t, knownHosts)
			if strings.Contains(content, authorizedKey(temporary.PublicKey())) {
				t.Errorf("known_hosts recorded the temporary key:\n%s", content)
			}

			want := knownhosts.Line([]string{recordedAs}, previous.PublicKey()) + "\n"
			if !onRecord {
				want += knownhosts.Line([]string{srv.address()}, previous.PublicKey()) + "\n"
			}

			if content != want {
				t.Errorf("known_hosts after the recycle:\n got: %q\nwant: %q", content, want)
			}
		})
	}
}

// The address a recycled bastion comes back on may hold an entry some
// earlier host left there. The previous key proves the host, so the stale
// lines for that address give way to it, and an ordinary connection
// afterwards finds the key it needs. Lines for the same host on other ports
// and lines for other hosts stay as they were.
func TestRecycledHostReplacesAStaleAddressEntryWithItsPreviousKey(t *testing.T) {
	knownHosts := isolateSSHEnv(t)

	previous, temporary, stale, otherPort := newEd25519Signer(t), newEd25519Signer(t), newEd25519Signer(t), newEd25519Signer(t)
	srv := startRecyclingServer(t, 1, temporary, previous)

	kept := knownhosts.Line([]string{"10.0.0.9"}, previous.PublicKey()) + "\n" +
		knownhosts.Line([]string{"127.0.0.1:2"}, otherPort.PublicKey()) + "\n" +
		knownhosts.Line([]string{"127.0.0.1"}, otherPort.PublicKey()) + "\n"
	content := kept +
		knownhosts.Line([]string{srv.address()}, stale.PublicKey()) + "\n" +
		knownhosts.Line([]string{knownhosts.HashHostname(knownhosts.Normalize(srv.address()))}, stale.PublicKey()) + "\n"

	writeKnownHosts(t, knownHosts, srv.address())

	err := os.WriteFile(knownHosts, []byte(content), privateKeyMode)
	if err != nil {
		t.Fatal(err)
	}

	core, logs := observer.New(zapcore.InfoLevel)
	client := newLoopbackClient(srv)
	client.log = zap.New(core).Sugar()
	client.ExpectRecycledHostKeys([]gossh.PublicKey{previous.PublicKey()}, 30*time.Second)

	err = connectWithin(t, client, 30*time.Second)
	if err != nil {
		t.Fatalf("connect to a recycled host over a stale address entry: %v", err)
	}

	want := kept + knownhosts.Line([]string{srv.address()}, previous.PublicKey()) + "\n"
	if after := readFile(t, knownHosts); after != want {
		t.Errorf("known_hosts after replacing the stale entry:\n got: %q\nwant: %q", after, want)
	}

	logged := false

	for _, entry := range logs.All() {
		fields := entry.ContextMap()
		if fields["old"] == nil || fields["new"] == nil {
			continue
		}

		old, _ := fields["old"].(string)
		if strings.Contains(old, gossh.FingerprintSHA256(stale.PublicKey())) &&
			fields["new"] == describeKey(previous.PublicKey()) {
			logged = true
		}
	}

	if !logged {
		t.Errorf("no log naming the stale fingerprint and the recorded one; logs: %v", logs.All())
	}

	// An ordinary connection, outside recycle mode, now finds the key.
	again := newLoopbackClient(srv)

	err = connectWithin(t, again, 10*time.Second)
	if err != nil {
		t.Fatalf("ordinary connect after the recycle: %v", err)
	}
}

// A recycled host that never presents its previous key must fail once the
// wait ends, naming both keys, and leave known_hosts as it was.
func TestRecycledHostTimesOutWithoutItsPreviousKey(t *testing.T) {
	knownHosts := isolateSSHEnv(t)

	previous, temporary := newEd25519Signer(t), newEd25519Signer(t)
	srv := startRecyclingServer(t, 1<<30, temporary, previous)
	writeKnownHosts(t, knownHosts, srv.address(), previous.PublicKey())

	before := readFile(t, knownHosts)

	client := newLoopbackClient(srv)
	client.ExpectRecycledHostKeys([]gossh.PublicKey{previous.PublicKey()}, 500*time.Millisecond)

	start := time.Now()
	err := connectWithin(t, client, 30*time.Second)

	if !errors.Is(err, ErrRecycledHostKeyNotRestored) {
		t.Fatalf("Connect error = %v, want ErrRecycledHostKeyNotRestored", err)
	}

	if errors.Is(err, ErrHostKeyMismatch) {
		t.Errorf("a temporary key reported as a plain mismatch: %v", err)
	}

	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Connect took %s, want it to stop near the 500ms wait", elapsed)
	}

	var timeout *RecycledHostKeyError
	if !errors.As(err, &timeout) {
		t.Fatalf("Connect error %v does not carry a *RecycledHostKeyError", err)
	}

	host := "[127.0.0.1]:" + strconv.Itoa(srv.port)
	for _, want := range []string{host, gossh.FingerprintSHA256(previous.PublicKey()), gossh.FingerprintSHA256(temporary.PublicKey())} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}

	if after := readFile(t, knownHosts); after != before {
		t.Errorf("known_hosts changed while waiting for the previous key:\nbefore: %q\nafter:  %q", before, after)
	}
}

// With no key on record from before the recycle there is nothing to check
// the host against. Recycle mode fails closed: it never dials, never records
// a key, and never hands the host to the external ssh fallback.
func TestRecycledHostWithNoPreviousKeyFailsClosed(t *testing.T) {
	knownHosts := isolateSSHEnv(t)
	ran := fakeSSHOnPath(t)

	signer := newEd25519Signer(t)
	srv := startHostKeyTestServer(t, signer)

	client := newLoopbackClient(srv)
	client.ExpectRecycledHostKeys(nil, time.Minute)

	start := time.Now()
	err := connectWithin(t, client, 30*time.Second)

	if !errors.Is(err, ErrNoPreviousHostKey) {
		t.Fatalf("Connect error = %v, want ErrNoPreviousHostKey", err)
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Connect took %s, want it to stop at once", elapsed)
	}

	if got := srv.conns.Load(); got != 0 {
		t.Errorf("server saw %d connections, want none", got)
	}

	if _, statErr := os.Stat(knownHosts); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("known_hosts was written: %v", statErr)
	}

	if _, statErr := os.Stat(ran); statErr == nil {
		t.Error("the external ssh fallback ran")
	}
}

// In recycle mode the external ssh fallback never runs, even when every
// native attempt fails, because it applies its own known_hosts rules. A
// fake ssh on PATH records whether it was started.
func TestRecycledHostNeverUsesTheExternalFallback(t *testing.T) {
	knownHosts := isolateSSHEnv(t)
	ran := fakeSSHOnPath(t)

	previous, temporary := newEd25519Signer(t), newEd25519Signer(t)
	srv := startRecyclingServer(t, 1<<30, temporary, previous)
	writeKnownHosts(t, knownHosts, srv.address(), previous.PublicKey())

	client := newLoopbackClient(srv)
	client.ExpectRecycledHostKeys([]gossh.PublicKey{previous.PublicKey()}, 500*time.Millisecond)

	err := connectWithin(t, client, 30*time.Second)
	if !errors.Is(err, ErrRecycledHostKeyNotRestored) {
		t.Fatalf("Connect error = %v, want ErrRecycledHostKeyNotRestored", err)
	}

	if _, statErr := os.Stat(ran); statErr == nil {
		t.Error("the external ssh fallback ran in recycle mode")
	}
}

// Cancelling the caller's context while Connect waits for a recycled host's
// previous key stops the wait at once with a cancellation, not a timeout.
func TestRecycledHostWaitStopsWhenTheCallerCancels(t *testing.T) {
	knownHosts := isolateSSHEnv(t)

	previous, temporary := newEd25519Signer(t), newEd25519Signer(t)
	srv := startRecyclingServer(t, 1<<30, temporary, previous)
	writeKnownHosts(t, knownHosts, srv.address(), previous.PublicKey())

	client := newLoopbackClient(srv)
	client.ExpectRecycledHostKeys([]gossh.PublicKey{previous.PublicKey()}, time.Minute)

	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var cancelled atomic.Int64

	go func() {
		for srv.conns.Load() < 1 {
			time.Sleep(10 * time.Millisecond)
		}

		time.Sleep(100 * time.Millisecond)
		cancelled.Store(time.Now().UnixNano())
		cancel()
	}()

	err := client.Connect(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect error = %v, want context.Canceled", err)
	}

	if errors.Is(err, ErrRecycledHostKeyNotRestored) {
		t.Errorf("a cancelled wait reported as a timeout: %v", err)
	}

	if since := time.Since(time.Unix(0, cancelled.Load())); since > 2*time.Second {
		t.Errorf("Connect returned %s after the cancel, want promptly", since)
	}
}
