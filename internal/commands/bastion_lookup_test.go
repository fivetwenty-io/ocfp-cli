package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// stubCompute implements cpi.ComputeManager with configurable ListInstances behavior.
type stubCompute struct {
	// listResponses maps a filter key to its response for deterministic test behavior.
	// The key is a string representation of the filters map.
	listResponses map[string][]*cpi.Instance
}

func (s *stubCompute) ListInstances(_ctx context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	key := fmt.Sprintf("%v", filters)
	if instances, ok := s.listResponses[key]; ok {
		return instances, nil
	}

	return nil, nil //nolint:nilnil // test stub returns empty when no match
}

func (s *stubCompute) CreateInstance(_ctx context.Context, _req *cpi.InstanceRequest) (*cpi.Instance, error) { //nolint:nilnil // test stub
	return nil, nil
}
func (s *stubCompute) GetInstance(_ctx context.Context, _id string) (*cpi.Instance, error) { //nolint:nilnil // test stub
	return nil, nil
}
func (s *stubCompute) StartInstance(_ctx context.Context, _id string) error  { return nil }
func (s *stubCompute) StopInstance(_ctx context.Context, _id string) error   { return nil }
func (s *stubCompute) RebootInstance(_ctx context.Context, _id string) error { return nil }
func (s *stubCompute) DeleteInstance(_ctx context.Context, _id string) error { return nil }
func (s *stubCompute) CreateKeyPair(_ctx context.Context, _req *cpi.KeyPairRequest) (*cpi.KeyPair, error) { //nolint:nilnil // test stub
	return nil, nil
}
func (s *stubCompute) ImportKeyPair(_ctx context.Context, _name string, _publicKey string) error {
	return nil
}
func (s *stubCompute) GetKeyPair(_ctx context.Context, _name string) (*cpi.KeyPair, error) { //nolint:nilnil // test stub
	return nil, nil
}
func (s *stubCompute) ListKeyPairs(_ctx context.Context) ([]*cpi.KeyPair, error) { //nolint:nilnil // test stub
	return nil, nil
}
func (s *stubCompute) DeleteKeyPair(_ctx context.Context, _name string) error { return nil }
func (s *stubCompute) CreateVolume(_ctx context.Context, _req *cpi.VolumeRequest) (*cpi.Volume, error) { //nolint:nilnil // test stub
	return nil, nil
}
func (s *stubCompute) GetVolume(_ctx context.Context, _id string) (*cpi.Volume, error) { //nolint:nilnil // test stub
	return nil, nil
}
func (s *stubCompute) ListVolumes(_ctx context.Context, _filters map[string]string) ([]*cpi.Volume, error) { //nolint:nilnil // test stub
	return nil, nil
}
func (s *stubCompute) DeleteVolume(_ctx context.Context, _id string) error { return nil }
func (s *stubCompute) ListImages(_ctx context.Context, _filters map[string]string) ([]*cpi.Image, error) { //nolint:nilnil // test stub
	return nil, nil
}
func (s *stubCompute) GetImage(_ctx context.Context, _id string) (*cpi.Image, error) { //nolint:nilnil // test stub
	return nil, nil
}
func (s *stubCompute) ListFlavors(_ctx context.Context) ([]*cpi.Flavor, error) { //nolint:nilnil // test stub
	return nil, nil
}
func (s *stubCompute) GetFlavor(_ctx context.Context, _id string) (*cpi.Flavor, error) { //nolint:nilnil // test stub
	return nil, nil
}

// stubProvider implements cpi.Provider with only a compute manager.
type stubProvider struct {
	compute cpi.ComputeManager
}

func (p *stubProvider) Name() string                                   { return "fake" }
func (p *stubProvider) Region() string                                 { return "us-east-1" }
func (p *stubProvider) Authenticate(_ctx context.Context) error        { return nil }
func (p *stubProvider) ValidateCredentials(_ctx context.Context) error { return nil }

//nolint:ireturn
func (p *stubProvider) Network() cpi.NetworkManager { return nil }

//nolint:ireturn
func (p *stubProvider) Compute() cpi.ComputeManager { return p.compute }

//nolint:ireturn
func (p *stubProvider) Storage() cpi.StorageManager { return nil }

//nolint:ireturn
func (p *stubProvider) Security() cpi.SecurityManager { return nil }

//nolint:ireturn
func (p *stubProvider) LoadBalancer() cpi.LoadBalancerManager { return nil }

//nolint:ireturn
func (p *stubProvider) NetworkManager() cpi.NetworkManager { return p.Network() }

//nolint:ireturn
func (p *stubProvider) ComputeManager() cpi.ComputeManager { return p.Compute() }

//nolint:ireturn
func (p *stubProvider) StorageManager() cpi.StorageManager { return p.Storage() }

//nolint:ireturn
func (p *stubProvider) SecurityManager() cpi.SecurityManager { return p.Security() }

//nolint:ireturn
func (p *stubProvider) LoadBalancerManager() cpi.LoadBalancerManager { return p.LoadBalancer() }

