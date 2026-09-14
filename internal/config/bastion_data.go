package config

import (
	"errors"
	"fmt"
	"strings"
)

// Supported filesystems for the bastion's persistent data disk.
//
// ZFS is deliberately absent. The artifacts VM offers it, but the pools these
// blocs sit on are usually already copy-on-write, and nesting ZFS inside a ZFS
// zvol buys two ARCs and write amplification for no benefit. The bastion's
// data disk holds a home directory, not an object store, so there is nothing
// here that wants snapshots at the filesystem layer.
const (
	BastionFilesystemExt4 = "ext4"
	BastionFilesystemXFS  = "xfs"
)

// BastionDataDiskSerial is the disk serial PVE stamps on the bastion's data
// disk, which the guest then exposes at
// /dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_<serial>.
//
// The boot-time unit resolves the disk by this path rather than by /dev/sdb,
// because the /dev/sdN letter depends on enumeration order and is correct
// today only by luck: the cloned template happens to occupy scsi0, so the
// first data disk happens to become scsi1 and happens to enumerate as sdb.
// The moment anything attaches a second disk that stops holding.
const BastionDataDiskSerial = "ocfpdata"

// BastionDataDefaultSizeGiB is the default size of the bastion's data disk.
//
// Measured against a fully built-out bastion, the genuinely irreplaceable
// state is about 411 MiB and the whole home directory is about 8.7 GiB, most
// of which is disposable tarballs and BOSH release cache. 64 GiB leaves ample
// headroom for staging without crowding the thin pools these blocs sit on.
const BastionDataDefaultSizeGiB = 64

// Errors returned by BastionDataConfig.Validate.
var (
	ErrBastionDataFilesystem = errors.New("bastion.data.filesystem must be ext4 or xfs")
	ErrBastionDataMountpoint = errors.New("bastion.data.mountpoint must be an absolute path below /")
	ErrBastionDataSize       = errors.New("bastion.data.diskSizeGiB must be greater than zero")
)

// BastionDataConfig controls the persistent data disk attached to the bastion.
//
// The disk exists so the bastion's operating system can be replaced without
// losing the operator's state. Everything under the home directory lives on
// it, along with the SSH host keys, the tailscale state directory, and the
// Let's Encrypt tree, so a rebuilt bastion comes back as the same machine
// rather than as a fresh one that has to be reconfigured.
//
// The shape mirrors ArtifactsDataConfig, but the key spellings are camelCase
// to match every other key under `bastion:` in the shipped example config.
type BastionDataConfig struct {
	// Enabled controls whether the data disk is created and attached.
	// Defaults to true on PVE and false elsewhere, because PVE is the only
	// provider whose bastion template carries the unit that prepares the disk.
	Enabled bool `json:"enabled,omitempty" mapstructure:"enabled" yaml:"enabled,omitempty"`

	// enabledSet records whether Enabled was explicitly set, so that
	// defaulting can tell an operator's `enabled: false` from an absent key.
	enabledSet bool

	// DiskSizeGiB is the size of the data disk in gibibytes.
	DiskSizeGiB int `json:"diskSizeGiB,omitempty" mapstructure:"diskSizeGiB" yaml:"diskSizeGiB,omitempty"`

	// StoragePool is the pool the data disk is allocated from. Empty falls
	// through to the provider's default storage, which is the pool the
	// bastion's own boot disk came from.
	StoragePool string `json:"storagePool,omitempty" mapstructure:"storagePool" yaml:"storagePool,omitempty"`

	// Filesystem selects how the disk is formatted inside the guest.
	Filesystem string `json:"filesystem,omitempty" mapstructure:"filesystem" yaml:"filesystem,omitempty"`

	// Mountpoint is where the disk is mounted inside the guest. The home
	// directory is then bind-mounted from a subdirectory of it.
	Mountpoint string `json:"mountpoint,omitempty" mapstructure:"mountpoint" yaml:"mountpoint,omitempty"`
}

// SetEnabled records an explicit Enabled value so defaulting will not override
// it. Config loaders call this when the key was present in the source.
func (d *BastionDataConfig) SetEnabled(v bool) {
	d.Enabled = v
	d.enabledSet = true
}

// Defaults applies default values to any field that has not been set.
func (d *BastionDataConfig) Defaults(provider string) {
	if !d.enabledSet {
		d.Enabled = strings.EqualFold(provider, "pve")
	}

	if d.DiskSizeGiB == 0 {
		d.DiskSizeGiB = BastionDataDefaultSizeGiB
	}

	if d.Filesystem == "" {
		d.Filesystem = BastionFilesystemExt4
	}

	if d.Mountpoint == "" {
		d.Mountpoint = "/data"
	}
}

// Validate reports configuration that cannot work. A disabled block is not
// validated, so an operator can leave a half-written block behind the flag.
func (d *BastionDataConfig) Validate() error {
	if !d.Enabled {
		return nil
	}

	switch d.Filesystem {
	case BastionFilesystemExt4, BastionFilesystemXFS:
	default:
		return fmt.Errorf("%w: got %q", ErrBastionDataFilesystem, d.Filesystem)
	}

	if !strings.HasPrefix(d.Mountpoint, "/") || d.Mountpoint == "/" {
		return fmt.Errorf("%w: got %q", ErrBastionDataMountpoint, d.Mountpoint)
	}

	if d.DiskSizeGiB <= 0 {
		return fmt.Errorf("%w: got %d", ErrBastionDataSize, d.DiskSizeGiB)
	}

	return nil
}

// HomeSource returns the directory on the data disk that is bind-mounted onto
// the operator's home directory.
func (d *BastionDataConfig) HomeSource(homeDir string) string {
	return strings.TrimSuffix(d.Mountpoint, "/") + homeDir
}

// SystemDir returns the directory on the data disk that holds the copies of
// system state living outside the home directory: the SSH host keys, the
// tailscale state directory, and the Let's Encrypt tree.
func (d *BastionDataConfig) SystemDir() string {
	return strings.TrimSuffix(d.Mountpoint, "/") + "/system"
}

// applyBastionDataDefaults applies the data-disk defaults using the provider
// resolved by the config loader.
//
// It takes the provider as an argument rather than reading cfg.Provider,
// because that field is not yet populated when applyDefaults runs. Keying off
// the struct field silently disabled the data disk on every PVE bloc.
func applyBastionDataDefaults(cfg *Config, provider string) {
	cfg.Bastion.Data.Defaults(provider)
}
