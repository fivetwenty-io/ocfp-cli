package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"testing"
)

type bootEC2 struct {
	EC2API
	instance      types.Instance
	root          string
	calls         []string
	create        *ec2.CreateVolumeInput
	userdata      []byte
	failAttach    bool
	failVolume    bool
	snapshotCount int
	snapshotCalls int
	recovery      []types.Volume
}

func newBootEC2() *bootEC2 {
	return &bootEC2{root: "old", instance: types.Instance{InstanceId: aws.String("i-1"), Architecture: types.ArchitectureValuesX8664, VirtualizationType: types.VirtualizationTypeHvm, RootDeviceType: types.DeviceTypeEbs, RootDeviceName: aws.String("/dev/xvda"), Placement: &types.Placement{AvailabilityZone: aws.String("zone")}, State: &types.InstanceState{Name: types.InstanceStateNameStopped}, NetworkInterfaces: []types.InstanceNetworkInterface{{NetworkInterfaceId: aws.String("eni"), PrivateIpAddress: aws.String("10.0.0.1")}}}}
}
func (f *bootEC2) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	i := f.instance
	i.BlockDeviceMappings = []types.InstanceBlockDeviceMapping{{DeviceName: aws.String("/dev/sdf"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String("data")}}}
	if f.root != "" {
		i.BlockDeviceMappings = append(i.BlockDeviceMappings, types.InstanceBlockDeviceMapping{DeviceName: aws.String("/dev/xvda"), Ebs: &types.EbsInstanceBlockDevice{VolumeId: aws.String(f.root), DeleteOnTermination: aws.Bool(true)}})
	}
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{i}}}}, nil
}
func (f *bootEC2) DescribeInstanceAttribute(context.Context, *ec2.DescribeInstanceAttributeInput, ...func(*ec2.Options)) (*ec2.DescribeInstanceAttributeOutput, error) {
	return &ec2.DescribeInstanceAttributeOutput{UserData: &types.AttributeValue{Value: aws.String(base64.StdEncoding.EncodeToString([]byte("old-userdata")))}}, nil
}
func (f *bootEC2) DescribeImages(context.Context, *ec2.DescribeImagesInput, ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	return &ec2.DescribeImagesOutput{Images: []types.Image{{Architecture: types.ArchitectureValuesX8664, VirtualizationType: types.VirtualizationTypeHvm, RootDeviceType: types.DeviceTypeEbs, RootDeviceName: aws.String("/dev/xvda"), BlockDeviceMappings: []types.BlockDeviceMapping{{DeviceName: aws.String("/dev/xvda"), Ebs: &types.EbsBlockDevice{SnapshotId: aws.String("source"), VolumeSize: aws.Int32(8)}}}}}}, nil
}
func (f *bootEC2) DescribeVolumes(_ context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	if f.failVolume {
		return nil, errors.New("read failed")
	}
	if len(in.VolumeIds) == 0 {
		return &ec2.DescribeVolumesOutput{Volumes: f.recovery}, nil
	}
	id := in.VolumeIds[0]
	v := types.Volume{VolumeId: aws.String(id), AvailabilityZone: aws.String("zone"), Size: aws.Int32(20), State: types.VolumeStateAvailable, Tags: []types.Tag{{Key: aws.String("ocfp:instance"), Value: aws.String("i-1")}}}
	if id == f.root {
		v.State = types.VolumeStateInUse
		v.Attachments = []types.VolumeAttachment{{InstanceId: aws.String("i-1"), Device: aws.String("/dev/xvda"), State: types.VolumeAttachmentStateAttached}}
	}
	return &ec2.DescribeVolumesOutput{Volumes: []types.Volume{v}}, nil
}
func (f *bootEC2) CreateVolume(_ context.Context, in *ec2.CreateVolumeInput, _ ...func(*ec2.Options)) (*ec2.CreateVolumeOutput, error) {
	f.create = in
	return &ec2.CreateVolumeOutput{VolumeId: aws.String("new")}, nil
}
func (f *bootEC2) DetachVolume(context.Context, *ec2.DetachVolumeInput, ...func(*ec2.Options)) (*ec2.DetachVolumeOutput, error) {
	f.calls = append(f.calls, "detach")
	f.root = ""
	return &ec2.DetachVolumeOutput{}, nil
}
func (f *bootEC2) AttachVolume(_ context.Context, in *ec2.AttachVolumeInput, _ ...func(*ec2.Options)) (*ec2.AttachVolumeOutput, error) {
	f.calls = append(f.calls, "attach")
	f.root = aws.ToString(in.VolumeId)
	if f.failAttach {
		return nil, errors.New("lost attach response")
	}
	return &ec2.AttachVolumeOutput{}, nil
}
func (f *bootEC2) ModifyInstanceAttribute(_ context.Context, in *ec2.ModifyInstanceAttributeInput, _ ...func(*ec2.Options)) (*ec2.ModifyInstanceAttributeOutput, error) {
	f.calls = append(f.calls, "modify")
	if in.UserData != nil {
		f.userdata = in.UserData.Value
	}
	return &ec2.ModifyInstanceAttributeOutput{}, nil
}
func TestBootRefreshSwitchAndRollback(t *testing.T) {
	f := newBootEC2()
	m := &ComputeManager{ec2: f}
	id, err := m.InspectBoot(t.Context(), "i-1")
	if err != nil {
		t.Fatal(err)
	}
	id.ReplacementUserData = "new-userdata"
	root, err := m.PrepareBoot(t.Context(), &cpi.BootRefreshRequest{Identity: *id, Image: "ami-new", Token: "token"})
	if err != nil || root.ID != "new" {
		t.Fatalf("%v %v", root, err)
	}
	if aws.ToString(f.create.ClientToken) != "token" || aws.ToInt32(f.create.Size) != 20 {
		t.Fatal(f.create)
	}
	if err = m.SwitchBoot(t.Context(), id, "old", "new"); err != nil {
		t.Fatal(err)
	}
	if f.root != "new" || string(f.userdata) != "new-userdata" {
		t.Fatal("switch lost payload")
	}
	if err = m.SwitchBoot(t.Context(), id, "new", "old"); err != nil {
		t.Fatal(err)
	}
	if string(f.userdata) != "old-userdata" {
		t.Fatal("rollback lost payload")
	}
}
func TestBootRefreshRefusesUnexpectedRoot(t *testing.T) {
	f := newBootEC2()
	m := &ComputeManager{ec2: f}
	id, err := m.InspectBoot(t.Context(), "i-1")
	if err != nil {
		t.Fatal(err)
	}
	id.ReplacementUserData = "new"
	f.root = "foreign"
	if m.SwitchBoot(t.Context(), id, "old", "new") == nil || len(f.calls) > 0 {
		t.Fatal("unsafe mutation")
	}
}
func TestBootRefreshRefusesIdentityDrift(t *testing.T) {
	f := newBootEC2()
	m := &ComputeManager{ec2: f}
	id, err := m.InspectBoot(t.Context(), "i-1")
	if err != nil {
		t.Fatal(err)
	}
	f.instance.NetworkInterfaces[0].NetworkInterfaceId = aws.String("foreign")
	if m.SwitchBoot(t.Context(), id, "old", "new") == nil || len(f.calls) > 0 {
		t.Fatal("unsafe mutation")
	}
}

