package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// Errors returned by `ocfp artifacts disks`.
var (
	ErrArtifactsDiskDrift        = errors.New("managed data disks on thin storage lack discard=on")
	ErrArtifactsDiskCheckFailed  = errors.New("could not check every managed disk")
	ErrArtifactsDiskFixFailed    = errors.New("setting discard=on failed on managed data disks")
	ErrArtifactsDisksUnsupported = errors.New("the disk check needs a provider that reports VM disk config (pve)")
	ErrArtifactsNoManagedVMs     = errors.New("no artifacts VM or bastion recorded for bloc")
)

// Verdicts for one disk. Only a managed data disk with verdictMissingOnThin
// fails the check.
const (
	verdictOK             = "ok"
	verdictMissingOnThin  = "missing-on-thin"
	verdictThickNotNeeded = "thick-not-needed"
	verdictUnknownPool    = "unknown-pool"
	verdictPendingRestart = "pending-restart"
	verdictPendingDelete  = "pending-delete"
)

// What the re-read after a write showed.
const (
	fixStateApplied     = "applied"
	fixStatePending     = "pending-restart"
	fixStateUnconfirmed = "unconfirmed"
)

// Disk roles. Root disks are shown for information and never fail the check.
const (
	diskRoleRoot = "root"
	diskRoleData = "data"
)

// rootDiskSlot is where the template puts every managed VM's boot disk.
const rootDiskSlot = "scsi0"

// vmDiskReader is the optional disk view a provider's storage manager offers.
// PVE implements it; the cpi.StorageManager interface stays unchanged.
// VMDisks lists a VM's disks, and ClassifyDisk looks up the pool type of one
// of them, so only the root and data disks cost a lookup.
type vmDiskReader interface {
	VMDisks(ctx context.Context, instanceID string) (*pve.VMDisks, error)
	ClassifyDisk(ctx context.Context, disk pve.VMDiskSlot) (pve.VMDiskSlot, error)
}

// vmDiskDiscardWriter is the optional write a provider's storage manager
// offers to set discard=on on one disk slot under the config digest.
type vmDiskDiscardWriter interface {
	EnableDiskDiscard(ctx context.Context, node, instanceID, slot, currentValue, digest string) (string, error)
}

// managedDiskVM is a VM ocfp owns, with what identifies its data disk.
type managedDiskVM struct {
	Name         string
	ID           string
	DataVolumeID string
	DataSerial   string
}

// artifactsDiskRow is one disk in the report.
type artifactsDiskRow struct {
	VM           string `json:"vm"`
	VMID         string `json:"vm_id"`
	Slot         string `json:"slot"`
	Role         string `json:"role"`
	Volume       string `json:"volume"`
	Value        string `json:"value"`
	LiveValue    string `json:"live_value"`
	Pool         string `json:"pool"`
	PoolType     string `json:"pool_type"`
	Provisioning string `json:"provisioning"`
	Discard      string `json:"discard"`
	Pending      string `json:"pending,omitempty"`
	Verdict      string `json:"verdict"`
	Fixed        bool   `json:"fixed,omitempty"`
	FixState     string `json:"fix_state,omitempty"`
	NewValue     string `json:"new_value,omitempty"`
	Error        string `json:"error,omitempty"`

	// digest is the config digest Value was read under. A fix sends it back
	// so PVE refuses the write if the config changed in between.
	digest string
	// node is the PVE node the config was read on, where a fix writes it.
	node string
	// confirmErr is why the re-read after a fix failed, when it did.
	confirmErr string
}

// artifactsDiskReport is the whole result of `ocfp artifacts disks`.
type artifactsDiskReport struct {
	Bloc      string             `json:"bloc"`
	Disks     []artifactsDiskRow `json:"disks"`
	Errors    []string           `json:"errors,omitempty"`
	Drift     int                `json:"drift"`
	Fixed     int                `json:"fixed"`
	FixFailed int                `json:"fix_failed"`
	Remedy    string             `json:"remedy,omitempty"`
	NextSteps []string           `json:"next_steps,omitempty"`
}

