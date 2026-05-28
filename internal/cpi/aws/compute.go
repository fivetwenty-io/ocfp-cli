package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

const (
	// Instance wait timeouts.
	instanceCreateTimeout = 10 * time.Minute //nolint:mnd // standard timeout
	instanceStartTimeout  = 5 * time.Minute  //nolint:mnd // standard timeout
	instanceStopTimeout   = 5 * time.Minute  //nolint:mnd // standard timeout
	instanceDeleteTimeout = 5 * time.Minute  //nolint:mnd // standard timeout

	filterKeyName = "name"

	// EBS volume size bounds (in GB) per AWS limits.
	ebsMinSizeGB = 1
	ebsMaxSizeGB = 16384
)

// getEC2 returns the EC2API to use for this manager.
// In tests, m.ec2 is set directly.  In production, m.ec2 is nil and
// this falls back to the real client, preserving existing behaviour.
func (m *ComputeManager) getEC2(ctx context.Context) (EC2API, error) {
	if m.ec2 != nil {
		return m.ec2, nil
	}

	return m.client.getEC2Client(ctx)
}

// CreateInstance creates a new EC2 instance with the specified configuration.
//
//nolint:funlen // EC2 instance creation requires extensive configuration
func (m *ComputeManager) CreateInstance(ctx context.Context, req *cpi.InstanceRequest) (*cpi.Instance, error) {
	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	// Build RunInstances input
	input := &ec2.RunInstancesInput{
		ImageId:      aws.String(req.Image),
		InstanceType: types.InstanceType(req.Flavor),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags:         buildTags(req.Name, req.Tags),
			},
		},
		MetadataOptions: &types.InstanceMetadataOptionsRequest{
			HttpTokens:   types.HttpTokensStateRequired, // IMDSv2 required
			HttpEndpoint: types.InstanceMetadataEndpointStateEnabled,
		},
	}

	// Key pair
	keyPairName := req.KeyPair
	if keyPairName == "" {
		keyPairName = req.KeyPairName
	}

	if keyPairName != "" {
		input.KeyName = aws.String(keyPairName)
	}

	// User data (must be base64 encoded)
	if req.UserData != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(req.UserData))
		input.UserData = aws.String(encoded)
	}

	// Security groups
	securityGroups := req.SecurityGroups
	if len(securityGroups) == 0 {
		securityGroups = req.SecurityGroupIDs
	}

	// Network configuration
	// When SubnetID is provided, use NetworkInterfaces to enable public IP assignment
	// This is the AWS best practice for instances that need public IPs
	if req.SubnetID != "" {
		input.NetworkInterfaces = []types.InstanceNetworkInterfaceSpecification{
			{
				DeviceIndex:              aws.Int32(0),
				SubnetId:                 aws.String(req.SubnetID),
				AssociatePublicIpAddress: aws.Bool(true), // Request public IP for instances in public subnets
				Groups:                   securityGroups,
			},
		}
	} else if len(securityGroups) > 0 {
		// If no subnet specified, use legacy configuration
		input.SecurityGroupIds = securityGroups
	}

	// Availability zone
	if req.AvailabilityZone != "" {
		input.Placement = &types.Placement{
			AvailabilityZone: aws.String(req.AvailabilityZone),
		}
	}

	// Root device size configuration
	if req.BootVolumeSize > 0 {
		if req.BootVolumeSize < ebsMinSizeGB || req.BootVolumeSize > ebsMaxSizeGB {
			return nil, fmt.Errorf("boot volume size %d GB out of range [%d, %d]",
				req.BootVolumeSize, ebsMinSizeGB, ebsMaxSizeGB)
		}

		input.BlockDeviceMappings = []types.BlockDeviceMapping{
			{
				DeviceName: aws.String("/dev/sda1"), // Standard root device name
				Ebs: &types.EbsBlockDevice{
					VolumeSize:          aws.Int32(int32(req.BootVolumeSize)),
					VolumeType:          types.VolumeTypeGp3,
					DeleteOnTermination: aws.Bool(true),
					Encrypted:           aws.Bool(true),
				},
			},
		}
	}

	// Run the instance
	result, err := client.RunInstances(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to create instance")
	}

	if len(result.Instances) == 0 {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "InstanceCreationFailed",
			Message:  "no instance returned from RunInstances",
		}
	}

	instance := m.ec2InstanceToCPIInstance(&result.Instances[0])

	// Wait for instance to be running
	err = m.waitForInstanceState(ctx, instance.ID, types.InstanceStateNameRunning, instanceCreateTimeout)
	if err != nil {
		return nil, fmt.Errorf("instance failed to reach running state: %w", err)
	}

	// Refresh instance data to get assigned IPs
	return m.GetInstance(ctx, instance.ID)
}

