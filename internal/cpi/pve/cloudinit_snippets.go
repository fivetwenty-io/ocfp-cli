package pve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// Snippet content types and naming conventions for PVE storage upload.
const (
	snippetContentType    = "snippets"
	snippetUploadField    = "file"
	snippetUserSuffix     = "-user.yml"
	snippetNetworkSuffix  = "-network.yml"
	defaultCIDRSuffix     = "/24"
	macOctetCount         = 6
	macLocallyAdminBit    = 0x02
	macMulticastBitMask   = 0xFE
	defaultBlobstoreSlash = "/"
)

// snippetStorageCache caches the result of snippet-capable storage lookups
// per-node. The PVE storage list is stable for the lifetime of a session, so
// querying once per node avoids re-walking the cluster for every VM.
type snippetStorageCache struct {
	mu   sync.Mutex
	byNode map[string]string
}

func newSnippetStorageCache() *snippetStorageCache {
	return &snippetStorageCache{byNode: map[string]string{}}
}

// resolveSnippetStorage returns the storage pool that should hold cloud-init
// snippets for VMs on the given node. Precedence:
//
//  1. Config.ISOStorage when set AND the pool advertises "snippets" content.
//  2. First node-local storage whose content list contains "snippets" and
//     whose enabled flag is non-zero.
//  3. "" — caller treats as "no snippet support, skip upload."
//
// Errors from the storage list query collapse to a "" return so callers fall
// back gracefully to the direct cloud-init config.
func (m *ComputeManager) resolveSnippetStorage(ctx context.Context, node string) string {
	if m.snippetStorages == nil {
		m.snippetStorages = newSnippetStorageCache()
	}

	m.snippetStorages.mu.Lock()
	defer m.snippetStorages.mu.Unlock()

	if cached, ok := m.snippetStorages.byNode[node]; ok {
		return cached
	}

	storages := m.listNodeStorages(ctx, node)
	choice := pickSnippetStorage(storages, m.client.config.ISOStorage)
	m.snippetStorages.byNode[node] = choice

	return choice
}

// listNodeStorages fetches /nodes/{node}/storage and normalises the entries
// the resolver cares about (name, content, enabled).
func (m *ComputeManager) listNodeStorages(ctx context.Context, node string) []nodeStorage {
	path := fmt.Sprintf("/nodes/%s/storage", node)

	resp, err := m.client.pveClient.GetCtx(ctx, path, nil)
	if err != nil {
		return nil
	}

	raw, ok := resp.([]interface{})
	if !ok {
		return nil
	}

	out := make([]nodeStorage, 0, len(raw))

	for _, item := range raw {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		out = append(out, nodeStorage{
			name:    getStringFromMap(entry, "storage"),
			content: getStringFromMap(entry, "content"),
			enabled: storageEnabled(entry),
		})
	}

	return out
}

type nodeStorage struct {
	name    string
	content string
	enabled bool
}

// storageEnabled reads the "enabled" / "active" hint from a storage entry.
// PVE returns the value as a number (1=enabled, 0=disabled) but tolerates an
// absent field which we interpret as enabled.
func storageEnabled(entry map[string]interface{}) bool {
	if v, ok := entry["enabled"].(float64); ok {
		return v != 0
	}

	if v, ok := entry["active"].(float64); ok {
		return v != 0
	}

	return true
}

// pickSnippetStorage filters the supplied entries by snippet capability and
// returns the chosen pool name. The ISOStorage override wins when it is
// present and snippet-capable; otherwise the first eligible entry takes it.
func pickSnippetStorage(entries []nodeStorage, isoStorage string) string {
	if isoStorage != "" {
		for _, e := range entries {
			if e.name == isoStorage && e.enabled && storageSupportsSnippets(e.content) {
				return e.name
			}
		}
	}

	for _, e := range entries {
		if e.enabled && storageSupportsSnippets(e.content) {
			return e.name
		}
	}

	return ""
}

func storageSupportsSnippets(content string) bool {
	for _, part := range strings.Split(content, ",") {
		if strings.EqualFold(strings.TrimSpace(part), snippetContentType) {
			return true
		}
	}

	return false
}

