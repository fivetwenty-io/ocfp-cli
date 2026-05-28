package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// secFakeEC2 is a test-local stub for EC2API. Fields hold canned responses;
// panics on any method not overridden (fail-loud contract).
// All overrideable methods are set via func fields so each test can customise.

type secFakeEC2 struct {
	EC2API // embed interface — un-overridden methods panic at runtime

	createSGOut *ec2.CreateSecurityGroupOutput
	createSGErr error

	describeSGOut *ec2.DescribeSecurityGroupsOutput
	describeSGErr error

	// Per-call describe sequences: if non-nil, each call pops the front.
	describeSGSeq    []*ec2.DescribeSecurityGroupsOutput
	describeSGErrSeq []error
	describeSGIdx    int

	deleteSGOut *ec2.DeleteSecurityGroupOutput
	deleteSGErr error

	authorizeIngressOut *ec2.AuthorizeSecurityGroupIngressOutput
	authorizeIngressErr error

	authorizeEgressOut *ec2.AuthorizeSecurityGroupEgressOutput
	authorizeEgressErr error

	revokeIngressOut *ec2.RevokeSecurityGroupIngressOutput
	revokeIngressErr error

	revokeEgressOut *ec2.RevokeSecurityGroupEgressOutput
	revokeEgressErr error

	// Capture last inputs for assertion.
	lastCreateSGInput         *ec2.CreateSecurityGroupInput
	lastDeleteSGInput         *ec2.DeleteSecurityGroupInput
	lastAuthorizeIngressInput *ec2.AuthorizeSecurityGroupIngressInput
	lastAuthorizeEgressInput  *ec2.AuthorizeSecurityGroupEgressInput
	lastRevokeIngressInput    *ec2.RevokeSecurityGroupIngressInput
	lastRevokeEgressInput     *ec2.RevokeSecurityGroupEgressInput
}

func (f *secFakeEC2) CreateSecurityGroup(_ context.Context, params *ec2.CreateSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	f.lastCreateSGInput = params
	return f.createSGOut, f.createSGErr
}

func (f *secFakeEC2) DescribeSecurityGroups(_ context.Context, _ *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	// If sequence is configured, advance through it.
	if len(f.describeSGSeq) > 0 {
		idx := f.describeSGIdx
		if idx >= len(f.describeSGSeq) {
			idx = len(f.describeSGSeq) - 1
		}

		f.describeSGIdx++

		out := f.describeSGSeq[idx]

		var errOut error
		if idx < len(f.describeSGErrSeq) {
			errOut = f.describeSGErrSeq[idx]
		}

		return out, errOut
	}

	return f.describeSGOut, f.describeSGErr
}

func (f *secFakeEC2) DeleteSecurityGroup(_ context.Context, params *ec2.DeleteSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error) {
	f.lastDeleteSGInput = params
	return f.deleteSGOut, f.deleteSGErr
}

func (f *secFakeEC2) AuthorizeSecurityGroupIngress(_ context.Context, params *ec2.AuthorizeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupIngressOutput, error) {
	f.lastAuthorizeIngressInput = params
	return f.authorizeIngressOut, f.authorizeIngressErr
}

func (f *secFakeEC2) AuthorizeSecurityGroupEgress(_ context.Context, params *ec2.AuthorizeSecurityGroupEgressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error) {
	f.lastAuthorizeEgressInput = params
	return f.authorizeEgressOut, f.authorizeEgressErr
}

func (f *secFakeEC2) RevokeSecurityGroupIngress(_ context.Context, params *ec2.RevokeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error) {
	f.lastRevokeIngressInput = params
	return f.revokeIngressOut, f.revokeIngressErr
}

func (f *secFakeEC2) RevokeSecurityGroupEgress(_ context.Context, params *ec2.RevokeSecurityGroupEgressInput, _ ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupEgressOutput, error) {
	f.lastRevokeEgressInput = params
	return f.revokeEgressOut, f.revokeEgressErr
}

// newSecMgr builds a SecurityManager with the given fake EC2.
func newSecMgr(fake *secFakeEC2) *SecurityManager {
	return &SecurityManager{ec2: fake}
}

// fakeSG returns a minimal ec2types.SecurityGroup for testing.
func fakeSG(id, name, vpcID string) ec2types.SecurityGroup {
	return ec2types.SecurityGroup{
		GroupId:     aws.String(id),
		GroupName:   aws.String(name),
		Description: aws.String("test sg"),
		VpcId:       aws.String(vpcID),
	}
}

