package artifacts

import "context"

// ResourceType is the state.Resource Type for the artifacts VM.
const ResourceType = "artifacts"

// Endpoint is the resolved RustFS S3 endpoint for the artifacts VM. URL is the
// full https://host:port (or http:// when TLS is disabled). PathStyle is always
// true for RustFS. Region is "us-east-1" (SigV4 needs a value; unused by RustFS).
type Endpoint struct {
	URL       string
	Host      string
	Port      int
	Region    string
	PathStyle bool
	CACert    string // empty when TLS is disabled
}

// VaultWriter persists artifacts blobstore credentials and metadata to vault.
// Implemented by internal/vault. Decoupled here so the artifacts package has
// no direct dependency on the vault client (avoids import cycles + simplifies tests).
type VaultWriter interface {
	WriteArtifacts(ctx context.Context, blocName string, ep Endpoint, creds Credentials, tls *TLSMaterial) error
}

// ReadinessProbe verifies the RustFS S3 endpoint is responding to ListBuckets.
// Production implementation hits the endpoint via the same AWS SDK v2 client
// used for bucket creation; tests substitute a stub.
type ReadinessProbe interface {
	Probe(ctx context.Context, ep Endpoint, creds Credentials, caPEM string) error
}
