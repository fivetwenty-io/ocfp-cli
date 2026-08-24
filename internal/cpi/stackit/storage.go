package stackit

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	objectstorage "github.com/stackitcloud/stackit-sdk-go/services/objectstorage/v2api"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	s3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// createAWSConfig creates an AWS config with the specified credentials.
func (m *StorageManager) createAWSConfig(accessKey, secretKey string) aws.Config {
	return aws.Config{
		Region:                  m.client.apiRegion(),
		Credentials:             aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		BearerAuthTokenProvider: nil,
		HTTPClient:              nil,
		RetryMaxAttempts:        0,
		RetryMode:               "",
		Retryer:                 nil,
		ConfigSources:           nil,
		APIOptions:              nil,
		Logger:                  nil,
		ClientLogMode:           0,
		DefaultsMode:            "",
		RuntimeEnvironment: aws.RuntimeEnvironment{
			EnvironmentIdentifier:     "",
			Region:                    "",
			EC2InstanceMetadataRegion: "",
		},
		AppID:                       "",
		BaseEndpoint:                nil,
		DisableRequestCompression:   false,
		RequestMinCompressSizeBytes: 0,
		AccountIDEndpointMode:       aws.AccountIDEndpointModeDisabled,
		RequestChecksumCalculation:  aws.RequestChecksumCalculationWhenSupported,
		ResponseChecksumValidation:  aws.ResponseChecksumValidationWhenSupported,
		Interceptors: smithyhttp.InterceptorRegistry{
			BeforeExecution:       nil,
			BeforeSerialization:   nil,
			AfterSerialization:    nil,
			BeforeRetryLoop:       nil,
			BeforeAttempt:         nil,
			BeforeSigning:         nil,
			AfterSigning:          nil,
			BeforeTransmit:        nil,
			AfterTransmit:         nil,
			BeforeDeserialization: nil,
			AfterDeserialization:  nil,
			AfterAttempt:          nil,
			AfterExecution:        nil,
		},
		AuthSchemePreference: nil,
		ServiceOptions:       nil,
	}
}

// CreateVolume creates a new storage volume.
func (m *StorageManager) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	logger.WithOperation("CreateVolume").Infof("Creating volume via SDK: %s", req.Name)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	payload := m.buildVolumePayload(req)

	created, err := cli.CreateVolume(ctx, m.client.config.ProjectID, m.client.apiRegion()).CreateVolumePayload(*payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas CreateVolume failed: %w", err)
	}

	out := m.buildVolumeResponse(created)

	err = m.waitForVolumeState(ctx, out.ID, cpi.ResourceStateAvailable, volumeWaitTimeout)
	if err != nil {
		return nil, fmt.Errorf("volume failed to become available: %w", err)
	}

	return out, nil
}

func (m *StorageManager) buildVolumePayload(req *cpi.VolumeRequest) *iaas.CreateVolumePayload {
	payload := iaas.NewCreateVolumePayload(req.AvailabilityZone)
	if req.Name != "" {
		payload.SetName(req.Name)
	}

	// Handle both Size (legacy) and SizeGB fields
	size := req.Size
	if req.SizeGB > 0 {
		size = req.SizeGB
	}

	if size > 0 {
		payload.SetSize(int64(size))
	}

	if req.Encrypted {
		payload.SetEncrypted(true)
	}

	if len(req.Tags) > 0 {
		// Use sanitization to ensure labels comply with STACKIT requirements
		labels := sanitizeLabelsForStackit(req.Tags)
		payload.SetLabels(labels)
	}

	return payload
}

func (m *StorageManager) buildVolumeResponse(created *iaas.Volume) *cpi.Volume {
	out := &cpi.Volume{
		ID:         stringOrEmpty(created.GetIdOk()),
		Name:       stringOrEmpty(created.GetNameOk()),
		Size:       0,
		Type:       "",
		State:      cpi.ResourceStateUnknown,
		Encrypted:  false,
		AttachedTo: "",
		Device:     "",
		Tags:       map[string]string{},
		CreatedAt:  time.Now(),
	}
	if size, ok := created.GetSizeOk(); ok {
		out.Size = int(derefOr(size))
	}

	if az, ok := created.GetAvailabilityZoneOk(); ok {
		out.Tags = map[string]string{"az": derefOr(az)}
	}

	return out
}

// mapVolumeStatus maps STACKIT volume status to cpi.ResourceState.
func mapVolumeStatus(status string) cpi.ResourceState {
	switch strings.ToUpper(status) {
	case "AVAILABLE":
		return cpi.ResourceStateAvailable
	case "IN_USE", "IN-USE", "INUSE":
		return cpi.ResourceStateInUse
	case "CREATING":
		return cpi.ResourceStateCreating
	case "DELETING":
		return cpi.ResourceStateDeleting
	case "ERROR":
		return cpi.ResourceStateError
	default:
		return cpi.ResourceState(status)
	}
}

