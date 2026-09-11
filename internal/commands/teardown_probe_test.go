package commands_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// pveNodeVMs maps a PVE node name to the guests the fake API reports for it.
type pveNodeVMs map[string][]map[string]interface{}

// newFakePVE serves /nodes/{node}/qemu for each node in vms, and an empty list
// for any node it does not know about.
func newFakePVE(t *testing.T, vms pveNodeVMs) *httptest.Server {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node := ""

		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		for i, part := range parts {
			if part == "nodes" && i+1 < len(parts) {
				node = parts[i+1]
			}
		}

		guests := vms[node]
		if guests == nil {
			guests = []map[string]interface{}{}
		}

		w.Header().Set("Content-Type", "application/json")

		if err := json.NewEncoder(w).Encode(map[string]interface{}{"data": guests}); err != nil {
			t.Errorf("encode fake pve response: %v", err)
		}
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return server
}

// blocConfig builds a PVE bloc config pointing at the fake API.
func blocConfig(server *httptest.Server, blocName string, nodes ...string) *config.Config {
	cfg := &config.Config{
		Name:        blocName,
		Provider:    "pve",
		APIEndpoint: server.URL,
		AuthToken:   "root@pam!testtoken",
		TokenSecret: "00000000-0000-0000-0000-000000000001",
		VerifySSL:   false,
	}

	if len(nodes) > 0 {
		cfg.Region = nodes[0]
		cfg.Nodes = nodes
	}

	return cfg
}

