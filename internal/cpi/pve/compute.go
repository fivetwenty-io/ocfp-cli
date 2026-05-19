package pve

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
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

	// Determine disk size
	diskSize := flavor.Disk
	if req.BootVolumeSize > 0 {
		diskSize = req.BootVolumeSize
	}

	storage := m.client.config.DefaultStorage

	// Create the VM either from template or blank
	if req.Image != "" {
		err = m.createFromTemplate(ctx, node, vmid, diskSize, storage, req)
	} else {
		err = m.createBlankVM(ctx, node, vmid, diskSize, storage, flavor, req)
	}

	if err != nil {
		return nil, err
	}

	return m.finalizeAndStartVM(ctx, node, vmid, req)
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
			vmTags := parsePVETags(getStringFromMap(vm, "tags"))

			// Apply name filter
			if nameFilter, ok := filters["name"]; ok && name != nameFilter {
				continue
			}

			// Apply label filters against the PVE tag set written by
			// setVMTags. A VM with no tags can't satisfy any label filter
			// — this is what makes label-based bastion discovery skip
			// unrelated VMs on the cluster.
			if !matchesLabelFilters(vmTags, filters) {
				continue
			}

			tagMap := make(map[string]string, len(vmTags))
			for _, tag := range vmTags {
				tagMap[tag] = ""
			}

			instance := &cpi.Instance{
				ID:               strconv.Itoa(vmid),
				Name:             name,
				State:            m.mapVMState(status),
				AvailabilityZone: nodeInfo.Name,
				Tags:             tagMap,
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
func (m *ComputeManager) CreateKeyPair(_ctx context.Context, _req *cpi.KeyPairRequest) (*cpi.KeyPair, error) {
	return nil, ErrCreateKeyPairNotSupported
}

// ImportKeyPair imports a public key.
func (m *ComputeManager) ImportKeyPair(_ctx context.Context, name string, _publicKey string) error {
	// Store the key for use in cloud-init
	// In Proxmox, keys are typically stored per-VM via cloud-init
	// We'll store this in a local cache for now
	logger.Infof("Imported key pair: %s (will be used in cloud-init)", name)

	return nil
}

// GetKeyPair retrieves a key pair.
func (m *ComputeManager) GetKeyPair(_ctx context.Context, _name string) (*cpi.KeyPair, error) {
	// Key pairs are not stored centrally in Proxmox
	return nil, ErrVMNotFound
}

// ListKeyPairs lists all key pairs.
func (m *ComputeManager) ListKeyPairs(_ctx context.Context) ([]*cpi.KeyPair, error) {
	// Key pairs are not stored centrally in Proxmox
	return []*cpi.KeyPair{}, nil
}

// DeleteKeyPair deletes a key pair.
func (m *ComputeManager) DeleteKeyPair(_ctx context.Context, _name string) error {
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
func (m *ComputeManager) ListImages(ctx context.Context, _filters map[string]string) ([]*cpi.Image, error) {
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
func (m *ComputeManager) ListFlavors(_ctx context.Context) ([]*cpi.Flavor, error) {
	flavors := make([]*cpi.Flavor, 0, len(flavorPresets))
	for _, f := range flavorPresets {
		flavors = append(flavors, f)
	}

	return flavors, nil
}

// GetFlavor retrieves a flavor preset.
func (m *ComputeManager) GetFlavor(_ctx context.Context, id string) (*cpi.Flavor, error) {
	if f, ok := flavorPresets[id]; ok {
		return f, nil
	}

	return nil, ErrFlavorNotFound(id)
}

// createFromTemplate creates a VM by cloning a template.
func (m *ComputeManager) createFromTemplate(ctx context.Context, node string, vmid, diskSize int, storage string, req *cpi.InstanceRequest) error {
	templateVMID, err := strconv.Atoi(req.Image)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidTemplateVMID, req.Image)
	}

	upid, err := m.cloneTemplate(ctx, node, templateVMID, vmid, req.Name, storage)
	if err != nil {
		return fmt.Errorf("failed to clone template: %w", err)
	}

	err = m.client.waitForTask(ctx, node, upid, taskTimeoutClone)
	if err != nil {
		return fmt.Errorf("clone task failed: %w", err)
	}

	// Resize disk if needed
	if diskSize > 0 {
		_ = m.resizeBootDisk(ctx, node, vmid, diskSize)
	}

	return nil
}

// createBlankVM creates a VM with a blank disk.
func (m *ComputeManager) createBlankVM(ctx context.Context, node string, vmid, diskSize int, storage string, flavor *cpi.Flavor, req *cpi.InstanceRequest) error {
	// Build VM creation parameters
	bridge := m.client.config.DefaultBridge
	if req.NetworkID != "" {
		bridge = req.NetworkID
	}

	params := map[string]interface{}{
		"vmid":    vmid,
		"name":    req.Name,
		"memory":  flavor.RAM,
		"cores":   flavor.VCPUs,
		"sockets": 1,
		"cpu":     "host",
		"ostype":  "l26", // Linux 2.6+ kernel
		"agent":   "1",   // Enable QEMU guest agent
		"net0":    fmt.Sprintf("virtio,bridge=%s,firewall=1", bridge),
		"scsi0":   fmt.Sprintf("%s:%d,format=qcow2", storage, diskSize),
		"scsihw":  "virtio-scsi-pci",
		"boot":    "order=scsi0",
	}

	qemuSvc := m.client.getQemuService()

	upid, err := qemuSvc.Create(ctx, node, params)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	err = m.client.waitForTask(ctx, node, upid, taskTimeoutDefault)
	if err != nil {
		return fmt.Errorf("VM creation task failed: %w", err)
	}

	return nil
}

// finalizeAndStartVM applies cloud-init, tags, and starts the VM.
func (m *ComputeManager) finalizeAndStartVM(ctx context.Context, node string, vmid int, req *cpi.InstanceRequest) (*cpi.Instance, error) {
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

// configureCloudInit applies cloud-init configuration to a freshly cloned
// VM. The order matters: we PUT the directly-supported fields (ipconfig0,
// nameserver, ciuser, sshkeys) first so basic networking + SSH work even
// when snippet upload of the larger UserData payload fails. Many Proxmox
// storage backends (local-lvm, lvm-thin, ZFS) don't accept "snippets"
// content, so attempting that path with the default storage is best-effort.
func (m *ComputeManager) configureCloudInit(ctx context.Context, node string, vmid int, req *cpi.InstanceRequest) error {
	configPath := fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid)

	// 1) Direct cloud-init config — these don't require a snippets-capable
	//    storage and are what makes the VM reachable after first boot.
	directConfig := buildPVEDirectCloudInitConfig(req)
	if len(directConfig) > 0 {
		_, err := m.client.pveClient.PutCtx(ctx, configPath, directConfig)
		if err != nil {
			return fmt.Errorf("failed to set cloud-init direct config: %w", err)
		}
	}

	// 2) Optional user-data snippet — preserves the larger inline bash
	//    script some bastion paths still pass via req.UserData. If this
	//    fails (typical for local-lvm-only clusters) we keep the VM
	//    reachable via the direct config above.
	if req.UserData != "" {
		ciSvc := m.client.getCloudinitService()
		storage := m.snippetStorageFor(node)

		err := ciSvc.Attach(ctx, node, vmid, storage, []byte(req.UserData))
		if err != nil {
			logger.Warnf("Failed to upload cloud-init user-data snippet to storage %s (continuing with direct config only): %v", storage, err)
		}
	}

	return nil
}

// buildPVEDirectCloudInitConfig assembles the cloud-init PUT body that uses
// only PVE config keys (no snippet upload required):
//
//   - ipconfig0/nameserver: static IP + gateway + DNS resolvers for the NIC.
//   - ciuser: default cloud-init user (image dependent — set when caller
//     supplied an explicit value via InstanceRequest.DefaultUsername).
//   - sshkeys: URL-encoded OpenSSH public key list, the form PVE expects.
//
// An empty map is returned when there's nothing to configure, in which case
// the caller skips the PUT entirely.
func buildPVEDirectCloudInitConfig(req *cpi.InstanceRequest) map[string]interface{} {
	out := make(map[string]interface{})

	if ipconfig := buildPVEIPConfig(req); ipconfig != "" {
		out["ipconfig0"] = ipconfig
	}

	if len(req.DNSServers) > 0 {
		out["nameserver"] = strings.Join(req.DNSServers, " ")
	}

	if req.DefaultUsername != "" {
		out["ciuser"] = req.DefaultUsername
	}

	if pub := strings.TrimSpace(req.PublicKey); pub != "" {
		// Proxmox's API requires the sshkeys value to be URL-encoded —
		// otherwise the spaces and trailing comments in OpenSSH lines
		// confuse the form parser. The apiclient passes our string through
		// directly, so we encode here.
		out["sshkeys"] = url.QueryEscape(pub + "\n")
	}

	return out
}

// buildPVEIPConfig converts the static-IP request into PVE's ipconfig0
// string. DHCP is implied when no static address is requested. The /24
// assumption matches the bastion CIDR layout but should be replaced with the
// caller-supplied prefix once that information flows through the request.
func buildPVEIPConfig(req *cpi.InstanceRequest) string {
	if req.StaticPrivateIP == "" {
		return "ip=dhcp"
	}

	addr := req.StaticPrivateIP
	if !strings.Contains(addr, "/") {
		addr += "/24"
	}

	cfg := "ip=" + addr
	if req.GatewayIP != "" {
		cfg += ",gw=" + req.GatewayIP
	}

	return cfg
}

// snippetStorageFor returns the storage pool to use for cloud-init snippet
// uploads. ISOStorage takes precedence (it's where the operator pinned
// snippet-capable storage), otherwise we fall back to the cluster-default
// "local" which is dir-typed on stock installs.
func (m *ComputeManager) snippetStorageFor(_node string) string {
	if m.client.config.ISOStorage != "" {
		return m.client.config.ISOStorage
	}

	return "local"
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

// setVMTags writes the OCFP tag set to PVE in both representations PVE
// supports:
//
//   - The native VM tags field as a semicolon-separated list of identifiers
//     (each "<key>-<value>" lowercased and ASCII-sanitised so it matches PVE's
//     allowed tag charset). This is what's filterable in the UI and what
//     ListInstances reads back to evaluate label-based queries.
//   - The description field as readable "key=value" lines, preserving the
//     original casing/values for operators inspecting the VM.
//
// Both writes happen in one PUT so the VM is fully labelled before the start
// task completes.
func (m *ComputeManager) setVMTags(ctx context.Context, node string, vmid int, tags map[string]string) {
	if len(tags) == 0 {
		return
	}

	// Stable ordering for reproducible PVE tag strings and descriptions.
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	tagList := make([]string, 0, len(keys))
	descParts := make([]string, 0, len(keys))

	for _, k := range keys {
		v := tags[k]
		descParts = append(descParts, fmt.Sprintf("%s=%s", k, v))

		if tag := formatPVETag(k, v); tag != "" {
			tagList = append(tagList, tag)
		}
	}

	description := "OCFP tags: " + strings.Join(descParts, ", ")
	pveTagStr := strings.Join(tagList, ";")

	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid)

	params := map[string]interface{}{
		"description": description,
	}

	if pveTagStr != "" {
		params["tags"] = pveTagStr
	}

	_, _ = m.client.pveClient.PutCtx(ctx, path, params)
}

// formatPVETag renders a "<key>=<value>" pair as a single PVE-compatible tag
// identifier of the form "<key>-<value>". PVE accepts a restricted character
// set per tag (lowercase ASCII alphanumerics plus a handful of separators)
// so any other byte is collapsed to "_". Empty results are filtered out by
// the caller.
func formatPVETag(key, value string) string {
	combined := strings.ToLower(strings.TrimSpace(key) + "-" + strings.TrimSpace(value))
	if combined == "-" {
		return ""
	}

	var builder strings.Builder

	builder.Grow(len(combined))

	for _, runeValue := range combined {
		switch {
		case runeValue >= 'a' && runeValue <= 'z',
			runeValue >= '0' && runeValue <= '9',
			runeValue == '-', runeValue == '_', runeValue == '.':
			builder.WriteRune(runeValue)
		default:
			builder.WriteRune('_')
		}
	}

	return builder.String()
}

// parsePVETags splits PVE's semicolon-separated tag string back into the set
// of canonical tag identifiers. Empty input returns nil.
func parsePVETags(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts))

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}

	return out
}

