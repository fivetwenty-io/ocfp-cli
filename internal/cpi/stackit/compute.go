package stackit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas"
)

const (
	defaultBootVolumeSize = 50 // Default boot volume size in GB
)

// CreateInstance creates a new compute instance.
func (m *ComputeManager) CreateInstance(ctx context.Context, req *cpi.InstanceRequest) (*cpi.Instance, error) {
	logger.WithOperation("CreateInstance").Infof("Creating instance via SDK: %s", req.Name)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	// If static IP requested, create NIC with static IP first, then create server
	if req.StaticPrivateIP != "" && req.NetworkID != "" {
		logger.WithOperation("CreateInstance").Infof("Creating NIC with static IP %s before server creation", req.StaticPrivateIP)

		nicID, err := m.createNICWithStaticIP(ctx, req.NetworkID, req.StaticPrivateIP, req.SecurityGroupIDs)
		if err != nil {
			logger.WithOperation("CreateInstance").Warnf("Failed to create NIC with static IP: %v, falling back to DHCP", err)
			// Continue with regular server creation (DHCP) as fallback
		} else {
			// Wait for NIC to be ready
			const nicReadyWaitDuration = 2 * time.Second
			time.Sleep(nicReadyWaitDuration)

			logger.WithOperation("CreateInstance").Infof("NIC created with static IP, creating server with attached NIC: %s", nicID)

			// Create server with pre-created NIC
			return m.createServerWithNIC(ctx, req, nicID)
		}
	}

	// Regular server creation (with DHCP IP assignment)
	payload := m.buildCreateServerPayload(req, "")

	created, err := cli.CreateServer(ctx, m.client.config.ProjectID).CreateServerPayload(*payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas CreateServer failed: %w", err)
	}

	inst := m.serverResponseToInstance(created)

	err = m.waitForInstanceState(ctx, inst.ID, cpi.ResourceStateActive, instanceWaitTimeout)
	if err != nil {
		return nil, fmt.Errorf("instance failed to become active: %w", err)
	}

	logger.WithOperation("CreateInstance").Infof("Instance created: %s (%s)", inst.Name, inst.ID)

	return inst, nil
}

// buildCreateServerPayload builds the server creation payload.
// If nicID is provided, the server will be created with that specific NIC (for static IP assignment).
// If nicID is empty, the server will be created with a default DHCP NIC.
func (m *ComputeManager) buildCreateServerPayload(req *cpi.InstanceRequest, nicID string) *iaas.CreateServerPayload {
	payload := iaas.NewCreateServerPayload(req.Flavor, req.Name)

	m.configureBootVolumeOrImage(payload, req)
	m.configureServerMetadata(payload, req)
	m.configureServerNetworking(payload, req, nicID)

	if req.UserData != "" {
		// SDK expects bytes; it will base64-encode for transport
		payload.SetUserData([]byte(req.UserData))
	}

	return payload
}

// configureBootVolumeOrImage configures boot volume for diskless flavors or image for regular flavors.
func (m *ComputeManager) configureBootVolumeOrImage(payload *iaas.CreateServerPayload, req *cpi.InstanceRequest) {
	// For STACKIT diskless flavors, use boot volume instead of direct image
	if req.UseBootVolume && req.Image != "" {
		bootVolume := m.buildBootVolume(req)
		payload.SetBootVolume(bootVolume)
	} else if req.Image != "" {
		// Regular image-based creation for flavors with local disk
		payload.SetImageId(req.Image)
	}
}

// buildBootVolume builds boot volume configuration for diskless flavors.
func (m *ComputeManager) buildBootVolume(req *cpi.InstanceRequest) iaas.CreateServerPayloadBootVolume {
	bootVolume := iaas.CreateServerPayloadBootVolume{}

	// Set boot volume source (the image ID)
	imageID := req.Image
	sourceType := "image"
	bootVolumeSource := iaas.BootVolumeSource{
		Id:   &imageID,
		Type: &sourceType,
	}
	bootVolume.SetSource(bootVolumeSource)

	// Set boot volume size if specified, otherwise use default
	bootVolumeSize := int64(defaultBootVolumeSize)
	if req.BootVolumeSize > 0 {
		bootVolumeSize = int64(req.BootVolumeSize)
	}

	bootVolume.SetSize(bootVolumeSize)

	// Don't delete the volume on termination
	bootVolume.SetDeleteOnTermination(false)

	return bootVolume
}

