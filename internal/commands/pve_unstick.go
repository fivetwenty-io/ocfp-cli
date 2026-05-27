package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
)

// boshVMsOutput is the top-level structure of `bosh vms --json` output.
type boshVMsOutput struct {
	Tables []boshVMsTable `json:"Tables"`
}

// boshVMsTable holds one result table from `bosh vms --json`.
type boshVMsTable struct {
	Rows []boshVMRow `json:"Rows"`
}

// boshVMRow is one row from the bosh vms JSON output.
// Field names match the lowercase snake_case keys that bosh CLI emits.
type boshVMRow struct {
	Instance string `json:"instance"`
	VMCID    string `json:"vm_cid"`
}

// ErrVMIDNonInteger is returned when the VMID string is not a valid integer.
var ErrVMIDNonInteger = fmt.Errorf("PVE VMID must be an integer (check that 'bosh vms' returned a numeric vm_cid)")

// ErrVMIDNonPositive is returned when the VMID is zero or negative.
var ErrVMIDNonPositive = fmt.Errorf("PVE VMID must be a positive integer (got zero or negative value)")

// coerceVMID parses raw into a positive integer VMID.
//
// Inputs:
//   - raw: any string, including adversarial input like "abc; rm -rf /"
//
// Failure modes:
//   - non-integer string → ErrVMIDNonInteger (wrapped with raw value for diagnostics)
//   - zero or negative → ErrVMIDNonPositive
//   - empty string → treated as non-integer, returns ErrVMIDNonInteger
func coerceVMID(raw string) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%w: got %q", ErrVMIDNonInteger, raw)
	}

	if v <= 0 {
		return 0, fmt.Errorf("%w: got %d", ErrVMIDNonPositive, v)
	}

	return v, nil
}

// resolveVMIDForInstance runs `bosh vms --json` (via exec.Command, no shell) and
// returns the VMID for the named instance.
//
// instanceRef accepts:
//   - "job/index" (e.g. "uaa/0")
//   - "job/<uuid>" (e.g. "diego-cell/abc-123")
//   - bare job name matching any index (e.g. "uaa")
//
// Failure modes:
//   - bosh vms exec failure → wrapped error
//   - JSON parse failure → wrapped error
//   - no matching instance → descriptive error with instance name and deployment
//   - VMID parse failure → delegated to coerceVMID
func resolveVMIDForInstance(ctx context.Context, boshEnv, boshDeployment, instanceRef string) (int, error) {
	args := []string{"-e", boshEnv, "-d", boshDeployment, "vms", "--json"}
	cmd := exec.CommandContext(ctx, "bosh", args...) //nolint:gosec // args are from trusted config/flags
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("bosh vms failed: %w", err)
	}

	var result boshVMsOutput
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		return 0, fmt.Errorf("failed to parse bosh vms output: %w", err)
	}

	rawCID := ""
	for _, table := range result.Tables {
		for _, row := range table.Rows {
			if matchesInstanceRef(row.Instance, instanceRef) {
				rawCID = row.VMCID
				break
			}
		}

		if rawCID != "" {
			break
		}
	}

	if rawCID == "" {
		return 0, fmt.Errorf("no VM CID found for instance %q in deployment %q — check `bosh vms -d %s`", //nolint:err113 // descriptive error, not caller-testable
			instanceRef, boshDeployment, boshDeployment)
	}

	return coerceVMID(rawCID)
}

// matchesInstanceRef returns true when boshInstance (e.g. "uaa/0" or "diego-cell/<uuid>")
// matches the user-supplied ref, which may be:
//   - exact match: "uaa/0"
//   - job-only match: "uaa" matches any "uaa/<anything>"
//   - job/uuid match: "diego-cell/abc-123"
func matchesInstanceRef(boshInstance, ref string) bool {
	if boshInstance == ref {
		return true
	}

	// Split bosh instance into job and index/uuid parts.
	job, _, found := strings.Cut(boshInstance, "/")
	if !found {
		return false
	}

	// Bare job name — match any index.
	if ref == job {
		return true
	}

	// ref is job/something — check prefix match.
	refJob, _, refHasSep := strings.Cut(ref, "/")
	if refHasSep && refJob == job {
		// Full job/index comparison already handled by exact-match above;
		// reach here only when the exact strings differed, so no match.
		return false
	}

	return false
}

