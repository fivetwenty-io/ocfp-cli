package vault

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// systemScopedServices are infra-service web UIs that are only reachable behind
// the *.system.<base> edge-cert wildcard (the Cloudflare tunnel's cert SAN on
// PVE). When the edge wildcard is in use they derive as {service}.system.{base}
// instead of {service}.{base}; otherwise (e.g. stackit behind a real LB with
// per-host certs) they keep the flat {service}.{base} form. Explicit overrides
// always win regardless.
//
//nolint:gochecknoglobals // package-level constant set of system-scoped services
var systemScopedServices = map[string]bool{
	"concourse":  true,
	"shield":     true,
	"prometheus": true,
	"blacksmith": true,
	"doomsday":   true,
	"grafana":    true,
}

// IsSystemScopedService reports whether a service is an infra UI that sits
// behind the *.system wildcard cert and therefore should gain a `.system`
// FQDN segment when the edge wildcard (e.g. the Cloudflare tunnel) fronts it.
func IsSystemScopedService(service string) bool {
	return systemScopedServices[service]
}

// DeriveFQDN generates an FQDN from service name and base domain.
// Pattern: {service}.{base}, or {service}.system.{base} when systemScoped is
// true and the service is an infra UI behind the *.system wildcard cert.
func DeriveFQDN(service, base string, systemScoped bool) string {
	if base == "" {
		return ""
	}

	if systemScoped && systemScopedServices[service] {
		return fmt.Sprintf("%s.system.%s", service, base)
	}

	return fmt.Sprintf("%s.%s", service, base)
}

// GetFQDN returns the explicit FQDN if set, otherwise derives from base.
// If neither explicit nor base is available, returns empty string.
// systemScoped routes infra-UI services through the *.system wildcard.
func GetFQDN(service string, explicit map[string]string, base string, systemScoped bool) string {
	if explicit != nil {
		if fqdn, ok := explicit[service]; ok && fqdn != "" {
			return fqdn
		}
	}

	if base != "" {
		return DeriveFQDN(service, base, systemScoped)
	}

	return ""
}

// ExplicitFQDNsForEnv returns the explicitly configured per-service FQDNs for
// the given environment type, or nil for an unknown env type / nil config.
func ExplicitFQDNsForEnv(fqdnConfig *config.FQDNConfig, envType string) map[string]string {
	if fqdnConfig == nil {
		return nil
	}

	switch envType {
	case MgmtEnvType:
		return fqdnConfig.Mgmt
	case OCFEnvType:
		return fqdnConfig.OCF
	default:
		return nil
	}
}

// PopulateFQDNsForEnv generates a complete map of FQDNs for an environment type.
// It uses explicit values where provided, and derives the rest from base domain.
// This pre-populates all known services for the environment. When systemScoped
// is true, infra-UI services are derived under the *.system wildcard.
func PopulateFQDNsForEnv(envType string, explicit map[string]string, base string, systemScoped bool) map[string]interface{} {
	services := GetServicesForEnvType(envType)
	if services == nil {
		return nil
	}

	fqdns := make(map[string]interface{}, len(services))
	for _, service := range services {
		fqdn := GetFQDN(service, explicit, base, systemScoped)
		if fqdn != "" {
			fqdns[service] = fqdn
		}
	}

	// Also include any explicit FQDNs that aren't in the known services list
	for service, fqdn := range explicit {
		if fqdn != "" {
			fqdns[service] = fqdn
		}
	}

	return fqdns
}
