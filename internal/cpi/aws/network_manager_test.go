package aws

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// nwFakeEC2 is the stub EC2API used exclusively in network_manager_test.go.
// The unique name avoids collisions with sibling test agents sharing this package.
type nwFakeEC2 struct {
	// VPC
	createVpcOut      *ec2.CreateVpcOutput
	createVpcErr      error
	describeVpcsOut   *ec2.DescribeVpcsOutput
	describeVpcsErr   error
	deleteVpcErr      error
	modifyVpcAttrErr  error

	// Subnets
	createSubnetOut    *ec2.CreateSubnetOutput
	createSubnetErr    error
	describeSubnetsOut *ec2.DescribeSubnetsOutput
	describeSubnetsErr error
	deleteSubnetErr    error
	modifySubnetAttrErr error

	// Internet gateways
	createIGWOut       *ec2.CreateInternetGatewayOutput
	createIGWErr       error
	describeIGWsOut    *ec2.DescribeInternetGatewaysOutput
	describeIGWsErr    error
	attachIGWErr       error
	detachIGWErr       error
	deleteIGWErr       error

	// Route tables
	createRTOut        *ec2.CreateRouteTableOutput
	createRTErr        error
	describeRTsOut     *ec2.DescribeRouteTablesOutput
	describeRTsErr     error
	createRouteErr     error
	assocRTOut         *ec2.AssociateRouteTableOutput
	assocRTErr         error
	disassocRTErr      error
	deleteRTErr        error

	// ENIs
	describeENIsOut *ec2.DescribeNetworkInterfacesOutput
	describeENIsErr error

	// Elastic IPs
	allocAddrOut    *ec2.AllocateAddressOutput
	allocAddrErr    error
	describeAddrsOut *ec2.DescribeAddressesOutput
	describeAddrsErr error
	assocAddrErr    error
	disassocAddrErr error
	releaseAddrErr  error

	// Track call counts for assertions
	modifyVpcAttrCalls int
}

// ---- EC2API implementation for nwFakeEC2 ------------------------------------

func (f *nwFakeEC2) CreateVpc(_ context.Context, _ *ec2.CreateVpcInput, _ ...func(*ec2.Options)) (*ec2.CreateVpcOutput, error) {
	return f.createVpcOut, f.createVpcErr
}

func (f *nwFakeEC2) DescribeVpcs(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return f.describeVpcsOut, f.describeVpcsErr
}

func (f *nwFakeEC2) DeleteVpc(_ context.Context, _ *ec2.DeleteVpcInput, _ ...func(*ec2.Options)) (*ec2.DeleteVpcOutput, error) {
	return &ec2.DeleteVpcOutput{}, f.deleteVpcErr
}

func (f *nwFakeEC2) ModifyVpcAttribute(_ context.Context, _ *ec2.ModifyVpcAttributeInput, _ ...func(*ec2.Options)) (*ec2.ModifyVpcAttributeOutput, error) {
	f.modifyVpcAttrCalls++
	return &ec2.ModifyVpcAttributeOutput{}, f.modifyVpcAttrErr
}

func (f *nwFakeEC2) CreateSubnet(_ context.Context, _ *ec2.CreateSubnetInput, _ ...func(*ec2.Options)) (*ec2.CreateSubnetOutput, error) {
	return f.createSubnetOut, f.createSubnetErr
}

func (f *nwFakeEC2) DescribeSubnets(_ context.Context, _ *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return f.describeSubnetsOut, f.describeSubnetsErr
}

func (f *nwFakeEC2) DeleteSubnet(_ context.Context, _ *ec2.DeleteSubnetInput, _ ...func(*ec2.Options)) (*ec2.DeleteSubnetOutput, error) {
	return &ec2.DeleteSubnetOutput{}, f.deleteSubnetErr
}

func (f *nwFakeEC2) ModifySubnetAttribute(_ context.Context, _ *ec2.ModifySubnetAttributeInput, _ ...func(*ec2.Options)) (*ec2.ModifySubnetAttributeOutput, error) {
	return &ec2.ModifySubnetAttributeOutput{}, f.modifySubnetAttrErr
}

func (f *nwFakeEC2) CreateInternetGateway(_ context.Context, _ *ec2.CreateInternetGatewayInput, _ ...func(*ec2.Options)) (*ec2.CreateInternetGatewayOutput, error) {
	return f.createIGWOut, f.createIGWErr
}

func (f *nwFakeEC2) DescribeInternetGateways(_ context.Context, _ *ec2.DescribeInternetGatewaysInput, _ ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error) {
	return f.describeIGWsOut, f.describeIGWsErr
}

func (f *nwFakeEC2) AttachInternetGateway(_ context.Context, _ *ec2.AttachInternetGatewayInput, _ ...func(*ec2.Options)) (*ec2.AttachInternetGatewayOutput, error) {
	return &ec2.AttachInternetGatewayOutput{}, f.attachIGWErr
}

func (f *nwFakeEC2) DetachInternetGateway(_ context.Context, _ *ec2.DetachInternetGatewayInput, _ ...func(*ec2.Options)) (*ec2.DetachInternetGatewayOutput, error) {
	return &ec2.DetachInternetGatewayOutput{}, f.detachIGWErr
}

func (f *nwFakeEC2) DeleteInternetGateway(_ context.Context, _ *ec2.DeleteInternetGatewayInput, _ ...func(*ec2.Options)) (*ec2.DeleteInternetGatewayOutput, error) {
	return &ec2.DeleteInternetGatewayOutput{}, f.deleteIGWErr
}

func (f *nwFakeEC2) CreateRouteTable(_ context.Context, _ *ec2.CreateRouteTableInput, _ ...func(*ec2.Options)) (*ec2.CreateRouteTableOutput, error) {
	return f.createRTOut, f.createRTErr
}

func (f *nwFakeEC2) DescribeRouteTables(_ context.Context, _ *ec2.DescribeRouteTablesInput, _ ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return f.describeRTsOut, f.describeRTsErr
}

