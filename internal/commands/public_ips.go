package commands

import (
	"context"
	"fmt"
	"errors"
	"sort"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewPublicIPsCmd creates the public-ips root command.
func NewPublicIPsCmd() *cobra.Command {
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

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List public IPs",
		Long:  "List current public IPs for the selected bloc (STACKIT only).",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPublicIPsList(cmd, output)
		},
	}

	cmd.Flags().StringVar(&output, "output", "table", "output format (table|json|yaml)")
	_ = viper.BindPFlag("public_ips.output", cmd.Flags().Lookup("output"))

	return cmd
}

func runPublicIPsList(cmd *cobra.Command, output string) error {
	ctx := context.Background()
	_ = logger.Get() // ensure logger initialized; UX goes to stdout

	blocName := viper.GetString("bloc_name")
	cfgFile := viper.GetString("config")

	if blocName == "" {
		return errors.New("bloc is required")
	}

	cfg, err := config.LoadWithParams(cfgFile, blocName)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if strings.ToLower(cfg.Provider) != "stackit" {
		return errors.New("public IP listing is currently supported for STACKIT only")
	}

	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return fmt.Errorf("failed to get provider: %w", err)
	}

	if err := provider.Initialize(ctx, cfg); err != nil {
		return fmt.Errorf("failed to initialize provider: %w", err)
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	network := provider.Network()
	if network == nil {
		return fmt.Errorf("network manager not available for provider %s", cfg.Provider)
	}

	// STACKIT-specific public IP lister
	type stackitPublicIPLister interface {
		ListPublicIPs(ctx context.Context, filters map[string]string) ([]*cpi.PublicIP, error)
	}

	stackitLister, ok := network.(stackitPublicIPLister)
	if !ok {
		return errors.New("provider does not support public IP listing")
	}

	filters := map[string]string{
		"label:managed-by": "ocfp",
		"label:bloc":       cfg.Name,
	}

	ips, err := stackitLister.ListPublicIPs(ctx, filters)
	if err != nil {
		return fmt.Errorf("failed to list public IPs: %w", err)
	}

	// Sort by job then numeric index
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

	// Build a UI table so we can consistently render table/json/yaml
	title := "Public IPs — bloc " + cfg.Name
	t := &ui.Table{Title: title}

	rows := make([][]string, 0, len(ips))
	for _, ip := range ips {
		rows = append(rows, []string{ip.Job, ip.Index, ip.Address, ip.ID, ip.Name, ip.NetworkID, formatLabels(ip.Labels)})
	}

	t.Sections = append(t.Sections, ui.Section{Title: "IPs", Headers: []string{"JOB", "INDEX", "ADDRESS", "ID", "NAME", "NETWORK", "LABELS"}, Rows: rows})

	if output == "" {
		output = "table"
	}

	return ui.Render(t, strings.ToLower(output))
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
			return 1 << 30 // non-numeric go to end
		}

		index = index*10 + int(ch-'0')
	}

	return index
}
