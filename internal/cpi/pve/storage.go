package pve

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/qemu"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// volumeIDPartCount is the expected number of parts when splitting a volume ID by ":".
	volumeIDPartCount = 2
	// snapshotIDPartCount is the expected number of parts when splitting a snapshot ID by ":".
	snapshotIDPartCount = 2
	// bytesPerGB is the number of bytes in a gigabyte for size conversions.
	bytesPerGB = 1024 * 1024 * 1024
	// taskTimeoutSnapshot is the timeout in seconds for snapshot operations.
	taskTimeoutSnapshot = 300
)

// StorageManager handles Proxmox storage operations.
type StorageManager struct {
	client *Client

	// External-blobstore state (mode=external). Both fields are zero in
	// local mode; see blobstore.go for the lazy-init path.
	blobstoreMu sync.Mutex
	blobstoreS3 *blobstoreS3Client

	// storageTypes caches GET /storage/{storage} "type" answers by pool
	// name. A pool's plugin type never changes while the CLI runs, and
	// CreateVolume consults it on every call.
	storageTypesMu sync.Mutex
	storageTypes   map[string]string
}

// fileBackedStorageTypes lists the PVE storage plugin types that keep
// volumes as files and parse the filename with a pattern that requires a
// format extension (vm-<vmid>-<name>.<qcow2|raw|vmdk>). Block plugins (lvm,
// lvmthin, zfspool, rbd, iscsi, ...) reject an extension outright.
//
//nolint:gochecknoglobals // package-level lookup set
var fileBackedStorageTypes = map[string]struct{}{
	"dir":       {},
	"nfs":       {},
	"cifs":      {},
	"glusterfs": {},
	"cephfs":    {},
	"btrfs":     {},
}

// fileBackedVolumeFormat is the disk format used for volumes on file-backed
// pools; it is the same default PVE itself picks for those plugins.
const fileBackedVolumeFormat = "qcow2"

// volumeNameAndFormat adapts a block-style volume name to the target pool's
// plugin. On file-backed pools the name gains a ".qcow2" extension and the
// matching format is returned so PVE's filename parser accepts it; on every
// other pool the name is returned untouched with an empty format so PVE
// applies its native default (raw). A name that already carries a
// recognised extension is kept as-is with that extension as the format.
func volumeNameAndFormat(volName, storageType string) (string, string) {
	if _, ok := fileBackedStorageTypes[strings.ToLower(storageType)]; !ok {
		return volName, ""
	}

	for _, ext := range []string{"qcow2", "raw", "vmdk"} {
		if strings.HasSuffix(volName, "."+ext) {
			return volName, ext
		}
	}

	return volName + "." + fileBackedVolumeFormat, fileBackedVolumeFormat
}

// storageType returns the plugin type of a storage pool (nfs, dir, lvmthin,
// zfspool, ...) from GET /storage/{storage}, caching the answer per pool.
func (m *StorageManager) storageType(ctx context.Context, storage string) (string, error) {
	m.storageTypesMu.Lock()
	defer m.storageTypesMu.Unlock()

	if cached, ok := m.storageTypes[storage]; ok {
		return cached, nil
	}

	resp, err := m.client.pveClient.GetCtx(ctx, "/storage/"+url.PathEscape(storage), nil)
	if err != nil {
		return "", fmt.Errorf("lookup storage %q type: %w", storage, err)
	}

	data, ok := resp.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("%w: storage %q", ErrStorageTypeUnknown, storage)
	}

	storageType := getStringFromMap(data, "type")
	if storageType == "" {
		return "", fmt.Errorf("%w: storage %q", ErrStorageTypeUnknown, storage)
	}

	if m.storageTypes == nil {
		m.storageTypes = make(map[string]string)
	}

	m.storageTypes[storage] = storageType

	return storageType, nil
}