// artifactsDisks implements `ocfp artifacts disks`. It reads the disks of the
// bloc's managed VMs and reports, for each root and data disk, whether a thin
// pool gets freed blocks back. It fails when a managed data disk on thin
// storage lacks discard=on, or when a VM's disks could not be read. A disk
// whose discard=on is queued for the next cold start reads as
// pending-restart, which is not drift and exits 0.
//
// With fix, each managed data disk that is missing-on-thin gets discard=on
// written into its config. Root disks and every other verdict are left
// alone, and no VM is stopped, started, or rebooted. A refused write fails
// the command even when other disks were fixed in the same run.
func artifactsDisks(ac *artifactsContext, out io.Writer, fix, asJSON bool) error {
	reader, ok := ac.provider.StorageManager().(vmDiskReader)
	if !ok {
		return ErrArtifactsDisksUnsupported
	}

	var writer vmDiskDiscardWriter

	if fix {
		writer, ok = ac.provider.StorageManager().(vmDiskDiscardWriter)
		if !ok {
			return ErrArtifactsDisksUnsupported
		}
	}

	vms := managedDiskVMs(ac)
	if len(vms) == 0 {
		return fmt.Errorf("%w: %s", ErrArtifactsNoManagedVMs, ac.blocName)
	}

	report := inspectManagedDisks(ac.parent, reader, ac.blocName, vms)

	if fix {
		applyDiscardFixes(ac.parent, reader, writer, &report)
	}

	if report.Fixed > 0 {
		report.NextSteps = artifactsDiskNextSteps(ac.blocName, report.Disks)
	}

	if report.Drift > 0 {
		report.Remedy = artifactsDiskRemedy(ac.blocName, report.Drift, fix)
	}

	err := renderArtifactsDiskReport(out, report, asJSON)
	if err != nil {
		return err
	}

	switch {
	case report.FixFailed > 0:
		return fmt.Errorf("%w: %d disk(s)", ErrArtifactsDiskFixFailed, report.FixFailed)
	case len(report.Errors) > 0:
		return fmt.Errorf("%w: %d error(s)", ErrArtifactsDiskCheckFailed, len(report.Errors))
	case report.Drift > 0:
		return fmt.Errorf("%w: %d disk(s)", ErrArtifactsDiskDrift, report.Drift)
	default:
		return nil
	}
}

// artifactsDiskWarning returns the one-line warning `artifacts status` shows
// when a managed data disk on thin storage lacks discard=on, or has it only
// queued for the next cold start. It is best effort: a provider with no disk
// view or a failed read is logged at debug level and yields no warning, so it
// never fails the status command.
func artifactsDiskWarning(ac *artifactsContext) string {
	reader, ok := ac.provider.StorageManager().(vmDiskReader)
	if !ok {
		logger.Debugf("artifacts status: provider has no disk view; skipping the discard check")

		return ""
	}

	report := inspectManagedDisks(ac.parent, reader, ac.blocName, managedDiskVMs(ac))

	for _, e := range report.Errors {
		logger.Debugf("artifacts status: discard check: %s", e)
	}

	var missing, pending []string

	for _, d := range report.Disks {
		if d.Role != diskRoleData {
			continue
		}

		switch d.Verdict {
		case verdictMissingOnThin:
			missing = append(missing, d.VM+" "+d.Slot)
		case verdictPendingRestart:
			pending = append(pending, d.VM+" "+d.Slot)
		}
	}

	if len(missing) == 0 && len(pending) == 0 {
		return ""
	}

	var parts []string

	if len(missing) > 0 {
		parts = append(parts, strings.Join(missing, ", ")+" on thin storage lack discard=on")
	}

	if len(pending) > 0 {
		parts = append(parts, strings.Join(pending, ", ")+" have discard=on pending until a cold stop and start")
	}

	return fmt.Sprintf("data disk(s) %s; run `ocfp artifacts disks --bloc %s` for details",
		strings.Join(parts, ", and "), ac.blocName)
}

