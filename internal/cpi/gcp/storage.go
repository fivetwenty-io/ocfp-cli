package gcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CreateVolume creates a persistent disk.
//
//nolint:dupl // intentionally similar CPI implementation
func (m *StorageManager) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	if req.AvailabilityZone != "" {
		zone = req.AvailabilityZone
	}

	labels := BuildLabels(req.Name, req.Tags)

	size := int64(req.Size)
	if req.SizeGB > 0 {
		size = int64(req.SizeGB)
	}

	diskType := "pd-balanced"
	if req.Type != "" {
		diskType = req.Type
	} else if req.VolumeType != "" {
		diskType = req.VolumeType
	}

	disk := &computepb.Disk{
		Name:   proto(req.Name),
		SizeGb: proto(size),
		Type:   proto(fmt.Sprintf("zones/%s/diskTypes/%s", zone, diskType)),
		Labels: labels,
	}

	op, err := m.client.getDisksClient().Insert(ctx, &computepb.InsertDiskRequest{ //nolint:varnamelen
		Project:      projectID,
		Zone:         zone,
		DiskResource: disk,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateVolume")
	}

	err = op.Wait(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "CreateVolume.Wait")
	}

	logger.Debugw("Created persistent disk", "name", req.Name, "size", size, "zone", zone)

	return m.GetVolume(ctx, req.Name)
}

// GetVolume retrieves a persistent disk by name.
func (m *StorageManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	disk, err := m.client.getDisksClient().Get(ctx, &computepb.GetDiskRequest{
		Project: projectID,
		Zone:    zone,
		Disk:    id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetVolume")
	}

	return m.convertDisk(disk), nil
}

// ListVolumes lists persistent disks.
func (m *StorageManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	var volumes []*cpi.Volume

	it := m.client.getDisksClient().List(ctx, &computepb.ListDisksRequest{ //nolint:varnamelen
		Project: projectID,
		Zone:    zone,
	})

	for {
		disk, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListVolumes")
		}

		if matchesStorageLabelFilters(disk.GetLabels(), filters) {
			volumes = append(volumes, m.convertDisk(disk))
		}
	}

	return volumes, nil
}

// AttachVolume attaches a persistent disk to an instance.
func (m *StorageManager) AttachVolume(ctx context.Context, volumeID string, instanceID string, device string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	diskURL := FormatDiskURL(projectID, zone, volumeID)

	attachedDisk := &computepb.AttachedDisk{
		Source:     proto(diskURL),
		AutoDelete: proto(false),
	}

	if device != "" {
		attachedDisk.DeviceName = proto(device)
	}

	op, err := m.client.getInstancesClient().AttachDisk(ctx, &computepb.AttachDiskInstanceRequest{ //nolint:varnamelen
		Project:              projectID,
		Zone:                 zone,
		Instance:             instanceID,
		AttachedDiskResource: attachedDisk,
	})
	if err != nil {
		return WrapGCPError(err, "AttachVolume")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "AttachVolume.Wait")
	}

	logger.Debugw("Attached disk to instance", "disk", volumeID, "instance", instanceID)

	return nil
}

// DetachVolume detaches a persistent disk from an instance.
func (m *StorageManager) DetachVolume(ctx context.Context, volumeID string, instanceID string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	op, err := m.client.getInstancesClient().DetachDisk(ctx, &computepb.DetachDiskInstanceRequest{ //nolint:varnamelen
		Project:    projectID,
		Zone:       zone,
		Instance:   instanceID,
		DeviceName: volumeID,
	})
	if err != nil {
		return WrapGCPError(err, "DetachVolume")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DetachVolume.Wait")
	}

	logger.Debugw("Detached disk from instance", "disk", volumeID, "instance", instanceID)

	return nil
}

// ResizeVolume resizes a persistent disk.
func (m *StorageManager) ResizeVolume(ctx context.Context, id string, size int) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	op, err := m.client.getDisksClient().Resize(ctx, &computepb.ResizeDiskRequest{ //nolint:varnamelen
		Project: projectID,
		Zone:    zone,
		Disk:    id,
		DisksResizeRequestResource: &computepb.DisksResizeRequest{
			SizeGb: proto(int64(size)),
		},
	})
	if err != nil {
		return WrapGCPError(err, "ResizeVolume")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "ResizeVolume.Wait")
	}

	logger.Debugw("Resized disk", "name", id, "newSize", size)

	return nil
}