// GetInstance retrieves an instance by ID.
//
//nolint:varnamelen // id is a standard parameter name
func (m *ComputeManager) GetInstance(ctx context.Context, id string) (*cpi.Instance, error) {
	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	result, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{id},
	})
	if err != nil {
		return nil, wrapError(err, "failed to get instance")
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  fmt.Sprintf("instance %s not found", id),
		}
	}

	return m.ec2InstanceToCPIInstance(&result.Reservations[0].Instances[0]), nil
}

// ListInstances lists all instances, optionally filtered.
func (m *ComputeManager) ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	input := &ec2.DescribeInstancesInput{}

	// Build tag filters, handling "name" specially for instances
	// buildAWSTagFilters treats "name" as an AWS-specific key (passes as-is),
	// but for instances we need "name" → "tag:Name"
	tagFilters := make(map[string]string, len(filters))

	var nameFilter string

	for k, v := range filters {
		if k == filterKeyName {
			nameFilter = v
		} else {
			tagFilters[k] = v
		}
	}

	ec2Filters := buildAWSTagFilters(tagFilters)

	if nameFilter != "" {
		ec2Filters = append(ec2Filters, types.Filter{
			Name:   aws.String("tag:Name"),
			Values: []string{nameFilter},
		})
	}

	// Always exclude terminated instances
	ec2Filters = append(ec2Filters, types.Filter{
		Name:   aws.String("instance-state-name"),
		Values: []string{"pending", "running", "stopping", "stopped"},
	})

	if len(ec2Filters) > 0 {
		input.Filters = ec2Filters
	}

	result, err := client.DescribeInstances(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to list instances")
	}

	var instances []*cpi.Instance //nolint:prealloc // size unknown until runtime

	for _, reservation := range result.Reservations {
		for i := range reservation.Instances {
			instances = append(instances, m.ec2InstanceToCPIInstance(&reservation.Instances[i]))
		}
	}

	return instances, nil
}

// StartInstance starts a stopped instance.
func (m *ComputeManager) StartInstance(ctx context.Context, instanceID string) error {
	client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	_, err = client.StartInstances(ctx, &ec2.StartInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return wrapError(err, "failed to start instance")
	}

	return m.waitForInstanceState(ctx, instanceID, types.InstanceStateNameRunning, instanceStartTimeout)
}

// StopInstance stops a running instance.
func (m *ComputeManager) StopInstance(ctx context.Context, instanceID string) error {
	client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	_, err = client.StopInstances(ctx, &ec2.StopInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return wrapError(err, "failed to stop instance")
	}

	return m.waitForInstanceState(ctx, instanceID, types.InstanceStateNameStopped, instanceStopTimeout)
}

// RebootInstance reboots a running instance.
//
//nolint:varnamelen // id is a standard parameter name
func (m *ComputeManager) RebootInstance(ctx context.Context, id string) error {
	client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	_, err = client.RebootInstances(ctx, &ec2.RebootInstancesInput{
		InstanceIds: []string{id},
	})
	if err != nil {
		return wrapError(err, "failed to reboot instance")
	}

	// Wait a moment for reboot to initiate
	time.Sleep(2 * time.Second) //nolint:mnd // brief delay for state transition

	// Wait for instance to be running again
	return m.waitForInstanceState(ctx, id, types.InstanceStateNameRunning, instanceStartTimeout)
}

