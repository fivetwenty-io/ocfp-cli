package pve

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

func TestParsePVERuleUsesCanonicalSpellings(t *testing.T) {
	t.Parallel()

	in := parsePVERule(map[string]interface{}{
		"type": "in", "proto": "tcp", "dport": "22", "source": "10.4.0.0/20", "comment": "SSH", "pos": float64(3),
	}, 0)
	if in.Direction != "ingress" || in.Protocol != "tcp" || in.PortRangeMin != 22 || in.PortRangeMax != 22 || in.ID != "3" {
		t.Errorf("unexpected ingress parse: %+v", in)
	}

	out := parsePVERule(map[string]interface{}{"type": "out", "source": "0.0.0.0/0"}, 1)
	if out.Direction != "egress" || out.Protocol != "all" || out.RemoteIPCIDR != "0.0.0.0/0" || out.ID != "1" {
		t.Errorf("unexpected egress parse: %+v", out)
	}

	rng := parsePVERule(map[string]interface{}{"type": "in", "proto": "TCP", "dport": "1024:2048"}, 2)
	if rng.Protocol != "tcp" || rng.PortRangeMin != 1024 || rng.PortRangeMax != 2048 {
		t.Errorf("unexpected range parse: %+v", rng)
	}
}

func TestParsePVERuleMatchesDesiredRuleShape(t *testing.T) {
	t.Parallel()

	got := parsePVERule(map[string]interface{}{"type": "out"}, 0)
	if cpi.NormalizeDirection(got.Direction) != cpi.DirectionEgress {
		t.Errorf("direction = %q", got.Direction)
	}
}

func TestParsePVERuleKeepsUnmodeledFields(t *testing.T) {
	t.Parallel()

	got := parsePVERule(map[string]interface{}{
		"type": "in", "action": "DROP", "enable": float64(1), "dport": "80,443", "dest": "10.0.0.5", "pos": float64(0),
	}, 0)

	want := map[string]string{"action": "DROP", "enable": "1", "dport": "80,443", "dest": "10.0.0.5"}
	for k, v := range want {
		if got.Attributes[k] != v {
			t.Errorf("attribute %s = %q, want %q (all: %v)", k, got.Attributes[k], v, got.Attributes)
		}
	}

	plain := parsePVERule(map[string]interface{}{"type": "in", "dport": "1024:2048", "action": "ACCEPT"}, 0)
	if _, ok := plain.Attributes["dport"]; ok {
		t.Errorf("a plain port range should not be kept as an attribute: %v", plain.Attributes)
	}
}

func TestParsePVERuleKeepsAnywhereSourcesApart(t *testing.T) {
	t.Parallel()

	for source, want := range map[string]string{"": "", "0.0.0.0/0": "0.0.0.0/0", "::/0": "::/0"} {
		data := map[string]interface{}{"type": "in"}
		if source != "" {
			data["source"] = source
		}

		if got := parsePVERule(data, 0).RemoteIPCIDR; got != want {
			t.Errorf("source %q read back as %q, want %q", source, got, want)
		}
	}
}