// ---- CreateSecurityGroup -------------------------------------------------------

func TestCreateSecurityGroup_Happy(t *testing.T) {
	t.Parallel()

	sg := fakeSG("sg-abc", "my-sg", "vpc-1")
	fake := &secFakeEC2{
		createSGOut: &ec2.CreateSecurityGroupOutput{GroupId: aws.String("sg-abc")},
		// Two DescribeSecurityGroups calls: one from configureSecurityGroupRules,
		// one from GetSecurityGroup (the final fetch).
		describeSGSeq: []*ec2.DescribeSecurityGroupsOutput{
			{SecurityGroups: []ec2types.SecurityGroup{sg}},
			{SecurityGroups: []ec2types.SecurityGroup{sg}},
		},
	}

	m := newSecMgr(fake)
	got, err := m.CreateSecurityGroup(context.Background(), &cpi.CreateSecurityGroupRequest{
		Name:      "my-sg",
		NetworkID: "vpc-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != "sg-abc" {
		t.Errorf("ID = %q, want sg-abc", got.ID)
	}

	if got.Name != "my-sg" {
		t.Errorf("Name = %q, want my-sg", got.Name)
	}
}

func TestCreateSecurityGroup_EmptyName(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	_, err := m.CreateSecurityGroup(context.Background(), &cpi.CreateSecurityGroupRequest{NetworkID: "vpc-1"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestCreateSecurityGroup_EmptyNetworkID(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	_, err := m.CreateSecurityGroup(context.Background(), &cpi.CreateSecurityGroupRequest{Name: "sg"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestCreateSecurityGroup_CreateError(t *testing.T) {
	t.Parallel()

	boom := errors.New("api failure")
	fake := &secFakeEC2{createSGErr: boom}
	m := newSecMgr(fake)
	_, err := m.CreateSecurityGroup(context.Background(), &cpi.CreateSecurityGroupRequest{Name: "sg", NetworkID: "vpc-1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestCreateSecurityGroup_Duplicate verifies idempotent path calls GetSecurityGroupByName.
func TestCreateSecurityGroup_Duplicate(t *testing.T) {
	t.Parallel()

	sg := fakeSG("sg-existing", "my-sg", "vpc-1")

	// CreateSecurityGroup returns a ProviderError with Code="AlreadyExists" to trigger
	// the duplicate path without going through WrapAWSError's smithy machinery.
	dupErr := &cpi.ProviderError{Provider: "aws", Code: "AlreadyExists", Message: "duplicate"}

	fake := &secFakeEC2{
		createSGErr: dupErr,
		// GetSecurityGroupByName calls DescribeSecurityGroups with name filter.
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{
			SecurityGroups: []ec2types.SecurityGroup{sg},
		},
	}

	m := newSecMgr(fake)
	got, err := m.CreateSecurityGroup(context.Background(), &cpi.CreateSecurityGroupRequest{
		Name:      "my-sg",
		NetworkID: "vpc-1",
	})
	if err != nil {
		t.Fatalf("unexpected error on duplicate: %v", err)
	}

	if got.ID != "sg-existing" {
		t.Errorf("ID = %q, want sg-existing", got.ID)
	}
}

// ---- GetSecurityGroup ---------------------------------------------------------

func TestGetSecurityGroup_Happy(t *testing.T) {
	t.Parallel()

	sg := fakeSG("sg-123", "my-sg", "vpc-2")
	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{sg}},
	}

	m := newSecMgr(fake)
	got, err := m.GetSecurityGroup(context.Background(), "sg-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != "sg-123" {
		t.Errorf("ID = %q, want sg-123", got.ID)
	}

	if got.NetworkID != "vpc-2" {
		t.Errorf("NetworkID = %q, want vpc-2", got.NetworkID)
	}
}

func TestGetSecurityGroup_EmptyID(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	_, err := m.GetSecurityGroup(context.Background(), "")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestGetSecurityGroup_NotFound(t *testing.T) {
	t.Parallel()

	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{}},
	}

	m := newSecMgr(fake)
	_, err := m.GetSecurityGroup(context.Background(), "sg-missing")
	if !errors.Is(err, ErrSecurityGroupNotFound) {
		t.Errorf("expected ErrSecurityGroupNotFound, got %v", err)
	}
}

func TestGetSecurityGroup_APIError(t *testing.T) {
	t.Parallel()

	fake := &secFakeEC2{describeSGErr: errors.New("network error")}
	m := newSecMgr(fake)
	_, err := m.GetSecurityGroup(context.Background(), "sg-123")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- GetSecurityGroupByName ---------------------------------------------------

func TestGetSecurityGroupByName_Happy(t *testing.T) {
	t.Parallel()

	sg := fakeSG("sg-name-1", "web-sg", "vpc-3")
	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{sg}},
	}

	m := newSecMgr(fake)
	got, err := m.GetSecurityGroupByName(context.Background(), "web-sg", "vpc-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.ID != "sg-name-1" {
		t.Errorf("ID = %q, want sg-name-1", got.ID)
	}
}

func TestGetSecurityGroupByName_EmptyName(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	_, err := m.GetSecurityGroupByName(context.Background(), "", "vpc-3")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestGetSecurityGroupByName_NotFound(t *testing.T) {
	t.Parallel()

	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{}},
	}

	m := newSecMgr(fake)
	_, err := m.GetSecurityGroupByName(context.Background(), "web-sg", "")
	if !errors.Is(err, ErrSecurityGroupNotFound) {
		t.Errorf("expected ErrSecurityGroupNotFound, got %v", err)
	}
}

func TestGetSecurityGroupByName_APIError(t *testing.T) {
	t.Parallel()

	fake := &secFakeEC2{describeSGErr: errors.New("api error")}
	m := newSecMgr(fake)
	_, err := m.GetSecurityGroupByName(context.Background(), "web-sg", "vpc-3")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- ListSecurityGroups -------------------------------------------------------

func TestListSecurityGroups_Happy(t *testing.T) {
	t.Parallel()

	sgs := []ec2types.SecurityGroup{
		fakeSG("sg-1", "a", "vpc-4"),
		fakeSG("sg-2", "b", "vpc-4"),
		fakeSG("sg-3", "c", "vpc-4"),
	}
	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: sgs},
	}

	m := newSecMgr(fake)
	list, err := m.ListSecurityGroups(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(list) != 3 {
		t.Errorf("len = %d, want 3", len(list))
	}
}

func TestListSecurityGroups_WithFilters(t *testing.T) {
	t.Parallel()

	sg := fakeSG("sg-5", "filtered", "vpc-5")
	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{sg}},
	}

	m := newSecMgr(fake)
	// "name" and "network-id" are remapped inside ListSecurityGroups.
	list, err := m.ListSecurityGroups(context.Background(), map[string]string{
		"name":       "filtered",
		"network-id": "vpc-5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(list) != 1 {
		t.Errorf("len = %d, want 1", len(list))
	}
}

func TestListSecurityGroups_APIError(t *testing.T) {
	t.Parallel()

	fake := &secFakeEC2{describeSGErr: errors.New("list error")}
	m := newSecMgr(fake)
	_, err := m.ListSecurityGroups(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- DeleteSecurityGroup ------------------------------------------------------

func TestDeleteSecurityGroup_Happy(t *testing.T) {
	t.Parallel()

	sg := fakeSG("sg-del", "not-default", "vpc-6")
	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{sg}},
		deleteSGOut:   &ec2.DeleteSecurityGroupOutput{},
	}

	m := newSecMgr(fake)
	err := m.DeleteSecurityGroup(context.Background(), "sg-del")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fake.lastDeleteSGInput == nil {
		t.Fatal("DeleteSecurityGroup was not called on fake")
	}

	if aws.ToString(fake.lastDeleteSGInput.GroupId) != "sg-del" {
		t.Errorf("GroupId = %q, want sg-del", aws.ToString(fake.lastDeleteSGInput.GroupId))
	}
}

func TestDeleteSecurityGroup_EmptyID(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	err := m.DeleteSecurityGroup(context.Background(), "")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestDeleteSecurityGroup_Default(t *testing.T) {
	t.Parallel()

	sg := fakeSG("sg-default", "default", "vpc-6")
	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{sg}},
	}

	m := newSecMgr(fake)
	err := m.DeleteSecurityGroup(context.Background(), "sg-default")
	if err == nil {
		t.Fatal("expected error for default SG, got nil")
	}
}

func TestDeleteSecurityGroup_APIError(t *testing.T) {
	t.Parallel()

	sg := fakeSG("sg-del", "not-default", "vpc-6")
	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{sg}},
		deleteSGErr:   errors.New("delete failed"),
	}

	m := newSecMgr(fake)
	err := m.DeleteSecurityGroup(context.Background(), "sg-del")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDeleteSecurityGroup_NotFound(t *testing.T) {
	t.Parallel()

	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{}},
	}

	m := newSecMgr(fake)
	err := m.DeleteSecurityGroup(context.Background(), "sg-gone")
	if !errors.Is(err, ErrSecurityGroupNotFound) {
		t.Errorf("expected ErrSecurityGroupNotFound, got %v", err)
	}
}

