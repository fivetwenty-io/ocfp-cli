package commands

import (
	"net/url"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// originHost extracts the bare host from a scheme://host[:port] config
// string (e.g. a cf.Origin, cf.SSHOrigin, or services[].Service value),
// dropping any scheme and port. Returns "" on empty input or any parse
// failure — never an error, since every caller only wants a best-effort
// display value for the ORIGIN column.
//
//nolint:unused // consumed by collectCloudflareSection/collectIngressSection, landing in follow-up commits
func originHost(serviceURL string) string {
	if serviceURL == "" {
		return ""
	}

	parsed, err := url.Parse(serviceURL)
	if err != nil {
		return ""
	}

	return parsed.Hostname()
}

// cfExactHostnameOrigins builds the exact-match hostname-to-origin map used
// as rung O1 of the ORIGIN fallback in Sections 1-3: for every configured
// Cloudflare hostname (services[] entries, plus the SSH route when
// configured), the bare origin host it terminates at. Always returns a
// non-nil, possibly empty map — cf == nil is not an error, it means no
// Cloudflare config at all, i.e. no exact routes.
//
//nolint:unused // consumed by Section 1/2/3 ORIGIN builders, landing in follow-up commits
func cfExactHostnameOrigins(cf *config.CloudflareConfig) map[string]string {
	origins := make(map[string]string)

	if cf == nil {
		return origins
	}

	for _, svc := range cf.Services {
		if svc.Hostname == "" {
			continue
		}

		origins[svc.Hostname] = originHost(svc.Service)
	}

	if cf.SSHHostname != "" {
		origins[cf.SSHHostname] = originHost(cf.SSHOrigin)
	}

	return origins
}
