// Package recycle replaces a VM's operating system while carrying its
// persistent data disk across to the replacement.
//
// The operation exists because a data disk that no command can carry across a
// rebuild is not persistence. It is written as an explicit state machine with
// a journal rather than as a straight-line script, because the case that will
// actually happen is an interrupted run, and an operator staring at a
// half-finished cluster needs a command that can tell them where they are.
package recycle

import (
	"errors"
	"fmt"
)

// Phase is one step of a recycle, recorded in bloc state before the next one
// begins.
type Phase string

// The phases, in order.
//
// The ordering carries the safety property. The replacement is built while the
// original is merely stopped, so the handover moves the disk straight from one
// live VM config to another and it never belongs to nothing. Proxmox enforces
// the same shape from its side: it refuses to reassign a disk out of a running
// VM, and it refuses to land one in an occupied slot.
const (
	// PhasePlanned records the intent. Nothing on the cluster has changed.
	PhasePlanned Phase = "planned"

	// PhaseQuiesced stops the original. The outage window starts here, and
	// Proxmox requires it before a disk can be reassigned.
	PhaseQuiesced Phase = "quiesced"

	// PhaseSecured takes our own snapshot and, when asked, an archive.
	PhaseSecured Phase = "secured"

	// PhaseBuilt creates the replacement under a transient name, stopped.
	//
	// The transient name is not cosmetic: bastion discovery matches on name,
	// and teardown deliberately refuses to act when several guests share one,
	// so two live guests with the final name would break both for as long as
	// the window lasted.
	PhaseBuilt Phase = "built"

	// PhaseHandedOver moves the data disk to the replacement.
	PhaseHandedOver Phase = "handed-over"

	// PhaseRetired destroys the original. This is the point of no return: its
	// boot disk and its snapshots go with it.
	PhaseRetired Phase = "retired"

	// PhaseAdopted renames the replacement, rewrites the state entry, starts it.
	PhaseAdopted Phase = "adopted"

	// PhaseProvisioned runs the role's provisioning.
	PhaseProvisioned Phase = "provisioned"

	// PhaseVerified runs the role's health check and clears the journal.
	PhaseVerified Phase = "verified"
)

// ErrPastPointOfNoReturn is returned by AbortPlan once the original VM has
// been destroyed and there is nothing left to walk back to.
var ErrPastPointOfNoReturn = errors.New(
	"the original VM has been destroyed; its boot disk and snapshots went with it. " +
		"Restore the archive taken during the secured phase instead")

// PhaseOrder returns the phases in execution order.
func PhaseOrder() []Phase {
	return []Phase{
		PhasePlanned,
		PhaseQuiesced,
		PhaseSecured,
		PhaseBuilt,
		PhaseHandedOver,
		PhaseRetired,
		PhaseAdopted,
		PhaseProvisioned,
		PhaseVerified,
	}
}

// phaseIndex returns a phase's position, or -1 when it is not a known phase.
func phaseIndex(p Phase) int {
	for i, candidate := range PhaseOrder() {
		if candidate == p {
			return i
		}
	}

	return -1
}

// NextPhase returns the phase that follows, or false at the end of the run or
// for an unrecognised phase.
func NextPhase(p Phase) (Phase, bool) {
	idx := phaseIndex(p)
	if idx < 0 {
		return "", false
	}

	order := PhaseOrder()
	if idx+1 >= len(order) {
		return "", false
	}

	return order[idx+1], true
}

// CanAbort reports whether the run can still be walked back.
//
// Everything up to and including the handover is reversible: the disk goes
// back and the replacement is deleted. Once the original is destroyed nothing
// can undo it, and the command must say so rather than attempt a recovery it
// cannot finish.
func CanAbort(p Phase) bool {
	idx := phaseIndex(p)
	if idx < 0 {
		return false
	}

	return idx < phaseIndex(PhaseRetired)
}