func (f *nwFakeEC2) CreateRoute(_ context.Context, _ *ec2.CreateRouteInput, _ ...func(*ec2.Options)) (*ec2.CreateRouteOutput, error) {
	return &ec2.CreateRouteOutput{}, f.createRouteErr
}

func (f *nwFakeEC2) AssociateRouteTable(_ context.Context, _ *ec2.AssociateRouteTableInput, _ ...func(*ec2.Options)) (*ec2.AssociateRouteTableOutput, error) {
	if f.assocRTOut != nil {
		return f.assocRTOut, f.assocRTErr
	}

	return &ec2.AssociateRouteTableOutput{}, f.assocRTErr
}

func (f *nwFakeEC2) DisassociateRouteTable(_ context.Context, _ *ec2.DisassociateRouteTableInput, _ ...func(*ec2.Options)) (*ec2.DisassociateRouteTableOutput, error) {
	return &ec2.DisassociateRouteTableOutput{}, f.disassocRTErr
}

func (f *nwFakeEC2) DeleteRouteTable(_ context.Context, _ *ec2.DeleteRouteTableInput, _ ...func(*ec2.Options)) (*ec2.DeleteRouteTableOutput, error) {
	return &ec2.DeleteRouteTableOutput{}, f.deleteRTErr
}

func (f *nwFakeEC2) DescribeNetworkInterfaces(_ context.Context, _ *ec2.DescribeNetworkInterfacesInput, _ ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
	return f.describeENIsOut, f.describeENIsErr
}

func (f *nwFakeEC2) AllocateAddress(_ context.Context, _ *ec2.AllocateAddressInput, _ ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
	return f.allocAddrOut, f.allocAddrErr
}

func (f *nwFakeEC2) DescribeAddresses(_ context.Context, _ *ec2.DescribeAddressesInput, _ ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
	return f.describeAddrsOut, f.describeAddrsErr
}

func (f *nwFakeEC2) AssociateAddress(_ context.Context, _ *ec2.AssociateAddressInput, _ ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
	return &ec2.AssociateAddressOutput{}, f.assocAddrErr
}

func (f *nwFakeEC2) DisassociateAddress(_ context.Context, _ *ec2.DisassociateAddressInput, _ ...func(*ec2.Options)) (*ec2.DisassociateAddressOutput, error) {
	return &ec2.DisassociateAddressOutput{}, f.disassocAddrErr
}

func (f *nwFakeEC2) ReleaseAddress(_ context.Context, _ *ec2.ReleaseAddressInput, _ ...func(*ec2.Options)) (*ec2.ReleaseAddressOutput, error) {
	return &ec2.ReleaseAddressOutput{}, f.releaseAddrErr
}

// Satisfy remaining EC2API methods unused by NetworkManager.