// configureServerMetadata configures server metadata (keypair, zone, labels).
func (m *ComputeManager) configureServerMetadata(payload *iaas.CreateServerPayload, req *cpi.InstanceRequest) {
	if req.KeyPair != "" {
		payload.SetKeypairName(req.KeyPair)
	}

	if req.AvailabilityZone != "" {
		payload.SetAvailabilityZone(req.AvailabilityZone)
	}

	if len(req.Tags) > 0 {
		// Use sanitization to ensure labels comply with STACKIT requirements
		labels := sanitizeLabelsForStackit(req.Tags)
		payload.SetLabels(labels)
	}
}

// configureServerNetworking configures server networking (NIC or DHCP).
func (m *ComputeManager) configureServerNetworking(payload *iaas.CreateServerPayload, req *cpi.InstanceRequest, nicID string) {
	// Configure networking - either with pre-created NIC (static IP) or default DHCP
	if nicID != "" {
		// Use pre-created NIC with static IP (matching STACKIT CLI pattern)
		logger.WithOperation("buildCreateServerPayload").Infof("Creating server with pre-created NIC: %s", nicID)

		netWithNics := iaas.NewCreateServerNetworkingWithNics()
		nicIDs := []string{nicID}
		netWithNics.SetNicIds(nicIDs)

		networking := iaas.CreateServerNetworkingWithNicsAsCreateServerPayloadNetworking(netWithNics)
		payload.SetNetworking(networking)
	} else if req.NetworkID != "" {
		// Default DHCP networking with NetworkID
		logger.WithOperation("buildCreateServerPayload").Infof("Creating server with default DHCP on network: %s", req.NetworkID)

		net := iaas.NewCreateServerNetworking()
		net.SetNetworkId(req.NetworkID)
		networking := iaas.CreateServerNetworkingAsCreateServerPayloadNetworking(net)
		payload.SetNetworking(networking)
	}
}

// serverResponseToInstance converts a server response to an instance.
func (m *ComputeManager) serverResponseToInstance(server interface {
	GetIdOk() (string, bool)
	GetNameOk() (string, bool)
	GetMachineTypeOk() (string, bool)
	GetImageIdOk() (string, bool)
	GetAvailabilityZoneOk() (string, bool)
	GetKeypairNameOk() (string, bool)
	GetLabelsOk() (map[string]interface{}, bool)
	GetStatusOk() (string, bool)
}) *cpi.Instance {
	return m.populateInstanceFromServer(server)
}

