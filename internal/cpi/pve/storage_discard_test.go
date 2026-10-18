package pve

import (
	"errors"
	"reflect"
	"testing"
)

// TestParseDiskSlotValue pins how a slot's config value splits into the volume
// id and its options. The options keep their order, because a corrected value
// is rebuilt from them and must differ from the original only where we meant
// it to.
func TestParseDiskSlotValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		value       string
		wantVolume  string
		wantOptions []string
	}{
		{
			name:        "bare volume id",
			value:       "local-lvm-data:vm-100-data",
			wantVolume:  "local-lvm-data:vm-100-data",
			wantOptions: nil,
		},
		{
			name:        "options keep their order",
			value:       "local-lvm-data:vm-100-data,size=500G,backup=0,serial=ocfpartifacts",
			wantVolume:  "local-lvm-data:vm-100-data",
			wantOptions: []string{"size=500G", "backup=0", "serial=ocfpartifacts"},
		},
		{
			name:        "file-backed volume",
			value:       "nfs-a:100/vm-100-data.qcow2,discard=on,size=64G",
			wantVolume:  "nfs-a:100/vm-100-data.qcow2",
			wantOptions: []string{"discard=on", "size=64G"},
		},
		{
			name:        "empty",
			value:       "",
			wantVolume:  "",
			wantOptions: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := parseDiskSlotValue(tc.value)

			if got.VolumeID != tc.wantVolume {
				t.Errorf("VolumeID = %q, want %q", got.VolumeID, tc.wantVolume)
			}

			if !reflect.DeepEqual(got.Options, tc.wantOptions) {
				t.Errorf("Options = %#v, want %#v", got.Options, tc.wantOptions)
			}

			if tc.value != "" && got.String() != tc.value {
				t.Errorf("String() = %q, want the original %q back", got.String(), tc.value)
			}
		})
	}
}

// TestDiskSlotValueOption looks an option up by its whole key. A substring
// match would read discard_granularity or a serial containing "discard" as
// the discard setting.
func TestDiskSlotValueOption(t *testing.T) {
	t.Parallel()

	v := parseDiskSlotValue("pool:vm-1-data,serial=discard-me,xdiscard=on,discard=ignore,ssd")

	got, ok := v.Option("discard")
	if !ok || got != "ignore" {
		t.Errorf("Option(discard) = %q, %v, want \"ignore\", true", got, ok)
	}

	if got, ok := v.Option("ssd"); !ok || got != "" {
		t.Errorf("Option(ssd) = %q, %v, want a present flag with no value", got, ok)
	}

	if got, ok := v.Option("size"); ok {
		t.Errorf("Option(size) = %q, want absent", got)
	}

	bare := parseDiskSlotValue("pool:vm-1-data,serial=discard=on")
	if got, ok := bare.Option("discard"); ok {
		t.Errorf("Option(discard) = %q on a serial that merely contains it, want absent", got)
	}
}

// TestVolumeFormatOf reads the format from the file extension, which is how
// file-backed storage names its volumes, and falls back to a format= option.
func TestVolumeFormatOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  string
	}{
		{"nfs-a:100/vm-100-data.qcow2,size=64G", "qcow2"},
		{"dir-a:100/vm-100-data.raw", "raw"},
		{"dir-a:100/vm-100-data.vmdk", "vmdk"},
		{"dir-a:100/vm-100-data,format=qcow2", "qcow2"},
		{"local-lvm-data:vm-100-data,size=64G", ""},
	}

	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()

			if got := volumeFormatOf(parseDiskSlotValue(tc.value)); got != tc.want {
				t.Errorf("volumeFormatOf(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestClassifyStorageProvisioning covers which pools hand freed blocks back
// when a disk carries discard=on. Plain lvm preallocates, so discard does
// nothing there. On file-backed storage only qcow2 is known to shrink; a raw
// file may or may not be sparse, so we say we do not know.
func TestClassifyStorageProvisioning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		poolType string
		format   string
		want     StorageProvisioning
	}{
		{"lvmthin", "", ProvisioningThin},
		{"zfspool", "", ProvisioningThin},
		{"rbd", "", ProvisioningThin},
		{"lvm", "", ProvisioningThick},
		{"dir", "qcow2", ProvisioningThin},
		{"nfs", "qcow2", ProvisioningThin},
		{"cifs", "qcow2", ProvisioningThin},
		{"nfs", "raw", ProvisioningUnknown},
		{"dir", "vmdk", ProvisioningUnknown},
		{"dir", "", ProvisioningUnknown},
		{"iscsi", "", ProvisioningUnknown},
		{"", "", ProvisioningUnknown},
		{"LVMThin", "", ProvisioningThin},
	}

	for _, tc := range tests {
		t.Run(tc.poolType+"/"+tc.format, func(t *testing.T) {
			t.Parallel()

			if got := classifyStorageProvisioning(tc.poolType, tc.format); got != tc.want {
				t.Errorf("classifyStorageProvisioning(%q, %q) = %q, want %q", tc.poolType, tc.format, got, tc.want)
			}
		})
	}
}

