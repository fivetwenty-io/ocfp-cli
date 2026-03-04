package proxmox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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

	// Generate volume name
	volName := req.Name
	if volName == "" {
		volName = fmt.Sprintf("vol-%d", time.Now().UnixNano())
	}

	// Create the volume
	volID, err := storageSvc.CreateVolume(ctx, node, storage, sizeGB, "qcow2", 0, volName)
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

// Object storage operations (not natively supported)

// CreateBucket creates a bucket (not supported).
func (m *StorageManager) CreateBucket(_ctx context.Context, _req *cpi.BucketRequest) (*cpi.Bucket, error) {
	return nil, ErrBucketsNotSupported
}

// GetBucket retrieves a bucket.
func (m *StorageManager) GetBucket(_ctx context.Context, _name string) (*cpi.Bucket, error) {
	return nil, ErrBucketsNotSupported
}

// ListBuckets lists buckets.
func (m *StorageManager) ListBuckets(_ctx context.Context) ([]*cpi.Bucket, error) {
	return []*cpi.Bucket{}, nil
}

// DeleteBucket deletes a bucket.
func (m *StorageManager) DeleteBucket(_ctx context.Context, _name string) error {
	return ErrBucketsNotSupported
}

// EmptyBucket empties a bucket.
func (m *StorageManager) EmptyBucket(_ctx context.Context, _name string) error {
	return ErrBucketsNotSupported
}

// IsBucketEmpty checks if a bucket is empty.
func (m *StorageManager) IsBucketEmpty(_ctx context.Context, _name string) (bool, error) {
	return false, ErrBucketsNotSupported
}

// CreateCredentialsGroup creates a credentials group (not supported).
func (m *StorageManager) CreateCredentialsGroup(_ctx context.Context, _req *cpi.CredentialsGroupRequest) (*cpi.CredentialsGroup, error) {
	return nil, ErrBucketsNotSupported
}
