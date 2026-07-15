package bootstrap

import (
	"context"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts/provision"
)

// artifactsDeliverer performs the provider-specific out-of-band step needed
// to get RustFS provisioning onto the artifacts VM after boot, for compute
// backends whose cloud-init user-data path is unreliable or blocked. The
// rendered cloud-init payload (provision.RenderArtifactsCloudInit) is always
// attached to the instance-create request as the primary delivery path;
// artifactsDeliverer covers only the extra step some providers need on top
// of that.
//
// resolveArtifactsDeliverer is the single place that maps a provider name to
// its artifactsDeliverer. Adding a provider that needs an out-of-band step
// means implementing this interface and adding a case there — CreateArtifacts
// itself never needs to change.
type artifactsDeliverer interface {
	// deliverArtifacts runs the out-of-band step for a just-created artifacts
	// VM. A non-nil error is treated as non-fatal by the caller: it warns and
	// lets the readiness probe surface any resulting unhealthy state, so
	// implementations should return a wrapped, descriptive error rather than
	// retrying internally.
	deliverArtifacts(ctx context.Context, m *Manager, in provision.ArtifactsCloudInitInputs, artifactsIP string) error
}

// pveSSHDeliverer runs the RustFS provisioning script over SSH via the
// bastion. PVE 9.x rejects the cloud-init snippet-upload API
// (the /storage/<pool>/upload endpoint forbids content=snippets), so
// cloud-init user-data never reaches a PVE-hosted VM and this SSH-delivered
// script is the only path that actually provisions RustFS.
type pveSSHDeliverer struct{}

// deliverArtifacts runs provisionArtifactsViaSSH, hopping through the
// bastion to reach the artifacts VM.
func (pveSSHDeliverer) deliverArtifacts(ctx context.Context, m *Manager, in provision.ArtifactsCloudInitInputs, artifactsIP string) error {
	return m.provisionArtifactsViaSSH(ctx, in, artifactsIP)
}

// cloudInitDeliverer is the default artifactsDeliverer: cloud-init user-data
// (already attached to the instance-create request by CreateArtifacts)
// delivers the provisioning payload on its own, so no additional out-of-band
// step is required.
type cloudInitDeliverer struct{}

// deliverArtifacts is a no-op: cloud-init user-data already did the work.
func (cloudInitDeliverer) deliverArtifacts(context.Context, *Manager, provision.ArtifactsCloudInitInputs, string) error {
	return nil
}

// resolveArtifactsDeliverer resolves the artifactsDeliverer for providerName.
// pve is the only provider whose compute backend currently needs an
// out-of-band step (pveSSHDeliverer, above); every other provider name
// resolves to cloudInitDeliverer, relying on the cloud-init user-data path
// alone.
//
// config.ErrArtifactsRequiresPVE (internal/config/artifacts_config.go)
// hard-gates the artifacts feature to provider=pve at config-load time, so
// the cloudInitDeliverer branch is presently unreachable via CreateArtifacts
// in production. It exists so lifting that gate for a future provider means
// adding a case here (or a real implementation), not editing CreateArtifacts.
func resolveArtifactsDeliverer(providerName string) artifactsDeliverer {
	if strings.EqualFold(providerName, "pve") {
		return pveSSHDeliverer{}
	}

	return cloudInitDeliverer{}
}