// pveVolumeName returns a volume filename that satisfies PVE's storage-pool
// naming constraints. ZFS, LVM-thin, and RBD require `vm-{vmid}-*`; dir/NFS
// accept any name but happily store one that follows convention. When vmid is
// zero we keep the caller's name verbatim so unowned volumes (which only land
// on dir-style storage) preserve their descriptive identity.
func pveVolumeName(reqName string, vmid int) string {
	if vmid <= 0 {
		return reqName
	}

	prefix := fmt.Sprintf("vm-%d-", vmid)
	if strings.HasPrefix(reqName, prefix) {
		return reqName
	}

	suffix := "disk"

	if reqName != "" {
		parts := strings.Split(reqName, "-")
		suffix = parts[len(parts)-1]
	}

	return prefix + suffix
}

// parseVolumeOwnerVMID returns the PVE VMID that should own a new volume.
// Empty InstanceID returns 0 (the caller may still error from PVE, but some
// pools accept unowned volumes — this preserves that). Non-numeric or negative
// values fail fast so we don't send a malformed vmid to PVE.
func parseVolumeOwnerVMID(req *cpi.VolumeRequest) (int, error) {
	if req == nil || req.InstanceID == "" {
		return 0, nil
	}

	vmid, err := strconv.Atoi(req.InstanceID)
	if err != nil {
		return 0, fmt.Errorf("InstanceID %q is not numeric: %w", req.InstanceID, err)
	}

	if vmid < 0 {
		return 0, fmt.Errorf("InstanceID %d must be non-negative", vmid) //nolint:err113 // descriptive error, not caller-testable
	}

	return vmid, nil
}

// CreateVolume creates a new volume on a storage pool.
func (m *StorageManager) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	logger.WithOperation("CreateVolume").Infof("Creating volume: %s", req.Name)

	node, err := m.client.getNode(ctx)
	if err != nil {
		return nil, err
	}

	storage := m.client.config.DefaultStorage
	if req.Type != "" {
		storage = req.Type
	}

	// Determine size
	sizeGB := req.Size
	if req.SizeGB > 0 {
		sizeGB = req.SizeGB
	}

	if sizeGB == 0 {
		sizeGB = 10 // Default 10GB
	}

	storageSvc := m.client.getStorageService()

	// PVE storage pools (local-lvm, local-zfs, ceph-rbd) reject vmid=0: a
	// volume must belong to a VM. Callers that create the VM first plumb the
	// owning instance id through req.InstanceID.
	vmid, err := parseVolumeOwnerVMID(req)
	if err != nil {
		return nil, fmt.Errorf("resolve volume owner: %w", err)
	}

	// Generate volume name. Block storage pools enforce `vm-{vmid}-*`; we
	// rewrite the caller's descriptive name into that form when a VMID is
	// supplied so the same call works across pool types.
	volName := pveVolumeName(req.Name, vmid)
	if volName == "" {
		volName = fmt.Sprintf("vol-%d", time.Now().UnixNano())
	}

	// File-backed pools (dir, nfs, cifs, ...) parse the filename with a
	// pattern that demands a format extension, so "vm-20001-data" fails
	// there with "unable to parse volume filename". Block pools (lvm, zfs,
	// rbd, ...) reject an extension and a forced qcow2 format. Ask PVE which
	// plugin backs the pool and shape the name and format to match.
	storageType, err := m.storageType(ctx, storage)
	if err != nil {
		return nil, err
	}

	volName, format := volumeNameAndFormat(volName, storageType)

	volID, err := storageSvc.CreateVolume(ctx, node, storage, sizeGB, format, vmid, volName)
	if err != nil {
		return nil, fmt.Errorf("failed to create volume: %w", err)
	}

	return &cpi.Volume{
		ID:        volID,
		Name:      volName,
		Size:      sizeGB,
		Type:      storage,
		State:     cpi.ResourceStateAvailable,
		Tags:      req.Tags,
		CreatedAt: time.Now(),
	}, nil
}

