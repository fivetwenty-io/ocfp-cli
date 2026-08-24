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
