package pve

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

func TestTailscaleSpecToSMBIOSPayload_NilEmpty(t *testing.T) {
	t.Parallel()

	if got := TailscaleSpecToSMBIOSPayload(nil); !got.IsEmpty() {
		t.Errorf("nil spec → expected empty payload, got %+v", got)
	}

	if got := TailscaleSpecToSMBIOSPayload(&cpi.TailscaleSpec{}); !got.IsEmpty() {
		t.Errorf("spec with no AuthKey → expected empty payload, got %+v", got)
	}
}

func TestTailscaleSpecToSMBIOSPayload_FullSpec(t *testing.T) {
	t.Parallel()

	spec := &cpi.TailscaleSpec{
		AuthKey:         "tskey-abc",
		Hostname:        "ocfp-wayne-bastion",
		Tags:            []string{"tag:ocfp-bastion"},
		AcceptDNS:       false,
		AcceptRoutes:    false,
		SSH:             true,
		ExitNode:        "",
		AdvertiseRoutes: "10.64.64.0/18",
	}

	got := TailscaleSpecToSMBIOSPayload(spec)

	if got.Serial != "tskey-abc" {
		t.Errorf("Serial = %q, want %q", got.Serial, "tskey-abc")
	}

	if got.Family != smbiosFamilyBastion {
		t.Errorf("Family = %q, want %q", got.Family, smbiosFamilyBastion)
	}

	var sku map[string]interface{}
	if err := json.Unmarshal([]byte(got.SKU), &sku); err != nil {
		t.Fatalf("SKU not valid JSON: %v\n%s", err, got.SKU)
	}

	if sku["hostname"] != "ocfp-wayne-bastion" {
		t.Errorf("sku hostname = %v, want ocfp-wayne-bastion", sku["hostname"])
	}

	if sku["ssh"] != true {
		t.Errorf("sku ssh = %v, want true", sku["ssh"])
	}

	if sku["advertise_routes"] != "10.64.64.0/18" {
		t.Errorf("sku advertise_routes = %v, want 10.64.64.0/18", sku["advertise_routes"])
	}
}

func TestSMBIOSPayload_IsEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		p    SMBIOSPayload
		want bool
	}{
		{"zero value", SMBIOSPayload{}, true},
		{"serial only", SMBIOSPayload{Serial: "x"}, false},
		{"sku only", SMBIOSPayload{SKU: "x"}, false},
		{"family only", SMBIOSPayload{Family: "x"}, false},
		{"all set", SMBIOSPayload{Serial: "a", SKU: "b", Family: "c"}, false},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.p.IsEmpty(); got != tc.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildSMBIOSConfigValue_EmptyReturnsEmpty(t *testing.T) {
	t.Parallel()

	if got := BuildSMBIOSConfigValue(SMBIOSPayload{}); got != "" {
		t.Errorf("empty payload should return empty string, got %q", got)
	}
}

func TestBuildSMBIOSConfigValue_FormatAndBase64(t *testing.T) {
	t.Parallel()

	payload := SMBIOSPayload{
		Serial: "tskey-auth-abc",
		SKU:    `{"v":1}`,
		Family: "ocfp-bastion",
	}

	got := BuildSMBIOSConfigValue(payload)

	if !strings.HasPrefix(got, "base64=1,") {
		t.Errorf("output must start with base64=1, got %q", got)
	}

	parts := strings.Split(got, ",")
	want := map[string]string{
		"serial": base64.StdEncoding.EncodeToString([]byte(payload.Serial)),
		"sku":    base64.StdEncoding.EncodeToString([]byte(payload.SKU)),
		"family": base64.StdEncoding.EncodeToString([]byte(payload.Family)),
	}

	gotFields := map[string]string{}

	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			t.Errorf("malformed field %q", p)
			continue
		}

		gotFields[k] = v
	}

	for k, expected := range want {
		if gotFields[k] != expected {
			t.Errorf("field %s = %q, want %q", k, gotFields[k], expected)
		}
	}
}

