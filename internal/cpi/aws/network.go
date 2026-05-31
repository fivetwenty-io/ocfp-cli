package aws

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// getEC2 returns the EC2API to use. In tests m.ec2 is set; in production
// it falls back to the real client, preserving existing behaviour.
func (m *NetworkManager) getEC2(ctx context.Context) (EC2API, error) {
	if m.ec2 != nil {
		return m.ec2, nil
	}

	return m.client.getEC2Client(ctx)
}

// defaultVPCWaiter calls the real AWS VPC available waiter.
// The waiter factory requires a concrete *ec2.Client — it is used only on the
// production path where getEC2Client already returns one.
func defaultVPCWaiter(ctx context.Context, client EC2API, vpcID string) error {
	concreteClient, ok := client.(*ec2.Client)
	if !ok {
		return fmt.Errorf("VPC waiter requires *ec2.Client, got %T", client)
	}

	const vpcWaitTimeout = 2 * time.Minute //nolint:mnd // Standard AWS VPC creation wait time

	return ec2.NewVpcAvailableWaiter(concreteClient).Wait(ctx, &ec2.DescribeVpcsInput{
		VpcIds: []string{vpcID},
	}, vpcWaitTimeout)
}

// defaultSubnetWaiter calls the real AWS subnet available waiter.
func defaultSubnetWaiter(ctx context.Context, client EC2API, subnetID string) error {
	concreteClient, ok := client.(*ec2.Client)
	if !ok {
		return fmt.Errorf("subnet waiter requires *ec2.Client, got %T", client)
	}

	const subnetWaitTimeout = 2 * time.Minute //nolint:mnd // Standard AWS subnet creation wait time

	return ec2.NewSubnetAvailableWaiter(concreteClient).Wait(ctx, &ec2.DescribeSubnetsInput{
		SubnetIds: []string{subnetID},
	}, subnetWaitTimeout)
}

// waitForVPC calls the VPC waiter, falling back to defaultVPCWaiter when no
// override is set.
func (m *NetworkManager) waitForVPC(ctx context.Context, client EC2API, vpcID string) error {
	fn := m.vpcWaiter
	if fn == nil {
		fn = defaultVPCWaiter
	}

	return fn(ctx, client, vpcID)
}

// waitForSubnet calls the subnet waiter, falling back to defaultSubnetWaiter
// when no override is set.
func (m *NetworkManager) waitForSubnet(ctx context.Context, client EC2API, subnetID string) error {
	fn := m.subnetWaiter
	if fn == nil {
		fn = defaultSubnetWaiter
	}

	return fn(ctx, client, subnetID)
}

// CreateNetwork creates a new VPC in AWS with the specified configuration.
//
//nolint:funlen // VPC creation requires setup steps
func (m *NetworkManager) CreateNetwork(ctx context.Context, req *cpi.NetworkRequest) (*cpi.Network, error) {
	if req.CIDR == "" {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "InvalidParameter",
			Message:  "CIDR block is required for VPC creation",
		}
	}

	// Validate CIDR
	err := validateCIDR(req.CIDR)
	if err != nil {
		return nil, wrapError(err, "invalid CIDR block")
	}

	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	// Create VPC
	input := &ec2.CreateVpcInput{
		CidrBlock: aws.String(req.CIDR),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeVpc,
				Tags:         buildTags(req.Name, req.Tags),
			},
		},
	}

	if len(req.DNSServers) > 0 {
		input.Ipv4IpamPoolId = nil // AWS uses DHCP options for DNS
	}

	output, err := ec2Client.CreateVpc(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to create VPC")
	}

	vpcID := aws.ToString(output.Vpc.VpcId)

	// Enable DNS hostname support
	_, err = ec2Client.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{
		VpcId:              aws.String(vpcID),
		EnableDnsHostnames: &types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	if err != nil {
		return nil, wrapError(err, "failed to enable DNS hostnames")
	}

	// Enable DNS support
	_, err = ec2Client.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{
		VpcId:            aws.String(vpcID),
		EnableDnsSupport: &types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	if err != nil {
		return nil, wrapError(err, "failed to enable DNS support")
	}

	// Wait for VPC to be available
	err = m.waitForVPC(ctx, ec2Client, vpcID)
	if err != nil {
		return nil, wrapError(err, "VPC creation timeout")
	}

	return &cpi.Network{
		ID:         vpcID,
		Name:       req.Name,
		CIDR:       req.CIDR,
		Region:     m.client.config.Region,
		State:      cpi.ResourceStateAvailable,
		Tags:       req.Tags,
		DNSServers: req.DNSServers,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}, nil
}

// GetNetwork retrieves a VPC by ID.
func (m *NetworkManager) GetNetwork(ctx context.Context, networkID string) (*cpi.Network, error) {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	output, err := ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		VpcIds: []string{networkID},
	})
	if err != nil {
		return nil, wrapError(err, "failed to get VPC")
	}

	if len(output.Vpcs) == 0 {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  fmt.Sprintf("VPC %s not found", networkID),
		}
	}

	vpc := output.Vpcs[0]

	return convertVPCToNetwork(vpc, m.client.config.Region), nil
}

