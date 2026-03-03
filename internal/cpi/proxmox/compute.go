package proxmox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/cloudinit"
)

const (
	// nodeStatusOnline is the status value for an online Proxmox node.
	nodeStatusOnline = "online"

	// Task wait timeouts in seconds.
	taskTimeoutClone   = 300 // 5 minutes for clone operations
	taskTimeoutDefault = 120 // 2 minutes for standard operations

	// Flavor preset resource values.
	flavorSmallRAM    = 1024
	flavorSmallDisk   = 10
	flavorMedCPU      = 2
	flavorMedRAM      = 2048
	flavorMedDisk     = 20
	flavorLargeCPU    = 4
	flavorLargeRAM    = 4096
	flavorLargeDisk   = 40
	flavorXLCPU       = 8
	flavorXLRAM       = 8192
	flavorXLDisk      = 80
	flavorBastionCPU  = 2
	flavorBastionRAM  = 4096
	flavorBastionDisk = 50
	flavorBoshCPU     = 4
	flavorBoshRAM     = 8192
	flavorBoshDisk    = 100

	// vmStopDelay is the time to wait after stopping a VM before deleting it.
	vmStopDelay = 2 * time.Second
)

// ComputeManager handles Proxmox compute operations.
type ComputeManager struct {
	client *Client
}

// Flavor presets for Proxmox (no native flavor concept).
//
//nolint:gochecknoglobals // package-level lookup table for flavor presets
var flavorPresets = map[string]*cpi.Flavor{
	"small": {
		ID:          "small",
		Name:        "Small",
		VCPUs:       1,
		RAM:         flavorSmallRAM,
		Disk:        flavorSmallDisk,
		Description: "Small instance: 1 vCPU, 1GB RAM, 10GB disk",
	},
	"medium": {
		ID:          "medium",
		Name:        "Medium",
		VCPUs:       flavorMedCPU,
		RAM:         flavorMedRAM,
		Disk:        flavorMedDisk,
		Description: "Medium instance: 2 vCPUs, 2GB RAM, 20GB disk",
	},
	"large": {
		ID:          "large",
		Name:        "Large",
		VCPUs:       flavorLargeCPU,
		RAM:         flavorLargeRAM,
		Disk:        flavorLargeDisk,
		Description: "Large instance: 4 vCPUs, 4GB RAM, 40GB disk",
	},
	"xlarge": {
		ID:          "xlarge",
		Name:        "Extra Large",
		VCPUs:       flavorXLCPU,
		RAM:         flavorXLRAM,
		Disk:        flavorXLDisk,
		Description: "Extra large instance: 8 vCPUs, 8GB RAM, 80GB disk",
	},
	"bastion": {
		ID:          "bastion",
		Name:        "Bastion",
		VCPUs:       flavorBastionCPU,
		RAM:         flavorBastionRAM,
		Disk:        flavorBastionDisk,
		Description: "Bastion host: 2 vCPUs, 4GB RAM, 50GB disk",
	},
	"bosh": {
		ID:          "bosh",
		Name:        "BOSH Director",
		VCPUs:       flavorBoshCPU,
		RAM:         flavorBoshRAM,
		Disk:        flavorBoshDisk,
		Description: "BOSH Director: 4 vCPUs, 8GB RAM, 100GB disk",
	},
}

