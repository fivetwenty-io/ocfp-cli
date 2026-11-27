package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/fivetwenty-io/ocfp-cli/internal/cloudflare"
	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
	"github.com/fivetwenty-io/ocfp-cli/internal/vault"
)

// dnsChallengeTokenKey is the key that holds the token in its leaf.
const dnsChallengeTokenKey = "token"

// dnsChallengeLeaf is the leaf's name beside the first-run code.
const dnsChallengeLeaf = "dns-challenge"

// dnsChallengeTokenPrefix starts the name of the token in Cloudflare, so an
// operator can tell it from every other token in the dashboard.
const dnsChallengeTokenPrefix = "ocfp-dns-challenge-" // #nosec G101 -- a token name prefix, not a credential

// dnsChallengeDeps is what the DNS challenge step reaches outside itself.
// A test passes a fake doer, so no test touches the network.
type dnsChallengeDeps struct {
	// doer carries the trace call and every Cloudflare API call. A nil
	// doer means a default client with a timeout.
	doer interface {
		Do(req *http.Request) (*http.Response, error)
	}
}

// dnsChallengePath returns the leaf that holds the DNS challenge token. It
// sits beside the first-run code in the bloc's operator tree, which only
// the admin policy reads.
func dnsChallengePath(bloc string) string {
	return path.Join(path.Dir(vault.FirstRunCodePath(bloc)), dnsChallengeLeaf)
}

// runPopulateDNSChallenge is the DNS challenge part of 'ocfp vault
// populate'. It makes the token that lets the managing system obtain its
// certificate through a DNS challenge, pins it to the bloc's egress range,
// and stores it at the operator leaf. It prints the range that it used and
// never the token.
//
// A bloc with no Cloudflare zone, or whose own Cloudflare token can't be
// read, needs nothing here, and the step says so and returns. A leaf that
// already holds a token is left alone, so a rerun never makes a second
// token. A dry run makes no call and no write.
func runPopulateDNSChallenge(
	ctx context.Context,
	w io.Writer,
	store vault.SafeInterface,
	cfg *config.Config,
	bloc string,
	dryRun bool,
	deps dnsChallengeDeps,
) error {
	if cfg == nil || cfg.Cloudflare == nil || strings.TrimSpace(cfg.Cloudflare.Zone) == "" {
		logger.Get().Debugw("No Cloudflare zone is configured, so no DNS challenge token is made", "bloc", bloc)

		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	leaf := dnsChallengePath(bloc)

	stored, err := dnsChallengeStored(store, leaf)
	if err != nil {
		return err
	}

	if stored {
		return printDNS(w, "The DNS challenge token is already stored at %s, so it was left as it is.\n", leaf)
	}

	if dryRun {
		return printDNS(w, "DRY RUN: Would make a DNS challenge token for zone %s, pinned to %s, and store it at %s\n",
			cfg.Cloudflare.Zone, dnsEgressPlan(cfg), leaf)
	}

	blocToken := vault.ResolveSecretRef(store, cfg.Cloudflare.APIToken, cfg.Cloudflare.APITokenVaultPath)
	if blocToken == "" {
		return printDNS(w, "The bloc's Cloudflare token couldn't be read, so no DNS challenge token was made. "+
			"Make one in the Cloudflare dashboard with DNS edit on zone %s, and store it as the key %s at %s.\n",
			cfg.Cloudflare.Zone, dnsChallengeTokenKey, leaf)
	}

	prefixes, err := config.ParseEgressRanges(cfg.EgressRanges)
	if err != nil {
		return fmt.Errorf("reading the bloc's egress range: %w", err)
	}

	egress, err := cloudflare.ResolveEgress(ctx, deps.doer, prefixes)
	if err != nil {
		return fmt.Errorf("pinning the DNS challenge token: %w", err)
	}

	err = printDNS(w, "The DNS challenge token will accept requests only from %s, which came from %s.\n",
		strings.Join(egress.Ranges, ", "), egressSourceText(egress.Source))
	if err != nil {
		return err
	}

	return createAndStoreDNSChallenge(ctx, w, store, cfg, bloc, blocToken, leaf, egress, deps)
}

// createAndStoreDNSChallenge makes the token in Cloudflare and stores it.
func createAndStoreDNSChallenge(
	ctx context.Context,
	w io.Writer,
	store vault.SafeInterface,
	cfg *config.Config,
	bloc, blocToken, leaf string,
	egress cloudflare.Egress,
	deps dnsChallengeDeps,
) error {
	client := cloudflare.NewClient(blocToken, deps.doer)

	_, zoneID, err := client.ResolveAccountAndZone(ctx, cfg.Cloudflare.Zone)
	if err != nil {
		return fmt.Errorf("finding the zone for the DNS challenge token: %w", err)
	}

	name := dnsChallengeTokenPrefix + bloc

	value, err := client.CreateDNSChallengeToken(ctx, name, zoneID, egress.Ranges)
	if err != nil {
		return fmt.Errorf("making the DNS challenge token: %w", err)
	}

	err = store.SetMultiple(leaf, map[string]interface{}{dnsChallengeTokenKey: value})
	if err != nil {
		return fmt.Errorf("the DNS challenge token %q was made in Cloudflare but could not be stored at %s, "+
			"so delete that token in the Cloudflare dashboard and run populate again: %w", name, leaf, err)
	}

	return printDNS(w, "Stored the DNS challenge token at %s.\n", leaf)
}

// dnsChallengeStored reports whether the leaf already holds a token. A leaf
// that doesn't exist yet holds none.
func dnsChallengeStored(store vault.SafeInterface, leaf string) (bool, error) {
	if store == nil {
		return false, vault.ErrFirstRunNoVault
	}

	data, err := store.GetAll(leaf)
	if errors.Is(err, vault.ErrSecretNotFound) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading the DNS challenge leaf %s: %w", leaf, err)
	}

	value, _ := data[dnsChallengeTokenKey].(string)

	return strings.TrimSpace(value) != "", nil
}

// dnsEgressPlan says, for a dry run, where the filter would come from.
func dnsEgressPlan(cfg *config.Config) string {
	if len(cfg.EgressRanges) > 0 {
		return "the egress_ranges setting (" + strings.Join(cfg.EgressRanges, ", ") + ")"
	}

	return "this host's egress address, read from Cloudflare's trace endpoint"
}

// egressSourceText turns an egress source into words.
func egressSourceText(source string) string {
	if source == cloudflare.EgressFromSetting {
		return "the egress_ranges setting"
	}

	return "this host's address as Cloudflare's trace endpoint reported it"
}

// printDNS writes one line of the step's output.
func printDNS(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	if err != nil {
		return fmt.Errorf("printing the DNS challenge notice: %w", err)
	}

	return nil
}
