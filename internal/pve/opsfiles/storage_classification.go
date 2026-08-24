package opsfiles

import "strings"

// StorageClass identifies the BOSH-CPI classification of a PVE storage backend.
type StorageClass string

const (
	// StorageShared identifies cluster-visible storage backends.
	// BOSH can place persistent disks on any node without pinning.
	// Backends: rbd, cephfs, nfs, cifs, glusterfs, pbs (or any with shared=1).
	StorageShared StorageClass = "shared"

	// StorageLocal identifies node-local storage backends.
	// BOSH must pin persistent disks to the VM's node.
	// Backends: lvm, lvmthin, zfspool, dir, btrfs.
	StorageLocal StorageClass = "local"
)

// ClassifyStorageType returns the BOSH-CPI classification (shared or local)
// for a PVE storage type string.
//
// Shared backends are cluster-visible: rbd, cephfs, nfs, cifs, glusterfs, pbs,
// or any storage with shared=1 reported by the PVE API. BOSH can migrate VMs
// without moving their persistent disks.
//
// Local backends are node-pinned: lvm, lvmthin, zfspool, dir, btrfs. BOSH
// must co-locate VM and persistent disk on the same node.
//
// NOTE: zfspool (and lvm/lvmthin) backends require disk_format: raw because
// block devices do not support qcow2. See RequiresRawDiskFormat.
//
// Unknown type strings return StorageLocal as the conservative default — it
// causes BOSH to pin disks, which is safe on any install. Callers should log
// a warning when an unknown type is returned.
func ClassifyStorageType(t string) StorageClass {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "rbd", "cephfs", "nfs", "cifs", "glusterfs", "pbs":
		return StorageShared
	default:
		// lvm, lvmthin, zfspool, dir, btrfs, and any unknown type are local.
		return StorageLocal
	}
}

// RequiresRawDiskFormat returns true for PVE storage types whose block devices
// require disk_format: raw. The bosh-proxmox-cpi-release rejects qcow2 for these
// backends because the underlying block device cannot hold a QEMU image file.
//
// Affected types: zfspool, lvm, lvmthin.
//
// All other types (dir, rbd, nfs, cifs, cephfs, glusterfs, pbs, and unknowns)
// return false — they support qcow2 or present a filesystem interface.
func RequiresRawDiskFormat(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "zfspool", "lvm", "lvmthin":
		return true
	default:
		return false
	}
}
