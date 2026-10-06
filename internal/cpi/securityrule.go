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
// equivalent spellings compare equal. An empty value, "any", and "0.0.0.0/0"
// all mean "anywhere" and become "". IPv6-any ("::/0") stays "::/0": PVE reads
// 0.0.0.0/0 as IPv4 only and ::/0 as IPv6 only, so the two are not the same
// rule. A bare IP becomes a host CIDR (/32 or /128), and a CIDR is rewritten
// to its masked network form.
func NormalizeRemoteCIDR(remote string) string {
	if exact := NormalizeRemoteCIDRExact(remote); exact != "0.0.0.0/0" {
		return exact
	}

	return ""
}

// NormalizeRemoteCIDRExact is NormalizeRemoteCIDR without the folding of
// 0.0.0.0/0 into the empty value. An empty source (or "any") applies to both
// address families, 0.0.0.0/0 to IPv4 only, and ::/0 to IPv6 only, so all
// three stay distinct. Use it to tell whether two rules are exact twins.
func NormalizeRemoteCIDRExact(remote string) string {
	r := strings.ToLower(strings.TrimSpace(remote))

	switch r {
	case "", "any", "*":
		return ""
	}

	if ip := net.ParseIP(r); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String() + "/32"
		}

		return ip.String() + "/128"
	}

	if _, network, err := net.ParseCIDR(r); err == nil {
		return network.String()
	}

	return r
}
