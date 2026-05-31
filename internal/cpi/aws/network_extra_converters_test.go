package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// ---- convertAddress ---------------------------------------------------------

func TestConvertAddress_Available(t *testing.T) {
	t.Parallel()

	addr := &types.Address{
		AllocationId: aws.String("eipalloc-abc"),
		PublicIp:     aws.String("54.1.2.3"),
		InstanceId:   nil, // not associated
		Tags: []types.Tag{
			{Key: aws.String("vpc-id"), Value: aws.String("vpc-net")},
			{Key: aws.String("env"), Value: aws.String("prod")},
		},
	}

	got := convertAddress(addr)

	if got.ID != "eipalloc-abc" {
		t.Errorf("ID = %q, want eipalloc-abc", got.ID)
	}
	if got.Address != "54.1.2.3" {
		t.Errorf("Address = %q, want 54.1.2.3", got.Address)
	}
	if got.Status != "available" {
		t.Errorf("Status = %q, want available", got.Status)
	}
	if got.InstanceID != "" {
		t.Errorf("InstanceID = %q, want empty (not associated)", got.InstanceID)
	}
	if got.NetworkID != "vpc-net" {
		t.Errorf("NetworkID = %q, want vpc-net", got.NetworkID)
	}
}

func TestConvertAddress_Associated(t *testing.T) {
	t.Parallel()

	addr := &types.Address{
		AllocationId: aws.String("eipalloc-xyz"),
		PublicIp:     aws.String("1.2.3.4"),
		InstanceId:   aws.String("i-assoc"),
	}

	got := convertAddress(addr)

	if got.Status != "associated" {
		t.Errorf("Status = %q, want associated", got.Status)
	}
	if got.InstanceID != "i-assoc" {
		t.Errorf("InstanceID = %q, want i-assoc", got.InstanceID)
	}
}

// ---- buildTagsFromMap -------------------------------------------------------

