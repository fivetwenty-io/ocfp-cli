package ssh

import (
	"context"
	"sort"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// KeyFetchFunc fetches SSH public keys for a given username from a provider.
type KeyFetchFunc func(ctx context.Context, username string) ([]string, error)

// ResolveKeySpecs resolves a map of label→key-spec entries into label→[]resolvedPublicKeys.
//
// Key spec formats are those config.ParseBastionKeySpec reads:
//   - "github/<username>" or "github.com/<username>" — fetch public keys via fetchGitHub
//   - "gitlab/<username>" or "gitlab.com/<username>" — fetch public keys via fetchGitLab
//   - a literal OpenSSH public key of any type ssh.ParseAuthorizedKey reads,
//     security keys included
//
// The host forms may carry https:// in front and .keys at the end. Every
// entry is parsed before anything is fetched, and an entry that cannot be
// parsed is an error naming its label, so a bastion is never brought up
// with an entry silently missing. A failed fetch is logged as a warning and
// processing continues with the other entries.
func ResolveKeySpecs(
	ctx context.Context,
	keySpecs map[string]string,
	fetchGitHub, fetchGitLab KeyFetchFunc,
) (map[string][]string, error) {
	labels := make([]string, 0, len(keySpecs))
	for label := range keySpecs {
		labels = append(labels, label)
	}

	sort.Strings(labels)

	parsed := make(map[string]config.BastionKeySpec, len(keySpecs))

	for _, label := range labels {
		spec, err := config.ParseBastionKeySpec(keySpecs[label])
		if err != nil {
			return nil, config.BastionKeyEntryError(label, keySpecs[label], err)
		}

		parsed[label] = spec
	}

	log := logger.Get()
	resolved := make(map[string][]string, len(keySpecs))

	for _, label := range labels {
		spec := parsed[label]

		fetch := fetchGitHub
		provider := "GitHub"

		switch spec.Source {
		case config.BastionKeyLiteral:
			resolved[label] = []string{spec.Value}

			continue
		case config.BastionKeyGitLab:
			fetch = fetchGitLab
			provider = "GitLab"
		case config.BastionKeyGitHub:
		}

		keys, err := fetch(ctx, spec.Value)
		if err != nil {
			log.Warnw("Failed to fetch "+provider+" keys, skipping",
				"label", label, "username", spec.Value, "error", err)

			continue
		}

		resolved[label] = keys
	}

	return resolved, nil
}
