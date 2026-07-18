package bootstrap_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// fakeExistingNet wraps fakeNet but reports an already-existing network on
// ListNetworks, exercising the importExistingNetwork path (as opposed to the
// createNewNetwork path the other network tests in this package exercise).
// The returned network's CIDR intentionally differs from the bloc's
// configured network CIDR — this simulates PVE's bridge-mode ListNetworks,
// which reports the physical bridge device's host-level address (e.g. a
// cluster node's own mgmt IP/24) rather than a real "network" range.
type fakeExistingNet struct {
	fakeNet

	existingName string
	existingCIDR string
}

func (f *fakeExistingNet) ListNetworks(_ctx context.Context, _filters map[string]string) ([]*cpi.Network, error) {
	return []*cpi.Network{
		{
			ID:         "vmbr0",
			Name:       f.existingName,
			CIDR:       f.existingCIDR,
			State:      cpi.ResourceStateActive,
			Tags:       map[string]string{},
			DNSServers: nil,
			CreatedAt:  time.Time{},
			UpdatedAt:  time.Time{},
		},
	}, nil
}

// setupImportCIDRTest builds a Manager whose provider reports an existing
// network (named to match resolveNetworkName()) with discoveredCIDR, and
// whose bloc config requests configuredCIDR. provider selects which CPI
// provider name the Manager runs under (e.g. "pve" vs "stackit"), since the
// fix under test is PVE-gated.
func setupImportCIDRTest(t *testing.T, provider, blocName, networkName, discoveredCIDR, configuredCIDR string) (*bootstrap.Manager, *state.Manager) {
	t.Helper()

	tmp := t.TempDir()

	sm, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err = sm.Load(blocName); err != nil {
		t.Fatal(err)
	}

	cfg := createTestConfig()
	cfg.Network.Name = networkName
	cfg.Network.CIDR = configuredCIDR

	fakeNetwork := &fakeExistingNet{existingName: networkName, existingCIDR: discoveredCIDR}
	fakeProvider := &fakeProv{n: fakeNetwork, c: &fakeCompute{}}

	mgr := bootstrap.NewManager(cfg, fakeProvider, sm, &bootstrap.Options{
		BlocName: blocName,
		Provider: provider,
		Region:   "eu01",
		Yes:      true,
	})

	return mgr, sm
}

// TestImportExistingNetwork_PVE_PrefersConfiguredCIDR verifies the fix: for
// PVE, when an existing same-named bridge is discovered with a host-level
// address, the bloc's configured network_cidr wins for both the state
// resource and the network_cidr output feeding subnet carve / bastion
// placement.
func TestImportExistingNetwork_PVE_PrefersConfiguredCIDR(t *testing.T) {
	t.Parallel()

	const (
		discoveredCIDR = "10.254.0.10/24" // node's own mgmt IP, as PVE reports it
		configuredCIDR = "10.254.16.0/20" // the bloc's actual workload CIDR
	)

	mgr, sm := setupImportCIDRTest(t, "pve", "ocfp-lab-pve-cpi", "vmbr0", discoveredCIDR, configuredCIDR)

	if err := mgr.CreateNetwork(context.Background()); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	got, err := sm.GetOutput("network_cidr")
	if err != nil {
		t.Fatalf("missing network_cidr output: %v", err)
	}

	if got != configuredCIDR {
		t.Errorf("network_cidr output = %q, want configured CIDR %q (discovered was %q)", got, configuredCIDR, discoveredCIDR)
	}

	res, err := sm.GetResource("network", "vmbr0")
	if err != nil {
		t.Fatalf("missing network resource: %v", err)
	}

	if cidr, _ := res.Properties["cidr"].(string); cidr != configuredCIDR {
		t.Errorf("network resource Properties[cidr] = %q, want %q", cidr, configuredCIDR)
	}
}

// TestImportExistingNetwork_PVE_FallsBackWhenUnconfigured verifies that PVE
// blocs which never set network_cidr keep the discovered CIDR (there is
// nothing more authoritative to prefer) rather than silently picking up the
// bootstrap package's generic default CIDR constant.
func TestImportExistingNetwork_PVE_FallsBackWhenUnconfigured(t *testing.T) {
	t.Parallel()

	const discoveredCIDR = "10.254.0.10/24"

	mgr, sm := setupImportCIDRTest(t, "pve", "ocfp-lab-pve-cpi", "vmbr0", discoveredCIDR, "")

	if err := mgr.CreateNetwork(context.Background()); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	got, err := sm.GetOutput("network_cidr")
	if err != nil {
		t.Fatalf("missing network_cidr output: %v", err)
	}

	if got != discoveredCIDR {
		t.Errorf("network_cidr output = %q, want discovered CIDR %q (no configured CIDR to prefer)", got, discoveredCIDR)
	}
}

// TestImportExistingNetwork_NonPVE_Unchanged verifies non-PVE providers
// (e.g. STACKIT, and by extension AWS's importConfiguredNetwork/vpc_id path,
// which shares this same helper) keep importing the discovered network's own
// CIDR exactly as before the fix — the PVE gate must not change their
// behavior.
func TestImportExistingNetwork_NonPVE_Unchanged(t *testing.T) {
	t.Parallel()

	const (
		discoveredCIDR = "10.4.0.0/20"
		configuredCIDR = "10.9.0.0/20" // deliberately different; must be ignored
	)

	mgr, sm := setupImportCIDRTest(t, "stackit", "prod", "prod-net", discoveredCIDR, configuredCIDR)

	if err := mgr.CreateNetwork(context.Background()); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	got, err := sm.GetOutput("network_cidr")
	if err != nil {
		t.Fatalf("missing network_cidr output: %v", err)
	}

	if got != discoveredCIDR {
		t.Errorf("network_cidr output = %q, want discovered CIDR %q (non-PVE must ignore configured CIDR on import)", got, discoveredCIDR)
	}
}
