package gcp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

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

	if req.InstanceID != "" {
		instance, lookupErr := (&ComputeManager{client: m.client}).GetInstance(ctx, req.InstanceID)
		if lookupErr != nil {
			return nil, lookupErr
		}

		projectID, zone, _ = resourceScope(projectID, zone, instance.ID)
		if req.AvailabilityZone != "" && req.AvailabilityZone != zone {
			return nil, errors.New("volume zone differs from owning instance")
		}
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
		wrapped := WrapGCPError(err, "CreateVolume")
		if IsAlreadyExists(wrapped) {
			existing, getErr := m.client.getDisksClient().Get(ctx, &computepb.GetDiskRequest{Project: projectID, Zone: zone, Disk: req.Name})
			if getErr != nil {
				return nil, WrapGCPError(getErr, "CreateVolume.Reconcile")
			}

			if len(req.Tags) == 0 || !matchesLabelFilters(existing.GetLabels(), req.Tags) || existing.GetSizeGb() != size || ExtractNameFromURL(existing.GetType()) != diskType {
				return nil, fmt.Errorf("existing disk %s does not match requested ownership/configuration", req.Name)
			}

			return m.convertDisk(existing), nil
		}

		return nil, wrapped
	}

	err = op.Wait(ctx)
	if err != nil {
		return &cpi.Volume{ID: scopedResource(projectID, zone, "disks", req.Name), Name: req.Name}, WrapGCPError(err, "CreateVolume.Wait")
	}

	logger.Debugw("Created persistent disk", "name", req.Name, "size", size, "zone", zone)

	return m.GetVolume(ctx, scopedResource(projectID, zone, "disks", req.Name))
}

// GetVolume retrieves a persistent disk by name.
func (m *StorageManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	projectID, zone, id := resourceScope(projectID, config.Zone, id)

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
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	it := m.client.getDisksClient().AggregatedList(ctx, &computepb.AggregatedListDisksRequest{Project: m.client.getConfig().ProjectID})
	volumes := []*cpi.Volume{}
	for {
		pair, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, WrapGCPError(err, "ListVolumes")
		}

		for _, disk := range pair.Value.GetDisks() {
			if matchesStorageLabelFilters(disk.GetLabels(), filters) {
				volumes = append(volumes, m.convertDisk(disk))
			}
		}
	}
	return volumes, nil
}

// AttachVolume attaches a persistent disk to an instance.
func (m *StorageManager) AttachVolume(ctx context.Context, volumeID, instanceID, device string) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	if isNumericResource(volumeID) {
		volume, err := m.GetVolume(ctx, volumeID)
		if err != nil {
			return err
		}

		volumeID = volume.ID
	}

	if isNumericResource(instanceID) {
		instance, err := (&ComputeManager{client: m.client}).GetInstance(ctx, instanceID)
		if err != nil {
			return err
		}

		instanceID = instance.ID
	}

	cfg := m.client.getConfig()
	project, zone, instance := resourceScope(cfg.ProjectID, cfg.Zone, instanceID)

	diskProject, diskZone, disk := resourceScope(project, zone, volumeID)
	if diskProject != project || diskZone != zone {
		return errors.New("disk and instance must be in the same project and zone")
	}

	if device == "" {
		device = disk
	}

	if strings.ContainsAny(device, "/,=") {
		return fmt.Errorf("GCP device name must be a bare name, got %q", device)
	}

	vm, err := m.client.getInstancesClient().Get(ctx, &computepb.GetInstanceRequest{Project: project, Zone: zone, Instance: instance})
	if err != nil {
		return WrapGCPError(err, "AttachVolume.GetInstance")
	}

	for _, attached := range vm.GetDisks() {
		if canonicalURL(attached.GetSource()) == canonicalURL(FormatDiskURL(project, zone, disk)) {
			if attached.GetBoot() || attached.GetAutoDelete() {
				return errors.New("refusing to reuse boot or auto-delete disk")
			}

			if attached.GetDeviceName() != device {
				return fmt.Errorf("disk already attached under different device name %s", attached.GetDeviceName())
			}

			return nil
		}

		if attached.GetDeviceName() == device {
			return fmt.Errorf("device %s already occupied", device)
		}
	}

	op, err := m.client.getInstancesClient().AttachDisk(ctx, &computepb.AttachDiskInstanceRequest{Project: project, Zone: zone, Instance: instance, AttachedDiskResource: &computepb.AttachedDisk{Source: proto(FormatDiskURL(project, zone, disk)), DeviceName: proto(device), AutoDelete: proto(false), Mode: proto("READ_WRITE"), Type: proto("PERSISTENT")}})
	if err != nil {
		return WrapGCPError(err, "AttachVolume")
	}

	return WrapGCPError(op.Wait(ctx), "AttachVolume.Wait")
}