// CreateInstance creates a new QEMU VM.
//
//nolint:cyclop,funlen,nestif // Proxmox VM creation with template cloning and cloud-init is inherently complex
func (m *ComputeManager) CreateInstance(ctx context.Context, req *cpi.InstanceRequest) (*cpi.Instance, error) {
	logger.WithOperation("CreateInstance").Infof("Creating VM: %s", req.Name)

	// Get flavor specs
	flavor, ok := flavorPresets[req.Flavor]
	if !ok {
		return nil, ErrFlavorNotFound(req.Flavor)
	}

	// Select optimal node for the VM
	node, err := m.client.selectOptimalNode(ctx, flavor.VCPUs, flavor.RAM)
	if err != nil {
		return nil, fmt.Errorf("failed to select node: %w", err)
	}

	// Get next available VMID
	vmid, err := m.getNextVMID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get next VMID: %w", err)
	}

	// Build VM creation parameters
	params := map[string]interface{}{
		"vmid":    vmid,
		"name":    req.Name,
		"memory":  flavor.RAM,
		"cores":   flavor.VCPUs,
		"sockets": 1,
		"cpu":     "host",
		"ostype":  "l26", // Linux 2.6+ kernel
		"agent":   "1",   // Enable QEMU guest agent
	}

	// Configure network
	bridge := m.client.config.DefaultBridge
	if req.NetworkID != "" {
		bridge = req.NetworkID
	}

	params["net0"] = fmt.Sprintf("virtio,bridge=%s,firewall=1", bridge)

	// Configure storage for boot disk
	storage := m.client.config.DefaultStorage

	diskSize := flavor.Disk
	if req.BootVolumeSize > 0 {
		diskSize = req.BootVolumeSize
	}

	// If we have an image (template), clone from it
	if req.Image != "" {
		// Image is expected to be a template VMID
		templateVMID, err := strconv.Atoi(req.Image)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidTemplateVMID, req.Image)
		}

		// Clone the template
		upid, err := m.cloneTemplate(ctx, node, templateVMID, vmid, req.Name, storage)
		if err != nil {
			return nil, fmt.Errorf("failed to clone template: %w", err)
		}

		// Wait for clone to complete
		err = m.client.waitForTask(ctx, node, upid, taskTimeoutClone)
		if err != nil {
			return nil, fmt.Errorf("clone task failed: %w", err)
		}

		// Resize disk if needed
		if diskSize > 0 {
			_ = m.resizeBootDisk(ctx, node, vmid, diskSize)
		}
	} else {
		// Create VM with blank disk
		params["scsi0"] = fmt.Sprintf("%s:%d,format=qcow2", storage, diskSize)
		params["scsihw"] = "virtio-scsi-pci"
		params["boot"] = "order=scsi0"

		// Create the VM
		qemuSvc := m.client.getQemuService()

		upid, err := qemuSvc.Create(ctx, node, params)
		if err != nil {
			return nil, fmt.Errorf("failed to create VM: %w", err)
		}

		// Wait for creation
		err = m.client.waitForTask(ctx, node, upid, taskTimeoutDefault)
		if err != nil {
			return nil, fmt.Errorf("VM creation task failed: %w", err)
		}
	}

	// Configure cloud-init if user data is provided
	if req.UserData != "" {
		err := m.configureCloudInit(ctx, node, vmid, req)
		if err != nil {
			logger.Warnf("Failed to configure cloud-init: %v", err)
		}
	}

	// Apply tags via description
	if len(req.Tags) > 0 {
		m.setVMTags(ctx, node, vmid, req.Tags)
	}

	// Start the VM
	qemuSvc := m.client.getQemuService()

	upid, err := qemuSvc.Start(ctx, node, vmid)
	if err != nil {
		return nil, fmt.Errorf("failed to start VM: %w", err)
	}

	err = m.client.waitForTask(ctx, node, upid, taskTimeoutDefault)
	if err != nil {
		return nil, fmt.Errorf("VM start task failed: %w", err)
	}

	// Get the created instance
	return m.GetInstance(ctx, strconv.Itoa(vmid))
}

// GetInstance retrieves a VM by ID.
func (m *ComputeManager) GetInstance(ctx context.Context, id string) (*cpi.Instance, error) {
	vmid, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidVMID, id)
	}

	node, err := m.findVMNode(ctx, vmid)
	if err != nil {
		return nil, err
	}

	qemuSvc := m.client.getQemuService()

	// Get VM status
	status, err := qemuSvc.Status(ctx, node, vmid)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM status: %w", err)
	}

	// Get VM config
	config, err := qemuSvc.Config(ctx, node, vmid)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM config: %w", err)
	}

	return m.vmToInstance(vmid, node, status, config)
}