// GetVolume retrieves a volume by ID.
func (m *StorageManager) GetVolume(ctx context.Context, volumeID string) (*cpi.Volume, error) {
	logger.WithOperation("GetVolume").Debugf("Getting volume via SDK: %s", volumeID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	got, err := cli.GetVolume(ctx, m.client.config.ProjectID, m.client.apiRegion(), volumeID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetVolume failed: %w", err)
	}

	out := &cpi.Volume{
		ID:         stringOrEmpty(got.GetIdOk()),
		Name:       stringOrEmpty(got.GetNameOk()),
		Size:       0,
		Type:       "",
		State:      cpi.ResourceStateUnknown,
		Encrypted:  false,
		AttachedTo: "",
		Device:     "",
		Tags:       map[string]string{},
		CreatedAt:  time.Now(),
	}
	if size, ok := got.GetSizeOk(); ok {
		out.Size = int(derefOr(size))
	}

	// Map the status to cpi.ResourceState
	if status, ok := got.GetStatusOk(); ok {
		out.State = mapVolumeStatus(derefOr(status))
	}

	// Include availability zone in tags
	if az, ok := got.GetAvailabilityZoneOk(); ok {
		out.Tags["az"] = derefOr(az)
	}

	return out, nil
}

// ListVolumes lists all volumes.
func (m *StorageManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	logger.WithOperation("ListVolumes").Debug("Listing volumes via SDK")

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	resp, err := cli.ListVolumes(ctx, m.client.config.ProjectID, m.client.apiRegion()).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListVolumes failed: %w", err)
	}

	items, _ := resp.GetItemsOk()

	out := make([]*cpi.Volume, 0, len(items))
	for _, volumeItem := range items {
		// Extract labels
		labels := mapAnyToString(volumeItem.GetLabels())

		// Apply label filtering - skip resources without required metadata
		if !matchLabels(labels, filters) {
			continue
		}

		vol := &cpi.Volume{
			ID:         stringOrEmpty(volumeItem.GetIdOk()),
			Name:       stringOrEmpty(volumeItem.GetNameOk()),
			Size:       0,
			Type:       "",
			State:      cpi.ResourceStateUnknown,
			Encrypted:  false,
			AttachedTo: "",
			Device:     "",
			Tags:       labels, // Store labels as tags
			CreatedAt:  time.Now(),
		}
		if size, ok := volumeItem.GetSizeOk(); ok {
			vol.Size = int(derefOr(size))
		}

		// Map the status to cpi.ResourceState
		if status, ok := volumeItem.GetStatusOk(); ok {
			vol.State = mapVolumeStatus(derefOr(status))
		}

		// Include availability zone in tags if available
		if az := stringOrEmpty(volumeItem.GetAvailabilityZoneOk()); az != "" {
			vol.Tags["az"] = az
		}

		out = append(out, vol)
	}

	logger.WithOperation("ListVolumes").Debugf("Found %d volumes (after filtering)", len(out))

	return out, nil
}

