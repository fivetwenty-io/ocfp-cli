package stackit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas"
)

// CreateInstance creates a new compute instance.
func (m *ComputeManager) CreateInstance(ctx context.Context, req *cpi.CreateInstanceRequest) (*cpi.Instance, error) {
	logger.WithOperation("CreateInstance").Infof("Creating instance via SDK: %s", req.Name)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	payload := iaas.NewCreateServerPayload(req.Flavor, req.Name)
	if req.Image != "" {
		payload.SetImageId(req.Image)
	}

	if req.KeyPair != "" {
		payload.SetKeypairName(req.KeyPair)
	}

	if req.AvailabilityZone != "" {
		payload.SetAvailabilityZone(req.AvailabilityZone)
	}

	if len(req.Tags) > 0 {
		lm := make(map[string]interface{}, len(req.Tags))
		for k, v := range req.Tags {
			lm[k] = v
		}

		payload.SetLabels(lm)
	}

	if req.NetworkID != "" {
		net := iaas.NewCreateServerNetworking()
		net.SetNetworkId(req.NetworkID)
		n := iaas.CreateServerNetworkingAsCreateServerPayloadNetworking(net)
		payload.SetNetworking(n)
	}

	if req.UserData != "" {
		// SDK expects bytes; it will base64-encode for transport
		payload.SetUserData([]byte(req.UserData))
	}

	created, err := cli.CreateServer(ctx, m.client.config.ProjectID).CreateServerPayload(*payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas CreateServer failed: %w", err)
	}

	inst := &cpi.Instance{}
	if id, ok := created.GetIdOk(); ok {
		inst.ID = id
	}

	if name, ok := created.GetNameOk(); ok {
		inst.Name = name
	}

	if mt, ok := created.GetMachineTypeOk(); ok {
		inst.Flavor = mt
	}

	if img, ok := created.GetImageIdOk(); ok {
		inst.Image = img
	}

	if az, ok := created.GetAvailabilityZoneOk(); ok {
		inst.AvailabilityZone = az
	}

	if kp, ok := created.GetKeypairNameOk(); ok {
		inst.KeyPair = kp
	}

	if labels, ok := created.GetLabelsOk(); ok {
		inst.Tags = mapAnyToString(labels)
	}

	if err := m.waitForInstanceState(ctx, inst.ID, cpi.ResourceStateActive, 10*time.Minute); err != nil {
		return nil, fmt.Errorf("instance failed to become active: %w", err)
	}

	logger.WithOperation("CreateInstance").Infof("Instance created: %s (%s)", inst.Name, inst.ID)

	return inst, nil
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

	inst := &cpi.Instance{}
	if sid, ok := server.GetIdOk(); ok {
		inst.ID = sid
	}

	if name, ok := server.GetNameOk(); ok {
		inst.Name = name
	}

	if mt, ok := server.GetMachineTypeOk(); ok {
		inst.Flavor = mt
	}

	if img, ok := server.GetImageIdOk(); ok {
		inst.Image = img
	}

	if az, ok := server.GetAvailabilityZoneOk(); ok {
		inst.AvailabilityZone = az
	}

	if kp, ok := server.GetKeypairNameOk(); ok {
		inst.KeyPair = kp
	}

	if labels, ok := server.GetLabelsOk(); ok {
		inst.Tags = mapAnyToString(labels)
	}
	// NICs and public IP
	// Build map from NIC->PublicIP
	pmap := make(map[string]string)

	if pipResp, err := cli.ListPublicIPs(ctx, m.client.config.ProjectID).Execute(); err == nil {
		if items, ok := pipResp.GetItemsOk(); ok {
			for _, ip := range items {
				if ni, ok := ip.GetNetworkInterfaceOk(); ok && ni != nil && *ni != "" {
					if val, vok := ip.GetIpOk(); vok {
						pmap[*ni] = val
					}
				}
			}
		}
	}

	if nicsResp, err := cli.ListServerNics(ctx, m.client.config.ProjectID, instanceID).Execute(); err == nil {
		if nics, ok := nicsResp.GetItemsOk(); ok {
			for _, nic := range nics {
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
		}
	}

	return inst, nil
}

// ListInstances lists all instances.
func (m *ComputeManager) ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	logger.WithOperation("ListInstances").Debug("Listing instances via SDK")

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	// Build label selector from filters (accepts keys like "label.foo" or "label:foo")
	var selectors []string

	for k, v := range filters {
		if strings.HasPrefix(k, "label.") {
			selectors = append(selectors, fmt.Sprintf("%s=%s", strings.TrimPrefix(k, "label."), v))
		} else if strings.HasPrefix(k, "label:") {
			selectors = append(selectors, fmt.Sprintf("%s=%s", strings.TrimPrefix(k, "label:"), v))
		}
	}

	selector := strings.Join(selectors, ",")

	req := cli.ListServers(ctx, m.client.config.ProjectID)
	if selector != "" {
		req = req.LabelSelector(selector)
	}

	resp, err := req.Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListServers failed: %w", err)
	}

	// Build NIC->PublicIP map once to derive floating/public IP association
	publicIPsByNIC := make(map[string]string)

	if pipResp, err := cli.ListPublicIPs(ctx, m.client.config.ProjectID).Execute(); err == nil {
		if items, ok := pipResp.GetItemsOk(); ok {
			for _, ip := range items {
				if ni, ok := ip.GetNetworkInterfaceOk(); ok && ni != nil && *ni != "" {
					if val, vok := ip.GetIpOk(); vok {
						publicIPsByNIC[*ni] = val
					}
				}
			}
		}
	}

	items, _ := resp.GetItemsOk()

	out := make([]*cpi.Instance, 0, len(items))
	for _, server := range items {
		inst := &cpi.Instance{}
		if id, ok := server.GetIdOk(); ok {
			inst.ID = id
		}

		if name, ok := server.GetNameOk(); ok {
			inst.Name = name
		}

		if mt, ok := server.GetMachineTypeOk(); ok {
			inst.Flavor = mt
		}

		if img, ok := server.GetImageIdOk(); ok {
			inst.Image = img
		}

		if az, ok := server.GetAvailabilityZoneOk(); ok {
			inst.AvailabilityZone = az
		}

		if kp, ok := server.GetKeypairNameOk(); ok {
			inst.KeyPair = kp
		}

		if labels, ok := server.GetLabelsOk(); ok {
			// Convert map[string]interface{} to map[string]string
			inst.Tags = mapAnyToString(labels)
		}

		if status, ok := server.GetStatusOk(); ok {
			inst.State = mapServerStatus(status)
		}

		// Populate networking: private and floating/public IP from NICs
		if inst.ID != "" {
			if nicsResp, err := cli.ListServerNics(ctx, m.client.config.ProjectID, inst.ID).Execute(); err == nil {
				if nics, ok := nicsResp.GetItemsOk(); ok {
					for _, nic := range nics {
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
				}
			}
		}

		out = append(out, inst)
	}

	logger.WithOperation("ListInstances").Debugf("Found %d instances", len(out))

	return out, nil
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

	if err := cli.DeleteServer(ctx, m.client.config.ProjectID, instanceID).Execute(); err != nil {
		return fmt.Errorf("stackit iaas DeleteServer failed: %w", err)
	}

	logger.WithOperation("DeleteInstance").Infof("Instance deleted: %s", instanceID)

	return nil
}

