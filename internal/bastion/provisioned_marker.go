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
	// a full re-provision the first time they are touched.
	LegacyProvisionedMarkerPath = "${HOME}/.ocfp/provisioned"
)

// provisionedMarkerCheckCommand reports the provisioning date from whichever
// marker exists, preferring the OS-disk one.
func provisionedMarkerCheckCommand() string {
	return fmt.Sprintf(
		"if [ -f '%s' ]; then cat '%s'; elif [ -f \"%s\" ]; then cat \"%s\"; else exit 1; fi",
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
