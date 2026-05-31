package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/pve/probes"
	"github.com/spf13/cobra"
)

const (
	// pveDefaultProbePort is the BOSH director API port dialled by defaultProbeBuilder.
	pveDefaultProbePort = 25555
	// pveDefaultProbeTimeout is the TCP dial timeout used by defaultProbeBuilder.
	pveDefaultProbeTimeout = 5 * time.Second
)

// probeBuilder constructs the slice of Probe instances for a given bloc config.
// Factored out to allow test injection of fake probes.
type probeBuilder func(boshEnv, deployment, directorIP string) []probes.Probe

// defaultProbeBuilder constructs the production probes for pve probe:
//   - UAAFlywayProbe: shells into database/0 via bosh ssh and checks Flyway history
//   - TCPDialProbe:   dials director IP:25555 with a 5 s timeout
//
// boshEnv is the BOSH environment alias (passed as -e to bosh).
// deployment is the BOSH deployment name (passed as -d to bosh); "cf" is the
// conventional name for the Cloud Foundry deployment on a PVE bloc director.
// directorIP is the BOSH director's IP used for the TCP reachability check.
func defaultProbeBuilder(boshEnv, deployment, directorIP string) []probes.Probe {
	uaa := &probes.UAAFlywayProbe{
		Env:        boshEnv,
		Deployment: deployment,
		Instance:   "database/0",
		RunBosh: func(ctx context.Context, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "bosh", args...) //nolint:gosec // args come from validated config
			cmd.Stderr = os.Stderr
			return cmd.Output()
		},
	}

	tcp := &probes.TCPDialProbe{
		Host:    directorIP,
		Port:    pveDefaultProbePort,
		Timeout: pveDefaultProbeTimeout,
		Label:   "bosh-director-api",
	}

	return []probes.Probe{uaa, tcp}
}

// probeFlags holds the resolved inputs for `ocfp pve probe`.
type probeFlags struct {
	boshEnv    string
	deployment string
	directorIP string
}

// NewPVEProbeCmd returns the `ocfp pve probe` cobra subcommand.
//
// Usage: ocfp pve probe <bloc>
//
// The command runs a set of pre-deploy health probes against the PVE bloc
// identified by <bloc>. Currently two probes execute in order:
//
//  1. uaa-flyway  — checks the UAA Flyway migration history table on database/0
//     via bosh ssh; fails when any schema_version row has success=0.
//  2. tcp-dial    — dials the BOSH director API port (25555) with a 5 s timeout.
//
// RunAll aborts on the first failure so the operator sees one actionable message.
//
// Required flags:
//
//	--bosh-env (-e)   BOSH environment alias
//	--deployment (-d) BOSH deployment name (default "cf")
//	--director-ip     BOSH director IP address
//
// Exit codes:
//
//	0  all probes OK
//	1  at least one probe failed (detail + remediation printed to stderr)
func NewPVEProbeCmd() *cobra.Command {
	return newPVEProbeCmdWithBuilder(defaultProbeBuilder)
}

// newPVEProbeCmdWithBuilder is the internal constructor that accepts a probeBuilder.
// Tests inject fake builders to control probe outcomes without real bosh/network calls.
func newPVEProbeCmdWithBuilder(builder probeBuilder) *cobra.Command {
	f := &probeFlags{
		deployment: "cf", // conventional CF deployment name on OCFP PVE directors
	}

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional cobra fields
		Use:   "probe <bloc>",
		Short: "Run pre-deploy health probes against a PVE bloc",
		Long: `Run pre-deploy health probes against the PVE BOSH director bloc.

Two probes execute in order:

  1. uaa-flyway  Checks the UAA Flyway migration history table on database/0
                 via bosh ssh. Fails when any schema_version row has success=0,
                 which causes UAA to crashloop on the next deploy. Skip conditions
                 (no PXC, no mysql binary, DB missing, fresh schema) are treated
                 as OK — they indicate CF is not yet deployed on this director.

  2. tcp-dial    Dials the BOSH director API (port 25555) from the bastion with
                 a 5-second timeout. Fails when the port is not reachable.

RunAll stops at the first failure and prints a remediation block. Fix the
reported issue, then re-run to confirm all probes pass before deploying.`,
		Example: `  ocfp pve probe lab-west -e lab -d cf --director-ip 10.128.0.2
  ocfp pve probe prod    -e prod --director-ip 10.0.0.2`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runPVEProbe(cmd, f, args[0], builder)
		},
	}

	cmd.Flags().StringVarP(&f.boshEnv, "bosh-env", "e", "", "BOSH environment alias (required)")
	cmd.Flags().StringVarP(&f.deployment, "deployment", "d", "cf", "BOSH deployment name")
	cmd.Flags().StringVar(&f.directorIP, "director-ip", "", "BOSH director IP address (required)")

	_ = cmd.MarkFlagRequired("bosh-env")
	_ = cmd.MarkFlagRequired("director-ip")

	return cmd
}

// runPVEProbe implements the probe logic.
//
// Steps:
//  1. Build probe list via builder(boshEnv, deployment, directorIP).
//  2. Call probes.RunAll with a background context.
//  3. Print OK or FAIL+remediation; return non-nil error on FAIL so cobra exits 1.
//
// Inputs:
//   - cmd: used for stdout/stderr writers (testable via cmd.SetOut/SetErr)
//   - f: resolved flag values; boshEnv and directorIP are non-empty (cobra enforces required)
//   - bloc: positional arg; validated non-empty by cobra.ExactArgs(1)
//   - builder: never nil; defaultProbeBuilder or test injected
//
// Failure modes:
//   - probes.RunAll returns !OK: prints FAIL block to stderr, returns error (exit 1)
//   - All probes OK: prints OK to stdout, returns nil (exit 0)
func runPVEProbe(cmd *cobra.Command, f *probeFlags, bloc string, builder probeBuilder) error {
	probeList := builder(f.boshEnv, f.deployment, f.directorIP)

	ctx := context.Background()
	result := probes.RunAll(ctx, probeList...)

	if result.OK {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "OK  bloc=%s detail=%q\n", bloc, result.Detail)
		return nil
	}

	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "FAIL  bloc=%s detail=%q\n", bloc, result.Detail)

	if result.Remediation != "" {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr())
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), result.Remediation)
	}

	return fmt.Errorf("probe failed for bloc %q: %s", bloc, result.Detail) //nolint:err113 // descriptive error, not caller-testable
}
