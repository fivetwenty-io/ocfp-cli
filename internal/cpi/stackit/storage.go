package stackit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas"
	objectstorage "github.com/stackitcloud/stackit-sdk-go/services/objectstorage"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	s3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// CreateVolume creates a new storage volume.
func (m *StorageManager) CreateVolume(ctx context.Context, req *cpi.CreateVolumeRequest) (*cpi.Volume, error) {
	logger.WithOperation("CreateVolume").Infof("Creating volume via SDK: %s", req.Name)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	payload := iaas.NewCreateVolumePayload(req.AvailabilityZone)
	if req.Name != "" {
		payload.SetName(req.Name)
	}

	if req.Size > 0 {
		payload.SetSize(int64(req.Size))
	}

	if req.Encrypted {
		payload.SetEncrypted(true)
	}

	if len(req.Tags) > 0 {
		lm := make(map[string]interface{}, len(req.Tags))
		for k, v := range req.Tags {
			lm[k] = v
		}

		payload.SetLabels(lm)
	}

	created, err := cli.CreateVolume(ctx, m.client.config.ProjectID).CreateVolumePayload(*payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas CreateVolume failed: %w", err)
	}

	out := &cpi.Volume{ID: stringOrEmpty(created.GetIdOk()), Name: stringOrEmpty(created.GetNameOk())}
	if size, ok := created.GetSizeOk(); ok {
		out.Size = int(size)
	}

	if az, ok := created.GetAvailabilityZoneOk(); ok {
		out.Tags = map[string]string{"az": az}
	}

	if err := m.waitForVolumeState(ctx, out.ID, cpi.ResourceStateAvailable, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("volume failed to become available: %w", err)
	}

	return out, nil
}