// TestDiscardOnValue builds the corrected slot value. Everything other than
// the discard option must come back exactly as it was, in the same order,
// because PVE rewrites the whole slot from what we send and an option we
// dropped (backup=0, serial=) would silently change the disk.
func TestDiscardOnValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "missing discard is appended",
			value: "local-lvm-data:vm-105-data,size=500G",
			want:  "local-lvm-data:vm-105-data,size=500G,discard=on",
		},
		{
			name:  "bare volume id gains the option",
			value: "local-lvm-data:vm-105-data",
			want:  "local-lvm-data:vm-105-data,discard=on",
		},
		{
			name:  "discard=ignore is replaced in place",
			value: "local-lvm-data:vm-105-data,discard=ignore,backup=0,size=500G",
			want:  "local-lvm-data:vm-105-data,discard=on,backup=0,size=500G",
		},
		{
			name:  "existing discard=on is left alone",
			value: "local-lvm-data:vm-105-data,discard=on,serial=ocfpartifacts,size=500G",
			want:  "local-lvm-data:vm-105-data,discard=on,serial=ocfpartifacts,size=500G",
		},
		{
			name:  "every other option survives in order",
			value: "local-lvm-data:vm-105-data,backup=0,iothread=1,serial=ocfpdata,size=64G,ssd=1",
			want:  "local-lvm-data:vm-105-data,backup=0,iothread=1,serial=ocfpdata,size=64G,ssd=1,discard=on",
		},
		{
			name:  "a serial containing discard is not mistaken for the option",
			value: "local-lvm-data:vm-105-data,serial=discard,size=64G",
			want:  "local-lvm-data:vm-105-data,serial=discard,size=64G,discard=on",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := discardOnValue(tc.value)
			if err != nil {
				t.Fatalf("discardOnValue(%q): %v", tc.value, err)
			}

			if got != tc.want {
				t.Errorf("discardOnValue(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestDiscardOnValue_RefusesMalformedValues makes sure we never build a value
// PVE might read as a different disk. Each of these would rather fail loudly
// than be rewritten.
func TestDiscardOnValue_RefusesMalformedValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"",
		"none,media=cdrom",
		"/dev/sdb,size=64G",
		"local-lvm-data:vm-105-data,,size=64G",
		"local-lvm-data:vm-105-data,discard=ignore,discard=on",
		":vm-105-data,size=64G",
		"local-lvm-data:,size=64G",
		"local-lvm-data:vm-105-cloudinit,media=cdrom",
		"local-lvm-data:iso/ubuntu.iso,media=cdrom,size=2G",
		"local-lvm-data:vm-105-data,MEDIA=cdrom",
	} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			got, err := discardOnValue(value)
			if err == nil {
				t.Fatalf("discardOnValue(%q) = %q, want an error", value, got)
			}

			if !errors.Is(err, ErrInvalidDiskSlotValue) {
				t.Errorf("error = %v, want it to wrap ErrInvalidDiskSlotValue", err)
			}
		})
	}
}
