package stackit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	objectstorage "github.com/stackitcloud/stackit-sdk-go/services/objectstorage"
	"net/url"
	"strings"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	s3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// CreateVolume creates a new storage volume
func (m *StorageManager) CreateVolume(ctx context.Context, req *cpi.CreateVolumeRequest) (*cpi.Volume, error) {
	logger.WithOperation("CreateVolume").Infof("Creating volume: %s", req.Name)

	apiReq := map[string]interface{}{
		"name":              req.Name,
		"size":              req.Size,
		"type":              req.Type,
		"availability_zone": req.AvailabilityZone,
		"labels":            req.Tags,
	}

	if req.SnapshotID != "" {
		apiReq["snapshot_id"] = req.SnapshotID
	}

	httpReq, err := m.client.newRequest(ctx, "POST", "/v1/projects/"+m.client.config.ProjectID+"/volumes", apiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create volume: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return nil, m.client.parseError(resp)
	}

	var volume cpi.Volume
	if err := json.NewDecoder(resp.Body).Decode(&volume); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if err := m.waitForVolumeState(ctx, volume.ID, cpi.ResourceStateAvailable, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("volume failed to become available: %w", err)
	}

	logger.WithOperation("CreateVolume").Infof("Volume created: %s (%s)", volume.Name, volume.ID)
	return &volume, nil
}

