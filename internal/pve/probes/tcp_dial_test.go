package probes_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/pve/probes"
)

// listenTCP starts a TCP listener on an ephemeral port and returns the listener
// and the port number. Caller must close the listener.
func listenTCP(t *testing.T) (net.Listener, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}
	return ln, ln.Addr().(*net.TCPAddr).Port
}

// T52 — dial an open port → OK.
func TestTCPDialProbe_Open(t *testing.T) {
	ln, port := listenTCP(t)
	defer ln.Close()

	p := &probes.TCPDialProbe{
		Host:    "127.0.0.1",
		Port:    port,
		Timeout: 2 * time.Second,
	}

	r := p.Run(context.Background())
	if !r.OK {
		t.Fatalf("expected OK=true for open port %d, got detail=%q remediation=%q", port, r.Detail, r.Remediation)
	}
	if !strings.Contains(r.Detail, "open") {
		t.Errorf("Detail should contain 'open': %q", r.Detail)
	}
}

// T53 — dial a port with no listener → FAIL with remediation.
func TestTCPDialProbe_Closed(t *testing.T) {
	// Allocate a port and immediately close the listener so nothing is listening.
	ln, port := listenTCP(t)
	ln.Close()

	p := &probes.TCPDialProbe{
		Host:    "127.0.0.1",
		Port:    port,
		Timeout: 2 * time.Second,
		Label:   "test-service",
	}

	r := p.Run(context.Background())
	if r.OK {
		t.Fatalf("expected OK=false for closed port %d", port)
	}
	if r.Remediation == "" {
		t.Error("expected non-empty Remediation for closed port")
	}
	if !strings.Contains(r.Remediation, "not reachable") {
		t.Errorf("Remediation should mention 'not reachable': %q", r.Remediation)
	}
}

func TestTCPDialProbe_EmptyHost_FAIL(t *testing.T) {
	p := &probes.TCPDialProbe{Host: "", Port: 25555}
	r := p.Run(context.Background())
	if r.OK {
		t.Fatal("expected OK=false for empty Host")
	}
	if !strings.Contains(r.Detail, "Host must not be empty") {
		t.Errorf("Detail=%q", r.Detail)
	}
}

func TestTCPDialProbe_ZeroPort_FAIL(t *testing.T) {
	p := &probes.TCPDialProbe{Host: "127.0.0.1", Port: 0}
	r := p.Run(context.Background())
	if r.OK {
		t.Fatal("expected OK=false for port 0")
	}
	if !strings.Contains(r.Detail, "not in range") {
		t.Errorf("Detail=%q", r.Detail)
	}
}

func TestTCPDialProbe_NegativePort_FAIL(t *testing.T) {
	p := &probes.TCPDialProbe{Host: "127.0.0.1", Port: -1}
	r := p.Run(context.Background())
	if r.OK {
		t.Fatal("expected OK=false for negative port")
	}
}

func TestTCPDialProbe_PortAboveMax_FAIL(t *testing.T) {
	p := &probes.TCPDialProbe{Host: "127.0.0.1", Port: 65536}
	r := p.Run(context.Background())
	if r.OK {
		t.Fatal("expected OK=false for port 65536")
	}
}

func TestTCPDialProbe_CancelledContext_FAIL(t *testing.T) {
	ln, port := listenTCP(t)
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	p := &probes.TCPDialProbe{
		Host:    "127.0.0.1",
		Port:    port,
		Timeout: 2 * time.Second,
	}
	r := p.Run(ctx)
	if r.OK {
		t.Fatal("expected OK=false for cancelled context")
	}
	if !strings.Contains(r.Detail, "context cancelled") {
		t.Errorf("Detail should mention context cancelled: %q", r.Detail)
	}
}

func TestTCPDialProbe_DefaultTimeout_Used(t *testing.T) {
	// Timeout=0 → default applied; probe should not panic or hang.
	ln, port := listenTCP(t)
	defer ln.Close()

	p := &probes.TCPDialProbe{
		Host:    "127.0.0.1",
		Port:    port,
		Timeout: 0, // trigger default
	}
	r := p.Run(context.Background())
	if !r.OK {
		t.Fatalf("expected OK=true for open port with default timeout, got detail=%q", r.Detail)
	}
}

func TestTCPDialProbe_LabelInName(t *testing.T) {
	p := &probes.TCPDialProbe{Host: "host", Port: 25555, Label: "director"}
	name := p.Name()
	if !strings.Contains(name, "director") {
		t.Errorf("Name()=%q should contain label 'director'", name)
	}
}

func TestTCPDialProbe_NoLabel_NameContainsHostPort(t *testing.T) {
	p := &probes.TCPDialProbe{Host: "10.0.0.1", Port: 4222}
	name := p.Name()
	if !strings.Contains(name, "10.0.0.1") || !strings.Contains(name, "4222") {
		t.Errorf("Name()=%q should contain host and port", name)
	}
}

func TestTCPDialProbe_OpenPort_RemediationEmpty(t *testing.T) {
	ln, port := listenTCP(t)
	defer ln.Close()

	p := &probes.TCPDialProbe{Host: "127.0.0.1", Port: port, Timeout: 2 * time.Second}
	r := p.Run(context.Background())
	if !r.OK {
		t.Fatalf("expected OK=true")
	}
	if r.Remediation != "" {
		t.Errorf("Remediation should be empty on success, got %q", r.Remediation)
	}
}
