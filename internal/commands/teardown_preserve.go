package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// shouldPreserveVolume reports whether a recorded volume asked to outlive the
// guest it is attached to.
//
// Only volumes that ask are spared. A teardown that silently left every volume
// behind would leak storage on every bloc it ran against, so the flag is
// opt-in and written by the code that creates the disk.
//
// Both encodings are accepted because the state file round-trips through JSON
// and a bool can come back either way depending on how it was written.
func shouldPreserveVolume(props map[string]interface{}) bool {
	raw, ok := props["preserve"]
	if !ok {
		return false
	}

	switch v := raw.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}

// preservedVolumesForInstance returns the volume ids that must be detached
// from the given instance before it is destroyed.
//
// This is the difference between a persistent disk and a disk that merely
// exists. PVE's DeleteInstance purges with purge=true, which destroys every
// disk the VM config still references — including anything sitting in an
// unusedN slot — so a data disk that is not detached first dies with the
// machine it was meant to outlive.
//
// The result is sorted so teardown output and tests are stable rather than
// state-file ordering.
func preservedVolumesForInstance(resources []*state.Resource, instanceID string) []string {
	if instanceID == "" {
		return nil
	}

	var preserved []string

	for _, res := range resources {
		if res == nil || res.Type != state.ResourceTypeVolume {
			continue
		}

		if !shouldPreserveVolume(res.Properties) {
			continue
		}

		owner, _ := res.Properties["vm_id"].(string)
		if owner != instanceID {
			continue
		}

		preserved = append(preserved, res.ID)
	}

	sort.Strings(preserved)

	return preserved
}

// detachPreservedVolumes detaches every volume recorded as preserved for this
// instance, before the instance is destroyed.
//
// A preservation failure stops deletion: warning and continuing would turn a
// transient API or state error into permanent loss of explicitly retained data.
func (m *TeardownManager) detachPreservedVolumes(ctx context.Context, resource *ResourceToDelete) error {
	if m.stateManager == nil {
		return errors.New("cannot verify preserved volumes without bloc state")
	}
	resources, err := m.stateManager.GetResourcesByType(state.ResourceTypeVolume)
	if err != nil {
		return fmt.Errorf("read recorded volumes before deleting %s: %w", resource.ID, err)
	}

	volumes := preservedVolumesForInstance(resources, resource.ID)
	if len(volumes) == 0 {
		return nil
	}

	storage := m.provider.StorageManager()
	if storage == nil {
		return fmt.Errorf("cannot detach preserved volumes for %s: storage manager unavailable", resource.ID)
	}

	for _, volID := range volumes {
		if err := storage.DetachVolume(ctx, volID, resource.ID); err != nil {
			return fmt.Errorf("refusing to delete %s: preserved volume %s could not be detached: %w", resource.ID, volID, err)
		}
		_, _ = fmt.Printf("    • Detached %s so it survives the teardown of %s\n", volID, resource.Name)
		logger.Infof("Detached preserved volume %s from instance %s", volID, resource.ID)
	}

	return nil
}
