package artifacts

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// resultFromResource is the only pure function in lookup.go.
// Lookup itself requires state.Manager + cpi.Provider — not unit-testable here.

func TestResultFromResource_AllFieldsPresent(t *testing.T) {
	t.Parallel()

	r := &state.Resource{
		Name: "mybloc-artifacts",
		Properties: map[string]interface{}{
			"vm_id":          "vm-001",
			"private_ip":     "10.0.0.42",
			"endpoint":       "https://10.0.0.42:9000",
			"access_key":     "deadbeef",
			"secret_key":     "cafebabe",
			"tls_mode":       "self-signed",
			"zfs_dataset":    "tank/rustfs",
			"data_volume_id": "vol-99",
			"ca_cert":        "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n",
		},
	}

	got := resultFromResource(r)

	if got.VMID != "vm-001" {
		t.Errorf("VMID = %q, want vm-001", got.VMID)
	}

	if got.Name != "mybloc-artifacts" {
		t.Errorf("Name = %q, want mybloc-artifacts", got.Name)
	}

	if got.PrivateIP != "10.0.0.42" {
		t.Errorf("PrivateIP = %q, want 10.0.0.42", got.PrivateIP)
	}

	if got.Endpoint != "https://10.0.0.42:9000" {
		t.Errorf("Endpoint = %q, want https://10.0.0.42:9000", got.Endpoint)
	}

	if got.AccessKey != "deadbeef" {
		t.Errorf("AccessKey = %q, want deadbeef", got.AccessKey)
	}

	if got.SecretKey != "cafebabe" {
		t.Errorf("SecretKey = %q, want cafebabe", got.SecretKey)
	}

	if got.TLSMode != "self-signed" {
		t.Errorf("TLSMode = %q, want self-signed", got.TLSMode)
	}

	if got.ZFSDataset != "tank/rustfs" {
		t.Errorf("ZFSDataset = %q, want tank/rustfs", got.ZFSDataset)
	}

	if got.DataVolumeID != "vol-99" {
		t.Errorf("DataVolumeID = %q, want vol-99", got.DataVolumeID)
	}

	if got.CACert == "" {
		t.Error("CACert empty, want non-empty")
	}
}

func TestResultFromResource_EmptyProperties(t *testing.T) {
	t.Parallel()

	r := &state.Resource{
		Name:       "empty-artifacts",
		Properties: map[string]interface{}{},
	}

	got := resultFromResource(r)

	if got.Name != "empty-artifacts" {
		t.Errorf("Name = %q, want empty-artifacts", got.Name)
	}

	// All property-derived fields default to empty string.
	for field, val := range map[string]string{
		"VMID":         got.VMID,
		"PrivateIP":    got.PrivateIP,
		"Endpoint":     got.Endpoint,
		"AccessKey":    got.AccessKey,
		"SecretKey":    got.SecretKey,
		"TLSMode":      got.TLSMode,
		"ZFSDataset":   got.ZFSDataset,
		"DataVolumeID": got.DataVolumeID,
		"CACert":       got.CACert,
	} {
		if val != "" {
			t.Errorf("%s = %q, want empty string", field, val)
		}
	}
}

func TestResultFromResource_NilProperties(t *testing.T) {
	t.Parallel()

	r := &state.Resource{
		Name:       "nil-props",
		Properties: nil,
	}

	// Must not panic.
	got := resultFromResource(r)

	if got.Name != "nil-props" {
		t.Errorf("Name = %q, want nil-props", got.Name)
	}

	if got.VMID != "" {
		t.Errorf("VMID = %q, want empty string", got.VMID)
	}
}

func TestResultFromResource_WrongPropertyTypes(t *testing.T) {
	t.Parallel()

	// Non-string property values must degrade to empty string, not panic.
	r := &state.Resource{
		Name: "typed-wrong",
		Properties: map[string]interface{}{
			"vm_id":          42,
			"private_ip":     true,
			"endpoint":       []string{"not-a-string"},
			"access_key":     nil,
			"secret_key":     3.14,
			"tls_mode":       struct{}{},
			"zfs_dataset":    map[string]string{},
			"data_volume_id": 0,
			"ca_cert":        false,
		},
	}

	got := resultFromResource(r)

	for field, val := range map[string]string{
		"VMID":         got.VMID,
		"PrivateIP":    got.PrivateIP,
		"Endpoint":     got.Endpoint,
		"AccessKey":    got.AccessKey,
		"SecretKey":    got.SecretKey,
		"TLSMode":      got.TLSMode,
		"ZFSDataset":   got.ZFSDataset,
		"DataVolumeID": got.DataVolumeID,
		"CACert":       got.CACert,
	} {
		if val != "" {
			t.Errorf("%s = %q, want empty string for wrong type", field, val)
		}
	}
}

func TestResultFromResource_MixedTypes(t *testing.T) {
	t.Parallel()

	// Some fields valid strings, some wrong types.
	r := &state.Resource{
		Name: "mixed",
		Properties: map[string]interface{}{
			"vm_id":      "vm-good",
			"private_ip": 999, // wrong type
			"endpoint":   "https://10.0.0.1:9000",
		},
	}

	got := resultFromResource(r)

	if got.VMID != "vm-good" {
		t.Errorf("VMID = %q, want vm-good", got.VMID)
	}

	if got.PrivateIP != "" {
		t.Errorf("PrivateIP = %q, want empty string for int property", got.PrivateIP)
	}

	if got.Endpoint != "https://10.0.0.1:9000" {
		t.Errorf("Endpoint = %q, want https://10.0.0.1:9000", got.Endpoint)
	}
}

func TestResultFromResource_NameFromResource(t *testing.T) {
	t.Parallel()

	// Name comes from r.Name, not Properties — verify that distinction.
	r := &state.Resource{
		Name: "resource-name",
		Properties: map[string]interface{}{
			"name": "property-name", // different key — not read by resultFromResource
		},
	}

	got := resultFromResource(r)

	if got.Name != "resource-name" {
		t.Errorf("Name = %q, want resource-name (from r.Name, not Properties)", got.Name)
	}
}