// DeleteVolume deletes a volume.
func (m *StorageManager) DeleteVolume(ctx context.Context, volumeID string) error {
	logger.WithOperation("DeleteVolume").Infof("Deleting volume: %s", volumeID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	err = cli.DeleteVolume(ctx, m.client.config.ProjectID, m.client.apiRegion(), volumeID).Execute()
	if err != nil {
		return fmt.Errorf("stackit iaas DeleteVolume failed: %w", err)
	}

	return nil
}

// AttachVolume attaches a volume to an instance.
func (m *StorageManager) AttachVolume(ctx context.Context, volumeID, instanceID, _device string) error {
	logger.WithOperation("AttachVolume").Infof("Attaching volume %s to instance %s", volumeID, instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	_, err = cli.AddVolumeToServer(ctx, m.client.config.ProjectID, m.client.apiRegion(), instanceID, volumeID).Execute()
	if err != nil {
		return fmt.Errorf("stackit iaas AddVolumeToServer failed: %w", err)
	}

	err = m.waitForVolumeState(ctx, volumeID, cpi.ResourceStateInUse, volumeAttachTimeout)
	if err != nil {
		return fmt.Errorf("volume failed to attach: %w", err)
	}

	logger.WithOperation("AttachVolume").Infof("Volume attached successfully")

	return nil
}

// DetachVolume detaches a volume from an instance.
func (m *StorageManager) DetachVolume(ctx context.Context, volumeID string, instanceID string) error {
	logger.WithOperation("DetachVolume").Infof("Detaching volume %s from instance %s", volumeID, instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	err = cli.RemoveVolumeFromServer(ctx, m.client.config.ProjectID, m.client.apiRegion(), instanceID, volumeID).Execute()
	if err != nil {
		return fmt.Errorf("stackit iaas RemoveVolumeFromServer failed: %w", err)
	}

	err = m.waitForVolumeState(ctx, volumeID, cpi.ResourceStateAvailable, volumeAttachTimeout)
	if err != nil {
		return fmt.Errorf("volume failed to detach: %w", err)
	}

	logger.WithOperation("DetachVolume").Infof("Volume detached successfully")

	return nil
}

// ResizeVolume resizes a volume.
func (m *StorageManager) ResizeVolume(ctx context.Context, volumeID string, newSize int) error {
	logger.WithOperation("ResizeVolume").Infof("Resizing volume %s to %d GB", volumeID, newSize)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	payload := iaas.NewResizeVolumePayload(int64(newSize))

	err = cli.ResizeVolume(ctx, m.client.config.ProjectID, m.client.apiRegion(), volumeID).ResizeVolumePayload(*payload).Execute()
	if err != nil {
		return fmt.Errorf("failed to resize volume %s: %w", volumeID, err)
	}

	return nil
}

// CreateSnapshot creates a snapshot of a volume.
func (m *StorageManager) CreateSnapshot(ctx context.Context, volumeID string, name string) (*cpi.Snapshot, error) {
	logger.WithOperation("CreateSnapshot").Infof("Creating snapshot via SDK: %s", name)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	payload := iaas.NewCreateSnapshotPayload(volumeID)
	if name != "" {
		payload.SetName(name)
	}

	created, err := cli.CreateSnapshot(ctx, m.client.config.ProjectID, m.client.apiRegion()).CreateSnapshotPayload(*payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas CreateSnapshot failed: %w", err)
	}

	out := &cpi.Snapshot{
		ID:          stringOrEmpty(created.GetIdOk()),
		Name:        stringOrEmpty(created.GetNameOk()),
		VolumeID:    stringOrEmpty(created.GetVolumeIdOk()),
		Size:        0,
		State:       cpi.ResourceStateUnknown,
		Description: "",
		Tags:        map[string]string{},
		CreatedAt:   time.Now(),
	}

	err = m.waitForSnapshotState(ctx, out.ID, cpi.ResourceStateAvailable, snapshotWaitTimeout)
	if err != nil {
		return nil, fmt.Errorf("snapshot failed to become available: %w", err)
	}

	return out, nil
}

// GetSnapshot retrieves a snapshot by ID.
func (m *StorageManager) GetSnapshot(ctx context.Context, snapshotID string) (*cpi.Snapshot, error) {
	logger.WithOperation("GetSnapshot").Debugf("Getting snapshot via SDK: %s", snapshotID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	got, err := cli.GetSnapshot(ctx, m.client.config.ProjectID, m.client.apiRegion(), snapshotID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetSnapshot failed: %w", err)
	}

	out := &cpi.Snapshot{
		ID:          stringOrEmpty(got.GetIdOk()),
		Name:        stringOrEmpty(got.GetNameOk()),
		VolumeID:    stringOrEmpty(got.GetVolumeIdOk()),
		Size:        0,
		State:       cpi.ResourceStateUnknown,
		Description: "",
		Tags:        map[string]string{},
		CreatedAt:   time.Now(),
	}

	return out, nil
}

// ListSnapshots lists all snapshots for a volume with optional filters.
func (m *StorageManager) ListSnapshots(ctx context.Context, volumeID string, _filters map[string]string) ([]*cpi.Snapshot, error) {
	logger.WithOperation("ListSnapshots").Debug("Listing snapshots via SDK")

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	req := cli.ListSnapshotsInProject(ctx, m.client.config.ProjectID, m.client.apiRegion())
	if volumeID != "" {
		req = req.LabelSelector("volumeId=" + volumeID)
	}

	// Note: STACKIT uses label selectors, not tag filters like AWS
	// Tag filters would need to be applied client-side for STACKIT
	// For now, we maintain the existing behavior

	resp, err := req.Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListSnapshots failed: %w", err)
	}

	items, _ := resp.GetItemsOk()

	out := make([]*cpi.Snapshot, 0, len(items))
	for _, s := range items {
		out = append(out, &cpi.Snapshot{
			ID:          stringOrEmpty(s.GetIdOk()),
			Name:        stringOrEmpty(s.GetNameOk()),
			VolumeID:    stringOrEmpty(s.GetVolumeIdOk()),
			Size:        0,
			State:       cpi.ResourceStateUnknown,
			Description: "",
			Tags:        map[string]string{},
			CreatedAt:   time.Now(),
		})
	}

	return out, nil
}

// DeleteSnapshot deletes a snapshot.
func (m *StorageManager) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	logger.WithOperation("DeleteSnapshot").Infof("Deleting snapshot: %s", snapshotID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	err = cli.DeleteSnapshot(ctx, m.client.config.ProjectID, m.client.apiRegion(), snapshotID).Execute()
	if err != nil {
		return fmt.Errorf("stackit iaas DeleteSnapshot failed: %w", err)
	}

	return nil
}

// waitForVolumeState waits for a volume to reach a specific state.
func (m *StorageManager) waitForVolumeState(ctx context.Context, volumeID string, targetState cpi.ResourceState, timeout time.Duration) error {
	err := cpi.WaitForCondition(ctx, conditionCheckInterval, timeout, func() (bool, error) {
		volume, err := m.GetVolume(ctx, volumeID)
		if err != nil {
			return false, err
		}

		return volume.State == targetState, nil
	})
	if err != nil {
		return fmt.Errorf("failed to wait for volume %s to reach state %s: %w", volumeID, targetState, err)
	}

	return nil
}

// waitForSnapshotState waits for a snapshot to reach a specific state.
func (m *StorageManager) waitForSnapshotState(ctx context.Context, snapshotID string, targetState cpi.ResourceState, timeout time.Duration) error {
	err := cpi.WaitForCondition(ctx, conditionCheckInterval, timeout, func() (bool, error) {
		snapshot, err := m.GetSnapshot(ctx, snapshotID)
		if err != nil {
			return false, err
		}

		return snapshot.State == targetState, nil
	})
	if err != nil {
		return fmt.Errorf("failed to wait for snapshot %s to reach state %s: %w", snapshotID, targetState, err)
	}

	return nil
}

// CreateBucket creates a new bucket in object storage using the official SDK.
// handleBucketCreationError analyzes bucket creation errors and returns appropriate error messages.
func (m *StorageManager) handleBucketCreationError(err error, bucketName string) error {
	errStr := err.Error()

	switch {
	case strings.Contains(errStr, "already exists") ||
		strings.Contains(errStr, "AlreadyExists") ||
		strings.Contains(errStr, "BucketAlreadyExists") ||
		strings.Contains(errStr, "409"):
		// Check if bucket already exists - treat as success
		logger.WithOperation("CreateBucket").Infof("Bucket %s already exists, treating as success", bucketName)

		return nil
	case strings.Contains(errStr, "404") ||
		strings.Contains(errStr, "Not Found") ||
		strings.Contains(errStr, "not enabled") ||
		strings.Contains(errStr, "service not found") ||
		strings.Contains(errStr, "service is not enabled"):
		// Object storage not enabled - provide clear error message
		return fmt.Errorf("is object storage enabled for the project?\n\nFailed to create bucket (service may not be enabled): %w\n\nTo enable object storage:\n1. Visit STACKIT portal: https://portal.stackit.cloud/\n2. Navigate to Project: %s\n3. Select Region: %s\n4. Enable Object Storage service",
			err, m.client.config.ProjectID, m.client.apiRegion())
	default:
		// Other error - fail
		return fmt.Errorf("failed to create bucket: %w", err)
	}
}

// initializeBucketTags sets up default tags for a bucket.
func (m *StorageManager) initializeBucketTags(bucket *cpi.Bucket, bucketName string) {
	// Add default tags if not provided
	if bucket.Tags == nil {
		bucket.Tags = make(map[string]string)
	}

	if _, exists := bucket.Tags["managed-by"]; !exists {
		bucket.Tags["managed-by"] = "ocfp"
	}

	if bloc := ParseBlocFromBucketName(bucketName); bloc != "" {
		if bucket.Tags == nil {
			bucket.Tags = map[string]string{}
		}

		bucket.Tags["bloc"] = bloc
	}
}

// CreateBucket creates a new object storage bucket in STACKIT.
func (m *StorageManager) CreateBucket(ctx context.Context, req *cpi.BucketRequest) (*cpi.Bucket, error) {
	logger.WithOperation("CreateBucket").Infof("Creating bucket: %s", req.Name)

	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return nil, err
	}

	// Try to create the bucket directly
	_, err = cli.CreateBucket(ctx, m.client.config.ProjectID, m.client.apiRegion(), req.Name).Execute()
	if err != nil {
		err = m.handleBucketCreationError(err, req.Name)
		if err != nil {
			return nil, err
		}
		// Continue to build and return bucket info if error was "already exists"
	}

	bucket := &cpi.Bucket{
		ID:           req.Name, // Use bucket name as ID
		Name:         req.Name,
		Region:       m.client.apiRegion(),
		StorageClass: "",
		Versioning:   false,
		Encryption:   false,
		Public:       false,
		Size:         0,
		ObjectCount:  0,
		Tags:         req.Tags,
		CreatedAt:    time.Now(),
	}

	m.initializeBucketTags(bucket, req.Name)

	logger.WithOperation("CreateBucket").Infof("Bucket created successfully: %s", req.Name)

	return bucket, nil
}

// ParseBlocFromBucketName extracts the bloc name from a bucket name following
// the pattern "<bloc>-<suffix>". Returns empty string if not matched.
func ParseBlocFromBucketName(name string) string {
	for index := range len(name) {
		if name[index] == '-' {
			if index > 0 {
				return name[:index]
			}

			return ""
		}
	}

	return ""
}

// GetBucket retrieves a bucket by name using the SDK.
func (m *StorageManager) GetBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	logger.WithOperation("GetBucket").Debugf("Getting bucket: %s", name)

	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return nil, err
	}

	resp, err := cli.GetBucket(ctx, m.client.config.ProjectID, m.client.apiRegion(), name).Execute()
	if err != nil {
		// Translate 404 if possible
		return nil, &cpi.ProviderError{
			Provider: "stackit",
			Code:     "GetBucketFailed",
			Message:  err.Error(),
			Details:  nil,
		}
	}

	// Map SDK model to CPI bucket
	var bucketData objectstorage.Bucket
	if bb, ok := resp.GetBucketOk(); ok && bb != nil {
		bucketData = *bb
	}

	bucket := &cpi.Bucket{
		Name:         bucketData.GetName(),
		Region:       bucketData.GetRegion(),
		StorageClass: "",
		Versioning:   false,
		Encryption:   false,
		Public:       false,
		Size:         0,
		ObjectCount:  0,
		Tags:         map[string]string{},
		CreatedAt:    time.Now(),
	}

	return bucket, nil
}

