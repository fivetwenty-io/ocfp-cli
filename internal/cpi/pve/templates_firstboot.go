package pve

import "github.com/ocfp/ocfp-cli-go/internal/guestboot"

// Shared guest services retain the PVE SMBIOS transport.
const (
	smbiosFamilyBastion = guestboot.SMBIOSFamilyBastion
	ingressNftTable     = guestboot.IngressNftTable
	firstbootScript     = guestboot.FirstbootScript
	watchdogScript      = guestboot.WatchdogScript
	firstbootService    = guestboot.FirstbootService
	watchdogService     = guestboot.WatchdogService
	watchdogTimer       = guestboot.WatchdogTimer
)
