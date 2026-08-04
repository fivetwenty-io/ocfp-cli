package bootstrap_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

var errNoPublicIPSupport = errors.New("test: floating IPs require external IP management")

// refusingNet models a provider like PVE: it has a NetworkManager, but every
// public IP operation is a stub that reports the feature is unavailable.
type refusingNet struct {
	*fakeNetEnhanced

	createCalls int
}

func (f *refusingNet) SupportsPublicIPs() bool { return false }

func (f *refusingNet) CreatePublicIP(_ context.Context, _ *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	f.createCalls++

	return nil, errNoPublicIPSupport
}

func newRefusingManager(t *testing.T, netMgr *refusingNet) (*bootstrap.Manager, *state.Manager) {
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
		PublicIPs: config.PublicIPsConfig{Ops: 1},
	}

	manager := bootstrap.NewManager(cfg, &fakeProv{n: netMgr, c: newFakeComputeEnhanced()}, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "custom",
		Region:   "eu01",
	})

	return manager, stateManager
}

// A NetworkManager that declares no public IP support must be skipped outright
// rather than driven through the create-and-retry loop.
func TestCreatePublicIPsSkipsProviderDeclaringNoSupport(t *testing.T) {
	t.Parallel()

	netMgr := &refusingNet{fakeNetEnhanced: newFakeNetEnhanced()}

	manager, stateManager := newRefusingManager(t, netMgr)

	err := manager.CreatePublicIPs(context.Background())
	if err != nil {
		t.Fatalf("CreatePublicIPs should skip, not fail: %v", err)
	}

	if netMgr.createCalls != 0 {
		t.Errorf("expected no create attempts, got %d", netMgr.createCalls)
	}

	resources, err := stateManager.GetResourcesByType("public_ip")
	if err != nil {
		t.Fatal(err)
	}

	if len(resources) != 0 {
		t.Errorf("expected no public IPs recorded, got %d", len(resources))
	}
}
