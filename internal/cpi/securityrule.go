package cpi

import (
	"net"
	"strings"
)

// Canonical security rule direction spellings.
const (
	DirectionIngress = "ingress"
	DirectionEgress  = "egress"
)

// NormalizeDirection maps every spelling of a rule direction to "ingress" or
// "egress". PVE reports "in" and "out"; our rule definitions use "ingress"
// and "egress"; "inbound" and "outbound" are accepted too, in any case.
// Unrecognized values come back trimmed and lower-cased so they still compare
// consistently.
func NormalizeDirection(direction string) string {
	switch d := strings.ToLower(strings.TrimSpace(direction)); d {
	case "in", "ingress", "inbound":
		return DirectionIngress
	case "out", "egress", "outbound":
		return DirectionEgress
	default:
		return d
	}
}

// NormalizeProtocol lower-cases a protocol and maps the empty value, "any",
// and "all" to "all".
func NormalizeProtocol(protocol string) string {
	switch p := strings.ToLower(strings.TrimSpace(protocol)); p {
	case "", "any", "all", "*":
		return "all"
	default:
		return p
	}
}

// NormalizeRemoteCIDR returns a canonical form of a remote address so that
// equivalent spellings compare equal. An empty value, "any", "0.0.0.0/0", and
// "::/0" all mean "anywhere" and become "". A bare IP becomes a host CIDR
// (/32 or /128), and a CIDR is rewritten to its masked network form.
func NormalizeRemoteCIDR(remote string) string {
	r := strings.ToLower(strings.TrimSpace(remote))

	switch r {
	case "", "any", "0.0.0.0/0", "::/0", "*":
		return ""
	}

	if ip := net.ParseIP(r); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String() + "/32"
		}

		return ip.String() + "/128"
	}

	if _, network, err := net.ParseCIDR(r); err == nil {
		if network.String() == "0.0.0.0/0" || network.String() == "::/0" {
			return ""
		}

		return network.String()
	}

	return r
}
