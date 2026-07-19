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
	ing := &cpi.IngressSpec{OriginIP: "10.108.20.13", Ports: []int{80, 443}}

	p := BastionSMBIOSPayload(ts, nil, ing)

	var sku map[string]any
	if err := json.Unmarshal([]byte(p.SKU), &sku); err != nil {
		t.Fatalf("sku not json: %v", err)
	}

	ingress, ok := sku["ingress"].(map[string]any)
	if !ok {
		t.Fatalf("sku missing ingress object: %s", p.SKU)
	}
	if ingress["origin_ip"] != "10.108.20.13" {
		t.Errorf("origin_ip = %v", ingress["origin_ip"])
	}
}

func TestBastionSMBIOSPayload_NilIngressOmitsKey(t *testing.T) {
	ts := &cpi.TailscaleSpec{AuthKey: "tskey-abc"}

	p := BastionSMBIOSPayload(ts, nil, nil)

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
