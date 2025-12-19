package azure

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v5"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CreateInstance creates a new virtual machine.
func (m *ComputeManager) CreateInstance(ctx context.Context, req *cpi.InstanceRequest) (*cpi.Instance, error) {
	if req == nil {
		return nil, ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	// Ensure resource group exists
	err = m.client.EnsureResourceGroup(ctx)
	if err != nil {
		return nil, err
	}

	// Create NIC first
	nicName := req.Name + "-nic"
	nic, err := m.createNetworkInterface(ctx, nicName, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create network interface: %w", err)
	}

	// Prepare VM parameters
	vmParams := armcompute.VirtualMachine{
		Location: to.Ptr(m.client.getLocation()),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{
				VMSize: to.Ptr(armcompute.VirtualMachineSizeTypes(req.Flavor)),
			},
			StorageProfile: &armcompute.StorageProfile{
				ImageReference: m.parseImageReference(req.Image),
				OSDisk: &armcompute.OSDisk{
					CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
					ManagedDisk: &armcompute.ManagedDiskParameters{
						StorageAccountType: to.Ptr(armcompute.StorageAccountTypesPremiumLRS),
					},
				},
			},
			OSProfile: &armcompute.OSProfile{
				ComputerName:  to.Ptr(req.Name),
				AdminUsername: to.Ptr("azureuser"),
				LinuxConfiguration: &armcompute.LinuxConfiguration{
					DisablePasswordAuthentication: to.Ptr(true),
					SSH: &armcompute.SSHConfiguration{
						PublicKeys: m.buildSSHKeys(req.KeyPair, req.KeyPairName),
					},
				},
			},
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkInterfaces: []*armcompute.NetworkInterfaceReference{
					{
						ID: nic.ID,
						Properties: &armcompute.NetworkInterfaceReferenceProperties{
							Primary: to.Ptr(true),
						},
					},
				},
			},
		},
		Tags: BuildTags(MergeTags(m.client.config.DefaultTags, req.Tags)),
	}

	// Add user data if provided
	if req.UserData != "" {
		if vmParams.Properties.OSProfile == nil {
			vmParams.Properties.OSProfile = &armcompute.OSProfile{}
		}
		vmParams.Properties.OSProfile.CustomData = to.Ptr(base64.StdEncoding.EncodeToString([]byte(req.UserData)))
	}

	// Add availability zone if specified
	if req.AvailabilityZone != "" && SupportsAvailabilityZones(m.client.getLocation()) {
		vmParams.Zones = []*string{to.Ptr(req.AvailabilityZone)}
	}

	// Create the VM
	poller, err := m.client.virtualMachinesClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		req.Name,
		vmParams,
		nil,
	)
	if err != nil {
		return nil, WrapAzureError(err, "CreateInstance")
	}

	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateInstance")
	}

	logger.Infow("Created virtual machine", "name", req.Name, "size", req.Flavor)

	return m.vmToInstance(&result.VirtualMachine), nil
}

// GetInstance retrieves a virtual machine by ID or name.
func (m *ComputeManager) GetInstance(ctx context.Context, id string) (*cpi.Instance, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	name := ExtractResourceName(id)

	result, err := m.client.virtualMachinesClient.Get(ctx, m.client.getResourceGroup(), name, &armcompute.VirtualMachinesClientGetOptions{
		Expand: to.Ptr(armcompute.InstanceViewTypesInstanceView),
	})
	if err != nil {
		return nil, WrapAzureError(err, "GetInstance")
	}

	return m.vmToInstance(&result.VirtualMachine), nil
}

// ListInstances lists all virtual machines.
func (m *ComputeManager) ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	pager := m.client.virtualMachinesClient.NewListPager(m.client.getResourceGroup(), nil)

	var instances []*cpi.Instance
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListInstances")
		}

		for _, vm := range page.Value {
			instance := m.vmToInstance(vm)
			if matchesInstanceFilters(instance.Tags, filters) {
				instances = append(instances, instance)
			}
		}
	}

	return instances, nil
}

// StartInstance starts a virtual machine.
func (m *ComputeManager) StartInstance(ctx context.Context, id string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	poller, err := m.client.virtualMachinesClient.BeginStart(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "StartInstance")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "StartInstance")
	}

	logger.Infow("Started virtual machine", "name", name)

	return nil
}

// StopInstance stops (deallocates) a virtual machine.
func (m *ComputeManager) StopInstance(ctx context.Context, id string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	poller, err := m.client.virtualMachinesClient.BeginDeallocate(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "StopInstance")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "StopInstance")
	}

	logger.Infow("Stopped virtual machine", "name", name)

	return nil
}