// ListNetworks lists all VPCs, optionally filtered.
func (m *NetworkManager) ListNetworks(ctx context.Context, filters map[string]string) ([]*cpi.Network, error) {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	input := &ec2.DescribeVpcsInput{}

	if len(filters) > 0 {
		input.Filters = buildAWSTagFilters(filters)
	}

	output, err := ec2Client.DescribeVpcs(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to list VPCs")
	}

	networks := make([]*cpi.Network, 0, len(output.Vpcs))
	for _, vpc := range output.Vpcs {
		networks = append(networks, convertVPCToNetwork(vpc, m.client.config.Region))
	}

	return networks, nil
}

// DeleteNetwork deletes a VPC with dependency checking.
func (m *NetworkManager) DeleteNetwork(ctx context.Context, networkID string) error {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	// Check for dependencies (subnets, IGWs, etc.)
	err = m.checkAndCleanVPCDependencies(ctx, ec2Client, networkID)
	if err != nil {
		return wrapError(err, "failed to clean VPC dependencies")
	}

	// Delete VPC
	_, err = ec2Client.DeleteVpc(ctx, &ec2.DeleteVpcInput{
		VpcId: aws.String(networkID),
	})
	if err != nil {
		return wrapError(err, "failed to delete VPC")
	}

	return nil
}

// CreateSubnet creates a new subnet within a VPC with route table association.
//
//nolint:funlen // Subnet creation with route table setup is complex
func (m *NetworkManager) CreateSubnet(ctx context.Context, req *cpi.SubnetRequest) (*cpi.Subnet, error) {
	if req.NetworkID == "" {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "InvalidParameter",
			Message:  "NetworkID (VPC ID) is required for subnet creation",
		}
	}

	if req.CIDR == "" {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "InvalidParameter",
			Message:  "CIDR block is required for subnet creation",
		}
	}

	// Validate CIDR
	err := validateCIDR(req.CIDR)
	if err != nil {
		return nil, wrapError(err, "invalid subnet CIDR block")
	}

	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	// Create subnet
	input := &ec2.CreateSubnetInput{
		VpcId:     aws.String(req.NetworkID),
		CidrBlock: aws.String(req.CIDR),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeSubnet,
				Tags:         buildTags(req.Name, req.Tags),
			},
		},
	}

	if req.AvailabilityZone != "" {
		input.AvailabilityZone = aws.String(req.AvailabilityZone)
	}

	output, err := ec2Client.CreateSubnet(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to create subnet")
	}

	subnetID := aws.ToString(output.Subnet.SubnetId)

	// Wait for subnet to be available
	err = m.waitForSubnet(ctx, ec2Client, subnetID)
	if err != nil {
		return nil, wrapError(err, "subnet creation timeout")
	}

	// Configure subnet based on type
	if req.Type == "public" {
		// Enable auto-assign public IP
		_, err = ec2Client.ModifySubnetAttribute(ctx, &ec2.ModifySubnetAttributeInput{
			SubnetId:            aws.String(subnetID),
			MapPublicIpOnLaunch: &types.AttributeBooleanValue{Value: aws.Bool(true)},
		})
		if err != nil {
			return nil, wrapError(err, "failed to configure public subnet")
		}

		// Ensure Internet Gateway exists for public subnet
		err = m.ensureInternetGateway(ctx, ec2Client, req.NetworkID)
		if err != nil {
			return nil, wrapError(err, "failed to ensure internet gateway")
		}

		// Create and associate route table for public subnet
		err = m.ensurePublicRouteTable(ctx, ec2Client, req.NetworkID, subnetID)
		if err != nil {
			return nil, wrapError(err, "failed to ensure public route table")
		}
	}

	subnet := convertSubnet(output.Subnet)

	// Override type from request since convertSubnet reads AWS attribute
	// which hasn't been updated yet if we just modified it
	if req.Type != "" {
		subnet.Type = req.Type
	}

	return subnet, nil
}

