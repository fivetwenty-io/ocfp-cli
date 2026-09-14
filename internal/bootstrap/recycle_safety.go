package bootstrap

import "errors"

// ErrNoSafetyCopy is returned when a recycle would run with nothing protecting
// the OS disk it is about to destroy.
var ErrNoSafetyCopy = errors.New(
	"a recycle destroys the original VM's boot disk. Name a backup storage with --archive-storage, " +
		"or pass --safety-copy-taken to confirm you already have one")

// recycleSafetyCopyRationale explains why the safety copy is an archive rather
// than a snapshot. It is returned as text, and asserted on by a test, so the
// reasoning cannot quietly rot out of the code.
func recycleSafetyCopyRationale() string {
	return `A recycle's safety copy is a vzdump archive, never a VM snapshot.

A snapshot pins every disk in the VM's config, and Proxmox then refuses to move
or detach any of them: "unable to delete '<volid>' - volume is still in use
(snapshot?)". A recycle that snapshots the VM before handing its data disk over
therefore deadlocks itself, because the safety step blocks the very operation it
was meant to protect. This was observed on a live PVE 9 cluster.

An archive pins nothing. With the data disk marked backup=0 it captures exactly
the half that is about to be thrown away, the OS disk, and it survives the VM
being destroyed, which a snapshot does not. The data disk needs no copy of its
own: it is not destroyed, it is carried across.`
}

// checkSafetyCopyPlan refuses a recycle that would leave the OS disk
// unprotected.
//
// The operator can say they already have a copy, which is the common case on a
// lab where an archive was taken by hand, but silence is not consent: with no
// storage named and no acknowledgement, there is nothing to restore from once
// the original is destroyed.
func checkSafetyCopyPlan(archiveStorage string, safetyCopyTaken bool) error {
	if archiveStorage != "" || safetyCopyTaken {
		return nil
	}

	return ErrNoSafetyCopy
}
