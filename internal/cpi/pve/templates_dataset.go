package pve

import "github.com/ocfp/ocfp-cli-go/internal/guestboot"

// Shared guest services retain the PVE SMBIOS transport.
const (
	datasetScript      = guestboot.DatasetScript
	datasetService     = guestboot.DatasetService
	keepHostKeysConfig = guestboot.KeepHostKeysConfig
)

type seedUnitFile struct {
	path    string
	content string
	mode    string
}

// seedUnitFiles is the set of scripts and units baked into the bastion
// template. Split out from seedWriteUnits so tests can assert on the set
// without driving a serial console.
func seedUnitFiles() []seedUnitFile {
	return []seedUnitFile{
		{"/usr/local/sbin/ocfp-firstboot", firstbootScript, "0755"},
		{"/usr/local/sbin/ocfp-tailscale-watchdog", watchdogScript, "0755"},
		{"/usr/local/sbin/ocfp-dataset", datasetScript, "0755"},
		{"/etc/systemd/system/ocfp-firstboot.service", firstbootService, "0644"},
		{"/etc/systemd/system/ocfp-tailscale-watchdog.service", watchdogService, "0644"},
		{"/etc/systemd/system/ocfp-tailscale-watchdog.timer", watchdogTimer, "0644"},
		{"/etc/systemd/system/ocfp-dataset.service", datasetService, "0644"},
		{"/etc/cloud/cloud.cfg.d/99-ocfp-keep-host-keys.cfg", keepHostKeysConfig, "0644"},
	}
}

// seedEnableCommands enables the units the seed installed.
func seedEnableCommands() []string {
	return []string{
		"sudo systemctl daemon-reload",
		"sudo systemctl enable ocfp-firstboot.service",
		"sudo systemctl enable ocfp-tailscale-watchdog.timer",
		"sudo systemctl enable ocfp-dataset.service",
	}
}