// DeleteInstance terminates an instance.
//
//nolint:varnamelen // id is a standard parameter name
func (m *ComputeManager) DeleteInstance(ctx context.Context, id string) error {
	client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	_, err = client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{id},
	})
	if err != nil {
		if IsTerminationProtected(err) {
			return &cpi.ProviderError{
				Provider: "aws",
				Code:     "TerminationProtected",
				Message:  fmt.Sprintf("instance %s has termination protection enabled; disable it in the AWS console or via: aws ec2 modify-instance-attribute --instance-id %s --no-disable-api-termination", id, id),
			}
		}

		return wrapError(err, "failed to terminate instance")
	}

	return m.waitForInstanceState(ctx, id, types.InstanceStateNameTerminated, instanceDeleteTimeout)
}

// CreateKeyPair creates a new SSH key pair.
func (m *ComputeManager) CreateKeyPair(ctx context.Context, req *cpi.KeyPairRequest) (*cpi.KeyPair, error) {
	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	// Check if keypair already exists — return a duplicate error so the
	// caller's handleDuplicateKeyPair flow can reconcile with any local key.
	existingKeyPair, err := m.GetKeyPair(ctx, req.Name)
	if err == nil && existingKeyPair != nil {
		return nil, fmt.Errorf("InvalidKeyPair.Duplicate: %w: %s", ErrDuplicateKeyPair, req.Name)
	}

	// Default to ed25519 if not specified
	keyType := req.KeyType
	if keyType == "" {
		keyType = "ed25519"
	}

	input := &ec2.CreateKeyPairInput{
		KeyName: aws.String(req.Name),
		KeyType: types.KeyType(keyType),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeKeyPair,
				Tags:         buildTags(req.Name, req.Tags),
			},
		},
	}

	result, err := client.CreateKeyPair(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to create key pair")
	}

	return &cpi.KeyPair{
		ID:          aws.ToString(result.KeyPairId),
		Name:        aws.ToString(result.KeyName),
		Fingerprint: aws.ToString(result.KeyFingerprint),
		PublicKey:   "", // AWS doesn't return public key for created pairs
		PrivateKey:  aws.ToString(result.KeyMaterial),
		CreatedAt:   time.Now(),
	}, nil
}

// ImportKeyPair imports an existing public key.
func (m *ComputeManager) ImportKeyPair(ctx context.Context, name string, publicKey string) error {
	client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	_, err = client.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{
		KeyName:           aws.String(name),
		PublicKeyMaterial: []byte(publicKey),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeKeyPair,
				Tags:         buildTags(name, nil),
			},
		},
	})
	if err != nil {
		return wrapError(err, "failed to import key pair")
	}

	return nil
}

// GetKeyPair retrieves a key pair by name.
func (m *ComputeManager) GetKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	result, err := client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{
		KeyNames: []string{name},
	})
	if err != nil {
		return nil, wrapError(err, "failed to get key pair")
	}

	if len(result.KeyPairs) == 0 {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  fmt.Sprintf("key pair %s not found", name),
		}
	}

	keyPair := result.KeyPairs[0]

	return &cpi.KeyPair{
		ID:          aws.ToString(keyPair.KeyPairId),
		Name:        aws.ToString(keyPair.KeyName),
		Fingerprint: aws.ToString(keyPair.KeyFingerprint),
		PublicKey:   aws.ToString(keyPair.PublicKey),
		PrivateKey:  "", // Never returned by AWS for security
		CreatedAt:   time.Now(),
	}, nil
}

