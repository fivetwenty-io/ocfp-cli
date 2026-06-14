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

// BastionSpecToSMBIOSPayload builds the SMBIOS payload from the bastion's
// tailscale + cloudflare specs. Serial carries the tailscale auth key; SKU is
// a JSON blob the firstboot script jq-parses, now including a "cloudflare"
// object with the connector token.
func BastionSpecToSMBIOSPayload(ts *cpi.TailscaleSpec, cf *cpi.CloudflareSpec) SMBIOSPayload {
	if ts == nil || ts.AuthKey == "" {
		return SMBIOSPayload{}
	}

	// Marshal the role config as compact JSON; the firstboot script
	// `jq`-parses fields with sane defaults so we can add keys later
	// without breaking older templates.
	skuMap := map[string]interface{}{
		"v":                1,
		"hostname":         ts.Hostname,
		"tags":             ts.Tags,
		"accept_dns":       ts.AcceptDNS,
		"accept_routes":    ts.AcceptRoutes,
		"ssh":              ts.SSH,
		"exit_node":        ts.ExitNode,
		"advertise_routes": ts.AdvertiseRoutes,
	}
	if cf != nil && cf.TunnelToken != "" {
		skuMap["cloudflare"] = map[string]interface{}{"token": cf.TunnelToken}
	}

	sku, err := json.Marshal(skuMap)
	if err != nil {
		// Marshalling fixed-shape data shouldn't realistically fail; if it
		// does, fall back to a minimal payload so the bastion at least has
		// the auth key and can join the tailnet manually.
		return SMBIOSPayload{Serial: ts.AuthKey, Family: smbiosFamilyBastion}
	}

	return SMBIOSPayload{
		Serial: ts.AuthKey,
		SKU:    string(sku),
		Family: smbiosFamilyBastion,
	}
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

	parts := []string{"base64=1"}

	if p.Serial != "" {
		parts = append(parts, "serial="+base64.StdEncoding.EncodeToString([]byte(p.Serial)))
	}

	if p.SKU != "" {
		parts = append(parts, "sku="+base64.StdEncoding.EncodeToString([]byte(p.SKU)))
	}

	if p.Family != "" {
		parts = append(parts, "family="+base64.StdEncoding.EncodeToString([]byte(p.Family)))
	}

	return strings.Join(parts, ",")
}
