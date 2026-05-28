package aws

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// ---------------------------------------------------------------------------
// stFakeEC2 — minimal EC2API stub for storage manager tests.
// Unique prefix "stFake" avoids collision with sibling agent stubs.
// ---------------------------------------------------------------------------

//nolint:govet // large stub; field alignment not a concern for test-only types
type stFakeEC2 struct {
	// CreateVolume
	createVolumeOut *ec2.CreateVolumeOutput
	createVolumeErr error

	// DescribeVolumes (used by GetVolume/ListVolumes/waitForVolumeState)
	describeVolumesOut *ec2.DescribeVolumesOutput
	describeVolumesErr error

	// AttachVolume
	attachVolumeOut *ec2.AttachVolumeOutput
	attachVolumeErr error

	// DetachVolume
	detachVolumeOut *ec2.DetachVolumeOutput
	detachVolumeErr error

	// ModifyVolume (ResizeVolume)
	modifyVolumeOut *ec2.ModifyVolumeOutput
	modifyVolumeErr error

	// DeleteVolume
	deleteVolumeOut *ec2.DeleteVolumeOutput
	deleteVolumeErr error

	// CreateSnapshot
	createSnapshotOut *ec2.CreateSnapshotOutput
	createSnapshotErr error

	// DescribeSnapshots
	describeSnapshotsOut *ec2.DescribeSnapshotsOutput
	describeSnapshotsErr error

	// DeleteSnapshot
	deleteSnapshotOut *ec2.DeleteSnapshotOutput
	deleteSnapshotErr error
}

// EC2API method implementations — only the ones called by StorageManager.

func (f *stFakeEC2) CreateVolume(_ context.Context, _ *ec2.CreateVolumeInput, _ ...func(*ec2.Options)) (*ec2.CreateVolumeOutput, error) {
	return f.createVolumeOut, f.createVolumeErr
}

func (f *stFakeEC2) DescribeVolumes(_ context.Context, _ *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	return f.describeVolumesOut, f.describeVolumesErr
}

func (f *stFakeEC2) AttachVolume(_ context.Context, _ *ec2.AttachVolumeInput, _ ...func(*ec2.Options)) (*ec2.AttachVolumeOutput, error) {
	return f.attachVolumeOut, f.attachVolumeErr
}

func (f *stFakeEC2) DetachVolume(_ context.Context, _ *ec2.DetachVolumeInput, _ ...func(*ec2.Options)) (*ec2.DetachVolumeOutput, error) {
	return f.detachVolumeOut, f.detachVolumeErr
}

func (f *stFakeEC2) ModifyVolume(_ context.Context, _ *ec2.ModifyVolumeInput, _ ...func(*ec2.Options)) (*ec2.ModifyVolumeOutput, error) {
	return f.modifyVolumeOut, f.modifyVolumeErr
}

func (f *stFakeEC2) DeleteVolume(_ context.Context, _ *ec2.DeleteVolumeInput, _ ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	return f.deleteVolumeOut, f.deleteVolumeErr
}

func (f *stFakeEC2) CreateSnapshot(_ context.Context, _ *ec2.CreateSnapshotInput, _ ...func(*ec2.Options)) (*ec2.CreateSnapshotOutput, error) {
	return f.createSnapshotOut, f.createSnapshotErr
}

func (f *stFakeEC2) DescribeSnapshots(_ context.Context, _ *ec2.DescribeSnapshotsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error) {
	return f.describeSnapshotsOut, f.describeSnapshotsErr
}

func (f *stFakeEC2) DeleteSnapshot(_ context.Context, _ *ec2.DeleteSnapshotInput, _ ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error) {
	return f.deleteSnapshotOut, f.deleteSnapshotErr
}

// Unused-by-storage EC2API methods — satisfy interface with zero values.

