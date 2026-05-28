package aws

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	volumeWaitTimeout    = 5 * time.Minute
	volumeAttachTimeout  = 5 * time.Minute
	snapshotWaitTimeout  = 30 * time.Minute
	volumePollInterval   = 5 * time.Second
	snapshotPollInterval = 10 * time.Second
	tagKeyName           = "Name"
	initialTagCapacity   = 2
)

// getEC2 returns the EC2API to use for this manager.
// In tests, m.ec2 is set directly. In production, m.ec2 is nil and
// this falls back to the real client, preserving existing behaviour.
func (m *StorageManager) getEC2(ctx context.Context) (EC2API, error) {
	if m.ec2 != nil {
		return m.ec2, nil
	}

	return m.client.getEC2Client(ctx)
}

// getS3 returns the S3API to use for this manager.
// In tests, m.s3 is set directly. In production, m.s3 is nil and
// this falls back to the real client, preserving existing behaviour.
func (m *StorageManager) getS3(ctx context.Context) (S3API, error) {
	if m.s3 != nil {
		return m.s3, nil
	}

	return m.client.getS3Client(ctx)
}

// EBS Volume Operations

// CreateVolume creates a new EBS volume.
//
//nolint:funlen // Volume creation requires extensive parameter handling and tagging
func (m *StorageManager) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	logger.WithOperation("CreateVolume").Infof("Creating EBS volume: %s", req.Name)

	cli, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	// Determine volume size
	size := req.Size
	if req.SizeGB > 0 {
		size = req.SizeGB
	}

	if size == 0 {
		size = 10 // Default 10GB
	}

	// Determine volume type
	volumeType := req.VolumeType
	if volumeType == "" {
		volumeType = req.Type
	}

	if volumeType == "" {
		volumeType = "gp3" // Default to gp3 (general purpose SSD)
	}

	// Validate availability zone
	if req.AvailabilityZone == "" {
		return nil, &cpi.ProviderError{
			Provider: ProviderName,
			Code:     "InvalidParameter",
			Message:  "AvailabilityZone is required for EBS volume creation",
		}
	}

	// Build create volume input
	//nolint:gosec // Size validation handled above, max volume size is provider-enforced
	input := &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String(req.AvailabilityZone),
		Size:             aws.Int32(int32(size)),
		VolumeType:       types.VolumeType(volumeType),
		Encrypted:        aws.Bool(req.Encrypted),
	}

	// Add tags including name
	tags := make([]types.Tag, 0, len(req.Tags)+initialTagCapacity)
	if req.Name != "" {
		tags = append(tags, types.Tag{
			Key:   aws.String(tagKeyName),
			Value: aws.String(req.Name),
		})
	}

	// Add managed-by tag only if not already present in req.Tags
	if _, exists := req.Tags["managed-by"]; !exists {
		tags = append(tags, types.Tag{
			Key:   aws.String("managed-by"),
			Value: aws.String("ocfp"),
		})
	}

	for k, v := range req.Tags {
		tags = append(tags, types.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}

	input.TagSpecifications = []types.TagSpecification{
		{
			ResourceType: types.ResourceTypeVolume,
			Tags:         tags,
		},
	}

	// Create the volume
	result, err := cli.CreateVolume(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to create EBS volume")
	}

	// Convert CreateVolumeOutput to types.Volume for mapping
	vol := &types.Volume{
		Attachments:        result.Attachments,
		AvailabilityZone:   result.AvailabilityZone,
		CreateTime:         result.CreateTime,
		Encrypted:          result.Encrypted,
		FastRestored:       result.FastRestored,
		Iops:               result.Iops,
		KmsKeyId:           result.KmsKeyId,
		MultiAttachEnabled: result.MultiAttachEnabled,
		OutpostArn:         result.OutpostArn,
		Size:               result.Size,
		SnapshotId:         result.SnapshotId,
		SseType:            result.SseType,
		State:              result.State,
		Tags:               result.Tags,
		Throughput:         result.Throughput,
		VolumeId:           result.VolumeId,
		VolumeType:         result.VolumeType,
	}

	volume := m.mapVolume(vol)

	// Wait for volume to become available
	err = m.waitForVolumeState(ctx, *result.VolumeId, cpi.ResourceStateAvailable, volumeWaitTimeout)
	if err != nil {
		return nil, fmt.Errorf("volume failed to become available: %w", err)
	}

	logger.WithOperation("CreateVolume").Infof("Created EBS volume: %s", volume.ID)

	return volume, nil
}