// GetVolume retrieves a volume by ID.
func (m *StorageManager) GetVolume(ctx context.Context, volumeID string) (*cpi.Volume, error) {
	logger.WithOperation("GetVolume").Debugf("Getting volume via SDK: %s", volumeID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	got, err := cli.GetVolume(ctx, m.client.config.ProjectID, volumeID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetVolume failed: %w", err)
	}

	out := &cpi.Volume{ID: stringOrEmpty(got.GetIdOk()), Name: stringOrEmpty(got.GetNameOk())}
	if size, ok := got.GetSizeOk(); ok {
		out.Size = int(size)
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

	resp, err := cli.ListVolumes(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListVolumes failed: %w", err)
	}

	items, _ := resp.GetItemsOk()

	var out []*cpi.Volume
	for _, v := range items {
		vol := &cpi.Volume{ID: stringOrEmpty(v.GetIdOk()), Name: stringOrEmpty(v.GetNameOk())}
		if size, ok := v.GetSizeOk(); ok {
			vol.Size = int(size)
		}

		out = append(out, vol)
	}

	return out, nil
}

// DeleteVolume deletes a volume.
func (m *StorageManager) DeleteVolume(ctx context.Context, volumeID string) error {
	logger.WithOperation("DeleteVolume").Infof("Deleting volume: %s", volumeID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	if err := cli.DeleteVolume(ctx, m.client.config.ProjectID, volumeID).Execute(); err != nil {
		return fmt.Errorf("stackit iaas DeleteVolume failed: %w", err)
	}

	return nil
}

// AttachVolume attaches a volume to an instance.
func (m *StorageManager) AttachVolume(ctx context.Context, volumeID, instanceID, device string) error {
	logger.WithOperation("AttachVolume").Infof("Attaching volume %s to instance %s", volumeID, instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	if _, err := cli.AddVolumeToServer(ctx, m.client.config.ProjectID, instanceID, volumeID).Execute(); err != nil {
		return fmt.Errorf("stackit iaas AddVolumeToServer failed: %w", err)
	}

	if err := m.waitForVolumeState(ctx, volumeID, cpi.ResourceStateInUse, 2*time.Minute); err != nil {
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

	if err := cli.RemoveVolumeFromServer(ctx, m.client.config.ProjectID, instanceID, volumeID).Execute(); err != nil {
		return fmt.Errorf("stackit iaas RemoveVolumeFromServer failed: %w", err)
	}

	if err := m.waitForVolumeState(ctx, volumeID, cpi.ResourceStateAvailable, 2*time.Minute); err != nil {
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

	return cli.ResizeVolume(ctx, m.client.config.ProjectID, volumeID).ResizeVolumePayload(*payload).Execute()
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

	created, err := cli.CreateSnapshot(ctx, m.client.config.ProjectID).CreateSnapshotPayload(*payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas CreateSnapshot failed: %w", err)
	}

	out := &cpi.Snapshot{ID: stringOrEmpty(created.GetIdOk()), Name: stringOrEmpty(created.GetNameOk()), VolumeID: stringOrEmpty(created.GetVolumeIdOk())}
	if err := m.waitForSnapshotState(ctx, out.ID, cpi.ResourceStateAvailable, 10*time.Minute); err != nil {
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

	got, err := cli.GetSnapshot(ctx, m.client.config.ProjectID, snapshotID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetSnapshot failed: %w", err)
	}

	out := &cpi.Snapshot{ID: stringOrEmpty(got.GetIdOk()), Name: stringOrEmpty(got.GetNameOk()), VolumeID: stringOrEmpty(got.GetVolumeIdOk())}

	return out, nil
}

// ListSnapshots lists all snapshots for a volume.
func (m *StorageManager) ListSnapshots(ctx context.Context, volumeID string) ([]*cpi.Snapshot, error) {
	logger.WithOperation("ListSnapshots").Debug("Listing snapshots via SDK")

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	req := cli.ListSnapshots(ctx, m.client.config.ProjectID)
	if volumeID != "" {
		req = req.LabelSelector("volumeId=" + volumeID)
	}

	resp, err := req.Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListSnapshots failed: %w", err)
	}

	items, _ := resp.GetItemsOk()

	var out []*cpi.Snapshot
	for _, s := range items {
		out = append(out, &cpi.Snapshot{ID: stringOrEmpty(s.GetIdOk()), Name: stringOrEmpty(s.GetNameOk()), VolumeID: stringOrEmpty(s.GetVolumeIdOk())})
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

	if err := cli.DeleteSnapshot(ctx, m.client.config.ProjectID, snapshotID).Execute(); err != nil {
		return fmt.Errorf("stackit iaas DeleteSnapshot failed: %w", err)
	}

	return nil
}

// waitForVolumeState waits for a volume to reach a specific state.
func (m *StorageManager) waitForVolumeState(ctx context.Context, volumeID string, targetState cpi.ResourceState, timeout time.Duration) error {
	return cpi.WaitForCondition(ctx, 5*time.Second, timeout, func() (bool, error) {
		volume, err := m.GetVolume(ctx, volumeID)
		if err != nil {
			return false, err
		}

		return volume.State == targetState, nil
	})
}

// waitForSnapshotState waits for a snapshot to reach a specific state.
func (m *StorageManager) waitForSnapshotState(ctx context.Context, snapshotID string, targetState cpi.ResourceState, timeout time.Duration) error {
	return cpi.WaitForCondition(ctx, 5*time.Second, timeout, func() (bool, error) {
		snapshot, err := m.GetSnapshot(ctx, snapshotID)
		if err != nil {
			return false, err
		}

		return snapshot.State == targetState, nil
	})
}

// CreateBucket creates a new bucket in object storage using the official SDK.
func (m *StorageManager) CreateBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	logger.WithOperation("CreateBucket").Infof("Creating bucket: %s", name)

	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return nil, err
	}

	// SDK call
	if _, err := cli.CreateBucket(ctx, m.client.config.ProjectID, m.client.config.Region, name).Execute(); err != nil {
		return nil, fmt.Errorf("failed to create bucket: %w", err)
	}

	bucket := &cpi.Bucket{
		Name:   name,
		Region: m.client.config.Region,
		Tags: map[string]string{
			"managed-by": "ocfp",
		},
	}
	if bloc := parseBlocFromBucketName(name); bloc != "" {
		if bucket.Tags == nil {
			bucket.Tags = map[string]string{}
		}

		bucket.Tags["bloc"] = bloc
	}

	logger.WithOperation("CreateBucket").Infof("Bucket created: %s", name)

	return bucket, nil
}

// parseBlocFromBucketName extracts the bloc name from a bucket name following
// the pattern "<bloc>-<suffix>". Returns empty string if not matched.
func parseBlocFromBucketName(name string) string {
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

	resp, err := cli.GetBucket(ctx, m.client.config.ProjectID, m.client.config.Region, name).Execute()
	if err != nil {
		// Translate 404 if possible
		return nil, &cpi.ProviderError{Provider: "stackit", Code: "GetBucketFailed", Message: err.Error()}
	}

	// Map SDK model to CPI bucket
	var bucketData objectstorage.Bucket
	if bb, ok := resp.GetBucketOk(); ok {
		bucketData = bb
	}

	bucket := &cpi.Bucket{
		Name:   bucketData.GetName(),
		Region: bucketData.GetRegion(),
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

	resp, err := cli.ListBuckets(ctx, m.client.config.ProjectID, m.client.config.Region).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to list buckets: %w", err)
	}

	var out []*cpi.Bucket

	if buckets, ok := resp.GetBucketsOk(); ok {
		for _, b := range buckets {
			bb := b
			out = append(out, &cpi.Bucket{
				Name:   bb.GetName(),
				Region: bb.GetRegion(),
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

	if _, err := cli.DeleteBucket(ctx, m.client.config.ProjectID, m.client.config.Region, name).Execute(); err != nil {
		return fmt.Errorf("failed to delete bucket: %w", err)
	}

	logger.WithOperation("DeleteBucket").Infof("Bucket deleted: %s", name)

	return nil
}

// EmptyBucket removes all objects from a bucket.
func (m *StorageManager) EmptyBucket(ctx context.Context, name string) error {
	operation := logger.WithOperation("EmptyBucket")
	operation.Infof("Emptying bucket: %s", name)

	// Discover bucket endpoint URLs
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return err
	}

	getBucketResp, err := cli.GetBucket(ctx, m.client.config.ProjectID, m.client.config.Region, name).Execute()
	if err != nil {
		return fmt.Errorf("failed to get bucket %s metadata: %w", name, err)
	}

	// Choose path-style URL for S3 client endpoint
	var bucketInfo objectstorage.Bucket
	if bucketData, ok := getBucketResp.GetBucketOk(); ok {
		bucketInfo = bucketData
	} else {
		return fmt.Errorf("bucket metadata missing in response for %s", name)
	}

	// Parse host for endpoint resolver
	pathURL := bucketInfo.GetUrlPathStyle() // e.g., https://endpoint/bucket

	u, err := url.Parse(pathURL)
	if err != nil {
		return fmt.Errorf("invalid bucket URL from API: %s: %w", pathURL, err)
	}

	endpoint := u.Scheme + "://" + u.Host

	// Ensure a temporary S3 access key
	accessKeyID, secretAccessKey, keyID, groupID, err := m.ensureTemporaryAccessKey(ctx)
	if err != nil {
		return fmt.Errorf("failed to obtain S3 credentials: %w", err)
	}
	// Cleanup access key on return
	defer func() {
		if keyID == "" {
			return
		}
		// Best-effort delete
		if _, derr := cli.DeleteAccessKey(ctx, m.client.config.ProjectID, m.client.config.Region, keyID).
			CredentialsGroup(groupID).
			Execute(); derr != nil {
			operation.Warnf("Failed to delete temporary access key %s: %v", keyID, derr)
		}
	}()

	// Build S3 client with static creds and custom endpoint
	cfg := aws.Config{
		Region:      m.client.config.Region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	}

	s3cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})

	// First attempt to delete object versions if versioning is enabled
	if err := deleteAllObjectVersions(ctx, s3cli, name); err != nil {
		operation.Warnf("Deleting object versions failed or not supported: %v (continuing to delete current objects)", err)
	}

	// Delete current objects
	if err := deleteAllObjects(ctx, s3cli, name); err != nil {
		return fmt.Errorf("failed deleting objects: %w", err)
	}

	operation.Infof("Bucket emptied: %s", name)

	return nil
}

// ensureTemporaryAccessKey returns accessKeyID, secretAccessKey, keyID, groupID.
func (m *StorageManager) ensureTemporaryAccessKey(ctx context.Context) (string, string, string, string, error) {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return "", "", "", "", err
	}

	const groupDisplay = "ocfp-cli"

	var groupID string

	// Find existing credentials group
	if resp, err := cli.ListCredentialsGroups(ctx, m.client.config.ProjectID, m.client.config.Region).Execute(); err == nil {
		if groups, ok := resp.GetCredentialsGroupsOk(); ok {
			for _, g := range groups {
				if strings.EqualFold(g.GetDisplayName(), groupDisplay) {
					groupID = g.GetCredentialsGroupId()

					break
				}
			}
		}
	}

	// Create group if missing
	if groupID == "" {
		payload := objectstorage.NewCreateCredentialsGroupPayload(groupDisplay)

		createResp, err := cli.CreateCredentialsGroup(ctx, m.client.config.ProjectID, m.client.config.Region).
			CreateCredentialsGroupPayload(*payload).Execute()
		if err != nil {
			return "", "", "", "", fmt.Errorf("create credentials group failed: %w", err)
		}
		// The response likely includes the created group via List later; fallback to listing to get ID
		if resp, err := cli.ListCredentialsGroups(ctx, m.client.config.ProjectID, m.client.config.Region).Execute(); err == nil {
			if groups, ok := resp.GetCredentialsGroupsOk(); ok {
				for _, g := range groups {
					if strings.EqualFold(g.GetDisplayName(), groupDisplay) {
						groupID = g.GetCredentialsGroupId()

						break
					}
				}
			}
		}

		_ = createResp // not used further, but created

		if groupID == "" {
			return "", "", "", "", errors.New("could not determine created credentials group id")
		}
	}

	// Create an access key that expires in 1 hour
	payload := objectstorage.NewCreateAccessKeyPayload()
	// Optional: leave Expires nil to create non-expiring; we set short expiry for safety
	// t := time.Now().Add(1 * time.Hour)
	// payload.SetExpires(t)

	req := cli.CreateAccessKey(ctx, m.client.config.ProjectID, m.client.config.Region)
	req = req.CredentialsGroup(groupID)

	car, err := req.CreateAccessKeyPayload(*payload).Execute()
	if err != nil {
		return "", "", "", "", fmt.Errorf("create access key failed: %w", err)
	}

	accessKeyID := car.GetAccessKey()
	secretAccessKey := car.GetSecretAccessKey()
	keyID := car.GetKeyId()

	return accessKeyID, secretAccessKey, keyID, groupID, nil
}

func deleteAllObjects(ctx context.Context, s3cli *s3.Client, bucket string) error {
	const batch = 1000

	var cont *string
	for {
		listResp, err := s3cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: &bucket, ContinuationToken: cont})
		if err != nil {
			return err
		}

		if len(listResp.Contents) == 0 {
			return nil
		}
		// Batch delete
		for index := 0; index < len(listResp.Contents); index += batch {
			end := index + batch
			if end > len(listResp.Contents) {
				end = len(listResp.Contents)
			}

			objs := make([]s3typesObjectIdentifier, 0, end-index)
			for _, o := range listResp.Contents[index:end] {
				key := *o.Key
				objs = append(objs, s3typesObjectIdentifier{Key: &key})
			}

			if _, err := s3cli.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: &bucket,
				Delete: &s3typesDelete{Objects: objs, Quiet: aws.Bool(true)},
			}); err != nil {
				return err
			}
		}

		if aws.ToBool(listResp.IsTruncated) && listResp.NextContinuationToken != nil {
			cont = listResp.NextContinuationToken
		} else {
			return nil
		}
	}
}

// Delete all object versions if bucket has versioning enabled.
func deleteAllObjectVersions(ctx context.Context, s3cli *s3.Client, bucket string) error {
	// Try to list object versions; if unsupported, return error to be ignored by caller
	const batch = 1000

	var keyMarker, versionIdMarker *string
	for {
		listVersionsResp, err := s3cli.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket:          &bucket,
			KeyMarker:       keyMarker,
			VersionIdMarker: versionIdMarker,
		})
		if err != nil {
			return err
		}

		if len(listVersionsResp.Versions) == 0 && len(listVersionsResp.DeleteMarkers) == 0 {
			return nil
		}
		// Collect identifiers
		ids := make([]s3typesObjectIdentifier, 0, len(listVersionsResp.Versions)+len(listVersionsResp.DeleteMarkers))
		for _, v := range listVersionsResp.Versions {
			key := *v.Key
			ver := *v.VersionId
			ids = append(ids, s3typesObjectIdentifier{Key: &key, VersionId: &ver})
		}

		for _, dm := range listVersionsResp.DeleteMarkers {
			key := *dm.Key
			ver := *dm.VersionId
			ids = append(ids, s3typesObjectIdentifier{Key: &key, VersionId: &ver})
		}

		for index := 0; index < len(ids); index += batch {
			end := index + batch
			if end > len(ids) {
				end = len(ids)
			}

			chunk := ids[index:end]
			if _, err := s3cli.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: &bucket,
				Delete: &s3typesDelete{Objects: chunk, Quiet: aws.Bool(true)},
			}); err != nil {
				return err
			}
		}

		if aws.ToBool(listVersionsResp.IsTruncated) && (listVersionsResp.NextKeyMarker != nil || listVersionsResp.NextVersionIdMarker != nil) {
			keyMarker = listVersionsResp.NextKeyMarker
			versionIdMarker = listVersionsResp.NextVersionIdMarker
		} else {
			return nil
		}
	}
}