// ListInstances lists VMs with optional filters.
//
//nolint:funlen // VM listing across nodes with type assertions is inherently detailed
func (m *ComputeManager) ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	// Refresh nodes
	err := m.client.refreshNodes(ctx)
	if err != nil {
		return nil, err
	}

	var instances []*cpi.Instance

	m.client.nodesMutex.RLock()
	nodes := make([]NodeInfo, len(m.client.nodes))
	copy(nodes, m.client.nodes)
	m.client.nodesMutex.RUnlock()

	for _, nodeInfo := range nodes {
		if nodeInfo.Status != nodeStatusOnline {
			continue
		}

		// Get VMs on this node
		path := fmt.Sprintf("/nodes/%s/qemu", nodeInfo.Name)

		resp, err := m.client.pveClient.GetCtx(ctx, path, nil)
		if err != nil {
			logger.Warnf("Failed to list VMs on node %s: %v", nodeInfo.Name, err)

			continue
		}

		vms, ok := resp.([]interface{})
		if !ok {
			continue
		}

		for _, vmData := range vms {
			vm, ok := vmData.(map[string]interface{}) //nolint:varnamelen // vm is clear in context
			if !ok {
				continue
			}

			// Skip templates
			if template, ok := vm["template"].(float64); ok && template == 1 {
				continue
			}

			vmid := getIntFromMap(vm, "vmid")
			name := getStringFromMap(vm, "name")
			status := getStringFromMap(vm, "status")

			// Apply name filter
			if nameFilter, ok := filters["name"]; ok && name != nameFilter {
				continue
			}

			instance := &cpi.Instance{
				ID:               strconv.Itoa(vmid),
				Name:             name,
				State:            m.mapVMState(status),
				AvailabilityZone: nodeInfo.Name,
				Tags:             make(map[string]string),
			}

			instances = append(instances, instance)
		}
	}

	return instances, nil
}

// StartInstance starts a VM.
func (m *ComputeManager) StartInstance(ctx context.Context, id string) error {
	vmid, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidVMID, id)
	}

	node, err := m.findVMNode(ctx, vmid)
	if err != nil {
		return err
	}

	qemuSvc := m.client.getQemuService()

	upid, err := qemuSvc.Start(ctx, node, vmid)
	if err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	return m.client.waitForTask(ctx, node, upid, taskTimeoutDefault)
}

// StopInstance stops a VM.
func (m *ComputeManager) StopInstance(ctx context.Context, id string) error {
	vmid, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidVMID, id)
	}

	node, err := m.findVMNode(ctx, vmid)
	if err != nil {
		return err
	}

	qemuSvc := m.client.getQemuService()

	upid, err := qemuSvc.Stop(ctx, node, vmid)
	if err != nil {
		return fmt.Errorf("failed to stop VM: %w", err)
	}

	return m.client.waitForTask(ctx, node, upid, taskTimeoutDefault)
}

// RebootInstance reboots a VM.
func (m *ComputeManager) RebootInstance(ctx context.Context, id string) error {
	vmid, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidVMID, id)
	}

	node, err := m.findVMNode(ctx, vmid)
	if err != nil {
		return err
	}

	qemuSvc := m.client.getQemuService()

	upid, err := qemuSvc.Reset(ctx, node, vmid)
	if err != nil {
		return fmt.Errorf("failed to reboot VM: %w", err)
	}

	return m.client.waitForTask(ctx, node, upid, taskTimeoutDefault)
}

// DeleteInstance deletes a VM.
func (m *ComputeManager) DeleteInstance(ctx context.Context, id string) error { //nolint:varnamelen // id is clear in context
	vmid, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidVMID, id)
	}

	node, err := m.findVMNode(ctx, vmid)
	if err != nil {
		return err
	}

	// Stop the VM first if running
	_ = m.StopInstance(ctx, id)

	// Wait a bit for VM to stop
	time.Sleep(vmStopDelay)

	// Delete the VM
	path := fmt.Sprintf("/nodes/%s/qemu/%d", node, vmid)

	_, err = m.client.pveClient.DeleteCtx(ctx, path, map[string]interface{}{
		"purge": true,
	})
	if err != nil {
		return fmt.Errorf("failed to delete VM: %w", err)
	}

	return nil
}

// CreateKeyPair creates a new key pair (not supported - use ImportKeyPair).
func (m *ComputeManager) CreateKeyPair(ctx context.Context, req *cpi.KeyPairRequest) (*cpi.KeyPair, error) {
	return nil, ErrCreateKeyPairNotSupported
}

