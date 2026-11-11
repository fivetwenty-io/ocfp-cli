package ssh

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// startSilentServer accepts TCP connections on 127.0.0.1 and never sends an
// SSH banner, the way a stalled or hostile peer can. Accepted connections
// stay open until the test ends.
func startSilentServer(t *testing.T) *hostKeyTestServer {
	t.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", listener.Addr())
	}

	srv := &hostKeyTestServer{port: tcpAddr.Port}

	var (
		mu    sync.Mutex
		conns []net.Conn
	)

	t.Cleanup(func() {
		_ = listener.Close()

		mu.Lock()
		defer mu.Unlock()

		for _, conn := range conns {
			_ = conn.Close()
		}
	})

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			srv.conns.Add(1)

			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()

	return srv
}

// returnsWithin runs fn and fails the test if it has not returned within
// limit.
func returnsWithin(t *testing.T, limit time.Duration, fn func() error) error {
	t.Helper()

	done := make(chan error, 1)

	go func() { done <- fn() }()

	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("still blocked in the SSH handshake after %s", limit)

		return nil
	}
}

// A peer that accepts the connection and never sends its banner must not
// hold Connect past the caller's deadline or past a cancellation.
func TestConnectHandshakeHonoursTheContext(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		isolateSSHEnv(t)

		srv := startSilentServer(t)
		client := newLoopbackClient(srv)

		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()

		err := returnsWithin(t, 5*time.Second, func() error { return client.Connect(ctx) })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Connect error = %v, want context.DeadlineExceeded", err)
		}

		if srv.conns.Load() == 0 {
			t.Fatal("the client never reached the silent server")
		}
	})

	t.Run("cancel", func(t *testing.T) {
		isolateSSHEnv(t)

		srv := startSilentServer(t)
		client := newLoopbackClient(srv)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		go func() {
			for srv.conns.Load() == 0 {
				time.Sleep(10 * time.Millisecond)
			}

			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		err := returnsWithin(t, 5*time.Second, func() error { return client.Connect(ctx) })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Connect error = %v, want context.Canceled", err)
		}
	})
}

// With no deadline on the context, the handshake is still bounded by the
// connect timeout in the client config.
func TestHandshakeIsBoundedByTheConnectTimeout(t *testing.T) {
	isolateSSHEnv(t)

	srv := startSilentServer(t)
	client := newLoopbackClient(srv)

	config, err := client.prepareSSHConfig()
	if err != nil {
		t.Fatal(err)
	}

	config.Timeout = 300 * time.Millisecond

	err = returnsWithin(t, 5*time.Second, func() error {
		conn, connectErr := client.connectWithNativeClient(context.Background(), config)
		if conn != nil {
			_ = conn.Close()
		}

		return connectErr
	})

	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("handshake error = %v, want a timeout", err)
	}
}

// Once the handshake succeeds its deadline is cleared, so the connection
// stays usable after the connect timeout has passed.
func TestHandshakeDeadlineIsClearedAfterSuccess(t *testing.T) {
	isolateSSHEnv(t)

	srv := startHostKeyTestServer(t, newEd25519Signer(t))
	client := newLoopbackClient(srv)

	config, err := client.prepareSSHConfig()
	if err != nil {
		t.Fatal(err)
	}

	config.Timeout = 300 * time.Millisecond

	conn, err := client.connectWithNativeClient(t.Context(), config)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	time.Sleep(600 * time.Millisecond)

	_, _, err = conn.SendRequest("keepalive@openssh.com", true, nil)
	if err != nil {
		t.Fatalf("request after the connect timeout passed: %v", err)
	}
}
