package opsfiles_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/pve/opsfiles"
)

// T29d TestClassifyStorageType_SharedTypes verifies shared backend classification
// for all documented shared PVE storage types.
func TestClassifyStorageType_SharedTypes(t *testing.T) {
	t.Parallel()

	sharedTypes := []string{"rbd", "cephfs", "nfs", "cifs", "glusterfs", "pbs"}

	for _, storageType := range sharedTypes {
		storageType := storageType // capture for parallel sub-tests
		t.Run(storageType, func(t *testing.T) {
			t.Parallel()
			got := opsfiles.ClassifyStorageType(storageType)
			if got != opsfiles.StorageShared {
				t.Errorf("ClassifyStorageType(%q) = %q, want %q", storageType, got, opsfiles.StorageShared)
			}
		})
	}
}

// T29e TestClassifyStorageType_LocalTypes verifies local backend classification
// for all documented local PVE storage types.
func TestClassifyStorageType_LocalTypes(t *testing.T) {
	t.Parallel()

	localTypes := []string{"lvm", "lvmthin", "zfspool", "dir", "btrfs"}

	for _, storageType := range localTypes {
		storageType := storageType // capture for parallel sub-tests
		t.Run(storageType, func(t *testing.T) {
			t.Parallel()
			got := opsfiles.ClassifyStorageType(storageType)
			if got != opsfiles.StorageLocal {
				t.Errorf("ClassifyStorageType(%q) = %q, want %q", storageType, got, opsfiles.StorageLocal)
			}
		})
	}
}

// TestClassifyStorageType_UnknownType verifies unknown types default to StorageLocal
// (conservative: forces BOSH disk pinning, safe on any install).
func TestClassifyStorageType_UnknownType(t *testing.T) {
	t.Parallel()

	got := opsfiles.ClassifyStorageType("something-unknown")
	if got != opsfiles.StorageLocal {
		t.Errorf("ClassifyStorageType(%q) = %q, want %q (conservative default)", "something-unknown", got, opsfiles.StorageLocal)
	}
}

// TestClassifyStorageType_CaseInsensitive verifies type matching is case-insensitive.
func TestClassifyStorageType_CaseInsensitive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  opsfiles.StorageClass
	}{
		{"RBD", opsfiles.StorageShared},
		{"NFS", opsfiles.StorageShared},
		{"LvmThin", opsfiles.StorageLocal},
		{"ZFSPOOL", opsfiles.StorageLocal},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := opsfiles.ClassifyStorageType(tc.input)
			if got != tc.want {
				t.Errorf("ClassifyStorageType(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// T29f TestRequiresRawDiskFormat_Zfspool verifies zfspool requires raw disk format.
func TestRequiresRawDiskFormat_Zfspool(t *testing.T) {
	t.Parallel()

	if !opsfiles.RequiresRawDiskFormat("zfspool") {
		t.Error("RequiresRawDiskFormat(\"zfspool\") = false, want true: zfspool block devices do not support qcow2")
	}
}

// TestRequiresRawDiskFormat_LvmTypes verifies lvm and lvmthin require raw disk format.
func TestRequiresRawDiskFormat_LvmTypes(t *testing.T) {
	t.Parallel()

	rawTypes := []string{"lvm", "lvmthin"}

	for _, storageType := range rawTypes {
		storageType := storageType
		t.Run(storageType, func(t *testing.T) {
			t.Parallel()
			if !opsfiles.RequiresRawDiskFormat(storageType) {
				t.Errorf("RequiresRawDiskFormat(%q) = false, want true: block devices require raw format", storageType)
			}
		})
	}
}

// TestRequiresRawDiskFormat_SharedTypes verifies shared backends do not require raw.
func TestRequiresRawDiskFormat_SharedTypes(t *testing.T) {
	t.Parallel()

	qcow2Types := []string{"rbd", "nfs", "cifs", "cephfs", "glusterfs", "pbs", "dir"}

	for _, storageType := range qcow2Types {
		storageType := storageType
		t.Run(storageType, func(t *testing.T) {
			t.Parallel()
			if opsfiles.RequiresRawDiskFormat(storageType) {
				t.Errorf("RequiresRawDiskFormat(%q) = true, want false: type supports qcow2", storageType)
			}
		})
	}
}
