package pve

import (
	"context"
	"encoding/json"
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
	// Value is the slot's value as GET .../config returns it, with any queued
	// change already applied. A write builds on it, so queued changes to the
	// slot's other options survive.
	Value string
	// LiveValue is the value the running VM uses, which the pending list
	// reports. It equals Value unless a change to the slot is queued, and it
	// is empty for a disk whose attachment is itself queued.
	LiveValue string
	// VolumeID is the storage:volume part of Value.
	VolumeID string
	// Pool is the storage pool named in VolumeID.
	Pool string
	// PoolType is the pool's plugin type, such as lvmthin or nfs. It is empty
	// until ClassifyDisk fills it in.
	PoolType string
	// Provisioning says whether the pool hands freed blocks back. It is empty
	// until ClassifyDisk fills it in.
	Provisioning StorageProvisioning
	// Discard is the discard option of LiveValue, or empty when it has none.
	Discard string
	// PendingValue is the value queued for the slot on the next cold start,
	// or empty when nothing is queued.
	PendingValue string
	// PendingDelete reports that the slot's removal is queued. GET .../config
	// already leaves such a slot out, so Value and LiveValue both hold the
	// live value from the pending list.
	PendingDelete bool
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

// VMDisks reads a VM's attached disks with any change queued for each. It
// does not look up pool types; ClassifyDisk does that for the disks that
// matter.
//
// The config read alone is not enough. On a running VM a disk change is
// queued until the next cold start, and GET .../config returns the config
// with that queued change already applied. What the running VM uses comes
// from GET .../pending, whose "value" is the live value and whose "pending"
// is the queued one. Every read error is returned: an empty answer here would
// read as "nothing to fix".
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

		live := value
		if entry, queued := pending[key]; queued {
			live = entry.Value
		}

		disk, isVolume := describeDisk(key, value, live)
		if !isVolume {
			continue
		}

		disk.PendingValue = pending[key].Pending
		disk.PendingDelete = pending[key].Delete
		disks.Disks = append(disks.Disks, disk)
	}

	// A slot whose removal is queued is already gone from the merged config,
	// so the pending list is the only place it shows up.
	for key, entry := range pending {
		if _, inConfig := config[key]; inConfig || !entry.Delete || !hasAnyPrefix(key, diskBusPrefixes) {
			continue
		}

		disk, isVolume := describeDisk(key, entry.Value, entry.Value)
		if !isVolume {
			continue
		}

		disk.PendingDelete = true
		disks.Disks = append(disks.Disks, disk)
	}

	sort.Slice(disks.Disks, func(i, j int) bool { return disks.Disks[i].Slot < disks.Disks[j].Slot })

	return disks, nil
}

// describeDisk builds the view of one slot from its merged config value and
// its live value. It reports false for a slot that is not a storage volume: a
// cdrom (including the cloud-init drive) or a device path passed through from
// the host.
func describeDisk(slot, value, live string) (VMDiskSlot, bool) {
	parsed := parseDiskSlotValue(value)

	if media, _ := parsed.Option("media"); media == "cdrom" {
		return VMDiskSlot{}, false
	}

	pool := parsed.Pool()
	if pool == "" {
		return VMDiskSlot{}, false
	}

	discard, _ := parseDiskSlotValue(live).Option(discardOption)

	return VMDiskSlot{
		Slot:      slot,
		Value:     value,
		LiveValue: live,
		VolumeID:  parsed.VolumeID,
		Pool:      pool,
		Discard:   discard,
	}, true
}

// pendingChange is one key's queued change from the pending list.
type pendingChange struct {
	// Value is the live value, or empty when the key itself is new.
	Value string
	// Pending is the queued value, or empty for a queued delete.
	Pending string
	// Delete reports that the key's removal is queued.
	Delete bool
}