// GetVolume retrieves a volume.
func (m *StorageManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) { //nolint:varnamelen // id is clear in context
	node, err := m.client.getNode(ctx)
	if err != nil {
		return nil, err
	}

	// Volume ID format: storage:type/name or storage:name
	parts := strings.Split(id, ":")
	if len(parts) != volumeIDPartCount {
		return nil, fmt.Errorf("%w: %s", ErrInvalidVolumeIDFormat, id)
	}

	storage := parts[0]

	storageSvc := m.client.getStorageService()

	exists, err := storageSvc.Exists(ctx, node, storage, id)
	if err != nil {
		return nil, fmt.Errorf("failed to check volume: %w", err)
	}

	if !exists {
		return nil, ErrVolumeNotFound
	}

	// Get volume details from storage content
	path := buildPVEPathf(node, "storage/%s/content/%s", storage, id)

	resp, err := m.client.pveClient.GetCtx(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get volume details: %w", err)
	}

	data, ok := resp.(map[string]interface{})
	if !ok {
		return &cpi.Volume{
			ID:    id,
			Name:  id,
			Type:  storage,
			State: cpi.ResourceStateAvailable,
			Tags:  make(map[string]string),
		}, nil
	}

	// Parse size (in bytes)
	var sizeGB int
	if size, ok := data["size"].(float64); ok {
		sizeGB = int(size / bytesPerGB)
	}

	return &cpi.Volume{
		ID:    id,
		Name:  getStringFromMap(data, "volid"),
		Size:  sizeGB,
		Type:  storage,
		State: cpi.ResourceStateAvailable,
		Tags:  make(map[string]string),
	}, nil
}

// ListVolumes lists volumes with optional filters.
//
// Recognised filters: "storage" picks the pool (default: the configured
// DefaultStorage), "node" picks the cluster node (default: the configured node,
// else the first online one), and "name" keeps volumes whose volid contains the
// given substring.
//
// Each returned volume carries the owning guest's VMID in AttachedTo when PVE
// reports one. PVE records that owner on the content entry itself, which is
// what lets a caller delete a guest's volumes by attribution rather than by
// sweeping a pool.
func (m *StorageManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	node, ok := filters["node"]
	if !ok || node == "" {
		var err error

		node, err = m.client.getNode(ctx)
		if err != nil {
			return nil, err
		}
	}

	storage := m.client.config.DefaultStorage
	if storageFilter, ok := filters["storage"]; ok {
		storage = storageFilter
	}

	// List storage content
	path := buildPVEPathf(node, "storage/%s/content", storage)

	resp, err := m.client.pveClient.GetCtx(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list storage content: %w", err)
	}

	data, ok := resp.([]interface{})
	if !ok {
		return []*cpi.Volume{}, nil
	}

	var volumes []*cpi.Volume

	for _, item := range data {
		volData, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		// Only include images/volumes (not ISOs, etc.)
		content := getStringFromMap(volData, "content")
		if content != "images" && content != "rootdir" {
			continue
		}

		volID := getStringFromMap(volData, "volid")

		// Apply name filter
		if nameFilter, ok := filters["name"]; ok && !strings.Contains(volID, nameFilter) {
			continue
		}

		var sizeGB int
		if size, ok := volData["size"].(float64); ok {
			sizeGB = int(size / bytesPerGB)
		}

		// PVE reports the owning guest on image and rootdir entries. Keep it:
		// it is the only authoritative link from a volume back to its VM.
		owner := ""
		if ownerVMID := getIntFromMap(volData, "vmid"); ownerVMID > 0 {
			owner = strconv.Itoa(ownerVMID)
		}

		volumes = append(volumes, &cpi.Volume{
			ID:         volID,
			Name:       volID,
			Size:       sizeGB,
			Type:       storage,
			State:      cpi.ResourceStateAvailable,
			AttachedTo: owner,
			Tags:       make(map[string]string),
		})
	}

	return volumes, nil
}

// attachBuses are the PVE disk buses a slot spec may name. Order matters only
// for readability; the match is exact on the alphabetic prefix.
//
//nolint:gochecknoglobals // intentional package-level lookup table
var attachBuses = []string{"virtio", "scsi", "sata", "ide"}