// matchesLabelFilters reports whether the given PVE-side tag set satisfies
// every "label.<key>=<value>" entry in filters. Non-label filters are
// ignored here — the caller applies them separately (e.g. the "name"
// filter is matched against the VM's name).
//
// The convention mirrors setVMTags: a filter "label.bloc=520-pve-wayne"
// matches when the tag "bloc-520-pve-wayne" (post-sanitisation) is
// present. A VM with no tags can never satisfy a label filter — that's the
// desirable behaviour because it keeps untagged VMs from being returned as
// false-positive bastions.
func matchesLabelFilters(vmTags []string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for key, value := range filters {
		if !strings.HasPrefix(key, "label.") {
			continue
		}

		want := formatPVETag(strings.TrimPrefix(key, "label."), value)
		if want == "" {
			continue
		}

		found := false

		for _, tag := range vmTags {
			if tag == want {
				found = true

				break
			}
		}

		if !found {
			return false
		}
	}

	return true
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

	// QGA may not have reported yet (fresh VM, missing/disabled agent). Fall
	// back to the static IP declared in cloud-init config so callers like
	// `ocfp ssh bastion` can still resolve the host immediately after
	// bootstrap.
	if instance.PrivateIP == "" {
		if ip := extractIPConfigAddress(getStringFromMap(config, "ipconfig0")); ip != "" {
			instance.PrivateIP = ip
		}
	}

	return instance, nil
}

// extractIPConfigAddress pulls just the IPv4 address out of a Proxmox
// cloud-init ipconfigN string. The expected shape is
//
//	"ip=192.168.1.67/24,gw=192.168.1.1[,ip6=...]"
//
// or "ip=dhcp". An empty string is returned for dhcp / unparseable input.
func extractIPConfigAddress(ipconfig string) string {
	if ipconfig == "" {
		return ""
	}

	for _, part := range strings.Split(ipconfig, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "ip=") {
			continue
		}

		val := strings.TrimPrefix(part, "ip=")
		if strings.EqualFold(val, "dhcp") {
			return ""
		}

		// Strip the CIDR suffix if present.
		if slash := strings.IndexByte(val, '/'); slash >= 0 {
			val = val[:slash]
		}

		return val
	}

	return ""
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
