package cpi_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

func TestNormalizeDirection(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"in": "ingress", "IN": "ingress", "ingress": "ingress", "Ingress": "ingress", " inbound ": "ingress",
		"out": "egress", "OUT": "egress", "egress": "egress", "Outbound": "egress",
		"": "", "weird": "weird",
	}
	for in, want := range cases {
		if got := cpi.NormalizeDirection(in); got != want {
			t.Errorf("NormalizeDirection(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeProtocol(t *testing.T) {
	t.Parallel()

	cases := map[string]string{"": "all", "all": "all", "ANY": "all", "TCP": "tcp", "Udp": "udp", "icmp": "icmp"}
	for in, want := range cases {
		if got := cpi.NormalizeProtocol(in); got != want {
			t.Errorf("NormalizeProtocol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeRemoteCIDR(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"": "", "any": "", "ANY": "", "0.0.0.0/0": "", "::/0": "",
		"10.0.0.5":       "10.0.0.5/32",
		"10.0.0.5/32":    "10.0.0.5/32",
		"10.4.1.7/20":    "10.4.0.0/20",
		"10.4.0.0/20":    "10.4.0.0/20",
		"2001:db8::1":    "2001:db8::1/128",
		" 10.0.0.5 ":     "10.0.0.5/32",
		"not-an-ip":      "not-an-ip",
		"+dc/some-ipset": "+dc/some-ipset",
	}
	for in, want := range cases {
		if got := cpi.NormalizeRemoteCIDR(in); got != want {
			t.Errorf("NormalizeRemoteCIDR(%q) = %q, want %q", in, got, want)
		}
	}
}
