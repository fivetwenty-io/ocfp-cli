package config

import (
	"errors"
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/version"
)

// Config errors.
var (
	ErrProviderOrIaasRequired = errors.New("provider or iaas must be specified")
	ErrNoConfigPath           = errors.New("no config file path available")

	// ErrNoConfigFile is returned when no configuration file is found at the
	// default locations and no explicit path was provided.
	ErrNoConfigFile = errors.New(
		"no configuration file found; use ~/.config/ocfp/config.yml " +
			"(or the legacy ~/.ocfp/config.yml if present), or specify -f configfile.yml",
	)

	// ErrNoBlocName is returned when LoadWithParams is called without a bloc name.
	ErrNoBlocName = errors.New("no bloc name provided")

	// ErrPVEAuthRequired is returned when a PVE bloc has neither API token auth
	// (auth_token + token_secret) nor user/password auth (username + password)
	// configured. At least one complete auth mode is required.
	ErrPVEAuthRequired = errors.New("pve config: at least one auth mode required: set (auth_token + token_secret) or (username + password)")

	// ErrPVEVMIDRangeInvalid is returned when the configured vmid_range_start
	// and vmid_range_end values are inconsistent: end must be greater than start,
	// both must be positive, and end must not exceed the PVE maximum (999999999).
	ErrPVEVMIDRangeInvalid = errors.New("pve config: vmid_range_end must be > vmid_range_start > 0 and <= 999999999")

	// ErrTemplateSeedIPRequired is returned when template_seed_gateway,
	// template_seed_dns, or template_seed_searchdomain is set without
	// template_seed_ip. The gateway/dns/searchdomain fields only make sense
	// alongside a static seed address; DHCP mode (the zero value) leaves all
	// four fields empty.
	ErrTemplateSeedIPRequired = errors.New("pve config: template_seed_gateway/dns/searchdomain require template_seed_ip")

	// ErrTemplateSeedIPInvalid is returned when template_seed_ip fails to
	// parse as an IPv4 CIDR, or names the network or broadcast address of
	// its own prefix rather than a host address.
	ErrTemplateSeedIPInvalid = errors.New("pve config: template_seed_ip must be a valid IPv4 host address in CIDR notation, choose an address reserved for template seeding, outside every static and dynamic band")

	// ErrTemplateSeedGatewayInvalid is returned when template_seed_ip is set
	// but template_seed_gateway is missing, fails to parse, falls outside
	// the template_seed_ip prefix, or equals template_seed_ip.
	ErrTemplateSeedGatewayInvalid = errors.New("pve config: template_seed_gateway is required with template_seed_ip, must be a valid IP contained in the template_seed_ip prefix, and must differ from template_seed_ip")

	// ErrTemplateSeedDNSInvalid is returned when a template_seed_dns entry
	// fails to parse as an IP address.
	ErrTemplateSeedDNSInvalid = errors.New("pve config: template_seed_dns entries must be valid IP addresses")
)

// ErrBlocNotFound returns an error indicating the specified bloc was not found in the configuration file.
func ErrBlocNotFound(blocName, configPath string) error {
	return fmt.Errorf("bloc '%s' not found in configuration file %s", blocName, configPath) //nolint:err113 // dynamic error with context
}

// ErrInvalidProvider returns an error for an unrecognized or unsupported cloud provider.
func ErrInvalidProvider(provider string) error {
	return fmt.Errorf("invalid provider: %s", provider) //nolint:err113 // dynamic error with context
}

// ErrConfigSchemaTooNew returns an error when a config file declares a
// config_schema version higher than the running binary's SupportedConfigSchema.
// The message names the running binary's version and build time so the
// operator immediately knows which binary is stale, and points at the fix
// (upgrade ocfp) rather than reporting a confusing downstream failure.
func ErrConfigSchemaTooNew(configSchema, supported int) error {
	info := version.Get()

	return fmt.Errorf( //nolint:err113 // dynamic error with context
		"config_schema %d is newer than this build supports (%d): this config requires a newer ocfp build "+
			"(running v%s, commit %s, built %s) — upgrade ocfp and try again",
		configSchema, supported, info.Version, info.GitCommit, info.BuildTime,
	)
}