// ListBuckets lists all buckets using the SDK.
func (m *StorageManager) ListBuckets(ctx context.Context) ([]*cpi.Bucket, error) {
	logger.WithOperation("ListBuckets").Debug("Listing buckets")

	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return nil, err
	}

	resp, err := cli.ListBuckets(ctx, m.client.config.ProjectID, m.client.apiRegion()).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to list buckets: %w", err)
	}

	var out []*cpi.Bucket

	if buckets, ok := resp.GetBucketsOk(); ok {
		for _, b := range buckets {
			bb := b
			out = append(out, &cpi.Bucket{
				Name:         bb.GetName(),
				Region:       bb.GetRegion(),
				StorageClass: "",
				Versioning:   false,
				Encryption:   false,
				Public:       false,
				Size:         0,
				ObjectCount:  0,
				Tags:         map[string]string{},
				CreatedAt:    time.Now(),
			})
		}
	}

	return out, nil
}

// DeleteBucket deletes a bucket using the SDK.
func (m *StorageManager) DeleteBucket(ctx context.Context, name string) error {
	logger.WithOperation("DeleteBucket").Infof("Deleting bucket: %s", name)

	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return err
	}

	_, err = cli.DeleteBucket(ctx, m.client.config.ProjectID, m.client.apiRegion(), name).Execute()
	if err != nil {
		return fmt.Errorf("failed to delete bucket: %w", err)
	}

	logger.WithOperation("DeleteBucket").Infof("Bucket deleted: %s", name)

	return nil
}