// parseAttachDevice interprets the cpi StorageManager's free-form `device`
// hint for PVE.
//
// The hint has two dialects. Historically callers passed a Linux device path
// such as "/dev/sdb", which says what the guest should end up seeing rather
// than what PVE should configure; that pins no slot, so PVE assigns the next
// free index on the bus and the correspondence to /dev/sdN holds only by
// luck. The second dialect is a PVE-native slot spec such as "scsi1", which
// pins the slot exactly, optionally followed by disk properties after a comma
// ("scsi1,discard=on,serial=ocfpdata").
//
// It returns the bus to attach on, the slot to pin (empty when the caller
// named none), and the properties to append to the disk's config value.
func parseAttachDevice(device string) (bus, slot, props string) {
	bus = "scsi"

	if device == "" || strings.HasPrefix(device, "/") {
		return bus, "", ""
	}

	spec := device

	if idx := strings.Index(spec, ","); idx >= 0 {
		props = spec[idx+1:]
		spec = spec[:idx]
	}

	for _, candidate := range attachBuses {
		if !strings.HasPrefix(spec, candidate) {
			continue
		}

		bus = candidate

		// A bare bus name with no index is not a slot: "scsi" selects the bus
		// but leaves the index to PVE, whereas "scsi1" pins it.
		if index := spec[len(candidate):]; index != "" && isAllDigits(index) {
			slot = spec
		}

		return bus, slot, props
	}

	// An unrecognised spec keeps the historical default bus and pins nothing.
	return bus, "", props
}

// isAllDigits reports whether s is non-empty and consists only of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

// attachValueForVolume builds the value PVE stores under the disk's config
// key. Properties must be appended to the volume id rather than sent as
// separate parameters, because the client library's AttachOpts.Extra map
// writes each of its keys as its own top-level VM config key.
func attachValueForVolume(volumeID, props string) string {
	if props == "" {
		return volumeID
	}

	return volumeID + "," + props
}

// AttachVolume attaches a volume to an instance.
//
// When the device hint names a PVE slot the slot is pinned, which makes the
// guest-side device path predictable instead of dependent on PVE's next-free
// arithmetic. Any properties carried on the hint ride along on the config
// value; `serial=` in particular gives the guest a stable
// /dev/disk/by-id/scsi-SQEMU_QEMU_HARDDISK_<serial> path.
func (m *StorageManager) AttachVolume(ctx context.Context, volumeID string, instanceID string, device string) error {
	vmid, err := strconv.Atoi(instanceID)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidVMID, instanceID)
	}

	node, err := m.client.getNode(ctx)
	if err != nil {
		return err
	}

	qemuSvc := m.client.getQemuService()

	bus, slot, props := parseAttachDevice(device)

	var opts *qemu.AttachOpts
	if slot != "" {
		opts = &qemu.AttachOpts{DiskID: slot}
	}

	// Attach the disk
	_, err = qemuSvc.AttachDisk(ctx, node, vmid, attachValueForVolume(volumeID, props), bus, opts)
	if err != nil {
		return fmt.Errorf("failed to attach volume: %w", err)
	}

	return nil
}

// diskSlotPrefixes are the VM config key prefixes that can hold a volume
// reference. `unused` belongs here alongside the buses: PVE's own detach moves
// a volume into an unusedN slot, so a scan that skips those reports an
// already-half-detached disk as missing.
//
//nolint:gochecknoglobals // intentional package-level lookup table
var diskSlotPrefixes = []string{"scsi", "virtio", "sata", "ide", "unused"}

// findDiskSlotForVolume returns the VM config key holding the given volume id.
//
// A config value is either the bare volid or the volid followed by a
// comma-separated property list, so the match is on the first comma-delimited
// field rather than on a substring. Matching on a substring would let
// vm-100-data match a slot holding vm-100-data2.
func findDiskSlotForVolume(config map[string]interface{}, volumeID string) (string, bool) {
	for key, value := range config {
		if !hasAnyPrefix(key, diskSlotPrefixes) {
			continue
		}

		valueStr, ok := value.(string)
		if !ok {
			continue
		}

		candidate := valueStr
		if idx := strings.Index(candidate, ","); idx >= 0 {
			candidate = candidate[:idx]
		}

		if candidate == volumeID {
			return key, true
		}
	}

	return "", false
}

// hasAnyPrefix reports whether s starts with any of the given prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}

	return false
}

// reassignTaskTimeout bounds the move_disk task. A reassignment is a config
// rewrite plus a volume rename rather than a data copy, so it completes in
// seconds; the headroom is for a busy cluster.
const reassignTaskTimeout = 300

