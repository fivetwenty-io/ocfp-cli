package aws

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"testing"
)

type parityEC2 struct {
	EC2API
	run          *ec2.RunInstancesInput
	modify       []*ec2.ModifyInstanceAttributeInput
	pages        int
	volumeCreate *ec2.CreateVolumeInput
	volume       types.Volume
	attachCalls  int
}

func (f *parityEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.run = in
	return &ec2.RunInstancesOutput{Instances: []types.Instance{{InstanceId: aws.String("i-1"), State: &types.InstanceState{Name: types.InstanceStateNameRunning}, Placement: &types.Placement{}}}}, nil
}
func (f *parityEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.pages++
	out := &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{InstanceId: aws.String("i-1"), State: &types.InstanceState{Name: types.InstanceStateNameRunning}, Placement: &types.Placement{AvailabilityZone: aws.String("us-east-1b")}}}}}}
	if len(in.InstanceIds) == 0 && in.NextToken == nil {
		out.NextToken = aws.String("next")
	}
	return out, nil
}
func (f *parityEC2) DescribeImages(context.Context, *ec2.DescribeImagesInput, ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	return &ec2.DescribeImagesOutput{Images: []types.Image{{RootDeviceName: aws.String("/dev/xvda"), RootDeviceType: types.DeviceTypeEbs}}}, nil
}
func (f *parityEC2) ModifyInstanceAttribute(_ context.Context, in *ec2.ModifyInstanceAttributeInput, _ ...func(*ec2.Options)) (*ec2.ModifyInstanceAttributeOutput, error) {
	f.modify = append(f.modify, in)
	return &ec2.ModifyInstanceAttributeOutput{}, nil
}
func (f *parityEC2) CreateVolume(_ context.Context, in *ec2.CreateVolumeInput, _ ...func(*ec2.Options)) (*ec2.CreateVolumeOutput, error) {
	f.volumeCreate = in
	return &ec2.CreateVolumeOutput{VolumeId: aws.String("vol-1"), State: types.VolumeStateAvailable}, nil
}
func (f *parityEC2) DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{f.volume}}, nil
}
func (f *parityEC2) AttachVolume(context.Context, *ec2.AttachVolumeInput, ...func(*ec2.Options)) (*ec2.AttachVolumeOutput, error) {
	f.attachCalls++
	return nil, errors.New("unexpected attach")
}
func TestParityAWSComputeRequest(t *testing.T) {
	f := &parityEC2{}
	m := &ComputeManager{ec2: f}
	_, err := m.CreateInstance(context.Background(), &cpi.InstanceRequest{Name: "bloc-bastion", Image: "ami-1", SubnetID: "subnet-1", StaticPrivateIP: "10.0.0.10", BootVolumeSize: 50, Protected: true, PublicIP: aws.Bool(false), Tailscale: &cpi.TailscaleSpec{}})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.run.NetworkInterfaces[0].PrivateIpAddress) != "10.0.0.10" {
		t.Error("private IP ignored")
	}
	if aws.ToBool(f.run.NetworkInterfaces[0].AssociatePublicIpAddress) {
		t.Error("private guest exposed")
	}
	if !aws.ToBool(f.run.DisableApiTermination) {
		t.Error("protection ignored")
	}
	if aws.ToString(f.run.BlockDeviceMappings[0].DeviceName) != "/dev/xvda" {
		t.Error("AMI root device ignored")
	}
	if len(f.modify) != 1 || f.modify[0].SourceDestCheck == nil || aws.ToBool(f.modify[0].SourceDestCheck.Value) {
		t.Error("forwarding not enabled")
	}
}
func TestParityAWSRejectStoppedBeforeMutation(t *testing.T) {
	f := &parityEC2{}
	_, err := (&ComputeManager{ec2: f}).CreateInstance(context.Background(), &cpi.InstanceRequest{CreateStopped: true})
	if err == nil || f.run != nil {
		t.Fatal("unsafe stopped creation accepted")
	}
}
func TestParityAWSInstancePagination(t *testing.T) {
	f := &parityEC2{}
	got, err := (&ComputeManager{ec2: f}).ListInstances(context.Background(), nil)
	if err != nil || len(got) != 2 {
		t.Fatalf("pagination got %d: %v", len(got), err)
	}
}
func TestParityAWSVolumeOwnerZone(t *testing.T) {
	f := &parityEC2{volume: types.Volume{VolumeId: aws.String("vol-1"), State: types.VolumeStateAvailable}}
	_, err := (&StorageManager{ec2: f, pollInterval: 1}).CreateVolume(context.Background(), &cpi.VolumeRequest{Name: "data", SizeGB: 64, InstanceID: "i-1"})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.volumeCreate.AvailabilityZone) != "us-east-1b" {
		t.Fatal("owner zone missing")
	}
}
func TestParityAWSAttachmentIdempotent(t *testing.T) {
	f := &parityEC2{volume: types.Volume{VolumeId: aws.String("vol-1"), State: types.VolumeStateInUse, Attachments: []types.VolumeAttachment{{InstanceId: aws.String("i-1"), Device: aws.String("/dev/sdf"), State: types.VolumeAttachmentStateAttached}}}}
	err := (&StorageManager{ec2: f}).AttachVolume(context.Background(), "vol-1", "i-1", "/dev/sdf")
	if err != nil || f.attachCalls != 0 {
		t.Fatalf("repeat attach: %v", err)
	}
}