// IsBucketEmpty checks if a bucket is empty (contains no objects).
func (m *StorageManager) IsBucketEmpty(ctx context.Context, name string) (bool, error) {
	operation := logger.WithOperation("IsBucketEmpty")
	operation.Debugf("Checking if bucket is empty: %s", name)

	// Get bucket endpoint
	endpoint, err := m.getBucketEndpoint(ctx, name)
	if err != nil {
		return false, err
	}

	// Create S3 client with temporary credentials
	s3cli, cleanup, err := m.createS3ClientWithTempCreds(ctx, endpoint)
	if err != nil {
		return false, err
	}
	defer cleanup()

	// Check for objects (with minimal result set for efficiency)
	maxKeys := int32(1) // Only need to know if at least one object exists

	listResp, err := listBucketObjects(ctx, s3cli, name, nil)
	if err != nil {
		return false, fmt.Errorf("failed to list objects in bucket: %w", err)
	}

	// If there are any objects, bucket is not empty
	if listResp.KeyCount != nil && *listResp.KeyCount > 0 {
		operation.Debugf("Bucket %s is not empty: found %d object(s)", name, *listResp.KeyCount)

		return false, nil
	}

	// For versioned buckets, also check for versions and delete markers
	listVersionsInput := &s3.ListObjectVersionsInput{
		Bucket:  aws.String(name),
		MaxKeys: &maxKeys,
	}

	versionsResp, err := s3cli.ListObjectVersions(ctx, listVersionsInput)
	if err != nil {
		// If versioning is not enabled, this might fail - treat as no versions
		operation.Debugf("Could not list versions for bucket %s: %v", name, err)

		return true, nil
	}

	// Check if there are any versions or delete markers
	hasVersions := len(versionsResp.Versions) > 0 || len(versionsResp.DeleteMarkers) > 0

	if hasVersions {
		operation.Debugf("Bucket %s is not empty: found object versions or delete markers", name)

		return false, nil
	}

	operation.Debugf("Bucket %s is empty", name)

	return true, nil
}

// EmptyBucket removes all objects from a bucket.
func (m *StorageManager) EmptyBucket(ctx context.Context, name string) error {
	operation := logger.WithOperation("EmptyBucket")
	operation.Infof("Emptying bucket: %s", name)

	// Get bucket endpoint
	endpoint, err := m.getBucketEndpoint(ctx, name)
	if err != nil {
		return err
	}

	// Create S3 client with temporary credentials
	s3cli, cleanup, err := m.createS3ClientWithTempCreds(ctx, endpoint)
	if err != nil {
		return err
	}
	defer cleanup()

	// Delete all objects from bucket
	err = m.deleteAllBucketContents(ctx, s3cli, name)
	if err != nil {
		return err
	}

	operation.Infof("Bucket emptied: %s", name)

	return nil
}

// getBucketEndpoint retrieves the S3 endpoint for a bucket.
func (m *StorageManager) getBucketEndpoint(ctx context.Context, name string) (string, error) {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return "", err
	}

	getBucketResp, err := cli.GetBucket(ctx, m.client.config.ProjectID, m.client.apiRegion(), name).Execute()
	if err != nil {
		return "", fmt.Errorf("failed to get bucket %s metadata: %w", name, err)
	}

	var bucketInfo objectstorage.Bucket
	if bucketData, ok := getBucketResp.GetBucketOk(); ok && bucketData != nil {
		bucketInfo = *bucketData
	} else {
		return "", ErrBucketMetadataMissingInResponse(name)
	}

	// Parse host for endpoint resolver
	pathURL := bucketInfo.GetUrlPathStyle()

	u, err := url.Parse(pathURL)
	if err != nil {
		return "", fmt.Errorf("invalid bucket URL from API: %s: %w", pathURL, err)
	}

	return u.Scheme + "://" + u.Host, nil
}

// createS3ClientWithTempCreds creates an S3 client with temporary credentials.
func (m *StorageManager) createS3ClientWithTempCreds(ctx context.Context, endpoint string) (*s3.Client, func(), error) {
	accessKeyID, secretAccessKey, keyID, groupID, err := m.ensureTemporaryAccessKey(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to obtain S3 credentials: %w", err)
	}

	// Build S3 client
	cfg := m.createAWSConfig(accessKeyID, secretAccessKey)

	s3cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})

	// Cleanup function
	cleanup := func() {
		if keyID == "" {
			return
		}

		cli, err := m.client.getObjectStorageClient()
		if err == nil {
			_, derr := cli.DeleteAccessKey(ctx, m.client.config.ProjectID, m.client.apiRegion(), keyID).
				CredentialsGroup(groupID).
				Execute()
			if derr != nil {
				logger.WithOperation("EmptyBucket").Warnf("Failed to delete temporary access key %s: %v", keyID, derr)
			}
		}
	}

	return s3cli, cleanup, nil
}

// deleteAllBucketContents deletes all objects and versions from a bucket.
func (m *StorageManager) deleteAllBucketContents(ctx context.Context, s3cli *s3.Client, name string) error {
	operation := logger.WithOperation("EmptyBucket")

	// First attempt to delete object versions if versioning is enabled
	err := deleteAllObjectVersions(ctx, s3cli, name)
	if err != nil {
		operation.Warnf("Deleting object versions failed or not supported: %v (continuing to delete current objects)", err)
	}

	// Delete current objects
	err = deleteAllObjects(ctx, s3cli, name)
	if err != nil {
		return fmt.Errorf("failed deleting objects: %w", err)
	}

	return nil
}