// RebootInstance restarts a virtual machine.
func (m *ComputeManager) RebootInstance(ctx context.Context, id string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	poller, err := m.client.virtualMachinesClient.BeginRestart(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "RebootInstance")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "RebootInstance")
	}

	logger.Infow("Rebooted virtual machine", "name", name)

	return nil
}

// DeleteInstance deletes a virtual machine.
func (m *ComputeManager) DeleteInstance(ctx context.Context, id string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	poller, err := m.client.virtualMachinesClient.BeginDelete(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteInstance")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteInstance")
	}

	logger.Infow("Deleted virtual machine", "name", name)

	return nil
}

// CreateKeyPair creates a new SSH public key resource.
func (m *ComputeManager) CreateKeyPair(ctx context.Context, req *cpi.KeyPairRequest) (*cpi.KeyPair, error) {
	if req == nil {
		return nil, ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	// Generate SSH key pair
	keyParams := armcompute.SSHPublicKeyResource{
		Location: to.Ptr(m.client.getLocation()),
		Tags:     BuildTags(MergeTags(m.client.config.DefaultTags, req.Tags)),
	}

	result, err := m.client.sshPublicKeysClient.Create(ctx, m.client.getResourceGroup(), req.Name, keyParams, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateKeyPair")
	}

	// Generate and retrieve the key pair
	generateResult, err := m.client.sshPublicKeysClient.GenerateKeyPair(ctx, m.client.getResourceGroup(), req.Name, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateKeyPair.GenerateKeyPair")
	}

	logger.Infow("Created SSH key pair", "name", req.Name)

	return &cpi.KeyPair{
		ID:         DerefString(result.ID),
		Name:       DerefString(result.Name),
		PublicKey:  DerefString(generateResult.PublicKey),
		PrivateKey: DerefString(generateResult.PrivateKey),
		CreatedAt:  time.Now(),
	}, nil
}

// ImportKeyPair imports an existing SSH public key.
func (m *ComputeManager) ImportKeyPair(ctx context.Context, name string, publicKey string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	keyParams := armcompute.SSHPublicKeyResource{
		Location: to.Ptr(m.client.getLocation()),
		Properties: &armcompute.SSHPublicKeyResourceProperties{
			PublicKey: to.Ptr(publicKey),
		},
	}

	_, err = m.client.sshPublicKeysClient.Create(ctx, m.client.getResourceGroup(), name, keyParams, nil)
	if err != nil {
		return WrapAzureError(err, "ImportKeyPair")
	}

	logger.Infow("Imported SSH key pair", "name", name)

	return nil
}

// GetKeyPair retrieves an SSH public key resource by name.
func (m *ComputeManager) GetKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	result, err := m.client.sshPublicKeysClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return nil, WrapAzureError(err, "GetKeyPair")
	}

	keyPair := &cpi.KeyPair{
		ID:        DerefString(result.ID),
		Name:      DerefString(result.Name),
		CreatedAt: time.Now(),
	}

	if result.Properties != nil {
		keyPair.PublicKey = DerefString(result.Properties.PublicKey)
	}

	return keyPair, nil
}

// ListKeyPairs lists all SSH public key resources.
func (m *ComputeManager) ListKeyPairs(ctx context.Context) ([]*cpi.KeyPair, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	pager := m.client.sshPublicKeysClient.NewListByResourceGroupPager(m.client.getResourceGroup(), nil)

	var keyPairs []*cpi.KeyPair
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListKeyPairs")
		}

		for _, key := range page.Value {
			keyPair := &cpi.KeyPair{
				ID:        DerefString(key.ID),
				Name:      DerefString(key.Name),
				CreatedAt: time.Now(),
			}
			if key.Properties != nil {
				keyPair.PublicKey = DerefString(key.Properties.PublicKey)
			}
			keyPairs = append(keyPairs, keyPair)
		}
	}

	return keyPairs, nil
}

// DeleteKeyPair deletes an SSH public key resource.
func (m *ComputeManager) DeleteKeyPair(ctx context.Context, name string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	_, err = m.client.sshPublicKeysClient.Delete(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteKeyPair")
	}

	logger.Infow("Deleted SSH key pair", "name", name)

	return nil
}

// CreateVolume creates a new managed disk (delegated to StorageManager).
func (m *ComputeManager) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	return m.client.storage.CreateVolume(ctx, req)
}

// GetVolume retrieves a managed disk (delegated to StorageManager).
func (m *ComputeManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) {
	return m.client.storage.GetVolume(ctx, id)
}

// ListVolumes lists all managed disks (delegated to StorageManager).
func (m *ComputeManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	return m.client.storage.ListVolumes(ctx, filters)
}