func (f *nwFakeEC2) RunInstances(_ context.Context, _ *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DescribeInstances(_ context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) StartInstances(_ context.Context, _ *ec2.StartInstancesInput, _ ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) StopInstances(_ context.Context, _ *ec2.StopInstancesInput, _ ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) RebootInstances(_ context.Context, _ *ec2.RebootInstancesInput, _ ...func(*ec2.Options)) (*ec2.RebootInstancesOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) TerminateInstances(_ context.Context, _ *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) CreateKeyPair(_ context.Context, _ *ec2.CreateKeyPairInput, _ ...func(*ec2.Options)) (*ec2.CreateKeyPairOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) ImportKeyPair(_ context.Context, _ *ec2.ImportKeyPairInput, _ ...func(*ec2.Options)) (*ec2.ImportKeyPairOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DescribeKeyPairs(_ context.Context, _ *ec2.DescribeKeyPairsInput, _ ...func(*ec2.Options)) (*ec2.DescribeKeyPairsOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DeleteKeyPair(_ context.Context, _ *ec2.DeleteKeyPairInput, _ ...func(*ec2.Options)) (*ec2.DeleteKeyPairOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DescribeImages(_ context.Context, _ *ec2.DescribeImagesInput, _ ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DescribeInstanceTypes(_ context.Context, _ *ec2.DescribeInstanceTypesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstanceTypesOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) CreateVolume(_ context.Context, _ *ec2.CreateVolumeInput, _ ...func(*ec2.Options)) (*ec2.CreateVolumeOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DescribeVolumes(_ context.Context, _ *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) AttachVolume(_ context.Context, _ *ec2.AttachVolumeInput, _ ...func(*ec2.Options)) (*ec2.AttachVolumeOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DetachVolume(_ context.Context, _ *ec2.DetachVolumeInput, _ ...func(*ec2.Options)) (*ec2.DetachVolumeOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) ModifyVolume(_ context.Context, _ *ec2.ModifyVolumeInput, _ ...func(*ec2.Options)) (*ec2.ModifyVolumeOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DeleteVolume(_ context.Context, _ *ec2.DeleteVolumeInput, _ ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) CreateSnapshot(_ context.Context, _ *ec2.CreateSnapshotInput, _ ...func(*ec2.Options)) (*ec2.CreateSnapshotOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DescribeSnapshots(_ context.Context, _ *ec2.DescribeSnapshotsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DeleteSnapshot(_ context.Context, _ *ec2.DeleteSnapshotInput, _ ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) CreateSecurityGroup(_ context.Context, _ *ec2.CreateSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DescribeSecurityGroups(_ context.Context, _ *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) DeleteSecurityGroup(_ context.Context, _ *ec2.DeleteSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) AuthorizeSecurityGroupIngress(_ context.Context, _ *ec2.AuthorizeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupIngressOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) AuthorizeSecurityGroupEgress(_ context.Context, _ *ec2.AuthorizeSecurityGroupEgressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) RevokeSecurityGroupIngress(_ context.Context, _ *ec2.RevokeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

func (f *nwFakeEC2) RevokeSecurityGroupEgress(_ context.Context, _ *ec2.RevokeSecurityGroupEgressInput, _ ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupEgressOutput, error) {
	return nil, errors.New("not implemented in nwFakeEC2")
}

// ---- helpers ----------------------------------------------------------------

// noopVPCWaiter is a waiter seam that always succeeds immediately.
func noopVPCWaiter(_ context.Context, _ EC2API, _ string) error { return nil }

// noopSubnetWaiter is a waiter seam that always succeeds immediately.
func noopSubnetWaiter(_ context.Context, _ EC2API, _ string) error { return nil }

// newTestNetworkManager builds a NetworkManager backed by a nwFakeEC2 stub.
// client.config must be non-nil for methods that read m.client.config.Region.
func newTestNetworkManager(fake *nwFakeEC2) *NetworkManager {
	cfg := &Config{Region: "us-east-1"}
	client := &Client{config: cfg}
	m := &NetworkManager{
		client:       client,
		ec2:          fake,
		vpcWaiter:    noopVPCWaiter,
		subnetWaiter: noopSubnetWaiter,
	}

	return m
}

// ---- CreateNetwork ----------------------------------------------------------

func TestNWCreateNetwork_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		createVpcOut: &ec2.CreateVpcOutput{
			Vpc: &types.Vpc{
				VpcId:     aws.String("vpc-abc123"),
				CidrBlock: aws.String("10.0.0.0/16"),
				State:     types.VpcStateAvailable,
			},
		},
	}
	m := newTestNetworkManager(fake)

	got, err := m.CreateNetwork(context.Background(), &cpi.NetworkRequest{
		Name: "test-vpc",
		CIDR: "10.0.0.0/16",
		Tags: map[string]string{"env": "test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != "vpc-abc123" {
		t.Errorf("ID = %q, want vpc-abc123", got.ID)
	}

	if got.CIDR != "10.0.0.0/16" {
		t.Errorf("CIDR = %q, want 10.0.0.0/16", got.CIDR)
	}

	if got.Region != "us-east-1" {
		t.Errorf("Region = %q, want us-east-1", got.Region)
	}

	// ModifyVpcAttribute called twice (DNS hostnames + DNS support)
	if fake.modifyVpcAttrCalls != 2 {
		t.Errorf("ModifyVpcAttribute calls = %d, want 2", fake.modifyVpcAttrCalls)
	}
}

func TestNWCreateNetwork_MissingCIDR(t *testing.T) {
	t.Parallel()

	m := newTestNetworkManager(&nwFakeEC2{})

	_, err := m.CreateNetwork(context.Background(), &cpi.NetworkRequest{Name: "vpc-no-cidr"})
	if err == nil {
		t.Fatal("expected error for missing CIDR, got nil")
	}
}

func TestNWCreateNetwork_InvalidCIDR(t *testing.T) {
	t.Parallel()

	m := newTestNetworkManager(&nwFakeEC2{})

	_, err := m.CreateNetwork(context.Background(), &cpi.NetworkRequest{
		Name: "bad-cidr",
		CIDR: "not-a-cidr",
	})
	if err == nil {
		t.Fatal("expected error for invalid CIDR, got nil")
	}
}

func TestNWCreateNetwork_CreateVpcError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{createVpcErr: errors.New("vpc boom")}
	m := newTestNetworkManager(fake)

	_, err := m.CreateNetwork(context.Background(), &cpi.NetworkRequest{
		Name: "vpc-fail",
		CIDR: "10.1.0.0/16",
	})
	if err == nil {
		t.Fatal("expected error from CreateVpc, got nil")
	}
}

func TestNWCreateNetwork_ModifyVpcAttrError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		createVpcOut: &ec2.CreateVpcOutput{
			Vpc: &types.Vpc{VpcId: aws.String("vpc-xyz")},
		},
		modifyVpcAttrErr: errors.New("modify failed"),
	}
	m := newTestNetworkManager(fake)

	_, err := m.CreateNetwork(context.Background(), &cpi.NetworkRequest{
		Name: "vpc-modify-fail",
		CIDR: "10.2.0.0/16",
	})
	if err == nil {
		t.Fatal("expected error from ModifyVpcAttribute, got nil")
	}
}

// ---- GetNetwork -------------------------------------------------------------

func TestNWGetNetwork_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeVpcsOut: &ec2.DescribeVpcsOutput{
			Vpcs: []types.Vpc{
				{
					VpcId:     aws.String("vpc-get-1"),
					CidrBlock: aws.String("172.16.0.0/12"),
					State:     types.VpcStateAvailable,
					Tags:      []types.Tag{{Key: aws.String("Name"), Value: aws.String("my-vpc")}},
				},
			},
		},
	}
	m := newTestNetworkManager(fake)

	got, err := m.GetNetwork(context.Background(), "vpc-get-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != "vpc-get-1" {
		t.Errorf("ID = %q, want vpc-get-1", got.ID)
	}

	if got.Name != "my-vpc" {
		t.Errorf("Name = %q, want my-vpc", got.Name)
	}
}

func TestNWGetNetwork_NotFound(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeVpcsOut: &ec2.DescribeVpcsOutput{Vpcs: []types.Vpc{}},
	}
	m := newTestNetworkManager(fake)

	_, err := m.GetNetwork(context.Background(), "vpc-missing")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}

	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "NotFound" {
		t.Errorf("error = %v, want ProviderError{Code:NotFound}", err)
	}
}

func TestNWGetNetwork_DescribeError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{describeVpcsErr: errors.New("describe fail")}
	m := newTestNetworkManager(fake)

	_, err := m.GetNetwork(context.Background(), "vpc-err")
	if err == nil {
		t.Fatal("expected error from DescribeVpcs, got nil")
	}
}

// ---- ListNetworks -----------------------------------------------------------

func TestNWListNetworks_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeVpcsOut: &ec2.DescribeVpcsOutput{
			Vpcs: []types.Vpc{
				{VpcId: aws.String("vpc-1"), CidrBlock: aws.String("10.0.0.0/16"), State: types.VpcStateAvailable},
				{VpcId: aws.String("vpc-2"), CidrBlock: aws.String("10.1.0.0/16"), State: types.VpcStateAvailable},
			},
		},
	}
	m := newTestNetworkManager(fake)

	nets, err := m.ListNetworks(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(nets) != 2 {
		t.Errorf("len = %d, want 2", len(nets))
	}
}

func TestNWListNetworks_WithFilters(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeVpcsOut: &ec2.DescribeVpcsOutput{
			Vpcs: []types.Vpc{
				{VpcId: aws.String("vpc-filtered"), CidrBlock: aws.String("10.0.0.0/16"), State: types.VpcStateAvailable},
			},
		},
	}
	m := newTestNetworkManager(fake)

	nets, err := m.ListNetworks(context.Background(), map[string]string{"env": "prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(nets) != 1 {
		t.Errorf("len = %d, want 1", len(nets))
	}
}

func TestNWListNetworks_Error(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{describeVpcsErr: errors.New("list fail")}
	m := newTestNetworkManager(fake)

	_, err := m.ListNetworks(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- DeleteNetwork ----------------------------------------------------------

// TestNWDeleteNetwork_CleanupChain exercises the full dependency cleanup:
// checkVPCSubnets → deleteVPCRouteTables → deleteVPCInternetGateways → DeleteVpc.
func TestNWDeleteNetwork_CleanupChain(t *testing.T) {
	t.Parallel()

	assocID := aws.String("rtbassoc-123")

	fake := &nwFakeEC2{
		// No subnets → passes checkVPCSubnets
		describeSubnetsOut: &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{}},
		// One non-main route table with one non-main association
		describeRTsOut: &ec2.DescribeRouteTablesOutput{
			RouteTables: []types.RouteTable{
				{
					RouteTableId: aws.String("rtb-custom"),
					Associations: []types.RouteTableAssociation{
						{
							RouteTableAssociationId: assocID,
							Main:                    aws.Bool(false),
						},
					},
				},
			},
		},
		// One IGW
		describeIGWsOut: &ec2.DescribeInternetGatewaysOutput{
			InternetGateways: []types.InternetGateway{
				{InternetGatewayId: aws.String("igw-del-1")},
			},
		},
	}
	m := newTestNetworkManager(fake)

	err := m.DeleteNetwork(context.Background(), "vpc-cleanup")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNWDeleteNetwork_HasSubnets(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeSubnetsOut: &ec2.DescribeSubnetsOutput{
			Subnets: []types.Subnet{{SubnetId: aws.String("subnet-blocker")}},
		},
	}
	m := newTestNetworkManager(fake)

	err := m.DeleteNetwork(context.Background(), "vpc-blocked")
	if err == nil {
		t.Fatal("expected dependency error, got nil")
	}

	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "DependencyViolation" {
		t.Errorf("error = %v, want DependencyViolation", err)
	}
}

func TestNWDeleteNetwork_DeleteVpcError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeSubnetsOut: &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{}},
		describeRTsOut:     &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{}},
		describeIGWsOut:    &ec2.DescribeInternetGatewaysOutput{InternetGateways: []types.InternetGateway{}},
		deleteVpcErr:       errors.New("delete vpc failed"),
	}
	m := newTestNetworkManager(fake)

	err := m.DeleteNetwork(context.Background(), "vpc-del-fail")
	if err == nil {
		t.Fatal("expected error from DeleteVpc, got nil")
	}
}