// DeleteVolume deletes a persistent disk.
func (m *StorageManager) DeleteVolume(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	op, err := m.client.getDisksClient().Delete(ctx, &computepb.DeleteDiskRequest{ //nolint:varnamelen
		Project: projectID,
		Zone:    zone,
		Disk:    id,
	})
	if err != nil {
		return WrapGCPError(err, "DeleteVolume")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DeleteVolume.Wait")
	}

	logger.Debugw("Deleted persistent disk", "name", id, "zone", zone)

	return nil
}

// CreateSnapshot creates a disk snapshot.
func (m *StorageManager) CreateSnapshot(ctx context.Context, volumeID string, name string) (*cpi.Snapshot, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	diskURL := FormatDiskURL(projectID, zone, volumeID)

	snapshot := &computepb.Snapshot{
		Name:       proto(name),
		SourceDisk: proto(diskURL),
	}

	op, err := m.client.getSnapshotsClient().Insert(ctx, &computepb.InsertSnapshotRequest{ //nolint:varnamelen
		Project:          projectID,
		SnapshotResource: snapshot,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateSnapshot")
	}

	err = op.Wait(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "CreateSnapshot.Wait")
	}

	logger.Debugw("Created snapshot", "name", name, "sourceDisk", volumeID)

	return m.GetSnapshot(ctx, name)
}

// GetSnapshot retrieves a snapshot by name.
func (m *StorageManager) GetSnapshot(ctx context.Context, id string) (*cpi.Snapshot, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID

	snapshot, err := m.client.getSnapshotsClient().Get(ctx, &computepb.GetSnapshotRequest{
		Project:  projectID,
		Snapshot: id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetSnapshot")
	}

	return m.convertSnapshot(snapshot), nil
}

// ListSnapshots lists snapshots.
func (m *StorageManager) ListSnapshots(ctx context.Context, volumeID string, filters map[string]string) ([]*cpi.Snapshot, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID

	var snapshots []*cpi.Snapshot

	it := m.client.getSnapshotsClient().List(ctx, &computepb.ListSnapshotsRequest{
		Project: projectID,
	})

	for {
		snapshot, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListSnapshots")
		}

		// Filter by volume ID if specified
		if volumeID != "" {
			sourceDisk := ExtractNameFromURL(snapshot.GetSourceDisk())
			if sourceDisk != volumeID {
				continue
			}
		}

		if matchesStorageLabelFilters(snapshot.GetLabels(), filters) {
			snapshots = append(snapshots, m.convertSnapshot(snapshot))
		}
	}

	return snapshots, nil
}

// DeleteSnapshot deletes a snapshot.
func (m *StorageManager) DeleteSnapshot(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID

	op, err := m.client.getSnapshotsClient().Delete(ctx, &computepb.DeleteSnapshotRequest{ //nolint:varnamelen
		Project:  projectID,
		Snapshot: id,
	})
	if err != nil {
		return WrapGCPError(err, "DeleteSnapshot")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DeleteSnapshot.Wait")
	}

	logger.Debugw("Deleted snapshot", "name", id)

	return nil
}

// CreateBucket creates a Cloud Storage bucket.
func (m *StorageManager) CreateBucket(ctx context.Context, req *cpi.BucketRequest) (*cpi.Bucket, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	bucket := m.client.getStorageClient().Bucket(req.Name)

	attrs := &storage.BucketAttrs{
		Location:     region,
		StorageClass: "STANDARD",
		Labels:       BuildLabels(req.Name, req.Tags),
	}

	err = bucket.Create(ctx, projectID, attrs)
	if err != nil {
		return nil, WrapGCPError(err, "CreateBucket")
	}

	logger.Debugw("Created Cloud Storage bucket", "name", req.Name, "region", region)

	return m.GetBucket(ctx, req.Name)
}

// GetBucket retrieves a bucket by name.
func (m *StorageManager) GetBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	bucket := m.client.getStorageClient().Bucket(name)

	attrs, err := bucket.Attrs(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "GetBucket")
	}

	return m.convertBucket(attrs), nil
}

