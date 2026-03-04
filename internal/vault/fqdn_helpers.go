package vault

import "fmt"

// DeriveFQDN generates an FQDN from service name and base domain.
// Pattern: {service}.{base}.
func DeriveFQDN(service, base string) string {
	if base == "" {
		return ""
	}

	return fmt.Sprintf("%s.%s", service, base)
}

// GetFQDN returns the explicit FQDN if set, otherwise derives from base.
// If neither explicit nor base is available, returns empty string.
func GetFQDN(service string, explicit map[string]string, base string) string {
	if explicit != nil {
		if fqdn, ok := explicit[service]; ok && fqdn != "" {
			return fqdn
		}
	}

	if base != "" {
		return DeriveFQDN(service, base)
	}

	return ""
}

// PopulateFQDNsForEnv generates a complete map of FQDNs for an environment type.
// It uses explicit values where provided, and derives the rest from base domain.
// This pre-populates all known services for the environment.
func PopulateFQDNsForEnv(envType string, explicit map[string]string, base string) map[string]interface{} {
	services := GetServicesForEnvType(envType)
	if services == nil {
		return nil
	}

	fqdns := make(map[string]interface{}, len(services))
	for _, service := range services {
		fqdn := GetFQDN(service, explicit, base)
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