// upidFromResponse pulls a task id out of a raw PVE POST response.
//
// The client hands back whatever sits under the API envelope's "data" key,
// which for a task-returning endpoint is the UPID string. Some endpoints wrap
// it in an object instead, and some return nothing at all when the work was
// synchronous, so all three shapes are accepted and only a genuinely
// unrecognised one is an error.
func upidFromResponse(resp interface{}) (string, error) {
	switch v := resp.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case map[string]interface{}:
		for _, key := range []string{"upid", "UPID", "data"} {
			if raw, ok := v[key]; ok {
				if s, isStr := raw.(string); isStr {
					return s, nil
				}
			}
		}

		return "", fmt.Errorf("%w: object response carried no task id: %v", ErrUnexpectedTaskResponse, v)
	default:
		return "", fmt.Errorf("%w: %T", ErrUnexpectedTaskResponse, resp)
	}
}

// reassignPath is the move_disk endpoint for a VM. The call is issued against
// the SOURCE VM; the destination travels in the body as target-vmid.
func reassignPath(node string, sourceVMID int) string {
	return fmt.Sprintf("/nodes/%s/qemu/%d/move_disk", url.PathEscape(node), sourceVMID)
}

// buildReassignParams builds the move_disk body. An empty targetSlot is
// omitted entirely rather than sent blank, which lets PVE pick the next free
// slot on the target.
func buildReassignParams(slot string, targetVMID int, targetSlot string) map[string]interface{} {
	params := map[string]interface{}{
		"disk":        slot,
		"target-vmid": targetVMID,
	}

	if targetSlot != "" {
		params["target-disk"] = targetSlot
	}

	return params
}

// diskOptionsFrom returns the options a disk's config value carries after the
// volume id, or empty when it carries none.
//
// Proxmox's move_disk hands the volume to the target VM and renames it, but
// writes the new config entry as the bare volume id. Everything the source
// slot was carrying is dropped, and for the OCFP data disk that means
// discard=on and, far more importantly, serial=ocfpdata. The serial is how the
// guest finds the disk at all: without it the dataset script refuses to guess
// a device, and the recycled bastion comes up with an empty home directory.
func diskOptionsFrom(value string) string {
	idx := strings.Index(value, ",")
	if idx < 0 {
		return ""
	}

	return value[idx+1:]
}

// reattachValue rebuilds a slot's config value from the volume the move
// produced and the options the source slot was carrying.
func reattachValue(volumeID, options string) string {
	if options == "" {
		return volumeID
	}

	return volumeID + "," + options
}

// restoreDiskOptions re-applies the options a reassign dropped.
//
// It reads the target's config rather than assuming the volume's new name,
// because the rename is Proxmox's to decide and guessing it is how this goes
// wrong quietly.
func (m *StorageManager) restoreDiskOptions(ctx context.Context, node string, targetVMID int, slot, options string) error {
	if options == "" {
		return nil
	}

	config, err := m.client.getQemuService().Config(ctx, node, targetVMID)
	if err != nil {
		return fmt.Errorf("read target VM config: %w", err)
	}

	current, _ := config[slot].(string)
	if current == "" {
		return fmt.Errorf("%w: %s on VM %d after reassign", ErrVolumeNotFoundOnVM, slot, targetVMID)
	}

	volumeID := current
	if idx := strings.Index(current, ","); idx >= 0 {
		volumeID = current[:idx]
	}

	want := reattachValue(volumeID, options)
	if want == current {
		return nil
	}

	logger.WithOperation("ReassignVolume").Infof(
		"restoring disk options on VM %d %s: %s", targetVMID, slot, options)

	_, err = m.client.pveClient.PutCtx(ctx,
		buildPVEPathf(node, "qemu/%d/config", targetVMID),
		map[string]interface{}{slot: want})
	if err != nil {
		return fmt.Errorf("restore disk options on VM %d %s: %w", targetVMID, slot, err)
	}

	return nil
}