// GetVolume retrieves an EBS volume by ID.
//
//nolint:dupl // Similar pattern to GetSnapshot but operates on different resources
func (m *StorageManager) GetVolume(ctx context.Context, volumeID string) (*cpi.Volume, error) {
	logger.WithOperation("GetVolume").Debugf("Getting EBS volume: %s", volumeID)

	cli, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	input := &ec2.DescribeVolumesInput{
		VolumeIds: []string{volumeID},
	}

	result, err := cli.DescribeVolumes(ctx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidVolume.NotFound" {
			return nil, &cpi.ProviderError{
				Provider: ProviderName,
				Code:     "NotFound",
				Message:  fmt.Sprintf("volume %s not found", volumeID),
			}
		}

		return nil, wrapError(err, "failed to get EBS volume")
	}

	if len(result.Volumes) == 0 {
		return nil, &cpi.ProviderError{
			Provider: ProviderName,
			Code:     "NotFound",
			Message:  fmt.Sprintf("volume %s not found", volumeID),
		}
	}

	return m.mapVolume(&result.Volumes[0]), nil
}

// ListVolumes lists all EBS volumes with optional filters.
func (m *StorageManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	logger.WithOperation("ListVolumes").Debug("Listing EBS volumes")

	cli, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	input := &ec2.DescribeVolumesInput{}

	// Add filters with proper tag handling
	ec2Filters := buildAWSTagFilters(filters)
	if len(ec2Filters) > 0 {
		input.Filters = ec2Filters
	}

	result, err := cli.DescribeVolumes(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to list EBS volumes")
	}

	volumes := make([]*cpi.Volume, 0, len(result.Volumes))
	for i := range result.Volumes {
		volumes = append(volumes, m.mapVolume(&result.Volumes[i]))
	}

	return volumes, nil
}

// AttachVolume attaches an EBS volume to an instance.
func (m *StorageManager) AttachVolume(ctx context.Context, volumeID, instanceID, device string) error {
	logger.WithOperation("AttachVolume").Infof("Attaching volume %s to instance %s", volumeID, instanceID)

	cli, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	// Default device name if not specified
	if device == "" {
		device = "/dev/sdf"
	}

	input := &ec2.AttachVolumeInput{
		VolumeId:   aws.String(volumeID),
		InstanceId: aws.String(instanceID),
		Device:     aws.String(device),
	}

	_, err = cli.AttachVolume(ctx, input)
	if err != nil {
		return wrapError(err, "failed to attach volume")
	}

	// Wait for volume to be in-use
	err = m.waitForVolumeState(ctx, volumeID, cpi.ResourceStateInUse, volumeAttachTimeout)
	if err != nil {
		return fmt.Errorf("volume failed to attach: %w", err)
	}

	logger.WithOperation("AttachVolume").Infof("Volume %s attached to instance %s", volumeID, instanceID)

	return nil
}

