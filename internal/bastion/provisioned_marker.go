package bastion

import "fmt"

const (
	// ProvisionedMarkerPath records that `ocfp bastion init` completed.
	//
	// It lives on the OS disk, not in the home directory, and that placement
	// is load-bearing rather than cosmetic. The bastion's home directory is
	// bind-mounted off a persistent data disk so operator state survives an
	// OS replacement; a marker kept there would survive too, onto a freshly
	// rebuilt machine with no brew, no binaries, and no genesis, and
	// isAlreadyProvisioned would short-circuit the entire provisioning run
	// on the strength of it.
	//
	// The rule generalises: the persistent disk carries data, never claims
	// about the state of the OS. /var/lib/ocfp already holds the firstboot
	// sentinel for the same reason.
	ProvisionedMarkerPath = "/var/lib/ocfp/provisioned"

	// LegacyProvisionedMarkerPath is where the marker used to live. It is
	// still read so bastions provisioned before the move are not put through
	// a full re-provision the first time they are touched, but only when the
	// home directory is this machine's own rather than a persisted one.
	LegacyProvisionedMarkerPath = "${HOME}/.ocfp/provisioned"
)

// provisionedMarkerCheckCommand reports the provisioning date from whichever
// marker exists, preferring the OS-disk one.
//
// The legacy marker is read only when the home directory is not a mount point.
// Moving the marker onto the OS disk was meant to stop a claim about the state
// of the OS riding across a rebuild on the data disk, and reading the old
// home-directory marker unconditionally put that back exactly as it was. It
// cost us the lab's first cycle onto Resolute: the new bastion came up with no
// brew, no binaries, and no genesis, and init read a marker dated three weeks
// earlier off the persisted home and reported the bastion already fully
// provisioned.
//
// A bind-mounted home is the tell. When the home directory is a mount point,
// whatever marker sits in it came from the data disk rather than from this
// installation, and it says nothing about the operating system now running.
func provisionedMarkerCheckCommand() string {
	return fmt.Sprintf(
		"if [ -f '%s' ]; then cat '%s'; "+
			"elif ! findmnt -n \"${HOME}\" >/dev/null 2>&1 && [ -f \"%s\" ]; then cat \"%s\"; "+
			"else exit 1; fi",
		ProvisionedMarkerPath, ProvisionedMarkerPath,
		LegacyProvisionedMarkerPath, LegacyProvisionedMarkerPath,
	)
}

// provisionedMarkerWriteCommand writes the marker on the OS disk.
//
// The mkdir is required: /var/lib/ocfp is created by the firstboot unit, which
// a bastion cloned from an older template or built on another provider may
// never have run.
func provisionedMarkerWriteCommand() string {
	return fmt.Sprintf(
		"sudo mkdir -p /var/lib/ocfp && date | sudo tee '%s' >/dev/null",
		ProvisionedMarkerPath,
	)
}
