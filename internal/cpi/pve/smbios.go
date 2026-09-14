package pve

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// SMBIOSPayload carries role-specific configuration OCFP injects into a VM via
// PVE's smbios1 string slots. The guest reads the fields back with `dmidecode`
// at first boot:
//
//	Serial → `dmidecode -s system-serial-number` (Type 1, Serial Number)
//	SKU    → `dmidecode -s system-sku-number`    (Type 1, SKU Number)
//	Family → `dmidecode -s system-family`        (Type 1, Family)
//
// SMBIOS injection replaces the snippet-upload path because PVE 9.x's
// /storage/<pool>/upload API explicitly forbids content=snippets (enum
// includes only iso, vztmpl, import). This mechanism is API-only — no SSH
// or file-delivery to the PVE host is required.
type SMBIOSPayload struct {
	// Serial carries an opaque secret (e.g. a tailscale auth key) the
	// guest-side firstboot script reads via dmidecode and acts on.
	Serial string

	// SKU carries a JSON-encoded config blob describing how the firstboot
	// script should configure the host (hostname, tags, routes, watchdog
	// cadence, etc.). Kept JSON so future additions stay backwards
	// compatible without schema migrations.
	SKU string

	// Family is a role discriminator (e.g. "ocfp-bastion"). The firstboot
	// script no-ops unless Family matches the role it implements, so the
	// same firstboot can ship in every cloned VM safely.
	Family string
}

// IsEmpty reports whether the payload has no fields set. Callers use this to
// skip the smbios1 PUT entirely so PVE keeps its zero-config default.
func (p SMBIOSPayload) IsEmpty() bool {
	return p.Serial == "" && p.SKU == "" && p.Family == ""
}

// smbiosMaxLength is PVE's cap on the smbios1 config string.
//
// Everything in that string is base64, so the encoding costs a third again on
// top of the JSON. A bastion carrying a tailscale block, an ingress block, and
// a data block with the bootstrap public key in it renders well past the cap,
// and PVE then rejects the whole value: the VM comes up with no SKU, which
// means no tailscale and an unprepared data disk.
const smbiosMaxLength = 512

// dataBlock renders the data-disk block, leaving out anything the guest
// already defaults.
//
// The dataset script defaults the mountpoint, the filesystem, the home
// directory, and the user, so sending them spends budget and buys nothing. The
// serial is the one field it cannot work without: given no serial it refuses
// to guess a device, and the disk goes unmounted.
func dataBlock(data *cpi.DataDiskSpec) map[string]interface{} {
	block := map[string]interface{}{"serial": data.Serial}

	for key, value := range map[string]string{
		"mountpoint":     data.Mountpoint,
		"filesystem":     data.Filesystem,
		"home":           data.HomeDir,
		"user":           data.User,
		"authorized_key": data.AuthorizedKey,
	} {
		if value != "" {
			block[key] = value
		}
	}

	return block
}

// trimToFit drops the optional data fields, largest first, until the rendered
// value fits PVE's limit.
//
// Dropping is better than sending something PVE refuses, because a refused
// smbios1 costs the guest every field rather than one. The order is chosen so
// the fields the guest can default go first and the bootstrap public key,
// which it cannot, goes last. The disk serial is never dropped.
func trimToFit(skuMap map[string]interface{}, serial, family string) string {
	render := func() string {
		blob, err := json.Marshal(skuMap)
		if err != nil {
			return ""
		}

		return renderSMBIOS(serial, string(blob), family)
	}

	value := render()

	block, _ := skuMap["data"].(map[string]interface{})
	if block == nil {
		return value
	}

	for _, field := range []string{"home", "user", "mountpoint", "filesystem", "authorized_key"} {
		if len(value) <= smbiosMaxLength {
			return value
		}

		if _, present := block[field]; !present {
			continue
		}

		delete(block, field)

		value = render()
	}

	return value
}