// ---- AddSecurityRule ----------------------------------------------------------

func TestAddSecurityRule_IngressHappy(t *testing.T) {
	t.Parallel()

	// ruleExists calls DescribeSecurityGroups; returns empty perms → rule absent.
	sg := fakeSG("sg-r", "web", "vpc-7")
	fake := &secFakeEC2{
		describeSGOut:       &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{sg}},
		authorizeIngressOut: &ec2.AuthorizeSecurityGroupIngressOutput{},
	}

	m := newSecMgr(fake)
	rule := &cpi.SecurityRule{
		Direction:    "ingress",
		Protocol:     "tcp",
		PortRangeMin: 80,
		PortRangeMax: 80,
		RemoteIPCIDR: "0.0.0.0/0",
	}
	err := m.AddSecurityRule(context.Background(), "sg-r", rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fake.lastAuthorizeIngressInput == nil {
		t.Fatal("AuthorizeSecurityGroupIngress was not called")
	}
}

func TestAddSecurityRule_EgressHappy(t *testing.T) {
	t.Parallel()

	sg := fakeSG("sg-r", "web", "vpc-7")
	fake := &secFakeEC2{
		describeSGOut:      &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{sg}},
		authorizeEgressOut: &ec2.AuthorizeSecurityGroupEgressOutput{},
	}

	m := newSecMgr(fake)
	rule := &cpi.SecurityRule{
		Direction:    "egress",
		Protocol:     "tcp",
		PortRangeMin: 443,
		PortRangeMax: 443,
		RemoteIPCIDR: "0.0.0.0/0",
	}
	err := m.AddSecurityRule(context.Background(), "sg-r", rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fake.lastAuthorizeEgressInput == nil {
		t.Fatal("AuthorizeSecurityGroupEgress was not called")
	}
}

