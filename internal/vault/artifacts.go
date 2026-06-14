package vault

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// ArtifactsWriter persists ocfp-artifacts blobstore configuration into vault.
// Implements artifacts.VaultWriter. The writer fans out to seven paths:
//
//   - {bloc}/mgmt/bosh/blobstores/bosh           (mgmt-BOSH director config)
//   - {bloc}/mgmt/bosh/blobstores/bosh/creds     (mgmt-BOSH director credentials)
//   - {bloc}/ocf/bosh/blobstores/bosh            (env-BOSH director config)
//   - {bloc}/ocf/bosh/blobstores/bosh/creds      (env-BOSH director credentials)
//   - {bloc}/ocf/cf/blobstores/main              (CF blobstore config)
//   - {bloc}/ocf/cf/blobstores/main/creds        (CF blobstore credentials)
//   - {bloc}/ocfp/artifacts                      (operational metadata)
//
// The CA cert (when TLS is enabled) is read from `ep.CACert` and written into
// the blobstore config paths under `ca_cert` so genesis kits can pin it. For
// internal-ca mode this is the bloc CA cert, not the leaf cert.
type ArtifactsWriter struct {
	Safe        SafeInterface
	PathBuilder *PathBuilder
	BlocName    string
	TLSMode     string
}

// NewArtifactsWriter constructs an ArtifactsWriter bound to a bloc.
func NewArtifactsWriter(cfg *config.Config, safe SafeInterface, blocName string) *ArtifactsWriter {
	mode := ""
	if cfg != nil {
		mode = cfg.Artifacts.TLS.Mode
	}

	return &ArtifactsWriter{
		Safe:        safe,
		PathBuilder: NewPathBuilder(cfg, blocName),
		BlocName:    blocName,
		TLSMode:     mode,
	}
}

// WriteArtifacts implements artifacts.VaultWriter. Context is accepted for API
// symmetry but the underlying Safe operations are synchronous and ignore it.
func (w *ArtifactsWriter) WriteArtifacts(_ context.Context, blocName string, ep artifacts.Endpoint, creds artifacts.Credentials, tls *artifacts.TLSMaterial) error {
	if w.BlocName == "" {
		w.BlocName = blocName
	}

	caPEM := ep.CACert

	pairs := []struct {
		path string
		body map[string]interface{}
	}{
		{
			path: w.PathBuilder.GetSystemBlobstorePath("mgmt", "bosh", "bosh"),
			body: blobstoreEntry(ep, caPEM, w.BlocName+"-mgmt-bosh"),
		},
		{
			path: w.PathBuilder.GetSystemBlobstorePath("ocf", "bosh", "bosh"),
			body: blobstoreEntry(ep, caPEM, w.BlocName+"-ocf-bosh"),
		},
		{
			path: w.PathBuilder.GetSystemBlobstorePath("ocf", "cf", "main"),
			body: blobstoreEntry(ep, caPEM, w.BlocName+"-ocf-cf"),
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
		"tls_mode": tlsModeLabel(w.TLSMode, tls, caPEM),
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
		"mode":     "external",
		"endpoint": ep.URL,
		// host + port are written alongside endpoint because the bosh
		// director's blobstore job template (and minio overlay convention)
		// uses `host` + `port` keys, not `endpoint`. Genesis kits read them
		// from this entry via `(( vault ... ":host" ))` / `:port`.
		"host":       ep.Host,
		"port":       ep.Port,
		"region":     ep.Region,
		"path_style": ep.PathStyle,
		// `bucket` is the historical key. `name` aliases it to match the
		// genesis-kit convention (meta.ocfp.bosh.s3.bucket_name reads
		// `:name`). Both are written so old + new consumers both work.
		"bucket": bucketName,
		"name":   bucketName,
		"status": "configured",
	}

	if caPEM != "" {
		entry["ca_cert"] = caPEM
	}

	return entry
}

// tlsModeLabel returns the operational mode label. `mode` is the bloc-config
// value (authoritative); the tls/caPEM args are fallbacks for callers that
// don't plumb mode through.
func tlsModeLabel(mode string, tls *artifacts.TLSMaterial, caPEM string) string {
	switch mode {
	case config.ArtifactsTLSModeInternalCA, config.ArtifactsTLSModeSelfSigned, config.ArtifactsTLSModeDisabled:
		return mode
	}

	switch {
	case tls != nil:
		return config.ArtifactsTLSModeSelfSigned
	case caPEM != "":
		return config.ArtifactsTLSModeInternalCA
	default:
		return config.ArtifactsTLSModeDisabled
	}
}
