package azure

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CreateVolume creates a new managed disk.
func (m *StorageManager) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	if req == nil {
		return nil, ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	// Ensure resource group exists
	err = m.client.EnsureResourceGroup(ctx)
	if err != nil {
		return nil, err
	}

	// Determine disk size
	sizeGB := req.SizeGB
	if sizeGB == 0 {
		sizeGB = req.Size
	}
	if sizeGB == 0 {
		sizeGB = 32 // Default 32 GB
	}

	// Determine storage account type
	storageType := armcompute.DiskStorageAccountTypesPremiumLRS
	volumeType := req.VolumeType
	if volumeType == "" {
		volumeType = req.Type
	}
	switch strings.ToLower(volumeType) {
	case "standard", "standard_lrs":
		storageType = armcompute.DiskStorageAccountTypesStandardLRS
	case "standardssd", "standard_ssd", "standardssd_lrs":
		storageType = armcompute.DiskStorageAccountTypesStandardSSDLRS
	case "premium", "premium_lrs":
		storageType = armcompute.DiskStorageAccountTypesPremiumLRS
	case "ultra", "ultrassd_lrs":
		storageType = armcompute.DiskStorageAccountTypesUltraSSDLRS
	}

	// Prepare disk parameters
	diskParams := armcompute.Disk{
		Location: to.Ptr(m.client.getLocation()),
		Properties: &armcompute.DiskProperties{
			CreationData: &armcompute.CreationData{
				CreateOption: to.Ptr(armcompute.DiskCreateOptionEmpty),
			},
			DiskSizeGB: to.Ptr(int32(sizeGB)),
		},
		SKU: &armcompute.DiskSKU{
			Name: to.Ptr(storageType),
		},
		Tags: BuildTags(MergeTags(m.client.config.DefaultTags, req.Tags)),
	}

	// Add availability zone if specified
	if req.AvailabilityZone != "" && SupportsAvailabilityZones(m.client.getLocation()) {
		diskParams.Zones = []*string{to.Ptr(req.AvailabilityZone)}
	}

	// Enable encryption if requested
	if req.Encrypted {
		diskParams.Properties.Encryption = &armcompute.Encryption{
			Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithPlatformKey),
		}
	}

	poller, err := m.client.disksClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		req.Name,
		diskParams,
		nil,
	)
	if err != nil {
		return nil, WrapAzureError(err, "CreateVolume")
	}

	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateVolume")
	}

	logger.Infow("Created managed disk", "name", req.Name, "sizeGB", sizeGB)

	return m.diskToVolume(&result.Disk), nil
}

// GetVolume retrieves a managed disk by ID or name.
func (m *StorageManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	name := ExtractResourceName(id)

	result, err := m.client.disksClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return nil, WrapAzureError(err, "GetVolume")
	}

	return m.diskToVolume(&result.Disk), nil
}

// ListVolumes lists all managed disks.
func (m *StorageManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	pager := m.client.disksClient.NewListByResourceGroupPager(m.client.getResourceGroup(), nil)

	var volumes []*cpi.Volume
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListVolumes")
		}

		for _, disk := range page.Value {
			volume := m.diskToVolume(disk)
			if matchesVolumeFilters(volume.Tags, filters) {
				volumes = append(volumes, volume)
			}
		}
	}

	return volumes, nil
}

// AttachVolume attaches a managed disk to a virtual machine.
func (m *StorageManager) AttachVolume(ctx context.Context, volumeID string, instanceID string, device string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	diskName := ExtractResourceName(volumeID)
	vmName := ExtractResourceName(instanceID)

	// Get the disk
	disk, err := m.client.disksClient.Get(ctx, m.client.getResourceGroup(), diskName, nil)
	if err != nil {
		return WrapAzureError(err, "AttachVolume.GetDisk")
	}

	// Get the VM
	vm, err := m.client.virtualMachinesClient.Get(ctx, m.client.getResourceGroup(), vmName, nil)
	if err != nil {
		return WrapAzureError(err, "AttachVolume.GetVM")
	}

	// Add the disk to the VM's data disks
	if vm.Properties == nil || vm.Properties.StorageProfile == nil {
		return fmt.Errorf("VM %s has no storage profile", vmName)
	}

	// Determine next available LUN
	lun := int32(0)
	if vm.Properties.StorageProfile.DataDisks != nil {
		for _, dd := range vm.Properties.StorageProfile.DataDisks {
			if dd.Lun != nil && *dd.Lun >= lun {
				lun = *dd.Lun + 1
			}
		}
	}

	// Add the new data disk
	newDisk := &armcompute.DataDisk{
		Lun:          to.Ptr(lun),
		Name:         to.Ptr(diskName),
		CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesAttach),
		ManagedDisk: &armcompute.ManagedDiskParameters{
			ID: disk.ID,
		},
	}

	vm.Properties.StorageProfile.DataDisks = append(vm.Properties.StorageProfile.DataDisks, newDisk)

	// Update the VM
	poller, err := m.client.virtualMachinesClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		vmName,
		vm.VirtualMachine,
		nil,
	)
	if err != nil {
		return WrapAzureError(err, "AttachVolume.UpdateVM")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "AttachVolume.UpdateVM")
	}

	logger.Infow("Attached disk to VM", "disk", diskName, "vm", vmName, "lun", lun)

	return nil
}