func TestParityAWSProtectionCanBeClearedExplicitly(t *testing.T) {
	f := &parityEC2{}
	m := &ComputeManager{ec2: f}
	optioner, ok := any(m).(interface {
		EnsureGuestOptions(context.Context, string, cpi.GuestOptions) error
	})
	if !ok {
		t.Fatal("missing protection convergence")
	}
	if err := optioner.EnsureGuestOptions(context.Background(), "i-1", cpi.GuestOptions{Protected: false}); err != nil {
		t.Fatal(err)
	}
	if len(f.modify) != 1 || f.modify[0].DisableApiTermination == nil || aws.ToBool(f.modify[0].DisableApiTermination.Value) {
		t.Fatal("protection not explicitly cleared")
	}
}

type pageEC2 struct{ EC2API }

func nextToken(in *string) *string {
	if in == nil {
		return aws.String("next")
	}
	return nil
}
func (*pageEC2) DescribeVpcs(_ context.Context, in *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{Vpcs: []types.Vpc{{VpcId: aws.String("vpc-1")}}, NextToken: nextToken(in.NextToken)}, nil
}
func (*pageEC2) DescribeSubnets(_ context.Context, in *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{{SubnetId: aws.String("subnet-1")}}, NextToken: nextToken(in.NextToken)}, nil
}
func (*pageEC2) DescribeSecurityGroups(_ context.Context, in *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []types.SecurityGroup{{GroupId: aws.String("sg-1")}}, NextToken: nextToken(in.NextToken)}, nil
}
func (*pageEC2) DescribeVolumes(_ context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String("vol-1")}}, NextToken: nextToken(in.NextToken)}, nil
}
func (*pageEC2) DescribeSnapshots(_ context.Context, in *ec2.DescribeSnapshotsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error) {
	return &ec2.DescribeSnapshotsOutput{Snapshots: []types.Snapshot{{SnapshotId: aws.String("snap-1")}}, NextToken: nextToken(in.NextToken)}, nil
}
func TestParityAWSDiscoveryPages(t *testing.T) {
	f := &pageEC2{}
	ctx := context.Background()
	networks, e := (&NetworkManager{ec2: f, client: &Client{config: &Config{Region: "us-east-1"}}}).ListNetworks(ctx, nil)
	if e != nil || len(networks) != 2 {
		t.Errorf("networks=%d %v", len(networks), e)
	}
	subnets, e := (&NetworkManager{ec2: f, client: &Client{config: &Config{Region: "us-east-1"}}}).ListSubnets(ctx, "")
	if e != nil || len(subnets) != 2 {
		t.Errorf("subnets=%d %v", len(subnets), e)
	}
	groups, e := (&SecurityManager{ec2: f}).ListSecurityGroups(ctx, nil)
	if e != nil || len(groups) != 2 {
		t.Errorf("groups=%d %v", len(groups), e)
	}
	volumes, e := (&StorageManager{ec2: f}).ListVolumes(ctx, nil)
	if e != nil || len(volumes) != 2 {
		t.Errorf("volumes=%d %v", len(volumes), e)
	}
	snapshots, e := (&StorageManager{ec2: f}).ListSnapshots(ctx, "", nil)
	if e != nil || len(snapshots) != 2 {
		t.Errorf("snapshots=%d %v", len(snapshots), e)
	}
}