func TestAddSecurityRule_AlreadyExists_Idempotent(t *testing.T) {
	t.Parallel()

	from, to := int32(80), int32(80)
	sg := ec2types.SecurityGroup{
		GroupId:   aws.String("sg-idem"),
		GroupName: aws.String("web"),
		VpcId:     aws.String("vpc-7"),
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   &from,
				ToPort:     &to,
				IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
			},
		},
	}

	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{sg}},
	}

	m := newSecMgr(fake)
	rule := &cpi.SecurityRule{
		Direction:    "ingress",
		Protocol:     "tcp",
		PortRangeMin: 80,
		PortRangeMax: 80,
		RemoteIPCIDR: "0.0.0.0/0",
	}
	err := m.AddSecurityRule(context.Background(), "sg-idem", rule)
	if err != nil {
		t.Fatalf("idempotent add should return nil, got: %v", err)
	}

	// Authorize must NOT have been called.
	if fake.lastAuthorizeIngressInput != nil {
		t.Error("AuthorizeSecurityGroupIngress should not be called when rule already exists")
	}
}

func TestAddSecurityRule_EmptyGroupID(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	rule := &cpi.SecurityRule{Direction: "ingress", Protocol: "tcp", RemoteIPCIDR: "0.0.0.0/0"}
	err := m.AddSecurityRule(context.Background(), "", rule)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestAddSecurityRule_NilRule(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	err := m.AddSecurityRule(context.Background(), "sg-r", nil)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestAddSecurityRule_InvalidDirection(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	rule := &cpi.SecurityRule{
		Direction:    "forward",
		Protocol:     "tcp",
		RemoteIPCIDR: "0.0.0.0/0",
	}
	err := m.AddSecurityRule(context.Background(), "sg-r", rule)
	if err == nil {
		t.Fatal("expected error for invalid direction, got nil")
	}
}

func TestAddSecurityRule_AuthorizeError(t *testing.T) {
	t.Parallel()

	sg := fakeSG("sg-r", "web", "vpc-7")
	fake := &secFakeEC2{
		describeSGOut:       &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{sg}},
		authorizeIngressErr: errors.New("authorize failed"),
	}

	m := newSecMgr(fake)
	rule := &cpi.SecurityRule{
		Direction:    "ingress",
		Protocol:     "tcp",
		PortRangeMin: 22,
		PortRangeMax: 22,
		RemoteIPCIDR: "10.0.0.0/8",
	}
	err := m.AddSecurityRule(context.Background(), "sg-r", rule)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- RemoveSecurityRule -------------------------------------------------------

func TestRemoveSecurityRule_IngressHappy(t *testing.T) {
	t.Parallel()

	fake := &secFakeEC2{
		revokeIngressOut: &ec2.RevokeSecurityGroupIngressOutput{},
	}

	m := newSecMgr(fake)
	// ruleID format: direction:protocol:fromPort:toPort:remote
	err := m.RemoveSecurityRule(context.Background(), "sg-r", "ingress:tcp:80:80:0.0.0.0/0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fake.lastRevokeIngressInput == nil {
		t.Fatal("RevokeSecurityGroupIngress was not called")
	}

	if aws.ToString(fake.lastRevokeIngressInput.GroupId) != "sg-r" {
		t.Errorf("GroupId = %q, want sg-r", aws.ToString(fake.lastRevokeIngressInput.GroupId))
	}
}

func TestRemoveSecurityRule_EgressHappy(t *testing.T) {
	t.Parallel()

	fake := &secFakeEC2{
		revokeEgressOut: &ec2.RevokeSecurityGroupEgressOutput{},
	}

	m := newSecMgr(fake)
	err := m.RemoveSecurityRule(context.Background(), "sg-r", "egress:tcp:443:443:0.0.0.0/0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fake.lastRevokeEgressInput == nil {
		t.Fatal("RevokeSecurityGroupEgress was not called")
	}
}

func TestRemoveSecurityRule_EmptyGroupID(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	err := m.RemoveSecurityRule(context.Background(), "", "ingress:tcp:80:80:0.0.0.0/0")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestRemoveSecurityRule_EmptyRuleID(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	err := m.RemoveSecurityRule(context.Background(), "sg-r", "")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestRemoveSecurityRule_BadRuleIDFormat(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	// Fewer than 5 colon-separated parts.
	err := m.RemoveSecurityRule(context.Background(), "sg-r", "ingress:tcp:80")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestRemoveSecurityRule_InvalidDirection(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	err := m.RemoveSecurityRule(context.Background(), "sg-r", "forward:tcp:80:80:0.0.0.0/0")
	if err == nil {
		t.Fatal("expected error for invalid direction, got nil")
	}
}

func TestRemoveSecurityRule_RevokeError(t *testing.T) {
	t.Parallel()

	fake := &secFakeEC2{
		revokeIngressErr: errors.New("revoke failed"),
	}

	m := newSecMgr(fake)
	err := m.RemoveSecurityRule(context.Background(), "sg-r", "ingress:tcp:22:22:10.0.0.0/8")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- ListSecurityRules --------------------------------------------------------

func TestListSecurityRules_Happy(t *testing.T) {
	t.Parallel()

	from, to := int32(443), int32(443)
	sg := ec2types.SecurityGroup{
		GroupId:   aws.String("sg-rules"),
		GroupName: aws.String("web"),
		VpcId:     aws.String("vpc-8"),
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   &from,
				ToPort:     &to,
				IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
			},
		},
	}

	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{sg}},
	}

	m := newSecMgr(fake)
	rules, err := m.ListSecurityRules(context.Background(), "sg-rules")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rules) != 1 {
		t.Errorf("len(rules) = %d, want 1", len(rules))
	}
}

func TestListSecurityRules_EmptyGroupID(t *testing.T) {
	t.Parallel()

	m := newSecMgr(&secFakeEC2{})
	_, err := m.ListSecurityRules(context.Background(), "")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestListSecurityRules_NotFound(t *testing.T) {
	t.Parallel()

	fake := &secFakeEC2{
		describeSGOut: &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{}},
	}

	m := newSecMgr(fake)
	_, err := m.ListSecurityRules(context.Background(), "sg-gone")
	if !errors.Is(err, ErrSecurityGroupNotFound) {
		t.Errorf("expected ErrSecurityGroupNotFound, got %v", err)
	}
}

func TestListSecurityRules_APIError(t *testing.T) {
	t.Parallel()

	fake := &secFakeEC2{describeSGErr: errors.New("api error")}
	m := newSecMgr(fake)
	_, err := m.ListSecurityRules(context.Background(), "sg-r")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
