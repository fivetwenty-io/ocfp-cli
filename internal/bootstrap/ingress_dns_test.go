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
	    "k1": {"HostName": "ocfp-lab-wayne-bastion", "TailscaleIPs": ["100.111.160.81", "fd7a::2"], "Online": true},
	    "k2": {"HostName": "other", "TailscaleIPs": ["100.64.0.9"], "Online": true}
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

// TestTailnetIPForHost_SkipsOfflinePeer is the deterministic red test: a
// single peer matches the hostname but is offline (a stale device left
// behind by a destroyed VM). The old implementation ignored Online and
// returned its IP; the fix must return "" so the poller in
// discoverBastionTailnetIP keeps waiting for a live device.
func TestTailnetIPForHost_SkipsOfflinePeer(t *testing.T) {
	t.Parallel()
	status := []byte(`{
	  "Self": {"HostName": "workstation", "TailscaleIPs": ["100.64.0.1"]},
	  "Peer": {
	    "stale": {"HostName": "ocfp-lab-wayne-bastion", "TailscaleIPs": ["100.87.207.41"], "Online": false}
	  }
	}`)

	if got := tailnetIPForHost(status, "ocfp-lab-wayne-bastion"); got != "" {
		t.Errorf("offline-only peer lookup = %q, want empty", got)
	}
}

// TestTailnetIPForHost_PrefersOnlinePeer covers two peers sharing a
// HostName, one offline (stale) and one online (live). Only the online
// peer's IP must be returned regardless of map iteration order.
func TestTailnetIPForHost_PrefersOnlinePeer(t *testing.T) {
	t.Parallel()
	status := []byte(`{
	  "Self": {"HostName": "workstation", "TailscaleIPs": ["100.64.0.1"]},
	  "Peer": {
	    "stale": {"HostName": "bastion-x", "TailscaleIPs": ["100.1.1.1"], "Online": false},
	    "live": {"HostName": "bastion-x", "TailscaleIPs": ["100.2.2.2"], "Online": true}
	  }
	}`)

	if got := tailnetIPForHost(status, "bastion-x"); got != "100.2.2.2" {
		t.Errorf("mixed online/offline lookup = %q, want 100.2.2.2", got)
	}
}

func TestConfigureIngressDNS_SkipsWhenProviderNotTailscale(t *testing.T) {
	t.Parallel()
	m := &Manager{config: &config.Config{}, options: &Options{BlocName: "b"}}
	if err := m.ConfigureIngressDNS(context.Background()); err != nil {
		t.Fatalf("expected soft skip, got %v", err)
	}
}