// GetInstance retrieves an instance by ID.
func (m *ComputeManager) GetInstance(ctx context.Context, instanceID string) (*cpi.Instance, error) {
	logger.WithOperation("GetInstance").Debugf("Getting instance via SDK: %s", instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	server, err := cli.GetServer(ctx, m.client.config.ProjectID, instanceID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetServer failed: %w", err)
	}

	inst := m.serverResponseToInstance(server)

	// Populate networking info
	err = m.populateInstanceNetworking(ctx, cli, instanceID, inst)
	if err != nil {
		// Log but don't fail - networking info is supplementary
		logger.WithOperation("GetInstance").Debugf("Failed to populate networking: %v", err)
	}

	return inst, nil
}

// populateInstanceNetworking populates networking info for an instance.
func (m *ComputeManager) populateInstanceNetworking(ctx context.Context, cli *iaas.APIClient, instanceID string, inst *cpi.Instance) error {
	// Build map from NIC->PublicIP
	pmap := m.buildPublicIPMap(ctx, cli)

	nicsResp, err := cli.ListServerNics(ctx, m.client.config.ProjectID, instanceID).Execute()
	if err != nil {
		return fmt.Errorf("failed to list server NICs: %w", err)
	}

	if nics, ok := nicsResp.GetItemsOk(); ok {
		m.processNetworkInterfaces(nics, pmap, inst)
	}

	return nil
}

// processNetworkInterfaces processes network interfaces and populates instance networking info.
func (m *ComputeManager) processNetworkInterfaces(nics []iaas.NIC, pmap map[string]string, inst *cpi.Instance) {
	for _, nic := range nics {
		m.processNetworkInterface(nic, pmap, inst)
	}
}

// processNetworkInterface processes a single network interface.
func (m *ComputeManager) processNetworkInterface(nic iaas.NIC, pmap map[string]string, inst *cpi.Instance) {
	if ip, ok := nic.GetIpv4Ok(); ok && inst.PrivateIP == "" {
		inst.PrivateIP = ip
	}

	if netID, ok := nic.GetNetworkIdOk(); ok && inst.NetworkID == "" {
		inst.NetworkID = netID
	}

	if nicID, ok := nic.GetIdOk(); ok {
		if pip, exists := pmap[nicID]; exists {
			inst.PublicIP, inst.FloatingIP = pip, pip
		}
	}
}

// buildPublicIPMap builds a map from NIC ID to public IP.
func (m *ComputeManager) buildPublicIPMap(ctx context.Context, cli *iaas.APIClient) map[string]string {
	pmap := make(map[string]string)

	pipResp, err := cli.ListPublicIPs(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return pmap
	}

	if items, ok := pipResp.GetItemsOk(); ok {
		for _, ip := range items {
			if ni, ok := ip.GetNetworkInterfaceOk(); ok && ni != nil && *ni != "" {
				if val, vok := ip.GetIpOk(); vok {
					pmap[*ni] = val
				}
			}
		}
	}

	return pmap
}

// ListInstances lists all instances.
func (m *ComputeManager) ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	logger.WithOperation("ListInstances").Debug("Listing instances via SDK")

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	// List servers with optional label selector
	resp, err := m.listServersWithFilters(ctx, cli, filters)
	if err != nil {
		return nil, err
	}

	// Build NIC->PublicIP map once
	publicIPsByNIC := m.buildPublicIPMap(ctx, cli)

	// Convert servers to instances
	instances := m.serversToInstances(ctx, cli, resp, publicIPsByNIC)

	logger.WithOperation("ListInstances").Debugf("Found %d instances", len(instances))

	return instances, nil
}

// listServersWithFilters lists servers applying the given filters.
func (m *ComputeManager) listServersWithFilters(ctx context.Context, cli *iaas.APIClient, filters map[string]string) (*iaas.ServerListResponse, error) {
	selector := m.buildLabelSelector(filters)

	req := cli.ListServers(ctx, m.client.config.ProjectID)
	if selector != "" {
		req = req.LabelSelector(selector)
	}

	resp, err := req.Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListServers failed: %w", err)
	}

	return resp, nil
}

// buildLabelSelector builds a label selector from filters.
func (m *ComputeManager) buildLabelSelector(filters map[string]string) string {
	selectors := make([]string, 0, len(filters))

	for k, v := range filters {
		if strings.HasPrefix(k, "label.") {
			selectors = append(selectors, fmt.Sprintf("%s=%s", strings.TrimPrefix(k, "label."), v))
		} else if strings.HasPrefix(k, "label:") {
			selectors = append(selectors, fmt.Sprintf("%s=%s", strings.TrimPrefix(k, "label:"), v))
		}
	}

	return strings.Join(selectors, ",")
}