// DetachVolume detaches an EBS volume from an instance.
func (m *StorageManager) DetachVolume(ctx context.Context, volumeID, instanceID string) error {
	logger.WithOperation("DetachVolume").Infof("Detaching volume %s from instance %s", volumeID, instanceID)

	cli, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	input := &ec2.DetachVolumeInput{
		VolumeId:   aws.String(volumeID),
		InstanceId: aws.String(instanceID),
		Force:      aws.Bool(false), // Don't force detach by default
	}

	_, err = cli.DetachVolume(ctx, input)
	if err != nil {
		return wrapError(err, "failed to detach volume")
	}

	// Wait for volume to become available
	err = m.waitForVolumeState(ctx, volumeID, cpi.ResourceStateAvailable, volumeAttachTimeout)
	if err != nil {
		return fmt.Errorf("volume failed to detach: %w", err)
	}

	logger.WithOperation("DetachVolume").Infof("Volume %s detached from instance %s", volumeID, instanceID)

	return nil
}

// ResizeVolume modifies an EBS volume's size.
func (m *StorageManager) ResizeVolume(ctx context.Context, volumeID string, newSize int) error {
	logger.WithOperation("ResizeVolume").Infof("Resizing volume %s to %d GB", volumeID, newSize)

	cli, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	//nolint:gosec // Size validation handled by caller, max volume size is provider-enforced
	input := &ec2.ModifyVolumeInput{
		VolumeId: aws.String(volumeID),
		Size:     aws.Int32(int32(newSize)),
	}

	_, err = cli.ModifyVolume(ctx, input)
	if err != nil {
		return wrapError(err, "failed to resize volume")
	}

	logger.WithOperation("ResizeVolume").Infof("Volume %s resize initiated", volumeID)

	return nil
}

// DeleteVolume deletes an EBS volume.
func (m *StorageManager) DeleteVolume(ctx context.Context, volumeID string) error {
	logger.WithOperation("DeleteVolume").Infof("Deleting volume: %s", volumeID)

	cli, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	input := &ec2.DeleteVolumeInput{
		VolumeId: aws.String(volumeID),
	}

	_, err = cli.DeleteVolume(ctx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidVolume.NotFound" {
			// Volume already deleted
			return nil
		}

		return wrapError(err, "failed to delete volume")
	}

	logger.WithOperation("DeleteVolume").Infof("Volume deleted: %s", volumeID)

	return nil
}

// EBS Snapshot Operations

// CreateSnapshot creates a snapshot of an EBS volume.
//
//nolint:funlen // Snapshot creation requires extensive parameter handling and type conversion
func (m *StorageManager) CreateSnapshot(ctx context.Context, volumeID, name string) (*cpi.Snapshot, error) {
	logger.WithOperation("CreateSnapshot").Infof("Creating snapshot of volume %s: %s", volumeID, name)

	cli, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	description := "Snapshot of volume " + volumeID
	if name != "" {
		description = name
	}

	input := &ec2.CreateSnapshotInput{
		VolumeId:    aws.String(volumeID),
		Description: aws.String(description),
	}

	// Add tags
	tags := []types.Tag{
		{
			Key:   aws.String("managed-by"),
			Value: aws.String("ocfp"),
		},
	}
	if name != "" {
		tags = append(tags, types.Tag{
			Key:   aws.String(tagKeyName),
			Value: aws.String(name),
		})
	}

	input.TagSpecifications = []types.TagSpecification{
		{
			ResourceType: types.ResourceTypeSnapshot,
			Tags:         tags,
		},
	}

	result, err := cli.CreateSnapshot(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to create snapshot")
	}

	// Convert CreateSnapshotOutput to types.Snapshot for mapping
	snap := &types.Snapshot{
		DataEncryptionKeyId: result.DataEncryptionKeyId,
		Description:         result.Description,
		Encrypted:           result.Encrypted,
		KmsKeyId:            result.KmsKeyId,
		OutpostArn:          result.OutpostArn,
		OwnerAlias:          result.OwnerAlias,
		OwnerId:             result.OwnerId,
		Progress:            result.Progress,
		RestoreExpiryTime:   result.RestoreExpiryTime,
		SnapshotId:          result.SnapshotId,
		SseType:             result.SseType,
		StartTime:           result.StartTime,
		State:               result.State,
		StateMessage:        result.StateMessage,
		StorageTier:         result.StorageTier,
		Tags:                result.Tags,
		VolumeId:            result.VolumeId,
		VolumeSize:          result.VolumeSize,
	}

	snapshot := m.mapSnapshot(snap)

	logger.WithOperation("CreateSnapshot").Infof("Created snapshot: %s", snapshot.ID)

	return snapshot, nil
}