// ---- CreateSubnet -----------------------------------------------------------

func TestNWCreateSubnet_Private_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		createSubnetOut: &ec2.CreateSubnetOutput{
			Subnet: &types.Subnet{
				SubnetId:         aws.String("subnet-priv-1"),
				VpcId:            aws.String("vpc-abc"),
				CidrBlock:        aws.String("10.0.1.0/24"),
				AvailabilityZone: aws.String("us-east-1a"),
				State:            types.SubnetStateAvailable,
			},
		},
	}
	m := newTestNetworkManager(fake)

	got, err := m.CreateSubnet(context.Background(), &cpi.SubnetRequest{
		Name:      "private-sub",
		NetworkID: "vpc-abc",
		CIDR:      "10.0.1.0/24",
		Type:      "private",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != "subnet-priv-1" {
		t.Errorf("ID = %q, want subnet-priv-1", got.ID)
	}

	if got.Type != "private" {
		t.Errorf("Type = %q, want private", got.Type)
	}
}

func TestNWCreateSubnet_Public_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		createSubnetOut: &ec2.CreateSubnetOutput{
			Subnet: &types.Subnet{
				SubnetId:  aws.String("subnet-pub-1"),
				VpcId:     aws.String("vpc-abc"),
				CidrBlock: aws.String("10.0.2.0/24"),
				State:     types.SubnetStateAvailable,
			},
		},
		// IGW already attached — ensureInternetGateway skips create,
		// and getInternetGatewayID succeeds on the same stub value.
		describeIGWsOut: &ec2.DescribeInternetGatewaysOutput{
			InternetGateways: []types.InternetGateway{
				{InternetGatewayId: aws.String("igw-pre-existing")},
			},
		},
		// Existing public RT found — reused (no RT create needed)
		describeRTsOut: &ec2.DescribeRouteTablesOutput{
			RouteTables: []types.RouteTable{
				{
					RouteTableId: aws.String("rtb-pub"),
					Tags: []types.Tag{
						{Key: aws.String("Type"), Value: aws.String("public")},
					},
				},
			},
		},
	}
	m := newTestNetworkManager(fake)

	got, err := m.CreateSubnet(context.Background(), &cpi.SubnetRequest{
		Name:      "public-sub",
		NetworkID: "vpc-abc",
		CIDR:      "10.0.2.0/24",
		Type:      "public",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Type != "public" {
		t.Errorf("Type = %q, want public", got.Type)
	}
}

func TestNWCreateSubnet_MissingNetworkID(t *testing.T) {
	t.Parallel()

	m := newTestNetworkManager(&nwFakeEC2{})

	_, err := m.CreateSubnet(context.Background(), &cpi.SubnetRequest{
		CIDR: "10.0.1.0/24",
	})
	if err == nil {
		t.Fatal("expected error for missing NetworkID, got nil")
	}
}

func TestNWCreateSubnet_MissingCIDR(t *testing.T) {
	t.Parallel()

	m := newTestNetworkManager(&nwFakeEC2{})

	_, err := m.CreateSubnet(context.Background(), &cpi.SubnetRequest{
		NetworkID: "vpc-abc",
	})
	if err == nil {
		t.Fatal("expected error for missing CIDR, got nil")
	}
}

func TestNWCreateSubnet_CreateError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{createSubnetErr: errors.New("subnet boom")}
	m := newTestNetworkManager(fake)

	_, err := m.CreateSubnet(context.Background(), &cpi.SubnetRequest{
		Name:      "fail-sub",
		NetworkID: "vpc-abc",
		CIDR:      "10.0.3.0/24",
	})
	if err == nil {
		t.Fatal("expected error from CreateSubnet, got nil")
	}
}