// managedDiskVMs resolves the bloc's managed VMs from state: the artifacts VM
// through its lookup, and the bastion through its data volume record. The
// recorded volid identifies each data disk, because the legacy disks this
// check exists for carry no serial; the serial is the fallback.
func managedDiskVMs(ac *artifactsContext) []managedDiskVM {
	var vms []managedDiskVM

	if ac.lookup != nil && ac.lookup.VMID != "" {
		vms = append(vms, managedDiskVM{
			Name:         ac.lookup.Name,
			ID:           ac.lookup.VMID,
			DataVolumeID: ac.lookup.DataVolumeID,
			DataSerial:   config.ArtifactsDataDiskSerial,
		})
	}

	if bastion, found := bastionDiskVM(ac.state, ac.blocName); found {
		vms = append(vms, bastion)
	}

	return vms
}

// bastionDiskVM resolves the bastion from its <bloc>-bastion-data volume
// record, whose vm_id follows the disk across a recycle. The bastion's own
// instance record is the fallback for a bloc whose data disk was never
// recorded; its data disk is then found by serial alone.
func bastionDiskVM(sm *state.Manager, blocName string) (managedDiskVM, bool) {
	if sm == nil {
		return managedDiskVM{}, false
	}

	vm := managedDiskVM{
		Name:       blocName + "-bastion",
		DataSerial: config.BastionDataDiskSerial,
	}

	if vol, _ := sm.GetResource(state.ResourceTypeVolume, blocName+"-bastion-data"); vol != nil {
		vm.DataVolumeID = vol.ID

		if id, ok := vol.Properties["vm_id"].(string); ok {
			vm.ID = id
		}
	}

	if vm.ID == "" {
		if inst, _ := sm.GetResource(state.ResourceTypeInstance, vm.Name); inst != nil {
			vm.ID = inst.ID
		}
	}

	return vm, vm.ID != ""
}

// inspectManagedDisks reads each VM's disks and builds the report rows. A VM
// whose disks cannot be read, or whose data disk cannot be identified, is
// recorded as an error and the rest are still checked.
func inspectManagedDisks(ctx context.Context, reader vmDiskReader, blocName string, vms []managedDiskVM) artifactsDiskReport {
	report := artifactsDiskReport{Bloc: blocName, Disks: []artifactsDiskRow{}}

	for _, vm := range vms {
		disks, err := reader.VMDisks(ctx, vm.ID)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s (%s): reading disks: %v", vm.Name, vm.ID, err))

			continue
		}

		// A root disk is informational, so a pool type that cannot be read
		// shows as unknown-pool with the error and does not fail the check.
		if root, found := disks.Slot(rootDiskSlot); found {
			root, err := reader.ClassifyDisk(ctx, root)

			row := artifactsDiskRowFor(vm, root, diskRoleRoot, disks)
			if err != nil {
				row.Error = err.Error()
			}

			report.Disks = append(report.Disks, row)
		}

		// A data disk that cannot be identified fails the check: a stale or
		// empty volid in state on a disk with no serial would otherwise pass
		// a disk nobody looked at.
		data, found := disks.DataDisk(vm.DataVolumeID, vm.DataSerial)
		if !found || data.Slot == rootDiskSlot {
			report.Errors = append(report.Errors, fmt.Sprintf(
				"%s (%s): no data disk found (looked for volume %q, then serial %q); "+
					"check the volume recorded in state against the VM's config",
				vm.Name, vm.ID, vm.DataVolumeID, vm.DataSerial))

			continue
		}

		// The data disk's verdict is the point of the check, so a pool type
		// that cannot be read fails it.
		data, err = reader.ClassifyDisk(ctx, data)

		row := artifactsDiskRowFor(vm, data, diskRoleData, disks)
		if err != nil {
			row.Error = err.Error()
			report.Errors = append(report.Errors, fmt.Sprintf("%s (%s) %s: %v", vm.Name, vm.ID, data.Slot, err))
		}

		if row.Verdict == verdictMissingOnThin {
			report.Drift++
		}

		report.Disks = append(report.Disks, row)
	}

	return report
}