// GetSnapshot retrieves a snapshot by ID.
//
//nolint:dupl // Similar pattern to GetVolume but operates on different resources
func (m *StorageManager) GetSnapshot(ctx context.Context, snapshotID string) (*cpi.Snapshot, error) {
	logger.WithOperation("GetSnapshot").Debugf("Getting snapshot: %s", snapshotID)

	cli, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	input := &ec2.DescribeSnapshotsInput{
		SnapshotIds: []string{snapshotID},
	}

	result, err := cli.DescribeSnapshots(ctx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidSnapshot.NotFound" {
			return nil, &cpi.ProviderError{
				Provider: ProviderName,
				Code:     "NotFound",
				Message:  fmt.Sprintf("snapshot %s not found", snapshotID),
			}
		}

		return nil, wrapError(err, "failed to get snapshot")
	}

	if len(result.Snapshots) == 0 {
		return nil, &cpi.ProviderError{
			Provider: ProviderName,
			Code:     "NotFound",
			Message:  fmt.Sprintf("snapshot %s not found", snapshotID),
		}
	}

	return m.mapSnapshot(&result.Snapshots[0]), nil
}

// ListSnapshots lists snapshots for a volume or all snapshots with optional tag filtering.
func (m *StorageManager) ListSnapshots(ctx context.Context, volumeID string, filters map[string]string) ([]*cpi.Snapshot, error) {
	logger.WithOperation("ListSnapshots").Debug("Listing snapshots")

	cli, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	input := &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"}, // Only list snapshots owned by this account
	}

	// Build filters: combine volume-id filter (if specified) with tag filters
	combinedFilters := make(map[string]string)

	// Add volume-id filter if specified
	if volumeID != "" {
		combinedFilters["volume-id"] = volumeID
	}

	// Add any additional filters (including bloc, managed-by tags)
	for k, v := range filters {
		combinedFilters[k] = v
	}

	// Apply filters with proper tag handling
	if len(combinedFilters) > 0 {
		input.Filters = buildAWSTagFilters(combinedFilters)
	}

	result, err := cli.DescribeSnapshots(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to list snapshots")
	}

	snapshots := make([]*cpi.Snapshot, 0, len(result.Snapshots))
	for i := range result.Snapshots {
		snapshots = append(snapshots, m.mapSnapshot(&result.Snapshots[i]))
	}

	return snapshots, nil
}

// DeleteSnapshot deletes a snapshot.
func (m *StorageManager) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	logger.WithOperation("DeleteSnapshot").Infof("Deleting snapshot: %s", snapshotID)

	cli, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	input := &ec2.DeleteSnapshotInput{
		SnapshotId: aws.String(snapshotID),
	}

	_, err = cli.DeleteSnapshot(ctx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidSnapshot.NotFound" {
			// Snapshot already deleted
			return nil
		}

		return wrapError(err, "failed to delete snapshot")
	}

	logger.WithOperation("DeleteSnapshot").Infof("Snapshot deleted: %s", snapshotID)

	return nil
}

// S3 Bucket Operations