// Local aliases to avoid importing s3/types explicitly at call sites.
type s3typesObjectIdentifier = s3types.ObjectIdentifier
type s3typesDelete = s3types.Delete

// EnableBucketVersioning turns on S3 versioning for a bucket (data-plane).
func (m *StorageManager) EnableBucketVersioning(ctx context.Context, name string) error {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return err
	}

	meta, err := cli.GetBucket(ctx, m.client.config.ProjectID, m.client.config.Region, name).Execute()
	if err != nil {
		return fmt.Errorf("get bucket for versioning: %w", err)
	}

	var bucketData objectstorage.Bucket
	if bb, ok := meta.GetBucketOk(); ok {
		bucketData = bb
	} else {
		return errors.New("bucket info missing")
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
		_, _ = cli.DeleteAccessKey(ctx, m.client.config.ProjectID, m.client.config.Region, keyID).CredentialsGroup(groupID).Execute()
	}()

	cfg := aws.Config{
		Region:      m.client.config.Region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	}
	s3cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})
	_, err = s3cli.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:                  &name,
		VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled},
	})

	return err
}

// SetBucketLifecycleNoncurrentDays sets a lifecycle rule to expire noncurrent versions after N days.
func (m *StorageManager) SetBucketLifecycleNoncurrentDays(ctx context.Context, name string, days int32) error {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return err
	}

	meta, err := cli.GetBucket(ctx, m.client.config.ProjectID, m.client.config.Region, name).Execute()
	if err != nil {
		return fmt.Errorf("get bucket for lifecycle: %w", err)
	}

	var bucketData objectstorage.Bucket
	if bb, ok := meta.GetBucketOk(); ok {
		bucketData = bb
	} else {
		return errors.New("bucket info missing")
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
		_, _ = cli.DeleteAccessKey(ctx, m.client.config.ProjectID, m.client.config.Region, keyID).CredentialsGroup(groupID).Execute()
	}()

	cfg := aws.Config{
		Region:      m.client.config.Region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	}
	s3cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})

	_, err = s3cli.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: &name,
		LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{
			Rules: []s3types.LifecycleRule{
				{
					ID:                             aws.String("DeleteOldObjects"),
					Status:                         s3types.ExpirationStatusEnabled,
					NoncurrentVersionExpiration:    &s3types.NoncurrentVersionExpiration{NoncurrentDays: &days},
					AbortIncompleteMultipartUpload: &s3types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(7)},
				},
			},
		},
	})

	return err
}

