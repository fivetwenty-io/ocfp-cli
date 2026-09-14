package provision

// ProvisionedMarker records that provisioning completed, and it lives on the
// OS disk rather than in the operator's home directory.
//
// That placement is load-bearing. The bastion's home directory is bind-mounted
// off a persistent data disk so operator state survives an OS replacement, and
// a marker kept there would survive too — onto a freshly rebuilt machine with
// no brew, no binaries, and no genesis — where the early-exit check at the top
// of the generated script would believe it and skip everything.
//
// The rule generalises: the persistent disk carries data, never claims about
// the state of the OS.
const ProvisionedMarker = "/var/lib/ocfp/provisioned"

// LegacyProvisionedMarker is the pre-move location, still probed so bastions
// provisioned before the change are not put through a needless re-provision.
const LegacyProvisionedMarker = "${HOME}/.ocfp/provisioned"