// CreateBucket creates a new S3 bucket.
func (m *StorageManager) CreateBucket(ctx context.Context, req *cpi.BucketRequest) (*cpi.Bucket, error) {
	logger.WithOperation("CreateBucket").Infof("Creating S3 bucket: %s", req.Name)

	cli, err := m.getS3(ctx)
	if err != nil {
		return nil, err
	}

	input := &s3.CreateBucketInput{
		Bucket: aws.String(req.Name),
	}

	// Add location constraint for non-us-east-1 regions
	if m.client.config.Region != "us-east-1" {
		input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(m.client.config.Region),
		}
	}

	_, err = cli.CreateBucket(ctx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "BucketAlreadyOwnedByYou" {
			// Bucket already exists and is owned by us
			logger.WithOperation("CreateBucket").Infof("Bucket already exists: %s", req.Name)
		} else {
			return nil, wrapError(err, "failed to create S3 bucket")
		}
	}

	// Add tags if specified
	if len(req.Tags) > 0 {
		tags := make([]s3types.Tag, 0, len(req.Tags)+1)

		// Add managed-by tag only if not already present in req.Tags
		if _, exists := req.Tags["managed-by"]; !exists {
			tags = append(tags, s3types.Tag{
				Key:   aws.String("managed-by"),
				Value: aws.String("ocfp"),
			})
		}

		for k, v := range req.Tags {
			tags = append(tags, s3types.Tag{
				Key:   aws.String(k),
				Value: aws.String(v),
			})
		}

		tagInput := &s3.PutBucketTaggingInput{
			Bucket: aws.String(req.Name),
			Tagging: &s3types.Tagging{
				TagSet: tags,
			},
		}

		_, err = cli.PutBucketTagging(ctx, tagInput)
		if err != nil {
			logger.WithOperation("CreateBucket").Warnf("Failed to add tags to bucket %s: %v", req.Name, err)
		}
	}

	logger.WithOperation("CreateBucket").Infof("Created S3 bucket: %s", req.Name)

	return m.GetBucket(ctx, req.Name)
}

// GetBucket retrieves S3 bucket information.
//
//nolint:funlen // Bucket retrieval requires multiple AWS API calls for complete information
func (m *StorageManager) GetBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	logger.WithOperation("GetBucket").Debugf("Getting S3 bucket: %s", name)

	cli, err := m.getS3(ctx)
	if err != nil {
		return nil, err
	}

	// Check if bucket exists
	_, err = cli.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(name),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchBucket") {
			return nil, &cpi.ProviderError{
				Provider: ProviderName,
				Code:     "NotFound",
				Message:  fmt.Sprintf("bucket %s not found", name),
			}
		}

		return nil, wrapError(err, "failed to get bucket")
	}

	// Get bucket location
	region := m.client.config.Region
	locationResult, err := cli.GetBucketLocation(ctx, &s3.GetBucketLocationInput{
		Bucket: aws.String(name),
	})

	if err == nil && locationResult.LocationConstraint != "" {
		region = string(locationResult.LocationConstraint)
	}

	// Get bucket tags
	tags := make(map[string]string)

	tagResult, err := cli.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{
		Bucket: aws.String(name),
	})
	if err == nil {
		for _, tag := range tagResult.TagSet {
			if tag.Key != nil && tag.Value != nil {
				tags[*tag.Key] = *tag.Value
			}
		}
	}

	// Get versioning status
	versioning := false

	versionResult, err := cli.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{
		Bucket: aws.String(name),
	})
	if err == nil && versionResult.Status == s3types.BucketVersioningStatusEnabled {
		versioning = true
	}

	// Get encryption status
	encryption := false

	_, err = cli.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{
		Bucket: aws.String(name),
	})
	if err == nil {
		encryption = true
	}

	bucket := &cpi.Bucket{
		ID:           name,
		Name:         name,
		Region:       region,
		StorageClass: "STANDARD",
		Versioning:   versioning,
		Encryption:   encryption,
		Public:       false,
		Tags:         tags,
		CreatedAt:    time.Now(), // S3 doesn't provide creation time via API
	}

	return bucket, nil
}

