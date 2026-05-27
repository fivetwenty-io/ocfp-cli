package probes

import (
	"context"
	"fmt"
	"net"
	"time"
)

const (
	defaultTCPTimeout = 10 * time.Second
)

// TCPDialProbe checks whether a TCP port is reachable within a timeout.
// It opens a connection, immediately closes it on success, and returns FAIL
// with a remediation hint when the port is not reachable.
type TCPDialProbe struct {
	// Host is the hostname or IP address to dial.
	Host string
	// Port is the TCP port number to dial.
	Port int
	// Timeout is the dial deadline. When zero, defaultTCPTimeout (10 s) is used.
	Timeout time.Duration
	// Label is an optional human-readable service name used in error messages.
	// When empty, "<host>:<port>" is used.
	Label string
}

// Name implements Probe.
func (p *TCPDialProbe) Name() string {
	if p.Label != "" {
		return fmt.Sprintf("tcp-dial(%s)", p.Label)
	}
	return fmt.Sprintf("tcp-dial(%s:%d)", p.Host, p.Port)
}

// Run implements Probe.
//
// Inputs and validation:
//   - Host must not be empty; an empty Host returns FAIL immediately.
//   - Port must be in the range 1–65535; out-of-range returns FAIL immediately.
//   - Timeout <= 0 uses the 10 s default.
//
// Failure modes:
//
//	Empty Host or invalid Port
//	  → FAIL with configuration error detail (no remediation; operator must fix config)
//	Context already cancelled before dial
//	  → FAIL with context error detail
//	Dial returns error (connection refused, timeout, no route, etc.)
//	  → FAIL with remediation hint to verify the service is running
//	Dial succeeds
//	  → OK; connection is closed immediately
func (p *TCPDialProbe) Run(ctx context.Context) Result {
	if p.Host == "" {
		return Result{OK: false, Detail: "TCPDialProbe: Host must not be empty"}
	}
	if p.Port < 1 || p.Port > 65535 {
		return Result{
			OK:     false,
			Detail: fmt.Sprintf("TCPDialProbe: Port %d is not in range 1–65535", p.Port),
		}
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTCPTimeout
	}

	addr := net.JoinHostPort(p.Host, fmt.Sprintf("%d", p.Port))
	label := p.label(addr)

	// Honour context cancellation before attempting the dial.
	select {
	case <-ctx.Done():
		return Result{
			OK:     false,
			Detail: fmt.Sprintf("%s: context cancelled before dial: %v", label, ctx.Err()),
		}
	default:
	}

	// DialContext honours both the per-probe timeout and the caller's ctx (e.g. SIGINT
	// via signal.NotifyContext), so the leading ctx.Done() fast-path above is kept
	// only as a lightweight check before the syscall.
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Result{
			OK:     false,
			Detail: fmt.Sprintf("%s: %v", label, err),
			Remediation: fmt.Sprintf(
				"TCP port %s is not reachable.\n\n"+
					"Possible causes:\n"+
					"  • The target service is not running or has not started yet.\n"+
					"  • A firewall rule is blocking the connection.\n"+
					"  • The host %q is unreachable from the bastion.\n\n"+
					"Wait for the service to start and re-run the probe, or check\n"+
					"firewall and routing rules on the bastion host.",
				addr, p.Host,
			),
		}
	}

	_ = conn.Close() // best-effort; error is harmless after successful dial
	return Result{OK: true, Detail: fmt.Sprintf("%s: open", label)}
}

// label returns a human-readable identifier for the probe target.
func (p *TCPDialProbe) label(addr string) string {
	if p.Label != "" {
		return fmt.Sprintf("%s (%s)", p.Label, addr)
	}
	return addr
}

// DialTimeout is a function type matching net.DialTimeout. Exposed so tests
// can inject a fake dialer without having a real network listener.
type DialTimeout func(network, addr string, timeout time.Duration) (net.Conn, error)
