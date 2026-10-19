package bootstrap

import (
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"testing"
)

func TestRollbackPreservesDisksOwnersAndDependencies(t *testing.T) {
	sm, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sm.Load("prod"); err != nil {
		t.Fatal(err)
	}
	add := func(typ, name, id string, props map[string]interface{}) {
		t.Helper()
		if err := sm.AddResource(&state.Resource{Type: typ, Name: name, ID: id, Properties: props}); err != nil {
			t.Fatal(err)
		}
	}
	add(state.ResourceTypeVolume, "prod-data", "vol-data", map[string]interface{}{"preserve": "true", "vm_id": "i-keep"})
	add(state.ResourceTypeInstance, "prod-bastion", "i-keep", nil)
	add(state.ResourceTypeSubnet, "infra", "subnet-keep", nil)
	add(state.ResourceTypeNetwork, "net", "vpc-keep", nil)
	add(state.ResourceTypeVolume, "scratch", "vol-scratch", nil)
	add(state.ResourceTypeInstance, "adopted", "i-adopted", map[string]interface{}{"adopted": true})
	if err := sm.AddDependency("instance.prod-bastion", "subnet.infra"); err != nil {
		t.Fatal(err)
	}
	if err := sm.AddDependency("subnet.infra", "network.net"); err != nil {
		t.Fatal(err)
	}
	m := &Manager{stateManager: sm, rollbackBaseline: map[string]string{}}
	got := m.collectRollbackResources()
	if len(got[state.ResourceTypeVolume]) != 1 {
		t.Fatalf("scratch resource missing: %+v", got)
	}
	for typ, resources := range got {
		for _, r := range resources {
			if typ != state.ResourceTypeVolume || r.ID != "vol-scratch" {
				t.Errorf("unsafe rollback candidate: %s %s", typ, r.ID)
			}
		}
	}
}

func TestRollbackScopeExcludesPriorResources(t *testing.T) {
	sm, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sm.Load("prod"); err != nil {
		t.Fatal(err)
	}
	add := func(name, id string) {
		t.Helper()
		if err := sm.AddResource(&state.Resource{Type: state.ResourceTypeVolume, Name: name, ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	add("old", "vol-old")
	m := &Manager{stateManager: sm}
	if len(m.collectRollbackResources()) != 0 {
		t.Fatal("rollback without a captured scope must fail closed")
	}
	m.captureRollbackBaseline()
	add("new", "vol-new")
	add("old-alias", "vol-old")
	got := m.collectRollbackResources()
	if len(got[state.ResourceTypeVolume]) != 1 || got[state.ResourceTypeVolume][0].ID != "vol-new" {
		t.Fatalf("rollback includes prior resources: %+v", got)
	}
}
