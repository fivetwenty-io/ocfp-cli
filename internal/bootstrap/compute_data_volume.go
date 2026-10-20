package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// bastionDataSlot is the PVE disk slot the bastion's data disk is pinned to.
//
// Pinning matters more than it looks. Left to PVE's next-free arithmetic the
// slot is correct today only because a cloned template happens to occupy
// scsi0, and the guest-side /dev/sdN letter that follows from it is correct
// only because nothing else is attached. Naming the slot makes the first half
// deterministic, and the disk serial makes the second half irrelevant.
const bastionDataSlot = "scsi1"

// bastionDataAttachSpec is the device hint handed to AttachVolume: the pinned
// slot plus the disk properties that ride along on the config value.
//
// discard=on lets the thin pool reclaim freed blocks. serial= is what gives
// the guest a stable /dev/disk/by-id path, which is how the boot-time unit
// finds the disk without trusting /dev/sdb.
func bastionDataAttachSpec() string {
	return fmt.Sprintf("%s,discard=on,serial=%s", bastionDataSlot, config.BastionDataDiskSerial)
}

// bastionDataVolumeName is the state-side name of the bastion's data volume.
// PVE rewrites the provider-side name into vm-<vmid>-data regardless, so this
// is the handle we look it up by, not what the storage pool calls it.
func (m *Manager) bastionDataVolumeName() string {
	return m.options.BlocName + "-bastion-data"
}

// EnsureBastionDataVolume creates and attaches the bastion's persistent data
// disk, or re-attaches the existing one.
//
// This is a convergence pass rather than a create, and it is a separate
// bootstrap step rather than a tail on CreateBastion for one reason:
// CreateBastion returns early the moment a bastion already exists, which is
// the ordinary case on every re-run and on the rebuild flow this disk exists
// to serve. Anything bolted onto the end of that function would never run
// when the disk most needs re-attaching.
func (m *Manager) EnsureBastionDataVolume(ctx context.Context) error {
	data := &m.config.Bastion.Data
	if !data.Enabled {
		logger.Debugf("Bastion data disk disabled; skipping")

		return nil
	}

	// Deliberately NOT gated on provider.SupportsStorage(). On PVE that
	// reports whether the configured blobstore is external — an object-store
	// capability with nothing to do with block volumes — so gating on it made
	// every bloc in the default local blobstore mode skip its data disk while
	// reporting the step as completed.
	storage := m.provider.StorageManager()
	if storage == nil {
		logger.Debugf("Provider %s has no storage manager; skipping bastion data disk", m.options.Provider)

		return nil
	}

	bastionName := m.options.BlocName + "-bastion"

	instanceID, ok := m.bastionInstanceID(bastionName)
	if !ok {
		logger.Debugf("No bastion recorded for %s; skipping data disk", bastionName)

		return nil
	}

	volume, err := m.resolveBastionDataVolume(ctx, instanceID)
	if err != nil {
		return err
	}

	created := false

	if volume == nil {
		created = true

		volume, err = m.createBastionDataVolume(ctx, instanceID)
		if err != nil && (volume == nil || volume.ID == "") {
			return err
		}
	}

	createErr := err

	// Record before attachment: a lost response can mean the disk is already
	// attached. Preserve both its identity and contents for a safe retry.
	if err := m.recordBastionDataVolume(volume, instanceID); err != nil {
		return fmt.Errorf("record bastion data volume: %w", err)
	}

	if err := m.stateManager.Save(); err != nil {
		return fmt.Errorf("save bastion data volume: %w", err)
	}

	if createErr != nil {
		return createErr
	}

	err = storage.AttachVolume(ctx, volume.ID, instanceID, m.bastionDataDevice())
	if err != nil {
		return fmt.Errorf("attach bastion data volume %s (preserved for retry): %w", volume.ID, err)
	}

	if created {
		_, _ = fmt.Fprintf(os.Stdout, "    • Created bastion data disk %s (%d GiB)\n",
			volume.ID, data.DiskSizeGiB)
	} else {
		_, _ = fmt.Fprintf(os.Stdout, "    • Bastion data disk %s attached\n", volume.ID)
	}

	return nil
}

// bastionInstanceID returns the VMID recorded for the bastion, if any.
func (m *Manager) bastionInstanceID(bastionName string) (string, bool) {
	res, _ := m.stateManager.GetResource(state.ResourceTypeInstance, bastionName)
	if res == nil || res.ID == "" {
		return "", false
	}

	return res.ID, true
}

