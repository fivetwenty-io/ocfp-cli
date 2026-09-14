package pve

import "testing"

// TestFindVolumeBySerial covers the identity problem a bastion recycle runs
// into, and which cost us a failed resume on a live bloc.
//
// Proxmox renames a reassigned volume to match its new owner, so the disk that
// was created as vm-100-data arrives on the replacement as vm-102-disk-1.
// Nothing in that name says "data", and it is indistinguishable by name from
// the OS disk sitting next to it at vm-102-disk-0. Every name-based rule we
// have tried has therefore been wrong after a handover.
//
// The serial is the identity that survives, which is the whole reason we set
// one. Look the disk up by that instead.
func TestFindVolumeBySerial(t *testing.T) {
	t.Parallel()

	config := map[string]interface{}{
		"name":  "ocfp-lab-nabramovitz-bastion",
		"scsi0": "local-lvm-data:vm-102-disk-0,discard=on,size=54784M,ssd=1",
		"scsi1": "local-lvm-data:vm-102-disk-1,discard=on,serial=ocfpdata,size=64G",
		"ide2":  "local-lvm-data:vm-102-cloudinit,media=cdrom,size=4M",
	}

	got, found := findVolumeBySerial(config, "ocfpdata")
	if !found {
		t.Fatal("the data disk was not found by its serial")
	}

	want := "local-lvm-data:vm-102-disk-1"
	if got != want {
		t.Errorf("findVolumeBySerial() = %q, want %q", got, want)
	}
}

// TestFindVolumeBySerial_NoMatch asserts we report absence rather than
// guessing. A wrong guess here attaches the recycle to the OS disk.
func TestFindVolumeBySerial_NoMatch(t *testing.T) {
	t.Parallel()

	config := map[string]interface{}{
		"scsi0": "local-lvm-data:vm-102-disk-0,discard=on,size=54784M,ssd=1",
	}

	if got, found := findVolumeBySerial(config, "ocfpdata"); found {
		t.Errorf("findVolumeBySerial() = %q, want no match on a VM carrying only its OS disk", got)
	}
}

// TestFindVolumeBySerial_IgnoresPartialSerialMatches guards against a serial
// that merely contains the one we asked for.
func TestFindVolumeBySerial_IgnoresPartialSerialMatches(t *testing.T) {
	t.Parallel()

	config := map[string]interface{}{
		"scsi1": "local-lvm-data:vm-102-disk-1,discard=on,serial=ocfpdata-old,size=64G",
	}

	if got, found := findVolumeBySerial(config, "ocfpdata"); found {
		t.Errorf("findVolumeBySerial() = %q, want no match: serial ocfpdata-old is a different disk", got)
	}
}
