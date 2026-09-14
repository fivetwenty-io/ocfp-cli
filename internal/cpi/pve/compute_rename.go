package pve

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// parseVMID turns an instance id into a Proxmox VMID.
func parseVMID(instanceID string) (int, error) {
	vmid, err := strconv.Atoi(instanceID)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrInvalidVMID, instanceID)
	}

	return vmid, nil
}

// vzdumpTaskTimeout bounds an archive. A vzdump streams the whole allocated
// disk, so it is measured in minutes even for a modest guest.
const vzdumpTaskTimeout = 7200

// renameParams is the config write that renames a guest.
func renameParams(name string) map[string]interface{} {
	return map[string]interface{}{"name": name}
}

// RenameInstance changes a guest's name.
//
// The recycle operation builds its replacement under a transient name, so that
// name-based discovery and teardown are not confused while both guests are
// live, and renames it once the original is gone. Without this the bloc ends
// up with a bastion permanently called "<bloc>-bastion-next" that every
// name-based lookup fails to find.
func (m *ComputeManager) RenameInstance(ctx context.Context, instanceID, name string) error {
	vmid, err := parseVMID(instanceID)
	if err != nil {
		return err
	}

	node, err := m.client.getNode(ctx)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", url.PathEscape(node), vmid)

	_, err = m.client.pveClient.PutCtx(ctx, path, renameParams(name))
	if err != nil {
		return fmt.Errorf("rename VM %d to %q: %w", vmid, name, err)
	}

	logger.Infof("Renamed VM %d to %s", vmid, name)

	return nil
}

// vzdumpParams builds the archive request.
//
// The archive is marked protected because it is the only route back once a
// recycle passes its point of no return, and an unprotected copy is one a
// retention job can remove.
func vzdumpParams(vmid int, storage, notes string) map[string]interface{} {
	return map[string]interface{}{
		"vmid":           vmid,
		"storage":        storage,
		"mode":           "snapshot",
		"compress":       "zstd",
		"protected":      1,
		"notes-template": notes,
	}
}

// ArchiveInstance takes a vzdump that survives the guest being destroyed.
//
// A snapshot is not enough for a recycle: destroying the VM takes its
// snapshots with it. This is the copy that does not go away.
func (m *ComputeManager) ArchiveInstance(ctx context.Context, instanceID, storage string) error {
	if storage == "" {
		return ErrArchiveStorageUnset
	}

	vmid, err := parseVMID(instanceID)
	if err != nil {
		return err
	}

	node, err := m.client.getNode(ctx)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/nodes/%s/vzdump", url.PathEscape(node))

	resp, err := m.client.pveClient.PostCtx(ctx, path,
		vzdumpParams(vmid, storage, "ocfp recycle archive of {{guestname}}"))
	if err != nil {
		return fmt.Errorf("archive VM %d to %s: %w", vmid, storage, err)
	}

	upid, err := upidFromResponse(resp)
	if err != nil {
		return fmt.Errorf("archive task id: %w", err)
	}

	if upid == "" {
		return nil
	}

	err = m.client.waitForTask(ctx, node, upid, vzdumpTaskTimeout)
	if err != nil {
		return fmt.Errorf("await archive task: %w", err)
	}

	logger.Infof("Archived VM %d to %s", vmid, storage)

	return nil
}
