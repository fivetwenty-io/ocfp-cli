package commands_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// errNukeFakeNotFound stands in for a provider's not-found error.
var errNukeFakeNotFound = errors.New("nuke fake: not found")

// nukeCompute is a compute manager whose inventory the test controls. Like the
// PVE manager it stands in for, it never reports templates: they are filtered
// out before a caller ever sees them.
type nukeCompute struct {
	instances []*cpi.Instance
	// configs maps a VMID to the volids that guest's config references.
	configs map[string][]string
	// deleted records every VMID DeleteInstance was asked to remove.
	deleted []string
}

func (c *nukeCompute) ListInstances(_ context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	out := make([]*cpi.Instance, 0, len(c.instances))

	for _, instance := range c.instances {
		if name, ok := filters["name"]; ok && instance.Name != name {
			continue
		}

		out = append(out, instance)
	}

	return out, nil
}

func (c *nukeCompute) GetInstance(_ context.Context, id string) (*cpi.Instance, error) {
	for _, instance := range c.instances {
		if instance.ID != id {
			continue
		}

		clone := *instance
		clone.Volumes = c.configs[id]

		return &clone, nil
	}

	return nil, errNukeFakeNotFound
}

func (c *nukeCompute) DeleteInstance(_ context.Context, id string) error {
	c.deleted = append(c.deleted, id)

	return nil
}

func (c *nukeCompute) CreateInstance(_ context.Context, _ *cpi.InstanceRequest) (*cpi.Instance, error) {
	return nil, errNukeFakeNotFound
}
func (c *nukeCompute) StartInstance(_ context.Context, _ string) error  { return nil }
func (c *nukeCompute) StopInstance(_ context.Context, _ string) error   { return nil }
func (c *nukeCompute) RebootInstance(_ context.Context, _ string) error { return nil }
func (c *nukeCompute) CreateKeyPair(_ context.Context, _ *cpi.KeyPairRequest) (*cpi.KeyPair, error) {
	return nil, errNukeFakeNotFound
}
func (c *nukeCompute) ImportKeyPair(_ context.Context, _ string, _ string) error { return nil }
func (c *nukeCompute) GetKeyPair(_ context.Context, _ string) (*cpi.KeyPair, error) {
	return nil, errNukeFakeNotFound
}
func (c *nukeCompute) ListKeyPairs(_ context.Context) ([]*cpi.KeyPair, error) { return nil, nil }
func (c *nukeCompute) DeleteKeyPair(_ context.Context, _ string) error        { return nil }
func (c *nukeCompute) DeleteVolume(_ context.Context, _ string) error         { return nil }
func (c *nukeCompute) ListFlavors(_ context.Context) ([]*cpi.Flavor, error)   { return nil, nil }
func (c *nukeCompute) CreateVolume(_ context.Context, _ *cpi.VolumeRequest) (*cpi.Volume, error) {
	return nil, errNukeFakeNotFound
}
func (c *nukeCompute) GetVolume(_ context.Context, _ string) (*cpi.Volume, error) {
	return nil, errNukeFakeNotFound
}
func (c *nukeCompute) ListVolumes(_ context.Context, _ map[string]string) ([]*cpi.Volume, error) {
	return nil, nil
}
func (c *nukeCompute) ListImages(_ context.Context, _ map[string]string) ([]*cpi.Image, error) {
	return nil, nil
}
func (c *nukeCompute) GetImage(_ context.Context, _ string) (*cpi.Image, error) {
	return nil, errNukeFakeNotFound
}
func (c *nukeCompute) GetFlavor(_ context.Context, _ string) (*cpi.Flavor, error) {
	return nil, errNukeFakeNotFound
}

// nukeStorage answers volume listings per node and pool, the way the PVE
// storage manager does.
type nukeStorage struct {
	fakeStorageCreds
	// byNodeStorage is keyed "<node>/<storage>".
	byNodeStorage map[string][]*cpi.Volume
	// listed records every "<node>/<storage>" the caller asked about.
	listed []string
	// deleted records every volume id DeleteVolume was asked to remove.
	deleted []string
}

func (s *nukeStorage) ListVolumes(_ context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	key := filters["node"] + "/" + filters["storage"]
	s.listed = append(s.listed, key)

	return s.byNodeStorage[key], nil
}

func (s *nukeStorage) DeleteVolume(_ context.Context, id string) error {
	s.deleted = append(s.deleted, id)

	return nil
}

// nukeProvider wires the two managers above into a cpi.Provider.
type nukeProvider struct {
	fakeProviderCreds
	compute *nukeCompute
	storage *nukeStorage
}

//nolint:ireturn
func (p *nukeProvider) ComputeManager() cpi.ComputeManager { return p.compute }

//nolint:ireturn
func (p *nukeProvider) StorageManager() cpi.StorageManager { return p.storage }

//nolint:ireturn
func (p *nukeProvider) Compute() cpi.ComputeManager { return p.compute }

//nolint:ireturn
func (p *nukeProvider) Storage() cpi.StorageManager { return p.storage }