// GetSubnet retrieves a subnet by ID.
func (m *NetworkManager) GetSubnet(ctx context.Context, subnetID string) (*cpi.Subnet, error) {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	output, err := ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		SubnetIds: []string{subnetID},
	})
	if err != nil {
		return nil, wrapError(err, "failed to get subnet")
	}

	if len(output.Subnets) == 0 {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Subnet %s not found", subnetID),
		}
	}

	return convertSubnet(&output.Subnets[0]), nil
}

// ListSubnets lists all subnets in a VPC.
func (m *NetworkManager) ListSubnets(ctx context.Context, networkID string) ([]*cpi.Subnet, error) {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	input := &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{networkID},
			},
		},
	}

	output, err := ec2Client.DescribeSubnets(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to list subnets")
	}

	subnets := make([]*cpi.Subnet, 0, len(output.Subnets))
	for i := range output.Subnets {
		subnets = append(subnets, convertSubnet(&output.Subnets[i]))
	}

	return subnets, nil
}

// DeleteSubnet deletes a subnet with ENI checks.
func (m *NetworkManager) DeleteSubnet(ctx context.Context, subnetID string) error {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	// Check for ENIs in subnet
	enis, err := ec2Client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("subnet-id"),
				Values: []string{subnetID},
			},
		},
	})
	if err != nil {
		return wrapError(err, "failed to check subnet dependencies")
	}

	if len(enis.NetworkInterfaces) > 0 {
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "DependencyViolation",
			Message:  fmt.Sprintf("Subnet %s has %d network interfaces attached", subnetID, len(enis.NetworkInterfaces)),
		}
	}

	// Delete subnet
	_, err = ec2Client.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{
		SubnetId: aws.String(subnetID),
	})
	if err != nil {
		return wrapError(err, "failed to delete subnet")
	}

	return nil
}

// Helper functions

// ensureInternetGateway ensures an Internet Gateway exists for the VPC.
func (m *NetworkManager) ensureInternetGateway(ctx context.Context, ec2Client EC2API, vpcID string) error {
	// Check if IGW already exists
	igws, err := ec2Client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil {
		return wrapError(err, "failed to describe internet gateways")
	}

	// IGW already exists
	if len(igws.InternetGateways) > 0 {
		return nil
	}

	// Create IGW
	igwOutput, err := ec2Client.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInternetGateway,
				Tags: []types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String("igw-" + vpcID),
					},
					{
						Key:   aws.String("managed-by"),
						Value: aws.String("ocfp"),
					},
				},
			},
		},
	})
	if err != nil {
		return wrapError(err, "failed to create internet gateway")
	}

	// Attach IGW to VPC
	_, err = ec2Client.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{
		InternetGatewayId: igwOutput.InternetGateway.InternetGatewayId,
		VpcId:             aws.String(vpcID),
	})
	if err != nil {
		return wrapError(err, "failed to attach internet gateway")
	}

	return nil
}

