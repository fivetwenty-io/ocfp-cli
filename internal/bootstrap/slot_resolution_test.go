package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
)

// customSlotsStrategy is a BYO definition that moves the bastion and
// artifacts statics off their built-in offsets (3 and 11), so tests can
// prove slot resolution derives from the layout rather than echoing the
// hardcoded fallback constants.
const customSlotsStrategy = `name: custom-slots
description: test strategy with relocated bastion/artifacts statics
scheme_version: "9-custom-slots"
placement: colocated
min_prefix: 25

tiers:
  mgmt:
    statics:
      bastion: 7
      bosh: 4
      artifacts: 12
    available:
      - start: 32
        end: 63

  ocf:
    statics:
      bosh: 64
    available:
      - start: 96
`

// newCustomCatalogManager builds an AWS-provider Manager whose config
// carries a catalog holding the custom-slots BYO strategy, selected as the
// bloc's strategy.
func newCustomCatalogManager(t *testing.T) *bootstrap.Manager {
	t.Helper()

	dir := t.TempDir()

	path := filepath.Join(dir, "custom-slots.yaml")
	if err := os.WriteFile(path, []byte(customSlotsStrategy), 0o600); err != nil {
		t.Fatal(err)
	}

	catalog, err := netlayout.BuildCatalog([]string{path}, dir)
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}

	mgr, _, cfg, _ := newAWSTestManager(t, nil)
	cfg.NetworkCatalog = catalog
	cfg.Network.Strategy = "custom-slots"

	return mgr
}

// TestSlotForNamedIP_DerivesFromLayout proves the bastion/artifacts VM
// static offsets come from the bloc's resolved strategy, not the hardcoded
// fallback constants: a BYO strategy that relocates bastion to 7 and
// artifacts to 12 places both VMs at the relocated offsets.
func TestSlotForNamedIP_DerivesFromLayout(t *testing.T) {
	t.Parallel()

	mgr := newCustomCatalogManager(t)

	if got := mgr.SlotForNamedIP("prod-ocfp-0", "10.4.4.0/22", "bastion_ip", 3); got != 7 {
		t.Errorf("bastion_ip slot = %d, want 7 (custom-slots)", got)
	}

	if got := mgr.SlotForNamedIP("prod-ocfp-0", "10.4.4.0/22", "artifacts_ip", 11); got != 12 {
		t.Errorf("artifacts_ip slot = %d, want 12 (custom-slots)", got)
	}
}

// TestSlotForNamedIP_InfraSubnetIsFixed proves an infra-role subnet
// (PVE's dedicated "-infra" child) keeps the fixed infra layout regardless
// of the bloc's strategy — bastion stays at 3 even under custom-slots.
func TestSlotForNamedIP_InfraSubnetIsFixed(t *testing.T) {
	t.Parallel()

	mgr := newCustomCatalogManager(t)

	if got := mgr.SlotForNamedIP("prod-infra", "10.4.0.0/22", "bastion_ip", 3); got != 3 {
		t.Errorf("infra bastion_ip slot = %d, want 3 (fixed infra layout)", got)
	}
}

// TestSlotForNamedIP_FallsBack proves the fallback constant is returned
// when the key is absent from the resolved slots (spanning pins bastion to
// index 0, so a subnet with no parseable workload index misses it) and
// when the strategy itself cannot resolve.
func TestSlotForNamedIP_FallsBack(t *testing.T) {
	t.Parallel()

	mgr, _, cfg, _ := newAWSTestManager(t, nil)
	cfg.Network.Strategy = "spanning"

	if got := mgr.SlotForNamedIP("prod-net-subnet", "10.4.4.0/22", "bastion_ip", 3); got != 3 {
		t.Errorf("unparseable subnet bastion_ip slot = %d, want fallback 3", got)
	}

	cfg.Network.Strategy = "no-such-strategy"

	if got := mgr.SlotForNamedIP("prod-ocfp-0", "10.4.4.0/22", "bastion_ip", 3); got != 3 {
		t.Errorf("unknown strategy bastion_ip slot = %d, want fallback 3", got)
	}
}

// TestSlotForNamedIP_SpanningWorkloadIndex proves the workload index is
// parsed from the "<bloc>-ocfp-<n>" subnet name: spanning pins bastion to
// index 0, so ocfp-0 resolves it and ocfp-1 falls back.
func TestSlotForNamedIP_SpanningWorkloadIndex(t *testing.T) {
	t.Parallel()

	mgr, _, cfg, _ := newAWSTestManager(t, nil)
	cfg.Network.Strategy = "spanning"

	if got := mgr.SlotForNamedIP("prod-ocfp-0", "10.4.4.0/22", "bastion_ip", 99); got != 3 {
		t.Errorf("ocfp-0 bastion_ip slot = %d, want 3 (spanning pins bastion to 0)", got)
	}

	if got := mgr.SlotForNamedIP("prod-ocfp-1", "10.4.8.0/22", "bastion_ip", 99); got != 99 {
		t.Errorf("ocfp-1 bastion_ip slot = %d, want fallback 99 (bastion not placed on index 1)", got)
	}
}

// TestSlotForNamedIP_CustomNameResolvesFromState proves slot resolution
// reads a subnet's recorded role and workload index from state when the
// name doesn't follow the "-ocfp-<n>" shape: after CreateSubnets records
// operator-named subnets, spanning's pinned statics resolve on the subnet
// holding the pinned index instead of degrading to the fallback constant.
func TestSlotForNamedIP_CustomNameResolvesFromState(t *testing.T) {
	t.Parallel()

	mgr, _, cfg, _ := newAWSTestManager(t, []config.Subnet{
		{Name: "prod-east-a", CIDR: "10.4.4.0/22", Type: "public", AvailabilityZone: "us-east-1a"},
		{Name: "prod-east-b", CIDR: "10.4.8.0/22", Type: "public", AvailabilityZone: "us-east-1b"},
		{Name: "prod-east-c", CIDR: "10.4.12.0/22", Type: "public", AvailabilityZone: "us-east-1c"},
	})
	cfg.Network.Strategy = "spanning"

	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	if err := mgr.CreateSubnets(ctx); err != nil {
		t.Fatalf("CreateSubnets: %v", err)
	}

	// prod-east-a recorded at workload index 0: spanning pins bastion there.
	if got := mgr.SlotForNamedIP("prod-east-a", "10.4.4.0/22", "bastion_ip", 99); got != 3 {
		t.Errorf("prod-east-a bastion_ip slot = %d, want 3 (index 0 from state)", got)
	}

	// prod-east-b recorded at index 1: doomsday is pinned there, bastion is not.
	if got := mgr.SlotForNamedIP("prod-east-b", "10.4.8.0/22", "doomsday_ip", 99); got != 18 {
		t.Errorf("prod-east-b doomsday_ip slot = %d, want 18 (index 1 from state)", got)
	}

	if got := mgr.SlotForNamedIP("prod-east-b", "10.4.8.0/22", "bastion_ip", 99); got != 99 {
		t.Errorf("prod-east-b bastion_ip slot = %d, want fallback 99 (bastion not placed on index 1)", got)
	}
}