// BastionSMBIOSPayload builds the SMBIOS payload from the bastion's
// tailscale + cloudflare + ingress specs. Serial carries the tailscale auth
// key; SKU is a JSON blob the firstboot script jq-parses.
func BastionSMBIOSPayload(
	ts *cpi.TailscaleSpec,
	cf *cpi.CloudflareSpec,
	ing *cpi.IngressSpec,
	data *cpi.DataDiskSpec,
) SMBIOSPayload {
	// The payload used to be gated entirely on a tailscale auth key. It no
	// longer can be: the data-disk block has to reach a bastion whether or
	// not tailscale is configured, or a tailscale-less bloc would boot with
	// an unprepared disk and an empty home directory.
	hasTailscale := ts != nil && ts.AuthKey != ""
	if !hasTailscale && data == nil {
		return SMBIOSPayload{}
	}

	// Marshal the role config as compact JSON; the guest scripts `jq`-parse
	// fields with sane defaults so we can add keys later without breaking
	// older templates.
	skuMap := map[string]interface{}{"v": 1}

	if hasTailscale {
		skuMap["hostname"] = ts.Hostname
		skuMap["tags"] = ts.Tags
		skuMap["accept_dns"] = ts.AcceptDNS
		skuMap["accept_routes"] = ts.AcceptRoutes
		skuMap["ssh"] = ts.SSH
		skuMap["exit_node"] = ts.ExitNode
		skuMap["advertise_routes"] = ts.AdvertiseRoutes
	}

	if data != nil {
		skuMap["data"] = dataBlock(data)
	}
	if cf != nil && cf.TunnelToken != "" {
		skuMap["cloudflare"] = map[string]interface{}{"token": cf.TunnelToken}
	}

	if ing != nil && ing.OriginIP != "" {
		skuMap["ingress"] = map[string]interface{}{
			"origin_ip": ing.OriginIP,
			"ports":     ing.Ports,
		}
	}

	// Trim before returning, so the caller never renders something PVE
	// refuses. Trimming reads the rendered length rather than the JSON
	// length, because base64 is what the limit actually applies to.
	trimToFit(skuMap, authKeyOf(ts), smbiosFamilyBastion)

	sku, err := json.Marshal(skuMap)
	if err != nil {
		// Marshalling fixed-shape data shouldn't realistically fail; if it
		// does, fall back to a minimal payload so the bastion at least has
		// the auth key and can join the tailnet manually.
		return SMBIOSPayload{Serial: authKeyOf(ts), Family: smbiosFamilyBastion}
	}

	return SMBIOSPayload{
		Serial: authKeyOf(ts),
		SKU:    string(sku),
		Family: smbiosFamilyBastion,
	}
}

// authKeyOf returns the tailscale auth key, or empty when there is no spec.
func authKeyOf(ts *cpi.TailscaleSpec) string {
	if ts == nil {
		return ""
	}

	return ts.AuthKey
}

// BastionSpecToSMBIOSPayload builds the SMBIOS payload from the bastion's
// tailscale + cloudflare specs. Serial carries the tailscale auth key; SKU is
// a JSON blob the firstboot script jq-parses, now including a "cloudflare"
// object with the connector token.
func BastionSpecToSMBIOSPayload(ts *cpi.TailscaleSpec, cf *cpi.CloudflareSpec) SMBIOSPayload {
	return BastionSMBIOSPayload(ts, cf, nil, nil)
}

// TailscaleSpecToSMBIOSPayload is retained for callers with no cloudflare spec.
// It delegates to BastionSpecToSMBIOSPayload with a nil cloudflare spec.
func TailscaleSpecToSMBIOSPayload(ts *cpi.TailscaleSpec) SMBIOSPayload {
	return BastionSpecToSMBIOSPayload(ts, nil)
}

// BuildSMBIOSConfigValue renders the payload as the `smbios1` config-value
// string PVE expects, with each field base64-encoded. Returns "" when the
// payload is empty so the caller can short-circuit the PUT.
//
// Example output:
//
//	base64=1,serial=dHNrZXktYXV0aC1hYmM=,sku=eyJ2IjoxfQ==,family=b2NmcC1iYXN0aW9u
func BuildSMBIOSConfigValue(p SMBIOSPayload) string {
	if p.IsEmpty() {
		return ""
	}

	return renderSMBIOS(p.Serial, p.SKU, p.Family)
}

// renderSMBIOS renders the three fields as PVE's smbios1 config value.
func renderSMBIOS(serial, sku, family string) string {
	parts := []string{"base64=1"}

	for _, f := range []struct{ key, value string }{
		{"serial", serial},
		{"sku", sku},
		{"family", family},
	} {
		if f.value != "" {
			parts = append(parts, f.key+"="+base64.StdEncoding.EncodeToString([]byte(f.value)))
		}
	}

	return strings.Join(parts, ",")
}
