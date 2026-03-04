package ssh

import (
	"context"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// ValidSSHKeyPrefixes returns the accepted OpenSSH public key type prefixes.
func ValidSSHKeyPrefixes() []string { //nolint:gochecknoglobals // read-only list, function avoids global
	return []string{
		"ssh-rsa",
		"ssh-ed25519",
		"ecdsa-sha2-nistp256",
		"ecdsa-sha2-nistp384",
		"ecdsa-sha2-nistp521",
	}
}

// KeyFetchFunc fetches SSH public keys for a given username from a provider.
type KeyFetchFunc func(ctx context.Context, username string) ([]string, error)

// ResolveKeySpecs resolves a map of label→key-spec entries into label→[]resolvedPublicKeys.
//
// Key spec formats:
//   - "github/<username>" — fetch public keys via fetchGitHub
//   - "gitlab/<username>" — fetch public keys via fetchGitLab
//   - direct SSH public key string (must start with a known prefix)
//
// Failed lookups are logged as warnings; processing continues for other entries.
func ResolveKeySpecs(
	ctx context.Context,
	keySpecs map[string]string,
	fetchGitHub, fetchGitLab KeyFetchFunc,
) (map[string][]string, error) {
	log := logger.Get()
	resolved := make(map[string][]string, len(keySpecs))

	for label, spec := range keySpecs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			log.Warnw("Empty key spec, skipping", "label", label)

			continue
		}

		switch {
		case strings.HasPrefix(spec, "github/"):
			username := strings.TrimPrefix(spec, "github/")

			keys, err := fetchGitHub(ctx, username)
			if err != nil {
				log.Warnw("Failed to fetch GitHub keys, skipping",
					"label", label, "username", username, "error", err)

				continue
			}

			resolved[label] = keys

		case strings.HasPrefix(spec, "gitlab/"):
			username := strings.TrimPrefix(spec, "gitlab/")

			keys, err := fetchGitLab(ctx, username)
			if err != nil {
				log.Warnw("Failed to fetch GitLab keys, skipping",
					"label", label, "username", username, "error", err)

				continue
			}

			resolved[label] = keys

		default:
			if !isValidSSHKeySpec(spec) {
				log.Warnw("Invalid SSH key format, skipping",
					"label", label)

				continue
			}

			resolved[label] = []string{spec}
		}
	}

	return resolved, nil
}

// isValidSSHKeySpec checks whether a string starts with a known SSH key type prefix.
func isValidSSHKeySpec(key string) bool {
	for _, prefix := range ValidSSHKeyPrefixes() {
		if strings.HasPrefix(key, prefix+" ") {
			return true
		}
	}

	return false
}
