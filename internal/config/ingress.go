package config

import "errors"

// Ingress provider identifiers accepted in ingress.provider.
const (
	IngressProviderCloudflared = "cloudflared"
	IngressProviderTailscale   = "tailscale"
)

// Ingress configuration errors. All are load-time hard errors — an explicit
// provider that cannot work is a config contradiction, unlike the soft
// runtime skips used for missing tokens.
var (
	ErrIngressProviderInvalid     = errors.New(`ingress: provider must be "cloudflared" or "tailscale"`)
	ErrIngressTailscaleDisabled   = errors.New("ingress: provider tailscale requires tailscale.enabled: true")
	ErrIngressTailscaleDNS        = errors.New("ingress: provider tailscale requires cloudflare.zone and an api token source for DNS records")
	ErrIngressCloudflaredDisabled = errors.New("ingress: provider cloudflared requires cloudflare.enabled: true")
)

// IngressConfig selects which ingress provider fronts the bloc. Carried
// optionally by both ConfigFile (global defaults) and Config (per-bloc);
// per-bloc wins via mergeIngressDefaults. An empty/absent section preserves
// pre-existing behavior (cloudflared when cloudflare.enabled, else none).
type IngressConfig struct {
	Provider string `json:"provider,omitempty" mapstructure:"provider" yaml:"provider,omitempty"`
}

// ResolveIngressProvider returns the effective provider for a bloc:
// the explicit ingress.provider when set, else "cloudflared" when the
// cloudflare tunnel is enabled, else "" (no ingress).
func ResolveIngressProvider(cfg *Config) string {
	if cfg == nil {
		return ""
	}

	if cfg.Ingress != nil && cfg.Ingress.Provider != "" {
		return cfg.Ingress.Provider
	}

	if CloudflareEnabled(cfg.Cloudflare) {
		return IngressProviderCloudflared
	}

	return ""
}

// SystemScoped reports whether system-scoped service FQDNs should carry the
// .system. infix for this bloc. True whenever an ingress provider is in
// effect (explicit ingress.provider, or the legacy cloudflared-tunnel-only
// default) — .system. routing is provider-independent, not tied to
// cloudflare specifically. False when no ingress fronts the bloc (e.g. the
// stackit real-LB shape with per-host certs and no ingress/cloudflare
// section at all).
func SystemScoped(cfg *Config) bool {
	return ResolveIngressProvider(cfg) != ""
}

// ValidateIngress checks cross-section consistency of an explicit provider
// choice. Called from the bloc validate chain after tailscale/cloudflare
// merging, so it sees final values.
func ValidateIngress(cfg *Config) error {
	if cfg == nil || cfg.Ingress == nil || cfg.Ingress.Provider == "" {
		return nil
	}

	switch cfg.Ingress.Provider {
	case IngressProviderCloudflared:
		if !CloudflareEnabled(cfg.Cloudflare) {
			return ErrIngressCloudflaredDisabled
		}
	case IngressProviderTailscale:
		if !TailscaleEnabled(cfg.Tailscale) {
			return ErrIngressTailscaleDisabled
		}

		cf := cfg.Cloudflare
		if cf == nil || cf.Zone == "" || (cf.APIToken == "" && cf.APITokenVaultPath == "") {
			return ErrIngressTailscaleDNS
		}
	default:
		return ErrIngressProviderInvalid
	}

	return nil
}

// mergeIngressDefaults fills bloc.Ingress from global defaults. Per-bloc
// values win; a nil bloc section is populated as a copy of defaults.
func mergeIngressDefaults(bloc *Config, defaults *IngressConfig) {
	if bloc == nil || defaults == nil {
		return
	}

	if bloc.Ingress == nil {
		clone := *defaults
		bloc.Ingress = &clone

		return
	}

	if bloc.Ingress.Provider == "" {
		bloc.Ingress.Provider = defaults.Provider
	}
}
