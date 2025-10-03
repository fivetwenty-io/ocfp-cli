package commands_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

const testBloc = "prod"

func TestResolveReservedIP(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".state")
	t.Setenv("OCFP_STATE_DIR", stateDir)
	// Prepare state
	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	bloc := testBloc

	_, err = stateManager.Load(bloc)
	if err != nil {
		t.Fatal(err)
	}
	// Inject output
	_ = stateManager.SetOutput("reserved_prod-ocfp-0_vault_ip", "10.4.0.5")
	if err := stateManager.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	reservedIP, err := commands.ResolveReservedIP(bloc, "reserved:vault_ip")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if reservedIP != "10.4.0.5" {
		t.Fatalf("got %s want 10.4.0.5", reservedIP)
	}
}

func TestResolvePublicIPToken(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".state")
	t.Setenv("OCFP_STATE_DIR", stateDir)

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	bloc := testBloc

	_, err = stateManager.Load(bloc)
	if err != nil {
		t.Fatal(err)
	}
	// Inject a public_ip resource
	_ = stateManager.AddResource(&state.Resource{
		ID:       "pip-1",
		Type:     "public_ip",
		Name:     "prod-jumpbox-0",
		Provider: "",
		State:    "active",
		Properties: map[string]interface{}{
			"job":     "router",
			"index":   "0",
			"address": "198.51.100.10",
		},
		Tags:      map[string]string{"job": "router", "index": "0"},
		CreatedAt: time.Time{},
		UpdatedAt: time.Time{},
	})

	if err := stateManager.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	targetIP, err := commands.ResolveTargetIP(bloc, "public-ip:router:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if targetIP != "198.51.100.10" {
		t.Fatalf("got %s want 198.51.100.10", targetIP)
	}
}

func TestResolveReservedIPWithIndex(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".state")
	t.Setenv("OCFP_STATE_DIR", stateDir)

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	bloc := testBloc

	_, err = stateManager.Load(bloc)
	if err != nil {
		t.Fatal(err)
	}

	// With index
	_ = stateManager.SetOutput("reserved_prod-ocfp-1_doomsday_ip", "10.4.4.9")
	if err := stateManager.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	reservedIP, err := commands.ResolveReservedIP(bloc, "reserved:doomsday_ip:1")
	if err != nil {
		t.Fatalf("resolve idx: %v", err)
	}

	if reservedIP != "10.4.4.9" {
		t.Fatalf("got %s want 10.4.4.9", reservedIP)
	}
}