// serversToInstances converts server responses to instances.
func (m *ComputeManager) serversToInstances(ctx context.Context, cli *iaas.APIClient, resp *iaas.ServerListResponse, publicIPsByNIC map[string]string) []*cpi.Instance {
	items, _ := resp.GetItemsOk()
	out := make([]*cpi.Instance, 0, len(items))

	for _, server := range items {
		inst := m.serverToInstance(server)

		// Populate networking for each instance
		if inst.ID != "" {
			m.populateInstanceNetworkingFromList(ctx, cli, inst.ID, inst, publicIPsByNIC)
		}

		out = append(out, inst)
	}

	return out
}

// serverToInstance converts a server object to an instance.
func (m *ComputeManager) serverToInstance(server iaas.Server) *cpi.Instance {
	return m.populateInstanceFromServer(&server)
}

// populateInstanceFromServer populates a CPI instance from a server object that implements the required interface.
//
//nolint:funlen // STACKIT SDK integration requires comprehensive field mapping
func (m *ComputeManager) populateInstanceFromServer(server interface {
	GetIdOk() (string, bool)
	GetNameOk() (string, bool)
	GetMachineTypeOk() (string, bool)
	GetImageIdOk() (string, bool)
	GetAvailabilityZoneOk() (string, bool)
	GetKeypairNameOk() (string, bool)
	GetLabelsOk() (map[string]interface{}, bool)
	GetStatusOk() (string, bool)
}) *cpi.Instance {
	inst := &cpi.Instance{
		ID:               "",
		Name:             "",
		State:            cpi.ResourceStateUnknown,
		Flavor:           "",
		Image:            "",
		NetworkID:        "",
		SubnetID:         "",
		PrivateIP:        "",
		PublicIP:         "",
		FloatingIP:       "",
		SecurityGroups:   []string{},
		KeyPair:          "",
		AvailabilityZone: "",
		Tags:             map[string]string{},
		Volumes:          []string{},
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if serverID, ok := server.GetIdOk(); ok {
		inst.ID = serverID
	}

	if name, ok := server.GetNameOk(); ok {
		inst.Name = name
	}

	if machineType, ok := server.GetMachineTypeOk(); ok {
		inst.Flavor = machineType
	}

	if imageID, ok := server.GetImageIdOk(); ok {
		inst.Image = imageID
	}

	if availabilityZone, ok := server.GetAvailabilityZoneOk(); ok {
		inst.AvailabilityZone = availabilityZone
	}

	if keypairName, ok := server.GetKeypairNameOk(); ok {
		inst.KeyPair = keypairName
	}

	if labels, ok := server.GetLabelsOk(); ok {
		inst.Tags = mapAnyToString(labels)
	}

	if status, ok := server.GetStatusOk(); ok {
		inst.State = mapServerStatus(status)
	}

	return inst
}

// populateInstanceNetworkingFromList populates networking info from cached public IPs.
func (m *ComputeManager) populateInstanceNetworkingFromList(ctx context.Context, cli *iaas.APIClient, instanceID string, inst *cpi.Instance, publicIPsByNIC map[string]string) {
	nicsResp, err := cli.ListServerNics(ctx, m.client.config.ProjectID, instanceID).Execute()
	if err != nil {
		return
	}

	if nics, ok := nicsResp.GetItemsOk(); ok {
		m.processNetworkInterfacesList(nics, publicIPsByNIC, inst)
	}
}

// processNetworkInterfacesList processes network interfaces and populates instance networking info from cached public IPs.
func (m *ComputeManager) processNetworkInterfacesList(nics []iaas.NIC, publicIPsByNIC map[string]string, inst *cpi.Instance) {
	for _, nic := range nics {
		m.processNetworkInterfaceFromList(nic, publicIPsByNIC, inst)
	}
}

// processNetworkInterfaceFromList processes a single network interface from cached public IPs.
func (m *ComputeManager) processNetworkInterfaceFromList(nic iaas.NIC, publicIPsByNIC map[string]string, inst *cpi.Instance) {
	if ip, ok := nic.GetIpv4Ok(); ok && inst.PrivateIP == "" {
		inst.PrivateIP = ip
	}

	if nicID, ok := nic.GetIdOk(); ok {
		if pip, exists := publicIPsByNIC[nicID]; exists {
			inst.PublicIP = pip
			inst.FloatingIP = pip
		}
	}

	if netID, ok := nic.GetNetworkIdOk(); ok && inst.NetworkID == "" {
		inst.NetworkID = netID
	}
}

// mapServerStatus maps STACKIT server status to cpi.ResourceState.
func mapServerStatus(status string) cpi.ResourceState {
	switch strings.ToUpper(status) {
	case "ACTIVE":
		return cpi.ResourceStateActive
	case "STOPPED", "INACTIVE":
		return cpi.ResourceStateStopped
	case "CREATING", "STARTING", "REBUILDING", "RESIZING":
		return cpi.ResourceStateCreating
	case "SNAPSHOTTING":
		return cpi.ResourceStateInUse
	default:
		return cpi.ResourceState(status)
	}
}

// DeleteInstance deletes an instance.
func (m *ComputeManager) DeleteInstance(ctx context.Context, instanceID string) error {
	logger.WithOperation("DeleteInstance").Infof("Deleting instance: %s", instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	err = cli.DeleteServer(ctx, m.client.config.ProjectID, instanceID).Execute()
	if err != nil {
		return fmt.Errorf("stackit iaas DeleteServer failed: %w", err)
	}

	logger.WithOperation("DeleteInstance").Infof("Instance deleted: %s", instanceID)

	return nil
}

// CreateKeyPair creates a new SSH key pair.
func (m *ComputeManager) CreateKeyPair(ctx context.Context, req *cpi.KeyPairRequest) (*cpi.KeyPair, error) {
	return nil, ErrCreateKeyPairUnsupported
}

// ImportKeyPair imports an existing public key.
func (m *ComputeManager) ImportKeyPair(ctx context.Context, name string, publicKey string) error {
	logger.WithOperation("ImportKeyPair").Infof("Importing key pair: %s", name)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	payload := iaas.NewCreateKeyPairPayload(publicKey)
	if name != "" {
		payload.SetName(name)
	}

	_, err = cli.CreateKeyPair(ctx).CreateKeyPairPayload(*payload).Execute()
	if err != nil {
		return fmt.Errorf("stackit iaas CreateKeyPair failed: %w", err)
	}

	return nil
}

// GetKeyPair retrieves a key pair by name.
func (m *ComputeManager) GetKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	logger.WithOperation("GetKeyPair").Debugf("Getting key pair via SDK: %s", name)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	keyPair, err := cli.GetKeyPair(ctx, name).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetKeyPair failed: %w", err)
	}

	out := &cpi.KeyPair{
		ID:          name, // STACKIT uses name as ID
		Name:        stringOrEmpty(keyPair.GetNameOk()),
		Fingerprint: stringOrEmpty(keyPair.GetFingerprintOk()),
		PublicKey:   stringOrEmpty(keyPair.GetPublicKeyOk()),
		PrivateKey:  "",
		CreatedAt:   time.Now(),
	}

	return out, nil
}