// ensurePublicRouteTable ensures a route table with internet access exists for public subnets.
func (m *NetworkManager) ensurePublicRouteTable(ctx context.Context, ec2Client EC2API, vpcID, subnetID string) error {
	igwID, err := m.getInternetGatewayID(ctx, ec2Client, vpcID)
	if err != nil {
		return err
	}

	rtID, err := m.getOrCreatePublicRouteTable(ctx, ec2Client, vpcID, igwID)
	if err != nil {
		return err
	}

	return m.associateRouteTableWithSubnet(ctx, ec2Client, rtID, subnetID)
}

// getInternetGatewayID retrieves the internet gateway ID for a VPC.
func (m *NetworkManager) getInternetGatewayID(ctx context.Context, ec2Client EC2API, vpcID string) (string, error) {
	igws, err := ec2Client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil || len(igws.InternetGateways) == 0 {
		return "", fmt.Errorf("%w: %s", ErrInternetGatewayNotFound, vpcID)
	}

	return aws.ToString(igws.InternetGateways[0].InternetGatewayId), nil
}

// getOrCreatePublicRouteTable gets an existing public route table or creates a new one.
func (m *NetworkManager) getOrCreatePublicRouteTable(ctx context.Context, ec2Client EC2API, vpcID, igwID string) (string, error) {
	// Check for existing public route table
	routeTables, err := ec2Client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
			{
				Name:   aws.String("tag:Type"),
				Values: []string{"public"},
			},
		},
	})
	if err != nil {
		return "", wrapError(err, "failed to describe route tables")
	}

	if len(routeTables.RouteTables) > 0 {
		return aws.ToString(routeTables.RouteTables[0].RouteTableId), nil
	}

	return m.createPublicRouteTable(ctx, ec2Client, vpcID, igwID)
}

// createPublicRouteTable creates a new public route table with IGW route.
func (m *NetworkManager) createPublicRouteTable(ctx context.Context, ec2Client EC2API, vpcID, igwID string) (string, error) {
	rtOutput, err := ec2Client.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{
		VpcId: aws.String(vpcID),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeRouteTable,
				Tags: []types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String("rt-public-" + vpcID),
					},
					{
						Key:   aws.String("Type"),
						Value: aws.String("public"),
					},
					{
						Key:   aws.String("managed-by"),
						Value: aws.String("ocfp"),
					},
				},
			},
		},
	})
	if err != nil {
		return "", wrapError(err, "failed to create route table")
	}

	rtID := aws.ToString(rtOutput.RouteTable.RouteTableId)

	// Add route to IGW
	_, err = ec2Client.CreateRoute(ctx, &ec2.CreateRouteInput{
		RouteTableId:         aws.String(rtID),
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		GatewayId:            aws.String(igwID),
	})
	if err != nil {
		return "", wrapError(err, "failed to create route to internet gateway")
	}

	return rtID, nil
}

// associateRouteTableWithSubnet associates a route table with a subnet.
func (m *NetworkManager) associateRouteTableWithSubnet(ctx context.Context, ec2Client EC2API, rtID, subnetID string) error {
	_, err := ec2Client.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{
		RouteTableId: aws.String(rtID),
		SubnetId:     aws.String(subnetID),
	})
	if err != nil {
		return wrapError(err, "failed to associate route table with subnet")
	}

	return nil
}

// checkAndCleanVPCDependencies checks and optionally cleans VPC dependencies.
func (m *NetworkManager) checkAndCleanVPCDependencies(ctx context.Context, ec2Client EC2API, vpcID string) error {
	err := m.checkVPCSubnets(ctx, ec2Client, vpcID)
	if err != nil {
		return err
	}

	err = m.deleteVPCRouteTables(ctx, ec2Client, vpcID)
	if err != nil {
		return err
	}

	return m.deleteVPCInternetGateways(ctx, ec2Client, vpcID)
}

// checkVPCSubnets verifies that VPC has no remaining subnets.
func (m *NetworkManager) checkVPCSubnets(ctx context.Context, ec2Client EC2API, vpcID string) error {
	subnets, err := ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil {
		return wrapError(err, "failed to describe subnets")
	}

	if len(subnets.Subnets) > 0 {
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "DependencyViolation",
			Message:  fmt.Sprintf("VPC %s has %d subnets", vpcID, len(subnets.Subnets)),
		}
	}

	return nil
}

