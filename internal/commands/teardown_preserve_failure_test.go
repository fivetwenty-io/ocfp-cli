package commands

import (
	"context"
	"errors"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"reflect"
	"testing"
)

type preservationCompute struct {
	cpi.ComputeManager
	calls      *[]string
	protectErr error
}

func (m *preservationCompute) DeleteInstance(context.Context, string) error {
	*m.calls = append(*m.calls, "delete")
	return nil
}
func (m *preservationCompute) EnsureGuestOptions(_ context.Context, _ string, o cpi.GuestOptions) error {
	if o.Protected {
		return errors.New("expected unprotect")
	}
	*m.calls = append(*m.calls, "unprotect")
	return m.protectErr
}

type preservationStorage struct {
	cpi.StorageManager
	calls *[]string
	err   error
}

func (m *preservationStorage) DetachVolume(context.Context, string, string) error {
	*m.calls = append(*m.calls, "detach")
	return m.err
}

type preservationProvider struct {
	cpi.Provider
	compute cpi.ComputeManager
	storage cpi.StorageManager
}

func (p *preservationProvider) ComputeManager() cpi.ComputeManager { return p.compute }
func (p *preservationProvider) StorageManager() cpi.StorageManager { return p.storage }
func TestTeardownPreservationFailureStopsDeletion(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		detachErr, protectErr error
		want                  []string
	}{
		{"detach failure", errors.New("detach denied"), nil, []string{"detach"}},
		{"protection failure", nil, errors.New("denied"), []string{"detach", "unprotect"}},
		{"safe sequence", nil, nil, []string{"detach", "unprotect", "delete"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm, err := state.NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = sm.Load("prod"); err != nil {
				t.Fatal(err)
			}
			if err = sm.AddResource(&state.Resource{Type: state.ResourceTypeVolume, Name: "prod-data", ID: "vol-1", Properties: map[string]interface{}{"preserve": true, "vm_id": "i-1"}}); err != nil {
				t.Fatal(err)
			}
			var calls []string
			p := &preservationProvider{compute: &preservationCompute{calls: &calls, protectErr: tc.protectErr}, storage: &preservationStorage{calls: &calls, err: tc.detachErr}}
			m := &TeardownManager{provider: p, stateManager: sm}
			err = m.deleteComputeResource(context.Background(), &ResourceToDelete{ID: "i-1", Name: "prod-bastion"})
			if (tc.detachErr != nil || tc.protectErr != nil) != (err != nil) {
				t.Errorf("error=%v", err)
			}
			if !reflect.DeepEqual(calls, tc.want) {
				t.Errorf("calls=%v want=%v", calls, tc.want)
			}
		})
	}
}

func TestTeardownPreservationUnreadableStateStopsDeletion(t *testing.T) {
	unloaded, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, sm := range []*state.Manager{nil, unloaded} {
		var calls []string
		m := &TeardownManager{provider: &preservationProvider{compute: &preservationCompute{calls: &calls}, storage: &preservationStorage{calls: &calls}}, stateManager: sm}
		if err := m.deleteComputeResource(context.Background(), &ResourceToDelete{ID: "i-1"}); err == nil {
			t.Fatal("unavailable state accepted")
		}
		if len(calls) != 0 {
			t.Fatalf("mutated before reading state: %v", calls)
		}
	}
}