func (m *StorageManager) DetachVolume(ctx context.Context, volumeID, instanceID string) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	if isNumericResource(volumeID) {
		volume, err := m.GetVolume(ctx, volumeID)
		if err != nil {
			return err
		}

		volumeID = volume.ID
	}

	if isNumericResource(instanceID) {
		instance, err := (&ComputeManager{client: m.client}).GetInstance(ctx, instanceID)
		if err != nil {
			return err
		}

		instanceID = instance.ID
	}

	cfg := m.client.getConfig()
	project, zone, instance := resourceScope(cfg.ProjectID, cfg.Zone, instanceID)

	diskProject, diskZone, disk := resourceScope(project, zone, volumeID)
	if diskProject != project || diskZone != zone {
		return errors.New("disk and instance must be in the same project and zone")
	}

	vm, err := m.client.getInstancesClient().Get(ctx, &computepb.GetInstanceRequest{Project: project, Zone: zone, Instance: instance})
	if err != nil {
		return WrapGCPError(err, "DetachVolume.GetInstance")
	}

	device := ""

	for _, attached := range vm.GetDisks() {
		if canonicalURL(attached.GetSource()) == canonicalURL(FormatDiskURL(project, zone, disk)) {
			if attached.GetBoot() {
				return errors.New("refusing to detach boot disk")
			}

			device = attached.GetDeviceName()

			break
		}
	}

	if device == "" {
		return nil
	}

	op, err := m.client.getInstancesClient().DetachDisk(ctx, &computepb.DetachDiskInstanceRequest{Project: project, Zone: zone, Instance: instance, DeviceName: device})
	if err != nil {
		return WrapGCPError(err, "DetachVolume")
	}

	return WrapGCPError(op.Wait(ctx), "DetachVolume.Wait")
}

