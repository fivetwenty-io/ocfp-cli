package commands

// cpiRoleID is the Proxmox role the CPI account is bound to.
const cpiRoleID = "OCFPCpi"

// cpiRunbook is the runbook that creates the role and owns its privilege
// list. Errors point operators at it because ocfp does not create the role.
const cpiRunbook = "docs/runbooks/pve/02-pve-foundation.md"

// cpiRolePrivileges is the privilege set the OCFPCpi role must hold. It
// matches the `pmx pve access role create OCFPCpi --privs` block in the
// runbook, and a test fails when the two differ. The runbook says this one
// list works on PVE 8 and PVE 9, so there are no per-version variants.
var cpiRolePrivileges = []string{ //nolint:gochecknoglobals // constant list
	"Datastore.Allocate",
	"Datastore.AllocateSpace",
	"Datastore.AllocateTemplate",
	"Datastore.Audit",
	"Pool.Allocate",
	"Pool.Audit",
	"SDN.Use",
	"Sys.Audit",
	"Sys.Console",
	"Sys.Modify",
	"VM.Allocate",
	"VM.Audit",
	"VM.Backup",
	"VM.Clone",
	"VM.Config.CDROM",
	"VM.Config.Cloudinit",
	"VM.Config.CPU",
	"VM.Config.Disk",
	"VM.Config.HWType",
	"VM.Config.Memory",
	"VM.Config.Network",
	"VM.Config.Options",
	"VM.Console",
	"VM.GuestAgent.Audit",
	"VM.GuestAgent.Unrestricted",
	"VM.Migrate",
	"VM.PowerMgmt",
	"VM.Snapshot",
}