// ReassignVolume hands a volume from one VM to another in a single server-side
// operation, so the volume never belongs to nothing.
//
// PVE renames the volume to match its new owner, which is why this is
// preferred over detach-then-re-attach: the storage entry's ownership stays
// truthful, so ListVolumes and teardown-by-attribution keep working without
// special cases.
//
// It does not, however, carry the disk's options across. The new config entry
// is the bare volume id, so we capture what the source slot was carrying and
// put it back afterwards.
//
// Two constraints, both confirmed against a live PVE 9 cluster and both
// enforced by PVE as safe refusals that leave the disk where it was. The
// source VM must be stopped; a running one fails with "Cannot move disk to
// another VM while the source VM is running - detach first". And the target
// slot must be free; an occupied one fails with "Target disk key '...' is
// already in use".
func (m *StorageManager) ReassignVolume(
	ctx context.Context,
	volumeID string,
	sourceInstanceID string,
	targetInstanceID string,
	targetSlot string,
) error {
	sourceVMID, err := strconv.Atoi(sourceInstanceID)
	if err != nil {
		return fmt.Errorf("%w: source %s", ErrInvalidVMID, sourceInstanceID)
	}

	targetVMID, err := strconv.Atoi(targetInstanceID)
	if err != nil {
		return fmt.Errorf("%w: target %s", ErrInvalidVMID, targetInstanceID)
	}

	node, err := m.client.getNode(ctx)
	if err != nil {
		return err
	}

	config, err := m.client.getQemuService().Config(ctx, node, sourceVMID)
	if err != nil {
		return fmt.Errorf("failed to get source VM config: %w", err)
	}

	slot, found := findDiskSlotForVolume(config, volumeID)
	if !found {
		return fmt.Errorf("%w: %s on VM %d", ErrVolumeNotFoundOnVM, volumeID, sourceVMID)
	}

	sourceValue, _ := config[slot].(string)
	options := diskOptionsFrom(sourceValue)

	logger.WithOperation("ReassignVolume").Infof(
		"reassigning %s from VM %d (%s) to VM %d (%s)", volumeID, sourceVMID, slot, targetVMID, targetSlot)

	resp, err := m.client.pveClient.PostCtx(ctx, reassignPath(node, sourceVMID),
		buildReassignParams(slot, targetVMID, targetSlot))
	if err != nil {
		return fmt.Errorf("reassign %s from VM %d to VM %d: %w", volumeID, sourceVMID, targetVMID, err)
	}

	upid, err := upidFromResponse(resp)
	if err != nil {
		return fmt.Errorf("reassign task id: %w", err)
	}

	if upid != "" {
		err = m.client.waitForTask(ctx, node, upid, reassignTaskTimeout)
		if err != nil {
			return fmt.Errorf("await reassign task: %w", err)
		}
	}

	return m.restoreDiskOptions(ctx, node, targetVMID, targetSlot, options)
}

// DetachVolume detaches a volume from an instance.
func (m *StorageManager) DetachVolume(ctx context.Context, volumeID string, instanceID string) error {
	vmid, err := strconv.Atoi(instanceID)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidVMID, instanceID)
	}

	node, err := m.client.getNode(ctx)
	if err != nil {
		return err
	}

	qemuSvc := m.client.getQemuService()

	// Get VM config to find the disk ID
	config, err := qemuSvc.Config(ctx, node, vmid)
	if err != nil {
		return fmt.Errorf("failed to get VM config: %w", err)
	}

	diskID, found := findDiskSlotForVolume(config, volumeID)
	if !found {
		return fmt.Errorf("%w: %s on VM %d", ErrVolumeNotFoundOnVM, volumeID, vmid)
	}

	// Detach the disk
	err = qemuSvc.DetachDisk(ctx, node, vmid, diskID)
	if err != nil {
		return fmt.Errorf("failed to detach volume: %w", err)
	}

	return nil
}

// ResizeVolume resizes a volume.
func (m *StorageManager) ResizeVolume(_ctx context.Context, _id string, _size int) error {
	// Volume resizing in Proxmox is done through the VM
	// This requires knowing which VM the volume is attached to
	logger.Warnf("Volume resize requires the volume to be attached to a VM")

	return ErrVolumeResizeUnsupported
}

