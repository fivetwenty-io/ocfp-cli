package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewEndpointsCmd creates the flat `endpoints` command (aliases `dns`,
// `domains`): a read-only, bloc-scoped report of every DNS-relevant
// hostname/IP fact across four sections (derived service FQDNs, Cloudflare
// service routes, ingress records, and the bastion), each with the EXPECTED
// IP (where applicable), ORIGIN (where traffic terminates today), and
// RESOLVED IP (a live lookup, skippable via --no-resolve) columns described
// in docs/networking/endpoints.md. No child subcommands — every input comes
// from the global --bloc/--config flags plus this command's own --output
// and --no-resolve.
func NewEndpointsCmd() *cobra.Command {
	var output string

	var noResolve bool

	//nolint:exhaustruct // Using zero values for optional cobra fields
	cmd := &cobra.Command{
		Use:     "endpoints",
		Aliases: []string{"dns", "domains"},
		Short:   "List DNS/endpoint facts for a bloc",
		Long: `List every DNS-relevant hostname/IP fact for the selected bloc across
four sections: derived service FQDNs, Cloudflare service routes, ingress
records, and the bastion. Each hostname-bearing row carries up to three
independent facts — EXPECTED IP (an allocation fact), ORIGIN (where traffic
terminates today, exact-match only), and RESOLVED IP (a live DNS lookup) —
that may or may not agree; see docs/networking/endpoints.md for the full
semantics. Use --no-resolve to skip live lookups entirely.`,
		RunE: func(_cmd *cobra.Command, _args []string) error {
			return runEndpoints(output, noResolve)
		},
	}

	cmd.Flags().StringVar(&output, "output", OutputTable, "output format (table|json|yaml)")
	cmd.Flags().BoolVar(&noResolve, "no-resolve", false, "skip live DNS lookups; RESOLVED IP columns stay blank")

	return cmd
}

// runEndpoints loads the bloc's config, builds the four-section table, and
// renders it in the requested format.
func runEndpoints(output string, noResolve bool) error {
	ctx := context.Background()
	_ = logger.Get() // ensure logger initialized; UX goes to stdout

	cfg, err := loadEndpointsConfig()
	if err != nil {
		return err
	}

	table := buildEndpointsTable(ctx, cfg, noResolve)

	if output == "" {
		output = OutputTable
	}

	err = ui.Render(table, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render endpoints output: %w", err)
	}

	return nil
}

// loadEndpointsConfig mirrors loadPublicIPsConfig minus the STACKIT-only
// provider restriction — endpoints reports on every provider, since
// Cloudflare/ingress/bastion config is provider-independent. Uses
// ErrBlocIsRequired (not the near-identical ErrBlocRequired), matching every
// other bloc-scoped listing command's config-loading convention.
func loadEndpointsConfig() (*config.Config, error) {
	blocName := viper.GetString("bloc")
	cfgFile := viper.GetString("config")

	if blocName == "" {
		return nil, ErrBlocIsRequired
	}

	cfg, err := config.LoadWithParams(cfgFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, nil
}