// ListKeyPairs lists all key pairs.
func (m *ComputeManager) ListKeyPairs(ctx context.Context) ([]*cpi.KeyPair, error) {
	logger.WithOperation("ListKeyPairs").Debug("Listing key pairs via SDK")

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	resp, err := cli.ListKeyPairs(ctx).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListKeyPairs failed: %w", err)
	}

	items, _ := resp.GetItemsOk()

	out := make([]*cpi.KeyPair, 0, len(items))
	for _, keyPair := range items {
		keyName := stringOrEmpty(keyPair.GetNameOk())
		out = append(out, &cpi.KeyPair{
			ID:          keyName, // STACKIT uses name as ID
			Name:        keyName,
			Fingerprint: stringOrEmpty(keyPair.GetFingerprintOk()),
			PublicKey:   stringOrEmpty(keyPair.GetPublicKeyOk()),
			PrivateKey:  "",
			CreatedAt:   time.Now(),
		})
	}

	return out, nil
}

// DeleteKeyPair deletes a key pair.
func (m *ComputeManager) DeleteKeyPair(ctx context.Context, name string) error {
	logger.WithOperation("DeleteKeyPair").Infof("Deleting key pair: %s", name)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	err = cli.DeleteKeyPair(ctx, name).Execute()
	if err != nil {
		return fmt.Errorf("stackit iaas DeleteKeyPair failed: %w", err)
	}

	return nil
}