// DeleteVolume deletes a managed disk (delegated to StorageManager).
func (m *ComputeManager) DeleteVolume(ctx context.Context, id string) error {
	return m.client.storage.DeleteVolume(ctx, id)
}

// ListImages lists available VM images.
func (m *ComputeManager) ListImages(ctx context.Context, filters map[string]string) ([]*cpi.Image, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	// List custom images in the resource group
	pager := m.client.imagesClient.NewListByResourceGroupPager(m.client.getResourceGroup(), nil)

	var images []*cpi.Image
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListImages")
		}

		for _, img := range page.Value {
			image := m.imageToImage(img)
			if matchesImageFilters(image.Tags, filters) {
				images = append(images, image)
			}
		}
	}

	return images, nil
}

// GetImage retrieves an image by ID or name.
func (m *ComputeManager) GetImage(ctx context.Context, id string) (*cpi.Image, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	name := ExtractResourceName(id)

	result, err := m.client.imagesClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return nil, WrapAzureError(err, "GetImage")
	}

	return m.imageToImage(&result.Image), nil
}

// ListFlavors lists available VM sizes.
func (m *ComputeManager) ListFlavors(ctx context.Context) ([]*cpi.Flavor, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	pager := m.client.virtualMachineSizesClient.NewListPager(m.client.getLocation(), nil)

	var flavors []*cpi.Flavor
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListFlavors")
		}

		for _, size := range page.Value {
			flavors = append(flavors, m.vmSizeToFlavor(size))
		}
	}

	return flavors, nil
}

// GetFlavor retrieves a VM size by name.
func (m *ComputeManager) GetFlavor(ctx context.Context, id string) (*cpi.Flavor, error) {
	flavors, err := m.ListFlavors(ctx)
	if err != nil {
		return nil, err
	}

	for _, flavor := range flavors {
		if flavor.ID == id || flavor.Name == id {
			return flavor, nil
		}
	}

	return nil, ErrNotFound
}

// Helper functions

func (m *ComputeManager) createNetworkInterface(ctx context.Context, nicName string, req *cpi.InstanceRequest) (*armnetwork.Interface, error) {
	// Build NIC client
	nicClient, err := armnetwork.NewInterfacesClient(m.client.getSubscriptionID(), m.client.credential, m.client.armClientOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create NIC client: %w", err)
	}

	// Build IP configuration
	ipConfig := &armnetwork.InterfaceIPConfiguration{
		Name: to.Ptr("ipconfig1"),
		Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
			PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
		},
	}

	// Add subnet reference if provided
	if req.SubnetID != "" {
		ipConfig.Properties.Subnet = &armnetwork.Subnet{
			ID: to.Ptr(req.SubnetID),
		}
	}

	// Prepare NIC parameters
	nicParams := armnetwork.Interface{
		Location: to.Ptr(m.client.getLocation()),
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{ipConfig},
		},
		Tags: BuildTags(req.Tags),
	}

	// Add NSG if specified
	if len(req.SecurityGroups) > 0 || len(req.SecurityGroupIDs) > 0 {
		var nsgID string
		if len(req.SecurityGroupIDs) > 0 {
			nsgID = req.SecurityGroupIDs[0]
		} else {
			nsgID = req.SecurityGroups[0]
		}
		nicParams.Properties.NetworkSecurityGroup = &armnetwork.SecurityGroup{
			ID: to.Ptr(nsgID),
		}
	}

	// Enable accelerated networking if configured
	if m.client.config.EnableAcceleratedNetworking {
		nicParams.Properties.EnableAcceleratedNetworking = to.Ptr(true)
	}

	poller, err := nicClient.BeginCreateOrUpdate(ctx, m.client.getResourceGroup(), nicName, nicParams, nil)
	if err != nil {
		return nil, WrapAzureError(err, "createNetworkInterface")
	}

	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "createNetworkInterface")
	}

	return &result.Interface, nil
}

func (m *ComputeManager) parseImageReference(image string) *armcompute.ImageReference {
	// Check if it's a resource ID (custom image)
	if len(image) > 0 && image[0] == '/' {
		return &armcompute.ImageReference{
			ID: to.Ptr(image),
		}
	}

	// Parse marketplace image format: publisher:offer:sku:version
	// Example: Canonical:UbuntuServer:18.04-LTS:latest
	parts := splitImageParts(image)
	if len(parts) >= 4 {
		return &armcompute.ImageReference{
			Publisher: to.Ptr(parts[0]),
			Offer:     to.Ptr(parts[1]),
			SKU:       to.Ptr(parts[2]),
			Version:   to.Ptr(parts[3]),
		}
	}

	// Default to Ubuntu if no valid format
	return &armcompute.ImageReference{
		Publisher: to.Ptr("Canonical"),
		Offer:     to.Ptr("0001-com-ubuntu-server-jammy"),
		SKU:       to.Ptr("22_04-lts"),
		Version:   to.Ptr("latest"),
	}
}