// resolvePVEHostFromVars runs `bosh int <varsFile> --path=/pve_host` and returns
// the trimmed host string.
//
// varsFile is the path to the BOSH vars file containing pve_host.
// Failure mode: exec error or empty result → descriptive error.
func resolvePVEHostFromVars(ctx context.Context, varsFile string) (string, error) {
	cmd := exec.CommandContext(ctx, "bosh", "int", varsFile, "--path=/pve_host") //nolint:gosec // varsFile is operator-provided path
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to resolve pve_host from %q: %w", varsFile, err)
	}

	host := strings.TrimSpace(out.String())
	if host == "" {
		return "", fmt.Errorf("pve_host is empty in vars file %q", varsFile) //nolint:err113 // descriptive error, not caller-testable
	}

	return host, nil
}

// unstickAgent SSHes to pveHost and restarts bosh-agent on the VM identified by vmid
// via `qm guest exec`.
//
// sshUnsafe=true disables host-key checking and prints a WARNING to stderr.
//
// The probe script is passed as a single argument to /bin/sh -c, avoiding any
// shell escaping of individual systemctl arguments.
func unstickAgent(ctx context.Context, pveHost string, vmid int, sshUnsafe bool) error {
	log := logger.Get()

	sshArgs := []string{"-o", "BatchMode=yes"}

	if sshUnsafe {
		fmt.Fprintln(os.Stderr,
			"WARNING: OCFP_SSH_UNSAFE=1 — SSH host-key checking is disabled. Do not use in production.")

		sshArgs = append(sshArgs, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null")
	}

	target := "root@" + pveHost

	probe := "set -e; systemctl restart bosh-agent && sleep 3 && systemctl is-active bosh-agent"

	vmidStr := strconv.Itoa(vmid)

	// Argv form — vmid is a discrete integer string argument, never
	// interpolated into a shell command. Allocate a fresh slice so we
	// never alias sshArgs when it has spare capacity (the previous
	// `append(sshArgs, ...)` could silently mutate the caller's slice).
	suffix := []string{
		target,
		"qm", "guest", "exec", vmidStr, "--timeout", "20",
		"--", "/bin/sh", "-c", probe,
	}
	allArgs := make([]string, 0, len(sshArgs)+len(suffix))
	allArgs = append(allArgs, sshArgs...)
	allArgs = append(allArgs, suffix...)

	log.Infow("restarting bosh-agent via qemu-guest-agent", "pve_host", pveHost, "vmid", vmid)

	cmd := exec.CommandContext(ctx, "ssh", allArgs...) //nolint:gosec // args constructed from validated integer + trusted config
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	combined := out.String()

	// qm guest exec returns JSON; PVE signals success via "exitcode" : 0 in the output.
	// Check both the ssh exit code and the PVE-level exit code.
	if err != nil || !strings.Contains(combined, `"exitcode" : 0`) {
		return fmt.Errorf("qm guest exec failed (ssh rc: %v):\n%s", err, combined) //nolint:err113 // descriptive error, not caller-testable
	}

	if !strings.Contains(combined, "active") {
		return fmt.Errorf("bosh-agent did not report active after restart:\n%s", combined) //nolint:err113 // descriptive error, not caller-testable
	}

	log.Info("bosh-agent active")

	return nil
}

// unstickFlags holds resolved inputs for `ocfp pve unstick`.
type unstickFlags struct {
	boshEnv        string
	boshDeployment string
	varsFile       string
}

// NewPVECmd returns the `ocfp pve` parent command.
// All PVE-specific subcommands hang off this.
func NewPVECmd() *cobra.Command {
	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional cobra fields
		Use:   "pve",
		Short: "Proxmox VE (PVE) operations",
		Long:  "Subcommands for operating on Proxmox VE infrastructure managed by OCFP.",
	}

	cmd.AddCommand(NewPVEUnstickCmd())
	cmd.AddCommand(NewPVEProbeCmd())
	cmd.AddCommand(NewPVEStemcellCmd())

	return cmd
}

