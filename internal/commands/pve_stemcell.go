package commands

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"

	"github.com/ocfp/ocfp-cli-go/internal/pve/stemcell"
	"github.com/spf13/cobra"
)

// stemcellUploadBuilder constructs the RunBosh and SHA1Fetcher closures for
// `ocfp pve stemcell upload`. Factored out so tests can inject fakes without
// shelling out.
type stemcellUploadBuilder func(boshEnv, sha1Override string) (stemcell.RunBosh, stemcell.SHA1Fetcher)

// defaultStemcellUploadBuilder returns production closures:
//   - RunBosh shells out to `bosh -e <env> <args...>` via exec.Command (no shell).
//   - SHA1Fetcher calls stemcell.FetchSHA1 via DefaultHTTPClient unless sha1Override
//     is non-empty, in which case the fetcher returns sha1Override immediately.
func defaultStemcellUploadBuilder(boshEnv, sha1Override string) (stemcell.RunBosh, stemcell.SHA1Fetcher) {
	runBosh := func(ctx context.Context, args ...string) ([]byte, error) {
		full := append([]string{"-e", boshEnv}, args...)
		cmd := exec.CommandContext(ctx, "bosh", full...) //nolint:gosec // args come from validated flag + positional inputs

		return cmd.Output()
	}

	var fetchSHA1 stemcell.SHA1Fetcher

	if sha1Override != "" {
		fetchSHA1 = func(_ context.Context, _, _ string) (string, error) {
			return sha1Override, nil
		}
	} else {
		fetchSHA1 = func(ctx context.Context, name, version string) (string, error) {
			return stemcell.FetchSHA1(ctx, http.DefaultClient, name, version)
		}
	}

	return runBosh, fetchSHA1
}

// stemcellUploadFlags holds resolved inputs for `ocfp pve stemcell upload`.
type stemcellUploadFlags struct {
	env  string
	sha1 string
}

// NewPVEStemcellCmd returns the `ocfp pve stemcell` parent subcommand.
// All stemcell-specific operations hang off this.
func NewPVEStemcellCmd() *cobra.Command {
	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional cobra fields
		Use:   "stemcell",
		Short: "Stemcell operations for PVE BOSH directors",
		Long:  "Subcommands for managing BOSH stemcells on PVE-hosted directors.",
	}

	cmd.AddCommand(NewPVEStemcellUploadCmd())

	return cmd
}

// NewPVEStemcellUploadCmd returns the `ocfp pve stemcell upload <name> <version> <url>` command.
//
// Usage: ocfp pve stemcell upload <name> <version> <url>
//
//   - name:    full stemcell name, e.g. "bosh-openstack-kvm-ubuntu-noble-go_agent"
//   - version: exact version string, e.g. "1.584"
//   - url:     download URL for the stemcell tarball
//
// Flags:
//
//	--env      BOSH environment alias (default "pve")
//	--sha1     optional SHA1 override; skips bosh.io lookup when set
//
// Behavior:
//   - If the stemcell is already present on the director, prints "already uploaded; skipping"
//     and exits 0.
//   - Otherwise fetches the SHA1 from bosh.io (or uses --sha1) and runs
//     `bosh -e <env> upload-stemcell --sha1 <sha1> <url>`, printing progress to stdout.
func NewPVEStemcellUploadCmd() *cobra.Command {
	return newPVEStemcellUploadCmdWithBuilder(defaultStemcellUploadBuilder)
}

// newPVEStemcellUploadCmdWithBuilder is the internal constructor that accepts a
// stemcellUploadBuilder. Tests inject fakes to control bosh and HTTP behaviour.
func newPVEStemcellUploadCmdWithBuilder(builder stemcellUploadBuilder) *cobra.Command {
	f := &stemcellUploadFlags{
		env: "pve",
	}

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional cobra fields
		Use:   "upload <name> <version> <url>",
		Short: "Idempotently upload a stemcell to the PVE BOSH director",
		Long: `Idempotently upload a BOSH stemcell to the PVE director.

The command checks whether the stemcell is already present via 'bosh stemcells
--json'. When already uploaded it exits 0 immediately with no upload traffic.

When the stemcell is absent it fetches the SHA1 checksum from bosh.io
(https://bosh.io/api/v1/stemcells/<name>) and runs:

  bosh -e <env> upload-stemcell --sha1 <sha1> <url>

Pass --sha1 to supply a known checksum and skip the bosh.io HTTP call.`,
		Example: `  ocfp pve stemcell upload bosh-openstack-kvm-ubuntu-noble-go_agent 1.584 \
      https://bosh.io/d/stemcells/bosh-openstack-kvm-ubuntu-noble-go_agent?v=1.584

  ocfp pve stemcell upload bosh-openstack-kvm-ubuntu-noble-go_agent 1.584 \
      https://storage.example.com/stemcells/noble-1.584.tgz \
      --sha1 abc123deadbeef`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			return runPVEStemcellUpload(cmd, f, args[0], args[1], args[2], builder)
		},
	}

	cmd.Flags().StringVar(&f.env, "env", "pve", "BOSH environment alias")
	cmd.Flags().StringVar(&f.sha1, "sha1", "", "SHA1 checksum override (skips bosh.io lookup)")

	return cmd
}

// runPVEStemcellUpload implements the stemcell upload logic.
//
// Steps:
//  1. Build RunBosh + SHA1Fetcher closures via builder.
//  2. Call stemcell.EnsureStemcell with the closures.
//  3. On nil error from EnsureStemcell, determine whether the stemcell was already
//     present (IsStemcellUploaded pre-check) and print an appropriate message.
//
// Inputs:
//   - cmd:     cobra command; stdout/stderr routed through cmd.OutOrStdout/ErrOrStderr.
//   - f:       resolved flag values; env defaults to "pve".
//   - name:    validated non-empty by cobra.ExactArgs(3).
//   - version: validated non-empty by cobra.ExactArgs(3).
//   - url:     validated non-empty by cobra.ExactArgs(3).
//   - builder: never nil; defaultStemcellUploadBuilder or test-injected.
//
// Failure modes:
//   - name/version/url empty → error from EnsureStemcell before any bosh call.
//   - bosh stemcells check fails → propagated error.
//   - bosh.io fetch fails (no --sha1) → propagated error.
//   - bosh upload-stemcell fails → propagated error.
func runPVEStemcellUpload(
	cmd *cobra.Command,
	f *stemcellUploadFlags,
	name, version, url string,
	builder stemcellUploadBuilder,
) error {
	ctx := context.Background()

	runBosh, fetchSHA1 := builder(f.env, f.sha1)

	// Pre-check to provide a clear "already uploaded" message before EnsureStemcell
	// would silently return nil. This avoids the ambiguity of a successful no-op.
	uploaded, err := stemcell.IsStemcellUploaded(ctx, runBosh, name, version)
	if err != nil {
		return fmt.Errorf("pve stemcell upload: check existing stemcells: %w", err)
	}

	if uploaded {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "already uploaded; skipping  name=%s version=%s\n", name, version)

		return nil
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "uploading stemcell  name=%s version=%s env=%s\n", name, version, f.env)

	if err := stemcell.EnsureStemcell(ctx, runBosh, fetchSHA1, name, version, url); err != nil {
		return fmt.Errorf("pve stemcell upload: %w", err)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "upload complete  name=%s version=%s\n", name, version)

	return nil
}