func (f *bootEC2) DescribeSnapshots(_ context.Context, in *ec2.DescribeSnapshotsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error) {
	out := &ec2.DescribeSnapshotsOutput{}
	count := f.snapshotCount
	if len(in.SnapshotIds) > 0 {
		count = 1
	}
	for range count {
		out.Snapshots = append(out.Snapshots, types.Snapshot{SnapshotId: aws.String("snap"), VolumeId: aws.String("old"), State: types.SnapshotStateCompleted})
	}
	return out, nil
}
func (f *bootEC2) CreateSnapshot(context.Context, *ec2.CreateSnapshotInput, ...func(*ec2.Options)) (*ec2.CreateSnapshotOutput, error) {
	f.snapshotCalls++
	f.snapshotCount = 1
	return &ec2.CreateSnapshotOutput{SnapshotId: aws.String("snap")}, nil
}
func TestBootRefreshSnapshotReplay(t *testing.T) {
	f := newBootEC2()
	m := &ComputeManager{ec2: f}
	id, err := m.InspectBoot(t.Context(), "i-1")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		snap, err := m.SnapshotBoot(t.Context(), id, "token")
		if err != nil || snap != "snap" {
			t.Fatalf("%s %v", snap, err)
		}
	}
	if f.snapshotCalls != 1 {
		t.Fatal("duplicate snapshot")
	}
	f.snapshotCount = 2
	if _, err = m.SnapshotBoot(t.Context(), id, "token"); err == nil {
		t.Fatal("ambiguous snapshots accepted")
	}
}
func TestBootRefreshAttachResponseLossReplay(t *testing.T) {
	f := newBootEC2()
	m := &ComputeManager{ec2: f}
	id, err := m.InspectBoot(t.Context(), "i-1")
	if err != nil {
		t.Fatal(err)
	}
	id.ReplacementUserData = "new"
	f.failAttach = true
	if m.SwitchBoot(t.Context(), id, "old", "new") == nil {
		t.Fatal("expected lost response")
	}
	f.failAttach = false
	f.calls = nil
	if err = m.SwitchBoot(t.Context(), id, "old", "new"); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls {
		if call == "detach" || call == "attach" {
			t.Fatal("repeated attachment mutation")
		}
	}
}
func TestBootRefreshVolumeReadFailureBeforeMutation(t *testing.T) {
	f := newBootEC2()
	m := &ComputeManager{ec2: f}
	id, err := m.InspectBoot(t.Context(), "i-1")
	if err != nil {
		t.Fatal(err)
	}
	id.ReplacementUserData = "new"
	f.failVolume = true
	if m.SwitchBoot(t.Context(), id, "old", "new") == nil || len(f.calls) > 0 {
		t.Fatal("mutated after read failure")
	}
}

