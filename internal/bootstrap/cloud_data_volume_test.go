package bootstrap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

func TestCloudBastionDataAttachment(t *testing.T) {
	for _, tc := range []struct{ provider, device string }{{"aws", "/dev/sdf"}, {"gcp", "ocfp-bastion-data"}} {
		t.Run(tc.provider, func(t *testing.T) {
			storage := &dataVolStorage{}
			manager, _ := newDataVolManagerWithOptions(t, storage, bootstrap.Options{BlocName: "prod", Provider: tc.provider})
			if err := manager.EnsureBastionDataVolume(context.Background()); err != nil {
				t.Fatal(err)
			}
			if storage.attachDev != tc.device {
				t.Fatalf("device = %q, want %q", storage.attachDev, tc.device)
			}
		})
	}
}

func TestBastionDataDiscoveryFailureDoesNotAllocate(t *testing.T) {
	storage := &dataVolStorage{listErr: errors.New("permission denied")}
	manager, _ := newDataVolManager(t, storage)
	if err := manager.EnsureBastionDataVolume(context.Background()); err == nil {
		t.Fatal("discovery failure must abort")
	}
	if storage.createdReq != nil {
		t.Fatal("allocated after failed discovery")
	}
}

func TestBastionDataAmbiguousAttachmentPreservesRecordedDisk(t *testing.T) {
	storage := &dataVolStorage{attachErr: errors.New("response lost")}
	manager, sm := newDataVolManager(t, storage)
	if err := manager.EnsureBastionDataVolume(context.Background()); err == nil {
		t.Fatal("expected attach error")
	}
	if storage.deletedID != "" {
		t.Fatal("ambiguous attachment must not delete data")
	}
	res, err := sm.GetResource(state.ResourceTypeVolume, "prod-bastion-data")
	if err != nil || res == nil || res.ID == "" {
		t.Fatal("allocated disk must remain discoverable in state")
	}
}

func TestCloudArtifactsAttachmentFailurePreservesDisk(t *testing.T) {
	storage := &dataVolStorage{attachErr: errors.New("response lost")}
	manager := newArtifactsAttachManager(t, storage, "aws", false)
	if _, err := manager.AttachArtifactsDataVolume(context.Background(), "i-1", "prod-artifacts"); err == nil {
		t.Fatal("expected error")
	}
	if storage.deletedID != "" {
		t.Fatal("ambiguous attachment must not delete artifacts disk")
	}
}

func TestBastionDataDuplicateDiscoveryFailsClosed(t *testing.T) {
	storage := &dataVolStorage{volumes: []*cpi.Volume{{ID: "one", Name: "prod-bastion-data"}, {ID: "two", Name: "prod-bastion-data"}}}
	manager, _ := newDataVolManager(t, storage)
	if err := manager.EnsureBastionDataVolume(context.Background()); err == nil {
		t.Fatal("ambiguous disks must abort")
	}
	if storage.called("AttachVolume") || storage.createdReq != nil {
		t.Fatal("ambiguous discovery mutated disks")
	}
}

func TestBastionPartialDataCreationPersistsHandle(t *testing.T) {
	storage := &partialArtifactsStorage{}
	manager, sm := newDataVolManager(t, storage)
	if err := manager.EnsureBastionDataVolume(context.Background()); err == nil {
		t.Fatal("expected readiness failure")
	}
	res, err := sm.GetResource(state.ResourceTypeVolume, "prod-bastion-data")
	if err != nil || res == nil || res.ID == "" {
		t.Fatal("lost disk handle")
	}
	if storage.called("AttachVolume") {
		t.Fatal("attached before successful readiness")
	}
}

func TestCloudBastionDataRejectsForeignNamedDisk(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		storage := &dataVolStorage{volumes: []*cpi.Volume{{ID: "foreign", Name: "prod-bastion-data", Tags: map[string]string{"ocfp:bloc": "other"}}}}
		m, _ := newDataVolManagerWithOptions(t, storage, bootstrap.Options{BlocName: "prod", Provider: provider})
		if err := m.EnsureBastionDataVolume(context.Background()); err == nil {
			t.Fatal("foreign named disk adopted")
		}
		if storage.called("AttachVolume") || storage.createdReq != nil {
			t.Fatal("foreign disk discovery mutated cloud")
		}
	}
}