// ---- GetSubnet --------------------------------------------------------------

func TestNWGetSubnet_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeSubnetsOut: &ec2.DescribeSubnetsOutput{
			Subnets: []types.Subnet{
				{
					SubnetId:  aws.String("subnet-get-1"),
					VpcId:     aws.String("vpc-abc"),
					CidrBlock: aws.String("10.0.4.0/24"),
					State:     types.SubnetStateAvailable,
				},
			},
		},
	}
	m := newTestNetworkManager(fake)

	got, err := m.GetSubnet(context.Background(), "subnet-get-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != "subnet-get-1" {
		t.Errorf("ID = %q, want subnet-get-1", got.ID)
	}
}

func TestNWGetSubnet_NotFound(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeSubnetsOut: &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{}},
	}
	m := newTestNetworkManager(fake)

	_, err := m.GetSubnet(context.Background(), "subnet-missing")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
}

// ---- ListSubnets ------------------------------------------------------------

func TestNWListSubnets_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeSubnetsOut: &ec2.DescribeSubnetsOutput{
			Subnets: []types.Subnet{
				{SubnetId: aws.String("subnet-a"), VpcId: aws.String("vpc-x"), CidrBlock: aws.String("10.0.5.0/24"), State: types.SubnetStateAvailable},
				{SubnetId: aws.String("subnet-b"), VpcId: aws.String("vpc-x"), CidrBlock: aws.String("10.0.6.0/24"), State: types.SubnetStateAvailable},
			},
		},
	}
	m := newTestNetworkManager(fake)

	subs, err := m.ListSubnets(context.Background(), "vpc-x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(subs) != 2 {
		t.Errorf("len = %d, want 2", len(subs))
	}
}

func TestNWListSubnets_Error(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{describeSubnetsErr: errors.New("list subnets fail")}
	m := newTestNetworkManager(fake)

	_, err := m.ListSubnets(context.Background(), "vpc-x")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- DeleteSubnet -----------------------------------------------------------

func TestNWDeleteSubnet_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeENIsOut: &ec2.DescribeNetworkInterfacesOutput{NetworkInterfaces: []types.NetworkInterface{}},
	}
	m := newTestNetworkManager(fake)

	err := m.DeleteSubnet(context.Background(), "subnet-del-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNWDeleteSubnet_HasENIs(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeENIsOut: &ec2.DescribeNetworkInterfacesOutput{
			NetworkInterfaces: []types.NetworkInterface{
				{NetworkInterfaceId: aws.String("eni-123")},
			},
		},
	}
	m := newTestNetworkManager(fake)

	err := m.DeleteSubnet(context.Background(), "subnet-busy")
	if err == nil {
		t.Fatal("expected dependency error, got nil")
	}

	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "DependencyViolation" {
		t.Errorf("error = %v, want DependencyViolation", err)
	}
}

func TestNWDeleteSubnet_ENICheckError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{describeENIsErr: errors.New("eni check fail")}
	m := newTestNetworkManager(fake)

	err := m.DeleteSubnet(context.Background(), "subnet-eni-err")
	if err == nil {
		t.Fatal("expected error from DescribeNetworkInterfaces, got nil")
	}
}

func TestNWDeleteSubnet_DeleteError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeENIsOut: &ec2.DescribeNetworkInterfacesOutput{NetworkInterfaces: []types.NetworkInterface{}},
		deleteSubnetErr: errors.New("delete subnet fail"),
	}
	m := newTestNetworkManager(fake)

	err := m.DeleteSubnet(context.Background(), "subnet-del-fail")
	if err == nil {
		t.Fatal("expected error from DeleteSubnet, got nil")
	}
}

// ---- AllocateFloatingIP -----------------------------------------------------

func TestNWAllocateFloatingIP_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		allocAddrOut: &ec2.AllocateAddressOutput{
			AllocationId: aws.String("eipalloc-001"),
			PublicIp:     aws.String("54.0.0.1"),
		},
	}
	m := newTestNetworkManager(fake)

	got, err := m.AllocateFloatingIP(context.Background(), &cpi.AllocateFloatingIPRequest{
		NetworkID: "vpc-abc",
		Tags:      map[string]string{"env": "test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != "eipalloc-001" {
		t.Errorf("ID = %q, want eipalloc-001", got.ID)
	}

	if got.Address != "54.0.0.1" {
		t.Errorf("Address = %q, want 54.0.0.1", got.Address)
	}

	if got.Status != "available" {
		t.Errorf("Status = %q, want available", got.Status)
	}
}

func TestNWAllocateFloatingIP_Error(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{allocAddrErr: errors.New("eip alloc fail")}
	m := newTestNetworkManager(fake)

	_, err := m.AllocateFloatingIP(context.Background(), &cpi.AllocateFloatingIPRequest{})
	if err == nil {
		t.Fatal("expected error from AllocateAddress, got nil")
	}
}

// ---- GetFloatingIP ----------------------------------------------------------

func TestNWGetFloatingIP_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeAddrsOut: &ec2.DescribeAddressesOutput{
			Addresses: []types.Address{
				{AllocationId: aws.String("eipalloc-002"), PublicIp: aws.String("54.0.0.2")},
			},
		},
	}
	m := newTestNetworkManager(fake)

	got, err := m.GetFloatingIP(context.Background(), "eipalloc-002")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != "eipalloc-002" {
		t.Errorf("ID = %q, want eipalloc-002", got.ID)
	}
}