// DeleteVolume deletes a volume.
func (m *StorageManager) DeleteVolume(ctx context.Context, id string) error { //nolint:varnamelen // id is clear in context
	node, err := m.client.getNode(ctx)
	if err != nil {
		return err
	}

	// Parse storage from volume ID
	parts := strings.Split(id, ":")
	if len(parts) != 2 { //nolint:mnd
		return fmt.Errorf("%w: %s", ErrInvalidVolumeIDFormat, id)
	}

	storage := parts[0]

	storageSvc := m.client.getStorageService()

	err = storageSvc.DeleteVolume(ctx, node, storage, id)
	if err != nil {
		return fmt.Errorf("failed to delete volume: %w", err)
	}

	return nil
}

// Snapshot operations

// CreateSnapshot creates a snapshot of a VM.
func (m *StorageManager) CreateSnapshot(ctx context.Context, volumeID string, name string) (*cpi.Snapshot, error) {
	// In Proxmox, snapshots are at the VM level, not volume level
	// volumeID here is expected to be the VMID
	vmid, err := strconv.Atoi(volumeID)
	if err != nil {
		return nil, fmt.Errorf("%w for snapshot: %s", ErrInvalidVMID, volumeID)
	}

	node, err := m.client.getNode(ctx)
	if err != nil {
		return nil, err
	}

	qemuSvc := m.client.getQemuService()

	upid, err := qemuSvc.Snapshot(ctx, node, vmid, name, map[string]interface{}{
		"description": "Snapshot created at " + time.Now().Format(time.RFC3339),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot: %w", err)
	}

	// Wait for snapshot to complete
	err = m.client.waitForTask(ctx, node, upid, taskTimeoutSnapshot)
	if err != nil {
		return nil, fmt.Errorf("snapshot task failed: %w", err)
	}

	return &cpi.Snapshot{
		ID:          name,
		Name:        name,
		VolumeID:    volumeID,
		State:       cpi.ResourceStateAvailable,
		Description: fmt.Sprintf("VM %d snapshot", vmid),
		CreatedAt:   time.Now(),
		Tags:        make(map[string]string),
	}, nil
}

// GetSnapshot retrieves a snapshot.
func (m *StorageManager) GetSnapshot(ctx context.Context, id string) (*cpi.Snapshot, error) { //nolint:varnamelen // id is clear in context
	// Snapshot ID format: vmid:snapshotname
	parts := strings.Split(id, ":")
	if len(parts) != snapshotIDPartCount {
		return nil, fmt.Errorf("%w: %s (expected vmid:snapshotname)", ErrInvalidSnapshotIDFormat, id)
	}

	vmid, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w in snapshot ID: %s", ErrInvalidVMID, parts[0])
	}

	snapshotName := parts[1]

	node, err := m.client.getNode(ctx)
	if err != nil {
		return nil, err
	}

	qemuSvc := m.client.getQemuService()

	snapshots, err := qemuSvc.ListSnapshots(ctx, node, vmid)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	for _, snap := range snapshots {
		name := getStringFromMap(snap, "name")
		if name == snapshotName {
			return &cpi.Snapshot{
				ID:          id,
				Name:        name,
				VolumeID:    parts[0],
				State:       cpi.ResourceStateAvailable,
				Description: getStringFromMap(snap, "description"),
				Tags:        make(map[string]string),
			}, nil
		}
	}

	return nil, ErrSnapshotNotFound
}

// ListSnapshots lists snapshots for a VM.
func (m *StorageManager) ListSnapshots(ctx context.Context, volumeID string, filters map[string]string) ([]*cpi.Snapshot, error) {
	vmid, err := strconv.Atoi(volumeID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidVMID, volumeID)
	}

	node, err := m.client.getNode(ctx)
	if err != nil {
		return nil, err
	}

	qemuSvc := m.client.getQemuService()

	snapshots, err := qemuSvc.ListSnapshots(ctx, node, vmid)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	var result []*cpi.Snapshot

	for _, snap := range snapshots {
		name := getStringFromMap(snap, "name")

		// Skip 'current' pseudo-snapshot
		if name == "current" {
			continue
		}

		// Apply name filter
		if nameFilter, ok := filters["name"]; ok && name != nameFilter {
			continue
		}

		result = append(result, &cpi.Snapshot{
			ID:          fmt.Sprintf("%d:%s", vmid, name),
			Name:        name,
			VolumeID:    volumeID,
			State:       cpi.ResourceStateAvailable,
			Description: getStringFromMap(snap, "description"),
			Tags:        make(map[string]string),
		})
	}

	return result, nil
}