// ensureTemporaryAccessKey returns accessKeyID, secretAccessKey, keyID, groupID.
func (m *StorageManager) ensureTemporaryAccessKey(ctx context.Context) (string, string, string, string, error) {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return "", "", "", "", err
	}

	// Ensure credentials group exists
	groupID, err := m.ensureCredentialsGroup(ctx, cli)
	if err != nil {
		return "", "", "", "", err
	}

	// Create access key in the group
	accessKey, err := m.createAccessKey(ctx, cli, groupID)
	if err != nil {
		return "", "", "", "", err
	}

	return accessKey.GetAccessKey(), accessKey.GetSecretAccessKey(), accessKey.GetKeyId(), groupID, nil
}

// ensureCredentialsGroup finds or creates a credentials group.
func (m *StorageManager) ensureCredentialsGroup(ctx context.Context, cli objectstorage.DefaultAPI) (string, error) {
	const groupDisplay = "ocfp-cli"

	// Find existing group
	groupID := m.findCredentialsGroup(ctx, cli, groupDisplay)
	if groupID != "" {
		return groupID, nil
	}

	// Create new group
	payload := objectstorage.NewCreateCredentialsGroupPayload(groupDisplay)

	_, err := cli.CreateCredentialsGroup(ctx, m.client.config.ProjectID, m.client.apiRegion()).
		CreateCredentialsGroupPayload(*payload).Execute()
	if err != nil {
		return "", fmt.Errorf("create credentials group failed: %w", err)
	}

	// Find the created group
	groupID = m.findCredentialsGroup(ctx, cli, groupDisplay)
	if groupID == "" {
		return "", ErrCouldNotDetermineCreatedCredentialsGroupID
	}

	return groupID, nil
}

// findCredentialsGroup finds a credentials group by display name.
func (m *StorageManager) findCredentialsGroup(ctx context.Context, cli objectstorage.DefaultAPI, displayName string) string {
	resp, err := cli.ListCredentialsGroups(ctx, m.client.config.ProjectID, m.client.apiRegion()).Execute()
	if err != nil {
		return ""
	}

	if groups, ok := resp.GetCredentialsGroupsOk(); ok {
		for _, g := range groups {
			if strings.EqualFold(g.GetDisplayName(), displayName) {
				return g.GetCredentialsGroupId()
			}
		}
	}

	return ""
}

// createAccessKey creates a new access key in the specified group.
func (m *StorageManager) createAccessKey(ctx context.Context, cli objectstorage.DefaultAPI, groupID string) (*objectstorage.CreateAccessKeyResponse, error) {
	payload := objectstorage.NewCreateAccessKeyPayload()
	// Optional: leave Expires nil to create non-expiring; we set short expiry for safety
	// t := time.Now().Add(1 * time.Hour)
	// payload.SetExpires(t)

	req := cli.CreateAccessKey(ctx, m.client.config.ProjectID, m.client.apiRegion())
	req = req.CredentialsGroup(groupID)

	car, err := req.CreateAccessKeyPayload(*payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("create access key failed: %w", err)
	}

	return car, nil
}

func deleteAllObjects(ctx context.Context, s3cli *s3.Client, bucket string) error {
	var cont *string
	for {
		listResp, err := listBucketObjects(ctx, s3cli, bucket, cont)
		if err != nil {
			return err
		}

		if len(listResp.Contents) == 0 {
			return nil
		}

		err = batchDeleteObjects(ctx, s3cli, bucket, listResp.Contents)
		if err != nil {
			return err
		}

		cont = getNextContinuationToken(listResp)
		if cont == nil {
			return nil
		}
	}
}

func listBucketObjects(ctx context.Context, s3cli *s3.Client, bucket string, cont *string) (*s3.ListObjectsV2Output, error) {
	listResp, err := s3cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:            &bucket,
		ContinuationToken: cont,
		FetchOwner:        aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list objects in bucket %s: %w", bucket, err)
	}

	return listResp, nil
}

func batchDeleteObjects(ctx context.Context, s3cli *s3.Client, bucket string, contents []s3typesObject) error {
	const batch = 1000

	for index := 0; index < len(contents); index += batch {
		end := index + batch
		if end > len(contents) {
			end = len(contents)
		}

		objs := buildObjectIdentifiers(contents[index:end])

		err := deleteObjectsBatch(ctx, s3cli, bucket, objs)
		if err != nil {
			return err
		}
	}

	return nil
}

func buildObjectIdentifiers(contents []s3typesObject) []s3typesObjectIdentifier {
	objs := make([]s3typesObjectIdentifier, 0, len(contents))
	for _, o := range contents {
		objs = append(objs, s3typesObjectIdentifier{
			Key: o.Key,
		})
	}

	return objs
}

func deleteObjectsBatch(ctx context.Context, s3cli *s3.Client, bucket string, objs []s3typesObjectIdentifier) error {
	_, err := s3cli.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: &bucket,
		Delete: &s3typesDelete{Objects: objs, Quiet: aws.Bool(true)},
	})
	if err != nil {
		return fmt.Errorf("failed to delete objects batch from bucket %s: %w", bucket, err)
	}

	return nil
}

