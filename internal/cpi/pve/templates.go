package pve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/cluster"
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
}

// templateCatalog is the compile-time registry of known templates.
//
//nolint:gochecknoglobals // intentional package-level lookup table
var templateCatalog = map[string]TemplateSpec{
	"ubuntu-noble-template": {
		Name: "ubuntu-noble-template",
		SourceURL: "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		// Ubuntu's .img cloud images are actually qcow2-formatted.
		// PVE's "import" content type rejects .img extensions, so store
		// with .qcow2 to match the validator regex.
		SourceFilename: "ubuntu-noble-amd64.qcow2",
		Memory:         2048,
		Cores:          2,
	},
	"ubuntu-jammy-template": {
		Name: "ubuntu-jammy-template",
		SourceURL: "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
		SourceFilename: "ubuntu-jammy-amd64.qcow2",
		Memory:         2048,
		Cores:          2,
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

	err = m.convertToTemplate(ctx, node, vmid)
	if err != nil {
		return 0, fmt.Errorf("convert to template: %w", err)
	}

	log.Infof("template %s ready (vmid %d)", name, vmid)

	return vmid, nil
}

// createTemplateVM creates the VM that will become the template. The
// `import-from=<scratch-volid>` syntax tells PVE to convert the downloaded
// image directly into scsi0 — no separate `qm importdisk` call needed.
func (m *ComputeManager) createTemplateVM(ctx context.Context, node, targetStorage string, vmid int, scratchVolID string, spec TemplateSpec) error {
	params := map[string]interface{}{
		"vmid":     vmid,
		"name":     spec.Name,
		"memory":   spec.Memory,
		"cores":    spec.Cores,
		"net0":     "virtio,bridge=" + m.client.config.DefaultBridge,
		"scsihw":   "virtio-scsi-pci",
		"scsi0":    fmt.Sprintf("%s:0,import-from=%s,discard=on,ssd=1", targetStorage, scratchVolID),
		"ide2":     targetStorage + ":cloudinit",
		"serial0":  "socket",
		"vga":      "serial0",
		"agent":    "enabled=1",
		"ostype":   "l26",
		"boot":     "order=scsi0",
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

// nextTemplateVMID asks PVE for the next free VMID at or above 9000. The
// `vmid` query parameter is the lower bound; PVE returns the first unused
// id ≥ that value.
func (c *Client) nextTemplateVMID(ctx context.Context) (int, error) {
	clusterSvc := cluster.New(c.pveClient)

	floor := templateVMIDFloor

	resp, err := clusterSvc.ListNextid(ctx, &cluster.ListNextidParams{Vmid: &floor})
	if err != nil {
		return 0, fmt.Errorf("cluster.ListNextid: %w", err)
	}

	if resp == nil || len(*resp) == 0 {
		return 0, fmt.Errorf("cluster.ListNextid: empty response")
	}

	// PVE returns the next id as a JSON-encoded string (e.g. `"9000"`).
	// Trim quotes and parse.
	raw := strings.TrimSpace(string(*resp))
	raw = strings.Trim(raw, `"`)

	id, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse VMID %q: %w", raw, err)
	}

	if int64(id) < templateVMIDFloor {
		return 0, fmt.Errorf("cluster.ListNextid returned %d below floor %d", id, templateVMIDFloor)
	}

	return id, nil
}

// downloadTemplateImage triggers PVE's native download-url task to fetch the
// cloud image directly onto the node into `storage`. Blocks until the task
// completes. If the file already exists on storage, PVE returns success
// without re-downloading.
func (c *Client) downloadTemplateImage(ctx context.Context, node, storage string, spec TemplateSpec) error {
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
