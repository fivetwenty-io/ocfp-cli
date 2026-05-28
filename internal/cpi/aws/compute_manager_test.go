package aws

// compute_manager_test.go — mock-driven tests for ComputeManager public methods.
// Uses computeFakeEC2 (unique name; sibling agents use different names).
// waitForInstanceState is exercised via a context-cancel path to avoid wall-clock waits.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// ---------------------------------------------------------------------------
// computeFakeEC2 — minimal EC2API stub for compute_manager_test.go
// ---------------------------------------------------------------------------

type computeFakeEC2 struct {
	// RunInstances
	runInstancesOut *ec2.RunInstancesOutput
	runInstancesErr error

	// DescribeInstances — supports a sequence of calls (queue) for waiter tests.
	// If describeQueue is non-empty, each call consumes the front.
	// Once empty, describeOut/describeErr are used.
	describeQueue    []*ec2.DescribeInstancesOutput
	describeQueueErr []error
	describeOut      *ec2.DescribeInstancesOutput
	describeErr      error

	// StartInstances / StopInstances / RebootInstances / TerminateInstances
	startInstancesOut     *ec2.StartInstancesOutput
	startInstancesErr     error
	stopInstancesOut      *ec2.StopInstancesOutput
	stopInstancesErr      error
	rebootInstancesErr    error
	terminateInstancesOut *ec2.TerminateInstancesOutput
	terminateInstancesErr error

	// CreateKeyPair / ImportKeyPair / DescribeKeyPairs / DeleteKeyPair
	createKeyPairOut    *ec2.CreateKeyPairOutput
	createKeyPairErr    error
	importKeyPairErr    error
	describeKeyPairsOut *ec2.DescribeKeyPairsOutput
	describeKeyPairsErr error
	deleteKeyPairErr    error

	// DescribeImages
	describeImagesOut *ec2.DescribeImagesOutput
	describeImagesErr error

	// DescribeInstanceTypes
	describeInstanceTypesOut *ec2.DescribeInstanceTypesOutput
	describeInstanceTypesErr error

	// call tracking
	runInstancesCalled       bool
	startInstancesCalled     bool
	stopInstancesCalled      bool
	rebootInstancesCalled    bool
	terminateInstancesCalled bool
	importKeyPairCalled      bool
	deleteKeyPairCalled      bool
}

func (f *computeFakeEC2) describeInstances(_ context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if len(f.describeQueue) > 0 {
		out := f.describeQueue[0]
		var err error
		if len(f.describeQueueErr) > 0 {
			err = f.describeQueueErr[0]
			f.describeQueueErr = f.describeQueueErr[1:]
		}
		f.describeQueue = f.describeQueue[1:]
		return out, err
	}
	return f.describeOut, f.describeErr
}

// EC2API method implementations

func (f *computeFakeEC2) RunInstances(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.runInstancesCalled = true
	return f.runInstancesOut, f.runInstancesErr
}

func (f *computeFakeEC2) DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return f.describeInstances(ctx, params, optFns...)
}

func (f *computeFakeEC2) StartInstances(ctx context.Context, params *ec2.StartInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	f.startInstancesCalled = true
	return f.startInstancesOut, f.startInstancesErr
}

func (f *computeFakeEC2) StopInstances(ctx context.Context, params *ec2.StopInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	f.stopInstancesCalled = true
	return f.stopInstancesOut, f.stopInstancesErr
}

func (f *computeFakeEC2) RebootInstances(ctx context.Context, params *ec2.RebootInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RebootInstancesOutput, error) {
	f.rebootInstancesCalled = true
	return &ec2.RebootInstancesOutput{}, f.rebootInstancesErr
}

func (f *computeFakeEC2) TerminateInstances(ctx context.Context, params *ec2.TerminateInstancesInput, optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.terminateInstancesCalled = true
	return f.terminateInstancesOut, f.terminateInstancesErr
}

func (f *computeFakeEC2) CreateKeyPair(ctx context.Context, params *ec2.CreateKeyPairInput, optFns ...func(*ec2.Options)) (*ec2.CreateKeyPairOutput, error) {
	return f.createKeyPairOut, f.createKeyPairErr
}