// ListBuckets lists all S3 buckets.
func (m *StorageManager) ListBuckets(ctx context.Context) ([]*cpi.Bucket, error) {
	logger.WithOperation("ListBuckets").Debug("Listing S3 buckets")

	cli, err := m.getS3(ctx)
	if err != nil {
		return nil, err
	}

	result, err := cli.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, wrapError(err, "failed to list S3 buckets")
	}

	buckets := make([]*cpi.Bucket, 0, len(result.Buckets))
	for _, bucketItem := range result.Buckets {
		if bucketItem.Name == nil {
			continue
		}

		// Get detailed bucket info
		bucket, err := m.GetBucket(ctx, *bucketItem.Name)
		if err != nil {
			logger.WithOperation("ListBuckets").Warnf("Failed to get bucket details for %s: %v", *bucketItem.Name, err)
			// Create basic bucket info
			bucket = &cpi.Bucket{
				ID:        *bucketItem.Name,
				Name:      *bucketItem.Name,
				Region:    m.client.config.Region,
				CreatedAt: *bucketItem.CreationDate,
			}
		}

		buckets = append(buckets, bucket)
	}

	return buckets, nil
}

// DeleteBucket deletes an S3 bucket (must be empty).
func (m *StorageManager) DeleteBucket(ctx context.Context, name string) error {
	logger.WithOperation("DeleteBucket").Infof("Deleting S3 bucket: %s", name)

	cli, err := m.getS3(ctx)
	if err != nil {
		return err
	}

	input := &s3.DeleteBucketInput{
		Bucket: aws.String(name),
	}

	_, err = cli.DeleteBucket(ctx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchBucket" || apiErr.ErrorCode() == "NotFound") {
			// Bucket already deleted
			return nil
		}

		return wrapError(err, "failed to delete S3 bucket")
	}

	logger.WithOperation("DeleteBucket").Infof("Bucket deleted: %s", name)

	return nil
}

// IsBucketEmpty checks if an S3 bucket is empty (contains no objects or versions).
func (m *StorageManager) IsBucketEmpty(ctx context.Context, name string) (bool, error) {
	logger.WithOperation("IsBucketEmpty").Debugf("Checking if S3 bucket is empty: %s", name)

	cli, err := m.getS3(ctx)
	if err != nil {
		return false, err
	}

	// Check for objects (with minimal result set for efficiency)
	listInput := &s3.ListObjectsV2Input{
		Bucket:  aws.String(name),
		MaxKeys: aws.Int32(1), // Only need to know if at least one object exists
	}

	result, err := cli.ListObjectsV2(ctx, listInput)
	if err != nil {
		return false, wrapError(err, "failed to list objects in bucket")
	}

	// If there are any objects, bucket is not empty
	if result.KeyCount != nil && *result.KeyCount > 0 {
		logger.WithOperation("IsBucketEmpty").Debugf("Bucket %s is not empty: found %d object(s)", name, *result.KeyCount)

		return false, nil
	}

	// Also check for versioned objects (delete markers and non-current versions)
	versionsInput := &s3.ListObjectVersionsInput{
		Bucket:  aws.String(name),
		MaxKeys: aws.Int32(1), // Only need to know if at least one version exists
	}

	versionsResult, err := cli.ListObjectVersions(ctx, versionsInput)
	if err != nil {
		// If versioning is not enabled, this might fail - treat as no versions
		logger.WithOperation("IsBucketEmpty").Debugf("Could not list versions for bucket %s: %v", name, err)

		return true, nil
	}

	// Check if there are any versions or delete markers
	hasVersions := len(versionsResult.Versions) > 0 || len(versionsResult.DeleteMarkers) > 0

	if hasVersions {
		logger.WithOperation("IsBucketEmpty").Debugf("Bucket %s is not empty: found object versions or delete markers", name)

		return false, nil
	}

	logger.WithOperation("IsBucketEmpty").Debugf("Bucket %s is empty", name)

	return true, nil
}

