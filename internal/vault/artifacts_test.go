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