// artifactsDiskRowFor builds the report row for one disk.
func artifactsDiskRowFor(vm managedDiskVM, disk pve.VMDiskSlot, role string, disks *pve.VMDisks) artifactsDiskRow {
	discard := disk.Discard
	if discard == "" {
		discard = "absent"
	}

	if !disk.DiscardOn() && disk.PendingDiscardOn() {
		discard += " (on pending)"
	}

	return artifactsDiskRow{
		VM:           vm.Name,
		VMID:         vm.ID,
		Slot:         disk.Slot,
		Role:         role,
		Volume:       disk.VolumeID,
		Value:        disk.Value,
		LiveValue:    disk.LiveValue,
		Pool:         disk.Pool,
		PoolType:     disk.PoolType,
		Provisioning: string(disk.Provisioning),
		Discard:      discard,
		Pending:      disk.PendingValue,
		Verdict:      diskDiscardVerdict(disk),
		digest:       disks.Digest,
		node:         disks.Node,
	}
}

// diskDiscardVerdict says whether a disk's pool gets freed blocks back.
// discard=on matters only on thin storage. A change queued on a running VM
// counts as pending-restart, because it does nothing until a cold start. A
// slot whose removal is queued is pending-delete whatever its storage, and is
// never fixed: writing to a slot on its way out would fight that change.
func diskDiscardVerdict(disk pve.VMDiskSlot) string {
	if disk.PendingDelete {
		return verdictPendingDelete
	}

	switch disk.Provisioning {
	case pve.ProvisioningThin:
		switch {
		case disk.DiscardOn():
			return verdictOK
		case disk.PendingDiscardOn():
			return verdictPendingRestart
		default:
			return verdictMissingOnThin
		}
	case pve.ProvisioningThick:
		return verdictThickNotNeeded
	case pve.ProvisioningUnknown:
		return verdictUnknownPool
	default:
		return verdictUnknownPool
	}
}

// applyDiscardFixes writes discard=on into each managed data disk that is
// missing-on-thin, using the value, digest, and node it was read under. A
// refused write stays missing-on-thin and is recorded as an error.
//
// After a write the VM's disks are read again, because PVE applies the change
// at once on a stopped VM and queues it on a running one. That re-read is the
// same read the check makes, and it says which happened without asking for
// the VM's power state. A disk whose change is live reads as ok, a queued one
// as pending-restart, and one the re-read could not confirm as
// pending-restart too, since that is the safe assumption.
func applyDiscardFixes(ctx context.Context, reader vmDiskReader, writer vmDiskDiscardWriter, report *artifactsDiskReport) {
	for i := range report.Disks {
		row := &report.Disks[i]

		if row.Role != diskRoleData || row.Verdict != verdictMissingOnThin {
			continue
		}

		wrote, err := writer.EnableDiskDiscard(ctx, row.node, row.VMID, row.Slot, row.Value, row.digest)
		if err != nil {
			row.Error = err.Error()
			report.FixFailed++
			report.Errors = append(report.Errors, fmt.Sprintf(
				"%s (%s) %s: setting discard=on failed: %v", row.VM, row.VMID, row.Slot, err))

			continue
		}

		row.Fixed = true
		row.NewValue = wrote
		report.Fixed++
		report.Drift--

		confirmDiscardFix(ctx, reader, row, wrote)
	}
}

// confirmDiscardFix re-reads a fixed disk and records whether its change is
// live, queued for a cold start, or could not be confirmed.
func confirmDiscardFix(ctx context.Context, reader vmDiskReader, row *artifactsDiskRow, wrote string) {
	row.FixState = fixStateUnconfirmed

	disks, err := reader.VMDisks(ctx, row.VMID)
	if err != nil {
		row.confirmErr = err.Error()
	} else if disk, found := disks.Slot(row.Slot); !found {
		row.confirmErr = "the slot is no longer in the VM's config"
	} else {
		switch {
		case disk.DiscardOn():
			row.FixState = fixStateApplied
			row.Value, row.LiveValue, row.Pending = disk.Value, disk.LiveValue, ""
			row.Discard = disk.Discard
			row.Verdict = verdictOK

			return
		case disk.PendingDiscardOn():
			row.FixState = fixStatePending
			row.Value, row.LiveValue = disk.Value, disk.LiveValue
		default:
			row.confirmErr = "the re-read shows discard=on neither live nor pending"
		}
	}

	row.Pending = wrote
	row.Discard += " (on pending)"
	row.Verdict = verdictPendingRestart
}