// GetVolume retrieves a volume by ID
func (m *StorageManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) {
	logger.WithOperation("GetVolume").Debugf("Getting volume: %s", id)

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/volumes/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get volume: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &cpi.ProviderError{
			Provider: "stackit",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Volume %s not found", id),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var volume cpi.Volume
	if err := json.NewDecoder(resp.Body).Decode(&volume); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &volume, nil
}

// ListVolumes lists all volumes
func (m *StorageManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	logger.WithOperation("ListVolumes").Debug("Listing volumes")

	query := "?"
	for k, v := range filters {
		query += fmt.Sprintf("%s=%s&", k, v)
	}

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/volumes"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var result struct {
		Volumes []*cpi.Volume `json:"volumes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.WithOperation("ListVolumes").Debugf("Found %d volumes", len(result.Volumes))
	return result.Volumes, nil
}

// DeleteVolume deletes a volume
func (m *StorageManager) DeleteVolume(ctx context.Context, id string) error {
	logger.WithOperation("DeleteVolume").Infof("Deleting volume: %s", id)

	httpReq, err := m.client.newRequest(ctx, "DELETE", "/v1/projects/"+m.client.config.ProjectID+"/volumes/"+id, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to delete volume: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return m.client.parseError(resp)
	}

	logger.WithOperation("DeleteVolume").Infof("Volume deleted: %s", id)
	return nil
}

// AttachVolume attaches a volume to an instance
func (m *StorageManager) AttachVolume(ctx context.Context, volumeID, instanceID, device string) error {
	logger.WithOperation("AttachVolume").Infof("Attaching volume %s to instance %s", volumeID, instanceID)

	apiReq := map[string]interface{}{
		"instance_id": instanceID,
	}

	httpReq, err := m.client.newRequest(ctx, "POST", fmt.Sprintf("/v1/projects/%s/volumes/%s/attach", m.client.config.ProjectID, volumeID), apiReq)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to attach volume: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return m.client.parseError(resp)
	}

	if err := m.waitForVolumeState(ctx, volumeID, cpi.ResourceStateInUse, 2*time.Minute); err != nil {
		return fmt.Errorf("volume failed to attach: %w", err)
	}

	logger.WithOperation("AttachVolume").Infof("Volume attached successfully")
	return nil
}

// DetachVolume detaches a volume from an instance
func (m *StorageManager) DetachVolume(ctx context.Context, volumeID string) error {
	logger.WithOperation("DetachVolume").Infof("Detaching volume: %s", volumeID)

	httpReq, err := m.client.newRequest(ctx, "POST", fmt.Sprintf("/v1/projects/%s/volumes/%s/detach", m.client.config.ProjectID, volumeID), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to detach volume: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return m.client.parseError(resp)
	}

	if err := m.waitForVolumeState(ctx, volumeID, cpi.ResourceStateAvailable, 2*time.Minute); err != nil {
		return fmt.Errorf("volume failed to detach: %w", err)
	}

	logger.WithOperation("DetachVolume").Infof("Volume detached successfully")
	return nil
}

// ResizeVolume resizes a volume
func (m *StorageManager) ResizeVolume(ctx context.Context, volumeID string, newSize int) error {
	logger.WithOperation("ResizeVolume").Infof("Resizing volume %s to %d GB", volumeID, newSize)

	apiReq := map[string]interface{}{
		"size": newSize,
	}

	httpReq, err := m.client.newRequest(ctx, "POST", fmt.Sprintf("/v1/projects/%s/volumes/%s/resize", m.client.config.ProjectID, volumeID), apiReq)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to resize volume: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return m.client.parseError(resp)
	}

	logger.WithOperation("ResizeVolume").Infof("Volume resize initiated")
	return nil
}

// CreateSnapshot creates a snapshot of a volume
func (m *StorageManager) CreateSnapshot(ctx context.Context, volumeID string, name string) (*cpi.Snapshot, error) {
	logger.WithOperation("CreateSnapshot").Infof("Creating snapshot: %s", name)

	apiReq := map[string]interface{}{
		"name":      name,
		"volume_id": volumeID,
	}

	httpReq, err := m.client.newRequest(ctx, "POST", "/v1/projects/"+m.client.config.ProjectID+"/snapshots", apiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return nil, m.client.parseError(resp)
	}

	var snapshot cpi.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if err := m.waitForSnapshotState(ctx, snapshot.ID, cpi.ResourceStateAvailable, 10*time.Minute); err != nil {
		return nil, fmt.Errorf("snapshot failed to become available: %w", err)
	}

	logger.WithOperation("CreateSnapshot").Infof("Snapshot created: %s (%s)", snapshot.Name, snapshot.ID)
	return &snapshot, nil
}

// GetSnapshot retrieves a snapshot by ID
func (m *StorageManager) GetSnapshot(ctx context.Context, id string) (*cpi.Snapshot, error) {
	logger.WithOperation("GetSnapshot").Debugf("Getting snapshot: %s", id)

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/snapshots/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get snapshot: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &cpi.ProviderError{
			Provider: "stackit",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Snapshot %s not found", id),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var snapshot cpi.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &snapshot, nil
}

// ListSnapshots lists all snapshots for a volume
func (m *StorageManager) ListSnapshots(ctx context.Context, volumeID string) ([]*cpi.Snapshot, error) {
	logger.WithOperation("ListSnapshots").Debug("Listing snapshots")

	query := "?"
	if volumeID != "" {
		query += fmt.Sprintf("volume_id=%s&", volumeID)
	}

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/snapshots"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var result struct {
		Snapshots []*cpi.Snapshot `json:"snapshots"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.WithOperation("ListSnapshots").Debugf("Found %d snapshots", len(result.Snapshots))
	return result.Snapshots, nil
}

// DeleteSnapshot deletes a snapshot
func (m *StorageManager) DeleteSnapshot(ctx context.Context, id string) error {
	logger.WithOperation("DeleteSnapshot").Infof("Deleting snapshot: %s", id)

	httpReq, err := m.client.newRequest(ctx, "DELETE", "/v1/projects/"+m.client.config.ProjectID+"/snapshots/"+id, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return m.client.parseError(resp)
	}

	logger.WithOperation("DeleteSnapshot").Infof("Snapshot deleted: %s", id)
	return nil
}

// waitForVolumeState waits for a volume to reach a specific state
func (m *StorageManager) waitForVolumeState(ctx context.Context, id string, targetState cpi.ResourceState, timeout time.Duration) error {
	return cpi.WaitForCondition(ctx, 5*time.Second, timeout, func() (bool, error) {
		volume, err := m.GetVolume(ctx, id)
		if err != nil {
			return false, err
		}
		return volume.State == targetState, nil
	})
}

// waitForSnapshotState waits for a snapshot to reach a specific state
func (m *StorageManager) waitForSnapshotState(ctx context.Context, id string, targetState cpi.ResourceState, timeout time.Duration) error {
	return cpi.WaitForCondition(ctx, 5*time.Second, timeout, func() (bool, error) {
		snapshot, err := m.GetSnapshot(ctx, id)
		if err != nil {
			return false, err
		}
		return snapshot.State == targetState, nil
	})
}

// CreateBucket creates a new bucket in object storage using the official SDK
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
	for i := 0; i < len(name); i++ {
		if name[i] == '-' {
			if i > 0 {
				return name[:i]
			}
			return ""
		}
	}
	return ""
}