// DetachVolume detaches a managed disk from a virtual machine.
func (m *StorageManager) DetachVolume(ctx context.Context, volumeID string, instanceID string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	diskName := ExtractResourceName(volumeID)
	vmName := ExtractResourceName(instanceID)

	// Get the VM
	vm, err := m.client.virtualMachinesClient.Get(ctx, m.client.getResourceGroup(), vmName, nil)
	if err != nil {
		return WrapAzureError(err, "DetachVolume.GetVM")
	}

	if vm.Properties == nil || vm.Properties.StorageProfile == nil || vm.Properties.StorageProfile.DataDisks == nil {
		return fmt.Errorf("VM %s has no data disks", vmName)
	}

	// Find and remove the disk
	var newDataDisks []*armcompute.DataDisk
	found := false
	for _, dd := range vm.Properties.StorageProfile.DataDisks {
		if dd.Name != nil && *dd.Name == diskName {
			found = true
			continue
		}
		newDataDisks = append(newDataDisks, dd)
	}

	if !found {
		return fmt.Errorf("disk %s not attached to VM %s", diskName, vmName)
	}

	vm.Properties.StorageProfile.DataDisks = newDataDisks

	// Update the VM
	poller, err := m.client.virtualMachinesClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		vmName,
		vm.VirtualMachine,
		nil,
	)
	if err != nil {
		return WrapAzureError(err, "DetachVolume.UpdateVM")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "DetachVolume.UpdateVM")
	}

	logger.Infow("Detached disk from VM", "disk", diskName, "vm", vmName)

	return nil
}

// ResizeVolume resizes a managed disk.
func (m *StorageManager) ResizeVolume(ctx context.Context, id string, size int) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	// Get current disk
	disk, err := m.client.disksClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "ResizeVolume.Get")
	}

	// Update disk size
	diskUpdate := armcompute.DiskUpdate{
		Properties: &armcompute.DiskUpdateProperties{
			DiskSizeGB: to.Ptr(int32(size)),
		},
	}

	poller, err := m.client.disksClient.BeginUpdate(ctx, m.client.getResourceGroup(), name, diskUpdate, nil)
	if err != nil {
		return WrapAzureError(err, "ResizeVolume")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "ResizeVolume")
	}

	logger.Infow("Resized disk", "name", name, "oldSize", DerefInt32(disk.Properties.DiskSizeGB), "newSize", size)

	return nil
}

// DeleteVolume deletes a managed disk.
func (m *StorageManager) DeleteVolume(ctx context.Context, id string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	poller, err := m.client.disksClient.BeginDelete(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteVolume")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteVolume")
	}

	logger.Infow("Deleted disk", "name", name)

	return nil
}

// CreateSnapshot creates a snapshot of a managed disk.
func (m *StorageManager) CreateSnapshot(ctx context.Context, volumeID string, name string) (*cpi.Snapshot, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	diskName := ExtractResourceName(volumeID)

	// Get the disk to get its ID
	disk, err := m.client.disksClient.Get(ctx, m.client.getResourceGroup(), diskName, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateSnapshot.GetDisk")
	}

	snapshotParams := armcompute.Snapshot{
		Location: to.Ptr(m.client.getLocation()),
		Properties: &armcompute.SnapshotProperties{
			CreationData: &armcompute.CreationData{
				CreateOption:     to.Ptr(armcompute.DiskCreateOptionCopy),
				SourceResourceID: disk.ID,
			},
		},
		Tags: m.client.buildDefaultTags(),
	}

	poller, err := m.client.snapshotsClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		name,
		snapshotParams,
		nil,
	)
	if err != nil {
		return nil, WrapAzureError(err, "CreateSnapshot")
	}

	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateSnapshot")
	}

	logger.Infow("Created snapshot", "name", name, "source", diskName)

	return m.snapshotToSnapshot(&result.Snapshot), nil
}