func (f *computeFakeEC2) ImportKeyPair(ctx context.Context, params *ec2.ImportKeyPairInput, optFns ...func(*ec2.Options)) (*ec2.ImportKeyPairOutput, error) {
	f.importKeyPairCalled = true
	return &ec2.ImportKeyPairOutput{}, f.importKeyPairErr
}

func (f *computeFakeEC2) DescribeKeyPairs(ctx context.Context, params *ec2.DescribeKeyPairsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeKeyPairsOutput, error) {
	return f.describeKeyPairsOut, f.describeKeyPairsErr
}

func (f *computeFakeEC2) DeleteKeyPair(ctx context.Context, params *ec2.DeleteKeyPairInput, optFns ...func(*ec2.Options)) (*ec2.DeleteKeyPairOutput, error) {
	f.deleteKeyPairCalled = true
	return &ec2.DeleteKeyPairOutput{}, f.deleteKeyPairErr
}

func (f *computeFakeEC2) DescribeImages(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	return f.describeImagesOut, f.describeImagesErr
}

func (f *computeFakeEC2) DescribeInstanceTypes(ctx context.Context, params *ec2.DescribeInstanceTypesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstanceTypesOutput, error) {
	return f.describeInstanceTypesOut, f.describeInstanceTypesErr
}

// Unused EC2API methods — required to satisfy interface.