// deleteVPCRouteTables deletes custom route tables for the VPC.
func (m *NetworkManager) deleteVPCRouteTables(ctx context.Context, ec2Client EC2API, vpcID string) error {
	routeTables, err := ec2Client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil {
		return wrapError(err, "failed to describe route tables")
	}

	for _, routeTable := range routeTables.RouteTables {
		if m.isMainRouteTable(routeTable) {
			continue
		}

		err = m.deleteRouteTable(ctx, ec2Client, routeTable)
		if err != nil {
			return err
		}
	}

	return nil
}

// isMainRouteTable checks if a route table is the main route table.
func (m *NetworkManager) isMainRouteTable(routeTable types.RouteTable) bool {
	for _, assoc := range routeTable.Associations {
		if aws.ToBool(assoc.Main) {
			return true
		}
	}

	return false
}

// deleteRouteTable disassociates and deletes a route table.
func (m *NetworkManager) deleteRouteTable(ctx context.Context, ec2Client EC2API, routeTable types.RouteTable) error {
	rtID := aws.ToString(routeTable.RouteTableId)

	// Disassociate route table from all subnets
	for _, assoc := range routeTable.Associations {
		if assoc.RouteTableAssociationId != nil && !aws.ToBool(assoc.Main) {
			_, err := ec2Client.DisassociateRouteTable(ctx, &ec2.DisassociateRouteTableInput{
				AssociationId: assoc.RouteTableAssociationId,
			})
			if err != nil {
				return wrapError(err, "failed to disassociate route table")
			}
		}
	}

	// Delete the route table
	_, err := ec2Client.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{
		RouteTableId: aws.String(rtID),
	})
	if err != nil {
		return wrapError(err, "failed to delete route table")
	}

	return nil
}

// deleteVPCInternetGateways detaches and deletes internet gateways for the VPC.
func (m *NetworkManager) deleteVPCInternetGateways(ctx context.Context, ec2Client EC2API, vpcID string) error {
	igws, err := ec2Client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil {
		return wrapError(err, "failed to describe internet gateways")
	}

	for _, igw := range igws.InternetGateways {
		igwID := aws.ToString(igw.InternetGatewayId)

		err = m.detachAndDeleteInternetGateway(ctx, ec2Client, igwID, vpcID)
		if err != nil {
			return err
		}
	}

	return nil
}

// detachAndDeleteInternetGateway detaches and deletes an internet gateway.
func (m *NetworkManager) detachAndDeleteInternetGateway(ctx context.Context, ec2Client EC2API, igwID, vpcID string) error {
	// Detach IGW
	_, err := ec2Client.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{
		InternetGatewayId: aws.String(igwID),
		VpcId:             aws.String(vpcID),
	})
	if err != nil {
		return wrapError(err, "failed to detach internet gateway")
	}

	// Delete IGW
	_, err = ec2Client.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{
		InternetGatewayId: aws.String(igwID),
	})
	if err != nil {
		return wrapError(err, "failed to delete internet gateway")
	}

	return nil
}