// GetBucket retrieves a bucket by name using the SDK
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
	var b objectstorage.Bucket
	if bb, ok := resp.GetBucketOk(); ok {
		b = bb
	}
	bucket := &cpi.Bucket{
		Name:   b.GetName(),
		Region: b.GetRegion(),
	}
	return bucket, nil
}

// ListBuckets lists all buckets using the SDK
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

// DeleteBucket deletes a bucket using the SDK
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

// EmptyBucket removes all objects from a bucket
func (m *StorageManager) EmptyBucket(ctx context.Context, name string) error {
	op := logger.WithOperation("EmptyBucket")
	op.Infof("Emptying bucket: %s", name)

	// Discover bucket endpoint URLs
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return err
	}
	gb, err := cli.GetBucket(ctx, m.client.config.ProjectID, m.client.config.Region, name).Execute()
	if err != nil {
		return fmt.Errorf("failed to get bucket %s metadata: %w", name, err)
	}

	// Choose path-style URL for S3 client endpoint
	var bucketInfo objectstorage.Bucket
	if b, ok := gb.GetBucketOk(); ok {
		bucketInfo = b
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
			op.Warnf("Failed to delete temporary access key %s: %v", keyID, derr)
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
		op.Warnf("Deleting object versions failed or not supported: %v (continuing to delete current objects)", err)
	}

	// Delete current objects
	if err := deleteAllObjects(ctx, s3cli, name); err != nil {
		return fmt.Errorf("failed deleting objects: %w", err)
	}

	op.Infof("Bucket emptied: %s", name)
	return nil
}

// ensureTemporaryAccessKey returns accessKeyID, secretAccessKey, keyID, groupID
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
		cr, err := cli.CreateCredentialsGroup(ctx, m.client.config.ProjectID, m.client.config.Region).
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
		_ = cr // not used further, but created
		if groupID == "" {
			return "", "", "", "", fmt.Errorf("could not determine created credentials group id")
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
		lo, err := s3cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: &bucket, ContinuationToken: cont})
		if err != nil {
			return err
		}
		if len(lo.Contents) == 0 {
			return nil
		}
		// Batch delete
		for i := 0; i < len(lo.Contents); i += batch {
			end := i + batch
			if end > len(lo.Contents) {
				end = len(lo.Contents)
			}
			objs := make([]s3typesObjectIdentifier, 0, end-i)
			for _, o := range lo.Contents[i:end] {
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
		if aws.ToBool(lo.IsTruncated) && lo.NextContinuationToken != nil {
			cont = lo.NextContinuationToken
		} else {
			return nil
		}
	}
}

