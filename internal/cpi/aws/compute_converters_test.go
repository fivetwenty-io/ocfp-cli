package aws

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// nilComputeManager returns a ComputeManager with a nil client.
// Safe for converter methods that don't touch the client.
func nilComputeManager() *ComputeManager {
	return &ComputeManager{client: nil}
}

// ---- ec2StateToResourceState ------------------------------------------------

func TestEc2StateToResourceState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state *types.InstanceState
		want  cpi.ResourceState
	}{
		{
			name:  "nil state",
			state: nil,
			want:  cpi.ResourceStateUnknown,
		},
		{
			name:  "pending",
			state: &types.InstanceState{Name: types.InstanceStateNamePending},
			want:  cpi.ResourceStateCreating,
		},
		{
			name:  "running",
			state: &types.InstanceState{Name: types.InstanceStateNameRunning},
			want:  cpi.ResourceStateActive,
		},
		{
			name:  "stopping",
			state: &types.InstanceState{Name: types.InstanceStateNameStopping},
			want:  cpi.ResourceStateDeleting,
		},
		{
			name:  "stopped",
			state: &types.InstanceState{Name: types.InstanceStateNameStopped},
			want:  cpi.ResourceStateStopped,
		},
		{
			name:  "shutting-down",
			state: &types.InstanceState{Name: types.InstanceStateNameShuttingDown},
			want:  cpi.ResourceStateDeleting,
		},
		{
			name:  "terminated",
			state: &types.InstanceState{Name: types.InstanceStateNameTerminated},
			want:  cpi.ResourceStateDeleted,
		},
		{
			name:  "unknown enum value",
			state: &types.InstanceState{Name: types.InstanceStateName("bogus")},
			want:  cpi.ResourceStateUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ec2StateToResourceState(tt.state)
			if got != tt.want {
				t.Errorf("ec2StateToResourceState(%v) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

// ---- ec2InstanceToCPIInstance -----------------------------------------------

func TestEc2InstanceToCPIInstance_MinimalFields(t *testing.T) {
	t.Parallel()

	m := nilComputeManager()
	inst := &types.Instance{
		InstanceId:   aws.String("i-abc123"),
		InstanceType: types.InstanceTypeT3Micro,
		ImageId:      aws.String("ami-999"),
		State:        &types.InstanceState{Name: types.InstanceStateNameRunning},
		Placement:    &types.Placement{AvailabilityZone: aws.String("us-east-1a")},
	}

	got := m.ec2InstanceToCPIInstance(inst)

	if got.ID != "i-abc123" {
		t.Errorf("ID = %q, want %q", got.ID, "i-abc123")
	}
	if got.State != cpi.ResourceStateActive {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateActive)
	}
	if got.Flavor != string(types.InstanceTypeT3Micro) {
		t.Errorf("Flavor = %q, want %q", got.Flavor, string(types.InstanceTypeT3Micro))
	}
	if got.Image != "ami-999" {
		t.Errorf("Image = %q, want %q", got.Image, "ami-999")
	}
	if got.AvailabilityZone != "us-east-1a" {
		t.Errorf("AvailabilityZone = %q, want %q", got.AvailabilityZone, "us-east-1a")
	}
	// nil pointer fields default to empty string
	if got.PrivateIP != "" {
		t.Errorf("PrivateIP = %q, want empty", got.PrivateIP)
	}
	if got.PublicIP != "" {
		t.Errorf("PublicIP = %q, want empty", got.PublicIP)
	}
	if got.SubnetID != "" {
		t.Errorf("SubnetID = %q, want empty", got.SubnetID)
	}
	if got.NetworkID != "" {
		t.Errorf("NetworkID = %q, want empty", got.NetworkID)
	}
	if got.KeyPair != "" {
		t.Errorf("KeyPair = %q, want empty", got.KeyPair)
	}
	if len(got.SecurityGroups) != 0 {
		t.Errorf("SecurityGroups len = %d, want 0", len(got.SecurityGroups))
	}
	if len(got.Volumes) != 0 {
		t.Errorf("Volumes len = %d, want 0", len(got.Volumes))
	}
}

func TestEc2InstanceToCPIInstance_FullFields(t *testing.T) {
	t.Parallel()

	launchTime := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	m := nilComputeManager()
	inst := &types.Instance{
		InstanceId:       aws.String("i-full123"),
		InstanceType:     types.InstanceTypeM5Large,
		ImageId:          aws.String("ami-full"),
		State:            &types.InstanceState{Name: types.InstanceStateNameStopped},
		Placement:        &types.Placement{AvailabilityZone: aws.String("us-west-2b")},
		PrivateIpAddress: aws.String("10.0.1.5"),
		PublicIpAddress:  aws.String("54.1.2.3"),
		SubnetId:         aws.String("subnet-aaa"),
		VpcId:            aws.String("vpc-bbb"),
		KeyName:          aws.String("my-key"),
		LaunchTime:       &launchTime,
		Tags: []types.Tag{
			{Key: aws.String("Name"), Value: aws.String("test-vm")},
			{Key: aws.String("env"), Value: aws.String("prod")},
		},
		SecurityGroups: []types.GroupIdentifier{
			{GroupId: aws.String("sg-111")},
			{GroupId: aws.String("sg-222")},
		},
		BlockDeviceMappings: []types.InstanceBlockDeviceMapping{
			{Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-aaa")}},
			{Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("vol-bbb")}},
			// entry without Ebs — must be skipped
			{Ebs: nil},
		},
	}

	got := m.ec2InstanceToCPIInstance(inst)

	if got.ID != "i-full123" {
		t.Errorf("ID = %q, want %q", got.ID, "i-full123")
	}
	if got.Name != "test-vm" {
		t.Errorf("Name = %q, want %q", got.Name, "test-vm")
	}
	if got.State != cpi.ResourceStateStopped {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateStopped)
	}
	if got.PrivateIP != "10.0.1.5" {
		t.Errorf("PrivateIP = %q, want %q", got.PrivateIP, "10.0.1.5")
	}
	if got.PublicIP != "54.1.2.3" {
		t.Errorf("PublicIP = %q, want %q", got.PublicIP, "54.1.2.3")
	}
	if got.SubnetID != "subnet-aaa" {
		t.Errorf("SubnetID = %q, want %q", got.SubnetID, "subnet-aaa")
	}
	if got.NetworkID != "vpc-bbb" {
		t.Errorf("NetworkID = %q, want %q", got.NetworkID, "vpc-bbb")
	}
	if got.KeyPair != "my-key" {
		t.Errorf("KeyPair = %q, want %q", got.KeyPair, "my-key")
	}
	if len(got.SecurityGroups) != 2 {
		t.Errorf("SecurityGroups len = %d, want 2", len(got.SecurityGroups))
	}
	// only 2 entries have non-nil Ebs
	if len(got.Volumes) != 2 {
		t.Errorf("Volumes len = %d, want 2", len(got.Volumes))
	}
	if got.Volumes[0] != "vol-aaa" || got.Volumes[1] != "vol-bbb" {
		t.Errorf("Volumes = %v, want [vol-aaa vol-bbb]", got.Volumes)
	}
	if got.Tags["env"] != "prod" {
		t.Errorf("Tags[env] = %q, want %q", got.Tags["env"], "prod")
	}
	if got.CreatedAt != launchTime {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, launchTime)
	}
}

func TestEc2InstanceToCPIInstance_BDMNilVolumeId(t *testing.T) {
	t.Parallel()

	m := nilComputeManager()
	inst := &types.Instance{
		InstanceId:   aws.String("i-nilbdm"),
		InstanceType: types.InstanceTypeT3Nano,
		ImageId:      aws.String("ami-x"),
		State:        &types.InstanceState{Name: types.InstanceStateNameRunning},
		Placement:    &types.Placement{AvailabilityZone: aws.String("us-east-1b")},
		BlockDeviceMappings: []types.InstanceBlockDeviceMapping{
			// Ebs present but VolumeId is nil — must be skipped
			{Ebs: &types.EbsInstanceBlockDevice{VolumeId: nil}},
		},
	}

	got := m.ec2InstanceToCPIInstance(inst)
	if len(got.Volumes) != 0 {
		t.Errorf("Volumes len = %d, want 0 (nil VolumeId must be skipped)", len(got.Volumes))
	}
}

func TestEc2InstanceToCPIInstance_NoNameTag(t *testing.T) {
	t.Parallel()

	m := nilComputeManager()
	inst := &types.Instance{
		InstanceId:   aws.String("i-notag"),
		InstanceType: types.InstanceTypeT3Micro,
		ImageId:      aws.String("ami-y"),
		State:        &types.InstanceState{Name: types.InstanceStateNameRunning},
		Placement:    &types.Placement{AvailabilityZone: aws.String("us-east-1c")},
		Tags: []types.Tag{
			{Key: aws.String("env"), Value: aws.String("staging")},
		},
	}

	got := m.ec2InstanceToCPIInstance(inst)
	if got.Name != "" {
		t.Errorf("Name = %q, want empty (no Name tag)", got.Name)
	}
}

// ---- ec2ImageToCPIImage -----------------------------------------------------

func TestEc2ImageToCPIImage_MinimalFields(t *testing.T) {
	t.Parallel()

	m := nilComputeManager()
	img := &types.Image{
		ImageId:      aws.String("ami-min"),
		Name:         aws.String("base-image"),
		Architecture: types.ArchitectureValuesX8664,
		State:        types.ImageStateAvailable,
	}

	got := m.ec2ImageToCPIImage(img)

	if got.ID != "ami-min" {
		t.Errorf("ID = %q, want %q", got.ID, "ami-min")
	}
	if got.Name != "base-image" {
		t.Errorf("Name = %q, want %q", got.Name, "base-image")
	}
	if got.Architecture != string(types.ArchitectureValuesX8664) {
		t.Errorf("Architecture = %q, want %q", got.Architecture, string(types.ArchitectureValuesX8664))
	}
	if got.State != string(types.ImageStateAvailable) {
		t.Errorf("State = %q, want %q", got.State, string(types.ImageStateAvailable))
	}
	if got.Size != 0 {
		t.Errorf("Size = %d, want 0 (no BDM)", got.Size)
	}
	if got.OS != "" {
		t.Errorf("OS = %q, want empty (no PlatformDetails)", got.OS)
	}
}

func TestEc2ImageToCPIImage_FullFields(t *testing.T) {
	t.Parallel()

	volSize := int32(20)
	m := nilComputeManager()
	img := &types.Image{
		ImageId:         aws.String("ami-full"),
		Name:            aws.String("ubuntu-22"),
		Description:     aws.String("Ubuntu 22.04 LTS"),
		Architecture:    types.ArchitectureValuesArm64,
		State:           types.ImageStatePending,
		Public:          aws.Bool(true),
		PlatformDetails: aws.String("Linux/UNIX"),
		BlockDeviceMappings: []types.BlockDeviceMapping{
			{Ebs: &types.EbsBlockDevice{VolumeSize: &volSize}},
		},
		Tags: []types.Tag{
			{Key: aws.String("Name"), Value: aws.String("ubuntu-22")},
		},
	}

	got := m.ec2ImageToCPIImage(img)

	if got.Description != "Ubuntu 22.04 LTS" {
		t.Errorf("Description = %q, want %q", got.Description, "Ubuntu 22.04 LTS")
	}
	if got.OS != "Linux/UNIX" {
		t.Errorf("OS = %q, want %q", got.OS, "Linux/UNIX")
	}
	if !got.Public {
		t.Errorf("Public = false, want true")
	}
	const bytesPerGB = int64(1024 * 1024 * 1024)
	if got.Size != 20*bytesPerGB {
		t.Errorf("Size = %d, want %d", got.Size, 20*bytesPerGB)
	}
}

func TestEc2ImageToCPIImage_BDMUsesFirstEntry(t *testing.T) {
	t.Parallel()

	vol1 := int32(10)
	vol2 := int32(50)
	m := nilComputeManager()
	img := &types.Image{
		ImageId:      aws.String("ami-multi"),
		Architecture: types.ArchitectureValuesX8664,
		State:        types.ImageStateAvailable,
		BlockDeviceMappings: []types.BlockDeviceMapping{
			{Ebs: &types.EbsBlockDevice{VolumeSize: &vol1}},
			{Ebs: &types.EbsBlockDevice{VolumeSize: &vol2}},
		},
	}

	got := m.ec2ImageToCPIImage(img)
	const bytesPerGB = int64(1024 * 1024 * 1024)
	if got.Size != 10*bytesPerGB {
		t.Errorf("Size = %d, want first BDM value %d", got.Size, 10*bytesPerGB)
	}
}

func TestEc2ImageToCPIImage_BDMNilEbs(t *testing.T) {
	t.Parallel()

	m := nilComputeManager()
	img := &types.Image{
		ImageId:      aws.String("ami-nilbdm"),
		Architecture: types.ArchitectureValuesX8664,
		State:        types.ImageStateAvailable,
		BlockDeviceMappings: []types.BlockDeviceMapping{
			{Ebs: nil},
		},
	}

	got := m.ec2ImageToCPIImage(img)
	if got.Size != 0 {
		t.Errorf("Size = %d, want 0 (nil Ebs skipped)", got.Size)
	}
}

// ---- ec2InstanceTypeToCPIFlavor ---------------------------------------------

func TestEc2InstanceTypeToCPIFlavor_AllFields(t *testing.T) {
	t.Parallel()

	vcpus := int32(4)
	memMiB := int64(8192)
	diskGB := int64(100)
	netPerf := "Up to 10 Gigabit"

	m := nilComputeManager()
	info := &types.InstanceTypeInfo{
		InstanceType: types.InstanceTypeM5Large,
		VCpuInfo: &types.VCpuInfo{
			DefaultVCpus: &vcpus,
		},
		MemoryInfo: &types.MemoryInfo{
			SizeInMiB: &memMiB,
		},
		InstanceStorageInfo: &types.InstanceStorageInfo{
			TotalSizeInGB: &diskGB,
		},
		NetworkInfo: &types.NetworkInfo{
			NetworkPerformance: &netPerf,
		},
	}

	got := m.ec2InstanceTypeToCPIFlavor(info)

	if got.ID != string(types.InstanceTypeM5Large) {
		t.Errorf("ID = %q, want %q", got.ID, string(types.InstanceTypeM5Large))
	}
	if got.Name != string(types.InstanceTypeM5Large) {
		t.Errorf("Name = %q, want %q", got.Name, string(types.InstanceTypeM5Large))
	}
	if got.VCPUs != 4 {
		t.Errorf("VCPUs = %d, want 4", got.VCPUs)
	}
	if got.RAM != 8192 {
		t.Errorf("RAM = %d, want 8192", got.RAM)
	}
	if got.Ephemeral != 100 {
		t.Errorf("Ephemeral = %d, want 100", got.Ephemeral)
	}
	if got.Description != "Network: Up to 10 Gigabit" {
		t.Errorf("Description = %q, want %q", got.Description, "Network: Up to 10 Gigabit")
	}
}

func TestEc2InstanceTypeToCPIFlavor_NilOptionalFields(t *testing.T) {
	t.Parallel()

	m := nilComputeManager()
	info := &types.InstanceTypeInfo{
		InstanceType:        types.InstanceTypeT2Micro,
		VCpuInfo:            nil,
		MemoryInfo:          nil,
		InstanceStorageInfo: nil,
		NetworkInfo:         nil,
	}

	got := m.ec2InstanceTypeToCPIFlavor(info)

	if got.ID != string(types.InstanceTypeT2Micro) {
		t.Errorf("ID = %q, want %q", got.ID, string(types.InstanceTypeT2Micro))
	}
	if got.VCPUs != 0 {
		t.Errorf("VCPUs = %d, want 0 (nil VCpuInfo)", got.VCPUs)
	}
	if got.RAM != 0 {
		t.Errorf("RAM = %d, want 0 (nil MemoryInfo)", got.RAM)
	}
	if got.Ephemeral != 0 {
		t.Errorf("Ephemeral = %d, want 0 (nil InstanceStorageInfo)", got.Ephemeral)
	}
	if got.Description != "" {
		t.Errorf("Description = %q, want empty (nil NetworkInfo)", got.Description)
	}
}

func TestEc2InstanceTypeToCPIFlavor_NilInnerPointers(t *testing.T) {
	t.Parallel()

	m := nilComputeManager()
	info := &types.InstanceTypeInfo{
		InstanceType:        types.InstanceTypeT3Nano,
		VCpuInfo:            &types.VCpuInfo{DefaultVCpus: nil},
		MemoryInfo:          &types.MemoryInfo{SizeInMiB: nil},
		InstanceStorageInfo: &types.InstanceStorageInfo{TotalSizeInGB: nil},
		NetworkInfo:         &types.NetworkInfo{NetworkPerformance: nil},
	}

	got := m.ec2InstanceTypeToCPIFlavor(info)

	if got.VCPUs != 0 {
		t.Errorf("VCPUs = %d, want 0 (nil DefaultVCpus ptr)", got.VCPUs)
	}
	if got.RAM != 0 {
		t.Errorf("RAM = %d, want 0 (nil SizeInMiB ptr)", got.RAM)
	}
	if got.Ephemeral != 0 {
		t.Errorf("Ephemeral = %d, want 0 (nil TotalSizeInGB ptr)", got.Ephemeral)
	}
	if got.Description != "" {
		t.Errorf("Description = %q, want empty (nil NetworkPerformance ptr)", got.Description)
	}
}