func getNextContinuationToken(listResp *s3.ListObjectsV2Output) *string {
	if aws.ToBool(listResp.IsTruncated) && listResp.NextContinuationToken != nil {
		return listResp.NextContinuationToken
	}

	return nil
}

// Delete all object versions if bucket has versioning enabled.
func deleteAllObjectVersions(ctx context.Context, s3cli *s3.Client, bucket string) error {
	var keyMarker, versionIDMarker *string
	for {
		listVersionsResp, err := listObjectVersions(ctx, s3cli, bucket, keyMarker, versionIDMarker)
		if err != nil {
			return err
		}

		if len(listVersionsResp.Versions) == 0 && len(listVersionsResp.DeleteMarkers) == 0 {
			return nil
		}

		ids := collectVersionIdentifiers(listVersionsResp)

		err = batchDeleteVersions(ctx, s3cli, bucket, ids)
		if err != nil {
			return err
		}

		keyMarker, versionIDMarker = getNextVersionMarkers(listVersionsResp)
		if keyMarker == nil && versionIDMarker == nil {
			return nil
		}
	}
}

func listObjectVersions(ctx context.Context, s3cli *s3.Client, bucket string, keyMarker, versionIDMarker *string) (*s3.ListObjectVersionsOutput, error) {
	listVersionsResp, err := s3cli.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
		Bucket:          &bucket,
		KeyMarker:       keyMarker,
		VersionIdMarker: versionIDMarker,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list object versions in bucket %s: %w", bucket, err)
	}

	return listVersionsResp, nil
}

func collectVersionIdentifiers(listVersionsResp *s3.ListObjectVersionsOutput) []s3typesObjectIdentifier {
	ids := make([]s3typesObjectIdentifier, 0, len(listVersionsResp.Versions)+len(listVersionsResp.DeleteMarkers))

	for _, v := range listVersionsResp.Versions {
		ids = append(ids, s3typesObjectIdentifier{
			Key:       v.Key,
			VersionId: v.VersionId,
		})
	}

	for _, dm := range listVersionsResp.DeleteMarkers {
		ids = append(ids, s3typesObjectIdentifier{
			Key:       dm.Key,
			VersionId: dm.VersionId,
		})
	}

	return ids
}

func batchDeleteVersions(ctx context.Context, s3cli *s3.Client, bucket string, ids []s3typesObjectIdentifier) error {
	const batch = 1000

	for index := 0; index < len(ids); index += batch {
		end := index + batch
		if end > len(ids) {
			end = len(ids)
		}

		chunk := ids[index:end]

		_, err := s3cli.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: &bucket,
			Delete: &s3typesDelete{Objects: chunk, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("failed to delete object versions batch from bucket %s: %w", bucket, err)
		}
	}

	return nil
}

func getNextVersionMarkers(listVersionsResp *s3.ListObjectVersionsOutput) (*string, *string) {
	if aws.ToBool(listVersionsResp.IsTruncated) && (listVersionsResp.NextKeyMarker != nil || listVersionsResp.NextVersionIdMarker != nil) {
		return listVersionsResp.NextKeyMarker, listVersionsResp.NextVersionIdMarker
	}

	return nil, nil
}

// Local aliases to avoid importing s3/types explicitly at call sites.
type s3typesObject = s3types.Object
type s3typesObjectIdentifier = s3types.ObjectIdentifier
type s3typesDelete = s3types.Delete

// EnableBucketVersioning turns on S3 versioning for a bucket (data-plane).
func (m *StorageManager) EnableBucketVersioning(ctx context.Context, name string) error {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return err
	}

	meta, err := cli.GetBucket(ctx, m.client.config.ProjectID, m.client.apiRegion(), name).Execute()
	if err != nil {
		return fmt.Errorf("get bucket for versioning: %w", err)
	}

	var bucketData objectstorage.Bucket
	if bb, ok := meta.GetBucketOk(); ok && bb != nil {
		bucketData = *bb
	} else {
		return ErrBucketInfoMissing
	}

	u, err := url.Parse(bucketData.GetUrlPathStyle())
	if err != nil {
		return fmt.Errorf("parse bucket path url: %w", err)
	}

	endpoint := u.Scheme + "://" + u.Host

	accessKey, secretKey, keyID, groupID, err := m.ensureTemporaryAccessKey(ctx)
	if err != nil {
		return err
	}

	defer func() {
		_, _ = cli.DeleteAccessKey(ctx, m.client.config.ProjectID, m.client.apiRegion(), keyID).CredentialsGroup(groupID).Execute()
	}()

	cfg := m.createAWSConfig(accessKey, secretKey)
	s3cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})

	_, err = s3cli.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:              &name,
		ChecksumAlgorithm:   "",
		ContentMD5:          nil,
		ExpectedBucketOwner: nil,
		MFA:                 nil,
		VersioningConfiguration: &s3types.VersioningConfiguration{
			Status:    s3types.BucketVersioningStatusEnabled,
			MFADelete: "",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to enable bucket versioning for %s: %w", name, err)
	}

	return nil
}

// SetBucketLifecycleNoncurrentDays sets a lifecycle rule to expire noncurrent versions after N days.
func (m *StorageManager) SetBucketLifecycleNoncurrentDays(ctx context.Context, name string, days int32) error {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return err
	}

	bucketData, err := m.getBucketInfo(ctx, cli, name)
	if err != nil {
		return err
	}

	endpoint, err := m.extractBucketEndpoint(bucketData)
	if err != nil {
		return err
	}

	s3cli, _, _, cleanup, err := m.createS3ClientWithTempKey(ctx, endpoint)
	if err != nil {
		return err
	}
	defer cleanup()

	return m.applyLifecycleConfiguration(ctx, s3cli, name, days)
}