// Delete all object versions if bucket has versioning enabled
func deleteAllObjectVersions(ctx context.Context, s3cli *s3.Client, bucket string) error {
	// Try to list object versions; if unsupported, return error to be ignored by caller
	const batch = 1000
	var keyMarker, versionIdMarker *string
	for {
		lv, err := s3cli.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket:          &bucket,
			KeyMarker:       keyMarker,
			VersionIdMarker: versionIdMarker,
		})
		if err != nil {
			return err
		}
		if len(lv.Versions) == 0 && len(lv.DeleteMarkers) == 0 {
			return nil
		}
		// Collect identifiers
		ids := make([]s3typesObjectIdentifier, 0, len(lv.Versions)+len(lv.DeleteMarkers))
		for _, v := range lv.Versions {
			key := *v.Key
			ver := *v.VersionId
			ids = append(ids, s3typesObjectIdentifier{Key: &key, VersionId: &ver})
		}
		for _, dm := range lv.DeleteMarkers {
			key := *dm.Key
			ver := *dm.VersionId
			ids = append(ids, s3typesObjectIdentifier{Key: &key, VersionId: &ver})
		}
		for i := 0; i < len(ids); i += batch {
			end := i + batch
			if end > len(ids) {
				end = len(ids)
			}
			chunk := ids[i:end]
			if _, err := s3cli.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: &bucket,
				Delete: &s3typesDelete{Objects: chunk, Quiet: aws.Bool(true)},
			}); err != nil {
				return err
			}
		}
		if aws.ToBool(lv.IsTruncated) && (lv.NextKeyMarker != nil || lv.NextVersionIdMarker != nil) {
			keyMarker = lv.NextKeyMarker
			versionIdMarker = lv.NextVersionIdMarker
		} else {
			return nil
		}
	}
}

// Local aliases to avoid importing s3/types explicitly at call sites
type s3typesObjectIdentifier = s3types.ObjectIdentifier
type s3typesDelete = s3types.Delete

// EnableBucketVersioning turns on S3 versioning for a bucket (data-plane)
func (m *StorageManager) EnableBucketVersioning(ctx context.Context, name string) error {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return err
	}
	meta, err := cli.GetBucket(ctx, m.client.config.ProjectID, m.client.config.Region, name).Execute()
	if err != nil {
		return fmt.Errorf("get bucket for versioning: %w", err)
	}
	var b objectstorage.Bucket
	if bb, ok := meta.GetBucketOk(); ok {
		b = bb
	} else {
		return fmt.Errorf("bucket info missing")
	}
	u, err := url.Parse(b.GetUrlPathStyle())
	if err != nil {
		return fmt.Errorf("parse bucket path url: %w", err)
	}
	endpoint := u.Scheme + "://" + u.Host

	ak, sk, keyID, groupID, err := m.ensureTemporaryAccessKey(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = cli.DeleteAccessKey(ctx, m.client.config.ProjectID, m.client.config.Region, keyID).CredentialsGroup(groupID).Execute()
	}()

	cfg := aws.Config{
		Region:      m.client.config.Region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(ak, sk, "")),
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

// SetBucketLifecycleNoncurrentDays sets a lifecycle rule to expire noncurrent versions after N days
func (m *StorageManager) SetBucketLifecycleNoncurrentDays(ctx context.Context, name string, days int32) error {
	cli, err := m.client.getObjectStorageClient()
	if err != nil {
		return err
	}
	meta, err := cli.GetBucket(ctx, m.client.config.ProjectID, m.client.config.Region, name).Execute()
	if err != nil {
		return fmt.Errorf("get bucket for lifecycle: %w", err)
	}
	var b objectstorage.Bucket
	if bb, ok := meta.GetBucketOk(); ok {
		b = bb
	} else {
		return fmt.Errorf("bucket info missing")
	}
	u, err := url.Parse(b.GetUrlPathStyle())
	if err != nil {
		return fmt.Errorf("parse bucket path url: %w", err)
	}
	endpoint := u.Scheme + "://" + u.Host

	ak, sk, keyID, groupID, err := m.ensureTemporaryAccessKey(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = cli.DeleteAccessKey(ctx, m.client.config.ProjectID, m.client.config.Region, keyID).CredentialsGroup(groupID).Execute()
	}()

	cfg := aws.Config{
		Region:      m.client.config.Region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(ak, sk, "")),
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

// EnsureObjectStorageCredentialsGroup ensures a credentials group exists and returns its ID
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

// DeleteCredentialsGroup removes a credentials group by ID (STACKIT-specific)
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