func TestBuildSMBIOSConfigValue_OmitsEmptyFields(t *testing.T) {
	t.Parallel()

	got := BuildSMBIOSConfigValue(SMBIOSPayload{Serial: "x", Family: "y"})

	if strings.Contains(got, "sku=") {
		t.Errorf("expected no sku= field for empty SKU, got %q", got)
	}

	if !strings.Contains(got, "serial=") {
		t.Errorf("expected serial= field, got %q", got)
	}

	if !strings.Contains(got, "family=") {
		t.Errorf("expected family= field, got %q", got)
	}
}

func TestBastionSMBIOSPayload_IncludesCloudflareToken(t *testing.T) {
	ts := &cpi.TailscaleSpec{AuthKey: "tskey-1", Hostname: "ocfp-lab-wayne-bastion"}
	cf := &cpi.CloudflareSpec{TunnelToken: "cf-conn-token"}
	p := BastionSpecToSMBIOSPayload(ts, cf)
	if p.Serial != "tskey-1" {
		t.Fatalf("serial = %q, want tskey-1", p.Serial)
	}
	if !strings.Contains(p.SKU, `"cloudflare"`) || !strings.Contains(p.SKU, "cf-conn-token") {
		t.Fatalf("sku missing cloudflare token: %s", p.SKU)
	}
}

func TestBastionSMBIOSPayload_IncludesIngress(t *testing.T) {
	ts := &cpi.TailscaleSpec{AuthKey: "tskey-abc", Hostname: "b1"}
	ing := &cpi.IngressSpec{OriginIP: "10.108.20.97", Ports: []int{80, 443}}

	p := BastionSMBIOSPayload(ts, nil, ing, nil)

	var sku map[string]any
	if err := json.Unmarshal([]byte(p.SKU), &sku); err != nil {
		t.Fatalf("sku not json: %v", err)
	}

	ingress, ok := sku["ingress"].(map[string]any)
	if !ok {
		t.Fatalf("sku missing ingress object: %s", p.SKU)
	}
	if ingress["origin_ip"] != "10.108.20.97" {
		t.Errorf("origin_ip = %v", ingress["origin_ip"])
	}
}

func TestBastionSMBIOSPayload_NilIngressOmitsKey(t *testing.T) {
	ts := &cpi.TailscaleSpec{AuthKey: "tskey-abc"}

	p := BastionSMBIOSPayload(ts, nil, nil, nil)

	var sku map[string]any
	if err := json.Unmarshal([]byte(p.SKU), &sku); err != nil {
		t.Fatalf("sku not json: %v", err)
	}
	if _, present := sku["ingress"]; present {
		t.Errorf("ingress key must be absent when spec nil")
	}
}

func TestBuildSMBIOSConfigValue_RealisticPayloadFitsSlot(t *testing.T) {
	t.Parallel()

	// The verified-on-lab payload — 201-char JSON → 268-char base64.
	// SMBIOS spec suggests 255-char per-string limit but QEMU/PVE accept longer.
	// This test pins the format so we notice if the field grows unbounded.
	sku := `{"v":1,"hostname":"ocfp-wayne-bastion","tags":["tag:ocfp-bastion"],"accept_dns":false,"accept_routes":false,"ssh":true,"exit_node":"","advertise_routes":"10.64.64.0/18","watchdog_interval_seconds":300}`

	got := BuildSMBIOSConfigValue(SMBIOSPayload{
		Serial: "tskey-auth-kMHx3KZHMj11CNTRL-LhPViYjthRNtZvCPHFX9RNcH2pnMX3DqW",
		SKU:    sku,
		Family: "ocfp-bastion",
	})

	for _, slot := range strings.Split(got, ",")[1:] {
		_, v, _ := strings.Cut(slot, "=")
		if len(v) > 400 {
			t.Errorf("slot value %d chars exceeds safe ceiling 400 — consider splitting", len(v))
		}
	}
}