// ResizeVolume resizes a persistent disk.
func (m *StorageManager) ResizeVolume(ctx context.Context, id string, size int) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	projectID, zone, id := resourceScope(projectID, config.Zone, id)

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
	projectID, zone, id := resourceScope(projectID, config.Zone, id)

	op, err := m.client.getDisksClient().Delete(ctx, &computepb.DeleteDiskRequest{ //nolint:varnamelen
		Project: projectID,
		Zone:    zone,
		Disk:    id,
	})
	if err != nil {
		wrapped := WrapGCPError(err, "DeleteVolume")
		if IsNotFound(wrapped) {
			return nil
		}

		return wrapped
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
			sourceDisk := canonicalURL(snapshot.GetSourceDisk())

			wanted := canonicalURL(FormatDiskURL(projectID, config.Zone, volumeID))
			if sourceDisk != wanted {
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
		wrapped := WrapGCPError(err, "DeleteSnapshot")
		if IsNotFound(wrapped) {
			return nil
		}

		return wrapped
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
	it := bucket.Objects(ctx, &storage.Query{Versions: true})

	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return WrapGCPError(err, "EmptyBucket.List")
		}

		err = bucket.Object(attrs.Name).Generation(attrs.Generation).Delete(ctx)
		if err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
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
	it := bucket.Objects(ctx, &storage.Query{Versions: true})

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
func (m *StorageManager) CreateCredentialsGroup(_ctx context.Context, req *cpi.CredentialsGroupRequest) (*cpi.CredentialsGroup, error) {
	return nil, fmt.Errorf("%w: GCP uses IAM credentials, not credentials groups", ErrNotImplemented)
}

// Helper functions

func (m *StorageManager) convertDisk(disk *computepb.Disk) *cpi.Volume {
	var attachedTo string
	if len(disk.GetUsers()) > 0 {
		attachedTo = strings.TrimPrefix(disk.GetUsers()[0], "https://www.googleapis.com/compute/v1/")
	}

	return &cpi.Volume{
		ID:         resourceIdentity(m.client.getConfig(), disk.GetZone(), "disks", disk.GetName()),
		Name:       disk.GetName(),
		Size:       int(disk.GetSizeGb()),
		Type:       ExtractNameFromURL(disk.GetType()),
		State:      diskState(disk),
		AttachedTo: attachedTo,
		Tags:       decodedLabels(disk.GetLabels()),
		CreatedAt:  ParseTimestamp(disk.GetCreationTimestamp()),
	}
}

func (m *StorageManager) convertSnapshot(snapshot *computepb.Snapshot) *cpi.Snapshot {
	return &cpi.Snapshot{
		ID:          snapshot.GetName(),
		Name:        snapshot.GetName(),
		VolumeID:    canonicalURL(snapshot.GetSourceDisk()),
		Size:        int(snapshot.GetDiskSizeGb()),
		State:       snapshotState(snapshot.GetStatus()),
		Description: snapshot.GetDescription(),
		Tags:        decodedLabels(snapshot.GetLabels()),
		CreatedAt:   ParseTimestamp(snapshot.GetCreationTimestamp()),
	}
}

func (m *StorageManager) convertBucket(attrs *storage.BucketAttrs) *cpi.Bucket {
	return &cpi.Bucket{
		Name:         attrs.Name,
		Region:       attrs.Location,
		StorageClass: attrs.StorageClass,
		Versioning:   attrs.VersioningEnabled,
		Tags:         decodedLabels(attrs.Labels),
		CreatedAt:    attrs.Created,
	}
}

func matchesStorageLabelFilters(labels map[string]string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for k, v := range filters {
		cleanKey := SanitizeLabel(stripLabelPrefix(k))
		if labels[cleanKey] != SanitizeLabel(v) {
			return false
		}
	}

	return true
}

func diskState(disk *computepb.Disk) cpi.ResourceState {
	if len(disk.GetUsers()) > 0 {
		return cpi.ResourceStateInUse
	}

	return MapDiskStateToResourceState(disk.GetStatus())
}

func (m *StorageManager) SetBucketVersioning(ctx context.Context, name string, enabled bool) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	_, err := m.client.getStorageClient().Bucket(name).Update(ctx, storage.BucketAttrsToUpdate{VersioningEnabled: enabled})

	return WrapGCPError(err, "SetBucketVersioning")
}
func (m *StorageManager) SetBucketLifecycle(ctx context.Context, name string, noncurrentDays int) error {
	if noncurrentDays < 1 {
		return errors.New("noncurrent retention days must be positive")
	}

	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	bucket := m.client.getStorageClient().Bucket(name)

	attrs, err := bucket.Attrs(ctx)
	if err != nil {
		return WrapGCPError(err, "SetBucketLifecycle.Get")
	}

	rules := attrs.Lifecycle.Rules
	replacement := storage.LifecycleRule{Action: storage.LifecycleAction{Type: storage.DeleteAction}, Condition: storage.LifecycleCondition{DaysSinceNoncurrentTime: int64(noncurrentDays), Liveness: storage.Archived}}
	// Preserve unrelated lifecycle rules; replace only our noncurrent expiry rule.
	kept := make([]storage.LifecycleRule, 0, len(rules)+1)
	for _, rule := range rules {
		condition := rule.Condition

		condition.DaysSinceNoncurrentTime = 0
		if rule.Action.Type == storage.DeleteAction && rule.Condition.DaysSinceNoncurrentTime > 0 && reflect.DeepEqual(condition, storage.LifecycleCondition{Liveness: storage.Archived}) {
			continue
		}

		kept = append(kept, rule)
	}

	kept = append(kept, replacement)
	_, err = bucket.If(storage.BucketConditions{MetagenerationMatch: attrs.MetaGeneration}).Update(ctx, storage.BucketAttrsToUpdate{Lifecycle: &storage.Lifecycle{Rules: kept}})

	return WrapGCPError(err, "SetBucketLifecycle")
}

func snapshotState(status string) cpi.ResourceState {
	if status == "READY" {
		return cpi.ResourceStateAvailable
	}

	return MapGCPStateToResourceState(status)
}
