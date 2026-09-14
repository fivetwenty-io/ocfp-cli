package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/recycle"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// recycleJournalResourceType is the state resource type holding a recycle's
// progress. It lives in the bloc's own state file so an operator who has the
// state file has the recovery map.
const recycleJournalResourceType = "recycle_journal"

// replacementSuffix is appended to a VM's name while its replacement is being
// built.
//
// The transient name is not cosmetic. Bastion discovery matches on name, and
// teardown deliberately refuses to act when several guests share one, so two
// live guests carrying the final name would break both for as long as the
// window lasted.
const replacementSuffix = "-next"

// Errors returned by the recycle adapter.
var (
	ErrRecycleNoBastion    = errors.New("no bastion recorded for this bloc; nothing to recycle")
	ErrRecycleNoDataVolume = errors.New("no preserved data volume recorded; the disk to carry across is unknown")
	ErrRecycleImageMissing = errors.New(
		"the configured image is not present on the cluster. Build it first with `ocfp pve template provision <name>`")
	ErrArchiveUnsupported = errors.New(
		"this provider cannot take an archive from the CLI; take one yourself before continuing")
)

// Archiver is the optional capability of taking a backup that survives the VM
// being destroyed.
//
// It is optional because a snapshot is universal and an archive is not: on
// Proxmox it is a vzdump to a backup-capable storage, which has no equivalent
// on every provider. A provider that cannot do it says so rather than
// pretending the safety copy exists.
type Archiver interface {
	ArchiveInstance(ctx context.Context, instanceID, storage string) error
}

// BastionRecycleCluster adapts the bootstrap manager to recycle.Cluster.
//
// It lives here rather than in the recycle package so it can reuse the bastion
// build path that already knows how to resolve the image, the flavor, the
// subnet, the static address, and the SMBIOS payload. Duplicating that in a
// second place is how the replacement quietly stops matching what a fresh
// bootstrap produces.
type BastionRecycleCluster struct {
	mgr *Manager

	// archiveStorage names the backup storage for the safety copy.
	archiveStorage string

	// safetyCopyTaken records that the operator already has a copy, for the
	// case where they took one by hand.
	safetyCopyTaken bool
}

// NewBastionRecycleCluster returns a recycle.Cluster for this bloc's bastion.
func NewBastionRecycleCluster(mgr *Manager, archiveStorage string) *BastionRecycleCluster {
	return &BastionRecycleCluster{mgr: mgr, archiveStorage: archiveStorage}
}

// WithSafetyCopyTaken records that the operator already holds a copy of the OS
// disk, so the recycle need not take one.
func (c *BastionRecycleCluster) WithSafetyCopyTaken(taken bool) *BastionRecycleCluster {
	c.safetyCopyTaken = taken

	return c
}

func (c *BastionRecycleCluster) finalName() string {
	return c.mgr.options.BlocName + "-bastion"
}

func (c *BastionRecycleCluster) transientName() string {
	return c.finalName() + replacementSuffix
}

// findByName returns the VMID of a guest with the given name, if one exists.
func (c *BastionRecycleCluster) findByName(ctx context.Context, name string) (string, bool) {
	instances, err := c.mgr.provider.ComputeManager().ListInstances(ctx, nil)
	if err != nil {
		logger.Warnf("Could not list instances while observing the cluster: %v", err)

		return "", false
	}

	for _, inst := range instances {
		if inst != nil && inst.Name == name {
			return inst.ID, true
		}
	}

	return "", false
}

// dataVolumeID returns the volume this recycle carries across.
func (c *BastionRecycleCluster) dataVolumeID() (string, error) {
	res, _ := c.mgr.stateManager.GetResource(state.ResourceTypeVolume, c.mgr.bastionDataVolumeName())
	if res == nil || res.ID == "" {
		return "", ErrRecycleNoDataVolume
	}

	return res.ID, nil
}