// artifactsDiskNextSteps tells the operator what a fix leaves for them to do,
// which depends on whether each VM took the change at once or queued it.
func artifactsDiskNextSteps(blocName string, disks []artifactsDiskRow) []string {
	var applied, pending []string

	var steps []string

	for _, d := range disks {
		if !d.Fixed {
			continue
		}

		vm := fmt.Sprintf("%s (%s)", d.VM, d.VMID)

		switch d.FixState {
		case fixStateApplied:
			applied = append(applied, vm)
		case fixStatePending:
			pending = append(pending, vm)
		default:
			steps = append(steps, fmt.Sprintf("Re-reading %s after the write failed (%s), so the change "+
				"could not be confirmed. If %s is running, cold stop and start it; a guest reboot is not enough.",
				vm, d.confirmErr, vm))
		}
	}

	if len(pending) > 0 {
		steps = append(steps, fmt.Sprintf("The change takes effect only after a cold stop and start of %s. "+
			"A guest reboot is not enough, because the running VM keeps its old disk config.",
			strings.Join(pending, " and ")))
	}

	if len(applied) > 0 {
		steps = append(steps, fmt.Sprintf("The change is already live on %s, because the VM was not running "+
			"when it was written. No restart is needed.", strings.Join(applied, " and ")))
	}

	return append(steps,
		"The disk's data is untouched. Only the disk's config option changed.",
		fmt.Sprintf("The guest must also trim before the pool gets blocks back. "+
			"Re-running `ocfp artifacts provision --bloc %s` enables fstrim.timer, "+
			"or you can run `sudo fstrim -v` on the data mount once.", blocName),
	)
}

// artifactsDiskRemedy tells the operator how to fix the disks that still lack
// discard=on.
func artifactsDiskRemedy(blocName string, drift int, fixAttempted bool) string {
	if fixAttempted {
		return fmt.Sprintf(
			"%d managed data disk(s) on thin storage still lack discard=on because the write was refused. "+
				"Check the error above, then re-run `ocfp artifacts disks --bloc %s --fix`.", drift, blocName)
	}

	return fmt.Sprintf(
		"%d managed data disk(s) on thin storage lack discard=on, so the pool never gets their freed blocks back. "+
			"Run `ocfp artifacts disks --bloc %s --fix` to add discard=on, keeping every other disk option, "+
			"then cold stop and start each running VM it names. The disk's data is untouched.", drift, blocName)
}

// renderArtifactsDiskReport writes the report as JSON or as a table.
func renderArtifactsDiskReport(out io.Writer, report artifactsDiskReport, asJSON bool) error {
	if asJSON {
		enc, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("artifacts disks: marshaling json: %w", err)
		}

		_, err = fmt.Fprintln(out, string(enc))
		if err != nil {
			return fmt.Errorf("artifacts disks: writing report: %w", err)
		}

		return nil
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0) //nolint:mnd // column padding

	_, _ = fmt.Fprintln(tw, "VM\tVMID\tSLOT\tROLE\tPOOL\tTYPE\tDISCARD\tVERDICT")

	for _, d := range report.Disks {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			d.VM, d.VMID, d.Slot, d.Role, d.Pool, d.PoolType, d.Discard, d.Verdict)
	}

	err := tw.Flush()
	if err != nil {
		return fmt.Errorf("artifacts disks: writing report: %w", err)
	}

	for _, e := range report.Errors {
		_, _ = fmt.Fprintf(out, "error: %s\n", e)
	}

	if len(report.NextSteps) > 0 {
		_, _ = fmt.Fprintf(out, "\nSet discard=on on %d disk(s). What happens next:\n", report.Fixed)

		for _, step := range report.NextSteps {
			_, _ = fmt.Fprintf(out, "  - %s\n", step)
		}
	}

	if report.Remedy != "" {
		_, _ = fmt.Fprintf(out, "\n%s\n", report.Remedy)
	}

	return nil
}

// Compile-time checks that the PVE storage manager offers the disk view and
// the discard write.
var (
	_ vmDiskReader        = (*pve.StorageManager)(nil)
	_ vmDiskDiscardWriter = (*pve.StorageManager)(nil)
)

// Compile-time check that the disk view is reached through cpi.StorageManager.
var _ cpi.StorageManager = (*pve.StorageManager)(nil)
