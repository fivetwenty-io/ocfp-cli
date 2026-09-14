package config

import "testing"

// TestBastionDataDefaults pins the defaults for the bastion's persistent data
// disk.
//
// The size is 64 GiB rather than the 100 the old dead constant carried,
// because the measured content on a fully built-out bastion is about 13 GiB
// (roughly 411 MiB of genuinely irreplaceable state plus a few gigabytes of
// BOSH release cache) and the thin pools these blocs sit on are not large.
//
// The storage pool defaults to empty on purpose. An empty pool falls through
// to the provider's DefaultStorage, which is the pool the bastion's own boot
// disk came from, so an operator who says nothing gets the data disk next to
// the VM it belongs to. Naming a default pool here would repeat the artifacts
// mistake, whose "local-zfs" default does not exist on every cluster.
func TestBastionDataDefaults(t *testing.T) {
	t.Parallel()

	var d BastionDataConfig

	d.Defaults("pve")

	tests := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"Enabled", d.Enabled, true},
		{"DiskSizeGiB", d.DiskSizeGiB, 64},
		{"StoragePool", d.StoragePool, ""},
		{"Filesystem", d.Filesystem, BastionFilesystemExt4},
		{"Mountpoint", d.Mountpoint, "/data"},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.got != tc.want {
				t.Errorf("got %v, want %v", tc.got, tc.want)
			}
		})
	}
}

// TestBastionDataDefaults_NonPVEDisabled asserts the feature is off wherever
// the guest-side format-and-mount work does not exist. Only the PVE bastion
// template carries the unit that prepares the disk.
func TestBastionDataDefaults_NonPVEDisabled(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"aws", "stackit", "gcp", "azure", ""} {
		provider := provider

		t.Run(provider, func(t *testing.T) {
			t.Parallel()

			var d BastionDataConfig

			d.Defaults(provider)

			if d.Enabled {
				t.Errorf("Enabled = true for provider %q, want false", provider)
			}
		})
	}
}

// TestBastionDataDefaults_RespectsExplicitValues asserts defaulting never
// overwrites what an operator set, including an explicit false on Enabled.
func TestBastionDataDefaults_RespectsExplicitValues(t *testing.T) {
	t.Parallel()

	disabled := false
	d := BastionDataConfig{
		Enabled:     true,
		enabledSet:  true,
		DiskSizeGiB: 256,
		StoragePool: "tank-lab",
		Filesystem:  BastionFilesystemXFS,
		Mountpoint:  "/srv/ocfp",
	}

	d.Defaults("pve")

	if d.DiskSizeGiB != 256 {
		t.Errorf("DiskSizeGiB = %d, want 256", d.DiskSizeGiB)
	}

	if d.StoragePool != "tank-lab" {
		t.Errorf("StoragePool = %q, want tank-lab", d.StoragePool)
	}

	if d.Filesystem != BastionFilesystemXFS {
		t.Errorf("Filesystem = %q, want xfs", d.Filesystem)
	}

	if d.Mountpoint != "/srv/ocfp" {
		t.Errorf("Mountpoint = %q, want /srv/ocfp", d.Mountpoint)
	}

	off := BastionDataConfig{Enabled: disabled, enabledSet: true}
	off.Defaults("pve")

	if off.Enabled {
		t.Error("an explicit Enabled=false was overwritten by defaulting")
	}
}

func TestBastionDataValidate(t *testing.T) {
	t.Parallel()

	valid := func() BastionDataConfig {
		return BastionDataConfig{
			Enabled:     true,
			DiskSizeGiB: 64,
			Filesystem:  BastionFilesystemExt4,
			Mountpoint:  "/data",
		}
	}

	tests := []struct {
		name    string
		mutate  func(*BastionDataConfig)
		wantErr bool
	}{
		{"valid", func(*BastionDataConfig) {}, false},
		{"disabled skips validation", func(d *BastionDataConfig) {
			d.Enabled = false
			d.Filesystem = "nonsense"
		}, false},
		{"unknown filesystem", func(d *BastionDataConfig) { d.Filesystem = "btrfs" }, true},
		{"relative mountpoint", func(d *BastionDataConfig) { d.Mountpoint = "data" }, true},
		{"root mountpoint", func(d *BastionDataConfig) { d.Mountpoint = "/" }, true},
		{"zero size", func(d *BastionDataConfig) { d.DiskSizeGiB = 0 }, true},
		{"negative size", func(d *BastionDataConfig) { d.DiskSizeGiB = -1 }, true},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := valid()
			tc.mutate(&d)

			err := d.Validate()

			if tc.wantErr && err == nil {
				t.Errorf("expected an error, got nil")
			}

			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestBastionDataDeviceSerial pins the disk serial the guest resolves the disk
// by. It must be stable across rebuilds, because the boot-time unit looks the
// disk up at /dev/disk/by-id/...<serial> rather than trusting /dev/sdb, whose
// letter depends on enumeration order.
func TestBastionDataDeviceSerial(t *testing.T) {
	t.Parallel()

	if BastionDataDiskSerial == "" {
		t.Fatal("BastionDataDiskSerial must not be empty")
	}

	// PVE accepts up to 20 characters for a disk serial and the guest exposes
	// it verbatim in the by-id path, so anything longer would be truncated and
	// stop matching.
	if len(BastionDataDiskSerial) > 20 {
		t.Errorf("BastionDataDiskSerial %q is %d characters, PVE truncates beyond 20",
			BastionDataDiskSerial, len(BastionDataDiskSerial))
	}
}
