package pve

import (
	"context"
	"testing"
)

// wrotePut reports whether any recorded PUT targeted path and carried key set
// to want. Tests use it to assert a config write happened without pinning the
// order of the other writes around it.
func (f *fakePVEClient) wrotePut(path, key string, want interface{}) bool {
	for _, call := range f.putParams {
		if call.Path != path {
			continue
		}

		if got, ok := call.Params[key]; ok && got == want {
			return true
		}
	}

	return false
}

// TestDetachVolumeClearsProtectionFirst covers the other operation PVE gates
// on the protection flag.
//
// A detach is two drive removals: `delete: scsiN` moves the volume into an
// unusedN slot, and a second `delete: unusedN` clears that. Both are refused
// on a protected guest ("can't remove drive", "can't remove unused disk"), so
// with protection left on, teardown's detach of the preserved data disk would
// fail and the disk would then be purged along with the guest — the exact
// loss the preserve path exists to prevent.
func TestDetachVolumeClearsProtectionFirst(t *testing.T) {
	t.Parallel()

	const (
		volID  = "local-lvm:vm-20000-data"
		config = "/nodes/pve1/qemu/20000/config"
	)

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			config: map[string]interface{}{
				"scsi1": volID + ",serial=ocfpdata",
			},
		},
	}

	manager := &StorageManager{client: &Client{
		config:    &Config{Node: "pve1", DefaultStorage: "local-lvm"},
		pveClient: fake,
	}}

	err := manager.DetachVolume(context.Background(), volID, "20000")
	if err != nil {
		t.Fatalf("DetachVolume: %v", err)
	}

	if !fake.wrotePut(config, pveKeyProtection, 0) {
		t.Errorf("detach must clear protection first, PUTs: %v", fake.putParams)
	}

	if !fake.wrotePut(config, "delete", "scsi1") {
		t.Errorf("detach must remove the drive from its slot, PUTs: %v", fake.putParams)
	}
}
