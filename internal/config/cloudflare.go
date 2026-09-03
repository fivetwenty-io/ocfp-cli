package config

import "errors"

// ErrCloudflareAPITokenConflict is returned when both api_token and
// api_token_vault_path are set on the same scope. Operators must pick one
// source per scope; the loader merges scopes after validation.
var ErrCloudflareAPITokenConflict = errors.New("cloudflare: api_token and api_token_vault_path are mutually exclusive")

// ErrCloudflareServiceIncomplete is returned when a services[] entry is missing
// its hostname or origin service. Both are required to build an ingress rule.
var ErrCloudflareServiceIncomplete = errors.New("cloudflare: service ingress entry requires both hostname and service")

// ServiceIngress is one extra per-hostname ingress rule for an infra-service web
// UI (shield, grafana, blacksmith, doomsday, concourse, ...) that is NOT fronted
// by the CF gorouter/haproxy. Each becomes an explicit ingress rule ordered
// BEFORE the *.apps/*.system wildcards (cloudflared first-match) plus a proxied
// CNAME. Hostnames under an existing edge-cert-covered wildcard (e.g.
// *.system.<domain>) reuse that cert; others require an ACM/Total-TLS cert.
type ServiceIngress struct {
	Hostname string `json:"hostname" mapstructure:"hostname" yaml:"hostname"`
	Service  string `json:"service"  mapstructure:"service"  yaml:"service"`
	// NoTLSVerify disables origin TLS verification (self-signed origin).
	NoTLSVerify *bool `json:"no_tls_verify,omitempty" mapstructure:"no_tls_verify" yaml:"no_tls_verify,omitempty"`
}

// CloudflareConfig is the YAML-facing cloudflare section, carried optionally
// by both ConfigFile (global defaults) and Config (per-bloc). Per-bloc values
// override global field-by-field via mergeCloudflareDefaults.
//
// Enabled controls provisioning. Nil means disabled (default-false); set
// enabled: true explicitly to opt in. A per-bloc explicit false overrides a
// global true (mirrors TailscaleConfig).
//
// API-token resolution at runtime: literal APIToken wins; else
// APITokenVaultPath ("path:key") is read from vault; else the feature is
// skipped (soft-warn).
type CloudflareConfig struct {
	Enabled *bool `json:"enabled,omitempty" mapstructure:"enabled" yaml:"enabled,omitempty"`

	APIToken          string `json:"api_token,omitempty"            mapstructure:"api_token"            yaml:"api_token,omitempty"`            // #nosec -- descriptive field name
	APITokenVaultPath string `json:"api_token_vault_path,omitempty" mapstructure:"api_token_vault_path" yaml:"api_token_vault_path,omitempty"` // #nosec -- descriptive field name

	Zone         string `json:"zone,omitempty"          mapstructure:"zone"          yaml:"zone,omitempty"`
	TunnelName   string `json:"tunnel_name,omitempty"   mapstructure:"tunnel_name"   yaml:"tunnel_name,omitempty"`
	Origin       string `json:"origin,omitempty"        mapstructure:"origin"        yaml:"origin,omitempty"`
	AppsDomain   string `json:"apps_domain,omitempty"   mapstructure:"apps_domain"   yaml:"apps_domain,omitempty"`
	SystemDomain string `json:"system_domain,omitempty" mapstructure:"system_domain" yaml:"system_domain,omitempty"`
	SSHHostname  string `json:"ssh_hostname,omitempty"  mapstructure:"ssh_hostname"  yaml:"ssh_hostname,omitempty"`
	SSHOrigin    string `json:"ssh_origin,omitempty"    mapstructure:"ssh_origin"    yaml:"ssh_origin,omitempty"`

	// OriginServerName is the SNI/cert name used when verifying TLS to the
	// origin for *.system (its cert SAN is *.system). Empty disables verify
	// (used for *.apps whose cert does not cover it).
	OriginServerName string `json:"origin_server_name,omitempty" mapstructure:"origin_server_name" yaml:"origin_server_name,omitempty"`

	// OriginNoTLSVerify disables TLS verification to the origin on the *.system
	// rule. Set true when the origin presents a self-signed cert (e.g. the PVE
	// lab haproxy), where the default OriginServerName verify path 502s.
	OriginNoTLSVerify *bool `json:"origin_no_tls_verify,omitempty" mapstructure:"origin_no_tls_verify" yaml:"origin_no_tls_verify,omitempty"`

	// Services are extra per-hostname ingress rules re-applied on every bootstrap
	// so manually-routed infra-service UIs survive re-provisioning.
	Services []ServiceIngress `json:"services,omitempty" mapstructure:"services" yaml:"services,omitempty"`
}