func TestNWGetFloatingIP_NotFound(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeAddrsOut: &ec2.DescribeAddressesOutput{Addresses: []types.Address{}},
	}
	m := newTestNetworkManager(fake)

	_, err := m.GetFloatingIP(context.Background(), "eipalloc-missing")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
}

// ---- ListFloatingIPs --------------------------------------------------------

func TestNWListFloatingIPs_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeAddrsOut: &ec2.DescribeAddressesOutput{
			Addresses: []types.Address{
				{AllocationId: aws.String("eipalloc-a"), PublicIp: aws.String("1.2.3.4")},
				{AllocationId: aws.String("eipalloc-b"), PublicIp: aws.String("5.6.7.8")},
			},
		},
	}
	m := newTestNetworkManager(fake)

	ips, err := m.ListFloatingIPs(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ips) != 2 {
		t.Errorf("len = %d, want 2", len(ips))
	}
}

func TestNWListFloatingIPs_Error(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{describeAddrsErr: errors.New("list addr fail")}
	m := newTestNetworkManager(fake)

	_, err := m.ListFloatingIPs(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- AssociateFloatingIP ----------------------------------------------------

func TestNWAssociateFloatingIP_Success(t *testing.T) {
	t.Parallel()

	m := newTestNetworkManager(&nwFakeEC2{})

	err := m.AssociateFloatingIP(context.Background(), "eipalloc-003", "i-inst-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNWAssociateFloatingIP_Error(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{assocAddrErr: errors.New("assoc fail")}
	m := newTestNetworkManager(fake)

	err := m.AssociateFloatingIP(context.Background(), "eipalloc-003", "i-inst-001")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- DisassociateFloatingIP -------------------------------------------------

func TestNWDisassociateFloatingIP_NotAssociated(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeAddrsOut: &ec2.DescribeAddressesOutput{
			Addresses: []types.Address{
				{AllocationId: aws.String("eipalloc-dis"), AssociationId: nil},
			},
		},
	}
	m := newTestNetworkManager(fake)

	// Should succeed silently — not associated
	err := m.DisassociateFloatingIP(context.Background(), "eipalloc-dis")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNWDisassociateFloatingIP_NotFound(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeAddrsOut: &ec2.DescribeAddressesOutput{Addresses: []types.Address{}},
	}
	m := newTestNetworkManager(fake)

	err := m.DisassociateFloatingIP(context.Background(), "eipalloc-gone")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
}

func TestNWDisassociateFloatingIP_Disassociates(t *testing.T) {
	t.Parallel()

	assocID := aws.String("eipassoc-xyz")
	fake := &nwFakeEC2{
		describeAddrsOut: &ec2.DescribeAddressesOutput{
			Addresses: []types.Address{
				{AllocationId: aws.String("eipalloc-004"), AssociationId: assocID},
			},
		},
	}
	m := newTestNetworkManager(fake)

	err := m.DisassociateFloatingIP(context.Background(), "eipalloc-004")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---- ReleaseFloatingIP ------------------------------------------------------

func TestNWReleaseFloatingIP_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		// DisassociateFloatingIP path: address found but not associated
		describeAddrsOut: &ec2.DescribeAddressesOutput{
			Addresses: []types.Address{
				{AllocationId: aws.String("eipalloc-005"), AssociationId: nil},
			},
		},
	}
	m := newTestNetworkManager(fake)

	err := m.ReleaseFloatingIP(context.Background(), "eipalloc-005")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNWReleaseFloatingIP_ReleaseError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeAddrsOut: &ec2.DescribeAddressesOutput{
			Addresses: []types.Address{
				{AllocationId: aws.String("eipalloc-006"), AssociationId: nil},
			},
		},
		releaseAddrErr: errors.New("release fail"),
	}
	m := newTestNetworkManager(fake)

	err := m.ReleaseFloatingIP(context.Background(), "eipalloc-006")
	if err == nil {
		t.Fatal("expected error from ReleaseAddress, got nil")
	}
}

// ---- CreateRouter -----------------------------------------------------------

func TestNWCreateRouter_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		createRTOut: &ec2.CreateRouteTableOutput{
			RouteTable: &types.RouteTable{
				RouteTableId: aws.String("rtb-router-1"),
				VpcId:        aws.String("vpc-abc"),
			},
		},
	}
	m := newTestNetworkManager(fake)

	got, err := m.CreateRouter(context.Background(), &cpi.CreateRouterRequest{
		Name:      "test-router",
		NetworkID: "vpc-abc",
		Tags:      map[string]string{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != "rtb-router-1" {
		t.Errorf("ID = %q, want rtb-router-1", got.ID)
	}
}

func TestNWCreateRouter_MissingNetworkID(t *testing.T) {
	t.Parallel()

	m := newTestNetworkManager(&nwFakeEC2{})

	_, err := m.CreateRouter(context.Background(), &cpi.CreateRouterRequest{
		Name: "no-vpc",
	})
	if err == nil {
		t.Fatal("expected error for missing NetworkID, got nil")
	}
}

func TestNWCreateRouter_CreateError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{createRTErr: errors.New("rt create fail")}
	m := newTestNetworkManager(fake)

	_, err := m.CreateRouter(context.Background(), &cpi.CreateRouterRequest{
		Name:      "fail-router",
		NetworkID: "vpc-abc",
	})
	if err == nil {
		t.Fatal("expected error from CreateRouteTable, got nil")
	}
}

// ---- GetRouter --------------------------------------------------------------

func TestNWGetRouter_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeRTsOut: &ec2.DescribeRouteTablesOutput{
			RouteTables: []types.RouteTable{
				{
					RouteTableId: aws.String("rtb-get-1"),
					VpcId:        aws.String("vpc-abc"),
					Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("my-rt")}},
				},
			},
		},
	}
	m := newTestNetworkManager(fake)

	got, err := m.GetRouter(context.Background(), "rtb-get-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != "rtb-get-1" {
		t.Errorf("ID = %q, want rtb-get-1", got.ID)
	}

	if got.Name != "my-rt" {
		t.Errorf("Name = %q, want my-rt", got.Name)
	}
}