// Compensation is the action that undoes a phase.
type Compensation string

// The compensations.
const (
	// CompensationNone means the phase left nothing to undo. A snapshot is
	// deliberately left behind rather than deleted on abort.
	CompensationNone Compensation = "none"

	// CompensationStartOriginal restarts the VM the quiesce stopped.
	CompensationStartOriginal Compensation = "start-original"

	// CompensationDeleteReplacement removes the replacement that was built.
	CompensationDeleteReplacement Compensation = "delete-replacement"

	// CompensationReturnDisk moves the data disk back to the original.
	CompensationReturnDisk Compensation = "return-disk"

	// CompensationImpossible marks a phase past the point of no return.
	CompensationImpossible Compensation = "impossible"
)

// CompensationFor returns the action that undoes a phase.
func CompensationFor(p Phase) Compensation {
	switch p {
	case PhaseQuiesced:
		return CompensationStartOriginal
	case PhaseBuilt:
		return CompensationDeleteReplacement
	case PhaseHandedOver:
		return CompensationReturnDisk
	case PhaseRetired, PhaseAdopted, PhaseProvisioned, PhaseVerified:
		return CompensationImpossible
	case PhasePlanned, PhaseSecured:
		return CompensationNone
	default:
		return CompensationNone
	}
}

// AbortPlan returns the compensations to run, in reverse order, to undo a run
// that reached the given phase.
//
// Reverse order is correctness rather than symmetry: the disk has to be
// returned while the replacement still exists to move it off, and the original
// is only restarted once nothing else needs it stopped.
func AbortPlan(reached Phase) ([]Compensation, error) {
	if !CanAbort(reached) {
		return nil, fmt.Errorf("%w (phase %q)", ErrPastPointOfNoReturn, reached)
	}

	idx := phaseIndex(reached)
	order := PhaseOrder()

	var plan []Compensation

	for i := idx; i >= 0; i-- {
		comp := CompensationFor(order[i])
		if comp == CompensationNone {
			continue
		}

		plan = append(plan, comp)
	}

	return plan, nil
}

// DiskOwner says which VM currently holds the data disk.
type DiskOwner string

// The possible owners.
const (
	DiskOnOriginal    DiskOwner = "original"
	DiskOnReplacement DiskOwner = "replacement"

	// DiskOrphaned means the disk belongs to no guest. Only the
	// detach-and-re-attach fallback can produce this; the reassignment path
	// moves the disk between two live VM configs.
	DiskOrphaned DiskOwner = "orphaned"
)

// Observation is what the cluster looks like right now.
//
// These three facts are enough to place an interrupted run unambiguously,
// which is what lets recovery work when the journal is gone.
type Observation struct {
	OriginalExists    bool
	ReplacementExists bool
	DiskOwner         DiskOwner
}

// ReconstructPhase infers how far a run got from the cluster alone.
//
// The journal is the fast path, not the only one. A workstation can lose its
// state file, or never have written one, and the operator still needs the
// command to work out where it is rather than guess.
func ReconstructPhase(obs Observation) (Phase, bool) {
	switch {
	case obs.OriginalExists && !obs.ReplacementExists:
		// Nothing irreversible has happened. Treat it as secured so a resume
		// re-takes the safety copy rather than assuming one exists.
		return PhaseSecured, true

	case obs.OriginalExists && obs.ReplacementExists && obs.DiskOwner == DiskOnReplacement:
		return PhaseHandedOver, true

	case obs.OriginalExists && obs.ReplacementExists:
		return PhaseBuilt, true

	case !obs.OriginalExists && obs.ReplacementExists:
		// Covers both the clean case and the one where the detach fallback
		// left the disk orphaned; the engine re-attaches it by id and
		// continues from here.
		return PhaseRetired, true

	default:
		// Neither VM exists. There is nothing to resume, and if a volume
		// survived the operator has to name it explicitly.
		return "", false
	}
}
