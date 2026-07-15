package artifacts

import (
	"net"
	"testing"
)

// TestArtifactsLeafSANIPs_IncludesLoopbackAndVMIP asserts the canonical IP SAN
// set always carries both loopback addresses plus the supplied VM IP, so
// on-VM clients (the provisioning script, local health probes) verify
// alongside operator-side clients hitting the SDN address.
func TestArtifactsLeafSANIPs_IncludesLoopbackAndVMIP(t *testing.T) {
	t.Parallel()

	vmIP := net.ParseIP("10.108.16.11")

	got := ArtifactsLeafSANIPs(vmIP)

	wantIPv4Loopback := net.IPv4(127, 0, 0, 1)

	if len(got) != 3 {
		t.Fatalf("ArtifactsLeafSANIPs returned %d entries, want 3 (loopback v4, loopback v6, VM IP): %v", len(got), got)
	}

	if !got[0].Equal(wantIPv4Loopback) {
		t.Errorf("got[0] = %v, want 127.0.0.1", got[0])
	}

	if !got[1].Equal(net.IPv6loopback) {
		t.Errorf("got[1] = %v, want ::1", got[1])
	}

	if !got[2].Equal(vmIP) {
		t.Errorf("got[2] = %v, want %v", got[2], vmIP)
	}
}

// TestArtifactsLeafSANIPs_NilVMIP asserts a nil VM IP (unresolvable/unknown)
// still yields the loopback pair rather than an empty or panicking result —
// on-VM verification must keep working even when the caller has no IP.
func TestArtifactsLeafSANIPs_NilVMIP(t *testing.T) {
	t.Parallel()

	got := ArtifactsLeafSANIPs(nil)

	if len(got) != 2 {
		t.Fatalf("ArtifactsLeafSANIPs(nil) returned %d entries, want 2 (loopback only): %v", len(got), got)
	}

	if !got[0].Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("got[0] = %v, want 127.0.0.1", got[0])
	}

	if !got[1].Equal(net.IPv6loopback) {
		t.Errorf("got[1] = %v, want ::1", got[1])
	}
}
