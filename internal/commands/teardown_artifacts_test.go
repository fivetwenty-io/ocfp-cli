package commands_test

import (
	"context"
	"sort"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// newArtifactsFixture records the two guests of a bloc the way bootstrap does.
// The bastion goes in under its VMID, the artifacts VM under its name, which is
// the mismatch teardown has to resolve before it can delete anything.
func newArtifactsFixture(t *testing.T) (*commands.TeardownManager, *nukeProvider) {
	t.Helper()

	provider := &nukeProvider{
		compute: &nukeCompute{
			instances: []*cpi.Instance{
				{ID: "20000", Name: "ocfp-cf1-lab-bastion", AvailabilityZone: "pvenode1", State: cpi.ResourceStateActive},
				{ID: "20001", Name: "ocfp-cf1-lab-artifacts", AvailabilityZone: "pvenode1", State: cpi.ResourceStateActive},
			},
			configs: map[string][]string{},
		},
		storage: &nukeStorage{byNodeStorage: map[string][]*cpi.Volume{}},
	}

	stateManager, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}

	if _, err = stateManager.Load("ocfp-cf1-lab"); err != nil {
		t.Fatalf("load state: %v", err)
	}

	err = stateManager.AddResource(&state.Resource{
		ID:       "20000",
		Type:     "compute_instance",
		Name:     "ocfp-cf1-lab-bastion",
		Provider: "pve",
		State:    "active",
		Tags:     map[string]string{"bloc": "ocfp-cf1-lab"},
	})
	if err != nil {
		t.Fatalf("add bastion: %v", err)
	}

	// Bootstrap records the artifacts VM under its name in both fields.
	err = stateManager.AddResource(&state.Resource{
		ID:       "ocfp-cf1-lab-artifacts",
		Type:     "artifacts",
		Name:     "ocfp-cf1-lab-artifacts",
		Provider: "pve",
		State:    "active",
	})
	if err != nil {
		t.Fatalf("add artifacts: %v", err)
	}

	cfg := &config.Config{Name: "ocfp-cf1-lab", Provider: "pve"}
	opts := &commands.TeardownOptions{BlocName: "ocfp-cf1-lab", Provider: "pve"}

	return commands.NewTeardownManager(cfg, provider, stateManager, opts), provider
}

// The dry-run plan has to show the VMID teardown resolved, not the name the
// state file happens to carry. An operator approving a deletion needs to see
// which machine is about to go.
func TestTeardown_PlanShowsBothGuestsWithResolvedVMIDs(t *testing.T) {
	_ = initTestLogger(t)

	manager, _ := newArtifactsFixture(t)

	planned, err := manager.PrepareResourcesForTest(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if len(planned) != 2 {
		t.Fatalf("expected a two-row plan, got %d rows: %v", len(planned), resourceNames(planned))
	}

	byName := make(map[string]*commands.ResourceToDelete, len(planned))
	for _, resource := range planned {
		byName[resource.Name] = resource
	}

	bastion, ok := byName["ocfp-cf1-lab-bastion"]
	if !ok {
		t.Fatal("the bastion is missing from the plan")
	}

	if bastion.ID != "20000" || bastion.Type != "instance" {
		t.Errorf("bastion row = type %q id %q, want type instance id 20000", bastion.Type, bastion.ID)
	}

	artifacts, ok := byName["ocfp-cf1-lab-artifacts"]
	if !ok {
		t.Fatal("the artifacts VM is missing from the plan")
	}

	if artifacts.Type != "artifacts" {
		t.Errorf("artifacts row type = %q, want artifacts", artifacts.Type)
	}

	if artifacts.ID != "20001" {
		t.Errorf("artifacts row id = %q, want the resolved VMID 20001", artifacts.ID)
	}
}

// A real run has to delete both guests by VMID. The artifacts row used to fail
// outright, because the delete switch had no case for its type.
func TestTeardown_DeletesBastionAndArtifactsByVMID(t *testing.T) {
	_ = initTestLogger(t)

	manager, provider := newArtifactsFixture(t)
	ctx := context.Background()

	planned, err := manager.PrepareResourcesForTest(ctx)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	for _, resource := range planned {
		if err = manager.DeleteResource(ctx, resource); err != nil {
			t.Fatalf("delete %s %s: %v", resource.Type, resource.ID, err)
		}
	}

	deleted := append([]string(nil), provider.compute.deleted...)
	sort.Strings(deleted)

	if len(deleted) != 2 || deleted[0] != "20000" || deleted[1] != "20001" {
		t.Fatalf("expected both VMIDs deleted, got %v", deleted)
	}
}

// Naming the artifacts VM in --skip still protects it, VMID resolution or not.
func TestTeardown_SkipArtifactsStillProtectsTheVM(t *testing.T) {
	_ = initTestLogger(t)

	provider := &nukeProvider{
		compute: &nukeCompute{
			instances: []*cpi.Instance{
				{ID: "20001", Name: "ocfp-cf1-lab-artifacts", AvailabilityZone: "pvenode1", State: cpi.ResourceStateActive},
			},
			configs: map[string][]string{},
		},
		storage: &nukeStorage{byNodeStorage: map[string][]*cpi.Volume{}},
	}

	stateManager, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}

	if _, err = stateManager.Load("ocfp-cf1-lab"); err != nil {
		t.Fatalf("load state: %v", err)
	}

	err = stateManager.AddResource(&state.Resource{
		ID:       "ocfp-cf1-lab-artifacts",
		Type:     "artifacts",
		Name:     "ocfp-cf1-lab-artifacts",
		Provider: "pve",
		State:    "active",
	})
	if err != nil {
		t.Fatalf("add artifacts: %v", err)
	}

	manager := commands.NewTeardownManager(
		&config.Config{Name: "ocfp-cf1-lab", Provider: "pve"},
		provider,
		stateManager,
		&commands.TeardownOptions{BlocName: "ocfp-cf1-lab", Provider: "pve", Skip: []string{"artifacts"}},
	)

	planned, err := manager.PrepareResourcesForTest(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if len(planned) != 0 {
		t.Fatalf("--skip artifacts should leave nothing to delete, got %v", resourceNames(planned))
	}
}