// uploadSnippet PUTs a cloud-init snippet to a snippets-capable storage pool
// using the PVE upload endpoint. Unlike ciSvc.Attach this DOES NOT touch ide2
// — the template clone already attaches an ide2 cloud-init drive on the
// correct storage, and re-pointing it at a non-images pool fails VM start.
func (m *ComputeManager) uploadSnippet(ctx context.Context, node, storage, filename string, payload []byte) error {
	path := fmt.Sprintf("/nodes/%s/storage/%s/upload", node, storage)
	fields := map[string]string{
		"content":  snippetContentType,
		"filename": filename,
	}

	_, err := m.client.pveClient.UploadCtx(ctx, path, fields, snippetUploadField, filename, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("upload snippet %s to %s/%s: %w", filename, node, storage, err)
	}

	return nil
}

// snippetBaseName returns the stable prefix used for snippet filenames. Tied
// to VMID so two runs of the same bloc don't stomp each other's snippets
// while both bastions exist (e.g. during a teardown + bootstrap cycle that
// keeps PVE state around for a few seconds).
func snippetBaseName(req *cpi.InstanceRequest, vmid int) string {
	name := sanitizeForFilename(req.Name)
	if name == "" {
		name = "ocfp"
	}

	return fmt.Sprintf("%s-%d", name, vmid)
}

// sanitizeForFilename strips characters PVE rejects in upload filenames.
func sanitizeForFilename(s string) string {
	var b strings.Builder

	b.Grow(len(s))

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	return b.String()
}

// buildUserDataSnippet renders a cloud-config user-data document that sets
// hostname/FQDN and reinstates the SSH keys + default user. PVE's direct
// cloud-init keys can't set hostname, so this snippet is the only way to
// stop a cloned VM from booting as the template default.
func buildUserDataSnippet(req *cpi.InstanceRequest) []byte {
	host := strings.TrimSpace(req.Hostname)
	domain := strings.TrimSpace(req.DomainSuffix)
	fqdn := host

	if host != "" && domain != "" {
		fqdn = host + "." + domain
	}

	user := strings.TrimSpace(req.DefaultUsername)
	pubKey := strings.TrimSpace(req.PublicKey)

	var buf bytes.Buffer

	buf.WriteString("#cloud-config\n")

	if host != "" {
		fmt.Fprintf(&buf, "hostname: %s\n", host)
	}

	if fqdn != "" {
		fmt.Fprintf(&buf, "fqdn: %s\n", fqdn)
	}

	buf.WriteString("manage_etc_hosts: true\n")
	buf.WriteString("preserve_hostname: false\n")

	if user != "" {
		buf.WriteString("users:\n")
		fmt.Fprintf(&buf, "  - name: %s\n", user)
		buf.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
		buf.WriteString("    shell: /bin/bash\n")

		if pubKey != "" {
			buf.WriteString("    ssh_authorized_keys:\n")

			for _, line := range strings.Split(pubKey, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					continue
				}

				fmt.Fprintf(&buf, "      - %s\n", trimmed)
			}
		}
	}

	buf.WriteString("chpasswd: { expire: false }\n")
	buf.WriteString("ssh_pwauth: false\n")

	return buf.Bytes()
}

// buildNetworkDataSnippet renders a cloud-init network-data v2 document that
// matches the supplied MAC and sets the configured static IPv4. v2 lets us
// rename the NIC to `eth0` regardless of the kernel-assigned predictable
// name (ens18 on Ubuntu 22.04+), making the static IP apply consistently.
func buildNetworkDataSnippet(req *cpi.InstanceRequest, mac string) []byte {
	addr, prefix := splitCIDR(req.StaticPrivateIP)
	if addr == "" {
		return nil
	}

	mac = strings.ToLower(strings.TrimSpace(mac))

	var buf bytes.Buffer

	buf.WriteString("version: 2\n")
	buf.WriteString("ethernets:\n")
	buf.WriteString("  primary:\n")

	if mac != "" {
		buf.WriteString("    match:\n")
		fmt.Fprintf(&buf, "      macaddress: \"%s\"\n", mac)
		buf.WriteString("    set-name: eth0\n")
	} else {
		buf.WriteString("    match:\n")
		buf.WriteString("      name: en*\n")
		buf.WriteString("    set-name: eth0\n")
	}

	fmt.Fprintf(&buf, "    addresses: [%s/%d]\n", addr, prefix)

	if gw := strings.TrimSpace(req.GatewayIP); gw != "" {
		fmt.Fprintf(&buf, "    gateway4: %s\n", gw)
	}

	if len(req.DNSServers) > 0 || strings.TrimSpace(req.DomainSuffix) != "" {
		buf.WriteString("    nameservers:\n")

		if len(req.DNSServers) > 0 {
			buf.WriteString("      addresses: [")

			for i, ns := range req.DNSServers {
				if i > 0 {
					buf.WriteString(", ")
				}

				buf.WriteString(strings.TrimSpace(ns))
			}

			buf.WriteString("]\n")
		}

		if strings.TrimSpace(req.DomainSuffix) != "" {
			fmt.Fprintf(&buf, "      search: [%s]\n", strings.TrimSpace(req.DomainSuffix))
		}
	}

	return buf.Bytes()
}