// DeleteSnapshot deletes a snapshot.
func (m *StorageManager) DeleteSnapshot(ctx context.Context, id string) error {
	// Snapshot ID format: vmid:snapshotname
	parts := strings.Split(id, ":")
	if len(parts) != snapshotIDPartCount {
		return fmt.Errorf("%w: %s", ErrInvalidSnapshotIDFormat, id)
	}

	vmid, err := strconv.Atoi(parts[0])
	if err != nil {
		return fmt.Errorf("%w in snapshot ID: %s", ErrInvalidVMID, parts[0])
	}

	snapshotName := parts[1]

	node, err := m.client.getNode(ctx)
	if err != nil {
		return err
	}

	qemuSvc := m.client.getQemuService()

	err = qemuSvc.DeleteSnapshot(ctx, node, vmid, snapshotName)
	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	return nil
}

// Object storage operations.
//
// PVE has no native object storage. When BlobstoreMode is "local" (the
// default) all bucket methods short-circuit with ErrBucketsNotSupported and
// SupportsStorage() reports false so the bootstrap layer skips bucket
// creation. When BlobstoreMode is "external" the methods route to an
// S3-compatible endpoint (Ceph RGW, RustFS, etc.) wired in blobstore.go.

// CreateBucket creates a bucket. External mode only.
func (m *StorageManager) CreateBucket(ctx context.Context, req *cpi.BucketRequest) (*cpi.Bucket, error) {
	if !m.client.config.isExternalBlobstore() {
		return nil, ErrBucketsNotSupported
	}

	return m.createBucketExternal(ctx, req)
}

// GetBucket retrieves a bucket. External mode only.
func (m *StorageManager) GetBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	if !m.client.config.isExternalBlobstore() {
		return nil, ErrBucketsNotSupported
	}

	return m.getBucketExternal(ctx, name)
}

// ListBuckets lists buckets. Returns an empty slice in local mode so callers
// that walk all buckets degrade gracefully.
func (m *StorageManager) ListBuckets(ctx context.Context) ([]*cpi.Bucket, error) {
	if !m.client.config.isExternalBlobstore() {
		return []*cpi.Bucket{}, nil
	}

	return m.listBucketsExternal(ctx)
}

// DeleteBucket deletes a bucket. External mode only.
func (m *StorageManager) DeleteBucket(ctx context.Context, name string) error {
	if !m.client.config.isExternalBlobstore() {
		return ErrBucketsNotSupported
	}

	return m.deleteBucketExternal(ctx, name)
}

// EmptyBucket empties a bucket. External mode only.
func (m *StorageManager) EmptyBucket(ctx context.Context, name string) error {
	if !m.client.config.isExternalBlobstore() {
		return ErrBucketsNotSupported
	}

	return m.emptyBucketExternal(ctx, name)
}

// IsBucketEmpty checks if a bucket is empty. External mode only.
func (m *StorageManager) IsBucketEmpty(ctx context.Context, name string) (bool, error) {
	if !m.client.config.isExternalBlobstore() {
		return false, ErrBucketsNotSupported
	}

	return m.isBucketEmptyExternal(ctx, name)
}

// CreateCredentialsGroup is a Stackit-specific concept; for PVE external mode
// the operator pre-provisions S3 credentials. Return a stub so callers that
// always invoke this don't break.
func (m *StorageManager) CreateCredentialsGroup(_ctx context.Context, req *cpi.CredentialsGroupRequest) (*cpi.CredentialsGroup, error) {
	if !m.client.config.isExternalBlobstore() {
		return nil, ErrBucketsNotSupported
	}

	name := ""
	if req != nil {
		name = req.Name
	}

	return &cpi.CredentialsGroup{
		ID:        name,
		Name:      name,
		CreatedAt: time.Now(),
	}, nil
}
