package bootstrap

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func TestIngressRecordNames(t *testing.T) {
	t.Parallel()
	got := ingressRecordNames("wayneeseguin.lab.fivetwenty.io")
	want := []string{"wayneeseguin.lab.fivetwenty.io", "*.wayneeseguin.lab.fivetwenty.io"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTailnetIPForHost_MatchesSelfAndPeers(t *testing.T) {
	t.Parallel()
	status := []byte(`{
	  "Self": {"HostName": "workstation", "TailscaleIPs": ["100.64.0.1", "fd7a::1"]},
	  "Peer": {
	    "k1": {"HostName": "ocfp-lab-wayne-bastion", "TailscaleIPs": ["100.111.160.81", "fd7a::2"]},
	    "k2": {"HostName": "other", "TailscaleIPs": ["100.64.0.9"]}
	  }
	}`)

	if got := tailnetIPForHost(status, "ocfp-lab-wayne-bastion"); got != "100.111.160.81" {
		t.Errorf("peer lookup = %q", got)
	}
	if got := tailnetIPForHost(status, "workstation"); got != "100.64.0.1" {
		t.Errorf("self lookup = %q", got)
	}
	if got := tailnetIPForHost(status, "absent"); got != "" {
		t.Errorf("absent lookup = %q", got)
	}
}

func TestConfigureIngressDNS_SkipsWhenProviderNotTailscale(t *testing.T) {
	t.Parallel()
	m := &Manager{config: &config.Config{}, options: &Options{BlocName: "b"}}
	if err := m.ConfigureIngressDNS(context.Background()); err != nil {
		t.Fatalf("expected soft skip, got %v", err)
	}
}