// The regression this whole change exists for. Bootstrap names a bloc's guests
// "<bloc>-bastion" and "<bloc>-artifacts"; nothing is named plain "<bloc>". The
// old probe looked for a guest named exactly "<bloc>", found none, and reported
// every live bloc as already torn down, so teardown exited 0 having done
// nothing at all — dry runs included.
func TestPVEProbe_BlocSuffixedVMsCountAsPresent(t *testing.T) {
	t.Parallel()

	server := newFakePVE(t, pveNodeVMs{
		"pvenode1": {
			{"vmid": 20000, "name": "ocfp-cf1-lab-bastion", "status": "running"},
			{"vmid": 20001, "name": "ocfp-cf1-lab-artifacts", "status": "running"},
		},
	})

	cfg := blocConfig(server, "ocfp-cf1-lab", "pvenode1")
	log := initTestLogger(t)

	done, err := commands.TestPVECheckAlreadyTornDown(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if done {
		t.Fatal("expected the probe to see the bloc's bastion and artifacts VMs and let teardown proceed")
	}
}

// A bloc whose VMs are all gone is genuinely torn down, even while other blocs
// still have guests on the same node.
func TestPVEProbe_ForeignVMsDoNotBlockTeardown(t *testing.T) {
	t.Parallel()

	server := newFakePVE(t, pveNodeVMs{
		"pvenode1": {
			{"vmid": 30000, "name": "ocfp-mgmt-bastion", "status": "running"},
			{"vmid": 30001, "name": "some-other-vm", "status": "running"},
		},
	})

	cfg := blocConfig(server, "ocfp-cf1-lab", "pvenode1")
	log := initTestLogger(t)

	done, err := commands.TestPVECheckAlreadyTornDown(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !done {
		t.Fatal("expected the probe to report the bloc torn down when only other blocs' VMs remain")
	}
}

// The hyphen in the prefix match is load-bearing: "ocfp-cf1-lab" must not claim
// the guests of "ocfp-cf1-lab2".
func TestPVEProbe_PrefixDoesNotSpanBlocNames(t *testing.T) {
	t.Parallel()

	server := newFakePVE(t, pveNodeVMs{
		"pvenode1": {
			{"vmid": 40000, "name": "ocfp-cf1-lab2-bastion", "status": "running"},
		},
	})

	cfg := blocConfig(server, "ocfp-cf1-lab", "pvenode1")
	log := initTestLogger(t)

	done, err := commands.TestPVECheckAlreadyTornDown(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !done {
		t.Fatal("expected 'ocfp-cf1-lab2-bastion' to belong to a different bloc")
	}
}

// A guest named exactly after the bloc still counts, which is what the original
// probe checked for.
func TestPVEProbe_BareBlocNameStillCounts(t *testing.T) {
	t.Parallel()

	server := newFakePVE(t, pveNodeVMs{
		"pvenode1": {
			{"vmid": 901, "name": "ocfp-mgmt", "status": "running"},
		},
	})

	cfg := blocConfig(server, "ocfp-mgmt", "pvenode1")
	log := initTestLogger(t)

	done, err := commands.TestPVECheckAlreadyTornDown(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if done {
		t.Fatal("expected a guest named exactly after the bloc to block teardown")
	}
}

// The probe has to look at every configured node. Checking only the first one
// would call a bloc torn down while its VMs run elsewhere in the cluster.
func TestPVEProbe_FindsVMsOnLaterNodes(t *testing.T) {
	t.Parallel()

	server := newFakePVE(t, pveNodeVMs{
		"pvenode1": {},
		"pvenode3": {
			{"vmid": 20000, "name": "ocfp-cf1-lab-bastion", "status": "running"},
		},
	})

	cfg := blocConfig(server, "ocfp-cf1-lab", "pvenode1", "pvenode2", "pvenode3")
	log := initTestLogger(t)

	done, err := commands.TestPVECheckAlreadyTornDown(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if done {
		t.Fatal("expected the probe to find the bloc's VM on the third node")
	}
}

// An unnamed bloc is a config error, not an invitation to delete nothing.
func TestPVEProbe_RequiresBlocName(t *testing.T) {
	t.Parallel()

	server := newFakePVE(t, pveNodeVMs{"pvenode1": {}})

	cfg := blocConfig(server, "", "pvenode1")
	log := initTestLogger(t)

	_, err := commands.TestPVECheckAlreadyTornDown(context.Background(), cfg, log)
	if err == nil {
		t.Fatal("expected an error when the bloc name is missing")
	}
}

// cloudflareEnabledConfig builds a bloc config whose Cloudflare teardown step
// will reach vault for the tunnel id.
func cloudflareEnabledConfig(vaultAddr string) *config.Config {
	enabled := true

	return &config.Config{
		Name:     "ocfp-cf1-lab",
		Provider: "pve",
		Cloudflare: &config.CloudflareConfig{
			Enabled:      &enabled,
			APIToken:     "cf-test-token",
			Zone:         "example.com",
			AppsDomain:   "apps.example.com",
			SystemDomain: "system.example.com",
		},
	}
}

// runTeardownAgainstFakeVault runs Execute with the given dry-run setting and
// reports how many requests reached vault.
func runTeardownAgainstFakeVault(t *testing.T, dryRun bool) int32 {
	t.Helper()

	var hits int32

	vaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"data":{}}}`))
	}))
	t.Cleanup(vaultServer.Close)

	t.Setenv("VAULT_ADDR", vaultServer.URL)
	t.Setenv("VAULT_TOKEN", "test-token")

	stateManager, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}

	if _, err = stateManager.Load("ocfp-cf1-lab"); err != nil {
		t.Fatalf("load state: %v", err)
	}

	manager := commands.NewTeardownManager(
		cloudflareEnabledConfig(vaultServer.URL),
		&fakeProviderCreds{},
		stateManager,
		&commands.TeardownOptions{
			BlocName: "ocfp-cf1-lab",
			Provider: "pve",
			DryRun:   dryRun,
		},
	)

	if err = manager.Execute(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}

	return atomic.LoadInt32(&hits)
}

// A dry run must not delete the Cloudflare CNAMEs, delete the tunnel, or blank
// the tunnel keys in vault. Those steps used to run ahead of the dry-run check,
// so `teardown --dry-run` tore down DNS for real.
func TestTeardown_DryRunLeavesCloudflareAlone(t *testing.T) {
	_ = initTestLogger(t)

	if hits := runTeardownAgainstFakeVault(t, true); hits != 0 {
		t.Fatalf("expected a dry run to leave vault and Cloudflare untouched, got %d vault request(s)", hits)
	}
}

// The companion assertion: without --dry-run the Cloudflare step really does
// run, which is what makes the test above meaningful.
func TestTeardown_RealRunReachesCloudflareStep(t *testing.T) {
	_ = initTestLogger(t)

	if hits := runTeardownAgainstFakeVault(t, false); hits == 0 {
		t.Fatal("expected a real teardown to reach the Cloudflare step and read the tunnel id from vault")
	}
}
