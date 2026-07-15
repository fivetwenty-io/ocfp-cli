package artifacts

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// resultFromResource is the only pure function in lookup.go.
// Lookup itself requires state.Manager + cpi.Provider — not unit-testable here.

func TestResultFromResource_AllFieldsPresent(t *testing.T) {
	t.Parallel()

	r := &state.Resource{
		Name: "mybloc-artifacts",
		Properties: map[string]interface{}{
			"vm_id":                  "vm-001",
			"private_ip":             "10.0.0.42",
			"endpoint":               "https://10.0.0.42:9000",
			"access_key":             "deadbeef",
			"secret_key":             "cafebabe",
			"tls_mode":               "self-signed",
			"zfs_dataset":            "tank/rustfs",
			"data_volume_id":         "vol-99",
			"ca_cert":                "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n",
			"tls_fingerprint_sha256": "deadbeef",
			"tls_leaf_not_after":     "2027-01-01T00:00:00Z",
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

	if got.TLSFingerprintSHA256 != "deadbeef" {
		t.Errorf("TLSFingerprintSHA256 = %q, want deadbeef", got.TLSFingerprintSHA256)
	}

	if got.TLSLeafNotAfter != "2027-01-01T00:00:00Z" {
		t.Errorf("TLSLeafNotAfter = %q, want 2027-01-01T00:00:00Z", got.TLSLeafNotAfter)
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
		"VMID":                 got.VMID,
		"PrivateIP":            got.PrivateIP,
		"Endpoint":             got.Endpoint,
		"AccessKey":            got.AccessKey,
		"SecretKey":            got.SecretKey,
		"TLSMode":              got.TLSMode,
		"ZFSDataset":           got.ZFSDataset,
		"DataVolumeID":         got.DataVolumeID,
		"CACert":               got.CACert,
		"TLSFingerprintSHA256": got.TLSFingerprintSHA256,
		"TLSLeafNotAfter":      got.TLSLeafNotAfter,
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
			"vm_id":                  42,
			"private_ip":             true,
			"endpoint":               []string{"not-a-string"},
			"access_key":             nil,
			"secret_key":             3.14,
			"tls_mode":               struct{}{},
			"zfs_dataset":            map[string]string{},
			"data_volume_id":         0,
			"ca_cert":                false,
			"tls_fingerprint_sha256": 12345,
			"tls_leaf_not_after":     []byte("not-a-string"),
		},
	}

	got := resultFromResource(r)

	for field, val := range map[string]string{
		"VMID":                 got.VMID,
		"PrivateIP":            got.PrivateIP,
		"Endpoint":             got.Endpoint,
		"AccessKey":            got.AccessKey,
		"SecretKey":            got.SecretKey,
		"TLSMode":              got.TLSMode,
		"ZFSDataset":           got.ZFSDataset,
		"DataVolumeID":         got.DataVolumeID,
		"CACert":               got.CACert,
		"TLSFingerprintSHA256": got.TLSFingerprintSHA256,
		"TLSLeafNotAfter":      got.TLSLeafNotAfter,
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

func TestEndpointForLookup(t *testing.T) {
	t.Parallel()

	const bloc = "mybloc"
	const httpsURL = "https://10.0.0.42:9000"
	const httpURL = "http://10.0.0.42:9000"
	const fakeCACert = "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"

	tests := []struct {
		name          string
		endpointURL   string
		tlsMode       string
		caCert        string
		allowInsecure bool

		wantErr     error
		wantURL     string
		wantCACert  string
		wantSkipTLS bool
	}{
		{
			name:        "https internal-ca with CA cert pins to CA",
			endpointURL: httpsURL,
			tlsMode:     config.ArtifactsTLSModeInternalCA,
			caCert:      fakeCACert,
			wantURL:     httpsURL,
			wantCACert:  fakeCACert,
		},
		{
			name:        "https internal-ca no CA cert errors, never skip-verifies",
			endpointURL: httpsURL,
			tlsMode:     config.ArtifactsTLSModeInternalCA,
			caCert:      "",
			wantErr:     ErrArtifactsCAMissing,
		},
		{
			name:          "https self-signed no CA cert without opt-in errors",
			endpointURL:   httpsURL,
			tlsMode:       config.ArtifactsTLSModeSelfSigned,
			caCert:        "",
			allowInsecure: false,
			wantErr:       ErrArtifactsInsecureOptInRequired,
		},
		{
			name:          "https self-signed no CA cert with explicit opt-in skips verify",
			endpointURL:   httpsURL,
			tlsMode:       config.ArtifactsTLSModeSelfSigned,
			caCert:        "",
			allowInsecure: true,
			wantURL:       httpsURL,
			wantSkipTLS:   true,
		},
		{
			name:        "https self-signed with CA cert pins to CA regardless of allowInsecure",
			endpointURL: httpsURL,
			tlsMode:     config.ArtifactsTLSModeSelfSigned,
			caCert:      fakeCACert,
			wantURL:     httpsURL,
			wantCACert:  fakeCACert,
		},
		{
			name:        "https disabled mode with no CA cert is a state inconsistency error",
			endpointURL: httpsURL,
			tlsMode:     config.ArtifactsTLSModeDisabled,
			caCert:      "",
			wantErr:     ErrArtifactsCAMissing,
		},
		{
			name:        "https empty mode with no CA cert is a state inconsistency error",
			endpointURL: httpsURL,
			tlsMode:     "",
			caCert:      "",
			wantErr:     ErrArtifactsCAMissing,
		},
		{
			name:        "https unknown mode with no CA cert is a state inconsistency error",
			endpointURL: httpsURL,
			tlsMode:     "bogus-mode",
			caCert:      "",
			wantErr:     ErrArtifactsCAMissing,
		},
		{
			name:        "http disabled mode needs no TLS material",
			endpointURL: httpURL,
			tlsMode:     config.ArtifactsTLSModeDisabled,
			caCert:      "",
			wantURL:     httpURL,
		},
		{
			name:        "http internal-ca mode still needs no TLS material (scheme wins)",
			endpointURL: httpURL,
			tlsMode:     config.ArtifactsTLSModeInternalCA,
			caCert:      "",
			wantURL:     httpURL,
		},
		{
			name:        "http endpoint with CA cert still pins (caller-supplied trust honored)",
			endpointURL: httpURL,
			tlsMode:     config.ArtifactsTLSModeDisabled,
			caCert:      fakeCACert,
			wantURL:     httpURL,
			wantCACert:  fakeCACert,
		},
		{
			name:        "empty endpoint URL is invalid",
			endpointURL: "",
			tlsMode:     config.ArtifactsTLSModeInternalCA,
			wantErr:     ErrArtifactsEndpointInvalid,
		},
		{
			name:        "endpoint URL without http(s) scheme is invalid",
			endpointURL: "ftp://10.0.0.42:9000",
			tlsMode:     config.ArtifactsTLSModeInternalCA,
			wantErr:     ErrArtifactsEndpointInvalid,
		},
	}

	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := EndpointForLookup(bloc, tt.endpointURL, tt.tlsMode, tt.caCert, tt.allowInsecure)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want wrapping %v", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", got.URL, tt.wantURL)
			}

			if got.CACert != tt.wantCACert {
				t.Errorf("CACert = %q, want %q", got.CACert, tt.wantCACert)
			}

			if got.SkipTLSVerify != tt.wantSkipTLS {
				t.Errorf("SkipTLSVerify = %v, want %v", got.SkipTLSVerify, tt.wantSkipTLS)
			}

			if !got.PathStyle {
				t.Error("PathStyle = false, want true")
			}

			if got.Region == "" {
				t.Error("Region empty, want default region set")
			}
		})
	}
}

func TestEndpointForLookup_ErrorMessagesNameBloc(t *testing.T) {
	t.Parallel()

	// Actionable errors must name the bloc so operators running against
	// multiple blocs can tell which one needs attention.
	_, err := EndpointForLookup("ocfp-lab-wayne", "https://10.0.0.1:9000", config.ArtifactsTLSModeInternalCA, "", false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	got := err.Error()
	for _, want := range []string{"ocfp-lab-wayne", "artifacts ca", "artifacts provision"} {
		if !strings.Contains(got, want) {
			t.Errorf("error message %q missing expected substring %q", got, want)
		}
	}
}