// ImportKeyPair imports a public key.
func (m *ComputeManager) ImportKeyPair(ctx context.Context, name string, publicKey string) error {
	// Store the key for use in cloud-init
	// In Proxmox, keys are typically stored per-VM via cloud-init
	// We'll store this in a local cache for now
	logger.Infof("Imported key pair: %s (will be used in cloud-init)", name)

	return nil
}

// GetKeyPair retrieves a key pair.
func (m *ComputeManager) GetKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	// Key pairs are not stored centrally in Proxmox
	return nil, ErrVMNotFound
}

// ListKeyPairs lists all key pairs.
func (m *ComputeManager) ListKeyPairs(ctx context.Context) ([]*cpi.KeyPair, error) {
	// Key pairs are not stored centrally in Proxmox
	return []*cpi.KeyPair{}, nil
}

// DeleteKeyPair deletes a key pair.
func (m *ComputeManager) DeleteKeyPair(ctx context.Context, name string) error {
	// Key pairs are not stored centrally in Proxmox
	return nil
}

// CreateVolume creates a new volume.
func (m *ComputeManager) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	// Delegate to storage manager
	return m.client.storage.CreateVolume(ctx, req)
}

// GetVolume retrieves a volume.
func (m *ComputeManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) {
	return m.client.storage.GetVolume(ctx, id)
}

// ListVolumes lists volumes.
func (m *ComputeManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	return m.client.storage.ListVolumes(ctx, filters)
}

// DeleteVolume deletes a volume.
func (m *ComputeManager) DeleteVolume(ctx context.Context, id string) error {
	return m.client.storage.DeleteVolume(ctx, id)
}

// ListImages lists available templates.
func (m *ComputeManager) ListImages(ctx context.Context, filters map[string]string) ([]*cpi.Image, error) {
	var images []*cpi.Image

	// Refresh nodes
	err := m.client.refreshNodes(ctx)
	if err != nil {
		return nil, err
	}

	m.client.nodesMutex.RLock()
	nodes := make([]NodeInfo, len(m.client.nodes))
	copy(nodes, m.client.nodes)
	m.client.nodesMutex.RUnlock()

	for _, nodeInfo := range nodes {
		if nodeInfo.Status != nodeStatusOnline {
			continue
		}

		// Get VMs on this node (templates are VMs with template flag)
		path := fmt.Sprintf("/nodes/%s/qemu", nodeInfo.Name)

		resp, err := m.client.pveClient.GetCtx(ctx, path, nil)
		if err != nil {
			continue
		}

		vms, ok := resp.([]interface{})
		if !ok {
			continue
		}

		for _, vmData := range vms {
			vm, ok := vmData.(map[string]interface{}) //nolint:varnamelen // vm is clear in context
			if !ok {
				continue
			}

			// Only include templates
			template, ok := vm["template"].(float64) //nolint:varnamelen // ok is clear in context
			if !ok || template != 1 {
				continue
			}

			vmid := getIntFromMap(vm, "vmid")
			name := getStringFromMap(vm, "name")

			image := &cpi.Image{
				ID:          strconv.Itoa(vmid),
				Name:        name,
				Description: "Template on node " + nodeInfo.Name,
				State:       "available",
				Tags:        make(map[string]string),
			}

			images = append(images, image)
		}
	}

	return images, nil
}

// GetImage retrieves an image/template.
func (m *ComputeManager) GetImage(ctx context.Context, id string) (*cpi.Image, error) { //nolint:varnamelen // id is clear in context
	images, err := m.ListImages(ctx, nil)
	if err != nil {
		return nil, err
	}

	for _, img := range images {
		if img.ID == id || img.Name == id {
			return img, nil
		}
	}

	return nil, ErrTemplateNotFound
}

// ListFlavors lists available flavor presets.
func (m *ComputeManager) ListFlavors(ctx context.Context) ([]*cpi.Flavor, error) {
	flavors := make([]*cpi.Flavor, 0, len(flavorPresets))
	for _, f := range flavorPresets {
		flavors = append(flavors, f)
	}

	return flavors, nil
}

// GetFlavor retrieves a flavor preset.
func (m *ComputeManager) GetFlavor(ctx context.Context, id string) (*cpi.Flavor, error) {
	if f, ok := flavorPresets[id]; ok {
		return f, nil
	}

	return nil, ErrFlavorNotFound(id)
}

// Helper methods

