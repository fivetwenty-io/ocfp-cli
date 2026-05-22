package pve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/nodes"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// templateVMIDFloor is the lower bound for cluster.nextid when allocating
	// VMIDs for OCFP-provisioned templates. Convention: 9000-9099. Operators
	// keep workload VMs below this range.
	templateVMIDFloor int64 = 9000

	// templateTaskTimeoutDownload bounds the per-image download phase.
	// Cloud images are ~600MB; allow generous headroom for slow links.
	templateTaskTimeoutDownload = 1200

	// templateTaskTimeoutCreate bounds the VM-create + import-from disk phase.
	templateTaskTimeoutCreate = 600

	// templateTaskTimeoutFlag bounds the qm-template conversion.
	templateTaskTimeoutFlag = 60
)

// ErrTemplateAutoProvisionUnknown is returned when ProvisionTemplate is asked
// for a template name that is not in templateCatalog.
var ErrTemplateAutoProvisionUnknown = errors.New("pve: template name not in auto-provision catalog")

// TemplateSpec describes a cloud-image template that OCFP knows how to
// provision automatically. The catalog is compile-time so new entries require
// a code change — operators don't add templates via YAML.
type TemplateSpec struct {
	// Name is the canonical template name as referenced from bloc configs
	// (e.g., "ubuntu-noble-template").
	Name string

	// SourceURL is the canonical download location for the .img file.
	SourceURL string

	// SourceFilename is the on-storage filename after download. Must match
	// the final path component of SourceURL for clarity.
	SourceFilename string

	// Memory is the template VM memory in MiB.
	Memory int

	// Cores is the template VM core count.
	Cores int

	// RequireBastionUnits, when true, makes ProvisionTemplate boot the VM
	// after create and seed it with the OCFP firstboot + watchdog units
	// via termproxy before converting to a template. Bastions need this
	// because tailscale install + watchdog must be baked into the image
	// (PVE 9.x can't deliver snippets to per-VM clones).
	RequireBastionUnits bool
}

// templateCatalog is the compile-time registry of known templates.
//
//nolint:gochecknoglobals // intentional package-level lookup table
var templateCatalog = map[string]TemplateSpec{
	"ubuntu-noble-template": {
		Name:      "ubuntu-noble-template",
		SourceURL: "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		// Ubuntu's .img cloud images are actually qcow2-formatted.
		// PVE's "import" content type rejects .img extensions, so store
		// with .qcow2 to match the validator regex.
		SourceFilename: "ubuntu-noble-amd64.qcow2",
		Memory:         2048,
		Cores:          2,
	},
	"ubuntu-jammy-template": {
		Name:           "ubuntu-jammy-template",
		SourceURL:      "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
		SourceFilename: "ubuntu-jammy-amd64.qcow2",
		Memory:         2048,
		Cores:          2,
	},
	// Bastion templates: same source image, plus seeded firstboot + watchdog
	// units. The seed boots the VM once, installs jq + qemu-guest-agent, writes
	// the scripts under /usr/local/sbin/, enables systemd units, and shuts down
	// before `qm template`. Cloned bastions inherit the units and run them on
	// first boot with per-VM config delivered via SMBIOS.
	"ubuntu-noble-bastion-template": {
		Name:                "ubuntu-noble-bastion-template",
		SourceURL:           "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		SourceFilename:      "ubuntu-noble-amd64.qcow2",
		Memory:              2048,
		Cores:               2,
		RequireBastionUnits: true,
	},
}

// LookupCatalogSpec returns the catalog entry for a template name, or false if
// the name is unknown. Exported so callers (e.g. bootstrap.resolveImageID) can
// decide whether to attempt auto-provisioning.
func LookupCatalogSpec(name string) (TemplateSpec, bool) {
	spec, ok := templateCatalog[name]
	return spec, ok
}

// LookupTemplateByName scans the cluster for a VM with template=1 and the
// given name. Returns nil + nil error when not found (caller distinguishes
// "absent" from "lookup failure").
func (m *ComputeManager) LookupTemplateByName(ctx context.Context, name string) (*cpi.Image, error) {
	images, err := m.ListImages(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}

	for _, img := range images {
		if img.Name == name {
			return img, nil
		}
	}

	return nil, nil
}