func TestBuildTagsFromMap_AddsManagedBy(t *testing.T) {
	t.Parallel()

	result := buildTagsFromMap(map[string]string{"env": "dev"})

	tagMap := make(map[string]string)
	for _, tag := range result {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	if tagMap["managed-by"] != "ocfp" {
		t.Errorf("managed-by = %q, want ocfp", tagMap["managed-by"])
	}
	if tagMap["env"] != "dev" {
		t.Errorf("env = %q, want dev", tagMap["env"])
	}
}

func TestBuildTagsFromMap_DoesNotOverrideManagedBy(t *testing.T) {
	t.Parallel()

	result := buildTagsFromMap(map[string]string{"managed-by": "custom"})

	tagMap := make(map[string]string)
	for _, tag := range result {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	if tagMap["managed-by"] != "custom" {
		t.Errorf("managed-by = %q, want custom (should not be overridden)", tagMap["managed-by"])
	}
}

func TestBuildTagsFromMap_EmptyInput(t *testing.T) {
	t.Parallel()

	result := buildTagsFromMap(map[string]string{})

	// Only managed-by added
	if len(result) != 1 {
		t.Errorf("len(tags) = %d, want 1 (only managed-by)", len(result))
	}
}

// ---- convertRouteTable ------------------------------------------------------

func TestConvertRouteTable_Basic(t *testing.T) {
	t.Parallel()

	gwID := "igw-abc"
	rt := &types.RouteTable{
		RouteTableId: aws.String("rtb-abc"),
		VpcId:        aws.String("vpc-rt"),
		Tags: []types.Tag{
			{Key: aws.String("Name"), Value: aws.String("main-rt")},
		},
		Routes: []types.Route{
			{DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: &gwID},
			{DestinationCidrBlock: aws.String("10.0.0.0/16"), GatewayId: aws.String("local")},
			// Route with nil DestinationCidrBlock must be skipped
			{DestinationCidrBlock: nil, GatewayId: &gwID},
		},
		Associations: []types.RouteTableAssociation{
			{SubnetId: aws.String("subnet-a")},
			{SubnetId: nil}, // nil must be skipped
		},
	}

	got := convertRouteTable(rt)

	if got.ID != "rtb-abc" {
		t.Errorf("ID = %q, want rtb-abc", got.ID)
	}
	if got.Name != "main-rt" {
		t.Errorf("Name = %q, want main-rt", got.Name)
	}
	if got.NetworkID != "vpc-rt" {
		t.Errorf("NetworkID = %q, want vpc-rt", got.NetworkID)
	}
	if got.State != cpi.ResourceStateAvailable {
		t.Errorf("State = %q, want %q", got.State, cpi.ResourceStateAvailable)
	}
	if len(got.Routes) != 2 {
		t.Errorf("Routes len = %d, want 2 (nil DestinationCidrBlock skipped)", len(got.Routes))
	}
	if got.Routes[0].Destination != "0.0.0.0/0" {
		t.Errorf("Routes[0].Destination = %q, want 0.0.0.0/0", got.Routes[0].Destination)
	}
	if got.Routes[0].NextHop != "igw-abc" {
		t.Errorf("Routes[0].NextHop = %q, want igw-abc", got.Routes[0].NextHop)
	}
	if len(got.Interfaces) != 1 {
		t.Errorf("Interfaces len = %d, want 1 (nil SubnetId skipped)", len(got.Interfaces))
	}
	if got.Interfaces[0] != "subnet-a" {
		t.Errorf("Interfaces[0] = %q, want subnet-a", got.Interfaces[0])
	}
}

func TestConvertRouteTable_NatGatewayNextHop(t *testing.T) {
	t.Parallel()

	natID := "nat-abc"
	rt := &types.RouteTable{
		RouteTableId: aws.String("rtb-nat"),
		Routes: []types.Route{
			{DestinationCidrBlock: aws.String("0.0.0.0/0"), NatGatewayId: &natID},
		},
	}

	got := convertRouteTable(rt)

	if len(got.Routes) != 1 {
		t.Fatalf("Routes len = %d, want 1", len(got.Routes))
	}
	if got.Routes[0].NextHop != "nat-abc" {
		t.Errorf("NextHop = %q, want nat-abc", got.Routes[0].NextHop)
	}
}

func TestConvertRouteTable_NetworkInterfaceNextHop(t *testing.T) {
	t.Parallel()

	eniID := "eni-abc"
	rt := &types.RouteTable{
		RouteTableId: aws.String("rtb-eni"),
		Routes: []types.Route{
			{DestinationCidrBlock: aws.String("10.0.0.0/8"), NetworkInterfaceId: &eniID},
		},
	}

	got := convertRouteTable(rt)

	if len(got.Routes) != 1 {
		t.Fatalf("Routes len = %d, want 1", len(got.Routes))
	}
	if got.Routes[0].NextHop != "eni-abc" {
		t.Errorf("NextHop = %q, want eni-abc", got.Routes[0].NextHop)
	}
}

// ---- isMainRouteTable -------------------------------------------------------

func TestIsMainRouteTable(t *testing.T) {
	t.Parallel()

	nm := &NetworkManager{client: nil}

	mainTrue := aws.Bool(true)
	rt := types.RouteTable{
		Associations: []types.RouteTableAssociation{
			{Main: mainTrue},
		},
	}
	if !nm.isMainRouteTable(rt) {
		t.Errorf("isMainRouteTable: expected true for Main=true association")
	}

	mainFalse := aws.Bool(false)
	rt2 := types.RouteTable{
		Associations: []types.RouteTableAssociation{
			{Main: mainFalse},
		},
	}
	if nm.isMainRouteTable(rt2) {
		t.Errorf("isMainRouteTable: expected false for Main=false association")
	}

	rt3 := types.RouteTable{}
	if nm.isMainRouteTable(rt3) {
		t.Errorf("isMainRouteTable: expected false for empty associations")
	}
}

func TestConvertRouteTable_NoNextHop(t *testing.T) {
	t.Parallel()

	rt := &types.RouteTable{
		RouteTableId: aws.String("rtb-empty"),
		Routes: []types.Route{
			// All next-hop fields nil
			{DestinationCidrBlock: aws.String("10.0.0.0/8")},
		},
	}

	got := convertRouteTable(rt)

	if len(got.Routes) != 1 {
		t.Fatalf("Routes len = %d, want 1", len(got.Routes))
	}
	if got.Routes[0].NextHop != "" {
		t.Errorf("NextHop = %q, want empty (all next-hop nil)", got.Routes[0].NextHop)
	}
}
