// Package netvalidate detects mismatch between a BOSH director network
// CIDR and a CF cloud-config CIDR.
//
// Background: when both overrides are applied to a PVE-hosted BOSH director
// the two CIDRs must match. A mismatch re-triggers the Tailscale LAN route
// hazard: the PVE Tailscale peer advertises the 192.168.1.x subnet route,
// causing traffic destined for 192.168.1.x to be hijacked, resulting in
// "no route to host" for userland clients. See cf-cloud-config-override.yml
// header for the full explanation.
package netvalidate

import (
	"errors"
	"fmt"
	"net"
)

// tailscaleLANRouteHazardMsg is the operator-readable explanation drawn
// verbatim from the cf-cloud-config-override.yml header comment.
const tailscaleLANRouteHazardMsg = "Mismatch between bosh network and cf cloud-config re-triggers the " +
	"tailscale LAN route hazard: traffic destined for 192.168.1.x is hijacked by the pve " +
	"Tailscale peer's advertised subnet route, causing \"no route to host\" for userland clients. " +
	"Apply both network-override.yml and cf-cloud-config-override.yml together so the CIDRs match."

// ErrCIDRMismatch is returned when the director CIDR and cf cloud-config CIDR
// do not match after normalization.
var ErrCIDRMismatch = errors.New("director network CIDR and cf cloud-config CIDR do not match")

// ValidateNetworkPairing checks that directorCIDR and cfCloudConfigCIDR refer
// to the same network.
//
// Both inputs are parsed and normalized via net.ParseCIDR before comparison so
// that equivalent representations (e.g., "10.64.64.5/18" and "10.64.64.0/18")
// are recognized as the same network. A parse failure for either input returns
// a wrapped error immediately; no mismatch check is performed.
//
// Returns nil when the normalized network addresses are equal.
// Returns a wrapped ErrCIDRMismatch with the Tailscale LAN route hazard
// explanation when they differ.
func ValidateNetworkPairing(directorCIDR, cfCloudConfigCIDR string) error {
	if directorCIDR == "" || cfCloudConfigCIDR == "" {
		return fmt.Errorf("ValidateNetworkPairing: both CIDRs must be non-empty; got %q and %q", directorCIDR, cfCloudConfigCIDR)
	}

	dirNet, err := parseCIDR(directorCIDR)
	if err != nil {
		return fmt.Errorf("ValidateNetworkPairing: invalid director CIDR %q: %w", directorCIDR, err)
	}

	cfNet, err := parseCIDR(cfCloudConfigCIDR)
	if err != nil {
		return fmt.Errorf("ValidateNetworkPairing: invalid cf cloud-config CIDR %q: %w", cfCloudConfigCIDR, err)
	}

	if dirNet != cfNet {
		return fmt.Errorf(
			"%w: director=%q cf-cloud-config=%q — %s",
			ErrCIDRMismatch,
			directorCIDR,
			cfCloudConfigCIDR,
			tailscaleLANRouteHazardMsg,
		)
	}

	return nil
}

// parseCIDR wraps net.ParseCIDR and returns the normalized network address
// string ("ip/prefix") so that host-bits-set representations compare equal
// to their canonical form (e.g., "10.64.64.5/18" → "10.64.64.0/18").
func parseCIDR(cidr string) (string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}

	return ipNet.String(), nil
}
