package aws

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// nilStorageManager returns a StorageManager with a nil client.
// Safe for converter methods that don't touch the client.
func nilStorageManager() *StorageManager {
	return &StorageManager{client: nil}
}

// ---- mapVolumeState ---------------------------------------------------------

func TestMapVolumeState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input types.VolumeState
		want  cpi.ResourceState
	}{
		{"creating", types.VolumeStateCreating, cpi.ResourceStateCreating},
		{"available", types.VolumeStateAvailable, cpi.ResourceStateAvailable},
		{"in-use", types.VolumeStateInUse, cpi.ResourceStateInUse},
		{"deleting", types.VolumeStateDeleting, cpi.ResourceStateDeleting},
		{"deleted", types.VolumeStateDeleted, cpi.ResourceStateDeleted},
		{"error", types.VolumeStateError, cpi.ResourceStateError},
		{"unknown enum", types.VolumeState("bogus"), cpi.ResourceStateUnknown},
	}

	m := nilStorageManager()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := m.mapVolumeState(tt.input)
			if got != tt.want {
				t.Errorf("mapVolumeState(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---- mapSnapshotState -------------------------------------------------------

func TestMapSnapshotState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input types.SnapshotState
		want  cpi.ResourceState
	}{
		{"pending", types.SnapshotStatePending, cpi.ResourceStateCreating},
		{"completed", types.SnapshotStateCompleted, cpi.ResourceStateAvailable},
		{"error", types.SnapshotStateError, cpi.ResourceStateError},
		{"recoverable", types.SnapshotStateRecoverable, cpi.ResourceStateCreating},
		{"recovering", types.SnapshotStateRecovering, cpi.ResourceStateCreating},
		{"unknown enum", types.SnapshotState("bogus"), cpi.ResourceStateUnknown},
	}

	m := nilStorageManager()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := m.mapSnapshotState(tt.input)
			if got != tt.want {
				t.Errorf("mapSnapshotState(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---- mapSnapshot ------------------------------------------------------------

func TestMapSnapshot_NilInput(t *testing.T) {
	t.Parallel()

	m := nilStorageManager()
	got := m.mapSnapshot(nil)
	if got != nil {
		t.Errorf("mapSnapshot(nil) = %+v, want nil", got)
	}
}

func TestMapSnapshot_MinimalFields(t *testing.T) {
	t.Parallel()

	m := nilStorageManager()
	snap := &types.Snapshot{
		SnapshotId: aws.String("snap-min"),
		VolumeId:   aws.String("vol-xyz"),
		VolumeSize: aws.Int32(50),
		State:      types.SnapshotStateCompleted,
	}

	got := m.mapSnapshot(snap)

	if got == nil {
		t.Fatal("mapSnapshot returned nil for non-nil input")
	}
	if got.ID != "snap-min" {
		t.Errorf("ID = %q, want %q", got.ID, "snap-min")
	}
	if got.VolumeID != "vol-xyz" {
		t.Errorf("VolumeID = %q, want %q", got.VolumeID, "vol-xyz")
	}
	if got.Size != 50 {
		t.Errorf("Size = %d, want 50", got.Size)
	}
	if got.State != cpi.ResourceStateAvailable {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateAvailable)
	}
	if got.Description != "" {
		t.Errorf("Description = %q, want empty", got.Description)
	}
	if got.Name != "" {
		t.Errorf("Name = %q, want empty (no Name tag)", got.Name)
	}
	if got.Tags == nil {
		t.Errorf("Tags must be non-nil map")
	}
}

func TestMapSnapshot_FullFields(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	m := nilStorageManager()
	snap := &types.Snapshot{
		SnapshotId:  aws.String("snap-full"),
		VolumeId:    aws.String("vol-full"),
		VolumeSize:  aws.Int32(100),
		State:       types.SnapshotStatePending,
		Description: aws.String("my backup"),
		StartTime:   &startTime,
		Tags: []types.Tag{
			{Key: aws.String("Name"), Value: aws.String("daily-backup")},
			{Key: aws.String("env"), Value: aws.String("prod")},
		},
	}

	got := m.mapSnapshot(snap)

	if got.Description != "my backup" {
		t.Errorf("Description = %q, want %q", got.Description, "my backup")
	}
	if got.Name != "daily-backup" {
		t.Errorf("Name = %q, want %q", got.Name, "daily-backup")
	}
	if got.State != cpi.ResourceStateCreating {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateCreating)
	}
	if got.CreatedAt != startTime {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, startTime)
	}
	if got.Tags["env"] != "prod" {
		t.Errorf("Tags[env] = %q, want %q", got.Tags["env"], "prod")
	}
	if got.Tags["Name"] != "daily-backup" {
		t.Errorf("Tags[Name] = %q, want %q", got.Tags["Name"], "daily-backup")
	}
}

