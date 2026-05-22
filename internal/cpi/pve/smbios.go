package pve

import (
	"encoding/base64"
	"strings"
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