// ProvisionTemplate provisions the named template if it is not already present
// on the cluster. The flow is:
//
//  1. Lookup — if present, return its VMID.
//  2. Allocate a VMID from the 9000+ range via cluster.nextid.
//  3. Download the source image to the configured ISO storage via PVE's
//     download-url task.
//  4. Create the template VM with `import-from=<scratch-volid>` so PVE
//     converts the downloaded image directly into the VM's scsi0 disk.
//  5. Convert to a template via qemu.Template.
//
// Returns the VMID of the resulting template (existing or newly created).
// Storage pools are pulled from the PVE Client config (DefaultStorage for the
// template disk, ISOStorage for the scratch download).
func (m *ComputeManager) ProvisionTemplate(ctx context.Context, name string) (int, error) {
	spec, ok := templateCatalog[name]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrTemplateAutoProvisionUnknown, name)
	}

	log := logger.WithOperation("ProvisionTemplate")

	existing, err := m.LookupTemplateByName(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("pre-flight lookup: %w", err)
	}

	if existing != nil {
		vmid, convErr := strconv.Atoi(existing.ID)
		if convErr != nil {
			return 0, fmt.Errorf("existing template %s has non-numeric VMID %q: %w", name, existing.ID, convErr)
		}

		log.Infof("template %s already present (vmid %d)", name, vmid)

		return vmid, nil
	}

	targetStorage := m.client.config.DefaultStorage
	if targetStorage == "" {
		return 0, fmt.Errorf("PVE config DefaultStorage required for template provisioning")
	}

	isoStorage := m.client.config.ISOStorage
	if isoStorage == "" {
		return 0, fmt.Errorf("PVE config ISOStorage required for template provisioning (set provider.iso_storage)")
	}

	node, err := m.client.getNode(ctx)
	if err != nil {
		return 0, fmt.Errorf("resolve node: %w", err)
	}

	vmid, err := m.client.nextTemplateVMID(ctx)
	if err != nil {
		return 0, fmt.Errorf("allocate VMID: %w", err)
	}

	log.Infof("provisioning template %s as vmid %d on node %s (target=%s, iso=%s)", name, vmid, node, targetStorage, isoStorage)

	err = m.client.downloadTemplateImage(ctx, node, isoStorage, spec)
	if err != nil {
		return 0, fmt.Errorf("download image: %w", err)
	}

	scratchVolID := fmt.Sprintf("%s:import/%s", isoStorage, spec.SourceFilename)

	err = m.createTemplateVM(ctx, node, targetStorage, vmid, scratchVolID, spec)
	if err != nil {
		return 0, fmt.Errorf("create template VM: %w", err)
	}

	if spec.RequireBastionUnits {
		if err := m.seedBastionTemplate(ctx, node, vmid); err != nil {
			return 0, fmt.Errorf("seed bastion template: %w", err)
		}
	}

	err = m.convertToTemplate(ctx, node, vmid)
	if err != nil {
		return 0, fmt.Errorf("convert to template: %w", err)
	}

	log.Infof("template %s ready (vmid %d)", name, vmid)

	return vmid, nil
}

// seedBastionTemplate configures the template VM for cloud-init login, starts
// it, drives the firstboot+watchdog seed via termproxy, and waits for the VM
// to halt cleanly. The VM stays in `stopped` state on return so the caller
// can `qm template` it.
func (m *ComputeManager) seedBastionTemplate(ctx context.Context, node string, vmid int) error {
	log := logger.WithOperation("seedBastionTemplate")

	// Update the VM config with the credentials + DHCP needed for termproxy
	// login and apt internet egress. These are wiped by `cloud-init clean`
	// inside the seed; cloned VMs get their own ciuser/cipassword from the
	// bloc config.
	configPath := fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid)

	// Override net0 to use vmbr1 (PVE host's default WAN bridge) for the
	// template build phase. The bloc's own SDN bridge (lvnet001 etc.)
	// typically doesn't run DHCP — VMs there use static IPs from state —
	// so the template VM can't reach apt without an internet-capable
	// bridge during seed. vmbr1 is wiped from the template config below
	// before convert-to-template via cloud-init clean so cloned VMs get
	// the per-bloc bridge from their own InstanceRequest.
	_, err := m.client.pveClient.PutCtx(ctx, configPath, map[string]interface{}{
		"ciuser":     templateSeedCIUser,
		"cipassword": templateSeedCIPassword,
		"ipconfig0":  "ip=dhcp",
		"net0":       "virtio,bridge=vmbr1",
	})
	if err != nil {
		return fmt.Errorf("seed config PUT: %w", err)
	}

	qemuSvc := m.client.getQemuService()

	startUPID, err := qemuSvc.Start(ctx, node, vmid)
	if err != nil {
		return fmt.Errorf("seed start: %w", err)
	}

	if startUPID != "" {
		if err := m.client.waitForTask(ctx, node, startUPID, 60); err != nil {
			return fmt.Errorf("await seed start: %w", err)
		}
	}

	log.Infof("template VM %d booted; running seed via termproxy", vmid)

	if err := m.seedTemplateVM(ctx, node, vmid); err != nil {
		return fmt.Errorf("seed termproxy: %w", err)
	}

	// Wait for the guest's `shutdown -h now` to actually halt the VM.
	if err := waitForVMStopped(ctx, m.client, node, vmid, 120*time.Second); err != nil {
		// Fallback: force stop if the guest never finished shutting down.
		log.Warnf("seed VM did not stop within deadline; forcing stop: %v", err)

		stopUPID, stopErr := qemuSvc.Stop(ctx, node, vmid)
		if stopErr != nil {
			return fmt.Errorf("seed force stop: %w", stopErr)
		}

		if stopUPID != "" {
			_ = m.client.waitForTask(ctx, node, stopUPID, 30)
		}
	}

	return nil
}