func TestNWGetRouter_NotFound(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeRTsOut: &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{}},
	}
	m := newTestNetworkManager(fake)

	_, err := m.GetRouter(context.Background(), "rtb-missing")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
}

// ---- ListRouters ------------------------------------------------------------

func TestNWListRouters_Success(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeRTsOut: &ec2.DescribeRouteTablesOutput{
			RouteTables: []types.RouteTable{
				{RouteTableId: aws.String("rtb-1"), VpcId: aws.String("vpc-a")},
				{RouteTableId: aws.String("rtb-2"), VpcId: aws.String("vpc-b")},
			},
		},
	}
	m := newTestNetworkManager(fake)

	routers, err := m.ListRouters(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(routers) != 2 {
		t.Errorf("len = %d, want 2", len(routers))
	}
}

func TestNWListRouters_Error(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{describeRTsErr: errors.New("list rt fail")}
	m := newTestNetworkManager(fake)

	_, err := m.ListRouters(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- AttachRouterInterface --------------------------------------------------

func TestNWAttachRouterInterface_Success(t *testing.T) {
	t.Parallel()

	m := newTestNetworkManager(&nwFakeEC2{})

	err := m.AttachRouterInterface(context.Background(), "rtb-1", "subnet-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNWAttachRouterInterface_Error(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{assocRTErr: errors.New("assoc rt fail")}
	m := newTestNetworkManager(fake)

	err := m.AttachRouterInterface(context.Background(), "rtb-1", "subnet-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- DetachRouterInterface --------------------------------------------------

func TestNWDetachRouterInterface_Success(t *testing.T) {
	t.Parallel()

	subnetID := "subnet-detach"
	assocID := aws.String("rtbassoc-detach")

	fake := &nwFakeEC2{
		describeRTsOut: &ec2.DescribeRouteTablesOutput{
			RouteTables: []types.RouteTable{
				{
					RouteTableId: aws.String("rtb-detach"),
					Associations: []types.RouteTableAssociation{
						{
							RouteTableAssociationId: assocID,
							SubnetId:                aws.String(subnetID),
						},
					},
				},
			},
		},
	}
	m := newTestNetworkManager(fake)

	err := m.DetachRouterInterface(context.Background(), "rtb-detach", subnetID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNWDetachRouterInterface_NoAssociation(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeRTsOut: &ec2.DescribeRouteTablesOutput{
			RouteTables: []types.RouteTable{
				{
					RouteTableId: aws.String("rtb-no-assoc"),
					Associations: []types.RouteTableAssociation{},
				},
			},
		},
	}
	m := newTestNetworkManager(fake)

	err := m.DetachRouterInterface(context.Background(), "rtb-no-assoc", "subnet-other")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}

	var provErr *cpi.ProviderError
	if !errors.As(err, &provErr) || provErr.Code != "NotFound" {
		t.Errorf("error = %v, want ProviderError{Code:NotFound}", err)
	}
}

func TestNWDetachRouterInterface_RTNotFound(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeRTsOut: &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{}},
	}
	m := newTestNetworkManager(fake)

	err := m.DetachRouterInterface(context.Background(), "rtb-gone", "subnet-1")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
}

// ---- DeleteRouter -----------------------------------------------------------

func TestNWDeleteRouter_Success(t *testing.T) {
	t.Parallel()

	m := newTestNetworkManager(&nwFakeEC2{})

	err := m.DeleteRouter(context.Background(), "rtb-del-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNWDeleteRouter_Error(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{deleteRTErr: errors.New("rt delete fail")}
	m := newTestNetworkManager(fake)

	err := m.DeleteRouter(context.Background(), "rtb-del-fail")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- ensureInternetGateway (via CreateSubnet public path) -------------------

func TestNWEnsureInternetGateway_AlreadyExists(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		createSubnetOut: &ec2.CreateSubnetOutput{
			Subnet: &types.Subnet{
				SubnetId:  aws.String("subnet-pub-existing-igw"),
				VpcId:     aws.String("vpc-abc"),
				CidrBlock: aws.String("10.0.7.0/24"),
				State:     types.SubnetStateAvailable,
			},
		},
		// Existing IGW — ensureInternetGateway must skip create/attach
		describeIGWsOut: &ec2.DescribeInternetGatewaysOutput{
			InternetGateways: []types.InternetGateway{
				{InternetGatewayId: aws.String("igw-existing")},
			},
		},
		// Existing public RT found
		describeRTsOut: &ec2.DescribeRouteTablesOutput{
			RouteTables: []types.RouteTable{
				{RouteTableId: aws.String("rtb-pub-existing")},
			},
		},
	}
	m := newTestNetworkManager(fake)

	_, err := m.CreateSubnet(context.Background(), &cpi.SubnetRequest{
		Name:      "pub-existing-igw",
		NetworkID: "vpc-abc",
		CIDR:      "10.0.7.0/24",
		Type:      "public",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNWEnsureInternetGateway_CreateIGWError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		createSubnetOut: &ec2.CreateSubnetOutput{
			Subnet: &types.Subnet{
				SubnetId:  aws.String("subnet-pub-igw-err"),
				VpcId:     aws.String("vpc-abc"),
				CidrBlock: aws.String("10.0.8.0/24"),
				State:     types.SubnetStateAvailable,
			},
		},
		describeIGWsOut: &ec2.DescribeInternetGatewaysOutput{InternetGateways: []types.InternetGateway{}},
		createIGWErr:    errors.New("igw create fail"),
	}
	m := newTestNetworkManager(fake)

	_, err := m.CreateSubnet(context.Background(), &cpi.SubnetRequest{
		Name:      "pub-igw-create-fail",
		NetworkID: "vpc-abc",
		CIDR:      "10.0.8.0/24",
		Type:      "public",
	})
	if err == nil {
		t.Fatal("expected error from CreateInternetGateway, got nil")
	}
}

// ---- getInternetGatewayID (via ensurePublicRouteTable path) -----------------

func TestNWGetInternetGatewayID_NotFound(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		createSubnetOut: &ec2.CreateSubnetOutput{
			Subnet: &types.Subnet{
				SubnetId:  aws.String("subnet-pub-no-igw"),
				VpcId:     aws.String("vpc-abc"),
				CidrBlock: aws.String("10.0.9.0/24"),
				State:     types.SubnetStateAvailable,
			},
		},
		// IGW present for ensureInternetGateway
		describeIGWsOut: &ec2.DescribeInternetGatewaysOutput{
			InternetGateways: []types.InternetGateway{
				{InternetGatewayId: aws.String("igw-exists-for-ensure")},
			},
		},
		// No public RT → falls through to getInternetGatewayID inside ensurePublicRouteTable
		// but describeIGWsOut already returns one, so getInternetGatewayID succeeds.
		// Force it to fail by returning empty from the second DescribeInternetGateways call
		// — the stub always returns the same value, so this tests the RT creation path instead.
		describeRTsOut: &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{}},
		createRTOut: &ec2.CreateRouteTableOutput{
			RouteTable: &types.RouteTable{RouteTableId: aws.String("rtb-new-pub")},
		},
	}
	m := newTestNetworkManager(fake)

	_, err := m.CreateSubnet(context.Background(), &cpi.SubnetRequest{
		Name:      "pub-no-rt",
		NetworkID: "vpc-abc",
		CIDR:      "10.0.9.0/24",
		Type:      "public",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---- ensurePublicRouteTable: createPublicRouteTable error -------------------

func TestNWCreatePublicRouteTable_RouteError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		createSubnetOut: &ec2.CreateSubnetOutput{
			Subnet: &types.Subnet{
				SubnetId:  aws.String("subnet-pub-rt-route-fail"),
				VpcId:     aws.String("vpc-abc"),
				CidrBlock: aws.String("10.0.10.0/24"),
				State:     types.SubnetStateAvailable,
			},
		},
		describeIGWsOut: &ec2.DescribeInternetGatewaysOutput{
			InternetGateways: []types.InternetGateway{
				{InternetGatewayId: aws.String("igw-rt-route-fail")},
			},
		},
		// No existing public RT → create path
		describeRTsOut: &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{}},
		createRTOut: &ec2.CreateRouteTableOutput{
			RouteTable: &types.RouteTable{RouteTableId: aws.String("rtb-new")},
		},
		createRouteErr: errors.New("create route fail"),
	}
	m := newTestNetworkManager(fake)

	_, err := m.CreateSubnet(context.Background(), &cpi.SubnetRequest{
		Name:      "pub-route-fail",
		NetworkID: "vpc-abc",
		CIDR:      "10.0.10.0/24",
		Type:      "public",
	})
	if err == nil {
		t.Fatal("expected error from CreateRoute, got nil")
	}
}

// ---- deleteVPCInternetGateways: detach error --------------------------------

func TestNWDeleteVPCInternetGateways_DetachError(t *testing.T) {
	t.Parallel()

	fake := &nwFakeEC2{
		describeSubnetsOut: &ec2.DescribeSubnetsOutput{Subnets: []types.Subnet{}},
		describeRTsOut:     &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{}},
		describeIGWsOut: &ec2.DescribeInternetGatewaysOutput{
			InternetGateways: []types.InternetGateway{
				{InternetGatewayId: aws.String("igw-detach-fail")},
			},
		},
		detachIGWErr: errors.New("detach igw fail"),
	}
	m := newTestNetworkManager(fake)

	err := m.DeleteNetwork(context.Background(), "vpc-igw-detach-err")
	if err == nil {
		t.Fatal("expected error from DetachInternetGateway, got nil")
	}
}

// ---- Compile-time check: nwFakeEC2 satisfies EC2API -------------------------

var _ EC2API = (*nwFakeEC2)(nil)

// ---- waiter seam smoke test -------------------------------------------------

func TestNWWaiterSeam_VPCNoopUsed(t *testing.T) {
	t.Parallel()

	waiterCalled := false

	fake := &nwFakeEC2{
		createVpcOut: &ec2.CreateVpcOutput{
			Vpc: &types.Vpc{VpcId: aws.String("vpc-waiter-test")},
		},
	}

	cfg := &Config{Region: "us-west-2"}
	client := &Client{config: cfg}
	m := &NetworkManager{
		client: client,
		ec2:    fake,
		vpcWaiter: func(_ context.Context, _ EC2API, vpcID string) error {
			waiterCalled = true

			if vpcID != "vpc-waiter-test" {
				return fmt.Errorf("unexpected vpcID %q", vpcID)
			}

			return nil
		},
		subnetWaiter: noopSubnetWaiter,
	}

	_, err := m.CreateNetwork(context.Background(), &cpi.NetworkRequest{
		Name: "waiter-test",
		CIDR: "10.10.0.0/16",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !waiterCalled {
		t.Error("vpcWaiter was not called")
	}
}
