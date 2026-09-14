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
		{"ubuntu-noble-bastion-template", "cloud-images.ubuntu.com"},
		{"ubuntu-resolute-template", "cloud-images.ubuntu.com"},
		{"ubuntu-resolute-bastion-template", "cloud-images.ubuntu.com"},
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

// TestCatalog_SharedSourceFilenameImpliesSharedURL is the trap test. Two specs
// may deliberately share a stored filename so the second costs no download —
// that is how the Noble bastion template reuses the vanilla template's image.
// But downloadTemplateImage skips the fetch whenever a file of that name is
// already on the import storage, so a spec that borrows another release's
// filename would silently build from the wrong image, with no error anywhere.
func TestCatalog_SharedSourceFilenameImpliesSharedURL(t *testing.T) {
	t.Parallel()

	byFilename := make(map[string]TemplateSpec, len(templateCatalog))

	for _, spec := range templateCatalog {
		prev, seen := byFilename[spec.SourceFilename]
		if seen && prev.SourceURL != spec.SourceURL {
			t.Errorf("templates %q and %q share SourceFilename %q but have different SourceURLs (%q vs %q): "+
				"downloadTemplateImage would skip the second download and build from the first image",
				prev.Name, spec.Name, spec.SourceFilename, prev.SourceURL, spec.SourceURL)
		}

		byFilename[spec.SourceFilename] = spec
	}
}

// TestCatalog_BastionVariantsRequireSeedUnits pins which catalog entries boot
// and seed during provisioning. Only the bastion variants do; the vanilla
// templates that artifacts and jumpbox clone from must not, because seeding
// costs several minutes over a serial console for units they never run.
func TestCatalog_BastionVariantsRequireSeedUnits(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		"ubuntu-noble-template":            false,
		"ubuntu-noble-bastion-template":    true,
		"ubuntu-resolute-template":         false,
		"ubuntu-resolute-bastion-template": true,
	}

	for name, wantSeed := range want {
		spec, ok := LookupCatalogSpec(name)
		if !ok {
			t.Errorf("LookupCatalogSpec(%q) returned ok=false", name)

			continue
		}

		if spec.RequireBastionUnits != wantSeed {
			t.Errorf("%s RequireBastionUnits = %v, want %v", name, spec.RequireBastionUnits, wantSeed)
		}
	}
}

// TestCatalog_ResoluteEntriesUseTheirOwnImage guards the specific mistake the
// trap test generalises: a Resolute entry that kept the Noble stored filename.
func TestCatalog_ResoluteEntriesUseTheirOwnImage(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"ubuntu-resolute-template", "ubuntu-resolute-bastion-template"} {
		spec, ok := LookupCatalogSpec(name)
		if !ok {
			t.Fatalf("LookupCatalogSpec(%q) returned ok=false", name)
		}

		if !strings.Contains(spec.SourceURL, "resolute") {
			t.Errorf("%s SourceURL = %q, want a resolute image", name, spec.SourceURL)
		}

		if !strings.Contains(spec.SourceFilename, "resolute") {
			t.Errorf("%s SourceFilename = %q, want a resolute-specific filename", name, spec.SourceFilename)
		}
	}
}

// TestPlanTemplateBuild_NeverDestroysUnlessAsked pins the safety property of
// the rebuild path.
//
// A bastion template takes a large download and a console-driven seed to
// build, and every bastion in the bloc is cloned from it. Destroying one the
// operator did not ask to have destroyed is expensive and surprising, so the
// default for a template that already exists stays "leave it alone".
func TestPlanTemplateBuild_NeverDestroysUnlessAsked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		exists      bool
		rebuild     bool
		wantDestroy bool
		wantBuild   bool
	}{
		{"absent, no rebuild asked", false, false, false, true},
		{"absent, rebuild asked", false, true, false, true},
		{"present, no rebuild asked", true, false, false, false},
		{"present, rebuild asked", true, true, true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := planTemplateBuild(tc.exists, tc.rebuild)

			if got.Destroy != tc.wantDestroy {
				t.Errorf("Destroy = %v, want %v", got.Destroy, tc.wantDestroy)
			}

			if got.Build != tc.wantBuild {
				t.Errorf("Build = %v, want %v", got.Build, tc.wantBuild)
			}
		})
	}
}