// waitForVMStopped polls qemu status until the VM reports `stopped` or the
// deadline elapses.
func waitForVMStopped(ctx context.Context, c *Client, node string, vmid int, timeout time.Duration) error {
	qemuSvc := c.getQemuService()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		status, err := qemuSvc.Status(ctx, node, vmid)
		if err == nil && status != nil {
			if s, ok := status["status"].(string); ok && s == "stopped" {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}

	return fmt.Errorf("timeout waiting for vmid %d to stop", vmid)
}

// createTemplateVM creates the VM that will become the template. The
// `import-from=<scratch-volid>` syntax tells PVE to convert the downloaded
// image directly into scsi0 — no separate `qm importdisk` call needed.
func (m *ComputeManager) createTemplateVM(ctx context.Context, node, targetStorage string, vmid int, scratchVolID string, spec TemplateSpec) error {
	params := map[string]interface{}{
		"vmid":    vmid,
		"name":    spec.Name,
		"memory":  spec.Memory,
		"cores":   spec.Cores,
		"net0":    "virtio,bridge=" + m.client.config.DefaultBridge,
		"scsihw":  "virtio-scsi-pci",
		"scsi0":   fmt.Sprintf("%s:0,import-from=%s,discard=on,ssd=1", targetStorage, scratchVolID),
		"ide2":    targetStorage + ":cloudinit",
		"serial0": "socket",
		"vga":     "serial0",
		"agent":   "enabled=1",
		"ostype":  "l26",
		"boot":    "order=scsi0",
	}

	qemuSvc := m.client.getQemuService()

	upid, err := qemuSvc.Create(ctx, node, params)
	if err != nil {
		return fmt.Errorf("qemu create: %w", err)
	}

	if upid == "" {
		return nil
	}

	err = m.client.waitForTask(ctx, node, upid, templateTaskTimeoutCreate)
	if err != nil {
		return fmt.Errorf("await qemu create task: %w", err)
	}

	return nil
}

// convertToTemplate calls qm-template on the staged VM. After this returns
// the VM has `template=1` and is no longer bootable as a regular instance.
func (m *ComputeManager) convertToTemplate(ctx context.Context, node string, vmid int) error {
	qemuSvc := m.client.getQemuService()

	upid, err := qemuSvc.Template(ctx, node, vmid)
	if err != nil {
		return fmt.Errorf("qemu template: %w", err)
	}

	if upid == "" {
		return nil
	}

	err = m.client.waitForTask(ctx, node, upid, templateTaskTimeoutFlag)
	if err != nil {
		return fmt.Errorf("await qemu template task: %w", err)
	}

	return nil
}

// nextTemplateVMID returns the lowest free VMID in the OCFP template range
// (≥ 9000). Despite its name, PVE's `/cluster/nextid?vmid=N` validates that
// N specifically is free — it does NOT return the next free id ≥ N. We
// instead probe the cluster's resource list once and pick the smallest gap.
func (c *Client) nextTemplateVMID(ctx context.Context) (int, error) {
	used, err := c.usedClusterVMIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("inventory cluster VMIDs: %w", err)
	}

	for id := int(templateVMIDFloor); id < int(templateVMIDFloor)+100; id++ {
		if !used[id] {
			return id, nil
		}
	}

	return 0, fmt.Errorf("no free VMID in template range %d-%d", templateVMIDFloor, int(templateVMIDFloor)+99)
}