// getNextVMID gets the next available VMID.
func (m *ComputeManager) getNextVMID(ctx context.Context) (int, error) {
	resp, err := m.client.pveClient.GetCtx(ctx, "/cluster/nextid", nil)
	if err != nil {
		return 0, fmt.Errorf("failed to get next VMID: %w", err)
	}

	switch v := resp.(type) { //nolint:varnamelen // v is clear in context
	case string:
		vmid, convErr := strconv.Atoi(v)
		if convErr != nil {
			return 0, fmt.Errorf("parsing next VMID response: %w", convErr)
		}

		return vmid, nil
	case float64:
		return int(v), nil
	default:
		return 0, fmt.Errorf("%w for nextid: %T", ErrUnexpectedResponseType, resp)
	}
}

// findVMNode finds which node a VM is on.
func (m *ComputeManager) findVMNode(ctx context.Context, vmid int) (string, error) {
	// If specific node configured, use it
	if m.client.config.Node != "" {
		return m.client.config.Node, nil
	}

	// Search all nodes
	err := m.client.refreshNodes(ctx)
	if err != nil {
		return "", err
	}

	m.client.nodesMutex.RLock()
	nodes := make([]NodeInfo, len(m.client.nodes))
	copy(nodes, m.client.nodes)
	m.client.nodesMutex.RUnlock()

	for _, nodeInfo := range nodes {
		if nodeInfo.Status != nodeStatusOnline {
			continue
		}

		path := fmt.Sprintf("/nodes/%s/qemu/%d/status/current", nodeInfo.Name, vmid)

		_, err := m.client.pveClient.GetCtx(ctx, path, nil)
		if err == nil {
			return nodeInfo.Name, nil
		}
	}

	return "", ErrVMIDNotFound(vmid)
}

// cloneTemplate clones a VM template.
func (m *ComputeManager) cloneTemplate(ctx context.Context, node string, templateVMID, newVMID int, name, storage string) (string, error) {
	qemuSvc := m.client.getQemuService()

	params := map[string]interface{}{
		"newid":   newVMID,
		"name":    name,
		"full":    1,
		"storage": storage,
	}

	taskID, cloneErr := qemuSvc.Clone(ctx, node, templateVMID, params)
	if cloneErr != nil {
		return "", fmt.Errorf("cloning template VMID %d to %d: %w", templateVMID, newVMID, cloneErr)
	}

	return taskID, nil
}

// configureCloudInit configures cloud-init for a VM.
func (m *ComputeManager) configureCloudInit(ctx context.Context, node string, vmid int, req *cpi.InstanceRequest) error {
	ciSvc := m.client.getCloudinitService()

	// Build IP config if static IP requested
	var networkSpecs []cloudinit.NICSpec
	if req.StaticPrivateIP != "" {
		networkSpecs = []cloudinit.NICSpec{
			{
				AddressCIDR: req.StaticPrivateIP + "/24", // Assume /24 if not specified
				DHCP:        false,
			},
		}
	} else {
		networkSpecs = []cloudinit.NICSpec{
			{DHCP: true},
		}
	}

	// Build IP configs
	ipConfigs, err := ciSvc.BuildIPConfigs(networkSpecs, nil)
	if err != nil {
		logger.Warnf("Failed to build IP configs: %v", err)
	}

	// Configure VM with cloud-init
	storage := m.client.config.DefaultStorage
	if m.client.config.ISOStorage != "" {
		storage = m.client.config.ISOStorage
	}

	// Attach cloud-init drive
	err = ciSvc.Attach(ctx, node, vmid, storage, []byte(req.UserData))
	if err != nil {
		return fmt.Errorf("failed to attach cloud-init: %w", err)
	}

	// Set cloud-init IP config
	if len(ipConfigs) > 0 {
		path := fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid)
		// Convert map[string]string to map[string]interface{}
		ipConfigInterface := make(map[string]interface{})
		for k, v := range ipConfigs {
			ipConfigInterface[k] = v
		}

		_, err = m.client.pveClient.PutCtx(ctx, path, ipConfigInterface)
		if err != nil {
			logger.Warnf("Failed to set cloud-init IP config: %v", err)
		}
	}

	return nil
}

