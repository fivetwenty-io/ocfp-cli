package stackit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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

// CreateBucket creates a new bucket in object storage
func (m *StorageManager) CreateBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	logger.WithOperation("CreateBucket").Infof("Creating bucket: %s", name)

	apiReq := map[string]interface{}{
		"name":   name,
		"region": m.client.config.Region,
	}

	httpReq, err := m.client.newRequest(ctx, "POST", "/v1/projects/"+m.client.config.ProjectID+"/object-storage/buckets", apiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create bucket: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	bucket := &cpi.Bucket{
		Name:   name,
		Region: m.client.config.Region,
	}

	logger.WithOperation("CreateBucket").Infof("Bucket created: %s", name)
	return bucket, nil
}

// GetBucket retrieves a bucket by name
func (m *StorageManager) GetBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	logger.WithOperation("GetBucket").Debugf("Getting bucket: %s", name)

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/object-storage/buckets/"+name, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &cpi.ProviderError{
			Provider: "stackit",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Bucket %s not found", name),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var bucket cpi.Bucket
	if err := json.NewDecoder(resp.Body).Decode(&bucket); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &bucket, nil
}

// ListBuckets lists all buckets
func (m *StorageManager) ListBuckets(ctx context.Context) ([]*cpi.Bucket, error) {
	logger.WithOperation("ListBuckets").Debug("Listing buckets")

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/object-storage/buckets", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to list buckets: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var result struct {
		Buckets []*cpi.Bucket `json:"buckets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.Buckets, nil
}

// DeleteBucket deletes a bucket from object storage
func (m *StorageManager) DeleteBucket(ctx context.Context, name string) error {
	logger.WithOperation("DeleteBucket").Infof("Deleting bucket: %s", name)

	httpReq, err := m.client.newRequest(ctx, "DELETE", "/v1/projects/"+m.client.config.ProjectID+"/object-storage/buckets/"+name, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to delete bucket: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return m.client.parseError(resp)
	}

	logger.WithOperation("DeleteBucket").Infof("Bucket deleted: %s", name)
	return nil
}

// EmptyBucket removes all objects from a bucket
func (m *StorageManager) EmptyBucket(ctx context.Context, name string) error {
	logger.WithOperation("EmptyBucket").Infof("Emptying bucket: %s", name)

	// TODO: Implement listing and deleting all objects in the bucket
	return fmt.Errorf("not implemented")
}
