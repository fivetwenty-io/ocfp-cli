package bootstrap

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// fakeGuestOptioner is a provider that supports converging guest options. It
// records what it was asked to do so a test can assert the VM and the flag.
type fakeGuestOptioner struct {
	ids       []string
	protected []bool
	err       error
}

func (f *fakeGuestOptioner) EnsureGuestOptions(_ context.Context, instanceID string, protected bool) error {
	f.ids = append(f.ids, instanceID)
	f.protected = append(f.protected, protected)

	return f.err
}

// fakeGuestOptionerless is a provider with no support for guest options, which
// is every provider but PVE today.
type fakeGuestOptionerless struct{}

// TestEnsureGuestOptionsAsksThePVEProvider covers the convergence path that
// fixes a bastion or artifacts VM created before these options existed. Every
// bloc in the field has one, and the PVE web UI is otherwise the only way to
// set them.
func TestEnsureGuestOptionsAsksThePVEProvider(t *testing.T) {
	t.Parallel()

	optioner := &fakeGuestOptioner{}

	ensureGuestOptions(context.Background(), optioner, "20000", "prod-bastion")

	if len(optioner.ids) != 1 || optioner.ids[0] != "20000" {
		t.Fatalf("ids = %v, want [20000]", optioner.ids)
	}

	if !optioner.protected[0] {
		t.Error("the bastion and artifacts VMs must be asked for protection")
	}
}

// TestEnsureGuestOptionsSkipsProvidersWithoutIt pins the graceful skip: the
// option names are Proxmox's, so a bloc on AWS, GCP, Azure, or STACKIT must
// pass through untouched rather than fail its bootstrap.
func TestEnsureGuestOptionsSkipsProvidersWithoutIt(t *testing.T) {
	t.Parallel()

	ensureGuestOptions(context.Background(), &fakeGuestOptionerless{}, "i-0abc", "prod-bastion")
	ensureGuestOptions(context.Background(), nil, "i-0abc", "prod-bastion")
}

// TestBastionInstanceRequestIsProtected asserts the bastion is created
// protected rather than protected by a later convergence pass, so a bastion
// is never briefly destroyable by an accidental click in the PVE UI.
func TestBastionInstanceRequestIsProtected(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "prod", Provider: "pve"}
	m := newConvergeTestManager(t, "prod", cfg)

	req := m.buildInstanceRequest("prod-bastion", "bastion", "9000", "ocfp", "", "z1", "sg-1", "", false, 0)

	if !req.Protected {
		t.Error("the bastion request must ask for a protected VM")
	}
}

// fakeGuestOptionerProvider is a provider whose compute manager converges
// guest options and nothing else. Only the one method is reachable through
// the interface assertion the convergence path makes, so the rest of
// cpi.Provider stays unimplemented on purpose.
type fakeGuestOptionerProvider struct {
	cpi.Provider

	compute *fakeGuestOptionerCompute
}

func (f *fakeGuestOptionerProvider) ComputeManager() cpi.ComputeManager {
	return f.compute
}

type fakeGuestOptionerCompute struct {
	cpi.ComputeManager

	ids []string
}

func (f *fakeGuestOptionerCompute) EnsureGuestOptions(_ context.Context, instanceID string, protected bool) error {
	if protected {
		f.ids = append(f.ids, instanceID)
	}

	return nil
}

// TestConvergeExistingArtifacts_EnsuresGuestOptions covers the artifacts half
// of the convergence. An artifacts VM that is already deployed takes the skip
// path on every bootstrap, so that path is the only place its options can be
// brought up to date, and it reads the VMID out of state rather than
// rediscovering the guest by name.
func TestConvergeExistingArtifacts_EnsuresGuestOptions(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "prod"}
	cfg.Artifacts.TLS.Mode = config.ArtifactsTLSModeDisabled

	m := newConvergeTestManager(t, "prod", cfg)

	compute := &fakeGuestOptionerCompute{}
	m.provider = &fakeGuestOptionerProvider{compute: compute}

	m.ensureArtifactsGuestOptions(context.Background(), &state.Resource{
		Name:       "prod-artifacts",
		Properties: map[string]interface{}{"vm_id": "20001"},
	})

	if len(compute.ids) != 1 || compute.ids[0] != "20001" {
		t.Fatalf("ids = %v, want [20001] from the recorded vm_id", compute.ids)
	}
}

// TestEnsureArtifactsGuestOptions_NoVMIDRecorded pins the graceful skip for a
// state record written before vm_id was captured, or hand-edited since.
func TestEnsureArtifactsGuestOptions_NoVMIDRecorded(t *testing.T) {
	t.Parallel()

	m := newConvergeTestManager(t, "prod", &config.Config{Name: "prod"})

	compute := &fakeGuestOptionerCompute{}
	m.provider = &fakeGuestOptionerProvider{compute: compute}

	m.ensureArtifactsGuestOptions(context.Background(), &state.Resource{
		Name:       "prod-artifacts",
		Properties: map[string]interface{}{},
	})

	if len(compute.ids) != 0 {
		t.Errorf("no vm_id must mean no call, got %v", compute.ids)
	}
}
