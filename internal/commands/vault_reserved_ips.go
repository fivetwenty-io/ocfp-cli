package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/bastion"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
)

// newVaultReservedIPsCmd creates the `vault reserved-ips` subcommand tree.
//
// Reallocation gets its own verb because of what it costs: a reserved
// address is where a service is already deployed, so moving one recreates
// the VM holding it. `vault populate` therefore keeps whatever addresses
// vault records and reports divergence; changing them happens here, after
// an operator has read the report.
func newVaultReservedIPsCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "reserved-ips",
		Short: "Inspect and migrate reserved IP assignments",
		Long: `Inspect and migrate a bloc's reserved IP assignments.

Reserved IPs are derived from a compile-time offset table when a bloc is
provisioned. From then on vault records where those services physically
live. When the table changes, the derivation and the record diverge:
'status' reports that divergence and 'migrate' applies it.`,
	}

	cmd.AddCommand(newVaultReservedIPsStatusCmd())
	cmd.AddCommand(newVaultReservedIPsMigrateCmd())

	return cmd
}

// newVaultReservedIPsStatusCmd creates the read-only report subcommand.
func newVaultReservedIPsStatusCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report reserved IPs that differ from this build's table",
		Long: `Report every reserved IP whose recorded address differs from the
address this build derives. Writes nothing.`,
		Example: `  # Review divergence before deciding to migrate
  ocfp vault reserved-ips status`,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runVaultReservedIPsStatus(os.Stdout)
		},
	}

	return cmd
}

// newVaultReservedIPsMigrateCmd creates the apply subcommand.
func newVaultReservedIPsMigrateCmd() *cobra.Command {
	var assumeYes bool

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Move reserved IPs onto this build's derived addresses",
		Long: `Move every reserved IP onto the address this build derives.

Services already deployed at the recorded addresses must be recreated
afterwards; BOSH will not move a VM to a new static IP on its own. Review
'ocfp vault reserved-ips status' first.`,
		Example: `  # Show what would move, then confirm
  ocfp vault reserved-ips migrate

  # Non-interactive
  ocfp vault reserved-ips migrate --yes`,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runVaultReservedIPsMigrate(os.Stdout, assumeYes)
		},
	}

	cmd.Flags().BoolVar(&assumeYes, "yes", false, "skip the confirmation prompt")

	return cmd
}

// runVaultReservedIPsStatus reports divergence without writing anything.
func runVaultReservedIPsStatus(w io.Writer) error {
	report, err := computeReservedIPReport(false)
	if err != nil {
		return err
	}

	if !report.HasDrift() && len(report.Schemes) == 0 {
		_, _ = fmt.Fprintln(w, "reserved IPs match this build's table; nothing to migrate")

		return nil
	}

	vault.WriteReservedIPReport(w, report)

	return nil
}

// runVaultReservedIPsMigrate reports what would move, confirms, then applies.
func runVaultReservedIPsMigrate(w io.Writer, assumeYes bool) error {
	log := logger.Get()

	report, err := computeReservedIPReport(false)
	if err != nil {
		return err
	}

	if !report.HasDrift() {
		_, _ = fmt.Fprintln(w, "reserved IPs match this build's table; nothing to migrate")

		return nil
	}

	vault.WriteReservedIPReport(w, report)

	if !assumeYes && !confirmReservedIPMigration(w, len(report.Drifts), log) {
		return nil
	}

	applied, err := computeReservedIPReport(true)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "\nmoved %d reserved IP(s).\n", len(applied.Drifts))
	_, _ = fmt.Fprintln(w,
		"Recreate the affected instances so they pick up their new addresses"+
			" (`bosh -d <deployment> recreate`), then redeploy.")

	return nil
}

// computeReservedIPReport runs the derivation against the recorded addresses,
// applying it when apply is set.
func computeReservedIPReport(apply bool) (vault.ReservedIPReport, error) {
	empty := vault.ReservedIPReport{Drifts: nil, Schemes: nil}

	manager, err := loadConfigAndManager()
	if err != nil {
		return empty, err
	}

	defer func() { _ = manager.Close() }()

	progress := &bastion.ProvisioningProgress{
		CurrentStep:    "",
		CompletedSteps: 0,
		TotalSteps:     0,
	}
	reporter := bastion.NewProgressReporter(os.Stdout, bastion.SelectOutputMode(os.Stdout), progress)

	report, err := manager.ReservedIPs(&vault.ReservedIPOptions{
		Apply:            apply,
		ProgressReporter: reporter,
	})
	if err != nil {
		return empty, fmt.Errorf("failed to evaluate reserved IPs: %w", err)
	}

	return report, nil
}

// confirmReservedIPMigration prompts before addresses are moved.
func confirmReservedIPMigration(w io.Writer, count int, log logger.Logger) bool {
	_, err := fmt.Fprintf(w,
		"\nThis moves %d reserved IP(s) and requires recreating the instances that hold them. Continue? [y/N]: ",
		count)
	if err != nil {
		log.Errorw("failed to write confirmation prompt", "error", err)

		return false
	}

	var response string

	_, _ = fmt.Scanln(&response)

	if !strings.HasPrefix(strings.ToLower(response), "y") {
		log.Info("Reserved IP migration cancelled by user")

		return false
	}

	return true
}