func (f *computeFakeEC2) CreateVpc(ctx context.Context, params *ec2.CreateVpcInput, optFns ...func(*ec2.Options)) (*ec2.CreateVpcOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DescribeVpcs(ctx context.Context, params *ec2.DescribeVpcsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DeleteVpc(ctx context.Context, params *ec2.DeleteVpcInput, optFns ...func(*ec2.Options)) (*ec2.DeleteVpcOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) ModifyVpcAttribute(ctx context.Context, params *ec2.ModifyVpcAttributeInput, optFns ...func(*ec2.Options)) (*ec2.ModifyVpcAttributeOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) CreateSubnet(ctx context.Context, params *ec2.CreateSubnetInput, optFns ...func(*ec2.Options)) (*ec2.CreateSubnetOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DescribeSubnets(ctx context.Context, params *ec2.DescribeSubnetsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DeleteSubnet(ctx context.Context, params *ec2.DeleteSubnetInput, optFns ...func(*ec2.Options)) (*ec2.DeleteSubnetOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) ModifySubnetAttribute(ctx context.Context, params *ec2.ModifySubnetAttributeInput, optFns ...func(*ec2.Options)) (*ec2.ModifySubnetAttributeOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) CreateInternetGateway(ctx context.Context, params *ec2.CreateInternetGatewayInput, optFns ...func(*ec2.Options)) (*ec2.CreateInternetGatewayOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DescribeInternetGateways(ctx context.Context, params *ec2.DescribeInternetGatewaysInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) AttachInternetGateway(ctx context.Context, params *ec2.AttachInternetGatewayInput, optFns ...func(*ec2.Options)) (*ec2.AttachInternetGatewayOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DetachInternetGateway(ctx context.Context, params *ec2.DetachInternetGatewayInput, optFns ...func(*ec2.Options)) (*ec2.DetachInternetGatewayOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DeleteInternetGateway(ctx context.Context, params *ec2.DeleteInternetGatewayInput, optFns ...func(*ec2.Options)) (*ec2.DeleteInternetGatewayOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) CreateRouteTable(ctx context.Context, params *ec2.CreateRouteTableInput, optFns ...func(*ec2.Options)) (*ec2.CreateRouteTableOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DescribeRouteTables(ctx context.Context, params *ec2.DescribeRouteTablesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) CreateRoute(ctx context.Context, params *ec2.CreateRouteInput, optFns ...func(*ec2.Options)) (*ec2.CreateRouteOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) AssociateRouteTable(ctx context.Context, params *ec2.AssociateRouteTableInput, optFns ...func(*ec2.Options)) (*ec2.AssociateRouteTableOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DisassociateRouteTable(ctx context.Context, params *ec2.DisassociateRouteTableInput, optFns ...func(*ec2.Options)) (*ec2.DisassociateRouteTableOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DeleteRouteTable(ctx context.Context, params *ec2.DeleteRouteTableInput, optFns ...func(*ec2.Options)) (*ec2.DeleteRouteTableOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DescribeNetworkInterfaces(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) AllocateAddress(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DescribeAddresses(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) AssociateAddress(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DisassociateAddress(ctx context.Context, params *ec2.DisassociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.DisassociateAddressOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) ReleaseAddress(ctx context.Context, params *ec2.ReleaseAddressInput, optFns ...func(*ec2.Options)) (*ec2.ReleaseAddressOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) CreateVolume(ctx context.Context, params *ec2.CreateVolumeInput, optFns ...func(*ec2.Options)) (*ec2.CreateVolumeOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DescribeVolumes(ctx context.Context, params *ec2.DescribeVolumesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) AttachVolume(ctx context.Context, params *ec2.AttachVolumeInput, optFns ...func(*ec2.Options)) (*ec2.AttachVolumeOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DetachVolume(ctx context.Context, params *ec2.DetachVolumeInput, optFns ...func(*ec2.Options)) (*ec2.DetachVolumeOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) ModifyVolume(ctx context.Context, params *ec2.ModifyVolumeInput, optFns ...func(*ec2.Options)) (*ec2.ModifyVolumeOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DeleteVolume(ctx context.Context, params *ec2.DeleteVolumeInput, optFns ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) CreateSnapshot(ctx context.Context, params *ec2.CreateSnapshotInput, optFns ...func(*ec2.Options)) (*ec2.CreateSnapshotOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DescribeSnapshots(ctx context.Context, params *ec2.DescribeSnapshotsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DeleteSnapshot(ctx context.Context, params *ec2.DeleteSnapshotInput, optFns ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) CreateSecurityGroup(ctx context.Context, params *ec2.CreateSecurityGroupInput, optFns ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DescribeSecurityGroups(ctx context.Context, params *ec2.DescribeSecurityGroupsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) DeleteSecurityGroup(ctx context.Context, params *ec2.DeleteSecurityGroupInput, optFns ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) AuthorizeSecurityGroupIngress(ctx context.Context, params *ec2.AuthorizeSecurityGroupIngressInput, optFns ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupIngressOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) AuthorizeSecurityGroupEgress(ctx context.Context, params *ec2.AuthorizeSecurityGroupEgressInput, optFns ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) RevokeSecurityGroupIngress(ctx context.Context, params *ec2.RevokeSecurityGroupIngressInput, optFns ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}
func (f *computeFakeEC2) RevokeSecurityGroupEgress(ctx context.Context, params *ec2.RevokeSecurityGroupEgressInput, optFns ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupEgressOutput, error) {
	return nil, errors.New("not implemented in computeFakeEC2")
}

// ---------------------------------------------------------------------------
// computeFakeClient — wraps computeFakeEC2 to satisfy the Client shape used
// only in DI wiring; NOT a real Client — we inject ec2 directly.
// ---------------------------------------------------------------------------

// newComputeMgr builds a ComputeManager with an injected EC2API stub.
// client field is nil because getEC2 short-circuits when ec2 is set.
func newComputeMgr(fake *computeFakeEC2) *ComputeManager {
	return &ComputeManager{
		client: nil,
		ec2:    fake,
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// runningReservation returns a DescribeInstancesOutput with one running instance.
func runningReservation(id string) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{
		Reservations: []types.Reservation{
			{
				Instances: []types.Instance{
					{
						InstanceId:   aws.String(id),
						InstanceType: types.InstanceTypeT3Micro,
						ImageId:      aws.String("ami-test"),
						State:        &types.InstanceState{Name: types.InstanceStateNameRunning},
						Placement:    &types.Placement{AvailabilityZone: aws.String("us-east-1a")},
					},
				},
			},
		},
	}
}

func pendingReservation(id string) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{
		Reservations: []types.Reservation{
			{
				Instances: []types.Instance{
					{
						InstanceId:   aws.String(id),
						InstanceType: types.InstanceTypeT3Micro,
						ImageId:      aws.String("ami-test"),
						State:        &types.InstanceState{Name: types.InstanceStateNamePending},
						Placement:    &types.Placement{AvailabilityZone: aws.String("us-east-1a")},
					},
				},
			},
		},
	}
}

func stoppedReservation(id string) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{
		Reservations: []types.Reservation{
			{
				Instances: []types.Instance{
					{
						InstanceId:   aws.String(id),
						InstanceType: types.InstanceTypeT3Micro,
						ImageId:      aws.String("ami-test"),
						State:        &types.InstanceState{Name: types.InstanceStateNameStopped},
						Placement:    &types.Placement{AvailabilityZone: aws.String("us-east-1a")},
					},
				},
			},
		},
	}
}

func terminatedReservation(id string) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{
		Reservations: []types.Reservation{
			{
				Instances: []types.Instance{
					{
						InstanceId:   aws.String(id),
						InstanceType: types.InstanceTypeT3Micro,
						ImageId:      aws.String("ami-test"),
						State:        &types.InstanceState{Name: types.InstanceStateNameTerminated},
						Placement:    &types.Placement{AvailabilityZone: aws.String("us-east-1a")},
					},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// GetInstance
// ---------------------------------------------------------------------------

func TestComputeManager_GetInstance_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		describeOut: runningReservation("i-gettest"),
	}
	m := newComputeMgr(fake)

	inst, err := m.GetInstance(context.Background(), "i-gettest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.ID != "i-gettest" {
		t.Errorf("ID = %q, want %q", inst.ID, "i-gettest")
	}
	if inst.State != cpi.ResourceStateActive {
		t.Errorf("State = %q, want Active", inst.State)
	}
}

func TestComputeManager_GetInstance_NotFound(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		describeOut: &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{}},
	}
	m := newComputeMgr(fake)

	_, err := m.GetInstance(context.Background(), "i-missing")
	if err == nil {
		t.Fatal("expected error for empty reservations, got nil")
	}
	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "NotFound" {
		t.Errorf("expected ProviderError NotFound, got %v", err)
	}
}

func TestComputeManager_GetInstance_APIError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		describeErr: errors.New("describe failed"),
	}
	m := newComputeMgr(fake)

	_, err := m.GetInstance(context.Background(), "i-err")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListInstances
// ---------------------------------------------------------------------------

func TestComputeManager_ListInstances_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		describeOut: &ec2.DescribeInstancesOutput{
			Reservations: []types.Reservation{
				{
					Instances: []types.Instance{
						{
							InstanceId:   aws.String("i-a"),
							InstanceType: types.InstanceTypeT3Micro,
							ImageId:      aws.String("ami-1"),
							State:        &types.InstanceState{Name: types.InstanceStateNameRunning},
							Placement:    &types.Placement{AvailabilityZone: aws.String("us-east-1a")},
						},
						{
							InstanceId:   aws.String("i-b"),
							InstanceType: types.InstanceTypeT3Micro,
							ImageId:      aws.String("ami-1"),
							State:        &types.InstanceState{Name: types.InstanceStateNameStopped},
							Placement:    &types.Placement{AvailabilityZone: aws.String("us-east-1a")},
						},
					},
				},
			},
		},
	}
	m := newComputeMgr(fake)

	list, err := m.ListInstances(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len = %d, want 2", len(list))
	}
}

func TestComputeManager_ListInstances_APIError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{describeErr: errors.New("list failed")}
	m := newComputeMgr(fake)

	_, err := m.ListInstances(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// CreateInstance — RunInstances returns instance; waiter uses context cancel
// to avoid 5-second tick. The waiter's first DescribeInstances is the
// running state so it returns immediately after one ticker fire.
// ---------------------------------------------------------------------------

func TestComputeManager_CreateInstance_HappyPath(t *testing.T) {
	t.Parallel()

	const iid = "i-created"

	// RunInstances result — returns a pending instance so the waiter starts.
	runOut := &ec2.RunInstancesOutput{
		Instances: []types.Instance{
			{
				InstanceId:   aws.String(iid),
				InstanceType: types.InstanceTypeT3Micro,
				ImageId:      aws.String("ami-new"),
				State:        &types.InstanceState{Name: types.InstanceStateNamePending},
				Placement:    &types.Placement{AvailabilityZone: aws.String("us-east-1a")},
			},
		},
	}

	// waitForInstanceState will call DescribeInstances on the first tick (5s),
	// but we use a queue: first call returns running → waiter exits immediately.
	// GetInstance (called after waiter) consumes the fallback describeOut.
	fake := &computeFakeEC2{
		runInstancesOut: runOut,
		// Queue: first DescribeInstances (from waitForInstanceState) → running.
		describeQueue: []*ec2.DescribeInstancesOutput{
			runningReservation(iid),
		},
		describeQueueErr: []error{nil},
		// Fallback for GetInstance call after waiter.
		describeOut: runningReservation(iid),
	}
	m := newComputeMgr(fake)

	req := &cpi.InstanceRequest{
		Name:   "test-vm",
		Image:  "ami-new",
		Flavor: string(types.InstanceTypeT3Micro),
	}

	// Use a short-deadline context: if the waiter doesn't see "running" on
	// the first tick it would time out, but we've queued a running response.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := m.CreateInstance(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.ID != iid {
		t.Errorf("ID = %q, want %q", inst.ID, iid)
	}
	if !fake.runInstancesCalled {
		t.Error("RunInstances was not called")
	}
}

func TestComputeManager_CreateInstance_RunInstancesError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		runInstancesErr: errors.New("run failed"),
	}
	m := newComputeMgr(fake)

	_, err := m.CreateInstance(context.Background(), &cpi.InstanceRequest{
		Image:  "ami-x",
		Flavor: "t3.micro",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestComputeManager_CreateInstance_EmptyRunInstancesResponse(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		runInstancesOut: &ec2.RunInstancesOutput{Instances: []types.Instance{}},
	}
	m := newComputeMgr(fake)

	_, err := m.CreateInstance(context.Background(), &cpi.InstanceRequest{
		Image:  "ami-x",
		Flavor: "t3.micro",
	})
	if err == nil {
		t.Fatal("expected ProviderError, got nil")
	}
	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "InstanceCreationFailed" {
		t.Errorf("expected InstanceCreationFailed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// StartInstance
// ---------------------------------------------------------------------------

func TestComputeManager_StartInstance_HappyPath(t *testing.T) {
	t.Parallel()

	const iid = "i-start"
	fake := &computeFakeEC2{
		startInstancesOut: &ec2.StartInstancesOutput{},
		// Waiter: first DescribeInstances → running.
		describeQueue:    []*ec2.DescribeInstancesOutput{runningReservation(iid)},
		describeQueueErr: []error{nil},
		describeOut:      runningReservation(iid),
	}
	m := newComputeMgr(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := m.StartInstance(ctx, iid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fake.startInstancesCalled {
		t.Error("StartInstances was not called")
	}
}

func TestComputeManager_StartInstance_APIError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{startInstancesErr: errors.New("start failed")}
	m := newComputeMgr(fake)

	if err := m.StartInstance(context.Background(), "i-x"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// StopInstance
// ---------------------------------------------------------------------------

func TestComputeManager_StopInstance_HappyPath(t *testing.T) {
	t.Parallel()

	const iid = "i-stop"
	fake := &computeFakeEC2{
		stopInstancesOut: &ec2.StopInstancesOutput{},
		// Waiter: first DescribeInstances → stopped.
		describeQueue:    []*ec2.DescribeInstancesOutput{stoppedReservation(iid)},
		describeQueueErr: []error{nil},
		describeOut:      stoppedReservation(iid),
	}
	m := newComputeMgr(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := m.StopInstance(ctx, iid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fake.stopInstancesCalled {
		t.Error("StopInstances was not called")
	}
}

func TestComputeManager_StopInstance_APIError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{stopInstancesErr: errors.New("stop failed")}
	m := newComputeMgr(fake)

	if err := m.StopInstance(context.Background(), "i-x"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// RebootInstance — RebootInstances + waiter
// ---------------------------------------------------------------------------

func TestComputeManager_RebootInstance_HappyPath(t *testing.T) {
	t.Parallel()

	const iid = "i-reboot"
	fake := &computeFakeEC2{
		// Waiter: first DescribeInstances → running.
		describeQueue:    []*ec2.DescribeInstancesOutput{runningReservation(iid)},
		describeQueueErr: []error{nil},
		describeOut:      runningReservation(iid),
	}
	m := newComputeMgr(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := m.RebootInstance(ctx, iid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fake.rebootInstancesCalled {
		t.Error("RebootInstances was not called")
	}
}

func TestComputeManager_RebootInstance_APIError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{rebootInstancesErr: errors.New("reboot failed")}
	m := newComputeMgr(fake)

	if err := m.RebootInstance(context.Background(), "i-x"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// DeleteInstance
// ---------------------------------------------------------------------------

func TestComputeManager_DeleteInstance_HappyPath(t *testing.T) {
	t.Parallel()

	const iid = "i-delete"
	fake := &computeFakeEC2{
		terminateInstancesOut: &ec2.TerminateInstancesOutput{},
		// Waiter: first DescribeInstances → terminated.
		describeQueue:    []*ec2.DescribeInstancesOutput{terminatedReservation(iid)},
		describeQueueErr: []error{nil},
		describeOut:      terminatedReservation(iid),
	}
	m := newComputeMgr(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := m.DeleteInstance(ctx, iid); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fake.terminateInstancesCalled {
		t.Error("TerminateInstances was not called")
	}
}

func TestComputeManager_DeleteInstance_APIError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{terminateInstancesErr: errors.New("terminate failed")}
	m := newComputeMgr(fake)

	if err := m.DeleteInstance(context.Background(), "i-x"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// waitForInstanceState — context-cancel path
// ---------------------------------------------------------------------------

func TestComputeManager_waitForInstanceState_ContextCancel(t *testing.T) {
	t.Parallel()

	// Fake always returns pending so waiter never exits on its own.
	fake := &computeFakeEC2{
		describeOut: pendingReservation("i-cancel"),
	}
	m := newComputeMgr(fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := m.waitForInstanceState(ctx, "i-cancel", types.InstanceStateNameRunning, 10*time.Second)
	if err == nil {
		t.Fatal("expected context-cancelled error, got nil")
	}
}

// TestComputeManager_waitForInstanceState_PendingThenRunning verifies the
// waiter exits as soon as DescribeInstances returns the desired state.
// Queue: pending → running. Ticker is 5s so this test needs a 30s timeout
// to allow at least one tick without making the suite slow on CI.
func TestComputeManager_waitForInstanceState_PendingThenRunning(t *testing.T) {
	t.Parallel()

	const iid = "i-wait"
	fake := &computeFakeEC2{
		describeQueue: []*ec2.DescribeInstancesOutput{
			runningReservation(iid), // first tick → running, waiter exits
		},
		describeQueueErr: []error{nil},
		describeOut:      runningReservation(iid),
	}
	m := newComputeMgr(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := m.waitForInstanceState(ctx, iid, types.InstanceStateNameRunning, 10*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CreateKeyPair
// ---------------------------------------------------------------------------

func TestComputeManager_CreateKeyPair_HappyPath(t *testing.T) {
	t.Parallel()

	// GetKeyPair (called first to detect duplicate) must return NotFound.
	// CreateKeyPair then succeeds.
	fake := &computeFakeEC2{
		// GetKeyPair calls DescribeKeyPairs; first call returns empty (not found).
		describeKeyPairsOut: &ec2.DescribeKeyPairsOutput{KeyPairs: []types.KeyPairInfo{}},
		describeKeyPairsErr: nil,
		createKeyPairOut: &ec2.CreateKeyPairOutput{
			KeyPairId:      aws.String("kp-new"),
			KeyName:        aws.String("my-key"),
			KeyFingerprint: aws.String("aa:bb:cc"),
			KeyMaterial:    aws.String("-----BEGIN RSA PRIVATE KEY-----"),
		},
	}
	m := newComputeMgr(fake)

	kp, err := m.CreateKeyPair(context.Background(), &cpi.KeyPairRequest{Name: "my-key"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kp.ID != "kp-new" {
		t.Errorf("ID = %q, want %q", kp.ID, "kp-new")
	}
	if kp.PrivateKey == "" {
		t.Error("PrivateKey should be populated from KeyMaterial")
	}
}

func TestComputeManager_CreateKeyPair_AlreadyExists(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		// GetKeyPair returns a key → duplicate path triggers.
		describeKeyPairsOut: &ec2.DescribeKeyPairsOutput{
			KeyPairs: []types.KeyPairInfo{
				{KeyPairId: aws.String("kp-existing"), KeyName: aws.String("my-key")},
			},
		},
	}
	m := newComputeMgr(fake)

	_, err := m.CreateKeyPair(context.Background(), &cpi.KeyPairRequest{Name: "my-key"})
	if err == nil {
		t.Fatal("expected duplicate error, got nil")
	}
	if !errors.Is(err, ErrDuplicateKeyPair) {
		t.Errorf("expected ErrDuplicateKeyPair, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ImportKeyPair
// ---------------------------------------------------------------------------

func TestComputeManager_ImportKeyPair_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{}
	m := newComputeMgr(fake)

	if err := m.ImportKeyPair(context.Background(), "my-key", "ssh-ed25519 AAAA..."); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fake.importKeyPairCalled {
		t.Error("ImportKeyPair was not called")
	}
}

func TestComputeManager_ImportKeyPair_APIError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{importKeyPairErr: errors.New("import failed")}
	m := newComputeMgr(fake)

	if err := m.ImportKeyPair(context.Background(), "k", "pub"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// GetKeyPair
// ---------------------------------------------------------------------------

func TestComputeManager_GetKeyPair_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		describeKeyPairsOut: &ec2.DescribeKeyPairsOutput{
			KeyPairs: []types.KeyPairInfo{
				{
					KeyPairId:      aws.String("kp-abc"),
					KeyName:        aws.String("prod-key"),
					KeyFingerprint: aws.String("11:22:33"),
				},
			},
		},
	}
	m := newComputeMgr(fake)

	kp, err := m.GetKeyPair(context.Background(), "prod-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kp.ID != "kp-abc" {
		t.Errorf("ID = %q, want %q", kp.ID, "kp-abc")
	}
}

func TestComputeManager_GetKeyPair_NotFound(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		describeKeyPairsOut: &ec2.DescribeKeyPairsOutput{KeyPairs: []types.KeyPairInfo{}},
	}
	m := newComputeMgr(fake)

	_, err := m.GetKeyPair(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "NotFound" {
		t.Errorf("expected NotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListKeyPairs
// ---------------------------------------------------------------------------

func TestComputeManager_ListKeyPairs_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		describeKeyPairsOut: &ec2.DescribeKeyPairsOutput{
			KeyPairs: []types.KeyPairInfo{
				{KeyPairId: aws.String("kp-1"), KeyName: aws.String("k1")},
				{KeyPairId: aws.String("kp-2"), KeyName: aws.String("k2")},
			},
		},
	}
	m := newComputeMgr(fake)

	list, err := m.ListKeyPairs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len = %d, want 2", len(list))
	}
}

func TestComputeManager_ListKeyPairs_APIError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{describeKeyPairsErr: errors.New("describe failed")}
	m := newComputeMgr(fake)

	_, err := m.ListKeyPairs(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// DeleteKeyPair
// ---------------------------------------------------------------------------

func TestComputeManager_DeleteKeyPair_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{}
	m := newComputeMgr(fake)

	if err := m.DeleteKeyPair(context.Background(), "my-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fake.deleteKeyPairCalled {
		t.Error("DeleteKeyPair was not called")
	}
}

func TestComputeManager_DeleteKeyPair_APIError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{deleteKeyPairErr: errors.New("delete failed")}
	m := newComputeMgr(fake)

	if err := m.DeleteKeyPair(context.Background(), "k"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListImages
// ---------------------------------------------------------------------------

func TestComputeManager_ListImages_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		describeImagesOut: &ec2.DescribeImagesOutput{
			Images: []types.Image{
				{ImageId: aws.String("ami-1"), Name: aws.String("img-1"), Architecture: types.ArchitectureValuesX8664, State: types.ImageStateAvailable},
				{ImageId: aws.String("ami-2"), Name: aws.String("img-2"), Architecture: types.ArchitectureValuesX8664, State: types.ImageStateAvailable},
			},
		},
	}
	m := newComputeMgr(fake)

	list, err := m.ListImages(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len = %d, want 2", len(list))
	}
}

func TestComputeManager_ListImages_APIError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{describeImagesErr: errors.New("describe failed")}
	m := newComputeMgr(fake)

	_, err := m.ListImages(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// GetImage
// ---------------------------------------------------------------------------

func TestComputeManager_GetImage_HappyPath(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		describeImagesOut: &ec2.DescribeImagesOutput{
			Images: []types.Image{
				{ImageId: aws.String("ami-abc"), Name: aws.String("test-img"), Architecture: types.ArchitectureValuesX8664, State: types.ImageStateAvailable},
			},
		},
	}
	m := newComputeMgr(fake)

	img, err := m.GetImage(context.Background(), "ami-abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if img.ID != "ami-abc" {
		t.Errorf("ID = %q, want %q", img.ID, "ami-abc")
	}
}

func TestComputeManager_GetImage_NotFound(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		describeImagesOut: &ec2.DescribeImagesOutput{Images: []types.Image{}},
	}
	m := newComputeMgr(fake)

	_, err := m.GetImage(context.Background(), "ami-missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "NotFound" {
		t.Errorf("expected NotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListFlavors
// ---------------------------------------------------------------------------

func TestComputeManager_ListFlavors_HappyPath(t *testing.T) {
	t.Parallel()

	vcpus := int32(2)
	memMiB := int64(4096)
	fake := &computeFakeEC2{
		describeInstanceTypesOut: &ec2.DescribeInstanceTypesOutput{
			InstanceTypes: []types.InstanceTypeInfo{
				{
					InstanceType: types.InstanceTypeT3Micro,
					VCpuInfo:     &types.VCpuInfo{DefaultVCpus: &vcpus},
					MemoryInfo:   &types.MemoryInfo{SizeInMiB: &memMiB},
				},
			},
		},
	}
	m := newComputeMgr(fake)

	list, err := m.ListFlavors(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len = %d, want 1", len(list))
	}
	if list[0].VCPUs != 2 {
		t.Errorf("VCPUs = %d, want 2", list[0].VCPUs)
	}
}

func TestComputeManager_ListFlavors_APIError(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{describeInstanceTypesErr: errors.New("describe failed")}
	m := newComputeMgr(fake)

	_, err := m.ListFlavors(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// GetFlavor
// ---------------------------------------------------------------------------

func TestComputeManager_GetFlavor_HappyPath(t *testing.T) {
	t.Parallel()

	vcpus := int32(4)
	fake := &computeFakeEC2{
		describeInstanceTypesOut: &ec2.DescribeInstanceTypesOutput{
			InstanceTypes: []types.InstanceTypeInfo{
				{
					InstanceType: types.InstanceTypeM5Large,
					VCpuInfo:     &types.VCpuInfo{DefaultVCpus: &vcpus},
				},
			},
		},
	}
	m := newComputeMgr(fake)

	flv, err := m.GetFlavor(context.Background(), string(types.InstanceTypeM5Large))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if flv.ID != string(types.InstanceTypeM5Large) {
		t.Errorf("ID = %q, want %q", flv.ID, string(types.InstanceTypeM5Large))
	}
	if flv.VCPUs != 4 {
		t.Errorf("VCPUs = %d, want 4", flv.VCPUs)
	}
}

func TestComputeManager_GetFlavor_NotFound(t *testing.T) {
	t.Parallel()

	fake := &computeFakeEC2{
		describeInstanceTypesOut: &ec2.DescribeInstanceTypesOutput{InstanceTypes: []types.InstanceTypeInfo{}},
	}
	m := newComputeMgr(fake)

	_, err := m.GetFlavor(context.Background(), "x99.mega")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "NotFound" {
		t.Errorf("expected NotFound, got %v", err)
	}
}