// CreateKeyPair creates a new SSH key pair.
func (m *ComputeManager) CreateKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	return nil, errors.New("stackit: CreateKeyPair unsupported; use ImportKeyPair with a public key")
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

	if _, err := cli.CreateKeyPair(ctx).CreateKeyPairPayload(*payload).Execute(); err != nil {
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

	kp, err := cli.GetKeyPair(ctx, name).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetKeyPair failed: %w", err)
	}

	out := &cpi.KeyPair{Name: stringOrEmpty(kp.GetNameOk()), Fingerprint: stringOrEmpty(kp.GetFingerprintOk()), PublicKey: stringOrEmpty(kp.GetPublicKeyOk())}

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
	for _, k := range items {
		out = append(out, &cpi.KeyPair{
			Name:        stringOrEmpty(k.GetNameOk()),
			Fingerprint: stringOrEmpty(k.GetFingerprintOk()),
			PublicKey:   stringOrEmpty(k.GetPublicKeyOk()),
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

	if err := cli.DeleteKeyPair(ctx, name).Execute(); err != nil {
		return fmt.Errorf("stackit iaas DeleteKeyPair failed: %w", err)
	}

	return nil
}

// waitForInstanceState waits for an instance to reach a specific state.
func (m *ComputeManager) waitForInstanceState(ctx context.Context, instanceID string, targetState cpi.ResourceState, timeout time.Duration) error {
	return cpi.WaitForCondition(ctx, 5*time.Second, timeout, func() (bool, error) {
		instance, err := m.GetInstance(ctx, instanceID)
		if err != nil {
			return false, err
		}

		return instance.State == targetState, nil
	})
}

// StartInstance starts a stopped instance.
func (m *ComputeManager) StartInstance(ctx context.Context, instanceID string) error {
	logger.WithOperation("StartInstance").Infof("Starting instance: %s", instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	return cli.StartServer(ctx, m.client.config.ProjectID, instanceID).Execute()
}

// StopInstance stops a running instance.
func (m *ComputeManager) StopInstance(ctx context.Context, instanceID string) error {
	logger.WithOperation("StopInstance").Infof("Stopping instance: %s", instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	return cli.StopServer(ctx, m.client.config.ProjectID, instanceID).Execute()
}

// RebootInstance reboots an instance.
func (m *ComputeManager) RebootInstance(ctx context.Context, instanceID string) error {
	logger.WithOperation("RebootInstance").Infof("Rebooting instance: %s", instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	return cli.RebootServer(ctx, m.client.config.ProjectID, instanceID).Action("SOFT").Execute()
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
		img := &cpi.Image{ID: stringOrEmpty(it.GetIdOk()), Name: stringOrEmpty(it.GetNameOk()), Public: true}
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

	out := &cpi.Image{ID: stringOrEmpty(got.GetIdOk()), Name: stringOrEmpty(got.GetNameOk()), Public: true}

	return out, nil
}

// ListFlavors lists available flavors.
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

	out := make([]*cpi.Flavor, 0, len(items))
	for _, machineType := range items {
		flavor := &cpi.Flavor{
			ID:   stringOrEmpty(machineType.GetNameOk()),
			Name: stringOrEmpty(machineType.GetNameOk()),
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
		return nil, fmt.Errorf("stackit iaas GetMachineType failed: %w", err)
	}

	flavor := &cpi.Flavor{ID: stringOrEmpty(machineType.GetNameOk()), Name: stringOrEmpty(machineType.GetNameOk())}
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