// TestBastionSMBIOSPayload_CarriesDataDisk asserts the data-disk block reaches
// the guest. The boot-time dataset script reads its whole configuration from
// here, because PVE 9.x cannot deliver cloud-init snippets to per-VM clones.
func TestBastionSMBIOSPayload_CarriesDataDisk(t *testing.T) {
	t.Parallel()

	ts := &cpi.TailscaleSpec{AuthKey: "tskey-auth-abc", Hostname: "bloc-bastion"}
	data := &cpi.DataDiskSpec{
		Serial:        "ocfpdata",
		Mountpoint:    "/data",
		Filesystem:    "ext4",
		HomeDir:       "/home/ubuntu",
		User:          "ubuntu",
		AuthorizedKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAFAKE bloc-keypair",
	}

	payload := BastionSMBIOSPayload(ts, nil, nil, data)

	var sku map[string]interface{}
	if err := json.Unmarshal([]byte(payload.SKU), &sku); err != nil {
		t.Fatalf("SKU is not valid JSON: %v", err)
	}

	raw, ok := sku["data"]
	if !ok {
		t.Fatalf("SKU carries no data block: %s", payload.SKU)
	}

	block, ok := raw.(map[string]interface{})
	if !ok {
		t.Fatalf("data block is not an object: %v", raw)
	}

	for key, want := range map[string]string{
		"serial":         "ocfpdata",
		"mountpoint":     "/data",
		"filesystem":     "ext4",
		"home":           "/home/ubuntu",
		"user":           "ubuntu",
		"authorized_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAFAKE bloc-keypair",
	} {
		if got := block[key]; got != want {
			t.Errorf("data.%s = %v, want %q", key, got, want)
		}
	}
}

// TestBastionSMBIOSPayload_OmitsDataWhenAbsent asserts a bloc with no data
// disk gets no data block, so the guest script no-ops rather than guessing.
func TestBastionSMBIOSPayload_OmitsDataWhenAbsent(t *testing.T) {
	t.Parallel()

	ts := &cpi.TailscaleSpec{AuthKey: "tskey-auth-abc"}

	payload := BastionSMBIOSPayload(ts, nil, nil, nil)

	var sku map[string]interface{}
	if err := json.Unmarshal([]byte(payload.SKU), &sku); err != nil {
		t.Fatalf("SKU is not valid JSON: %v", err)
	}

	if _, present := sku["data"]; present {
		t.Errorf("SKU carries a data block when none was configured: %s", payload.SKU)
	}
}

// TestBastionSMBIOSPayload_DataWithoutTailscale asserts the data disk is
// delivered even when tailscale is not configured. The payload used to be
// gated entirely on a tailscale auth key, which would have left a
// tailscale-less bastion with an unprepared disk.
func TestBastionSMBIOSPayload_DataWithoutTailscale(t *testing.T) {
	t.Parallel()

	data := &cpi.DataDiskSpec{Serial: "ocfpdata", Mountpoint: "/data", Filesystem: "ext4"}

	payload := BastionSMBIOSPayload(nil, nil, nil, data)

	if payload.IsEmpty() {
		t.Fatal("payload is empty; a data disk must be delivered without tailscale")
	}

	if payload.Family != smbiosFamilyBastion {
		t.Errorf("Family = %q, want the bastion discriminator", payload.Family)
	}

	var sku map[string]interface{}
	if err := json.Unmarshal([]byte(payload.SKU), &sku); err != nil {
		t.Fatalf("SKU is not valid JSON: %v", err)
	}

	if _, present := sku["data"]; !present {
		t.Errorf("SKU carries no data block: %s", payload.SKU)
	}
}