// pendingValues reads GET /nodes/{node}/qemu/{vmid}/pending and returns the
// live and queued value of each key that has a change or a delete queued.
//
// The endpoint returns a list of {key, value, pending?, delete?}, where
// "value" is the live value and "pending" the queued one, and a "delete" that
// is present and not 0 marks a queued removal (2 is a forced one). An entry
// with neither is just the live value, which GET .../config already shows.
func (m *StorageManager) pendingValues(ctx context.Context, node string, vmid int) (map[string]pendingChange, error) {
	resp, err := m.client.pveClient.GetCtx(ctx, buildPVEPathf(node, "qemu/%d/pending", vmid), nil)
	if err != nil {
		return nil, fmt.Errorf("read VM %d pending changes: %w", vmid, err)
	}

	entries, ok := resp.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: VM %d pending changes: %T", ErrUnexpectedResponseType, vmid, resp)
	}

	pending := make(map[string]pendingChange)

	for _, raw := range entries {
		entry, isMap := raw.(map[string]interface{})
		if !isMap {
			continue
		}

		key, _ := entry["key"].(string)
		queued, _ := entry["pending"].(string)
		deleting := pendingDeleteFlag(entry["delete"])

		if key == "" || (strings.TrimSpace(queued) == "" && !deleting) {
			continue
		}

		live, _ := entry["value"].(string)
		pending[key] = pendingChange{Value: live, Pending: queued, Delete: deleting}
	}

	return pending, nil
}

// EnableDiskDiscard sets discard=on on one disk slot and returns the value it
// wrote. A value that already carries discard=on is returned without a write.
//
// currentValue and digest must come from the same config read, normally a
// VMDisks call. The new value is PUT together with that digest, so PVE
// refuses the write if the config changed in between rather than letting us
// overwrite a value we never saw. Every other option on the slot is kept
// exactly, because PVE rewrites the whole slot from what we send.
//
// It never stops, starts, or reboots the VM. On a running VM PVE queues the
// change, and it takes effect only after a cold stop and start.
func (m *StorageManager) EnableDiskDiscard(ctx context.Context, instanceID, slot, currentValue, digest string) (string, error) {
	vmid, err := strconv.Atoi(instanceID)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidVMID, instanceID)
	}

	if !hasAnyPrefix(slot, diskBusPrefixes) {
		return "", fmt.Errorf("%w: %s is not an attached disk slot", ErrInvalidDiskSlotValue, slot)
	}

	want, err := discardOnValue(currentValue)
	if err != nil {
		return "", fmt.Errorf("VM %d %s: %w", vmid, slot, err)
	}

	if want == currentValue {
		return currentValue, nil
	}

	if digest == "" {
		return "", fmt.Errorf("%w: VM %d %s", ErrConfigDigestMissing, vmid, slot)
	}

	node, err := m.client.getNode(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve node for VM %d: %w", vmid, err)
	}

	_, err = m.client.pveClient.PutCtx(ctx,
		buildPVEPathf(node, "qemu/%d/config", vmid),
		map[string]interface{}{slot: want, "digest": digest})
	if err != nil {
		return "", fmt.Errorf("set discard=on on VM %d %s: %w", vmid, slot, err)
	}

	return want, nil
}

// pendingDeleteFlag reads the pending list's "delete" field, which is an
// integer that is 1 or 2 for a queued removal. JSON decoding hands it over as
// a float64, but a string or a json.Number is read the same way.
func pendingDeleteFlag(raw interface{}) bool {
	switch v := raw.(type) {
	case float64:
		return v != 0
	case int:
		return v != 0
	case json.Number:
		return v.String() != "0"
	case string:
		return v != "" && v != "0"
	default:
		return false
	}
}

// ClassifyDisk returns the disk with its pool's plugin type and provisioning
// class filled in. Each pool is looked up once per StorageManager.
//
// VMDisks leaves this out so a caller pays for, and can fail on, only the
// disks it cares about. A failed lookup returns the disk as unknown together
// with the error, and the caller decides whether that disk matters.
func (m *StorageManager) ClassifyDisk(ctx context.Context, disk VMDiskSlot) (VMDiskSlot, error) {
	poolType, err := m.storageType(ctx, disk.Pool)
	if err != nil {
		disk.PoolType = ""
		disk.Provisioning = ProvisioningUnknown

		return disk, fmt.Errorf("read type of pool %s: %w", disk.Pool, err)
	}

	disk.PoolType = poolType
	disk.Provisioning = classifyStorageProvisioning(poolType, volumeFormatOf(parseDiskSlotValue(disk.Value)))

	return disk, nil
}