// CloudflareEnabled reports whether cloudflare tunnel provisioning is active.
// Only an explicit enabled: true in YAML returns true.
func CloudflareEnabled(cfg *CloudflareConfig) bool {
	return cfg != nil && cfg.Enabled != nil && *cfg.Enabled
}

// Validate ensures a single scope holds at most one api-token source.
func (c *CloudflareConfig) Validate() error {
	if c == nil {
		return nil
	}

	if c.APIToken != "" && c.APITokenVaultPath != "" {
		return ErrCloudflareAPITokenConflict
	}

	for _, s := range c.Services {
		if s.Hostname == "" || s.Service == "" {
			return ErrCloudflareServiceIncomplete
		}
	}

	return nil
}

// mergeCloudflareDefaults fills empty fields on bloc.Cloudflare from global
// defaults. Per-bloc fields take precedence. No-op when either arg is nil.
// When bloc.Cloudflare is nil, it is populated as a deep copy of defaults.
// Auth-token inheritance is paired: if the bloc sets EITHER token field,
// neither global token field is inherited.
func mergeCloudflareDefaults(bloc *Config, defaults *CloudflareConfig) {
	if bloc == nil || defaults == nil {
		return
	}

	if bloc.Cloudflare == nil {
		bloc.Cloudflare = cloneCloudflareConfig(defaults)

		return
	}

	merged := bloc.Cloudflare
	if merged.APIToken == "" && merged.APITokenVaultPath == "" {
		merged.APIToken = defaults.APIToken
		merged.APITokenVaultPath = defaults.APITokenVaultPath
	}

	merged.Zone = firstSetString(merged.Zone, defaults.Zone)
	merged.TunnelName = firstSetString(merged.TunnelName, defaults.TunnelName)
	merged.Origin = firstSetString(merged.Origin, defaults.Origin)
	merged.AppsDomain = firstSetString(merged.AppsDomain, defaults.AppsDomain)
	merged.SystemDomain = firstSetString(merged.SystemDomain, defaults.SystemDomain)
	merged.SSHHostname = firstSetString(merged.SSHHostname, defaults.SSHHostname)
	merged.SSHOrigin = firstSetString(merged.SSHOrigin, defaults.SSHOrigin)

	merged.OriginServerName = firstSetString(merged.OriginServerName, defaults.OriginServerName)
	if merged.Enabled == nil && defaults.Enabled != nil {
		v := *defaults.Enabled
		merged.Enabled = &v
	}

	if merged.OriginNoTLSVerify == nil && defaults.OriginNoTLSVerify != nil {
		v := *defaults.OriginNoTLSVerify
		merged.OriginNoTLSVerify = &v
	}

	if len(merged.Services) == 0 && len(defaults.Services) > 0 {
		merged.Services = cloneServiceIngress(defaults.Services)
	}
}

// cloneServiceIngress deep-copies a services slice so callers cannot mutate the
// source (and vice versa) through the shared backing array or *bool fields.
func cloneServiceIngress(src []ServiceIngress) []ServiceIngress {
	if len(src) == 0 {
		return nil
	}

	out := make([]ServiceIngress, len(src))
	for i, s := range src {
		out[i] = s
		if s.NoTLSVerify != nil {
			v := *s.NoTLSVerify
			out[i].NoTLSVerify = &v
		}
	}

	return out
}

func cloneCloudflareConfig(src *CloudflareConfig) *CloudflareConfig {
	if src == nil {
		return nil
	}

	clone := *src
	if src.Enabled != nil {
		v := *src.Enabled
		clone.Enabled = &v
	}

	if src.OriginNoTLSVerify != nil {
		v := *src.OriginNoTLSVerify
		clone.OriginNoTLSVerify = &v
	}

	clone.Services = cloneServiceIngress(src.Services)

	return &clone
}