// TestBuildSMBIOSConfigValue_StaysUnderPVELimit pins the budget that broke the
// first Resolute bastion build.
//
// PVE caps the smbios1 config string at 512 characters, and every field in it
// is base64, so the encoding costs a third again on top of the JSON. A bastion
// carrying a tailscale block, an ingress block, and a data block with the
// bootstrap public key in it rendered well past the cap, and the VM came up
// with no SKU at all: no tailscale, and an unprepared data disk.
func TestBuildSMBIOSConfigValue_StaysUnderPVELimit(t *testing.T) {
	t.Parallel()

	ts := &cpi.TailscaleSpec{
		AuthKey:         "tskey-auth-kH8vQ2mXnR4pL9wYtZ1bC7dF3gJ6sA0eU5iO",
		Hostname:        "ocfp-lab-wayneeseguin-bastion",
		Tags:            []string{"tag:ocfp-bastion"},
		AcceptDNS:       true,
		AcceptRoutes:    true,
		SSH:             true,
		AdvertiseRoutes: "10.108.16.0/20",
	}

	ing := &cpi.IngressSpec{OriginIP: "10.108.20.97", Ports: []int{80, 443}}

	data := &cpi.DataDiskSpec{
		Serial:     "ocfpdata",
		Mountpoint: "/data",
		Filesystem: "ext4",
		HomeDir:    "/home/ubuntu",
		User:       "ubuntu",
		AuthorizedKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB7kQ2mXnR4pL9wYtZ1bC7dF3gJ6sA0eU5iOxV8nMpQr " +
			"ocfp-lab-wayneeseguin",
	}

	value := BuildSMBIOSConfigValue(BastionSMBIOSPayload(ts, nil, ing, data))

	if len(value) > smbiosMaxLength {
		t.Errorf("smbios1 renders to %d characters, over PVE's limit of %d:\n%s",
			len(value), smbiosMaxLength, value)
	}

	// Dropping fields to fit must never drop the one the guest cannot work
	// without. Without the serial the dataset script refuses to guess a
	// device, so the data disk goes unmounted.
	if !strings.Contains(value, "sku=") {
		t.Fatal("the payload lost its SKU entirely")
	}

	decoded := decodeSKU(t, value)
	if !strings.Contains(decoded, `"serial":"ocfpdata"`) {
		t.Errorf("the data block lost the disk serial:\n%s", decoded)
	}

	if !strings.Contains(decoded, "ocfp-lab-wayneeseguin-bastion") {
		t.Errorf("the payload lost the tailscale hostname:\n%s", decoded)
	}
}

// TestBastionSMBIOSPayload_OmitsUnsetDataFields keeps the payload small by
// leaving out what the guest already defaults.
//
// The dataset script defaults the mountpoint, the filesystem, the home
// directory, and the user, so sending them costs budget and buys nothing.
func TestBastionSMBIOSPayload_OmitsUnsetDataFields(t *testing.T) {
	t.Parallel()

	p := BastionSMBIOSPayload(nil, nil, nil, &cpi.DataDiskSpec{Serial: "ocfpdata"})

	for _, unwanted := range []string{"mountpoint", "filesystem", "home", "user", "authorized_key"} {
		if strings.Contains(p.SKU, `"`+unwanted+`"`) {
			t.Errorf("the SKU carries an unset %q field: %s", unwanted, p.SKU)
		}
	}

	if !strings.Contains(p.SKU, `"serial":"ocfpdata"`) {
		t.Errorf("the SKU lost the disk serial: %s", p.SKU)
	}
}

// decodeSKU pulls the sku field back out of a rendered smbios1 value.
func decodeSKU(t *testing.T, value string) string {
	t.Helper()

	for _, part := range strings.Split(value, ",") {
		if !strings.HasPrefix(part, "sku=") {
			continue
		}

		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(part, "sku="))
		if err != nil {
			t.Fatalf("decode sku: %v", err)
		}

		return string(raw)
	}

	t.Fatal("no sku field in the rendered value")

	return ""
}
