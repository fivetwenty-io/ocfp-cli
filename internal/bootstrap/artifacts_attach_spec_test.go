package bootstrap_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// newArtifactsAttachManager builds a Manager for the artifacts data-volume
// attach, optionally with a preserved volume already recorded in state.
func newArtifactsAttachManager(
	t *testing.T,
	st *dataVolStorage,
	provider string,
	preserved bool,
) *bootstrap.Manager {
	t.Helper()

	stateManager, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatal(err)
	}

	if preserved {
		err = stateManager.AddResource(&state.Resource{
			ID:   "local-lvm-data:vm-101-data",
			Type: state.ResourceTypeVolume,
			Name: "prod-artifacts-data",
			Properties: map[string]interface{}{
				"role": "artifacts-data", "preserve": true,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{Name: "prod", Provider: provider}

	return bootstrap.NewManager(
		cfg, &fakeProvWithStorage{s: st}, stateManager,
		&bootstrap.Options{BlocName: "prod", Provider: provider, Yes: true},
	)
}

// TestAttachArtifactsDataVolume_PVEDiscardSpec asserts that on PVE the
// artifacts data disk is attached with discard on and a pinned slot, on both
// the new-volume path and the adopt-preserved path.
//
// The legacy /dev/sdb hint pins no slot and carries no disk properties, so the
// disk never gets discard=on and the thin pool never reclaims freed blocks.
func TestAttachArtifactsDataVolume_PVEDiscardSpec(t *testing.T) {
	t.Parallel()

	for _, preserved := range []bool{false, true} {
		name := "new volume"
		if preserved {
			name = "adopted preserved volume"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			st := &dataVolStorage{}
			manager := newArtifactsAttachManager(t, st, "pve", preserved)

			_, err := manager.AttachArtifactsDataVolume(context.Background(), "101", "prod-artifacts")
			if err != nil {
				t.Fatalf("AttachArtifactsDataVolume: %v", err)
			}

			for _, want := range []string{
				"scsi1",
				"discard=on",
				"serial=" + config.ArtifactsDataDiskSerial,
			} {
				if !strings.Contains(st.attachDev, want) {
					t.Errorf("attach spec %q missing %q", st.attachDev, want)
				}
			}

			if !strings.HasPrefix(st.attachDev, "scsi1,") {
				t.Errorf("attach spec %q must pin slot scsi1 first", st.attachDev)
			}
		})
	}
}

// TestAttachArtifactsDataVolume_OtherProvidersKeepDevicePath asserts providers
// that treat the hint as a real device name still receive /dev/sdb.
func TestAttachArtifactsDataVolume_OtherProvidersKeepDevicePath(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"aws", "gcp", "azure", "stackit"} {
		for _, preserved := range []bool{false, true} {
			st := &dataVolStorage{}
			manager := newArtifactsAttachManager(t, st, provider, preserved)

			_, err := manager.AttachArtifactsDataVolume(context.Background(), "i-1", "prod-artifacts")
			if err != nil {
				t.Fatalf("%s: AttachArtifactsDataVolume: %v", provider, err)
			}

			if st.attachDev != "/dev/sdb" {
				t.Errorf("%s (preserved=%v): attach device = %q, want /dev/sdb", provider, preserved, st.attachDev)
			}
		}
	}
}
