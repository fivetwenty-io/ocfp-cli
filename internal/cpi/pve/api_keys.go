package pve

// PVE API map key constants used in VM config / firewall rule payloads.
// Centralised here so goconst finds no duplicates across the package.
const (
	pveKeyMemory = "memory"
	pveKeyCores  = "cores"
	pveKeyName   = "name"
	pveKeyNet0   = "net0"
	pveKeyScsi0  = "scsi0"
	pveKeyEnable = "enable"
	pveKeyType   = "type"
)