// Observe reports the three facts that place an interrupted run.
func (c *BastionRecycleCluster) Observe(ctx context.Context) (recycle.Observation, error) {
	originalID, originalExists := c.findByName(ctx, c.finalName())
	replacementID, replacementExists := c.findByName(ctx, c.transientName())

	obs := recycle.Observation{
		OriginalExists:    originalExists,
		ReplacementExists: replacementExists,
		DiskOwner:         recycle.DiskOrphaned,
	}

	if _, owned := c.dataVolumeOwnedBy(ctx, replacementID); owned && replacementExists {
		obs.DiskOwner = recycle.DiskOnReplacement

		return obs, nil
	}

	if _, owned := c.dataVolumeOwnedBy(ctx, originalID); owned && originalExists {
		obs.DiskOwner = recycle.DiskOnOriginal
	}

	return obs, nil
}

// dataVolumeOwnedBy returns the data volume currently owned by the given
// instance, if any.
//
// Ownership is resolved from the provider rather than from the recorded volume
// id, because a Proxmox reassignment RENAMES the volume to match its new
// owner: vm-100-data becomes vm-137-data. Tracking the disk by its id across a
// handover would therefore lose it exactly when it matters most. The
// authoritative link is the owner the provider reports on each volume.
func (c *BastionRecycleCluster) dataVolumeOwnedBy(ctx context.Context, instanceID string) (string, bool) {
	if instanceID == "" {
		return "", false
	}

	vols, err := c.mgr.provider.StorageManager().ListVolumes(ctx, nil)
	if err != nil {
		logger.Warnf("Could not list volumes to locate the data disk: %v", err)

		return "", false
	}

	// The recorded id is consulted first because a handover renames the disk.
	// Ask for it rather than requiring it: a bloc whose disk has never been
	// recorded still has to be recognisable.
	recorded, _ := c.dataVolumeID()

	for _, v := range vols {
		if v.AttachedTo != instanceID {
			continue
		}

		if isPreservedDataVolume(v, recorded) {
			return v.ID, true
		}
	}

	return "", false
}

// isPreservedDataVolume reports whether a provider volume is the disk this
// recycle carries across.
//
// The disk starts life named after the bloc, so a "-data" suffix finds it.
// Proxmox's move_disk then renames it after its new owner, and the disk
// becomes "vm-102-disk-1": no suffix, and nothing about the name says what it
// is any more. That broke both halves of a resumed run, because Observe could
// no longer see who held the disk and the handover fell back to a recorded id
// naming a volume that no longer existed.
//
// So the recorded id decides when we have one, and the suffix stays as the
// fallback for a disk this bloc has not recorded yet.
func isPreservedDataVolume(vol *cpi.Volume, recordedID string) bool {
	if vol == nil {
		return false
	}

	if recordedID != "" {
		return vol.ID == recordedID || vol.Name == recordedID
	}

	return strings.HasSuffix(vol.Name, "-data") || strings.HasSuffix(vol.ID, "-data")
}

// StopOriginal stops the VM being replaced. Proxmox requires this before a
// disk can be reassigned out of it.
func (c *BastionRecycleCluster) StopOriginal(ctx context.Context) error {
	id, ok := c.findByName(ctx, c.finalName())
	if !ok {
		return ErrRecycleNoBastion
	}

	_, _ = fmt.Fprintf(os.Stdout, "    • Stopping %s (%s)\n", c.finalName(), id)

	return c.mgr.provider.ComputeManager().StopInstance(ctx, id)
}

// StartOriginal restarts the VM the quiesce stopped. This is a compensation,
// run only on abort.
func (c *BastionRecycleCluster) StartOriginal(ctx context.Context) error {
	id, ok := c.findByName(ctx, c.finalName())
	if !ok {
		return nil
	}

	return c.mgr.provider.ComputeManager().StartInstance(ctx, id)
}

