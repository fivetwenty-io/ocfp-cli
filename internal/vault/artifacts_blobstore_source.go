package vault

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// ArtifactsBlobstoreSource bundles the blobstore config values sourced from a
// bloc's artifacts VM state (endpoint, credentials, CA cert PEM, and TLS
// mode). Returned by ConfigureBlobstoresFromArtifactsState for a
// VaultProvider to promote to external-mode blobstore config when no
// explicit --blobstore-endpoint flag was supplied.
type ArtifactsBlobstoreSource struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	CACert    string
	TLSMode   string

	// Host and Port are the bare hostname and S3 port parsed from Endpoint —
	// matches the host/port fields vault.ArtifactsWriter writes alongside
	// endpoint (see blobstoreEntry), for callers (PVEVaultProvider.
	// writeArtifactsMeta) that need the same shape for the operational
	// metadata path.
	Host string
	Port int

	// FingerprintSHA256 and LeafNotAfter mirror the state resource's
	// tls_fingerprint_sha256 / tls_leaf_not_after properties (see
	// artifacts.LookupResult and internal/bootstrap.recordArtifactsState) —
	// operator/status metadata only, never a trust decision input. Empty
	// when state never recorded them.
	FingerprintSHA256 string
	LeafNotAfter      string
}

// ConfigureBlobstoresFromArtifactsState resolves blobstore auto-source
// values from a bloc's bootstrap state artifacts resource (the state entry
// internal/bootstrap.CreateArtifacts records). Returns nil (no error) when sm
// is nil, state has no artifacts resource, or the artifacts VM has no
// recorded endpoint yet — callers then fall back to their own
// CLI-flag-driven blobstore config flow.
//
// Extracted from PVEVaultProvider.ConfigureBlobstores's auto-source block so
// any VaultProvider whose bloc has an artifacts VM can reuse the same
// state-sourced blobstore promotion without duplicating the artifacts.Lookup
// call. Region defaulting, vault writes, and logging stay with the caller —
// this function only resolves the source values.
func ConfigureBlobstoresFromArtifactsState(sm *state.Manager, blocName string) (*ArtifactsBlobstoreSource, error) {
	if sm == nil {
		return nil, nil
	}

	// provider is nil: a tag-based fallback query needs live compute
	// credentials, which are unavailable at vault-populate time. State is the
	// only source here.
	lr, err := artifacts.Lookup(context.Background(), sm, nil, blocName)
	if err != nil {
		return nil, fmt.Errorf("artifacts lookup: %w", err)
	}

	if lr == nil || lr.Endpoint == "" {
		return nil, nil
	}

	return &ArtifactsBlobstoreSource{
		Endpoint:          lr.Endpoint,
		AccessKey:         lr.AccessKey,
		SecretKey:         lr.SecretKey,
		CACert:            lr.CACert,
		TLSMode:           lr.TLSMode,
		Host:              pveHostnameOnly(lr.Endpoint),
		Port:              portFromEndpoint(lr.Endpoint),
		FingerprintSHA256: lr.TLSFingerprintSHA256,
		LeafNotAfter:      lr.TLSLeafNotAfter,
	}, nil
}

// portFromEndpoint parses the numeric port out of an S3 endpoint URL (e.g.
// "https://10.0.0.11:9000" -> 9000). Returns 0 when the URL is unparseable
// or carries no explicit port — callers treat 0 the same way
// artifacts.Endpoint.Port's zero value is treated elsewhere (kits resolve a
// default from `endpoint` when `port` is absent/0).
func portFromEndpoint(endpoint string) int {
	u, err := url.Parse(endpoint)
	if err != nil || u.Port() == "" {
		return 0
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return 0
	}

	return port
}
