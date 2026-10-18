package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// Errors returned by `ocfp artifacts disks`.
var (
	ErrArtifactsDiskDrift        = errors.New("managed data disks on thin storage lack discard=on")
	ErrArtifactsDiskCheckFailed  = errors.New("could not check every managed disk")
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
type vmDiskReader interface {
	VMDisks(ctx context.Context, instanceID string) (*pve.VMDisks, error)
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
	Pool         string `json:"pool"`
	PoolType     string `json:"pool_type"`
	Provisioning string `json:"provisioning"`
	Discard      string `json:"discard"`
	Pending      string `json:"pending,omitempty"`
	Verdict      string `json:"verdict"`
}

// artifactsDiskReport is the whole result of `ocfp artifacts disks`.
type artifactsDiskReport struct {
	Bloc   string             `json:"bloc"`
	Disks  []artifactsDiskRow `json:"disks"`
	Notes  []string           `json:"notes,omitempty"`
	Errors []string           `json:"errors,omitempty"`
	Drift  int                `json:"drift"`
	Remedy string             `json:"remedy,omitempty"`
}

// artifactsDisks implements `ocfp artifacts disks`. It reads the disks of the
// bloc's managed VMs and reports, for each root and data disk, whether a thin
// pool gets freed blocks back. It fails when a managed data disk on thin
// storage lacks discard=on, or when a VM's disks could not be read.
func artifactsDisks(ac *artifactsContext, out io.Writer, asJSON bool) error {
	reader, ok := ac.provider.StorageManager().(vmDiskReader)
	if !ok {
		return ErrArtifactsDisksUnsupported
	}

	vms := managedDiskVMs(ac)
	if len(vms) == 0 {
		return fmt.Errorf("%w: %s", ErrArtifactsNoManagedVMs, ac.blocName)
	}

	report := inspectManagedDisks(ac.parent, reader, ac.blocName, vms)

	if report.Drift > 0 {
		report.Remedy = artifactsDiskRemedy(report.Drift)
	}

	err := renderArtifactsDiskReport(out, report, asJSON)
	if err != nil {
		return err
	}

	switch {
	case len(report.Errors) > 0:
		return fmt.Errorf("%w: %d error(s)", ErrArtifactsDiskCheckFailed, len(report.Errors))
	case report.Drift > 0:
		return fmt.Errorf("%w: %d disk(s)", ErrArtifactsDiskDrift, report.Drift)
	default:
		return nil
	}
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
// whose disks cannot be read is recorded as an error and the rest are still
// checked.
func inspectManagedDisks(ctx context.Context, reader vmDiskReader, blocName string, vms []managedDiskVM) artifactsDiskReport {
	report := artifactsDiskReport{Bloc: blocName, Disks: []artifactsDiskRow{}}

	for _, vm := range vms {
		disks, err := reader.VMDisks(ctx, vm.ID)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s (%s): reading disks: %v", vm.Name, vm.ID, err))

			continue
		}

		if root, found := disks.Slot(rootDiskSlot); found {
			report.Disks = append(report.Disks, artifactsDiskRowFor(vm, root, diskRoleRoot))
		}

		data, found := disks.DataDisk(vm.DataVolumeID, vm.DataSerial)
		if !found || data.Slot == rootDiskSlot {
			report.Notes = append(report.Notes, fmt.Sprintf(
				"%s (%s): no data disk found (looked for volume %q or serial %q)",
				vm.Name, vm.ID, vm.DataVolumeID, vm.DataSerial))

			continue
		}

		row := artifactsDiskRowFor(vm, data, diskRoleData)
		if row.Verdict == verdictMissingOnThin {
			report.Drift++
		}

		report.Disks = append(report.Disks, row)
	}

	return report
}

// artifactsDiskRowFor builds the report row for one disk.
func artifactsDiskRowFor(vm managedDiskVM, disk pve.VMDiskSlot, role string) artifactsDiskRow {
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
		Pool:         disk.Pool,
		PoolType:     disk.PoolType,
		Provisioning: string(disk.Provisioning),
		Discard:      discard,
		Pending:      disk.PendingValue,
		Verdict:      diskDiscardVerdict(disk),
	}
}

// diskDiscardVerdict says whether a disk's pool gets freed blocks back.
// discard=on matters only on thin storage. A change queued on a running VM
// counts as pending-restart, because it does nothing until a cold start.
func diskDiscardVerdict(disk pve.VMDiskSlot) string {
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

// artifactsDiskRemedy tells the operator how to fix drifted disks by hand.
func artifactsDiskRemedy(drift int) string {
	return fmt.Sprintf(
		"%d managed data disk(s) on thin storage lack discard=on, so the pool never gets their freed blocks back. "+
			"Add discard=on to each disk's config, keeping its other options, then cold stop and start the VM. "+
			"The disk's data is untouched.", drift)
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

	for _, note := range report.Notes {
		_, _ = fmt.Fprintf(out, "note: %s\n", note)
	}

	for _, e := range report.Errors {
		_, _ = fmt.Fprintf(out, "error: %s\n", e)
	}

	if report.Remedy != "" {
		_, _ = fmt.Fprintf(out, "\n%s\n", report.Remedy)
	}

	return nil
}

// Compile-time check that the PVE storage manager offers the disk view.
var _ vmDiskReader = (*pve.StorageManager)(nil)

// Compile-time check that the disk view is reached through cpi.StorageManager.
var _ cpi.StorageManager = (*pve.StorageManager)(nil)