// GetSnapshot retrieves a snapshot by ID or name.
func (m *StorageManager) GetSnapshot(ctx context.Context, id string) (*cpi.Snapshot, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	name := ExtractResourceName(id)

	result, err := m.client.snapshotsClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return nil, WrapAzureError(err, "GetSnapshot")
	}

	return m.snapshotToSnapshot(&result.Snapshot), nil
}

// ListSnapshots lists all snapshots.
func (m *StorageManager) ListSnapshots(ctx context.Context, volumeID string, filters map[string]string) ([]*cpi.Snapshot, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	pager := m.client.snapshotsClient.NewListByResourceGroupPager(m.client.getResourceGroup(), nil)

	var snapshots []*cpi.Snapshot
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListSnapshots")
		}

		for _, snap := range page.Value {
			snapshot := m.snapshotToSnapshot(snap)

			// Filter by volume ID if specified
			if volumeID != "" {
				if snap.Properties != nil && snap.Properties.CreationData != nil {
					sourceID := DerefString(snap.Properties.CreationData.SourceResourceID)
					if !strings.HasSuffix(sourceID, "/"+ExtractResourceName(volumeID)) {
						continue
					}
				}
			}

			if matchesSnapshotFilters(snapshot.Tags, filters) {
				snapshots = append(snapshots, snapshot)
			}
		}
	}

	return snapshots, nil
}

// DeleteSnapshot deletes a snapshot.
func (m *StorageManager) DeleteSnapshot(ctx context.Context, id string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	poller, err := m.client.snapshotsClient.BeginDelete(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteSnapshot")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteSnapshot")
	}

	logger.Infow("Deleted snapshot", "name", name)

	return nil
}

// CreateBucket creates a new storage account (Azure's equivalent to a bucket).
func (m *StorageManager) CreateBucket(ctx context.Context, req *cpi.BucketRequest) (*cpi.Bucket, error) {
	if req == nil {
		return nil, ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	// Storage account names must be globally unique and 3-24 characters
	accountName := SanitizeResourceName(req.Name, 24)
	accountName = strings.ToLower(accountName)

	// Prepare storage account parameters
	accountParams := armstorage.AccountCreateParameters{
		Location: to.Ptr(m.client.getLocation()),
		Kind:     to.Ptr(armstorage.KindStorageV2),
		SKU: &armstorage.SKU{
			Name: to.Ptr(armstorage.SKUNameStandardLRS),
		},
		Properties: &armstorage.AccountPropertiesCreateParameters{
			AccessTier:            to.Ptr(armstorage.AccessTierHot),
			EnableHTTPSTrafficOnly: to.Ptr(true),
			MinimumTLSVersion:     to.Ptr(armstorage.MinimumTLSVersionTLS12),
		},
		Tags: BuildTags(MergeTags(m.client.config.DefaultTags, req.Tags)),
	}

	poller, err := m.client.storageAccountsClient.BeginCreate(
		ctx,
		m.client.getResourceGroup(),
		accountName,
		accountParams,
		nil,
	)
	if err != nil {
		return nil, WrapAzureError(err, "CreateBucket")
	}

	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateBucket")
	}

	// Create a default container
	containerName := "data"
	_, err = m.client.blobContainersClient.Create(
		ctx,
		m.client.getResourceGroup(),
		accountName,
		containerName,
		armstorage.BlobContainer{},
		nil,
	)
	if err != nil {
		logger.Warnw("Failed to create default container", "error", err)
	}

	logger.Infow("Created storage account", "name", accountName)

	return m.storageAccountToBucket(&result.Account), nil
}

// GetBucket retrieves a storage account by name.
func (m *StorageManager) GetBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	accountName := strings.ToLower(ExtractResourceName(name))

	result, err := m.client.storageAccountsClient.GetProperties(ctx, m.client.getResourceGroup(), accountName, nil)
	if err != nil {
		return nil, WrapAzureError(err, "GetBucket")
	}

	return m.storageAccountToBucket(&result.Account), nil
}