// EmptyBucket deletes all objects in an S3 bucket.
//
//nolint:funlen,nilerr // Bucket emptying requires iteration and pagination, errors are handled appropriately
func (m *StorageManager) EmptyBucket(ctx context.Context, name string) error {
	logger.WithOperation("EmptyBucket").Infof("Emptying S3 bucket: %s", name)

	cli, err := m.getS3(ctx)
	if err != nil {
		return err
	}

	// Delete all current objects using manual pagination (no SDK paginator,
	// so cli stays as S3API and is fully testable via interface mocks).
	var contToken *string

	for {
		listInput := &s3.ListObjectsV2Input{
			Bucket:            aws.String(name),
			ContinuationToken: contToken,
		}

		page, listErr := cli.ListObjectsV2(ctx, listInput)
		if listErr != nil {
			return wrapError(listErr, "failed to list objects in bucket")
		}

		if len(page.Contents) > 0 {
			objects := make([]s3types.ObjectIdentifier, 0, len(page.Contents))
			for _, obj := range page.Contents {
				objects = append(objects, s3types.ObjectIdentifier{Key: obj.Key})
			}

			deleteInput := &s3.DeleteObjectsInput{
				Bucket: aws.String(name),
				Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
			}

			if _, delErr := cli.DeleteObjects(ctx, deleteInput); delErr != nil {
				return wrapError(delErr, "failed to delete objects from bucket")
			}

			logger.WithOperation("EmptyBucket").Debugf("Deleted %d objects from bucket %s", len(objects), name)
		}

		if page.NextContinuationToken == nil || *page.NextContinuationToken == "" {
			break
		}

		contToken = page.NextContinuationToken
	}

	// Delete all object versions using manual pagination (no SDK paginator).
	var keyMarker, versionIDMarker *string

	for {
		versionInput := &s3.ListObjectVersionsInput{
			Bucket:          aws.String(name),
			KeyMarker:       keyMarker,
			VersionIdMarker: versionIDMarker,
		}

		page, pageErr := cli.ListObjectVersions(ctx, versionInput)
		if pageErr != nil {
			// Versioning may not be enabled; treat as no versions.
			break
		}

		objects := make([]s3types.ObjectIdentifier, 0, len(page.Versions)+len(page.DeleteMarkers))

		for _, version := range page.Versions {
			objects = append(objects, s3types.ObjectIdentifier{
				Key:       version.Key,
				VersionId: version.VersionId,
			})
		}

		for _, marker := range page.DeleteMarkers {
			objects = append(objects, s3types.ObjectIdentifier{
				Key:       marker.Key,
				VersionId: marker.VersionId,
			})
		}

		if len(objects) == 0 {
			break
		}

		deleteInput := &s3.DeleteObjectsInput{
			Bucket: aws.String(name),
			Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		}

		if _, delErr := cli.DeleteObjects(ctx, deleteInput); delErr != nil {
			return wrapError(delErr, "failed to delete object versions from bucket")
		}

		logger.WithOperation("EmptyBucket").Debugf("Deleted %d object versions from bucket %s", len(objects), name)

		if !aws.ToBool(page.IsTruncated) {
			break
		}

		keyMarker = page.NextKeyMarker
		versionIDMarker = page.NextVersionIdMarker
	}

	logger.WithOperation("EmptyBucket").Infof("Bucket emptied: %s", name)

	return nil
}

// CreateCredentialsGroup creates S3 access credentials (not implemented for AWS).
func (m *StorageManager) CreateCredentialsGroup(_ctx context.Context, _req *cpi.CredentialsGroupRequest) (*cpi.CredentialsGroup, error) {
	return nil, &cpi.ProviderError{
		Provider: ProviderName,
		Code:     "NotImplemented",
		Message:  "CreateCredentialsGroup is not applicable for AWS (use IAM users/roles instead)",
	}
}

// Helper Functions