func (p *stubProvider) SupportsStorage() bool                                   { return false }
func (p *stubProvider) Initialize(_ctx context.Context, _cfg interface{}) error { return nil }
func (p *stubProvider) Cleanup(_ctx context.Context) error                      { return nil }

func TestFindBastionInstance(t *testing.T) {
	t.Parallel()

	bastionByRole := &cpi.Instance{
		ID:   "i-role",
		Name: "520-aws-wayne-bastion",
	}

	bastionByComponent := &cpi.Instance{
		ID:   "i-component",
		Name: "520-aws-wayne-bastion",
	}

	bastionByName := &cpi.Instance{
		ID:   "i-name",
		Name: "520-aws-wayne-bastion",
	}

	blocName := "520-aws-wayne"

	tests := []struct {
		name      string
		responses map[string][]*cpi.Instance
		wantID    string
		wantErr   bool
	}{
		{
			name: "found by role tag",
			responses: map[string][]*cpi.Instance{
				fmt.Sprintf("map[label.bloc:%s label.role:bastion]", blocName): {bastionByRole},
			},
			wantID: "i-role",
		},
		{
			name: "found by component tag when role missing",
			responses: map[string][]*cpi.Instance{
				fmt.Sprintf("map[label.bloc:%s label.component:bastion]", blocName): {bastionByComponent},
			},
			wantID: "i-component",
		},
		{
			name: "found by name pattern when no tags match",
			responses: map[string][]*cpi.Instance{
				// No label matches, but bloc-level list returns instances
				fmt.Sprintf("map[label.bloc:%s]", blocName): {
					{ID: "i-worker", Name: "520-aws-wayne-worker"},
					bastionByName,
				},
			},
			wantID: "i-name",
		},
		{
			name:      "no bastion found returns error",
			responses: map[string][]*cpi.Instance{},
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &stubProvider{
				compute: &stubCompute{listResponses: tc.responses},
			}

			inst, err := findBastionInstance(context.Background(), provider, blocName)

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if inst.ID != tc.wantID {
				t.Errorf("expected instance ID %s, got %s", tc.wantID, inst.ID)
			}
		})
	}
}

// TestTryReservedBastionIP confirms the last-resort fallback that reads the
// reserved bastion address bootstrap records under the bloc's primary
// subnet. This path keeps `ocfp ssh bastion` working on PVE bridges where
// the guest agent hasn't reported and ipconfig0 was set to DHCP.
func TestTryReservedBastionIP(t *testing.T) {
	blocName := "tst-bloc"

	t.Setenv("OCFP_HOME", t.TempDir())

	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		t.Fatalf("GetStateDir: %v", err)
	}

	mgr, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	_, err = mgr.Load(blocName)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	err = mgr.SetOutput("reserved_"+blocName+"-ocfp-0_bastion_ip", "10.4.4.3")
	if err != nil {
		t.Fatalf("SetOutput: %v", err)
	}

	err = mgr.Save()
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := tryReservedBastionIP(blocName, logger.WithOperation("test"))
	if got != "10.4.4.3" {
		t.Errorf("tryReservedBastionIP = %q, want 10.4.4.3", got)
	}
}

// TestTryReservedBastionIP_LegacyKey verifies the alternate
// reserved_<bloc>_bastion_ip layout is still picked up.
func TestTryReservedBastionIP_LegacyKey(t *testing.T) {
	blocName := "legacy-bloc"

	t.Setenv("OCFP_HOME", t.TempDir())

	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		t.Fatalf("GetStateDir: %v", err)
	}

	mgr, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	_, err = mgr.Load(blocName)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	err = mgr.SetOutput("reserved_"+blocName+"_bastion_ip", "192.168.1.67")
	if err != nil {
		t.Fatalf("SetOutput: %v", err)
	}

	err = mgr.Save()
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := tryReservedBastionIP(blocName, logger.WithOperation("test"))
	if got != "192.168.1.67" {
		t.Errorf("tryReservedBastionIP = %q, want 192.168.1.67", got)
	}
}

// TestTryReservedBastionIP_Missing returns empty when no key is present.
func TestTryReservedBastionIP_Missing(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	got := tryReservedBastionIP("missing-bloc", logger.WithOperation("test"))
	if got != "" {
		t.Errorf("tryReservedBastionIP = %q, want empty", got)
	}
}

func TestFindBastionInstancePrefersComponentOverNameFallback(t *testing.T) {
	t.Parallel()

	blocName := "test-bloc"

	provider := &stubProvider{
		compute: &stubCompute{
			listResponses: map[string][]*cpi.Instance{
				// component tag matches
				fmt.Sprintf("map[label.bloc:%s label.component:bastion]", blocName): {
					{ID: "i-component", Name: "test-bloc-bastion"},
				},
				// name-based would also match, but should not be reached
				fmt.Sprintf("map[label.bloc:%s]", blocName): {
					{ID: "i-name-only", Name: "test-bloc-bastion-vm"},
				},
			},
		},
	}

	inst, err := findBastionInstance(context.Background(), provider, blocName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if inst.ID != "i-component" {
		t.Errorf("expected component-tagged instance i-component, got %s", inst.ID)
	}
}