// Snapshot takes the recycle's safety copy.
//
// It deliberately does NOT take a VM snapshot, despite the name the engine
// gives the phase. A snapshot pins every disk in the VM's config, and Proxmox
// then refuses to move or detach any of them, so snapshotting here would
// deadlock the handover two phases later. recycleSafetyCopyRationale explains
// it at length, and a test pins the reasoning.
//
// The copy that works is a vzdump archive: it pins nothing, it survives the VM
// being destroyed, and with the data disk marked backup=0 it captures exactly
// the half that is about to be thrown away.
func (c *BastionRecycleCluster) Snapshot(ctx context.Context) error {
	err := checkSafetyCopyPlan(c.archiveStorage, c.safetyCopyTaken)
	if err != nil {
		return err
	}

	if c.archiveStorage == "" {
		_, _ = fmt.Fprintln(os.Stdout,
			"    • Skipping the safety copy; you confirmed one already exists.")

		return nil
	}

	return c.Archive(ctx)
}

// Archive takes a copy that survives the VM being destroyed.
func (c *BastionRecycleCluster) Archive(ctx context.Context) error {
	archiver, ok := c.mgr.provider.ComputeManager().(Archiver)
	if !ok {
		return ErrArchiveUnsupported
	}

	id, found := c.findByName(ctx, c.finalName())
	if !found {
		return ErrRecycleNoBastion
	}

	_, _ = fmt.Fprintf(os.Stdout, "    • Archiving %s to %s (this takes a while)\n", c.finalName(), c.archiveStorage)

	return archiver.ArchiveInstance(ctx, id, c.archiveStorage)
}