// mapVolume converts an EC2 Volume to cpi.Volume.
func (m *StorageManager) mapVolume(volume *types.Volume) *cpi.Volume {
	if volume == nil {
		return nil
	}

	vol := &cpi.Volume{
		ID:         aws.ToString(volume.VolumeId),
		Size:       int(aws.ToInt32(volume.Size)),
		Type:       string(volume.VolumeType),
		State:      m.mapVolumeState(volume.State),
		Encrypted:  aws.ToBool(volume.Encrypted),
		AttachedTo: "",
		Device:     "",
		Tags:       make(map[string]string),
		CreatedAt:  aws.ToTime(volume.CreateTime),
	}

	// Extract name from tags
	for _, tag := range volume.Tags {
		key := aws.ToString(tag.Key)

		value := aws.ToString(tag.Value)
		if key == tagKeyName {
			vol.Name = value
		}

		vol.Tags[key] = value
	}

	// Get attachment info
	if len(volume.Attachments) > 0 {
		attachment := volume.Attachments[0]
		vol.AttachedTo = aws.ToString(attachment.InstanceId)
		vol.Device = aws.ToString(attachment.Device)
	}

	// Add availability zone to tags
	if volume.AvailabilityZone != nil {
		vol.Tags["availability-zone"] = *volume.AvailabilityZone
	}

	return vol
}

// mapVolumeState converts EC2 VolumeState to cpi.ResourceState.
func (m *StorageManager) mapVolumeState(state types.VolumeState) cpi.ResourceState {
	switch state {
	case types.VolumeStateCreating:
		return cpi.ResourceStateCreating
	case types.VolumeStateAvailable:
		return cpi.ResourceStateAvailable
	case types.VolumeStateInUse:
		return cpi.ResourceStateInUse
	case types.VolumeStateDeleting:
		return cpi.ResourceStateDeleting
	case types.VolumeStateDeleted:
		return cpi.ResourceStateDeleted
	case types.VolumeStateError:
		return cpi.ResourceStateError
	default:
		return cpi.ResourceStateUnknown
	}
}

// mapSnapshot converts an EC2 Snapshot to cpi.Snapshot.
func (m *StorageManager) mapSnapshot(snapshot *types.Snapshot) *cpi.Snapshot {
	if snapshot == nil {
		return nil
	}

	snap := &cpi.Snapshot{
		ID:          aws.ToString(snapshot.SnapshotId),
		VolumeID:    aws.ToString(snapshot.VolumeId),
		Size:        int(aws.ToInt32(snapshot.VolumeSize)),
		State:       m.mapSnapshotState(snapshot.State),
		Description: aws.ToString(snapshot.Description),
		Tags:        make(map[string]string),
		CreatedAt:   aws.ToTime(snapshot.StartTime),
	}

	// Extract name from tags
	for _, tag := range snapshot.Tags {
		key := aws.ToString(tag.Key)

		value := aws.ToString(tag.Value)
		if key == tagKeyName {
			snap.Name = value
		}

		snap.Tags[key] = value
	}

	return snap
}

// mapSnapshotState converts EC2 SnapshotState to cpi.ResourceState.
func (m *StorageManager) mapSnapshotState(state types.SnapshotState) cpi.ResourceState {
	switch state {
	case types.SnapshotStatePending:
		return cpi.ResourceStateCreating
	case types.SnapshotStateCompleted:
		return cpi.ResourceStateAvailable
	case types.SnapshotStateError:
		return cpi.ResourceStateError
	case types.SnapshotStateRecoverable, types.SnapshotStateRecovering:
		return cpi.ResourceStateCreating
	default:
		return cpi.ResourceStateUnknown
	}
}

// waitForVolumeState waits for a volume to reach the desired state.
func (m *StorageManager) waitForVolumeState(ctx context.Context, volumeID string, desiredState cpi.ResourceState, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	interval := m.pollInterval
	if interval == 0 {
		interval = volumePollInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %s (desired: %s)", ErrVolumeWaitTimeout, volumeID, desiredState)
		case <-ticker.C:
			volume, err := m.GetVolume(ctx, volumeID)
			if err != nil {
				return err
			}

			if volume.State == desiredState {
				return nil
			}

			if volume.State == cpi.ResourceStateError {
				return fmt.Errorf("%w: %s", ErrVolumeErrorState, volumeID)
			}
		}
	}
}
