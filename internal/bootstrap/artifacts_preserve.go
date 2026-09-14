package bootstrap

import "github.com/ocfp/ocfp-cli-go/internal/state"

// artifactsDataVolumeResource builds the state record for the artifacts VM's
// data disk.
//
// The disk was previously recorded only as a data_volume_id property on the
// VM's own resource, which the teardown guard cannot see. That meant a
// teardown destroyed the disk along with the guest — and that disk holds every
// compiled release and cached stemcell for the bloc, which is the expensive
// thing to lose. The OS on the artifacts VM is 2.5 GiB of replaceable
// software; the data beside it is tens of gigabytes of compile output.
//
// Recording it as a preserved block volume in its own right is what lets the
// teardown path detach it before the purge, exactly as it does for the
// bastion's.
func artifactsDataVolumeResource(
	blocName, provider, vmName, instanceID, volumeID string,
	sizeGiB int,
	storagePool, filesystem, mountpoint string,
) *state.Resource {
	return &state.Resource{
		ID:       volumeID,
		Type:     state.ResourceTypeVolume,
		Name:     vmName + "-data",
		Provider: provider,
		State:    "active",
		Properties: map[string]interface{}{
			"role":         "artifacts-data",
			"bloc":         blocName,
			"vm_id":        instanceID,
			"size_gib":     sizeGiB,
			"storage_pool": storagePool,
			"filesystem":   filesystem,
			"mountpoint":   mountpoint,
			"preserve":     true,
		},
	}
}
