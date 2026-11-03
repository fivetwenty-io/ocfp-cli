package pve

import "github.com/fivetwenty-io/ocfp-cli/internal/guestboot"

// Shared guest services retain the PVE SMBIOS transport.
const (
	smbiosFamilyBastion = guestboot.SMBIOSFamilyBastion
	ingressNftTable     = guestboot.IngressNftTable
	firstbootScript     = guestboot.FirstbootScript
	watchdogScript      = guestboot.WatchdogScript
	firstbootService    = guestboot.FirstbootService
	watchdogService     = guestboot.WatchdogService
	watchdogTimer       = guestboot.WatchdogTimer
	ingressService      = guestboot.IngressService
)