// resolveBastionDataVolume finds the existing data volume, preferring the
// state record and falling back to asking the provider.
//
// The fallback mirrors bastionAlreadyExists: state is the fast path, but it
// drifts, and a volume the provider knows about must be adopted rather than
// duplicated. Creating a second data disk beside a first would leave the
// operator's state stranded on a disk nothing mounts.
//
// A nil volume without error means absent. Failed or ambiguous discovery
// aborts before allocation so existing data cannot be stranded.
func (m *Manager) resolveBastionDataVolume(ctx context.Context, instanceID string) (*cpi.Volume, error) {
	name := m.bastionDataVolumeName()

	if res, _ := m.stateManager.GetResource(state.ResourceTypeVolume, name); res != nil && res.ID != "" {
		logger.Debugf("Bastion data volume %s found in state as %s", name, res.ID)

		return &cpi.Volume{
			ID:   res.ID,
			Name: name,
			Size: m.config.Bastion.Data.DiskSizeGiB,
			Type: m.config.Bastion.Data.StoragePool,
		}, nil
	}

	volumes, err := m.provider.StorageManager().ListVolumes(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("discover bastion data volume: %w", err)
	}

	var match *cpi.Volume

	for _, vol := range volumes {
		if vol == nil {
			continue
		}

		if isBastionDataVolume(vol, instanceID, name) {
			if p := strings.ToLower(m.options.Provider); p == "aws" || p == "gcp" {
				if vol.Tags["ocfp:bloc"] != m.options.BlocName || vol.Tags["ocfp:role"] != "bastion-data" {
					return nil, fmt.Errorf("data volume %s matches name but lacks exact bloc/role ownership; refusing adoption", vol.ID)
				}
			}

			if match != nil && match.ID != vol.ID {
				return nil, fmt.Errorf("multiple bastion data volumes match %s; refusing to select one", name)
			}

			match = vol
		}
	}

	return match, nil
}

// isBastionDataVolume reports whether a provider volume is this bastion's data
// disk. PVE names it vm-<vmid>-data, so the owning VMID plus the -data suffix
// identifies it; the state-side name is matched too for providers that keep
// the descriptive name.
func isBastionDataVolume(vol *cpi.Volume, instanceID, stateName string) bool {
	if vol.Name == stateName {
		return true
	}

	return vol.Name == fmt.Sprintf("vm-%s-data", instanceID)
}

// createBastionDataVolume allocates the disk, owned by the bastion so pools
// that reject unowned volumes (PVE's lvm-thin and zfs among them) accept it.
func (m *Manager) createBastionDataVolume(ctx context.Context, instanceID string) (*cpi.Volume, error) {
	data := &m.config.Bastion.Data

	vol, err := m.provider.StorageManager().CreateVolume(ctx, &cpi.VolumeRequest{
		Name:       m.bastionDataVolumeName(),
		SizeGB:     data.DiskSizeGiB,
		Type:       data.StoragePool,
		InstanceID: instanceID,
		Tags: map[string]string{
			"ocfp:role": "bastion-data",
			"ocfp:bloc": m.options.BlocName,
		},
	})
	if err != nil {
		return vol, fmt.Errorf("create bastion data volume: %w", err)
	}

	return vol, nil
}

// recordBastionDataVolume writes the state entry the rebuild and teardown
// paths read. This record is how a later run knows which volid to hand over
// to a replacement VM and which one teardown must spare.
func (m *Manager) recordBastionDataVolume(vol *cpi.Volume, instanceID string) error {
	data := &m.config.Bastion.Data

	return m.stateManager.AddResource(&state.Resource{
		ID:       vol.ID,
		Type:     state.ResourceTypeVolume,
		Name:     m.bastionDataVolumeName(),
		Provider: m.options.Provider,
		State:    "active",
		Properties: map[string]interface{}{
			"role":          "bastion-data",
			"vm_id":         instanceID,
			"size_gib":      data.DiskSizeGiB,
			"storage_pool":  strings.TrimSpace(data.StoragePool),
			"filesystem":    data.Filesystem,
			"mountpoint":    data.Mountpoint,
			"device_serial": config.BastionDataDiskSerial,
			"slot":          m.bastionDataDevice(),
			"preserve":      true,
		},
	})
}

// bastionDataDiskSpec builds the spec the provider delivers to the guest via
// SMBIOS, describing how to prepare the data disk and what to restore off it.
//
// Returns nil when the feature is off, so the guest script no-ops rather than
// guessing at a device.
func (m *Manager) bastionDataDiskSpec() *cpi.DataDiskSpec {
	data := &m.config.Bastion.Data
	if !data.Enabled {
		return nil
	}

	user := m.bastionDefaultUsername()
	if user == "" {
		user = "ubuntu"
	}

	return &cpi.DataDiskSpec{
		Serial:     config.BastionDataDiskSerial,
		Mountpoint: data.Mountpoint,
		Filesystem: data.Filesystem,
		HomeDir:    "/home/" + user,
		User:       user,
		// The bootstrap public key, re-asserted on every boot. Without it a
		// replacement bastion would trust only the authorized_keys the
		// persistent home brought over from the machine it replaced.
		AuthorizedKey: strings.TrimSpace(m.bastionPublicKey()),
	}
}

// bastionDataDevice maps the persistent data disk to the provider API's device syntax.
func (m *Manager) bastionDataDevice() string {
	switch strings.ToLower(m.options.Provider) {
	case "aws":
		return "/dev/sdf"
	case "gcp":
		return "ocfp-bastion-data"
	default:
		return bastionDataAttachSpec()
	}
}
