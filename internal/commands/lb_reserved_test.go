package commands

import (
    "path/filepath"
    "testing"

	"github.com/ocfp/ocfp-cli-go/internal/state"
)

const testBloc = "prod"

func TestResolveReservedIP(t *testing.T) {
	t.Parallel()
	// Prepare state
	stateManager, err := state.NewManager(filepath.Join(t.TempDir(), ".state"))
	if err != nil {
		t.Fatal(err)
	}

    bloc := testBloc
	if _, err := stateManager.Load(bloc); err != nil {
		t.Fatal(err)
	}
	// Inject output
	_ = stateManager.SetOutput("reserved_prod-ocfp-0_vault_ip", "10.4.0.5")

	ip, err := resolveReservedIP(bloc, "reserved:vault_ip")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if ip != "10.4.0.5" {
		t.Fatalf("got %s want 10.4.0.5", ip)
	}
}

func TestResolvePublicIPToken(t *testing.T) {
	t.Parallel()

	stateManager, err := state.NewManager(filepath.Join(t.TempDir(), ".state"))
	if err != nil {
		t.Fatal(err)
	}

    bloc := testBloc
	if _, err := stateManager.Load(bloc); err != nil {
		t.Fatal(err)
	}
	// Inject a public_ip resource
	_ = stateManager.AddResource(&state.Resource{
		ID:    "pip-1",
		Type:  "public_ip",
		Name:  "prod-jumpbox-0",
		State: "active",
		Properties: map[string]interface{}{
			"job":     "router",
			"index":   "0",
			"address": "198.51.100.10",
		},
		Tags: map[string]string{"job": "router", "index": "0"},
	})

	ip, err := resolveTargetIP(bloc, "public-ip:router:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if ip != "198.51.100.10" {
		t.Fatalf("got %s want 198.51.100.10", ip)
	}
}

func TestResolveReservedIPWithIndex(t *testing.T) {
	t.Parallel()

	stateManager, err := state.NewManager(filepath.Join(t.TempDir(), ".state"))
	if err != nil {
		t.Fatal(err)
	}

    bloc := testBloc
	if _, err := stateManager.Load(bloc); err != nil {
		t.Fatal(err)
	}

	// With index
	_ = stateManager.SetOutput("reserved_prod-ocfp-1_doomsday_ip", "10.4.4.9")

	ip, err := resolveReservedIP(bloc, "reserved:doomsday_ip:1")
	if err != nil {
		t.Fatalf("resolve idx: %v", err)
	}

	if ip != "10.4.4.9" {
		t.Fatalf("got %s want 10.4.4.9", ip)
	}
}