// usedClusterVMIDs returns the set of VMIDs currently allocated across the
// cluster (both running VMs and stopped templates).
func (c *Client) usedClusterVMIDs(ctx context.Context) (map[int]bool, error) {
	resp, err := c.pveClient.GetCtx(ctx, "/cluster/resources?type=vm", nil)
	if err != nil {
		return nil, fmt.Errorf("GET /cluster/resources: %w", err)
	}

	raw, ok := resp.([]interface{})
	if !ok {
		return nil, fmt.Errorf("cluster/resources: unexpected response shape %T", resp)
	}

	used := make(map[int]bool, len(raw))

	for _, item := range raw {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		if v, ok := entry["vmid"].(float64); ok {
			used[int(v)] = true
		}
	}

	return used, nil
}

// downloadTemplateImage triggers PVE's native download-url task to fetch the
// cloud image directly onto the node into `storage`. Blocks until the task
// completes. Earlier doc claimed PVE returns success if the file already
// exists; it doesn't — it refuses to overwrite. Probe first and skip the
// download when the file is already present (the bastion template re-uses
// the vanilla template's qcow2).
func (c *Client) downloadTemplateImage(ctx context.Context, node, storage string, spec TemplateSpec) error {
	exists, err := c.importVolumeExists(ctx, node, storage, spec.SourceFilename)
	if err != nil {
		logger.Warnf("could not probe import storage for %s; attempting download anyway: %v", spec.SourceFilename, err)
	}

	if exists {
		logger.Debugf("image %s already on %s; skipping download", spec.SourceFilename, storage)
		return nil
	}

	nodesSvc := nodes.New(c.pveClient)

	// PVE 8.2+ uses content type "import" for raw cloud images that will be
	// imported into a VM disk via the `import-from=<volid>` config syntax.
	// The legacy "iso" content type rejects scsi0 import with a wrong-type
	// error.
	params := &nodes.CreateStorageDownloadUrlParams{
		Content:  "import",
		Filename: spec.SourceFilename,
		Url:      spec.SourceURL,
	}

	resp, err := nodesSvc.CreateStorageDownloadUrl(ctx, node, storage, params)
	if err != nil {
		return fmt.Errorf("nodes.CreateStorageDownloadUrl: %w", err)
	}

	upid, err := parseUPID(resp)
	if err != nil {
		return fmt.Errorf("parse download UPID: %w", err)
	}

	if upid == "" {
		return nil
	}

	err = c.waitForTask(ctx, node, upid, templateTaskTimeoutDownload)
	if err != nil {
		return fmt.Errorf("await download task: %w", err)
	}

	return nil
}

// importVolumeExists returns true when filename is already in storage's
// "import" content view (PVE 8.2+ filename format: <storage>:import/<file>).
func (c *Client) importVolumeExists(ctx context.Context, node, storage, filename string) (bool, error) {
	path := fmt.Sprintf("/nodes/%s/storage/%s/content?content=import", node, storage)

	resp, err := c.pveClient.GetCtx(ctx, path, nil)
	if err != nil {
		return false, err
	}

	raw, ok := resp.([]interface{})
	if !ok {
		return false, nil
	}

	want := fmt.Sprintf("%s:import/%s", storage, filename)
	for _, item := range raw {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		if vid, ok := entry["volid"].(string); ok && vid == want {
			return true, nil
		}
	}

	return false, nil
}

// parseUPID extracts a UPID string from a raw-JSON response. The PVE
// download-url endpoint returns the UPID either as a bare quoted string or
// as a JSON object with a `upid` field; this helper handles both shapes.
func parseUPID(resp *nodes.CreateStorageDownloadUrlResponse) (string, error) {
	if resp == nil || len(*resp) == 0 {
		return "", nil
	}

	raw := *resp

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString, nil
	}

	var asObject map[string]interface{}
	if err := json.Unmarshal(raw, &asObject); err == nil {
		if v, ok := asObject["upid"].(string); ok {
			return v, nil
		}
	}

	return "", fmt.Errorf("unrecognized UPID payload: %s", string(raw))
}
