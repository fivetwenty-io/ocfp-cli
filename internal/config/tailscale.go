package config

import "errors"

// ErrTailscaleAuthKeyConflict is returned when both auth_key and
// auth_key_vault_path are set on the same scope. Operators must pick one
// source per scope; the loader merges scopes after validation.
var ErrTailscaleAuthKeyConflict = errors.New("tailscale: auth_key and auth_key_vault_path are mutually exclusive")

// TailscaleConfig is the YAML-facing tailscale section. It mirrors
// cpi.TailscaleSpec field-for-field plus a vault-path alternative for the
// auth key. Both ConfigFile (global defaults) and Config (per-bloc) carry
// an optional *TailscaleConfig. Per-bloc values override global on a
// field-by-field basis (see mergeTailscaleDefaults).
//
// Auth-key resolution at runtime:
//
//  1. If AuthKey is set, bootstrap uses the literal value.
//  2. Else if AuthKeyVaultPath is set, bootstrap parses "path:key" and
//     reads from vault.
//  3. Else, tailscale provisioning is skipped (soft-warn).
//
// The pointer-bool fields distinguish "unset" (inherit from global, or
// fall through to bootstrap default) from "explicit false". A plain bool
// would conflate the two.
//
// Enabled controls whether Tailscale is provisioned. Nil means disabled
// (default-false). Set enabled: true explicitly in YAML to opt in. A
// per-bloc explicit false overrides a global true.
type TailscaleConfig struct {
	Enabled *bool `json:"enabled,omitempty" mapstructure:"enabled" yaml:"enabled,omitempty"`

	AuthKey          string `json:"auth_key,omitempty"            mapstructure:"auth_key"            yaml:"auth_key,omitempty"`            //nolint:gosec // field name describes a credential, not a hardcoded one
	AuthKeyVaultPath string `json:"auth_key_vault_path,omitempty" mapstructure:"auth_key_vault_path" yaml:"auth_key_vault_path,omitempty"` //nolint:gosec // descriptive field name

	Hostname        string   `json:"hostname,omitempty"         mapstructure:"hostname"         yaml:"hostname,omitempty"`
	Tags            []string `json:"tags,omitempty"             mapstructure:"tags"             yaml:"tags,omitempty"`
	AcceptDNS       *bool    `json:"accept_dns,omitempty"       mapstructure:"accept_dns"       yaml:"accept_dns,omitempty"`
	AcceptRoutes    *bool    `json:"accept_routes,omitempty"    mapstructure:"accept_routes"    yaml:"accept_routes,omitempty"`
	SSH             *bool    `json:"ssh,omitempty"              mapstructure:"ssh"              yaml:"ssh,omitempty"`
	ExitNode        string   `json:"exit_node,omitempty"        mapstructure:"exit_node"        yaml:"exit_node,omitempty"`
	AdvertiseRoutes string   `json:"advertise_routes,omitempty" mapstructure:"advertise_routes" yaml:"advertise_routes,omitempty"`
}

// TailscaleEnabled reports whether Tailscale provisioning is active for
// the given config scope. Returns false when cfg is nil, when
// cfg.Enabled is nil (default-false), or when *cfg.Enabled is false.
// Only an explicit enabled: true in YAML returns true.
func TailscaleEnabled(cfg *TailscaleConfig) bool {
	return cfg != nil && cfg.Enabled != nil && *cfg.Enabled
}

// Validate ensures a single tailscale scope holds at most one auth-key
// source. Nil receiver is valid (no tailscale config).
func (t *TailscaleConfig) Validate() error {
	if t == nil {
		return nil
	}

	if t.AuthKey != "" && t.AuthKeyVaultPath != "" {
		return ErrTailscaleAuthKeyConflict
	}

	return nil
}

// mergeTailscaleDefaults fills empty fields on bloc.Tailscale from the
// global defaults. Per-bloc fields take precedence; defaults supply
// values only when the corresponding bloc field is unset. No-op when
// either argument is nil. When bloc.Tailscale is nil but defaults is
// not, bloc.Tailscale is populated as a deep copy of defaults.
//
// Auth-key inheritance is paired: if the bloc explicitly sets EITHER
// AuthKey or AuthKeyVaultPath, neither global auth-key field is inherited.
// This prevents the loader from emitting a merged config that violates
// the per-scope mutual-exclusion rule.
func mergeTailscaleDefaults(bloc *Config, defaults *TailscaleConfig) {
	if bloc == nil || defaults == nil {
		return
	}

	if bloc.Tailscale == nil {
		bloc.Tailscale = cloneTailscaleConfig(defaults)

		return
	}

	merged := bloc.Tailscale

	if merged.AuthKey == "" && merged.AuthKeyVaultPath == "" {
		merged.AuthKey = defaults.AuthKey
		merged.AuthKeyVaultPath = defaults.AuthKeyVaultPath
	}

	merged.Hostname = firstSetString(merged.Hostname, defaults.Hostname)
	merged.ExitNode = firstSetString(merged.ExitNode, defaults.ExitNode)
	merged.AdvertiseRoutes = firstSetString(merged.AdvertiseRoutes, defaults.AdvertiseRoutes)

	if len(merged.Tags) == 0 && len(defaults.Tags) > 0 {
		merged.Tags = append([]string(nil), defaults.Tags...)
	}

	if merged.AcceptDNS == nil && defaults.AcceptDNS != nil {
		v := *defaults.AcceptDNS
		merged.AcceptDNS = &v
	}

	if merged.AcceptRoutes == nil && defaults.AcceptRoutes != nil {
		v := *defaults.AcceptRoutes
		merged.AcceptRoutes = &v
	}

	if merged.SSH == nil && defaults.SSH != nil {
		v := *defaults.SSH
		merged.SSH = &v
	}

	if merged.Enabled == nil && defaults.Enabled != nil {
		v := *defaults.Enabled
		merged.Enabled = &v
	}
}

func cloneTailscaleConfig(src *TailscaleConfig) *TailscaleConfig {
	if src == nil {
		return nil
	}

	clone := *src

	if src.Tags != nil {
		clone.Tags = append([]string(nil), src.Tags...)
	}

	if src.Enabled != nil {
		v := *src.Enabled
		clone.Enabled = &v
	}

	if src.AcceptDNS != nil {
		v := *src.AcceptDNS
		clone.AcceptDNS = &v
	}

	if src.AcceptRoutes != nil {
		v := *src.AcceptRoutes
		clone.AcceptRoutes = &v
	}

	if src.SSH != nil {
		v := *src.SSH
		clone.SSH = &v
	}

	return &clone
}