func (m *StorageManager) getBucketInfo(ctx context.Context, cli objectstorage.DefaultAPI, name string) (objectstorage.Bucket, error) {
	meta, err := cli.GetBucket(ctx, m.client.config.ProjectID, m.client.apiRegion(), name).Execute()
	if err != nil {
		return objectstorage.Bucket{}, fmt.Errorf("get bucket for lifecycle: %w", err)
	}

	if bb, ok := meta.GetBucketOk(); ok && bb != nil {
		return *bb, nil
	}

	return objectstorage.Bucket{}, ErrBucketInfoMissing
}

func (m *StorageManager) extractBucketEndpoint(bucketData objectstorage.Bucket) (string, error) {
	u, err := url.Parse(bucketData.GetUrlPathStyle())
	if err != nil {
		return "", fmt.Errorf("parse bucket path url: %w", err)
	}

	return u.Scheme + "://" + u.Host, nil
}

func (m *StorageManager) createS3ClientWithTempKey(ctx context.Context, endpoint string) (*s3.Client, string, string, func(), error) {
	accessKey, secretKey, keyID, groupID, err := m.ensureTemporaryAccessKey(ctx)
	if err != nil {
		return nil, "", "", nil, err
	}

	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return nil, "", "", nil, err
	}

	cleanup := func() {
		_, _ = cli.DeleteAccessKey(ctx, m.client.config.ProjectID, m.client.apiRegion(), keyID).CredentialsGroup(groupID).Execute()
	}

	cfg := m.createAWSConfig(accessKey, secretKey)
	s3cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})

	return s3cli, keyID, groupID, cleanup, nil
}

func (m *StorageManager) applyLifecycleConfiguration(ctx context.Context, s3cli *s3.Client, name string, days int32) error {
	_, err := s3cli.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: &name,
		LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{
			Rules: []s3types.LifecycleRule{
				{
					ID:     aws.String("DeleteOldObjects"),
					Status: s3types.ExpirationStatusEnabled,
					NoncurrentVersionExpiration: &s3types.NoncurrentVersionExpiration{
						NoncurrentDays: &days,
					},
					AbortIncompleteMultipartUpload: &s3types.AbortIncompleteMultipartUpload{
						DaysAfterInitiation: aws.Int32(s3LifecycleDays),
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to set bucket lifecycle for %s: %w", name, err)
	}

	return nil
}

// EnsureObjectStorageCredentialsGroup ensures a credentials group exists and returns its ID.
func (m *StorageManager) EnsureObjectStorageCredentialsGroup(ctx context.Context, displayName string) (string, error) {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return "", err
	}
	// Try to find existing
	resp, err := cli.ListCredentialsGroups(ctx, m.client.config.ProjectID, m.client.apiRegion()).Execute()
	if err == nil {
		if groups, ok := resp.GetCredentialsGroupsOk(); ok {
			for _, g := range groups {
				if strings.EqualFold(g.GetDisplayName(), displayName) {
					return g.GetCredentialsGroupId(), nil
				}
			}
		}
	}
	// Create new
	payload := objectstorage.NewCreateCredentialsGroupPayload(displayName)

	_, err = cli.CreateCredentialsGroup(ctx, m.client.config.ProjectID, m.client.apiRegion()).
		CreateCredentialsGroupPayload(*payload).Execute()
	if err != nil {
		return "", fmt.Errorf("create credentials group: %w", err)
	}
	// Fetch ID
	resp, err = cli.ListCredentialsGroups(ctx, m.client.config.ProjectID, m.client.apiRegion()).Execute()
	if err == nil {
		if groups, ok := resp.GetCredentialsGroupsOk(); ok {
			for _, g := range groups {
				if strings.EqualFold(g.GetDisplayName(), displayName) {
					return g.GetCredentialsGroupId(), nil
				}
			}
		}
	}

	return "", ErrCredentialsGroupNotFoundAfterCreation(displayName)
}

// CreateCredentialsGroup creates a new credentials group.
func (m *StorageManager) CreateCredentialsGroup(ctx context.Context, req *cpi.CredentialsGroupRequest) (*cpi.CredentialsGroup, error) {
	logger.WithOperation("CreateCredentialsGroup").Infof("Creating credentials group: %s", req.Name)

	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return nil, err
	}

	// Create the credentials group using SDK
	payload := objectstorage.NewCreateCredentialsGroupPayload(req.Name)

	_, err = cli.CreateCredentialsGroup(ctx, m.client.config.ProjectID, m.client.apiRegion()).CreateCredentialsGroupPayload(*payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to create credentials group: %w", err)
	}

	// Use the request name as ID since SDK response structure is unclear
	group := &cpi.CredentialsGroup{
		ID:        req.Name, // Use name as ID for now
		Name:      req.Name,
		CreatedAt: time.Now(),
	}

	return group, nil
}

// DeleteCredentialsGroup removes a credentials group by ID (STACKIT-specific).
func (m *StorageManager) DeleteCredentialsGroup(ctx context.Context, groupID string) error {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return err
	}

	_, err = cli.DeleteCredentialsGroup(ctx, m.client.config.ProjectID, m.client.apiRegion(), groupID).Execute()
	if err != nil {
		return fmt.Errorf("delete credentials group %s: %w", groupID, err)
	}

	return nil
}
