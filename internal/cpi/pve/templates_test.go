package pve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/nodes"
)

func TestLookupCatalogSpec_KnownNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		wantSourceHost string
	}{
		{"ubuntu-noble-template", "cloud-images.ubuntu.com"},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spec, ok := LookupCatalogSpec(tc.name)
			if !ok {
				t.Fatalf("LookupCatalogSpec(%q) returned ok=false", tc.name)
			}

			if spec.Name != tc.name {
				t.Errorf("spec.Name = %q, want %q", spec.Name, tc.name)
			}

			if !strings.Contains(spec.SourceURL, tc.wantSourceHost) {
				t.Errorf("spec.SourceURL = %q, want it to reference %q", spec.SourceURL, tc.wantSourceHost)
			}

			// SourceFilename is independent of the URL filename: Ubuntu's .img
			// downloads are qcow2-formatted, so we store with a .qcow2
			// extension to satisfy PVE's "import" content-type validator.
			if !strings.HasSuffix(spec.SourceFilename, ".qcow2") {
				t.Errorf("spec.SourceFilename %q must end in .qcow2 for PVE import content type", spec.SourceFilename)
			}

			if spec.Memory <= 0 || spec.Cores <= 0 {
				t.Errorf("spec memory/cores invalid: memory=%d cores=%d", spec.Memory, spec.Cores)
			}
		})
	}
}

func TestLookupCatalogSpec_UnknownName(t *testing.T) {
	t.Parallel()

	_, ok := LookupCatalogSpec("not-a-real-template")
	if ok {
		t.Error("LookupCatalogSpec returned ok=true for unknown name")
	}
}

func TestParseUPID_StringForm(t *testing.T) {
	t.Parallel()

	raw := nodes.CreateStorageDownloadUrlResponse(json.RawMessage(`"UPID:pve:001234:00ABCD:6646FFFF:download:noble:root@pam:"`))

	upid, err := parseUPID(&raw)
	if err != nil {
		t.Fatalf("parseUPID: %v", err)
	}

	if !strings.HasPrefix(upid, "UPID:") {
		t.Errorf("upid = %q, want UPID: prefix", upid)
	}
}

func TestParseUPID_ObjectForm(t *testing.T) {
	t.Parallel()

	raw := nodes.CreateStorageDownloadUrlResponse(json.RawMessage(`{"upid":"UPID:pve:00:00:00:download::"}`))

	upid, err := parseUPID(&raw)
	if err != nil {
		t.Fatalf("parseUPID: %v", err)
	}

	if upid != "UPID:pve:00:00:00:download::" {
		t.Errorf("upid = %q", upid)
	}
}

func TestParseUPID_EmptyReturnsEmpty(t *testing.T) {
	t.Parallel()

	upid, err := parseUPID(nil)
	if err != nil {
		t.Fatalf("parseUPID(nil) err = %v, want nil", err)
	}

	if upid != "" {
		t.Errorf("upid = %q, want empty", upid)
	}
}

func TestParseUPID_UnrecognizedShapeErrors(t *testing.T) {
	t.Parallel()

	raw := nodes.CreateStorageDownloadUrlResponse(json.RawMessage(`12345`))

	_, err := parseUPID(&raw)
	if err == nil {
		t.Error("expected error for unrecognized UPID payload shape")
	}
}

// TestBuildTemplateSeedNetParams pins the DHCP-default backward-compatibility
// contract (a zero-value Config must reproduce exactly today's single-key
// ipconfig0 map) alongside the static-mode key set.
func TestBuildTemplateSeedNetParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *Config
		want map[string]interface{}
	}{
		{
			name: "dhcp default",
			cfg:  &Config{},
			want: map[string]interface{}{
				"ipconfig0": "ip=dhcp",
			},
		},
		{
			name: "static full",
			cfg: &Config{
				TemplateSeedIP:           "10.61.148.2/24",
				TemplateSeedGateway:      "10.61.148.1",
				TemplateSeedDNS:          []string{"10.97.160.160", "10.97.160.161"},
				TemplateSeedSearchDomain: "ldschurch.org",
			},
			want: map[string]interface{}{
				"ipconfig0":    "ip=10.61.148.2/24,gw=10.61.148.1",
				"nameserver":   "10.97.160.160 10.97.160.161",
				"searchdomain": "ldschurch.org",
			},
		},
		{
			name: "static no dns",
			cfg: &Config{
				TemplateSeedIP:      "10.61.148.2/24",
				TemplateSeedGateway: "10.61.148.1",
			},
			want: map[string]interface{}{
				"ipconfig0":  "ip=10.61.148.2/24,gw=10.61.148.1",
				"nameserver": defaultPVECloudInitDNS,
			},
		},
		{
			name: "static no searchdomain",
			cfg: &Config{
				TemplateSeedIP:      "10.61.148.2/24",
				TemplateSeedGateway: "10.61.148.1",
				TemplateSeedDNS:     []string{"10.97.160.160"},
			},
			want: map[string]interface{}{
				"ipconfig0":  "ip=10.61.148.2/24,gw=10.61.148.1",
				"nameserver": "10.97.160.160",
			},
		},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := buildTemplateSeedNetParams(tc.cfg)

			if len(got) != len(tc.want) {
				t.Fatalf("buildTemplateSeedNetParams(%+v) returned %d keys %v, want %d keys %v",
					tc.cfg, len(got), got, len(tc.want), tc.want)
			}

			for k, wantV := range tc.want {
				gotV, ok := got[k]
				if !ok {
					t.Errorf("missing key %q in %v", k, got)

					continue
				}

				if gotV != wantV {
					t.Errorf("key %q = %v, want %v", k, gotV, wantV)
				}
			}

			if _, ok := tc.want["searchdomain"]; !ok {
				if _, present := got["searchdomain"]; present {
					t.Errorf("searchdomain key must be absent, not empty-valued; got %v", got)
				}
			}
		})
	}
}
