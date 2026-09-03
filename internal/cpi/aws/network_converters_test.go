package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// ---- convertVPCState --------------------------------------------------------

func TestConvertVPCState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input types.VpcState
		want  cpi.ResourceState
	}{
		{"pending", types.VpcStatePending, cpi.ResourceStateCreating},
		{"available", types.VpcStateAvailable, cpi.ResourceStateAvailable},
		{"deleting", types.VpcStateDeleting, cpi.ResourceStateDeleting},
		{"unknown enum", types.VpcState("bogus"), cpi.ResourceStateUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := convertVPCState(tt.input)
			if got != tt.want {
				t.Errorf("convertVPCState(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---- convertSubnetState -----------------------------------------------------

func TestConvertSubnetState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input types.SubnetState
		want  cpi.ResourceState
	}{
		{"pending", types.SubnetStatePending, cpi.ResourceStateCreating},
		{"available", types.SubnetStateAvailable, cpi.ResourceStateAvailable},
		{"unknown enum", types.SubnetState("bogus"), cpi.ResourceStateUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := convertSubnetState(tt.input)
			if got != tt.want {
				t.Errorf("convertSubnetState(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---- convertVPCToNetwork ----------------------------------------------------

func TestConvertVPCToNetwork_MinimalFields(t *testing.T) {
	t.Parallel()

	vpc := types.Vpc{
		VpcId:     aws.String("vpc-abc"),
		CidrBlock: aws.String("10.0.0.0/16"),
		State:     types.VpcStateAvailable,
	}

	got := convertVPCToNetwork(vpc, "us-east-1")

	if got.ID != "vpc-abc" {
		t.Errorf("ID = %q, want %q", got.ID, "vpc-abc")
	}
	if got.CIDR != "10.0.0.0/16" {
		t.Errorf("CIDR = %q, want %q", got.CIDR, "10.0.0.0/16")
	}
	if got.Region != "us-east-1" {
		t.Errorf("Region = %q, want %q", got.Region, "us-east-1")
	}
	if got.State != cpi.ResourceStateAvailable {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateAvailable)
	}
	if got.Name != "" {
		t.Errorf("Name = %q, want empty (no Name tag)", got.Name)
	}
}

func TestConvertVPCToNetwork_WithNameTag(t *testing.T) {
	t.Parallel()

	vpc := types.Vpc{
		VpcId:     aws.String("vpc-named"),
		CidrBlock: aws.String("172.16.0.0/12"),
		State:     types.VpcStatePending,
		Tags: []types.Tag{
			{Key: aws.String("Name"), Value: aws.String("prod-vpc")},
			{Key: aws.String("env"), Value: aws.String("prod")},
		},
	}

	got := convertVPCToNetwork(vpc, "us-west-2")

	if got.Name != "prod-vpc" {
		t.Errorf("Name = %q, want %q", got.Name, "prod-vpc")
	}
	if got.State != cpi.ResourceStateCreating {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateCreating)
	}
	if got.Tags["env"] != "prod" {
		t.Errorf("Tags[env] = %q, want %q", got.Tags["env"], "prod")
	}
	if got.DNSServers == nil {
		t.Errorf("DNSServers must be non-nil slice")
	}
}

// ---- convertSubnet ----------------------------------------------------------

func TestConvertSubnet_Private(t *testing.T) {
	t.Parallel()

	mapPublic := false
	subnet := &types.Subnet{
		SubnetId:            aws.String("subnet-priv"),
		VpcId:               aws.String("vpc-xyz"),
		CidrBlock:           aws.String("10.1.0.0/24"),
		AvailabilityZone:    aws.String("us-east-1a"),
		MapPublicIpOnLaunch: &mapPublic,
		State:               types.SubnetStateAvailable,
	}

	got := convertSubnet(subnet)

	if got.ID != "subnet-priv" {
		t.Errorf("ID = %q, want %q", got.ID, "subnet-priv")
	}
	if got.NetworkID != "vpc-xyz" {
		t.Errorf("NetworkID = %q, want %q", got.NetworkID, "vpc-xyz")
	}
	if got.Type != "private" {
		t.Errorf("Type = %q, want %q", got.Type, "private")
	}
	if got.State != cpi.ResourceStateAvailable {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateAvailable)
	}
}

func TestConvertSubnet_Public(t *testing.T) {
	t.Parallel()

	mapPublic := true
	subnet := &types.Subnet{
		SubnetId:            aws.String("subnet-pub"),
		VpcId:               aws.String("vpc-xyz"),
		CidrBlock:           aws.String("10.2.0.0/24"),
		AvailabilityZone:    aws.String("us-east-1b"),
		MapPublicIpOnLaunch: &mapPublic,
		State:               types.SubnetStatePending,
	}

	got := convertSubnet(subnet)

	if got.Type != "public" {
		t.Errorf("Type = %q, want %q", got.Type, "public")
	}
	if got.State != cpi.ResourceStateCreating {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateCreating)
	}
}

func TestConvertSubnet_WithNameTag(t *testing.T) {
	t.Parallel()

	mapPublic := false
	subnet := &types.Subnet{
		SubnetId:            aws.String("subnet-named"),
		VpcId:               aws.String("vpc-abc"),
		CidrBlock:           aws.String("192.168.1.0/24"),
		AvailabilityZone:    aws.String("eu-west-1a"),
		MapPublicIpOnLaunch: &mapPublic,
		State:               types.SubnetStateAvailable,
		Tags: []types.Tag{
			{Key: aws.String("Name"), Value: aws.String("app-subnet")},
		},
	}

	got := convertSubnet(subnet)

	if got.Name != "app-subnet" {
		t.Errorf("Name = %q, want %q", got.Name, "app-subnet")
	}
	if got.AvailabilityZone != "eu-west-1a" {
		t.Errorf("AvailabilityZone = %q, want %q", got.AvailabilityZone, "eu-west-1a")
	}
}

// ---- buildTags --------------------------------------------------------------

func TestBuildTags_AddsNameAndManagedBy(t *testing.T) {
	t.Parallel()

	result := buildTags("my-resource", map[string]string{"env": "prod"})

	tagMap := make(map[string]string)
	for _, tag := range result {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	if tagMap["Name"] != "my-resource" {
		t.Errorf("Name tag = %q, want %q", tagMap["Name"], "my-resource")
	}
	if tagMap["managed-by"] != "ocfp" {
		t.Errorf("managed-by tag = %q, want %q", tagMap["managed-by"], "ocfp")
	}
	if tagMap["env"] != "prod" {
		t.Errorf("env tag = %q, want %q", tagMap["env"], "prod")
	}
}

func TestBuildTags_DoesNotOverrideManagedBy(t *testing.T) {
	t.Parallel()

	result := buildTags("res", map[string]string{"managed-by": "custom"})

	tagMap := make(map[string]string)
	for _, tag := range result {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	if tagMap["managed-by"] != "custom" {
		t.Errorf("managed-by tag = %q, want %q (should not be overridden)", tagMap["managed-by"], "custom")
	}
}

func TestBuildTags_EmptyTagMap(t *testing.T) {
	t.Parallel()

	result := buildTags("res", map[string]string{})

	if len(result) != 2 {
		t.Errorf("len(tags) = %d, want 2 (Name + managed-by)", len(result))
	}
}

// ---- extractTags ------------------------------------------------------------

func TestExtractTags_NilAndEmpty(t *testing.T) {
	t.Parallel()

	got := extractTags(nil)
	if got == nil {
		t.Errorf("extractTags(nil) returned nil, want empty map")
	}
	if len(got) != 0 {
		t.Errorf("extractTags(nil) len = %d, want 0", len(got))
	}

	got = extractTags([]types.Tag{})
	if len(got) != 0 {
		t.Errorf("extractTags([]) len = %d, want 0", len(got))
	}
}

func TestExtractTags_MultipleTags(t *testing.T) {
	t.Parallel()

	tags := []types.Tag{
		{Key: aws.String("Name"), Value: aws.String("my-vm")},
		{Key: aws.String("env"), Value: aws.String("staging")},
	}

	got := extractTags(tags)

	if got["Name"] != "my-vm" {
		t.Errorf("got[Name] = %q, want %q", got["Name"], "my-vm")
	}
	if got["env"] != "staging" {
		t.Errorf("got[env] = %q, want %q", got["env"], "staging")
	}
}

// ---- validateCIDR -----------------------------------------------------------

func TestValidateCIDR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cidr    string
		wantErr bool
	}{
		{"valid /16", "10.0.0.0/16", false},
		{"valid /24", "192.168.1.0/24", false},
		{"valid /32", "1.2.3.4/32", false},
		// net.ParseCIDR masks host bits in network.IP, so host-bit-set CIDRs parse OK.
		{"host bit set accepted by net.ParseCIDR", "10.0.0.1/24", false},
		{"invalid format", "not-a-cidr", true},
		{"empty string", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateCIDR(tt.cidr)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCIDR(%q) error = %v, wantErr = %v", tt.cidr, err, tt.wantErr)
			}
		})
	}
}