func TestBootRefreshRejectsUnrelatedPair(t *testing.T) {
	f := newBootEC2()
	m := &ComputeManager{ec2: f}
	id, err := m.InspectBoot(t.Context(), "i-1")
	if err != nil {
		t.Fatal(err)
	}
	id.ReplacementUserData = "new"
	f.root = "new"
	if m.SwitchBoot(t.Context(), id, "new", "unrelated") == nil || len(f.calls) > 0 {
		t.Fatal("unrelated roots accepted")
	}
}

func TestBootRefreshRecoveryNeverCreates(t *testing.T) {
	f := newBootEC2()
	m := &ComputeManager{ec2: f}
	req := &cpi.BootRefreshRequest{Token: "run", Identity: cpi.BootIdentity{InstanceID: "i-1", RootVolumeID: "old"}}
	got, err := m.RecoverBoot(t.Context(), req)
	if err != nil || got != nil {
		t.Fatal(got, err)
	}
	f.recovery = []types.Volume{{VolumeId: aws.String("new"), Tags: buildResourceTags(map[string]string{"ocfp:boot-refresh": "run", "ocfp:instance": "i-1"})}}
	got, err = m.RecoverBoot(t.Context(), req)
	if err != nil || got.ID != "new" || got.Fingerprint != "new" || f.create != nil {
		t.Fatal(got, err)
	}
	f.recovery = append(f.recovery, f.recovery[0])
	if _, err = m.RecoverBoot(t.Context(), req); err == nil {
		t.Fatal("ambiguous recovery accepted")
	}
}
