package precompile

import (
	"fmt"
	"strings"
)

// compiledKeyPrefix is the blobstore key prefix for compiled release tarballs.
const compiledKeyPrefix = "compiled-releases"

// CompiledKey returns the blobstore object key for a release's compiled tarball,
// encoding name, version, and stemcell so a mismatched stemcell can never be
// confused with a match (e.g. compiled-releases/capi-1.235.0-ubuntu-noble-1.383.tgz).
func CompiledKey(r Release, sc Stemcell) string {
	return fmt.Sprintf("%s/%s-%s-%s-%s.tgz",
		compiledKeyPrefix,
		slug(r.Name),
		slug(r.Version),
		slug(sc.OS),
		slug(sc.Version),
	)
}

// HTTPSURL builds a path-style https URL for an object on the RustFS endpoint.
// Path-style (host/bucket/key) is required because RustFS self-signed certs
// lack wildcard TLS for virtual-hosted-style (bucket.host) addressing.
func HTTPSURL(endpoint, bucket, key string) string {
	base := strings.TrimRight(endpoint, "/")

	return fmt.Sprintf("%s/%s/%s", base, bucket, key)
}

// slug normalizes a field for safe use in an object key, replacing any
// character outside [A-Za-z0-9._-] with '-' (handles '+' in dev versions).
func slug(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	return b.String()
}