// ListBuckets lists all storage accounts.
func (m *StorageManager) ListBuckets(ctx context.Context) ([]*cpi.Bucket, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	pager := m.client.storageAccountsClient.NewListByResourceGroupPager(m.client.getResourceGroup(), nil)

	var buckets []*cpi.Bucket
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListBuckets")
		}

		for _, account := range page.Value {
			buckets = append(buckets, m.storageAccountToBucket(account))
		}
	}

	return buckets, nil
}

// DeleteBucket deletes a storage account.
func (m *StorageManager) DeleteBucket(ctx context.Context, name string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	accountName := strings.ToLower(ExtractResourceName(name))

	_, err = m.client.storageAccountsClient.Delete(ctx, m.client.getResourceGroup(), accountName, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteBucket")
	}

	logger.Infow("Deleted storage account", "name", accountName)

	return nil
}

// EmptyBucket empties all containers in a storage account.
func (m *StorageManager) EmptyBucket(ctx context.Context, name string) error {
	// This would require listing and deleting all blobs
	// For now, return not implemented
	return ErrNotImplemented
}

// IsBucketEmpty checks if a storage account has any blobs.
func (m *StorageManager) IsBucketEmpty(ctx context.Context, name string) (bool, error) {
	// This would require listing blobs
	// For now, return not implemented
	return false, ErrNotImplemented
}

// CreateCredentialsGroup creates a credentials group (not directly applicable to Azure).
func (m *StorageManager) CreateCredentialsGroup(ctx context.Context, req *cpi.CredentialsGroupRequest) (*cpi.CredentialsGroup, error) {
	// Azure uses storage account keys or SAS tokens for access
	// This could be implemented as generating SAS tokens
	return nil, ErrNotImplemented
}

// Helper functions

func (m *StorageManager) diskToVolume(disk *armcompute.Disk) *cpi.Volume {
	if disk == nil {
		return nil
	}

	volume := &cpi.Volume{
		ID:        DerefString(disk.ID),
		Name:      DerefString(disk.Name),
		Tags:      ExtractTags(disk.Tags),
		CreatedAt: time.Now(),
	}

	if disk.Properties != nil {
		volume.Size = int(DerefInt32(disk.Properties.DiskSizeGB))
		volume.State = MapDiskStateToResourceState(string(*disk.Properties.DiskState))

		if disk.Properties.Encryption != nil {
			volume.Encrypted = true
		}

		// Check if attached
		if disk.ManagedBy != nil && *disk.ManagedBy != "" {
			volume.AttachedTo = DerefString(disk.ManagedBy)
		}
	}

	if disk.SKU != nil && disk.SKU.Name != nil {
		volume.Type = string(*disk.SKU.Name)
	}

	return volume
}

func (m *StorageManager) snapshotToSnapshot(snap *armcompute.Snapshot) *cpi.Snapshot {
	if snap == nil {
		return nil
	}

	snapshot := &cpi.Snapshot{
		ID:        DerefString(snap.ID),
		Name:      DerefString(snap.Name),
		Tags:      ExtractTags(snap.Tags),
		CreatedAt: time.Now(),
	}

	if snap.Properties != nil {
		snapshot.Size = int(DerefInt32(snap.Properties.DiskSizeGB))
		snapshot.State = MapProvisioningStateToResourceState(string(*snap.Properties.ProvisioningState))

		if snap.Properties.CreationData != nil && snap.Properties.CreationData.SourceResourceID != nil {
			snapshot.VolumeID = DerefString(snap.Properties.CreationData.SourceResourceID)
		}
	}

	return snapshot
}

func (m *StorageManager) storageAccountToBucket(account *armstorage.Account) *cpi.Bucket {
	if account == nil {
		return nil
	}

	bucket := &cpi.Bucket{
		ID:        DerefString(account.ID),
		Name:      DerefString(account.Name),
		Region:    DerefString(account.Location),
		Tags:      ExtractTags(account.Tags),
		CreatedAt: time.Now(),
	}

	if account.Kind != nil {
		bucket.StorageClass = string(*account.Kind)
	}

	return bucket
}

func matchesVolumeFilters(tags map[string]string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for key, value := range filters {
		if tagValue, ok := tags[key]; !ok || tagValue != value {
			return false
		}
	}

	return true
}

func matchesSnapshotFilters(tags map[string]string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for key, value := range filters {
		if tagValue, ok := tags[key]; !ok || tagValue != value {
			return false
		}
	}

	return true
}