// ListBuckets lists Cloud Storage buckets.
func (m *StorageManager) ListBuckets(ctx context.Context) ([]*cpi.Bucket, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID

	var buckets []*cpi.Bucket

	it := m.client.getStorageClient().Buckets(ctx, projectID)

	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListBuckets")
		}

		buckets = append(buckets, m.convertBucket(attrs))
	}

	return buckets, nil
}

// DeleteBucket deletes a Cloud Storage bucket.
func (m *StorageManager) DeleteBucket(ctx context.Context, name string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	bucket := m.client.getStorageClient().Bucket(name)

	err = bucket.Delete(ctx)
	if err != nil {
		return WrapGCPError(err, "DeleteBucket")
	}

	logger.Debugw("Deleted Cloud Storage bucket", "name", name)

	return nil
}

// EmptyBucket deletes all objects in a bucket.
func (m *StorageManager) EmptyBucket(ctx context.Context, name string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	bucket := m.client.getStorageClient().Bucket(name)
	it := bucket.Objects(ctx, nil)

	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return WrapGCPError(err, "EmptyBucket.List")
		}

		err = bucket.Object(attrs.Name).Delete(ctx)
		if err != nil {
			return WrapGCPError(err, "EmptyBucket.DeleteObject")
		}
	}

	logger.Debugw("Emptied Cloud Storage bucket", "name", name)

	return nil
}

// IsBucketEmpty checks if a bucket is empty.
func (m *StorageManager) IsBucketEmpty(ctx context.Context, name string) (bool, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return false, err
	}

	bucket := m.client.getStorageClient().Bucket(name)
	it := bucket.Objects(ctx, &storage.Query{Prefix: ""})

	_, err = it.Next()
	if errors.Is(err, iterator.Done) {
		return true, nil
	}

	if err != nil {
		return false, WrapGCPError(err, "IsBucketEmpty")
	}

	return false, nil
}

// CreateCredentialsGroup creates a credentials group (not applicable for GCP).
func (m *StorageManager) CreateCredentialsGroup(ctx context.Context, req *cpi.CredentialsGroupRequest) (*cpi.CredentialsGroup, error) {
	// GCP uses IAM for credentials, not credentials groups
	return &cpi.CredentialsGroup{
		Name:      req.Name,
		CreatedAt: time.Now(),
	}, nil
}

// Helper functions

func (m *StorageManager) convertDisk(disk *computepb.Disk) *cpi.Volume {
	var attachedTo string
	if len(disk.GetUsers()) > 0 {
		attachedTo = ExtractNameFromURL(disk.GetUsers()[0])
	}

	return &cpi.Volume{
		ID:         strconv.FormatUint(disk.GetId(), 10),
		Name:       disk.GetName(),
		Size:       int(disk.GetSizeGb()),
		Type:       ExtractNameFromURL(disk.GetType()),
		State:      MapDiskStateToResourceState(disk.GetStatus()),
		AttachedTo: attachedTo,
		Tags:       disk.GetLabels(),
		CreatedAt:  ParseTimestamp(disk.GetCreationTimestamp()),
	}
}

func (m *StorageManager) convertSnapshot(snapshot *computepb.Snapshot) *cpi.Snapshot {
	return &cpi.Snapshot{
		ID:          strconv.FormatUint(snapshot.GetId(), 10),
		Name:        snapshot.GetName(),
		VolumeID:    ExtractNameFromURL(snapshot.GetSourceDisk()),
		Size:        int(snapshot.GetDiskSizeGb()),
		State:       MapGCPStateToResourceState(snapshot.GetStatus()),
		Description: snapshot.GetDescription(),
		Tags:        snapshot.GetLabels(),
		CreatedAt:   ParseTimestamp(snapshot.GetCreationTimestamp()),
	}
}

func (m *StorageManager) convertBucket(attrs *storage.BucketAttrs) *cpi.Bucket {
	return &cpi.Bucket{
		Name:         attrs.Name,
		Region:       attrs.Location,
		StorageClass: attrs.StorageClass,
		Versioning:   attrs.VersioningEnabled,
		Tags:         attrs.Labels,
		CreatedAt:    attrs.Created,
	}
}

func matchesStorageLabelFilters(labels map[string]string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for k, v := range filters {
		if labels[k] != v {
			return false
		}
	}

	return true
}