// waitForInstanceState waits for an instance to reach a specific state.
func (m *ComputeManager) waitForInstanceState(ctx context.Context, instanceID string, targetState cpi.ResourceState, timeout time.Duration) error {
	err := cpi.WaitForCondition(ctx, conditionCheckInterval, timeout, func() (bool, error) {
		instance, err := m.GetInstance(ctx, instanceID)
		if err != nil {
			return false, err
		}

		return instance.State == targetState, nil
	})
	if err != nil {
		return fmt.Errorf("failed to wait for instance %s to reach state %s: %w", instanceID, targetState, err)
	}

	return nil
}

// StartInstance starts a stopped instance.
func (m *ComputeManager) StartInstance(ctx context.Context, instanceID string) error {
	logger.WithOperation("StartInstance").Infof("Starting instance: %s", instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return fmt.Errorf("failed to get IAAS client: %w", err)
	}

	err = cli.StartServer(ctx, m.client.config.ProjectID, instanceID).Execute()
	if err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	return nil
}

// StopInstance stops a running instance.
func (m *ComputeManager) StopInstance(ctx context.Context, instanceID string) error {
	logger.WithOperation("StopInstance").Infof("Stopping instance: %s", instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return fmt.Errorf("failed to get IAAS client: %w", err)
	}

	err = cli.StopServer(ctx, m.client.config.ProjectID, instanceID).Execute()
	if err != nil {
		return fmt.Errorf("failed to stop server: %w", err)
	}

	return nil
}

// RebootInstance reboots an instance.
func (m *ComputeManager) RebootInstance(ctx context.Context, instanceID string) error {
	logger.WithOperation("RebootInstance").Infof("Rebooting instance: %s", instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return fmt.Errorf("failed to get IAAS client: %w", err)
	}

	err = cli.RebootServer(ctx, m.client.config.ProjectID, instanceID).Action("SOFT").Execute()
	if err != nil {
		return fmt.Errorf("failed to reboot server: %w", err)
	}

	return nil
}

// ListImages lists available images.
func (m *ComputeManager) ListImages(ctx context.Context, filters map[string]string) ([]*cpi.Image, error) {
	logger.WithOperation("ListImages").Debug("Listing images via SDK")

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	resp, err := cli.ListImages(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListImages failed: %w", err)
	}

	items, _ := resp.GetItemsOk()

	out := make([]*cpi.Image, 0, len(items))
	for _, it := range items {
		img := &cpi.Image{
			ID:           stringOrEmpty(it.GetIdOk()),
			Name:         stringOrEmpty(it.GetNameOk()),
			Description:  "",
			OS:           "",
			OSVersion:    "",
			Architecture: "",
			Size:         0,
			MinDisk:      0,
			MinRAM:       0,
			Public:       true,
			State:        "",
			Tags:         map[string]string{},
			CreatedAt:    time.Now(),
		}
		out = append(out, img)
	}

	return out, nil
}

