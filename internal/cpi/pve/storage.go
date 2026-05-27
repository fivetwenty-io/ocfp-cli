package pve

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

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

	// Omit format so PVE picks the storage's native default (qcow2 for
	// dir/NFS, raw for LVM/ZFS/RBD). Forcing qcow2 here breaks every block
	// storage type because ZFSPoolPlugin et al. reject it.
	volID, err := storageSvc.CreateVolume(ctx, node, storage, sizeGB, "", vmid, volName)
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
	path := fmt.Sprintf("/nodes/%s/storage/%s/content/%s", node, storage, id)

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
func (m *StorageManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	node, err := m.client.getNode(ctx)
	if err != nil {
		return nil, err
	}

	storage := m.client.config.DefaultStorage
	if storageFilter, ok := filters["storage"]; ok {
		storage = storageFilter
	}

	// List storage content
	path := fmt.Sprintf("/nodes/%s/storage/%s/content", node, storage)

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

		volumes = append(volumes, &cpi.Volume{
			ID:    volID,
			Name:  volID,
			Size:  sizeGB,
			Type:  storage,
			State: cpi.ResourceStateAvailable,
			Tags:  make(map[string]string),
		})
	}

	return volumes, nil
}

// AttachVolume attaches a volume to an instance.
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

	// Determine bus and device ID
	bus := "scsi"
	if device != "" && strings.HasPrefix(device, "virtio") {
		bus = "virtio"
	}

	// Attach the disk
	_, err = qemuSvc.AttachDisk(ctx, node, vmid, volumeID, bus, nil)
	if err != nil {
		return fmt.Errorf("failed to attach volume: %w", err)
	}

	return nil
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

	// Find the disk ID that matches the volume
	var diskID string

	for key, value := range config {
		if strings.HasPrefix(key, "scsi") || strings.HasPrefix(key, "virtio") || strings.HasPrefix(key, "ide") {
			if valueStr, ok := value.(string); ok && strings.Contains(valueStr, volumeID) {
				diskID = key

				break
			}
		}
	}

	if diskID == "" {
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