func (*pageEC2) DescribeImages(_ context.Context, in *ec2.DescribeImagesInput, _ ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	return &ec2.DescribeImagesOutput{Images: []types.Image{{ImageId: aws.String("ami-1")}}, NextToken: nextToken(in.NextToken)}, nil
}
func (*pageEC2) DescribeInstanceTypes(_ context.Context, in *ec2.DescribeInstanceTypesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstanceTypesOutput, error) {
	return &ec2.DescribeInstanceTypesOutput{InstanceTypes: []types.InstanceTypeInfo{{InstanceType: types.InstanceTypeT3Medium, VCpuInfo: &types.VCpuInfo{DefaultVCpus: aws.Int32(2)}, MemoryInfo: &types.MemoryInfo{SizeInMiB: aws.Int64(4096)}}}, NextToken: nextToken(in.NextToken)}, nil
}
func (*pageEC2) DescribeRouteTables(_ context.Context, in *ec2.DescribeRouteTablesInput, _ ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{{RouteTableId: aws.String("rtb-1")}}, NextToken: nextToken(in.NextToken)}, nil
}
func TestParityAWSImageFlavorRouterPages(t *testing.T) {
	f := &pageEC2{}
	ctx := context.Background()
	images, e := (&ComputeManager{ec2: f}).ListImages(ctx, nil)
	if e != nil || len(images) != 2 {
		t.Errorf("images=%d %v", len(images), e)
	}
	flavors, e := (&ComputeManager{ec2: f}).ListFlavors(ctx)
	if e != nil || len(flavors) != 2 {
		t.Errorf("flavors=%d %v", len(flavors), e)
	}
	routers, e := (&NetworkManager{ec2: f}).ListRouters(ctx)
	if e != nil || len(routers) != 2 {
		t.Errorf("routers=%d %v", len(routers), e)
	}
}

func TestParityAWSDetachAlreadyDetached(t *testing.T) {
	f := &stFakeEC2{describeVolumesOut: &ec2.DescribeVolumesOutput{Volumes: []types.Volume{{VolumeId: aws.String("vol-1"), State: types.VolumeStateAvailable}}}, detachVolumeErr: errors.New("must not detach an available disk")}
	if err := (&StorageManager{ec2: f}).DetachVolume(context.Background(), "vol-1", "i-1"); err != nil {
		t.Fatal(err)
	}
}

type refreshFailureEC2 struct {
	parityEC2
	reads int
}

func (f *refreshFailureEC2) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.reads++
	if f.reads > 1 {
		return nil, errors.New("refresh unavailable")
	}
	return f.parityEC2.DescribeInstances(ctx, in, opts...)
}
func TestParityAWSCreatePreservesHandleOnRefreshFailure(t *testing.T) {
	f := &refreshFailureEC2{}
	instance, err := (&ComputeManager{ec2: f}).CreateInstance(context.Background(), &cpi.InstanceRequest{Name: "bastion", Image: "ami-1"})
	if err == nil || instance == nil || instance.ID != "i-1" {
		t.Fatalf("created identity lost: instance=%+v err=%v", instance, err)
	}
}

func TestParityAWSCreateVolumePreservesHandleOnWaitFailure(t *testing.T) {
	f := &stFakeEC2{createVolumeOut: &ec2.CreateVolumeOutput{VolumeId: aws.String("vol-recover")}, describeVolumesErr: errors.New("read unavailable")}
	volume, err := (&StorageManager{ec2: f}).CreateVolume(context.Background(), &cpi.VolumeRequest{Name: "data", SizeGB: 64, AvailabilityZone: "us-east-1b"})
	if err == nil || volume == nil || volume.ID != "vol-recover" {
		t.Fatalf("must preserve created volume for recovery: volume=%+v err=%v", volume, err)
	}
}