// convertVPCToNetwork converts AWS VPC to CPI Network.
func convertVPCToNetwork(vpc types.Vpc, region string) *cpi.Network {
	tags := extractTags(vpc.Tags)
	name := tags["Name"]

	return &cpi.Network{
		ID:         aws.ToString(vpc.VpcId),
		Name:       name,
		CIDR:       aws.ToString(vpc.CidrBlock),
		Region:     region,
		State:      convertVPCState(vpc.State),
		Tags:       tags,
		DNSServers: []string{},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
}

// convertSubnet converts AWS Subnet to CPI Subnet.
func convertSubnet(subnet *types.Subnet) *cpi.Subnet {
	tags := extractTags(subnet.Tags)
	name := tags["Name"]

	subnetType := "private"
	if aws.ToBool(subnet.MapPublicIpOnLaunch) {
		subnetType = "public"
	}

	return &cpi.Subnet{
		ID:               aws.ToString(subnet.SubnetId),
		Name:             name,
		NetworkID:        aws.ToString(subnet.VpcId),
		CIDR:             aws.ToString(subnet.CidrBlock),
		AvailabilityZone: aws.ToString(subnet.AvailabilityZone),
		Type:             subnetType,
		State:            convertSubnetState(subnet.State),
		Tags:             tags,
		CreatedAt:        time.Now(),
	}
}

// convertVPCState converts AWS VPC state to CPI ResourceState.
func convertVPCState(state types.VpcState) cpi.ResourceState {
	switch state {
	case types.VpcStatePending:
		return cpi.ResourceStateCreating
	case types.VpcStateAvailable:
		return cpi.ResourceStateAvailable
	default:
		return cpi.ResourceStateUnknown
	}
}

// convertSubnetState converts AWS Subnet state to CPI ResourceState.
func convertSubnetState(state types.SubnetState) cpi.ResourceState {
	switch state {
	case types.SubnetStatePending:
		return cpi.ResourceStateCreating
	case types.SubnetStateAvailable:
		return cpi.ResourceStateAvailable
	default:
		return cpi.ResourceStateUnknown
	}
}

// buildTags builds AWS tags from name and tag map.
func buildTags(name string, tags map[string]string) []types.Tag {
	return buildNamedResourceTags(name, tags)
}

// extractTags extracts tags from AWS tag list.
func extractTags(awsTags []types.Tag) map[string]string {
	tags := make(map[string]string)
	for _, tag := range awsTags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	return tags
}

// validateCIDR validates a CIDR block.
func validateCIDR(cidr string) error {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid CIDR format: %w", err)
	}

	// Ensure it's a valid network address
	if network.IP.String() != network.IP.Mask(network.Mask).String() {
		return fmt.Errorf("%w: %s", ErrInvalidNetworkCIDR, cidr)
	}

	return nil
}

// Elastic IP (Floating IP) Operations

// AllocateFloatingIP allocates an Elastic IP address.
func (m *NetworkManager) AllocateFloatingIP(ctx context.Context, req *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	// Allocate Elastic IP
	input := &ec2.AllocateAddressInput{
		Domain: types.DomainTypeVpc,
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeElasticIp,
				Tags:         buildTagsFromMap(req.Tags),
			},
		},
	}

	if req.NetworkID != "" {
		// Store VPC ID in tags for tracking
		input.TagSpecifications[0].Tags = append(input.TagSpecifications[0].Tags, types.Tag{
			Key:   aws.String("vpc-id"),
			Value: aws.String(req.NetworkID),
		})
	}

	output, err := ec2Client.AllocateAddress(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to allocate elastic IP")
	}

	return &cpi.FloatingIP{
		ID:         aws.ToString(output.AllocationId),
		Address:    aws.ToString(output.PublicIp),
		Status:     "available",
		InstanceID: "",
		NetworkID:  req.NetworkID,
		Tags:       req.Tags,
		CreatedAt:  time.Now(),
	}, nil
}

// GetFloatingIP retrieves an Elastic IP by allocation ID.
func (m *NetworkManager) GetFloatingIP(ctx context.Context, allocationID string) (*cpi.FloatingIP, error) {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	output, err := ec2Client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		AllocationIds: []string{allocationID},
	})
	if err != nil {
		return nil, wrapError(err, "failed to get elastic IP")
	}

	if len(output.Addresses) == 0 {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Elastic IP %s not found", allocationID),
		}
	}

	return convertAddress(&output.Addresses[0]), nil
}

// ListFloatingIPs lists all Elastic IPs.
func (m *NetworkManager) ListFloatingIPs(ctx context.Context, filters map[string]string) ([]*cpi.FloatingIP, error) {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	input := &ec2.DescribeAddressesInput{}

	// Apply tag filters
	if len(filters) > 0 {
		input.Filters = buildAWSTagFilters(filters)
	}

	output, err := ec2Client.DescribeAddresses(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to list elastic IPs")
	}

	ips := make([]*cpi.FloatingIP, 0, len(output.Addresses))
	for i := range output.Addresses {
		ips = append(ips, convertAddress(&output.Addresses[i]))
	}

	return ips, nil
}

