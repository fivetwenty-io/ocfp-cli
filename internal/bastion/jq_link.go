package bastion

import (
	"context"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/provision"
)

// syncJQBrewLink is the form init runs. The link is a convenience, so a
// bastion that refuses it, for example because sudo asks for a password, still
// goes on to the rest of init. The next run converges whatever is left.
func (m *Manager) syncJQBrewLink(ctx context.Context) error {
	err := m.convergeJQBrewLink(ctx)
	if err != nil {
		m.log.Warnw("Could not converge the Homebrew jq link, continuing without it",
			"error", err)
	}

	return nil
}

// convergeJQBrewLink makes sure /usr/local/bin/jq points at Homebrew's jq. The
// script decides whether anything needs doing, so a correct link is left as it
// is, and a real binary or a foreign symlink is left alone with a warning.
//
// Unlike the cloud-init drop-ins, this applies to every provider, because the
// guest scripts that need jq run on AWS and GCP bastions too.
func (m *Manager) convergeJQBrewLink(ctx context.Context) error {
	if m.options != nil && m.options.DryRun {
		m.log.Infow("DRY RUN: Would converge the Homebrew jq link on the bastion")

		return nil
	}

	brewMgr := provision.NewBrewManager(m.config.Provider, m.config)

	return m.executeScript(ctx, brewMgr.GenerateJQBrewLinkScript(ctx), "jq-brew-link")
}
