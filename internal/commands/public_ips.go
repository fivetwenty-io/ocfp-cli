package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// IndexParsingShift is the multiplier used when parsing multi-digit indices.
	IndexParsingShift = 10

	// NonNumericIndexValue is a sentinel value used for non-numeric indices during sorting.
	NonNumericIndexValue = 1 << 30
)

// NewPublicIPsCmd creates the public-ips root command.
func NewPublicIPsCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "public-ips",
		Short: "Manage public IPs",
		Long:  "List and manage public IP addresses managed by OCFP.",
	}

	cmd.AddCommand(newPublicIPsListCmd())

	return cmd
}

func newPublicIPsListCmd() *cobra.Command {
	var output string

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List public IPs",
		Long:  "List current public IPs for the selected bloc (STACKIT only).",
		RunE: func(_cmd *cobra.Command, _args []string) error {
			return runPublicIPsList(output)
		},
	}

	cmd.Flags().StringVar(&output, "output", OutputTable, "output format (table|json|yaml)")
	_ = viper.BindPFlag("public_ips.output", cmd.Flags().Lookup("output"))

	return cmd
}

func runPublicIPsList(output string) error {
	ctx := context.Background()
	_ = logger.Get() // ensure logger initialized; UX goes to stdout

	cfg, err := loadPublicIPsConfig()
	if err != nil {
		return err
	}

	lister, err := setupStackitPublicIPLister(ctx, cfg)
	if err != nil {
		return err
	}

	defer func() { _ = lister.provider.Cleanup(ctx) }()

	ips, err := fetchAndSortPublicIPs(ctx, lister.lister, cfg.Name)
	if err != nil {
		return err
	}

	return renderPublicIPsTable(ips, cfg.Name, output)
}

func loadPublicIPsConfig() (*config.Config, error) {
	blocName := viper.GetString("bloc")
	cfgFile := viper.GetString("config")

	if blocName == "" {
		return nil, ErrBlocIsRequired
	}

	cfg, err := config.LoadWithParams(cfgFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	if strings.ToLower(cfg.Provider) != "stackit" {
		return nil, ErrPublicIPListingSupportedForStackitOnly
	}

	return cfg, nil
}

type stackitPublicIPLister interface {
	ListPublicIPsWithFilters(ctx context.Context, filters map[string]string) ([]*cpi.PublicIP, error)
}

type publicIPListerWrapper struct {
	provider cpi.Provider
	lister   stackitPublicIPLister
}

func setupStackitPublicIPLister(ctx context.Context, cfg *config.Config) (*publicIPListerWrapper, error) {
	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to get provider: %w", err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize provider: %w", err)
	}

	network := provider.NetworkManager()
	if network == nil {
		return nil, ErrNetworkManagerNotAvailableForProvider(cfg.Provider)
	}

	stackitLister, ok := network.(stackitPublicIPLister)
	if !ok {
		return nil, ErrProviderDoesNotSupportPublicIPListing
	}

	return &publicIPListerWrapper{
		provider: provider,
		lister:   stackitLister,
	}, nil
}

func fetchAndSortPublicIPs(ctx context.Context, lister stackitPublicIPLister, blocName string) ([]*cpi.PublicIP, error) {
	filters := map[string]string{
		"label:managed-by": "ocfp",
		"label:bloc":       blocName,
	}

	ips, err := lister.ListPublicIPsWithFilters(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("failed to list public IPs: %w", err)
	}

	sortPublicIPs(ips)

	return ips, nil
}

func sortPublicIPs(ips []*cpi.PublicIP) {
	sort.Slice(ips, func(iIndex, jIndex int) bool {
		jobI, jobJ := ips[iIndex].Job, ips[jIndex].Job
		if jobI == jobJ {
			indexI, indexJ := parseIndex(ips[iIndex].Index), parseIndex(ips[jIndex].Index)
			if indexI == indexJ {
				return ips[iIndex].Index < ips[jIndex].Index
			}

			return indexI < indexJ
		}

		return jobI < jobJ
	})
}

func renderPublicIPsTable(ips []*cpi.PublicIP, blocName, output string) error {
	table := buildPublicIPsTable(ips, blocName)

	if output == "" {
		output = OutputTable
	}

	err := ui.Render(table, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render public IPs output: %w", err)
	}

	return nil
}

func buildPublicIPsTable(ips []*cpi.PublicIP, blocName string) *ui.Table {
	title := "Public IPs — bloc " + blocName
	table := &ui.Table{
		Title:    title,
		Summary:  "",
		Sections: nil,
	}

	rows := make([][]string, 0, len(ips))
	for _, ip := range ips {
		rows = append(rows, []string{ip.Job, ip.Index, ip.Address, ip.ID, ip.Name, ip.NetworkID, formatLabels(ip.Labels)})
	}

	table.Sections = append(table.Sections, ui.Section{Title: "IPs", Headers: []string{"JOB", "INDEX", "ADDRESS", "ID", "NAME", "NETWORK", "LABELS"}, Rows: rows})

	return table
}

// drop direct table rendering; handled by ui.Render above

func formatLabels(labelsMap map[string]string) string {
	if len(labelsMap) == 0 {
		return ""
	}
	// Stable order
	keys := make([]string, 0, len(labelsMap))
	for k := range labelsMap {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, labelsMap[k]))
	}

	return strings.Join(parts, ",")
}

func parseIndex(s string) int {
	// fallback-friendly atoi
	var index int

	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return NonNumericIndexValue // non-numeric go to end
		}

		index = index*IndexParsingShift + int(ch-'0')
	}

	return index
}