// NewPVEUnstickCmd returns the `ocfp pve unstick <instance>` cobra command.
//
// Usage: ocfp pve unstick <instance>
//
//	instance: bosh-style identifier, e.g. "uaa/0" or "diego-cell/<uuid>"
//
// Required flags:
//
//	--bosh-env (-e)        BOSH environment alias
//	--bosh-deployment (-d) BOSH deployment name
//	--vars-file            path to BOSH vars file containing pve_host
//
// Environment:
//
//	OCFP_SSH_UNSAFE=1  disables SSH host-key checking (prints WARNING)
func NewPVEUnstickCmd() *cobra.Command {
	f := &unstickFlags{}

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional cobra fields
		Use:   "unstick <instance>",
		Short: "Force-restart the BOSH agent on a wedged PVE instance",
		Long: `Force-restart the BOSH agent on a PVE-hosted instance that has wedged.

When a BOSH agent stops replying to RPC calls but keeps sending heartbeats,
bosh ssh is unusable because it goes through the same wedged channel. This
command bypasses BOSH entirely: it resolves the PVE VMID from 'bosh vms',
SSHes into the PVE host, and issues 'qm guest exec <vmid> -- systemctl restart
bosh-agent' directly inside the guest VM.

Environment variables:
  OCFP_SSH_UNSAFE=1   Disable SSH host-key checking. WARNING: exposes the
                      connection to MITM attacks. Never use in production.`,
		Example: `  ocfp pve unstick uaa/0 -e my-bosh -d cf --vars-file ~/vars.yml
  ocfp pve unstick diego-cell/0 -e bosh -d cf --vars-file /ocfp/vars.yml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			return runPVEUnstick(cmd.Context(), f, args[0])
		},
	}

	cmd.Flags().StringVarP(&f.boshEnv, "bosh-env", "e", "", "BOSH environment alias (required)")
	cmd.Flags().StringVarP(&f.boshDeployment, "bosh-deployment", "d", "", "BOSH deployment name (required)")
	cmd.Flags().StringVar(&f.varsFile, "vars-file", "", "path to BOSH vars file containing pve_host (required)")

	_ = cmd.MarkFlagRequired("bosh-env")
	_ = cmd.MarkFlagRequired("bosh-deployment")
	_ = cmd.MarkFlagRequired("vars-file")

	return cmd
}

// runPVEUnstick implements the unstick logic.
//
// Steps:
//  1. Resolve VMID from `bosh vms --json`
//  2. Resolve PVE host from vars file via `bosh int`
//  3. SSH to PVE host and restart bosh-agent via qm guest exec
func runPVEUnstick(ctx context.Context, f *unstickFlags, instanceRef string) error {
	log := logger.Get()

	log.Infow("unstick-agent", "instance", instanceRef, "deployment", f.boshDeployment)

	vmid, err := resolveVMIDForInstance(ctx, f.boshEnv, f.boshDeployment, instanceRef)
	if err != nil {
		return err
	}

	log.Infow("resolved VMID", "instance", instanceRef, "vmid", vmid)

	pveHost, err := resolvePVEHostFromVars(ctx, f.varsFile)
	if err != nil {
		return err
	}

	log.Infow("resolved PVE host", "host", pveHost)

	sshUnsafe := os.Getenv("OCFP_SSH_UNSAFE") == "1"

	return unstickAgent(ctx, pveHost, vmid, sshUnsafe)
}