// ListKeyPairs lists all key pairs.
func (m *ComputeManager) ListKeyPairs(ctx context.Context) ([]*cpi.KeyPair, error) {
	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	result, err := client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{})
	if err != nil {
		return nil, wrapError(err, "failed to list key pairs")
	}

	var keyPairs []*cpi.KeyPair //nolint:prealloc // size unknown until runtime

	for _, kp := range result.KeyPairs {
		keyPairs = append(keyPairs, &cpi.KeyPair{
			ID:          aws.ToString(kp.KeyPairId),
			Name:        aws.ToString(kp.KeyName),
			Fingerprint: aws.ToString(kp.KeyFingerprint),
			PublicKey:   aws.ToString(kp.PublicKey),
			CreatedAt:   time.Now(),
		})
	}

	return keyPairs, nil
}

// DeleteKeyPair deletes a key pair.
func (m *ComputeManager) DeleteKeyPair(ctx context.Context, name string) error {
	client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	_, err = client.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{
		KeyName: aws.String(name),
	})
	if err != nil {
		return wrapError(err, "failed to delete key pair")
	}

	return nil
}

// CreateVolume creates an EBS volume (delegated to StorageManager).
func (m *ComputeManager) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	return m.client.storage.CreateVolume(ctx, req)
}

// GetVolume retrieves a volume (delegated to StorageManager).
func (m *ComputeManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) {
	return m.client.storage.GetVolume(ctx, id)
}

// ListVolumes lists volumes (delegated to StorageManager).
func (m *ComputeManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	return m.client.storage.ListVolumes(ctx, filters)
}

// DeleteVolume deletes a volume (delegated to StorageManager).
func (m *ComputeManager) DeleteVolume(ctx context.Context, id string) error {
	return m.client.storage.DeleteVolume(ctx, id)
}

// ListImages lists available AMIs.
func (m *ComputeManager) ListImages(ctx context.Context, filters map[string]string) ([]*cpi.Image, error) {
	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	input := &ec2.DescribeImagesInput{}

	// Build filters
	var ec2Filters []types.Filter

	for key, value := range filters {
		switch key {
		case filterKeyName:
			ec2Filters = append(ec2Filters, types.Filter{
				Name:   aws.String(filterKeyName),
				Values: []string{value},
			})
		case "owner":
			// Special handling for owner filter
			input.Owners = []string{value}
		case "architecture":
			ec2Filters = append(ec2Filters, types.Filter{
				Name:   aws.String("architecture"),
				Values: []string{value},
			})
		case "root-device-type":
			ec2Filters = append(ec2Filters, types.Filter{
				Name:   aws.String("root-device-type"),
				Values: []string{value},
			})
		case "virtualization-type":
			ec2Filters = append(ec2Filters, types.Filter{
				Name:   aws.String("virtualization-type"),
				Values: []string{value},
			})
		}
	}

	// Default to available images only
	ec2Filters = append(ec2Filters, types.Filter{
		Name:   aws.String("state"),
		Values: []string{"available"},
	})

	if len(ec2Filters) > 0 {
		input.Filters = ec2Filters
	}

	// Default to self-owned images if no owner specified
	if len(input.Owners) == 0 {
		input.Owners = []string{"self"}
	}

	result, err := client.DescribeImages(ctx, input)
	if err != nil {
		return nil, wrapError(err, "failed to list images")
	}

	var images []*cpi.Image //nolint:prealloc // size unknown until runtime

	for i := range result.Images {
		images = append(images, m.ec2ImageToCPIImage(&result.Images[i]))
	}

	return images, nil
}

// GetImage retrieves an image by ID.
func (m *ComputeManager) GetImage(ctx context.Context, imageID string) (*cpi.Image, error) {
	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	result, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		ImageIds: []string{imageID},
	})
	if err != nil {
		return nil, wrapError(err, "failed to get image")
	}

	if len(result.Images) == 0 {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  fmt.Sprintf("image %s not found", imageID),
		}
	}

	return m.ec2ImageToCPIImage(&result.Images[0]), nil
}