// AssociateFloatingIP associates an Elastic IP with an instance.
func (m *NetworkManager) AssociateFloatingIP(ctx context.Context, allocationID, instanceID string) error {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	_, err = ec2Client.AssociateAddress(ctx, &ec2.AssociateAddressInput{
		AllocationId: aws.String(allocationID),
		InstanceId:   aws.String(instanceID),
	})
	if err != nil {
		return wrapError(err, "failed to associate elastic IP")
	}

	return nil
}

// DisassociateFloatingIP disassociates an Elastic IP from its instance.
func (m *NetworkManager) DisassociateFloatingIP(ctx context.Context, allocationID string) error {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	// Get association ID
	output, err := ec2Client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		AllocationIds: []string{allocationID},
	})
	if err != nil {
		return wrapError(err, "failed to get elastic IP")
	}

	if len(output.Addresses) == 0 {
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Elastic IP %s not found", allocationID),
		}
	}

	addr := output.Addresses[0]
	if addr.AssociationId == nil {
		// Not associated, nothing to do
		return nil
	}

	_, err = ec2Client.DisassociateAddress(ctx, &ec2.DisassociateAddressInput{
		AssociationId: addr.AssociationId,
	})
	if err != nil {
		return wrapError(err, "failed to disassociate elastic IP")
	}

	return nil
}

// ReleaseFloatingIP releases an Elastic IP.
func (m *NetworkManager) ReleaseFloatingIP(ctx context.Context, allocationID string) error {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	// Disassociate first if needed
	err = m.DisassociateFloatingIP(ctx, allocationID)
	if err != nil {
		return err
	}

	// Release the address
	_, err = ec2Client.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{
		AllocationId: aws.String(allocationID),
	})
	if err != nil {
		return wrapError(err, "failed to release elastic IP")
	}

	return nil
}

// convertAddress converts AWS Address to CPI FloatingIP.
func convertAddress(addr *types.Address) *cpi.FloatingIP {
	status := "available"
	instanceID := ""

	if addr.InstanceId != nil {
		status = "associated"
		instanceID = aws.ToString(addr.InstanceId)
	}

	tags := extractTags(addr.Tags)
	networkID := tags["vpc-id"]

	return &cpi.FloatingIP{
		ID:         aws.ToString(addr.AllocationId),
		Address:    aws.ToString(addr.PublicIp),
		Status:     status,
		InstanceID: instanceID,
		NetworkID:  networkID,
		Tags:       tags,
		CreatedAt:  time.Now(),
	}
}

// buildTagsFromMap builds AWS tags from a tag map.
func buildTagsFromMap(tags map[string]string) []types.Tag {
	return buildResourceTags(tags)
}

// Router operations (AWS doesn't have direct router resources, but we can manage route tables)

// CreateRouter creates a custom route table (router abstraction in AWS).
func (m *NetworkManager) CreateRouter(ctx context.Context, req *cpi.CreateRouterRequest) (*cpi.Router, error) {
	if req.NetworkID == "" {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "InvalidParameter",
			Message:  "NetworkID (VPC ID) is required for router creation",
		}
	}

	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	// Create route table
	output, err := ec2Client.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{
		VpcId: aws.String(req.NetworkID),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeRouteTable,
				Tags:         buildTags(req.Name, req.Tags),
			},
		},
	})
	if err != nil {
		return nil, wrapError(err, "failed to create route table")
	}

	rtID := aws.ToString(output.RouteTable.RouteTableId)

	return &cpi.Router{
		ID:              rtID,
		Name:            req.Name,
		NetworkID:       req.NetworkID,
		ExternalGateway: "",
		State:           cpi.ResourceStateAvailable,
		Routes:          []*cpi.Route{},
		Interfaces:      []string{},
		Tags:            req.Tags,
		CreatedAt:       time.Now(),
	}, nil
}

// GetRouter retrieves a route table by ID.
func (m *NetworkManager) GetRouter(ctx context.Context, routeTableID string) (*cpi.Router, error) {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	output, err := ec2Client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		RouteTableIds: []string{routeTableID},
	})
	if err != nil {
		return nil, wrapError(err, "failed to get route table")
	}

	if len(output.RouteTables) == 0 {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Route table %s not found", routeTableID),
		}
	}

	return convertRouteTable(&output.RouteTables[0]), nil
}

