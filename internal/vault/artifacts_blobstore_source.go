package vault

import (
	"context"
	"fmt"

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
		Endpoint:  lr.Endpoint,
		AccessKey: lr.AccessKey,
		SecretKey: lr.SecretKey,
		CACert:    lr.CACert,
		TLSMode:   lr.TLSMode,
	}, nil
}