func (f *stFakeEC2) RunInstances(_ context.Context, _ *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DescribeInstances(_ context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) StartInstances(_ context.Context, _ *ec2.StartInstancesInput, _ ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) StopInstances(_ context.Context, _ *ec2.StopInstancesInput, _ ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) RebootInstances(_ context.Context, _ *ec2.RebootInstancesInput, _ ...func(*ec2.Options)) (*ec2.RebootInstancesOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) TerminateInstances(_ context.Context, _ *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) CreateKeyPair(_ context.Context, _ *ec2.CreateKeyPairInput, _ ...func(*ec2.Options)) (*ec2.CreateKeyPairOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) ImportKeyPair(_ context.Context, _ *ec2.ImportKeyPairInput, _ ...func(*ec2.Options)) (*ec2.ImportKeyPairOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DescribeKeyPairs(_ context.Context, _ *ec2.DescribeKeyPairsInput, _ ...func(*ec2.Options)) (*ec2.DescribeKeyPairsOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DeleteKeyPair(_ context.Context, _ *ec2.DeleteKeyPairInput, _ ...func(*ec2.Options)) (*ec2.DeleteKeyPairOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DescribeImages(_ context.Context, _ *ec2.DescribeImagesInput, _ ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DescribeInstanceTypes(_ context.Context, _ *ec2.DescribeInstanceTypesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstanceTypesOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) CreateVpc(_ context.Context, _ *ec2.CreateVpcInput, _ ...func(*ec2.Options)) (*ec2.CreateVpcOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DescribeVpcs(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DeleteVpc(_ context.Context, _ *ec2.DeleteVpcInput, _ ...func(*ec2.Options)) (*ec2.DeleteVpcOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) ModifyVpcAttribute(_ context.Context, _ *ec2.ModifyVpcAttributeInput, _ ...func(*ec2.Options)) (*ec2.ModifyVpcAttributeOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) CreateSubnet(_ context.Context, _ *ec2.CreateSubnetInput, _ ...func(*ec2.Options)) (*ec2.CreateSubnetOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DescribeSubnets(_ context.Context, _ *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DeleteSubnet(_ context.Context, _ *ec2.DeleteSubnetInput, _ ...func(*ec2.Options)) (*ec2.DeleteSubnetOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) ModifySubnetAttribute(_ context.Context, _ *ec2.ModifySubnetAttributeInput, _ ...func(*ec2.Options)) (*ec2.ModifySubnetAttributeOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) CreateInternetGateway(_ context.Context, _ *ec2.CreateInternetGatewayInput, _ ...func(*ec2.Options)) (*ec2.CreateInternetGatewayOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DescribeInternetGateways(_ context.Context, _ *ec2.DescribeInternetGatewaysInput, _ ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) AttachInternetGateway(_ context.Context, _ *ec2.AttachInternetGatewayInput, _ ...func(*ec2.Options)) (*ec2.AttachInternetGatewayOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DetachInternetGateway(_ context.Context, _ *ec2.DetachInternetGatewayInput, _ ...func(*ec2.Options)) (*ec2.DetachInternetGatewayOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DeleteInternetGateway(_ context.Context, _ *ec2.DeleteInternetGatewayInput, _ ...func(*ec2.Options)) (*ec2.DeleteInternetGatewayOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) CreateRouteTable(_ context.Context, _ *ec2.CreateRouteTableInput, _ ...func(*ec2.Options)) (*ec2.CreateRouteTableOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DescribeRouteTables(_ context.Context, _ *ec2.DescribeRouteTablesInput, _ ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) CreateRoute(_ context.Context, _ *ec2.CreateRouteInput, _ ...func(*ec2.Options)) (*ec2.CreateRouteOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) AssociateRouteTable(_ context.Context, _ *ec2.AssociateRouteTableInput, _ ...func(*ec2.Options)) (*ec2.AssociateRouteTableOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DisassociateRouteTable(_ context.Context, _ *ec2.DisassociateRouteTableInput, _ ...func(*ec2.Options)) (*ec2.DisassociateRouteTableOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DeleteRouteTable(_ context.Context, _ *ec2.DeleteRouteTableInput, _ ...func(*ec2.Options)) (*ec2.DeleteRouteTableOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DescribeNetworkInterfaces(_ context.Context, _ *ec2.DescribeNetworkInterfacesInput, _ ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) AllocateAddress(_ context.Context, _ *ec2.AllocateAddressInput, _ ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DescribeAddresses(_ context.Context, _ *ec2.DescribeAddressesInput, _ ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) AssociateAddress(_ context.Context, _ *ec2.AssociateAddressInput, _ ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DisassociateAddress(_ context.Context, _ *ec2.DisassociateAddressInput, _ ...func(*ec2.Options)) (*ec2.DisassociateAddressOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) ReleaseAddress(_ context.Context, _ *ec2.ReleaseAddressInput, _ ...func(*ec2.Options)) (*ec2.ReleaseAddressOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) CreateSecurityGroup(_ context.Context, _ *ec2.CreateSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DescribeSecurityGroups(_ context.Context, _ *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) DeleteSecurityGroup(_ context.Context, _ *ec2.DeleteSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) AuthorizeSecurityGroupIngress(_ context.Context, _ *ec2.AuthorizeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupIngressOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) AuthorizeSecurityGroupEgress(_ context.Context, _ *ec2.AuthorizeSecurityGroupEgressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) RevokeSecurityGroupIngress(_ context.Context, _ *ec2.RevokeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error) {
	return nil, nil
}

func (f *stFakeEC2) RevokeSecurityGroupEgress(_ context.Context, _ *ec2.RevokeSecurityGroupEgressInput, _ ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupEgressOutput, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// stFakeS3 — minimal S3API stub for storage manager tests.
// ---------------------------------------------------------------------------

type stFakeS3 struct {
	createBucketOut *s3.CreateBucketOutput
	createBucketErr error

	headBucketOut *s3.HeadBucketOutput
	headBucketErr error

	getBucketLocationOut *s3.GetBucketLocationOutput
	getBucketLocationErr error

	getBucketTaggingOut *s3.GetBucketTaggingOutput
	getBucketTaggingErr error

	putBucketTaggingOut *s3.PutBucketTaggingOutput
	putBucketTaggingErr error

	getBucketVersioningOut *s3.GetBucketVersioningOutput
	getBucketVersioningErr error

	getBucketEncryptionOut *s3.GetBucketEncryptionOutput
	getBucketEncryptionErr error

	listBucketsOut *s3.ListBucketsOutput
	listBucketsErr error

	deleteBucketOut *s3.DeleteBucketOutput
	deleteBucketErr error

	// listObjectsV2 may be called multiple times (paginator loop).
	// Each call pops the next response from the queue; last entry is reused.
	listObjectsV2Responses []*s3.ListObjectsV2Output
	listObjectsV2Err       error
	listObjectsV2CallCount int

	listObjectVersionsOut *s3.ListObjectVersionsOutput
	listObjectVersionsErr error

	deleteObjectsOut *s3.DeleteObjectsOutput
	deleteObjectsErr error
}

func (f *stFakeS3) CreateBucket(_ context.Context, _ *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	return f.createBucketOut, f.createBucketErr
}

func (f *stFakeS3) HeadBucket(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return f.headBucketOut, f.headBucketErr
}

func (f *stFakeS3) GetBucketLocation(_ context.Context, _ *s3.GetBucketLocationInput, _ ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error) {
	return f.getBucketLocationOut, f.getBucketLocationErr
}

func (f *stFakeS3) GetBucketTagging(_ context.Context, _ *s3.GetBucketTaggingInput, _ ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error) {
	return f.getBucketTaggingOut, f.getBucketTaggingErr
}

func (f *stFakeS3) PutBucketTagging(_ context.Context, _ *s3.PutBucketTaggingInput, _ ...func(*s3.Options)) (*s3.PutBucketTaggingOutput, error) {
	return f.putBucketTaggingOut, f.putBucketTaggingErr
}

func (f *stFakeS3) GetBucketVersioning(_ context.Context, _ *s3.GetBucketVersioningInput, _ ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error) {
	return f.getBucketVersioningOut, f.getBucketVersioningErr
}

func (f *stFakeS3) GetBucketEncryption(_ context.Context, _ *s3.GetBucketEncryptionInput, _ ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error) {
	return f.getBucketEncryptionOut, f.getBucketEncryptionErr
}

func (f *stFakeS3) ListBuckets(_ context.Context, _ *s3.ListBucketsInput, _ ...func(*s3.Options)) (*s3.ListBucketsOutput, error) {
	return f.listBucketsOut, f.listBucketsErr
}

func (f *stFakeS3) DeleteBucket(_ context.Context, _ *s3.DeleteBucketInput, _ ...func(*s3.Options)) (*s3.DeleteBucketOutput, error) {
	return f.deleteBucketOut, f.deleteBucketErr
}

func (f *stFakeS3) ListObjectsV2(_ context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if f.listObjectsV2Err != nil {
		return nil, f.listObjectsV2Err
	}

	if len(f.listObjectsV2Responses) == 0 {
		return &s3.ListObjectsV2Output{}, nil
	}

	idx := f.listObjectsV2CallCount
	if idx >= len(f.listObjectsV2Responses) {
		idx = len(f.listObjectsV2Responses) - 1
	}

	f.listObjectsV2CallCount++

	return f.listObjectsV2Responses[idx], nil
}

func (f *stFakeS3) ListObjectVersions(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	return f.listObjectVersionsOut, f.listObjectVersionsErr
}

func (f *stFakeS3) DeleteObjects(_ context.Context, _ *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	return f.deleteObjectsOut, f.deleteObjectsErr
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// stNewStorageManager builds a StorageManager with fake clients injected.
// client is a minimal *Client with only config set (no real AWS calls).
func stNewStorageManager(fakeEC2 *stFakeEC2, fakeS3 *stFakeS3) *StorageManager {
	cfg := &Config{Region: "us-east-1"}
	c := &Client{config: cfg}

	return &StorageManager{
		client: c,
		ec2:    fakeEC2,
		s3:     fakeS3,
	}
}

// stNewStorageManagerWithRegion builds a StorageManager configured for a non-us-east-1 region.
func stNewStorageManagerWithRegion(region string, fakeEC2 *stFakeEC2, fakeS3 *stFakeS3) *StorageManager {
	cfg := &Config{Region: region}
	c := &Client{config: cfg}

	return &StorageManager{
		client: c,
		ec2:    fakeEC2,
		s3:     fakeS3,
	}
}

// stNewFastPollStorageManager builds a StorageManager with a 1ms poll interval
// so waitForVolumeState returns almost immediately in tests. Race-safe because
// pollInterval is per-instance, not a global var.
func stNewFastPollStorageManager(fakeEC2 *stFakeEC2, fakeS3 *stFakeS3) *StorageManager {
	m := stNewStorageManager(fakeEC2, fakeS3)
	m.pollInterval = 1 * time.Millisecond

	return m
}

// stSetFastPoll is kept for backward compatibility with call sites that
// already use it. It sets the per-instance poll interval via the helper.
// Deprecated: prefer stNewFastPollStorageManager at construction time.
func stSetFastPoll(_ *testing.T) {
	// no-op: fast poll now done at construction; this shim satisfies
	// existing call sites without introducing a global var race.
}

// ---------------------------------------------------------------------------
// EBS — CreateVolume
// ---------------------------------------------------------------------------

func TestStorageManager_CreateVolume_HappyPath(t *testing.T) {
	t.Parallel()

	volID := "vol-abc123"
	fakeEC2 := &stFakeEC2{
		createVolumeOut: &ec2.CreateVolumeOutput{
			VolumeId:         aws.String(volID),
			AvailabilityZone: aws.String("us-east-1a"),
			Size:             aws.Int32(20),
			State:            ec2types.VolumeStateAvailable,
			VolumeType:       ec2types.VolumeTypeGp3,
			Encrypted:        aws.Bool(false),
		},
		// DescribeVolumes returns Available on first call (waitForVolumeState).
		describeVolumesOut: &ec2.DescribeVolumesOutput{
			Volumes: []ec2types.Volume{
				{
					VolumeId:         aws.String(volID),
					AvailabilityZone: aws.String("us-east-1a"),
					Size:             aws.Int32(20),
					State:            ec2types.VolumeStateAvailable,
					VolumeType:       ec2types.VolumeTypeGp3,
				},
			},
		},
	}

	mgr := stNewFastPollStorageManager(fakeEC2, nil)

	vol, err := mgr.CreateVolume(context.Background(), &cpi.VolumeRequest{
		Name:             "test-vol",
		SizeGB:           20,
		AvailabilityZone: "us-east-1a",
	})

	if err != nil {
		t.Fatalf("CreateVolume error: %v", err)
	}

	if vol.ID != volID {
		t.Errorf("vol.ID = %q, want %q", vol.ID, volID)
	}
}

func TestStorageManager_CreateVolume_MissingAZ(t *testing.T) {
	t.Parallel()

	mgr := stNewStorageManager(&stFakeEC2{}, nil)

	_, err := mgr.CreateVolume(context.Background(), &cpi.VolumeRequest{
		Name:   "no-az",
		SizeGB: 10,
	})

	if err == nil {
		t.Fatal("expected error for missing AvailabilityZone")
	}

	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "InvalidParameter" {
		t.Errorf("expected ProviderError InvalidParameter, got %v", err)
	}
}

func TestStorageManager_CreateVolume_APIError(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		createVolumeErr: fmt.Errorf("simulated create error"),
	}

	mgr := stNewStorageManager(fakeEC2, nil)

	_, err := mgr.CreateVolume(context.Background(), &cpi.VolumeRequest{
		Name:             "fail-vol",
		SizeGB:           10,
		AvailabilityZone: "us-east-1a",
	})

	if err == nil {
		t.Fatal("expected error from CreateVolume API failure")
	}
}

// ---------------------------------------------------------------------------
// EBS — GetVolume
// ---------------------------------------------------------------------------

func TestStorageManager_GetVolume_HappyPath(t *testing.T) {
	t.Parallel()

	volID := "vol-get123"
	fakeEC2 := &stFakeEC2{
		describeVolumesOut: &ec2.DescribeVolumesOutput{
			Volumes: []ec2types.Volume{
				{
					VolumeId:   aws.String(volID),
					Size:       aws.Int32(10),
					State:      ec2types.VolumeStateAvailable,
					VolumeType: ec2types.VolumeTypeGp2,
				},
			},
		},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	vol, err := mgr.GetVolume(context.Background(), volID)

	if err != nil {
		t.Fatalf("GetVolume error: %v", err)
	}

	if vol.ID != volID {
		t.Errorf("vol.ID = %q, want %q", vol.ID, volID)
	}
}

func TestStorageManager_GetVolume_NotFound(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		describeVolumesOut: &ec2.DescribeVolumesOutput{Volumes: []ec2types.Volume{}},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	_, err := mgr.GetVolume(context.Background(), "vol-missing")

	if err == nil {
		t.Fatal("expected error for missing volume")
	}

	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "NotFound" {
		t.Errorf("expected ProviderError NotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// EBS — ListVolumes
// ---------------------------------------------------------------------------

func TestStorageManager_ListVolumes_HappyPath(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		describeVolumesOut: &ec2.DescribeVolumesOutput{
			Volumes: []ec2types.Volume{
				{VolumeId: aws.String("vol-1"), Size: aws.Int32(10), State: ec2types.VolumeStateAvailable},
				{VolumeId: aws.String("vol-2"), Size: aws.Int32(20), State: ec2types.VolumeStateInUse},
			},
		},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	vols, err := mgr.ListVolumes(context.Background(), nil)

	if err != nil {
		t.Fatalf("ListVolumes error: %v", err)
	}

	if len(vols) != 2 {
		t.Errorf("len(vols) = %d, want 2", len(vols))
	}
}

func TestStorageManager_ListVolumes_APIError(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		describeVolumesErr: fmt.Errorf("simulated list error"),
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	_, err := mgr.ListVolumes(context.Background(), nil)

	if err == nil {
		t.Fatal("expected error from ListVolumes API failure")
	}
}

// ---------------------------------------------------------------------------
// EBS — AttachVolume
// ---------------------------------------------------------------------------

func TestStorageManager_AttachVolume_HappyPath(t *testing.T) {
	t.Parallel()

	volID := "vol-attach"
	fakeEC2 := &stFakeEC2{
		attachVolumeOut: &ec2.AttachVolumeOutput{},
		// waitForVolumeState calls DescribeVolumes and expects InUse.
		describeVolumesOut: &ec2.DescribeVolumesOutput{
			Volumes: []ec2types.Volume{
				{
					VolumeId: aws.String(volID),
					State:    ec2types.VolumeStateInUse,
				},
			},
		},
	}

	mgr := stNewFastPollStorageManager(fakeEC2, nil)
	err := mgr.AttachVolume(context.Background(), volID, "i-instance", "/dev/sdf")

	if err != nil {
		t.Fatalf("AttachVolume error: %v", err)
	}
}

func TestStorageManager_AttachVolume_APIError(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		attachVolumeErr: fmt.Errorf("simulated attach error"),
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	err := mgr.AttachVolume(context.Background(), "vol-x", "i-x", "")

	if err == nil {
		t.Fatal("expected error from AttachVolume API failure")
	}
}

// ---------------------------------------------------------------------------
// EBS — DetachVolume
// ---------------------------------------------------------------------------

func TestStorageManager_DetachVolume_HappyPath(t *testing.T) {
	t.Parallel()

	volID := "vol-detach"
	fakeEC2 := &stFakeEC2{
		detachVolumeOut: &ec2.DetachVolumeOutput{},
		describeVolumesOut: &ec2.DescribeVolumesOutput{
			Volumes: []ec2types.Volume{
				{VolumeId: aws.String(volID), State: ec2types.VolumeStateAvailable},
			},
		},
	}

	mgr := stNewFastPollStorageManager(fakeEC2, nil)
	err := mgr.DetachVolume(context.Background(), volID, "i-instance")

	if err != nil {
		t.Fatalf("DetachVolume error: %v", err)
	}
}

func TestStorageManager_DetachVolume_APIError(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		detachVolumeErr: fmt.Errorf("simulated detach error"),
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	err := mgr.DetachVolume(context.Background(), "vol-x", "i-x")

	if err == nil {
		t.Fatal("expected error from DetachVolume API failure")
	}
}

// ---------------------------------------------------------------------------
// EBS — ResizeVolume
// ---------------------------------------------------------------------------

func TestStorageManager_ResizeVolume_HappyPath(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		modifyVolumeOut: &ec2.ModifyVolumeOutput{},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	err := mgr.ResizeVolume(context.Background(), "vol-resize", 50)

	if err != nil {
		t.Fatalf("ResizeVolume error: %v", err)
	}
}

func TestStorageManager_ResizeVolume_APIError(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		modifyVolumeErr: fmt.Errorf("simulated modify error"),
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	err := mgr.ResizeVolume(context.Background(), "vol-x", 100)

	if err == nil {
		t.Fatal("expected error from ResizeVolume API failure")
	}
}

// ---------------------------------------------------------------------------
// EBS — DeleteVolume
// ---------------------------------------------------------------------------

func TestStorageManager_DeleteVolume_HappyPath(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		deleteVolumeOut: &ec2.DeleteVolumeOutput{},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	err := mgr.DeleteVolume(context.Background(), "vol-delete")

	if err != nil {
		t.Fatalf("DeleteVolume error: %v", err)
	}
}

func TestStorageManager_DeleteVolume_AlreadyGone(t *testing.T) {
	t.Parallel()

	// InvalidVolume.NotFound should be silently swallowed.
	fakeEC2 := &stFakeEC2{
		deleteVolumeErr: &fakeAPIError{code: "InvalidVolume.NotFound", msg: "volume not found"},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	err := mgr.DeleteVolume(context.Background(), "vol-gone")

	if err != nil {
		t.Fatalf("expected nil for already-deleted volume, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// EBS — CreateSnapshot
// ---------------------------------------------------------------------------

func TestStorageManager_CreateSnapshot_HappyPath(t *testing.T) {
	t.Parallel()

	snapID := "snap-abc"
	fakeEC2 := &stFakeEC2{
		createSnapshotOut: &ec2.CreateSnapshotOutput{
			SnapshotId: aws.String(snapID),
			VolumeId:   aws.String("vol-src"),
			State:      ec2types.SnapshotStateCompleted,
		},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	snap, err := mgr.CreateSnapshot(context.Background(), "vol-src", "my-snap")

	if err != nil {
		t.Fatalf("CreateSnapshot error: %v", err)
	}

	if snap.ID != snapID {
		t.Errorf("snap.ID = %q, want %q", snap.ID, snapID)
	}
}

func TestStorageManager_CreateSnapshot_APIError(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		createSnapshotErr: fmt.Errorf("simulated snapshot error"),
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	_, err := mgr.CreateSnapshot(context.Background(), "vol-x", "fail-snap")

	if err == nil {
		t.Fatal("expected error from CreateSnapshot API failure")
	}
}

// ---------------------------------------------------------------------------
// EBS — GetSnapshot
// ---------------------------------------------------------------------------

func TestStorageManager_GetSnapshot_HappyPath(t *testing.T) {
	t.Parallel()

	snapID := "snap-get"
	fakeEC2 := &stFakeEC2{
		describeSnapshotsOut: &ec2.DescribeSnapshotsOutput{
			Snapshots: []ec2types.Snapshot{
				{SnapshotId: aws.String(snapID), VolumeId: aws.String("vol-1"), State: ec2types.SnapshotStateCompleted},
			},
		},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	snap, err := mgr.GetSnapshot(context.Background(), snapID)

	if err != nil {
		t.Fatalf("GetSnapshot error: %v", err)
	}

	if snap.ID != snapID {
		t.Errorf("snap.ID = %q, want %q", snap.ID, snapID)
	}
}

func TestStorageManager_GetSnapshot_NotFound(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		describeSnapshotsOut: &ec2.DescribeSnapshotsOutput{Snapshots: []ec2types.Snapshot{}},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	_, err := mgr.GetSnapshot(context.Background(), "snap-missing")

	if err == nil {
		t.Fatal("expected error for missing snapshot")
	}

	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "NotFound" {
		t.Errorf("expected ProviderError NotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// EBS — ListSnapshots
// ---------------------------------------------------------------------------

func TestStorageManager_ListSnapshots_HappyPath(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		describeSnapshotsOut: &ec2.DescribeSnapshotsOutput{
			Snapshots: []ec2types.Snapshot{
				{SnapshotId: aws.String("snap-1"), VolumeId: aws.String("vol-1")},
				{SnapshotId: aws.String("snap-2"), VolumeId: aws.String("vol-1")},
			},
		},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	snaps, err := mgr.ListSnapshots(context.Background(), "vol-1", nil)

	if err != nil {
		t.Fatalf("ListSnapshots error: %v", err)
	}

	if len(snaps) != 2 {
		t.Errorf("len(snaps) = %d, want 2", len(snaps))
	}
}

func TestStorageManager_ListSnapshots_APIError(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		describeSnapshotsErr: fmt.Errorf("simulated list snapshots error"),
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	_, err := mgr.ListSnapshots(context.Background(), "", nil)

	if err == nil {
		t.Fatal("expected error from ListSnapshots API failure")
	}
}

// ---------------------------------------------------------------------------
// EBS — DeleteSnapshot
// ---------------------------------------------------------------------------

func TestStorageManager_DeleteSnapshot_HappyPath(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		deleteSnapshotOut: &ec2.DeleteSnapshotOutput{},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	err := mgr.DeleteSnapshot(context.Background(), "snap-del")

	if err != nil {
		t.Fatalf("DeleteSnapshot error: %v", err)
	}
}

func TestStorageManager_DeleteSnapshot_AlreadyGone(t *testing.T) {
	t.Parallel()

	fakeEC2 := &stFakeEC2{
		deleteSnapshotErr: &fakeAPIError{code: "InvalidSnapshot.NotFound", msg: "snapshot not found"},
	}

	mgr := stNewStorageManager(fakeEC2, nil)
	err := mgr.DeleteSnapshot(context.Background(), "snap-gone")

	if err != nil {
		t.Fatalf("expected nil for already-deleted snapshot, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// S3 — CreateBucket
// ---------------------------------------------------------------------------

func TestStorageManager_CreateBucket_HappyPath(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		createBucketOut:        &s3.CreateBucketOutput{},
		headBucketOut:          &s3.HeadBucketOutput{},
		getBucketLocationOut:   &s3.GetBucketLocationOutput{},
		getBucketTaggingErr:    fmt.Errorf("no tags"),
		getBucketVersioningOut: &s3.GetBucketVersioningOutput{},
		getBucketEncryptionErr: fmt.Errorf("no encryption"),
	}

	mgr := stNewStorageManager(nil, fakeS3)
	bucket, err := mgr.CreateBucket(context.Background(), &cpi.BucketRequest{Name: "my-bucket"})

	if err != nil {
		t.Fatalf("CreateBucket error: %v", err)
	}

	if bucket.Name != "my-bucket" {
		t.Errorf("bucket.Name = %q, want %q", bucket.Name, "my-bucket")
	}
}

func TestStorageManager_CreateBucket_WithTags(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		createBucketOut:        &s3.CreateBucketOutput{},
		putBucketTaggingOut:    &s3.PutBucketTaggingOutput{},
		headBucketOut:          &s3.HeadBucketOutput{},
		getBucketLocationOut:   &s3.GetBucketLocationOutput{},
		getBucketTaggingOut:    &s3.GetBucketTaggingOutput{TagSet: []s3types.Tag{{Key: aws.String("env"), Value: aws.String("test")}}},
		getBucketVersioningOut: &s3.GetBucketVersioningOutput{},
		getBucketEncryptionErr: fmt.Errorf("no encryption"),
	}

	mgr := stNewStorageManager(nil, fakeS3)
	bucket, err := mgr.CreateBucket(context.Background(), &cpi.BucketRequest{
		Name: "tagged-bucket",
		Tags: map[string]string{"env": "test"},
	})

	if err != nil {
		t.Fatalf("CreateBucket with tags error: %v", err)
	}

	if bucket.Name != "tagged-bucket" {
		t.Errorf("bucket.Name = %q, want %q", bucket.Name, "tagged-bucket")
	}
}

func TestStorageManager_CreateBucket_NonUSEast1(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		createBucketOut:        &s3.CreateBucketOutput{},
		headBucketOut:          &s3.HeadBucketOutput{},
		getBucketLocationOut:   &s3.GetBucketLocationOutput{LocationConstraint: s3types.BucketLocationConstraintEuWest1},
		getBucketTaggingErr:    fmt.Errorf("no tags"),
		getBucketVersioningOut: &s3.GetBucketVersioningOutput{},
		getBucketEncryptionErr: fmt.Errorf("no encryption"),
	}

	mgr := stNewStorageManagerWithRegion("eu-west-1", nil, fakeS3)
	bucket, err := mgr.CreateBucket(context.Background(), &cpi.BucketRequest{Name: "eu-bucket"})

	if err != nil {
		t.Fatalf("CreateBucket eu-west-1 error: %v", err)
	}

	if bucket.Region != "eu-west-1" {
		t.Errorf("bucket.Region = %q, want eu-west-1", bucket.Region)
	}
}

func TestStorageManager_CreateBucket_APIError(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		createBucketErr: fmt.Errorf("simulated bucket create error"),
	}

	mgr := stNewStorageManager(nil, fakeS3)
	_, err := mgr.CreateBucket(context.Background(), &cpi.BucketRequest{Name: "fail-bucket"})

	if err == nil {
		t.Fatal("expected error from CreateBucket API failure")
	}
}

// ---------------------------------------------------------------------------
// S3 — GetBucket
// ---------------------------------------------------------------------------

func TestStorageManager_GetBucket_HappyPath(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		headBucketOut:          &s3.HeadBucketOutput{},
		getBucketLocationOut:   &s3.GetBucketLocationOutput{LocationConstraint: ""},
		getBucketTaggingOut:    &s3.GetBucketTaggingOutput{TagSet: []s3types.Tag{{Key: aws.String("k"), Value: aws.String("v")}}},
		getBucketVersioningOut: &s3.GetBucketVersioningOutput{Status: s3types.BucketVersioningStatusEnabled},
		getBucketEncryptionOut: &s3.GetBucketEncryptionOutput{},
	}

	mgr := stNewStorageManager(nil, fakeS3)
	bucket, err := mgr.GetBucket(context.Background(), "test-bucket")

	if err != nil {
		t.Fatalf("GetBucket error: %v", err)
	}

	if !bucket.Versioning {
		t.Error("expected versioning to be enabled")
	}

	if !bucket.Encryption {
		t.Error("expected encryption to be detected")
	}

	if bucket.Tags["k"] != "v" {
		t.Errorf("bucket.Tags[k] = %q, want v", bucket.Tags["k"])
	}
}

func TestStorageManager_GetBucket_NotFound(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		headBucketErr: &fakeAPIError{code: "NotFound", msg: "bucket not found"},
	}

	mgr := stNewStorageManager(nil, fakeS3)
	_, err := mgr.GetBucket(context.Background(), "missing-bucket")

	if err == nil {
		t.Fatal("expected error for missing bucket")
	}

	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "NotFound" {
		t.Errorf("expected ProviderError NotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// S3 — ListBuckets
// ---------------------------------------------------------------------------

func TestStorageManager_ListBuckets_HappyPath(t *testing.T) {
	t.Parallel()

	createdAt := time.Now()
	fakeS3 := &stFakeS3{
		listBucketsOut: &s3.ListBucketsOutput{
			Buckets: []s3types.Bucket{
				{Name: aws.String("bucket-a"), CreationDate: &createdAt},
			},
		},
		// GetBucket calls for each bucket:
		headBucketOut:          &s3.HeadBucketOutput{},
		getBucketLocationOut:   &s3.GetBucketLocationOutput{},
		getBucketTaggingErr:    fmt.Errorf("no tags"),
		getBucketVersioningOut: &s3.GetBucketVersioningOutput{},
		getBucketEncryptionErr: fmt.Errorf("no encryption"),
	}

	mgr := stNewStorageManager(nil, fakeS3)
	buckets, err := mgr.ListBuckets(context.Background())

	if err != nil {
		t.Fatalf("ListBuckets error: %v", err)
	}

	if len(buckets) != 1 {
		t.Errorf("len(buckets) = %d, want 1", len(buckets))
	}
}

func TestStorageManager_ListBuckets_APIError(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		listBucketsErr: fmt.Errorf("simulated list error"),
	}

	mgr := stNewStorageManager(nil, fakeS3)
	_, err := mgr.ListBuckets(context.Background())

	if err == nil {
		t.Fatal("expected error from ListBuckets API failure")
	}
}

// ---------------------------------------------------------------------------
// S3 — DeleteBucket
// ---------------------------------------------------------------------------

func TestStorageManager_DeleteBucket_HappyPath(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		deleteBucketOut: &s3.DeleteBucketOutput{},
	}

	mgr := stNewStorageManager(nil, fakeS3)
	err := mgr.DeleteBucket(context.Background(), "del-bucket")

	if err != nil {
		t.Fatalf("DeleteBucket error: %v", err)
	}
}

func TestStorageManager_DeleteBucket_AlreadyGone(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		deleteBucketErr: &fakeAPIError{code: "NoSuchBucket", msg: "no such bucket"},
	}

	mgr := stNewStorageManager(nil, fakeS3)
	err := mgr.DeleteBucket(context.Background(), "gone-bucket")

	if err != nil {
		t.Fatalf("expected nil for already-deleted bucket, got %v", err)
	}
}

func TestStorageManager_DeleteBucket_APIError(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		deleteBucketErr: fmt.Errorf("simulated delete error"),
	}

	mgr := stNewStorageManager(nil, fakeS3)
	err := mgr.DeleteBucket(context.Background(), "fail-bucket")

	if err == nil {
		t.Fatal("expected error from DeleteBucket API failure")
	}
}

// ---------------------------------------------------------------------------
// S3 — IsBucketEmpty
// ---------------------------------------------------------------------------

func TestStorageManager_IsBucketEmpty_Empty(t *testing.T) {
	t.Parallel()

	zero := int32(0)
	fakeS3 := &stFakeS3{
		listObjectsV2Responses: []*s3.ListObjectsV2Output{
			{KeyCount: &zero},
		},
		listObjectVersionsOut: &s3.ListObjectVersionsOutput{},
	}

	mgr := stNewStorageManager(nil, fakeS3)
	empty, err := mgr.IsBucketEmpty(context.Background(), "empty-bucket")

	if err != nil {
		t.Fatalf("IsBucketEmpty error: %v", err)
	}

	if !empty {
		t.Error("expected bucket to be empty")
	}
}

func TestStorageManager_IsBucketEmpty_HasObjects(t *testing.T) {
	t.Parallel()

	one := int32(1)
	fakeS3 := &stFakeS3{
		listObjectsV2Responses: []*s3.ListObjectsV2Output{
			{KeyCount: &one},
		},
	}

	mgr := stNewStorageManager(nil, fakeS3)
	empty, err := mgr.IsBucketEmpty(context.Background(), "full-bucket")

	if err != nil {
		t.Fatalf("IsBucketEmpty error: %v", err)
	}

	if empty {
		t.Error("expected bucket to not be empty")
	}
}

func TestStorageManager_IsBucketEmpty_APIError(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		listObjectsV2Err: fmt.Errorf("simulated list objects error"),
	}

	mgr := stNewStorageManager(nil, fakeS3)
	_, err := mgr.IsBucketEmpty(context.Background(), "error-bucket")

	if err == nil {
		t.Fatal("expected error from IsBucketEmpty API failure")
	}
}

// ---------------------------------------------------------------------------
// S3 — EmptyBucket
// ---------------------------------------------------------------------------

func TestStorageManager_EmptyBucket_HappyPath(t *testing.T) {
	t.Parallel()

	// One page with two objects; pagination stops because NextContinuationToken is nil.
	fakeS3 := &stFakeS3{
		listObjectsV2Responses: []*s3.ListObjectsV2Output{
			{
				Contents: []s3types.Object{
					{Key: aws.String("obj-1")},
					{Key: aws.String("obj-2")},
				},
				// NextContinuationToken nil → only one page
			},
		},
		deleteObjectsOut: &s3.DeleteObjectsOutput{},
		// ListObjectVersions returns empty (no versions).
		listObjectVersionsOut: &s3.ListObjectVersionsOutput{},
	}

	mgr := stNewStorageManager(nil, fakeS3)
	err := mgr.EmptyBucket(context.Background(), "bucket-to-empty")

	if err != nil {
		t.Fatalf("EmptyBucket error: %v", err)
	}
}

func TestStorageManager_EmptyBucket_ListObjectsError(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		listObjectsV2Err: fmt.Errorf("simulated list error"),
	}

	mgr := stNewStorageManager(nil, fakeS3)
	err := mgr.EmptyBucket(context.Background(), "error-bucket")

	if err == nil {
		t.Fatal("expected error from EmptyBucket list failure")
	}
}

func TestStorageManager_EmptyBucket_DeleteObjectsError(t *testing.T) {
	t.Parallel()

	fakeS3 := &stFakeS3{
		listObjectsV2Responses: []*s3.ListObjectsV2Output{
			{Contents: []s3types.Object{{Key: aws.String("obj-x")}}},
		},
		deleteObjectsErr: fmt.Errorf("simulated delete error"),
	}

	mgr := stNewStorageManager(nil, fakeS3)
	err := mgr.EmptyBucket(context.Background(), "del-error-bucket")

	if err == nil {
		t.Fatal("expected error from EmptyBucket delete failure")
	}
}

// ---------------------------------------------------------------------------
// S3 — CreateCredentialsGroup (always NotImplemented)
// ---------------------------------------------------------------------------

func TestStorageManager_CreateCredentialsGroup_NotImplemented(t *testing.T) {
	t.Parallel()

	mgr := stNewStorageManager(nil, nil)
	_, err := mgr.CreateCredentialsGroup(context.Background(), &cpi.CredentialsGroupRequest{Name: "creds"})

	if err == nil {
		t.Fatal("expected error from CreateCredentialsGroup")
	}

	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "NotImplemented" {
		t.Errorf("expected ProviderError NotImplemented, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// fakeAPIError — minimal smithy.APIError implementation for testing.
// ---------------------------------------------------------------------------

type fakeAPIError struct {
	code string
	msg  string
}

func (e *fakeAPIError) Error() string                { return fmt.Sprintf("%s: %s", e.code, e.msg) }
func (e *fakeAPIError) ErrorCode() string            { return e.code }
func (e *fakeAPIError) ErrorMessage() string         { return e.msg }
func (e *fakeAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }
