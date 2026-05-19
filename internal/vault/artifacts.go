package vault

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// ArtifactsWriter persists ocfp-artifacts blobstore configuration into vault.
// Implements artifacts.VaultWriter. The writer fans out to four paths:
//
//   - {bloc}/mgmt/bosh/blobstores/bosh           (BOSH director config)
//   - {bloc}/mgmt/bosh/blobstores/bosh/creds     (BOSH director credentials)
//   - {bloc}/ocf/cf/blobstores/main              (CF blobstore config)
//   - {bloc}/ocf/cf/blobstores/main/creds        (CF blobstore credentials)
//   - {bloc}/ocfp/artifacts                      (operational metadata)
//
// CA cert (when TLS is enabled) is written into the blobstore config paths
// under `ca_cert` so genesis kits can pin it.
type ArtifactsWriter struct {
	Safe        SafeInterface
	PathBuilder *PathBuilder
	BlocName    string
}

// NewArtifactsWriter constructs an ArtifactsWriter bound to a bloc.
func NewArtifactsWriter(cfg *config.Config, safe SafeInterface, blocName string) *ArtifactsWriter {
	return &ArtifactsWriter{
		Safe:        safe,
		PathBuilder: NewPathBuilder(cfg, blocName),
		BlocName:    blocName,
	}
}

// WriteArtifacts implements artifacts.VaultWriter. Context is accepted for API
// symmetry but the underlying Safe operations are synchronous and ignore it.
func (w *ArtifactsWriter) WriteArtifacts(_ context.Context, blocName string, ep artifacts.Endpoint, creds artifacts.Credentials, tls *artifacts.TLSMaterial) error {
	if w.BlocName == "" {
		w.BlocName = blocName
	}

	caPEM := ""
	if tls != nil {
		caPEM = tls.CertPEM
	}

	pairs := []struct {
		path string
		body map[string]interface{}
	}{
		{
			path: w.PathBuilder.GetSystemBlobstorePath("mgmt", "bosh", "bosh"),
			body: blobstoreEntry(ep, caPEM, fmt.Sprintf("%s-mgmt-bosh", w.BlocName)),
		},
		{
			path: w.PathBuilder.GetSystemBlobstorePath("ocf", "cf", "main"),
			body: blobstoreEntry(ep, caPEM, fmt.Sprintf("%s-ocf-cf", w.BlocName)),
		},
	}

	for _, p := range pairs {
		err := w.Safe.SetMultiple(p.path, p.body)
		if err != nil {
			return fmt.Errorf("writing %s: %w", p.path, err)
		}

		err = w.Safe.SetMultiple(p.path+"/creds", map[string]interface{}{
			"access_key": creds.AccessKey,
			"secret_key": creds.SecretKey,
		})
		if err != nil {
			return fmt.Errorf("writing %s/creds: %w", p.path, err)
		}
	}

	meta := map[string]interface{}{
		"endpoint": ep.URL,
		"host":     ep.Host,
		"port":     ep.Port,
		"tls_mode": tlsModeLabel(tls, caPEM),
	}

	if tls != nil {
		meta["tls_fingerprint_sha256"] = tls.Fingerprint
	}

	metaPath := filepath.Join("secret/ocfp", w.BlocName, "artifacts")

	err := w.Safe.SetMultiple(metaPath, meta)
	if err != nil {
		return fmt.Errorf("writing %s: %w", metaPath, err)
	}

	return nil
}

func blobstoreEntry(ep artifacts.Endpoint, caPEM, bucketName string) map[string]interface{} {
	entry := map[string]interface{}{
		"mode":       "external",
		"endpoint":   ep.URL,
		"region":     ep.Region,
		"path_style": ep.PathStyle,
		"bucket":     bucketName,
		"status":     "configured",
	}

	if caPEM != "" {
		entry["ca_cert"] = caPEM
	}

	return entry
}

func tlsModeLabel(tls *artifacts.TLSMaterial, caPEM string) string {
	switch {
	case tls != nil:
		return "self-signed"
	case caPEM != "":
		return "internal-ca"
	default:
		return "disabled"
	}
}