// ListFlavors lists available instance types.
func (m *ComputeManager) ListFlavors(ctx context.Context) ([]*cpi.Flavor, error) {
	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	// List all instance types in the region
	result, err := client.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{})
	if err != nil {
		return nil, wrapError(err, "failed to list instance types")
	}

	var flavors []*cpi.Flavor //nolint:prealloc // size unknown until runtime

	for i := range result.InstanceTypes {
		flavors = append(flavors, m.ec2InstanceTypeToCPIFlavor(&result.InstanceTypes[i]))
	}

	return flavors, nil
}

// GetFlavor retrieves an instance type by ID.
func (m *ComputeManager) GetFlavor(ctx context.Context, flavorID string) (*cpi.Flavor, error) {
	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	result, err := client.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{
		InstanceTypes: []types.InstanceType{types.InstanceType(flavorID)},
	})
	if err != nil {
		return nil, wrapError(err, "failed to get instance type")
	}

	if len(result.InstanceTypes) == 0 {
		return nil, &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  fmt.Sprintf("instance type %s not found", flavorID),
		}
	}

	return m.ec2InstanceTypeToCPIFlavor(&result.InstanceTypes[0]), nil
}

// Helper functions

// ec2InstanceToCPIInstance converts an EC2 instance to a CPI instance.
func (m *ComputeManager) ec2InstanceToCPIInstance(instance *types.Instance) *cpi.Instance {
	inst := &cpi.Instance{
		ID:               aws.ToString(instance.InstanceId),
		State:            ec2StateToResourceState(instance.State),
		Flavor:           string(instance.InstanceType),
		Image:            aws.ToString(instance.ImageId),
		AvailabilityZone: aws.ToString(instance.Placement.AvailabilityZone),
		PrivateIP:        aws.ToString(instance.PrivateIpAddress),
		PublicIP:         aws.ToString(instance.PublicIpAddress),
		Tags:             extractTags(instance.Tags),
		CreatedAt:        aws.ToTime(instance.LaunchTime),
		UpdatedAt:        time.Now(),
	}

	// Extract name from tags
	for _, tag := range instance.Tags {
		if aws.ToString(tag.Key) == "Name" {
			inst.Name = aws.ToString(tag.Value)

			break
		}
	}

	// Network information
	if instance.SubnetId != nil {
		inst.SubnetID = aws.ToString(instance.SubnetId)
	}

	if instance.VpcId != nil {
		inst.NetworkID = aws.ToString(instance.VpcId)
	}

	// Security groups
	for _, sg := range instance.SecurityGroups {
		inst.SecurityGroups = append(inst.SecurityGroups, aws.ToString(sg.GroupId))
	}

	// Key pair
	if instance.KeyName != nil {
		inst.KeyPair = aws.ToString(instance.KeyName)
	}

	// Volume information
	for _, bdm := range instance.BlockDeviceMappings {
		if bdm.Ebs != nil && bdm.Ebs.VolumeId != nil {
			inst.Volumes = append(inst.Volumes, aws.ToString(bdm.Ebs.VolumeId))
		}
	}

	return inst
}

// ec2ImageToCPIImage converts an EC2 image to a CPI image.
func (m *ComputeManager) ec2ImageToCPIImage(image *types.Image) *cpi.Image {
	img := &cpi.Image{
		ID:           aws.ToString(image.ImageId),
		Name:         aws.ToString(image.Name),
		Description:  aws.ToString(image.Description),
		Architecture: string(image.Architecture),
		Public:       aws.ToBool(image.Public),
		State:        string(image.State),
		Tags:         extractTags(image.Tags),
		CreatedAt:    time.Now(), // AWS provides creation date as a string
	}

	// Parse platform details for OS information
	if image.PlatformDetails != nil {
		img.OS = aws.ToString(image.PlatformDetails)
	}

	// Block device mappings for size info
	for _, bdm := range image.BlockDeviceMappings {
		if bdm.Ebs != nil && bdm.Ebs.VolumeSize != nil {
			const bytesPerGB = 1024 * 1024 * 1024 //nolint:mnd // bytes per gigabyte

			img.Size = int64(aws.ToInt32(bdm.Ebs.VolumeSize)) * bytesPerGB

			break
		}
	}

	return img
}

