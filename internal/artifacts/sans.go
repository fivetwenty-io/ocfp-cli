package artifacts

import "net"

// ArtifactsLeafSANIPs builds the canonical IP SAN set for an artifacts VM
// leaf certificate (self-signed or internal-ca): the VM's private IP (when
// resolvable) plus the loopback addresses 127.0.0.1 and ::1. Without the
// loopback entries, clients running ON the artifacts VM itself (the
// provisioning script, `curl https://127.0.0.1:9000` health checks, RustFS's
// own bucket-creation step) can never pass certificate verification against
// anything but the VM's SDN IP, forcing SkipTLSVerify on-box even when the
// operator wants full verification everywhere else.
//
// vmIP may be nil (e.g. the caller could not parse a recorded IP); the
// loopback addresses are still returned so on-VM verification keeps working.
func ArtifactsLeafSANIPs(vmIP net.IP) []net.IP {
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}

	if vmIP != nil {
		ips = append(ips, vmIP)
	}

	return ips
}
