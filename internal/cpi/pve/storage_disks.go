package pve

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// diskBusPrefixes are the config key prefixes of slots that attach a disk to
// the guest. Unlike diskSlotPrefixes it leaves out unusedN, because a volume
// parked there is not attached and its options do nothing.
//
//nolint:gochecknoglobals // intentional package-level lookup table
var diskBusPrefixes = []string{"scsi", "virtio", "sata", "ide"}

// VMDiskSlot is one disk attached to a VM, as its config and pending list
// report it.
type VMDiskSlot struct {
	// Slot is the config key, such as scsi1.
	Slot string
	// Value is the slot's current config value.
	Value string
	// VolumeID is the storage:volume part of Value.
	VolumeID string
	// Pool is the storage pool named in VolumeID.
	Pool string
	// PoolType is the pool's plugin type, such as lvmthin or nfs.
	PoolType string
	// Provisioning says whether the pool hands freed blocks back.
	Provisioning StorageProvisioning
	// Discard is the current discard option, or empty when the slot has none.
	Discard string
	// PendingValue is the value queued for the slot on the next cold start,
	// or empty when nothing is queued.
	PendingValue string
}

// DiscardOn reports whether the slot's current value carries discard=on.
func (s VMDiskSlot) DiscardOn() bool {
	return s.Discard == discardOn
}

// PendingDiscardOn reports whether a queued value for the slot carries
// discard=on.
func (s VMDiskSlot) PendingDiscardOn() bool {
	if s.PendingValue == "" {
		return false
	}

	discard, _ := parseDiskSlotValue(s.PendingValue).Option(discardOption)

	return discard == discardOn
}

// VMDisks is the disk view of one VM: its attached disks in slot order and
// the config digest they were read under.
type VMDisks struct {
	Node   string
	VMID   int
	Digest string
	Disks  []VMDiskSlot
}

// Slot returns the disk in the given slot.
func (d *VMDisks) Slot(slot string) (VMDiskSlot, bool) {
	for _, disk := range d.Disks {
		if disk.Slot == slot {
			return disk, true
		}
	}

	return VMDiskSlot{}, false
}

// DataDisk returns the disk holding the given volume, falling back to the
// disk carrying the given serial.
//
// The volid comes first because it is what state recorded, and the legacy
// disks this exists to find carry no serial. The serial is the fallback for a
// volid that is unknown, or stale after a reassign renamed the volume.
func (d *VMDisks) DataDisk(volumeID, serial string) (VMDiskSlot, bool) {
	config := make(map[string]interface{}, len(d.Disks))
	for _, disk := range d.Disks {
		config[disk.Slot] = disk.Value
	}

	if volumeID != "" {
		if slot, found := findDiskSlotForVolume(config, volumeID); found {
			return d.Slot(slot)
		}
	}

	if serial == "" {
		return VMDiskSlot{}, false
	}

	for _, disk := range d.Disks {
		if diskCarriesSerial(disk.Value, serial) {
			return disk, true
		}
	}

	return VMDiskSlot{}, false
}

// VMDisks reads a VM's attached disks with each disk's pool type and any
// change queued for it.
//
// The config read alone is not enough. On a running VM a disk change is
// queued until the next cold start, and GET .../config shows only the current
// value, so the queued one comes from GET .../pending. Every read error is
// returned: an empty answer here would read as "nothing to fix".
func (m *StorageManager) VMDisks(ctx context.Context, instanceID string) (*VMDisks, error) {
	vmid, err := strconv.Atoi(instanceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidVMID, instanceID)
	}

	node, err := m.client.getNode(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve node for VM %d: %w", vmid, err)
	}

	config, err := m.client.getQemuService().Config(ctx, node, vmid)
	if err != nil {
		return nil, fmt.Errorf("read VM %d config: %w", vmid, err)
	}

	pending, err := m.pendingValues(ctx, node, vmid)
	if err != nil {
		return nil, err
	}

	digest, _ := config["digest"].(string)

	disks := &VMDisks{Node: node, VMID: vmid, Digest: digest}

	for key, raw := range config {
		value, ok := raw.(string)
		if !ok || !hasAnyPrefix(key, diskBusPrefixes) {
			continue
		}

		disk, isVolume, err := m.describeDisk(ctx, key, value)
		if err != nil {
			return nil, fmt.Errorf("VM %d %s: %w", vmid, key, err)
		}

		if !isVolume {
			continue
		}

		disk.PendingValue = pending[key]
		disks.Disks = append(disks.Disks, disk)
	}

	sort.Slice(disks.Disks, func(i, j int) bool { return disks.Disks[i].Slot < disks.Disks[j].Slot })

	return disks, nil
}

// describeDisk builds the view of one slot. It reports false for a slot that
// is not a storage volume: a cdrom (including the cloud-init drive) or a
// device path passed through from the host.
func (m *StorageManager) describeDisk(ctx context.Context, slot, value string) (VMDiskSlot, bool, error) {
	parsed := parseDiskSlotValue(value)

	if media, _ := parsed.Option("media"); media == "cdrom" {
		return VMDiskSlot{}, false, nil
	}

	pool := parsed.Pool()
	if pool == "" {
		return VMDiskSlot{}, false, nil
	}

	poolType, err := m.storageType(ctx, pool)
	if err != nil {
		return VMDiskSlot{}, false, err
	}

	discard, _ := parsed.Option(discardOption)

	return VMDiskSlot{
		Slot:         slot,
		Value:        value,
		VolumeID:     parsed.VolumeID,
		Pool:         pool,
		PoolType:     poolType,
		Provisioning: classifyStorageProvisioning(poolType, volumeFormatOf(parsed)),
		Discard:      discard,
	}, true, nil
}

// pendingValues reads GET /nodes/{node}/qemu/{vmid}/pending and returns the
// queued value of each key that has one.
//
// The endpoint returns a list of {key, value, pending?, delete?}. A queued
// change shows up as a "pending" field holding the new value; an entry without
// one is just the current value repeated.
func (m *StorageManager) pendingValues(ctx context.Context, node string, vmid int) (map[string]string, error) {
	resp, err := m.client.pveClient.GetCtx(ctx, buildPVEPathf(node, "qemu/%d/pending", vmid), nil)
	if err != nil {
		return nil, fmt.Errorf("read VM %d pending changes: %w", vmid, err)
	}

	entries, ok := resp.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: VM %d pending changes: %T", ErrUnexpectedResponseType, vmid, resp)
	}

	pending := make(map[string]string)

	for _, raw := range entries {
		entry, isMap := raw.(map[string]interface{})
		if !isMap {
			continue
		}

		key, _ := entry["key"].(string)
		value, hasPending := entry["pending"].(string)

		if key == "" || !hasPending || strings.TrimSpace(value) == "" {
			continue
		}

		pending[key] = value
	}

	return pending, nil
}