// GetImage retrieves an image.
func (m *ComputeManager) GetImage(ctx context.Context, imageID string) (*cpi.Image, error) {
	logger.WithOperation("GetImage").Debugf("Getting image via SDK: %s", imageID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	got, err := cli.GetImage(ctx, m.client.config.ProjectID, imageID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetImage failed: %w", err)
	}

	out := &cpi.Image{
		ID:           stringOrEmpty(got.GetIdOk()),
		Name:         stringOrEmpty(got.GetNameOk()),
		Description:  "",
		OS:           "",
		OSVersion:    "",
		Architecture: "",
		Size:         0,
		MinDisk:      0,
		MinRAM:       0,
		Public:       true,
		State:        "",
		Tags:         map[string]string{},
		CreatedAt:    time.Now(),
	}

	return out, nil
}

// ListFlavors lists available flavors.
//
//nolint:funlen // STACKIT SDK flavor mapping and debug logging requires comprehensive processing
func (m *ComputeManager) ListFlavors(ctx context.Context) ([]*cpi.Flavor, error) {
	logger.WithOperation("ListFlavors").Debug("Listing machine types via SDK")

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	resp, err := cli.ListMachineTypes(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListMachineTypes failed: %w", err)
	}

	items, _ := resp.GetItemsOk()

	const maxSampleMachineTypes = 10

	// Debug: Log what machine types were returned
	logger.Infof("STACKIT ListMachineTypes returned %d machine types", len(items))

	sampleCount := len(items)
	if sampleCount > maxSampleMachineTypes {
		sampleCount = maxSampleMachineTypes
	}

	foundG1a2d := false

	for i := range sampleCount {
		if name, ok := items[i].GetNameOk(); ok {
			logger.Infof("  Sample machine type [%d]: %s", i+1, name)

			if name == "g1a.2d" {
				foundG1a2d = true
			}
		}
	}
	// Check if g1a.2d exists in the full list
	if !foundG1a2d {
		for _, mt := range items {
			if name, ok := mt.GetNameOk(); ok && name == "g1a.2d" {
				foundG1a2d = true

				break
			}
		}
	}

	logger.Infof("STACKIT ListMachineTypes: g1a.2d found = %v", foundG1a2d)

	out := make([]*cpi.Flavor, 0, len(items))
	for _, machineType := range items {
		flavorName := stringOrEmpty(machineType.GetNameOk())

		diskSize := 0
		if d, ok := machineType.GetDiskOk(); ok {
			diskSize = int(d)
		}

		// Debug logging for STACKIT machine types
		logger.Debugf("STACKIT Machine Type: name='%s', disk=%dGB", flavorName, diskSize)

		flavor := &cpi.Flavor{
			ID:          flavorName,
			Name:        flavorName,
			VCPUs:       0,
			RAM:         0,
			Disk:        diskSize,
			Ephemeral:   0,
			NetworkCap:  0,
			Description: "",
		}
		if v, ok := machineType.GetVcpusOk(); ok {
			flavor.VCPUs = int(v)
		}

		if r, ok := machineType.GetRamOk(); ok {
			flavor.RAM = int(r)
		}

		out = append(out, flavor)
	}

	return out, nil
}

// GetFlavor retrieves a flavor.
func (m *ComputeManager) GetFlavor(ctx context.Context, flavorID string) (*cpi.Flavor, error) {
	logger.WithOperation("GetFlavor").Debugf("Getting machine type via SDK: %s", flavorID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	machineType, err := cli.GetMachineType(ctx, m.client.config.ProjectID, flavorID).Execute()
	if err != nil {
		logger.WithOperation("GetFlavor").Infof("GetMachineType API error for '%s' in project '%s': %v",
			flavorID, m.client.config.ProjectID, err)

		return nil, fmt.Errorf("stackit iaas GetMachineType failed: %w", err)
	}

	flavor := &cpi.Flavor{
		ID:          stringOrEmpty(machineType.GetNameOk()),
		Name:        stringOrEmpty(machineType.GetNameOk()),
		VCPUs:       0,
		RAM:         0,
		Disk:        0,
		Ephemeral:   0,
		NetworkCap:  0,
		Description: "",
	}
	if v, ok := machineType.GetVcpusOk(); ok {
		flavor.VCPUs = int(v)
	}

	if r, ok := machineType.GetRamOk(); ok {
		flavor.RAM = int(r)
	}

	if d, ok := machineType.GetDiskOk(); ok {
		flavor.Disk = int(d)
	}

	return flavor, nil
}

// CreateVolume creates a new volume.
func (m *ComputeManager) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	return m.client.storage.CreateVolume(ctx, req)
}

// GetVolume retrieves a volume by ID.
func (m *ComputeManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) {
	return m.client.storage.GetVolume(ctx, id)
}

// ListVolumes lists all volumes.
func (m *ComputeManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	return m.client.storage.ListVolumes(ctx, filters)
}

// DeleteVolume deletes a volume.
func (m *ComputeManager) DeleteVolume(ctx context.Context, id string) error {
	return m.client.storage.DeleteVolume(ctx, id)
}

// ==============================================================================
// Static IP Support - Matches Perl Implementation
// ==============================================================================

// createNICWithStaticIP creates a network interface with a specific IPv4 address.
// This matches the Perl implementation's behavior of creating NICs with predetermined IPs.
func (m *ComputeManager) createNICWithStaticIP(ctx context.Context, networkID, ipv4 string, securityGroups []string) (string, error) {
	logger.WithOperation("createNICWithStaticIP").Infof("Creating NIC with static IP %s on network %s", ipv4, networkID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return "", fmt.Errorf("failed to get IAAS client: %w", err)
	}

	// Build NIC payload with static IP
	// NOTE: networkId is passed as URL parameter to CreateNic(), NOT in the payload
	// STACKIT API returns 400 "readOnly property networkId" if included in payload
	nicPayload := iaas.NewCreateNicPayload()
	nicPayload.SetIpv4(ipv4) // Static IP assignment!

	if len(securityGroups) > 0 {
		sgList := make([]string, len(securityGroups))
		copy(sgList, securityGroups)
		nicPayload.SetSecurityGroups(sgList)
	}

	// Create NIC with static IP (networkID passed as URL parameter)
	resp, err := cli.CreateNic(ctx, m.client.config.ProjectID, networkID).CreateNicPayload(*nicPayload).Execute()
	if err != nil {
		return "", fmt.Errorf("failed to create NIC with static IP %s: %w", ipv4, err)
	}

	nicID, ok := resp.GetIdOk()
	if !ok || nicID == "" {
		return "", ErrNICCreatedButNoID
	}

	logger.WithOperation("createNICWithStaticIP").Infof("NIC created successfully: ID=%s, IP=%s", nicID, ipv4)

	return nicID, nil
}

// createServerWithNIC creates a server with a pre-created NIC that has a static IP.
// This is the STACKIT-specific flow for static IP assignment matching Perl implementation.
// The NIC ID is passed in the server creation payload (matching STACKIT CLI pattern).
func (m *ComputeManager) createServerWithNIC(ctx context.Context, req *cpi.InstanceRequest, nicID string) (*cpi.Instance, error) {
	logger.WithOperation("createServerWithNIC").Infof("Creating server %s with pre-created NIC %s", req.Name, nicID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, fmt.Errorf("failed to get IAAS client: %w", err)
	}

	// Build server payload WITH the NIC ID (matching STACKIT CLI pattern)
	// This ensures the server is created with the static IP NIC from the start
	payload := m.buildCreateServerPayload(req, nicID)

	// Create server with the pre-created NIC
	created, err := cli.CreateServer(ctx, m.client.config.ProjectID).CreateServerPayload(*payload).Execute()
	if err != nil {
		// Cleanup: delete the orphaned NIC since server creation failed
		_ = cli.DeleteNic(ctx, m.client.config.ProjectID, req.NetworkID, nicID).Execute()

		return nil, fmt.Errorf("failed to create server: %w", err)
	}

	// Convert to instance
	inst := m.serverResponseToInstance(created)

	// Wait for instance to become active
	err = m.waitForInstanceState(ctx, inst.ID, cpi.ResourceStateActive, instanceWaitTimeout)
	if err != nil {
		return nil, fmt.Errorf("instance failed to become active: %w", err)
	}

	logger.WithOperation("createServerWithNIC").Infof("Server created with static IP NIC: %s (%s)", inst.Name, inst.ID)

	return inst, nil
}
