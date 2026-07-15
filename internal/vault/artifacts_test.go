package vault

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func newTestCfg(mode string) *config.Config {
	cfg := &config.Config{}
	cfg.Artifacts.Defaults()
	cfg.Artifacts.TLS.Mode = mode

	return cfg
}

func TestArtifactsWriter_InternalCALabelAndCACertFromEndpoint(t *testing.T) {
	t.Parallel()

	safe := newFakeSafe()
	cfg := newTestCfg(config.ArtifactsTLSModeInternalCA)
	w := NewArtifactsWriter(cfg, safe, "pve-wayne")

	ep := artifacts.Endpoint{
		URL:       "https://10.0.0.11:9000",
		Host:      "10.0.0.11",
		Port:      9000,
		Region:    config.BlobstoreDefaultRegion,
		PathStyle: true,
		CACert:    "CA-CERT-PEM",
	}
	creds := artifacts.Credentials{AccessKey: "AK", SecretKey: "SK"}
	tls := &artifacts.TLSMaterial{CertPEM: "LEAF-CERT-PEM", Fingerprint: "fp"}

	err := w.WriteArtifacts(context.Background(), "pve-wayne", ep, creds, tls)
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}

	meta := safe.data["secret/ocfp/pve-wayne/artifacts"]
	if meta["tls_mode"] != config.ArtifactsTLSModeInternalCA {
		t.Errorf("tls_mode = %v, want internal-ca", meta["tls_mode"])
	}

	if meta["tls_fingerprint_sha256"] != "fp" {
		t.Errorf("tls_fingerprint_sha256 = %v, want fp", meta["tls_fingerprint_sha256"])
	}

	if _, present := meta["tls_leaf_not_after"]; present {
		t.Errorf("tls_leaf_not_after should be absent when TLSMaterial.NotAfter is empty, got %v", meta["tls_leaf_not_after"])
	}

	bosh := safe.data[w.PathBuilder.GetSystemBlobstorePath("mgmt", "bosh", "bosh")]
	if bosh["ca_cert"] != "CA-CERT-PEM" {
		t.Errorf("ca_cert = %v, want CA-CERT-PEM (from ep.CACert, NOT leaf cert)", bosh["ca_cert"])
	}

	if bosh["ca_cert"] == "LEAF-CERT-PEM" {
		t.Errorf("ca_cert pinned to LEAF-CERT-PEM — must be the bloc CA, not the leaf")
	}

	cfBucket := safe.data[w.PathBuilder.GetSystemBlobstorePath("ocf", "cf", "main")]
	if cfBucket["ca_cert"] != "CA-CERT-PEM" {
		t.Errorf("cf ca_cert = %v, want CA-CERT-PEM", cfBucket["ca_cert"])
	}
}

// TestArtifactsWriter_TLSLeafNotAfterIncludedWhenSet (task 6.2 gap 1) asserts
// WriteArtifacts persists tls_leaf_not_after into the artifacts vault meta
// when TLSMaterial.NotAfter is populated — closing the gap where
// docs/artifacts-tls.md claimed this field existed before it actually did.
func TestArtifactsWriter_TLSLeafNotAfterIncludedWhenSet(t *testing.T) {
	t.Parallel()

	safe := newFakeSafe()
	cfg := newTestCfg(config.ArtifactsTLSModeInternalCA)
	w := NewArtifactsWriter(cfg, safe, "pve-wayne")

	ep := artifacts.Endpoint{URL: "https://10.0.0.11:9000", CACert: "CA-CERT-PEM"}
	tls := &artifacts.TLSMaterial{CertPEM: "LEAF-CERT-PEM", Fingerprint: "fp", NotAfter: "2027-01-02T03:04:05Z"}

	err := w.WriteArtifacts(context.Background(), "pve-wayne", ep, artifacts.Credentials{}, tls)
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}

	meta := safe.data["secret/ocfp/pve-wayne/artifacts"]
	if meta["tls_leaf_not_after"] != "2027-01-02T03:04:05Z" {
		t.Errorf("tls_leaf_not_after = %v, want 2027-01-02T03:04:05Z", meta["tls_leaf_not_after"])
	}
}

// TestArtifactsWriter_TLSLeafNotAfterOmittedWhenEmpty asserts the key is
// never written with an empty value — e.g. a TLSMaterial recovered from a
// skip-path state resource that never recorded tls_leaf_not_after (state
// predates task 6.2, or the leaf issuance path failed to parse expiry).
func TestArtifactsWriter_TLSLeafNotAfterOmittedWhenEmpty(t *testing.T) {
	t.Parallel()

	safe := newFakeSafe()
	cfg := newTestCfg(config.ArtifactsTLSModeInternalCA)
	w := NewArtifactsWriter(cfg, safe, "pve-wayne")

	ep := artifacts.Endpoint{URL: "https://10.0.0.11:9000", CACert: "CA-CERT-PEM"}
	tls := &artifacts.TLSMaterial{Fingerprint: "fp"} // NotAfter deliberately empty

	err := w.WriteArtifacts(context.Background(), "pve-wayne", ep, artifacts.Credentials{}, tls)
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}

	meta := safe.data["secret/ocfp/pve-wayne/artifacts"]
	if _, present := meta["tls_leaf_not_after"]; present {
		t.Errorf("tls_leaf_not_after should be absent when NotAfter is empty, got %v", meta["tls_leaf_not_after"])
	}
}