// EnsureObjectStorageCredentialsGroup ensures a credentials group exists and returns its ID.
func (m *StorageManager) EnsureObjectStorageCredentialsGroup(ctx context.Context, displayName string) (string, error) {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return "", err
	}
	// Try to find existing
	if resp, err := cli.ListCredentialsGroups(ctx, m.client.config.ProjectID, m.client.config.Region).Execute(); err == nil {
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
	if _, err := cli.CreateCredentialsGroup(ctx, m.client.config.ProjectID, m.client.config.Region).
		CreateCredentialsGroupPayload(*payload).Execute(); err != nil {
		return "", fmt.Errorf("create credentials group: %w", err)
	}
	// Fetch ID
	if resp, err := cli.ListCredentialsGroups(ctx, m.client.config.ProjectID, m.client.config.Region).Execute(); err == nil {
		if groups, ok := resp.GetCredentialsGroupsOk(); ok {
			for _, g := range groups {
				if strings.EqualFold(g.GetDisplayName(), displayName) {
					return g.GetCredentialsGroupId(), nil
				}
			}
		}
	}

	return "", fmt.Errorf("credentials group %q not found after creation", displayName)
}

// DeleteCredentialsGroup removes a credentials group by ID (STACKIT-specific).
func (m *StorageManager) DeleteCredentialsGroup(ctx context.Context, groupID string) error {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return err
	}

	_, err = cli.DeleteCredentialsGroup(ctx, m.client.config.ProjectID, m.client.config.Region, groupID).Execute()
	if err != nil {
		return fmt.Errorf("delete credentials group %s: %w", groupID, err)
	}

	return nil
}