// ec2InstanceTypeToCPIFlavor converts an EC2 instance type to a CPI flavor.
func (m *ComputeManager) ec2InstanceTypeToCPIFlavor(instanceType *types.InstanceTypeInfo) *cpi.Flavor {
	flavor := &cpi.Flavor{
		ID:   string(instanceType.InstanceType),
		Name: string(instanceType.InstanceType),
	}

	// vCPU count
	if instanceType.VCpuInfo != nil && instanceType.VCpuInfo.DefaultVCpus != nil {
		flavor.VCPUs = int(aws.ToInt32(instanceType.VCpuInfo.DefaultVCpus))
	}

	// Memory in MB
	if instanceType.MemoryInfo != nil && instanceType.MemoryInfo.SizeInMiB != nil {
		flavor.RAM = int(aws.ToInt64(instanceType.MemoryInfo.SizeInMiB))
	}

	// Instance storage (ephemeral disk)
	if instanceType.InstanceStorageInfo != nil && instanceType.InstanceStorageInfo.TotalSizeInGB != nil {
		flavor.Ephemeral = int(aws.ToInt64(instanceType.InstanceStorageInfo.TotalSizeInGB))
	}

	// Network performance
	if instanceType.NetworkInfo != nil && instanceType.NetworkInfo.NetworkPerformance != nil {
		flavor.Description = "Network: " + aws.ToString(instanceType.NetworkInfo.NetworkPerformance) //nolint:perfsprint // string concat is clearer
	}

	return flavor
}

// ec2StateToResourceState converts EC2 instance state to resource state.
func ec2StateToResourceState(state *types.InstanceState) cpi.ResourceState {
	if state == nil {
		return cpi.ResourceStateUnknown
	}

	switch state.Name {
	case types.InstanceStateNamePending:
		return cpi.ResourceStateCreating
	case types.InstanceStateNameRunning:
		return cpi.ResourceStateActive
	case types.InstanceStateNameStopping:
		return cpi.ResourceStateDeleting
	case types.InstanceStateNameStopped:
		return cpi.ResourceStateStopped
	case types.InstanceStateNameShuttingDown:
		return cpi.ResourceStateDeleting
	case types.InstanceStateNameTerminated:
		return cpi.ResourceStateDeleted
	default:
		return cpi.ResourceStateUnknown
	}
}

// waitForInstanceState waits for an instance to reach the desired state.
func (m *ComputeManager) waitForInstanceState(ctx context.Context, instanceID string, desiredState types.InstanceStateName, timeout time.Duration) error {
	client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)

	ticker := time.NewTicker(5 * time.Second) //nolint:mnd // polling interval
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled: %w", ctx.Err()) //nolint:wrapcheck // wrapping context error
		case <-ticker.C:
			if time.Now().After(deadline) {
				//nolint:err113 // dynamic error with instance context is appropriate
				return fmt.Errorf("timeout waiting for instance %s to reach state %s", instanceID, desiredState)
			}

			result, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
				InstanceIds: []string{instanceID},
			})
			if err != nil {
				return wrapError(err, "failed to check instance state")
			}

			if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
				//nolint:err113 // dynamic error with instance ID is appropriate
				return fmt.Errorf("instance %s not found", instanceID)
			}

			instance := result.Reservations[0].Instances[0]
			if instance.State.Name == desiredState {
				return nil
			}

			// Check for error states
			if instance.State.Name == types.InstanceStateNameTerminated && desiredState != types.InstanceStateNameTerminated {
				//nolint:err113 // dynamic error with instance ID is appropriate
				return fmt.Errorf("instance %s was terminated", instanceID)
			}
		}
	}
}
