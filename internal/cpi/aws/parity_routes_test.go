package aws

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"testing"
)

type routeEC2 struct {
	EC2API
	routes []*ec2.CreateRouteInput
}

func (f *routeEC2) CreateRouteTable(context.Context, *ec2.CreateRouteTableInput, ...func(*ec2.Options)) (*ec2.CreateRouteTableOutput, error) {
	return &ec2.CreateRouteTableOutput{RouteTable: &types.RouteTable{RouteTableId: aws.String("rtb-1")}}, nil
}
func (f *routeEC2) CreateRoute(_ context.Context, in *ec2.CreateRouteInput, _ ...func(*ec2.Options)) (*ec2.CreateRouteOutput, error) {
	f.routes = append(f.routes, in)
	return &ec2.CreateRouteOutput{Return: aws.Bool(true)}, nil
}
func TestParityAWSRouterAppliesRoutes(t *testing.T) {
	f := &routeEC2{}
	got, err := (&NetworkManager{ec2: f}).CreateRouter(context.Background(), &cpi.CreateRouterRequest{Name: "router", NetworkID: "vpc-1", ExternalGateway: "igw-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.routes) != 1 {
		t.Fatalf("routes not applied: %d", len(f.routes))
	}
	if aws.ToString(f.routes[0].GatewayId) != "igw-1" || got.ExternalGateway != "igw-1" {
		t.Fatal("route target mapping wrong")
	}
}