// splitCIDR returns the address portion and the prefix length. When the
// caller omits the suffix we fall back to /24 — matches PVE's existing
// ipconfig0 default.
func splitCIDR(s string) (string, int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0
	}

	if _, ipnet, err := net.ParseCIDR(s); err == nil {
		ones, _ := ipnet.Mask.Size()

		return strings.SplitN(s, defaultBlobstoreSlash, 2)[0], ones
	}

	if ip := net.ParseIP(s); ip != nil {
		return s, 24 //nolint:mnd // documented default matches PVE's ipconfig0 fallback
	}

	return "", 0
}

// generateDeterministicMAC returns a locally-administered unicast MAC seeded
// by the supplied identifier. Used so cloned VMs receive the same MAC on
// retry and the matching network-data snippet keeps applying.
//
// First byte is forced to 0x02 (locally administered bit set, multicast bit
// cleared); the remaining five bytes come from SHA-256(seed)[:5].
func generateDeterministicMAC(seed string) string {
	sum := sha256.Sum256([]byte(seed))

	bytes := make([]byte, macOctetCount)
	bytes[0] = (sum[0] | macLocallyAdminBit) & macMulticastBitMask

	for i := 1; i < macOctetCount; i++ {
		bytes[i] = sum[i]
	}

	parts := make([]string, macOctetCount)
	for i, b := range bytes {
		parts[i] = fmt.Sprintf("%02x", b)
	}

	return strings.Join(parts, ":")
}

// cloudInitSnippetPlan records the result of uploadCloudInitSnippets so the
// caller can wire cicustom and net0 in the subsequent PUT.
type cloudInitSnippetPlan struct {
	Storage         string
	UserFilename    string
	NetworkFilename string
	MAC             string
	HasNetwork      bool
}

// uploadCloudInitSnippets builds and pushes the user-data + network-data
// snippets when a snippet-capable storage is available. The MAC is generated
// even when no storage is found so the caller can still set net0 with a
// stable MAC; only the snippet upload requires the storage.
func (m *ComputeManager) uploadCloudInitSnippets(ctx context.Context, node string, vmid int, req *cpi.InstanceRequest) cloudInitSnippetPlan {
	plan := cloudInitSnippetPlan{
		MAC: generateDeterministicMAC(fmt.Sprintf("%s-%d", req.Name, vmid)),
	}

	storage := m.resolveSnippetStorage(ctx, node)
	if storage == "" {
		return plan
	}

	plan.Storage = storage

	base := snippetBaseName(req, vmid)
	userFilename := base + snippetUserSuffix
	networkFilename := base + snippetNetworkSuffix

	userData := buildUserDataSnippet(req)

	err := m.uploadSnippet(ctx, node, storage, userFilename, userData)
	if err != nil {
		plan.Storage = ""

		return plan
	}

	plan.UserFilename = userFilename

	networkData := buildNetworkDataSnippet(req, plan.MAC)
	if len(networkData) == 0 {
		return plan
	}

	err = m.uploadSnippet(ctx, node, storage, networkFilename, networkData)
	if err != nil {
		return plan
	}

	plan.NetworkFilename = networkFilename
	plan.HasNetwork = true

	return plan
}
