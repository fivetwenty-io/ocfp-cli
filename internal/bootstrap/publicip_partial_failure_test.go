package bootstrap_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

var errPersistentCreate = errors.New("test: persistent create failure")

// partialFailNet fails one address by name on every attempt, modelling a single
// address in a set that cannot be allocated while its siblings succeed.
type partialFailNet struct {
	*fakeNetEnhanced

	failName string
}

func (f *partialFailNet) CreatePublicIP(ctx context.Context, req *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	if req.Name == f.failName {
		return nil, errPersistentCreate
	}

	return f.fakeNetEnhanced.CreatePublicIP(ctx, req)
}

func newPartialFailManager(t *testing.T, failName string, ops int) (*bootstrap.Manager, *state.Manager) {
	t.Helper()

	stateManager, err := state.NewManager(filepath.Join(t.TempDir(), ".state"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err = stateManager.Load("prod"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Name:      "prod",
		Region:    "eu01",
		PublicIPs: config.PublicIPsConfig{Ops: ops},
	}

	netMgr := &partialFailNet{fakeNetEnhanced: newFakeNetEnhanced(), failName: failName}

	manager := bootstrap.NewManager(cfg, &fakeProv{n: netMgr, c: newFakeComputeEnhanced()}, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "custom",
		Region:   "eu01",
	})

	return manager, stateManager
}

// An address that cannot be created after every retry must fail the step. The
// bootstrap runner aborts on a returned error, so swallowing it here reports a
// completed step for work that did not happen.
func TestCreatePublicIPsFailsWhenAnAddressCannotBeCreated(t *testing.T) {
	t.Parallel()

	manager, _ := newPartialFailManager(t, "prod-ops-1", 3)

	err := manager.CreatePublicIPs(context.Background())
	if err == nil {
		t.Fatal("CreatePublicIPs must return an error when an address cannot be created")
	}

	if !strings.Contains(err.Error(), "prod-ops-1") {
		t.Errorf("error should name the address that failed, got: %v", err)
	}
}

// Addresses that do succeed stay recorded, so a retry of the bootstrap picks up
// where it left off rather than reallocating.
func TestCreatePublicIPsKeepsSucceededAddressesOnFailure(t *testing.T) {
	t.Parallel()

	manager, stateManager := newPartialFailManager(t, "prod-ops-1", 3)

	_ = manager.CreatePublicIPs(context.Background())

	resources, err := stateManager.GetResourcesByType("public_ip")
	if err != nil {
		t.Fatal(err)
	}

	if len(resources) != 2 {
		t.Errorf("expected the 2 succeeded addresses to be recorded, got %d", len(resources))
	}
}

// When one address in a set fails, the surviving addresses must keep their own
// identities. Recording them by position in the success slice renames every
// address after the gap, so state ends up pointing at the wrong resource.
func TestPublicIPsRecordedAtTrueIndexAfterPartialFailure(t *testing.T) {
	t.Parallel()

	stateManager, err := state.NewManager(filepath.Join(t.TempDir(), ".state"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err = stateManager.Load("prod"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Name:      "prod",
		Region:    "eu01",
		PublicIPs: config.PublicIPsConfig{Ops: 3},
	}

	netMgr := &partialFailNet{fakeNetEnhanced: newFakeNetEnhanced(), failName: "prod-ops-1"}

	manager := bootstrap.NewManager(cfg, &fakeProv{n: netMgr, c: newFakeComputeEnhanced()}, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "custom",
		Region:   "eu01",
	})

	// The returned error is asserted separately; this test is about what the
	// surviving addresses are recorded as.
	_ = manager.CreatePublicIPs(context.Background())

	resources, err := stateManager.GetResourcesByType("public_ip")
	if err != nil {
		t.Fatal(err)
	}

	byName := make(map[string]string, len(resources))
	for _, resource := range resources {
		byName[resource.Name] = resource.ID
	}

	if id, ok := byName["prod-ops-0"]; !ok || id != "ip-prod-ops-0" {
		t.Errorf("prod-ops-0 recorded as %q (present=%v), want ip-prod-ops-0", id, ok)
	}

	if id, ok := byName["prod-ops-2"]; !ok || id != "ip-prod-ops-2" {
		t.Errorf("prod-ops-2 recorded as %q (present=%v), want ip-prod-ops-2", id, ok)
	}

	if id, ok := byName["prod-ops-1"]; ok {
		t.Errorf("prod-ops-1 must not be recorded — it was never created, but state holds %q", id)
	}

	if _, err := stateManager.GetOutput("ops_public_ip_2"); err != nil {
		t.Errorf("ops_public_ip_2 output missing: %v", err)
	}

	if _, err := stateManager.GetOutput("ops_public_ip_1"); err == nil {
		t.Error("ops_public_ip_1 must not be set — that address was never created")
	}
}