func TestArtifactsWriter_SelfSignedLabel(t *testing.T) {
	t.Parallel()

	safe := newFakeSafe()
	cfg := newTestCfg(config.ArtifactsTLSModeSelfSigned)
	w := NewArtifactsWriter(cfg, safe, "pve-wayne")

	ep := artifacts.Endpoint{URL: "https://10.0.0.11:9000", CACert: "SELF-SIGNED-CERT"}
	tls := &artifacts.TLSMaterial{CertPEM: "SELF-SIGNED-CERT", Fingerprint: "fp"}

	err := w.WriteArtifacts(context.Background(), "pve-wayne", ep, artifacts.Credentials{}, tls)
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}

	meta := safe.data["secret/ocfp/pve-wayne/artifacts"]
	if meta["tls_mode"] != config.ArtifactsTLSModeSelfSigned {
		t.Errorf("tls_mode = %v, want self-signed", meta["tls_mode"])
	}
}

func TestArtifactsWriter_DisabledModeNoCACert(t *testing.T) {
	t.Parallel()

	safe := newFakeSafe()
	cfg := newTestCfg(config.ArtifactsTLSModeDisabled)
	w := NewArtifactsWriter(cfg, safe, "pve-wayne")

	ep := artifacts.Endpoint{URL: "http://10.0.0.11:9000"}

	err := w.WriteArtifacts(context.Background(), "pve-wayne", ep, artifacts.Credentials{}, nil)
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}

	meta := safe.data["secret/ocfp/pve-wayne/artifacts"]
	if meta["tls_mode"] != config.ArtifactsTLSModeDisabled {
		t.Errorf("tls_mode = %v, want disabled", meta["tls_mode"])
	}

	bosh := safe.data[w.PathBuilder.GetSystemBlobstorePath("mgmt", "bosh", "bosh")]
	if _, present := bosh["ca_cert"]; present {
		t.Errorf("ca_cert should be absent when TLS is disabled")
	}
}

// TestArtifactsWriter_WritesAllThreeBlobstoreTriples pins the vault tree the
// writer fans out to: mgmt-bosh, ocf-bosh, and ocf-cf-main. Each triple has
// a config entry, a /creds entry, and consistent bucket naming. Forgetting
// the ocf-bosh triple (added when env-BOSH switched to external blobstore)
// silently breaks the env-BOSH deploy with a manifest-resolve failure.
func TestArtifactsWriter_WritesAllThreeBlobstoreTriples(t *testing.T) {
	t.Parallel()

	safe := newFakeSafe()
	cfg := newTestCfg(config.ArtifactsTLSModeDisabled)
	w := NewArtifactsWriter(cfg, safe, "ocfp-lab-wayne")

	ep := artifacts.Endpoint{
		URL:       "https://10.64.64.11:9000",
		Host:      "10.64.64.11",
		Port:      9000,
		Region:    config.BlobstoreDefaultRegion,
		PathStyle: true,
	}
	creds := artifacts.Credentials{AccessKey: "AK", SecretKey: "SK"}

	err := w.WriteArtifacts(context.Background(), "ocfp-lab-wayne", ep, creds, nil)
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}

	cases := []struct {
		envType    string
		kind       string
		entry      string
		wantBucket string
	}{
		{"mgmt", "bosh", "bosh", "ocfp-lab-wayne-mgmt-bosh"},
		{"ocf", "bosh", "bosh", "ocfp-lab-wayne-ocf-bosh"},
		{"ocf", "cf", "main", "ocfp-lab-wayne-ocf-cf"},
	}

	for _, c := range cases {
		basePath := w.PathBuilder.GetSystemBlobstorePath(c.envType, c.kind, c.entry)

		body, ok := safe.data[basePath]
		if !ok {
			t.Errorf("missing blobstore entry at %s", basePath)

			continue
		}

		if body["bucket"] != c.wantBucket {
			t.Errorf("%s bucket = %v, want %s", basePath, body["bucket"], c.wantBucket)
		}

		if body["name"] != c.wantBucket {
			t.Errorf("%s name = %v, want %s (kit-convention alias)", basePath, body["name"], c.wantBucket)
		}

		// host + port required for bosh director blobstore job
		// (host/port keys, not endpoint).
		if body["host"] != "10.64.64.11" {
			t.Errorf("%s host = %v, want 10.64.64.11", basePath, body["host"])
		}

		if body["port"] != 9000 {
			t.Errorf("%s port = %v, want 9000", basePath, body["port"])
		}

		creds, ok := safe.data[basePath+"/creds"]
		if !ok {
			t.Errorf("missing creds entry at %s/creds", basePath)

			continue
		}

		if creds["access_key"] != "AK" || creds["secret_key"] != "SK" {
			t.Errorf("%s/creds = %+v, want AK/SK", basePath, creds)
		}
	}
}