func splitImageParts(image string) []string {
	var parts []string
	var current string

	for _, c := range image {
		if c == ':' {
			parts = append(parts, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}

	return parts
}

func (m *ComputeManager) buildSSHKeys(keyPair, keyPairName string) []*armcompute.SSHPublicKey {
	name := keyPair
	if name == "" {
		name = keyPairName
	}
	if name == "" {
		return nil
	}

	// Try to get the public key from the SSH public key resource
	ctx := context.Background()
	keyResource, err := m.GetKeyPair(ctx, name)
	if err == nil && keyResource.PublicKey != "" {
		return []*armcompute.SSHPublicKey{
			{
				Path:    to.Ptr("/home/azureuser/.ssh/authorized_keys"),
				KeyData: to.Ptr(keyResource.PublicKey),
			},
		}
	}

	return nil
}

func (m *ComputeManager) vmToInstance(vm *armcompute.VirtualMachine) *cpi.Instance {
	if vm == nil {
		return nil
	}

	instance := &cpi.Instance{
		ID:        DerefString(vm.ID),
		Name:      DerefString(vm.Name),
		Tags:      ExtractTags(vm.Tags),
		CreatedAt: time.Now(),
	}

	if vm.Properties != nil {
		// Hardware profile
		if vm.Properties.HardwareProfile != nil && vm.Properties.HardwareProfile.VMSize != nil {
			instance.Flavor = string(*vm.Properties.HardwareProfile.VMSize)
		}

		// Storage profile
		if vm.Properties.StorageProfile != nil && vm.Properties.StorageProfile.ImageReference != nil {
			imgRef := vm.Properties.StorageProfile.ImageReference
			if imgRef.ID != nil {
				instance.Image = DerefString(imgRef.ID)
			} else {
				instance.Image = fmt.Sprintf("%s:%s:%s:%s",
					DerefString(imgRef.Publisher),
					DerefString(imgRef.Offer),
					DerefString(imgRef.SKU),
					DerefString(imgRef.Version))
			}
		}

		// Instance view for state
		if vm.Properties.InstanceView != nil && vm.Properties.InstanceView.Statuses != nil {
			for _, status := range vm.Properties.InstanceView.Statuses {
				if status.Code != nil {
					code := DerefString(status.Code)
					if len(code) > 11 && code[:11] == "PowerState/" {
						instance.State = MapVMPowerStateToResourceState(code)
						break
					}
				}
			}
		}

		// Network profile
		if vm.Properties.NetworkProfile != nil && vm.Properties.NetworkProfile.NetworkInterfaces != nil {
			for _, nicRef := range vm.Properties.NetworkProfile.NetworkInterfaces {
				if nicRef.ID != nil {
					// Would need to query NIC to get IP addresses
					// For now, just note that NIC exists
				}
			}
		}
	}

	// Availability zone
	if vm.Zones != nil && len(vm.Zones) > 0 {
		instance.AvailabilityZone = DerefString(vm.Zones[0])
	}

	return instance
}

func (m *ComputeManager) imageToImage(img *armcompute.Image) *cpi.Image {
	if img == nil {
		return nil
	}

	image := &cpi.Image{
		ID:        DerefString(img.ID),
		Name:      DerefString(img.Name),
		Tags:      ExtractTags(img.Tags),
		CreatedAt: time.Now(),
	}

	if img.Properties != nil {
		if img.Properties.ProvisioningState != nil {
			image.State = string(*img.Properties.ProvisioningState)
		}
	}

	return image
}

func (m *ComputeManager) vmSizeToFlavor(size *armcompute.VirtualMachineSize) *cpi.Flavor {
	if size == nil {
		return nil
	}

	return &cpi.Flavor{
		ID:    DerefString(size.Name),
		Name:  DerefString(size.Name),
		VCPUs: int(DerefInt32(size.NumberOfCores)),
		RAM:   int(DerefInt32(size.MemoryInMB)),
		Disk:  int(DerefInt32(size.ResourceDiskSizeInMB) / 1024), // Convert MB to GB
	}
}

func matchesInstanceFilters(tags map[string]string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for key, value := range filters {
		if tagValue, ok := tags[key]; !ok || tagValue != value {
			return false
		}
	}

	return true
}

func matchesImageFilters(tags map[string]string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for key, value := range filters {
		if tagValue, ok := tags[key]; !ok || tagValue != value {
			return false
		}
	}

	return true
}
