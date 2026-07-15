package artifacts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// ErrArtifactsCAMissing is returned by EndpointForLookup when an https
// artifacts endpoint is configured for tls.mode=internal-ca but no CA
// certificate is available from state or vault. Trust decisions never fall
// back to skipping verification in this case — the caller must recover or
// re-mint the bloc CA first.
var ErrArtifactsCAMissing = errors.New("artifacts CA missing from state/vault")

// ErrArtifactsInsecureOptInRequired is returned by EndpointForLookup when an
// https self-signed artifacts endpoint has no CA cert to pin to and the
// caller has not explicitly opted into skipping TLS verification.
var ErrArtifactsInsecureOptInRequired = errors.New("artifacts endpoint has no CA cert; explicit insecure opt-in required")

// ErrArtifactsEndpointInvalid is returned by EndpointForLookup when the
// endpoint URL is empty or does not start with http:// or https://.
var ErrArtifactsEndpointInvalid = errors.New("artifacts endpoint URL must start with http:// or https://")

// LookupResult bundles the artifacts VM identity for CLI commands and other
// callers that need to operate on the existing VM.
type LookupResult struct {
	VMID         string
	Name         string
	PrivateIP    string
	Endpoint     string
	AccessKey    string
	SecretKey    string
	TLSMode      string
	ZFSDataset   string
	DataVolumeID string
	CACert       string
}

// Lookup resolves the artifacts VM by checking state first, then falling back
// to a provider tag query. Returns nil + nil error when no artifacts VM exists.
func Lookup(ctx context.Context, sm *state.Manager, provider cpi.Provider, blocName string) (*LookupResult, error) {
	vmName := blocName + "-artifacts"

	if r, err := sm.GetResource(ResourceType, vmName); err == nil && r != nil {
		return resultFromResource(r), nil
	}

	if provider == nil {
		return nil, nil
	}

	insts, err := provider.ComputeManager().ListInstances(ctx, map[string]string{
		"ocfp:role": "artifacts",
		"ocfp:bloc": blocName,
	})
	if err != nil {
		return nil, fmt.Errorf("listing artifacts instances by tag: %w", err)
	}

	for _, inst := range insts {
		if inst.Name == vmName {
			return &LookupResult{
				VMID:      inst.ID,
				Name:      inst.Name,
				PrivateIP: inst.PrivateIP,
			}, nil
		}
	}

	return nil, nil
}

func resultFromResource(r *state.Resource) *LookupResult {
	get := func(k string) string {
		v, ok := r.Properties[k].(string)
		if !ok {
			return ""
		}

		return v
	}

	return &LookupResult{
		VMID:         get("vm_id"),
		Name:         r.Name,
		PrivateIP:    get("private_ip"),
		Endpoint:     get("endpoint"),
		AccessKey:    get("access_key"),
		SecretKey:    get("secret_key"),
		TLSMode:      get("tls_mode"),
		ZFSDataset:   get("zfs_dataset"),
		DataVolumeID: get("data_volume_id"),
		CACert:       get("ca_cert"),
	}
}

// EndpointForLookup derives the S3 Endpoint (base URL plus TLS trust
// material) an artifacts client should use, from the raw fields a caller has
// resolved out of state and/or vault: the endpoint URL, the configured
// tls.mode, and any CA certificate PEM already recovered. It is the single
// place the mode × cert-presence × scheme trust decision is made, so callers
// (precompile, the PVE vault provider, future providers) do not each
// reimplement — or diverge on — when it is safe to skip TLS verification.
//
// blocName is used only to make returned errors actionable; it does not
// affect the trust decision.
//
// Decision table:
//
//   - endpointURL empty, or missing an http(s):// scheme → ErrArtifactsEndpointInvalid.
//   - caCert non-empty → always CA-pool pinning, regardless of tlsMode. A
//     recovered CA is trust material; use it.
//   - scheme is http (not https) → no TLS material is meaningful; returns a
//     plain Endpoint with no error. Matches tls.mode "disabled" and any
//     endpoint that genuinely has TLS turned off.
//   - scheme is https, caCert empty, tlsMode is "internal-ca" → the CA is
//     mandatory for this mode and none was found: ErrArtifactsCAMissing.
//     Never silently downgrades to skip-verify.
//   - scheme is https, caCert empty, tlsMode is "self-signed" → the operator
//     may explicitly opt into skipping verification via allowInsecure; without
//     that opt-in, ErrArtifactsInsecureOptInRequired.
//   - scheme is https, caCert empty, tlsMode is "disabled", "", or anything
//     else unrecognized → the endpoint scheme contradicts the configured
//     mode (or the mode is unknown); this is a state inconsistency, so it
//     errors rather than guessing.
func EndpointForLookup(blocName, endpointURL, tlsMode, caCert string, allowInsecure bool) (Endpoint, error) {
	if !strings.HasPrefix(endpointURL, "http://") && !strings.HasPrefix(endpointURL, "https://") {
		return Endpoint{}, fmt.Errorf("%w: %q", ErrArtifactsEndpointInvalid, endpointURL)
	}

	ep := Endpoint{
		URL:       endpointURL,
		Region:    "us-east-1",
		PathStyle: true,
	}

	if caCert != "" {
		ep.CACert = caCert

		return ep, nil
	}

	if !strings.HasPrefix(endpointURL, "https://") {
		// http:// (or tls.mode disabled with an http endpoint): no TLS trust
		// material needed at all.
		return ep, nil
	}

	switch tlsMode {
	case config.ArtifactsTLSModeInternalCA:
		return Endpoint{}, fmt.Errorf(
			"%w: bloc %q tls.mode=internal-ca but no CA cert in state or vault; run `ocfp artifacts ca --bloc %s` to inspect, or `ocfp artifacts provision --bloc %s` to recover/re-mint it",
			ErrArtifactsCAMissing, blocName, blocName, blocName)
	case config.ArtifactsTLSModeSelfSigned:
		if !allowInsecure {
			return Endpoint{}, fmt.Errorf(
				"%w: bloc %q artifacts endpoint %s is self-signed with no CA cert to pin; pass the explicit insecure opt-in to skip verification, or provide a CA/cert bundle",
				ErrArtifactsInsecureOptInRequired, blocName, endpointURL)
		}

		ep.SkipTLSVerify = true

		return ep, nil
	default:
		return Endpoint{}, fmt.Errorf(
			"%w: bloc %q artifacts endpoint %s is https but tls.mode is %q with no CA cert; state may be stale — run `ocfp artifacts status --bloc %s` and `ocfp artifacts provision --bloc %s`",
			ErrArtifactsCAMissing, blocName, endpointURL, tlsMode, blocName, blocName)
	}
}