// ListRouters lists all route tables.
func (m *NetworkManager) ListRouters(ctx context.Context) ([]*cpi.Router, error) {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	output, err := ec2Client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{})
	if err != nil {
		return nil, wrapError(err, "failed to list route tables")
	}

	routers := make([]*cpi.Router, 0, len(output.RouteTables))
	for i := range output.RouteTables {
		routers = append(routers, convertRouteTable(&output.RouteTables[i]))
	}

	return routers, nil
}

// AttachRouterInterface associates a route table with a subnet.
func (m *NetworkManager) AttachRouterInterface(ctx context.Context, routeTableID, subnetID string) error {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	_, err = ec2Client.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{
		RouteTableId: aws.String(routeTableID),
		SubnetId:     aws.String(subnetID),
	})
	if err != nil {
		return wrapError(err, "failed to associate route table with subnet")
	}

	return nil
}

// DetachRouterInterface disassociates a route table from a subnet.
func (m *NetworkManager) DetachRouterInterface(ctx context.Context, routeTableID, subnetID string) error {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	// Find association ID
	output, err := ec2Client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		RouteTableIds: []string{routeTableID},
	})
	if err != nil {
		return wrapError(err, "failed to get route table")
	}

	if len(output.RouteTables) == 0 {
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Route table %s not found", routeTableID),
		}
	}

	var associationID string

	for _, assoc := range output.RouteTables[0].Associations {
		if aws.ToString(assoc.SubnetId) == subnetID {
			associationID = aws.ToString(assoc.RouteTableAssociationId)

			break
		}
	}

	if associationID == "" {
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  fmt.Sprintf("No association found between route table %s and subnet %s", routeTableID, subnetID),
		}
	}

	_, err = ec2Client.DisassociateRouteTable(ctx, &ec2.DisassociateRouteTableInput{
		AssociationId: aws.String(associationID),
	})
	if err != nil {
		return wrapError(err, "failed to disassociate route table")
	}

	return nil
}

// DeleteRouter deletes a route table.
func (m *NetworkManager) DeleteRouter(ctx context.Context, routeTableID string) error {
	ec2Client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	_, err = ec2Client.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{
		RouteTableId: aws.String(routeTableID),
	})
	if err != nil {
		return wrapError(err, "failed to delete route table")
	}

	return nil
}

// convertRouteTable converts AWS RouteTable to CPI Router.
func convertRouteTable(routeTable *types.RouteTable) *cpi.Router {
	tags := extractTags(routeTable.Tags)
	name := tags["Name"]

	routes := make([]*cpi.Route, 0, len(routeTable.Routes))
	for _, route := range routeTable.Routes {
		if route.DestinationCidrBlock != nil {
			var nextHop string

			switch {
			case route.GatewayId != nil:
				nextHop = aws.ToString(route.GatewayId)
			case route.NatGatewayId != nil:
				nextHop = aws.ToString(route.NatGatewayId)
			case route.NetworkInterfaceId != nil:
				nextHop = aws.ToString(route.NetworkInterfaceId)
			}

			routes = append(routes, &cpi.Route{
				Destination: aws.ToString(route.DestinationCidrBlock),
				NextHop:     nextHop,
			})
		}
	}

	interfaces := make([]string, 0, len(routeTable.Associations))
	for _, assoc := range routeTable.Associations {
		if assoc.SubnetId != nil {
			interfaces = append(interfaces, aws.ToString(assoc.SubnetId))
		}
	}

	return &cpi.Router{
		ID:              aws.ToString(routeTable.RouteTableId),
		Name:            name,
		NetworkID:       aws.ToString(routeTable.VpcId),
		ExternalGateway: "",
		State:           cpi.ResourceStateAvailable,
		Routes:          routes,
		Interfaces:      interfaces,
		Tags:            tags,
		CreatedAt:       time.Now(),
	}
}