// BuildReplacement creates the replacement under the transient name, stopped.
//
// It goes through the same bastion build path a fresh bootstrap uses, so the
// replacement matches what a new bloc would get rather than drifting from it.
func (c *BastionRecycleCluster) BuildReplacement(ctx context.Context) error {
	if _, exists := c.findByName(ctx, c.transientName()); exists {
		logger.Infof("Replacement %s already exists; not rebuilding", c.transientName())

		return nil
	}

	// Refuse rather than trigger a multi-gigabyte download and a
	// serial-console build with the bastion already stopped.
	if _, err := c.mgr.resolveImageID(ctx, c.mgr.config.Bastion.Image); err != nil {
		return fmt.Errorf("%w: %s", ErrRecycleImageMissing, c.mgr.config.Bastion.Image)
	}

	networkID, subnetInfo, err := c.mgr.resolveBastionNetworking()
	if err != nil {
		return fmt.Errorf("resolve networking: %w", err)
	}

	sgID, err := c.mgr.getBastionSecurityGroup()
	if err != nil {
		return fmt.Errorf("resolve security group: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "    • Building replacement %s (stopped)\n", c.transientName())

	inst, err := c.mgr.createBastionInstanceStopped(ctx, c.transientName(), networkID, subnetInfo, sgID)
	if err != nil {
		return fmt.Errorf("build replacement: %w", err)
	}

	logger.Infof("Replacement bastion %s created as %s", c.transientName(), inst.ID)

	return nil
}

// DeleteReplacement removes the replacement. This is a compensation.
func (c *BastionRecycleCluster) DeleteReplacement(ctx context.Context) error {
	id, ok := c.findByName(ctx, c.transientName())
	if !ok {
		return nil
	}

	return c.mgr.provider.ComputeManager().DeleteInstance(ctx, id)
}

// HandOverDisk moves the data disk from the original to the replacement.
func (c *BastionRecycleCluster) HandOverDisk(ctx context.Context) error {
	// Safe to re-enter. The journal records a phase before it runs, so a
	// resume lands here whether the handover finished or failed, and moving a
	// disk that has already moved is an error rather than a no-op.
	toID, ok := c.findByName(ctx, c.transientName())
	if ok {
		volID, owned := c.dataVolumeOwnedBy(ctx, toID)
		if owned {
			_, _ = fmt.Fprintf(os.Stdout,
				"    • %s already holds %s; nothing to hand over\n", c.transientName(), volID)

			return c.recordVolumeOwner(volID, toID)
		}
	}

	return c.moveDisk(ctx, c.finalName(), c.transientName())
}

// ReturnDisk moves the data disk back. This is a compensation.
func (c *BastionRecycleCluster) ReturnDisk(ctx context.Context) error {
	return c.moveDisk(ctx, c.transientName(), c.finalName())
}

// moveDisk reassigns the data volume between two named guests, falling back to
// detach-and-re-attach when the provider cannot reassign.
func (c *BastionRecycleCluster) moveDisk(ctx context.Context, fromName, toName string) error {
	fromID, ok := c.findByName(ctx, fromName)
	if !ok {
		return fmt.Errorf("%w: %s", ErrRecycleNoBastion, fromName)
	}

	toID, ok := c.findByName(ctx, toName)
	if !ok {
		return fmt.Errorf("%w: %s", ErrRecycleNoBastion, toName)
	}

	// Resolve the disk from who currently owns it, falling back to the
	// recorded id. A previous handover may already have renamed it.
	volID, found := c.dataVolumeOwnedBy(ctx, fromID)
	if !found {
		recorded, err := c.dataVolumeID()
		if err != nil {
			return err
		}

		volID = recorded
	}

	_, _ = fmt.Fprintf(os.Stdout, "    • Handing %s from %s to %s\n", volID, fromName, toName)

	reassigner, ok := c.mgr.provider.StorageManager().(interface {
		ReassignVolume(ctx context.Context, volumeID, sourceInstanceID, targetInstanceID, targetSlot string) error
	})
	if ok {
		err := reassigner.ReassignVolume(ctx, volID, fromID, toID, bastionDataSlot)
		if err == nil {
			// Proxmox renamed the volume to match its new owner, so read the
			// new id back rather than recording the one we sent.
			newID, found := c.dataVolumeOwnedBy(ctx, toID)
			if !found {
				newID = volID
			}

			return c.recordVolumeOwner(newID, toID)
		}

		logger.Warnf("Reassignment failed (%v); falling back to detach and re-attach", err)
	}

	// Fallback. This leaves the volume owned by nothing for the length of the
	// re-attach, which is why it is not the default.
	err := c.mgr.provider.StorageManager().DetachVolume(ctx, volID, fromID)
	if err != nil {
		return fmt.Errorf("detach %s from %s: %w", volID, fromName, err)
	}

	err = c.mgr.provider.StorageManager().AttachVolume(ctx, volID, toID, bastionDataAttachSpec())
	if err != nil {
		return fmt.Errorf("re-attach %s to %s: %w", volID, toName, err)
	}

	return c.recordVolumeOwner(volID, toID)
}

// recordVolumeOwner updates the state record so a later run and any teardown
// know which guest holds the disk.
func (c *BastionRecycleCluster) recordVolumeOwner(volumeID, instanceID string) error {
	res, _ := c.mgr.stateManager.GetResource(state.ResourceTypeVolume, c.mgr.bastionDataVolumeName())
	if res == nil {
		return nil
	}

	if res.Properties == nil {
		res.Properties = map[string]interface{}{}
	}

	res.Properties["vm_id"] = instanceID
	res.ID = volumeID

	err := c.mgr.stateManager.AddResource(res)
	if err != nil {
		return err
	}

	return c.mgr.stateManager.Save()
}

// RetireOriginal destroys the VM being replaced. Past this, nothing can be
// undone.
func (c *BastionRecycleCluster) RetireOriginal(ctx context.Context) error {
	id, ok := c.findByName(ctx, c.finalName())
	if !ok {
		return nil
	}

	_, _ = fmt.Fprintf(os.Stdout, "    • Destroying the original %s (%s)\n", c.finalName(), id)

	return c.mgr.provider.ComputeManager().DeleteInstance(ctx, id)
}

// AdoptReplacement renames the replacement, rewrites the state entry, and
// starts it.
func (c *BastionRecycleCluster) AdoptReplacement(ctx context.Context) error {
	id, ok := c.findByName(ctx, c.transientName())
	if !ok {
		// Already adopted on an earlier attempt.
		return nil
	}

	renamer, ok := c.mgr.provider.ComputeManager().(interface {
		RenameInstance(ctx context.Context, instanceID, name string) error
	})
	if ok {
		err := renamer.RenameInstance(ctx, id, c.finalName())
		if err != nil {
			return fmt.Errorf("rename %s to %s: %w", c.transientName(), c.finalName(), err)
		}
	}

	// Rewrite the instance record. Leaving it stale aims the next teardown at
	// a VMID that no longer exists.
	err := c.mgr.stateManager.AddResource(&state.Resource{
		ID:       id,
		Type:     state.ResourceTypeInstance,
		Name:     c.finalName(),
		Provider: c.mgr.options.Provider,
		State:    "active",
		Properties: map[string]interface{}{
			"role":  "bastion",
			"image": c.mgr.config.Bastion.Image,
		},
	})
	if err != nil {
		return fmt.Errorf("record replacement in state: %w", err)
	}

	err = c.mgr.stateManager.Save()
	if err != nil {
		return fmt.Errorf("persist replacement in state: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "    • Adopted %s as %s; starting\n", id, c.finalName())

	return c.mgr.provider.ComputeManager().StartInstance(ctx, id)
}

// Provision runs the role's provisioning. The bastion's is `ocfp bastion
// init`, driven over SSH from the workstation, which the recycle command
// invokes separately so its long output is not buried inside a phase.
func (c *BastionRecycleCluster) Provision(_ context.Context) error {
	_, _ = fmt.Fprintln(os.Stdout,
		"    • Replacement is up. Run `ocfp bastion init --bloc "+c.mgr.options.BlocName+"` to finish provisioning.")

	return nil
}

// Verify checks the replacement is healthy enough to hand back.
func (c *BastionRecycleCluster) Verify(ctx context.Context) error {
	id, ok := c.findByName(ctx, c.finalName())
	if !ok {
		return ErrRecycleNoBastion
	}

	inst, err := c.mgr.provider.ComputeManager().GetInstance(ctx, id)
	if err != nil {
		return fmt.Errorf("verify %s: %w", c.finalName(), err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "    • %s is %s\n", c.finalName(), inst.State)

	return nil
}

// SaveJournal writes the recycle's progress into the bloc state file.
func (c *BastionRecycleCluster) SaveJournal(_ context.Context, j *recycle.Journal) error {
	blob, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("marshal journal: %w", err)
	}

	err = c.mgr.stateManager.AddResource(&state.Resource{
		ID:       c.finalName(),
		Type:     recycleJournalResourceType,
		Name:     c.finalName(),
		Provider: c.mgr.options.Provider,
		State:    string(j.Phase),
		Properties: map[string]interface{}{
			"journal": string(blob),
		},
	})
	if err != nil {
		return err
	}

	// Flush. AddResource only mutates the in-memory state, and a journal that
	// never reaches disk is no journal at all: the next invocation reads
	// nothing, concludes the run never started, and begins again from the top.
	return c.mgr.stateManager.Save()
}

// LoadJournal reads a recycle in progress, or nil when there is none.
func (c *BastionRecycleCluster) LoadJournal(_ context.Context) (*recycle.Journal, error) {
	res, _ := c.mgr.stateManager.GetResource(recycleJournalResourceType, c.finalName())
	if res == nil {
		return nil, nil //nolint:nilnil // no journal is a normal state, not an error
	}

	blob, _ := res.Properties["journal"].(string)
	if blob == "" {
		return nil, nil //nolint:nilnil // an empty record is the same as none
	}

	var j recycle.Journal
	if err := json.Unmarshal([]byte(blob), &j); err != nil {
		return nil, fmt.Errorf("parse journal: %w", err)
	}

	return &j, nil
}

// ClearJournal removes the progress record after a successful run.
func (c *BastionRecycleCluster) ClearJournal(_ context.Context) error {
	err := c.mgr.stateManager.RemoveResource(recycleJournalResourceType, c.finalName())
	if err != nil {
		return err
	}

	return c.mgr.stateManager.Save()
}

// compile-time check that the adapter satisfies the engine's contract.
var _ recycle.Cluster = (*BastionRecycleCluster)(nil)
