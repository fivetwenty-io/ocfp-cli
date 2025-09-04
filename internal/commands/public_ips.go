package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	yaml "gopkg.in/yaml.v3"
)

// NewPublicIPsCmd creates the public-ips root command
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
	log := logger.Get()

	blocName := viper.GetString("bloc_name")
	cfgFile := viper.GetString("config")

	if blocName == "" {
		return fmt.Errorf("bloc is required")
	}

	cfg, err := config.LoadWithParams(cfgFile, blocName)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if strings.ToLower(cfg.Provider) != "stackit" {
		return fmt.Errorf("public IP listing is currently supported for STACKIT only")
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

	s, ok := network.(stackitPublicIPLister)
	if !ok {
		return fmt.Errorf("provider does not support public IP listing")
	}

	filters := map[string]string{
		"label:managed-by": "ocfp",
		"label:bloc":       cfg.Name,
	}

	ips, err := s.ListPublicIPs(ctx, filters)
	if err != nil {
		return fmt.Errorf("failed to list public IPs: %w", err)
	}

	// Sort by job then numeric index
	sort.Slice(ips, func(i, j int) bool {
		ji, jj := ips[i].Job, ips[j].Job
		if ji == jj {
			ii, ij := parseIndex(ips[i].Index), parseIndex(ips[j].Index)
			if ii == ij {
				return ips[i].Index < ips[j].Index
			}
			return ii < ij
		}
		return ji < jj
	})

	switch strings.ToLower(output) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(ips)
	case "yaml":
		data, err := yaml.Marshal(ips)
		if err != nil {
			return err
		}
		fmt.Print(string(data))
		return nil
	case "table", "":
		fallthrough
	default:
		renderPublicIPsTable(ips)
		log.Info("Use --output json|yaml for machine-readable formats")
		return nil
	}
}

func renderPublicIPsTable(ips []*cpi.PublicIP) {
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"JOB", "INDEX", "ADDRESS", "ID", "NAME", "NETWORK", "LABELS"})
	table.SetAutoWrapText(false)

	for _, ip := range ips {
		labels := formatLabels(ip.Labels)
		table.Append([]string{
			ip.Job,
			ip.Index,
			ip.Address,
			ip.ID,
			ip.Name,
			ip.NetworkID,
			labels,
		})
	}

	table.Render()
}

func formatLabels(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	// Stable order
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, m[k]))
	}
	return strings.Join(parts, ",")
}

func parseIndex(s string) int {
	// fallback-friendly atoi
	var n int
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 1 << 30 // non-numeric go to end
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