func TestMapSnapshot_NilPointerFields(t *testing.T) {
	t.Parallel()

	m := nilStorageManager()
	snap := &types.Snapshot{
		SnapshotId: nil,
		VolumeId:   nil,
		VolumeSize: nil,
		State:      types.SnapshotStateError,
		StartTime:  nil,
	}

	got := m.mapSnapshot(snap)

	if got == nil {
		t.Fatal("mapSnapshot returned nil for non-nil input with nil fields")
	}
	if got.ID != "" {
		t.Errorf("ID = %q, want empty (nil SnapshotId)", got.ID)
	}
	if got.VolumeID != "" {
		t.Errorf("VolumeID = %q, want empty (nil VolumeId)", got.VolumeID)
	}
	if got.Size != 0 {
		t.Errorf("Size = %d, want 0 (nil VolumeSize)", got.Size)
	}
	if got.State != cpi.ResourceStateError {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateError)
	}
	zeroTime := time.Time{}
	if got.CreatedAt != zeroTime {
		t.Errorf("CreatedAt = %v, want zero time (nil StartTime)", got.CreatedAt)
	}
}

// ---- mapVolume --------------------------------------------------------------

func TestMapVolume_NilInput(t *testing.T) {
	t.Parallel()

	m := nilStorageManager()
	got := m.mapVolume(nil)
	if got != nil {
		t.Errorf("mapVolume(nil) = %+v, want nil", got)
	}
}

func TestMapVolume_MinimalFields(t *testing.T) {
	t.Parallel()

	m := nilStorageManager()
	size := int32(50)
	vol := &types.Volume{
		VolumeId:   aws.String("vol-min"),
		Size:       &size,
		VolumeType: types.VolumeTypeGp2,
		State:      types.VolumeStateAvailable,
		Encrypted:  aws.Bool(false),
	}

	got := m.mapVolume(vol)

	if got == nil {
		t.Fatal("mapVolume returned nil for non-nil input")
	}
	if got.ID != "vol-min" {
		t.Errorf("ID = %q, want vol-min", got.ID)
	}
	if got.Size != 50 {
		t.Errorf("Size = %d, want 50", got.Size)
	}
	if got.Type != string(types.VolumeTypeGp2) {
		t.Errorf("Type = %q, want %q", got.Type, string(types.VolumeTypeGp2))
	}
	if got.State != cpi.ResourceStateAvailable {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateAvailable)
	}
	if got.Encrypted {
		t.Errorf("Encrypted = true, want false")
	}
	if got.AttachedTo != "" {
		t.Errorf("AttachedTo = %q, want empty (no attachments)", got.AttachedTo)
	}
	if got.Tags == nil {
		t.Errorf("Tags must be non-nil map")
	}
}

func TestMapVolume_FullFields(t *testing.T) {
	t.Parallel()

	size := int32(100)
	az := "us-east-1a"
	instanceID := "i-attached"
	device := "/dev/xvdf"
	m := nilStorageManager()
	vol := &types.Volume{
		VolumeId:         aws.String("vol-full"),
		Size:             &size,
		VolumeType:       types.VolumeTypeIo1,
		State:            types.VolumeStateInUse,
		Encrypted:        aws.Bool(true),
		AvailabilityZone: &az,
		Tags: []types.Tag{
			{Key: aws.String("Name"), Value: aws.String("data-vol")},
			{Key: aws.String("env"), Value: aws.String("prod")},
		},
		Attachments: []types.VolumeAttachment{
			{InstanceId: &instanceID, Device: &device},
		},
	}

	got := m.mapVolume(vol)

	if got.Name != "data-vol" {
		t.Errorf("Name = %q, want data-vol", got.Name)
	}
	if got.State != cpi.ResourceStateInUse {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateInUse)
	}
	if !got.Encrypted {
		t.Errorf("Encrypted = false, want true")
	}
	if got.AttachedTo != "i-attached" {
		t.Errorf("AttachedTo = %q, want i-attached", got.AttachedTo)
	}
	if got.Device != "/dev/xvdf" {
		t.Errorf("Device = %q, want /dev/xvdf", got.Device)
	}
	if got.Tags["availability-zone"] != "us-east-1a" {
		t.Errorf("Tags[availability-zone] = %q, want us-east-1a", got.Tags["availability-zone"])
	}
	if got.Tags["env"] != "prod" {
		t.Errorf("Tags[env] = %q, want prod", got.Tags["env"])
	}
}

func TestMapVolume_NilAvailabilityZone(t *testing.T) {
	t.Parallel()

	size := int32(10)
	m := nilStorageManager()
	vol := &types.Volume{
		VolumeId:         aws.String("vol-noaz"),
		Size:             &size,
		VolumeType:       types.VolumeTypeGp3,
		State:            types.VolumeStateAvailable,
		AvailabilityZone: nil,
	}

	got := m.mapVolume(vol)

	if _, exists := got.Tags["availability-zone"]; exists {
		t.Errorf("availability-zone tag must not be set when AvailabilityZone is nil")
	}
}

func TestMapSnapshot_EmptyTagList(t *testing.T) {
	t.Parallel()

	m := nilStorageManager()
	snap := &types.Snapshot{
		SnapshotId: aws.String("snap-notags"),
		VolumeId:   aws.String("vol-notags"),
		VolumeSize: aws.Int32(10),
		State:      types.SnapshotStateCompleted,
		Tags:       []types.Tag{},
	}

	got := m.mapSnapshot(snap)

	if got.Name != "" {
		t.Errorf("Name = %q, want empty (empty tag list)", got.Name)
	}
	if len(got.Tags) != 0 {
		t.Errorf("Tags len = %d, want 0", len(got.Tags))
	}
}