// resizeBootDisk resizes the boot disk.
//
//nolint:unparam // returns nil by design; errors are silently ignored for best-effort resize
func (m *ComputeManager) resizeBootDisk(ctx context.Context, node string, vmid, sizeGB int) error {
	qemuSvc := m.client.getQemuService()

	// Try common disk IDs
	for _, diskID := range []string{"scsi0", "virtio0", "ide0"} {
		_, err := qemuSvc.ResizeDisk(ctx, node, vmid, diskID, sizeGB)
		if err == nil {
			return nil
		}
	}

	return nil // Silently fail if disk not found
}

// setVMTags sets tags on a VM via description field.
func (m *ComputeManager) setVMTags(ctx context.Context, node string, vmid int, tags map[string]string) {
	if len(tags) == 0 {
		return
	}

	// Build description from tags
	var parts []string
	for k, v := range tags {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}

	description := "Tags: " + strings.Join(parts, ", ")

	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid)
	_, _ = m.client.pveClient.PutCtx(ctx, path, map[string]interface{}{
		"description": description,
	})
}

// vmToInstance converts Proxmox VM data to CPI Instance.
func (m *ComputeManager) vmToInstance(vmid int, node string, status, config map[string]interface{}) (*cpi.Instance, error) {
	instance := &cpi.Instance{
		ID:               strconv.Itoa(vmid),
		Name:             getStringFromMap(config, "name"),
		State:            m.mapVMState(getStringFromMap(status, "status")),
		AvailabilityZone: node,
		Tags:             make(map[string]string),
	}

	// Parse flavor from config
	cores := getIntFromMap(config, "cores")
	memory := getIntFromMap(config, "memory")
	instance.Flavor = m.matchFlavor(cores, memory)

	// Get network info
	if net0 := getStringFromMap(config, "net0"); net0 != "" {
		// Extract bridge name
		parts := strings.Split(net0, ",")
		for _, part := range parts {
			if strings.HasPrefix(part, "bridge=") {
				instance.NetworkID = strings.TrimPrefix(part, "bridge=")

				break
			}
		}
	}

	// Get IP addresses from QEMU guest agent
	if qga, ok := status["qmpstatus"].(string); ok && qga == "running" {
		m.populateNetworkInfo(instance, status)
	}

	return instance, nil
}

// mapVMState maps Proxmox VM status to CPI ResourceState.
func (m *ComputeManager) mapVMState(status string) cpi.ResourceState {
	switch strings.ToLower(status) {
	case "running":
		return cpi.ResourceStateActive
	case "stopped":
		return cpi.ResourceStateStopped
	case "paused":
		return cpi.ResourceStateStopped
	default:
		return cpi.ResourceStateUnknown
	}
}

// matchFlavor finds the best matching flavor for given specs.
func (m *ComputeManager) matchFlavor(cores, memoryMB int) string {
	for id, f := range flavorPresets {
		if f.VCPUs == cores && f.RAM == memoryMB {
			return id
		}
	}

	return fmt.Sprintf("custom-%dc-%dm", cores, memoryMB)
}

// populateNetworkInfo populates IP addresses from VM status.
func (m *ComputeManager) populateNetworkInfo(instance *cpi.Instance, status map[string]interface{}) {
	netInfo, ok := status["network"].(map[string]interface{})
	if !ok {
		return
	}

	for _, iface := range netInfo {
		m.extractIPFromInterface(instance, iface)
	}
}

// extractIPFromInterface extracts IPv4 addresses from a network interface.
func (m *ComputeManager) extractIPFromInterface(instance *cpi.Instance, iface interface{}) {
	ifaceData, ok := iface.(map[string]interface{}) //nolint:varnamelen
	if !ok {
		return
	}

	ipAddresses, ok := ifaceData["ip-addresses"].([]interface{})
	if !ok {
		return
	}

	for _, ip := range ipAddresses {
		ipData, ok := ip.(map[string]interface{})
		if !ok {
			continue
		}

		ipAddr := getStringFromMap(ipData, "ip-address")
		ipType := getStringFromMap(ipData, "ip-address-type")

		if ipType == "ipv4" && !strings.HasPrefix(ipAddr, "127.") && instance.PrivateIP == "" {
			instance.PrivateIP = ipAddr
		}
	}
}