// newNukeFixture builds a cluster holding one bloc's two guests, another bloc's
// guest, and a pool carrying an attributed disk plus an unattributed one.
func newNukeFixture() *nukeProvider {
	return &nukeProvider{
		compute: &nukeCompute{
			instances: []*cpi.Instance{
				{ID: "20000", Name: "ocfp-cf1-lab-bastion", AvailabilityZone: "pvenode1", State: cpi.ResourceStateActive},
				{ID: "20001", Name: "ocfp-cf1-lab-artifacts", AvailabilityZone: "pvenode1", State: cpi.ResourceStateActive},
				{ID: "30000", Name: "client-prod-bastion", AvailabilityZone: "pvenode1", State: cpi.ResourceStateActive},
			},
			configs: map[string][]string{
				"20000": {"vmdata:vm-20000-disk-0"},
				"20001": {"vmdata:vm-20001-disk-0"},
				"30000": {"clientdata:vm-30000-disk-0"},
			},
		},
		storage: &nukeStorage{
			byNodeStorage: map[string][]*cpi.Volume{
				"pvenode1/vmdata": {
					{ID: "vmdata:vm-20000-disk-0", Name: "vmdata:vm-20000-disk-0", AttachedTo: "20000"},
					{ID: "vmdata:vm-20001-disk-1", Name: "vmdata:vm-20001-disk-1", AttachedTo: "20001"},
					{ID: "vmdata:vm-30000-disk-0", Name: "vmdata:vm-30000-disk-0", AttachedTo: "30000"},
					{ID: "vmdata:orphan-disk", Name: "vmdata:orphan-disk"},
				},
			},
		},
	}
}

func newNukeManager(t *testing.T, provider cpi.Provider, opts *commands.TeardownOptions) *commands.TeardownManager {
	t.Helper()

	stateManager, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}

	if _, err = stateManager.Load(opts.BlocName); err != nil {
		t.Fatalf("load state: %v", err)
	}

	cfg := &config.Config{Name: opts.BlocName, Provider: "pve"}

	return commands.NewTeardownManager(cfg, provider, stateManager, opts)
}

// resourceNames returns the sorted "<type>:<id>" of each planned deletion.
func resourceNames(resources []*commands.ResourceToDelete) []string {
	out := make([]string, 0, len(resources))
	for _, resource := range resources {
		out = append(out, resource.Type+":"+resource.ID)
	}

	sort.Strings(out)

	return out
}

// The whole point of the change. A nuke on a shared cluster must take the
// bloc's own guests and nothing else, however broad the flag sounds.
func TestNuke_SparesForeignGuestsTemplatesAndOrphanVolumes(t *testing.T) {
	_ = initTestLogger(t)

	provider := newNukeFixture()
	manager := newNukeManager(t, provider, &commands.TeardownOptions{
		BlocName: "ocfp-cf1-lab",
		Provider: "pve",
		Nuke:     true,
		Force:    true,
	})

	planned, err := manager.DiscoverResourcesForTest(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	got := resourceNames(planned)
	want := []string{
		"instance:20000",
		"instance:20001",
		"volume:vmdata:vm-20000-disk-0",
		"volume:vmdata:vm-20001-disk-1",
	}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("nuke plan\n got: %v\nwant: %v", got, want)
	}

	for _, entry := range got {
		if strings.Contains(entry, "30000") {
			t.Error("a guest belonging to another bloc entered the nuke plan")
		}

		if strings.Contains(entry, "orphan") {
			t.Error("an unattributed volume entered the nuke plan")
		}
	}
}

// Nuke may only read the storages the bloc's own guests use. Reading a pool no
// bloc guest touched is the first step toward deleting from it.
func TestNuke_ReadsOnlyStoragesTheBlocUses(t *testing.T) {
	_ = initTestLogger(t)

	provider := newNukeFixture()
	manager := newNukeManager(t, provider, &commands.TeardownOptions{
		BlocName: "ocfp-cf1-lab",
		Provider: "pve",
		Nuke:     true,
		Force:    true,
	})

	if _, err := manager.DiscoverResourcesForTest(context.Background()); err != nil {
		t.Fatalf("discover: %v", err)
	}

	for _, key := range provider.storage.listed {
		if strings.Contains(key, "clientdata") {
			t.Errorf("nuke listed %q, a pool no bloc guest uses", key)
		}
	}

	if len(provider.storage.listed) != 1 || provider.storage.listed[0] != "pvenode1/vmdata" {
		t.Errorf("expected exactly one listing of pvenode1/vmdata, got %v", provider.storage.listed)
	}
}

// --skip has to bite in nuke mode too. It used to be ignored entirely.
func TestNuke_HonorsSkip(t *testing.T) {
	_ = initTestLogger(t)

	provider := newNukeFixture()
	manager := newNukeManager(t, provider, &commands.TeardownOptions{
		BlocName: "ocfp-cf1-lab",
		Provider: "pve",
		Nuke:     true,
		Force:    true,
		Skip:     []string{"storage"},
	})

	planned, err := manager.DiscoverResourcesForTest(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	for _, resource := range planned {
		if resource.Type == "volume" {
			t.Errorf("--skip storage should have removed %s from the plan", resource.ID)
		}
	}

	if len(planned) != 2 {
		t.Fatalf("expected the two guests to remain, got %d resources", len(planned))
	}
}

// With no state file there is no record of a bootstrap-created network, so a
// nuke has nothing to delete on the network side and must not go looking.
func TestNuke_TakesNetworksOnlyFromState(t *testing.T) {
	_ = initTestLogger(t)

	provider := newNukeFixture()
	manager := newNukeManager(t, provider, &commands.TeardownOptions{
		BlocName: "ocfp-cf1-lab",
		Provider: "pve",
		Nuke:     true,
		Force:    true,
	})

	planned, err := manager.DiscoverResourcesForTest(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	for _, resource := range planned {
		if resource.Type == "network" || resource.Type == "subnet" {
			t.Errorf("nuke planned %s %q with nothing in state to justify it", resource.Type, resource.ID)
		}
	}
}
