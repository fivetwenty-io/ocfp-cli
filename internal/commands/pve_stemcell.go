package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/precompile"
	"github.com/ocfp/ocfp-cli-go/internal/pve/stemcell"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// ErrStemcellBlocRequired is returned when the artifacts blobstore must be
// resolved (a cache-miss upload) but no --bloc was supplied and none is set
// on the persistent --bloc flag / OCFP_BLOC.
var ErrStemcellBlocRequired = errors.New("--bloc is required to cache a stemcell in the artifacts blobstore")

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
		cmd := exec.CommandContext(ctx, "bosh", full...) // #nosec G204 -- args come from validated flag + positional inputs

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

// stemcellResolveFunc resolves upstreamURL into a cached artifacts-blobstore
// URL, uploading a copy on a cache miss (or when force is set), and returns
// the URL callers should hand to `bosh upload-stemcell` plus the cached
// tarball's sha256 (cache-integrity metadata only, not the bosh sha1 pin).
// Mirrors precompile.ResolveStemcell's signature with the client/bucket/
// endpoint already closed over by the builder that produced it.
type stemcellResolveFunc func(ctx context.Context, name, version, upstreamURL, expectedSHA1 string, force bool) (url, sha256hex string, err error)

// stemcellResolverBuilder resolves the artifacts blobstore for a stemcell
// upload run and returns a stemcellResolveFunc bound to it. Tests inject a
// fake to avoid touching S3/HTTP; defaultStemcellResolverBuilder is the
// production implementation.
type stemcellResolverBuilder func(ctx context.Context, f *stemcellUploadFlags) (stemcellResolveFunc, error)

// defaultStemcellResolverBuilder resolves the artifacts blobstore using the
// same flag-override-or-bootstrap-state precedence as `ocfp precompile cf`
// (see resolveBlobstoreOverride / precompileS3Client in precompile.go, both
// in this package), then returns a stemcellResolveFunc closing over the
// resulting S3 client, bucket, and endpoint.
//
// Inputs: f.bloc, resolved here from the flag or the persistent --bloc/
// OCFP_BLOC value if empty, is required — the artifacts bucket is named
// "<bloc>-ocf-bosh" per the canonical bucket list in internal/artifacts.
// f.blobAccessKey/f.blobSecretKey fall back to OCFP_BLOBSTORE_ACCESS_KEY/
// OCFP_BLOBSTORE_SECRET_KEY when unset, so secrets need not appear in argv.
//
// Failure modes: empty bloc (flag and persistent/env fallback both empty)
// returns ErrStemcellBlocRequired. Blobstore lookup/credential/TLS errors
// (missing bootstrap state, --blobstore-endpoint without both key flags,
// unreadable CA cert file, vault CA recovery failure, S3 client
// construction failure) are returned wrapped.
func defaultStemcellResolverBuilder(ctx context.Context, f *stemcellUploadFlags) (stemcellResolveFunc, error) {
	if f.bloc == "" {
		f.bloc = viper.GetString("bloc")
	}

	if f.bloc == "" {
		return nil, ErrStemcellBlocRequired
	}

	if f.blobAccessKey == "" {
		f.blobAccessKey = os.Getenv("OCFP_BLOBSTORE_ACCESS_KEY")
	}

	if f.blobSecretKey == "" {
		f.blobSecretKey = os.Getenv("OCFP_BLOBSTORE_SECRET_KEY")
	}

	lr, err := resolveBlobstoreOverride(ctx, f.bloc, f.blobEndpoint, f.blobAccessKey, f.blobSecretKey, f.blobCACertFile)
	if err != nil {
		return nil, err
	}

	s3c, ep, err := precompileS3Client(f.bloc, lr, f.insecureBlobstore)
	if err != nil {
		return nil, err
	}

	bucket := stemcellBucket(f.bloc)
	hc := &http.Client{Timeout: 30 * time.Minute}

	return func(ctx context.Context, name, version, upstreamURL, expectedSHA1 string, force bool) (string, string, error) {
		return precompile.ResolveStemcell(ctx, s3c, hc, bucket, ep, name, version, upstreamURL, expectedSHA1, force)
	}, nil
}

// stemcellUploadFlags holds resolved inputs for `ocfp pve stemcell upload`.
type stemcellUploadFlags struct {
	env  string
	sha1 string
	// bloc identifies the artifacts blobstore bucket ("<bloc>-ocf-bosh").
	// Read from the persistent --bloc flag / OCFP_BLOC when empty; only
	// consulted on a cache-miss upload (the already-uploaded pre-check needs
	// no blobstore access).
	bloc string
	// force bypasses the blobstore present-check, re-downloading from
	// upstreamURL and re-uploading even if a cached copy exists.
	force bool
	// Blobstore overrides. When blobEndpoint is set, the artifacts blobstore is
	// taken from these flags instead of bootstrap state — required on the
	// bastion, which holds no bootstrap state (only the operator machine does).
	blobEndpoint   string
	blobAccessKey  string
	blobSecretKey  string
	blobCACertFile string
	// insecureBlobstore is the explicit --insecure-blobstore opt-in. Required
	// whenever the blobstore is https with no CA cert available (state,
	// vault-recovered, or --blobstore-ca-cert-file); TLS verification is
	// otherwise never silently skipped.
	insecureBlobstore bool
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
//   - url:     upstream download URL for the stemcell tarball, used only on a
//     blobstore cache miss
//
// Flags:
//
//	--env                     BOSH environment alias (default "pve")
//	--sha1                    optional SHA1 override; skips bosh.io lookup when set
//	--force                   bypass the blobstore cache when re-uploading to the director; has no
//	                          effect once the director already reports the stemcell present (see
//	                          Behavior below) -- use `ocfp precompile stemcell --force` to refresh a
//	                          cached tarball in that case
//	--blobstore-endpoint      artifacts blobstore URL override (bypasses bootstrap state)
//	--blobstore-access-key    blobstore access key (or OCFP_BLOBSTORE_ACCESS_KEY)
//	--blobstore-secret-key    blobstore secret key (or OCFP_BLOBSTORE_SECRET_KEY)
//	--blobstore-ca-cert-file  path to blobstore CA cert PEM
//	--insecure-blobstore      skip TLS verification for a self-signed blobstore
//
// Behavior:
//   - If the stemcell is already present on the director, prints "already uploaded; skipping"
//     and exits 0 (no blobstore access performed on this path).
//   - Otherwise fetches the SHA1 from bosh.io (or uses --sha1), resolves the
//     tarball through the artifacts blobstore cache (downloading from url on
//     a miss, reusing the cached object on a hit), and runs
//     `bosh -e <env> upload-stemcell --sha1 <sha1> <blobstore-url>` — never
//     the raw upstream url — printing progress to stdout.
func NewPVEStemcellUploadCmd() *cobra.Command {
	return newPVEStemcellUploadCmdWithBuilder(defaultStemcellUploadBuilder, defaultStemcellResolverBuilder)
}

// newPVEStemcellUploadCmdWithBuilder is the internal constructor that accepts a
// stemcellUploadBuilder and a stemcellResolverBuilder. Tests inject fakes to
// control bosh, HTTP, and blobstore behaviour without a live director or S3.
func newPVEStemcellUploadCmdWithBuilder(builder stemcellUploadBuilder, resolverBuilder stemcellResolverBuilder) *cobra.Command {
	f := &stemcellUploadFlags{
		env: "pve",
	}

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional cobra fields
		Use:   "upload <name> <version> <url>",
		Short: "Idempotently upload a stemcell to the PVE BOSH director",
		Long: `Idempotently upload a BOSH stemcell to the PVE director, caching the
tarball in the bloc's artifacts blobstore.

The command checks whether the stemcell is already present via 'bosh stemcells
--json'. When already uploaded it exits 0 immediately with no upload traffic
and no blobstore access.

When the stemcell is absent it fetches the SHA1 checksum from bosh.io
(https://bosh.io/api/v1/stemcells/<name>) — or uses --sha1 — resolves the
tarball through the artifacts blobstore cache (fetching from <url> into the
cache on a miss, reusing the cached copy on a hit), and runs:

  bosh -e <env> upload-stemcell --sha1 <sha1> <blobstore-url>

The blobstore is resolved from bootstrap state for --bloc (or the persistent
--bloc flag / OCFP_BLOC) unless --blobstore-endpoint overrides it, matching
'ocfp precompile cf'.

Pass --sha1 to supply a known checksum and skip the bosh.io HTTP call.

--force bypasses the blobstore cache when re-uploading to the director, but
only takes effect on that miss path: once the director already reports the
stemcell present, this command exits early and --force has no effect. To
refresh a stale or corrupt cached tarball in the blobstore itself, use
'ocfp precompile stemcell --force' instead.`,
		Example: `  ocfp pve stemcell upload bosh-openstack-kvm-ubuntu-noble-go_agent 1.584 \
      https://bosh.io/d/stemcells/bosh-openstack-kvm-ubuntu-noble-go_agent?v=1.584 \
      --bloc dev

  ocfp pve stemcell upload bosh-openstack-kvm-ubuntu-noble-go_agent 1.584 \
      https://storage.example.com/stemcells/noble-1.584.tgz \
      --bloc dev --sha1 abc123deadbeef`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			return runPVEStemcellUpload(cmd, f, args[0], args[1], args[2], builder, resolverBuilder)
		},
	}

	cmd.Flags().StringVar(&f.env, "env", "pve", "BOSH environment alias")
	cmd.Flags().StringVar(&f.sha1, "sha1", "", "SHA1 checksum override (skips bosh.io lookup)")
	cmd.Flags().BoolVar(&f.force, "force", false,
		"Bypass the blobstore cache when re-uploading to the director; has no effect when the director "+
			"already reports the stemcell present (see the already-uploaded pre-check above) -- to refresh "+
			"a cached tarball in that case, use 'ocfp precompile stemcell --force' instead")
	cmd.Flags().StringVar(&f.blobEndpoint, "blobstore-endpoint", "",
		"Blobstore base URL override (e.g. https://10.0.0.5:9000); bypasses bootstrap-state lookup")
	cmd.Flags().StringVar(&f.blobAccessKey, "blobstore-access-key", "", "Blobstore access key (or env OCFP_BLOBSTORE_ACCESS_KEY)")
	cmd.Flags().StringVar(&f.blobSecretKey, "blobstore-secret-key", "",
		"Blobstore secret key (or env OCFP_BLOBSTORE_SECRET_KEY; avoids argv exposure)")
	cmd.Flags().StringVar(&f.blobCACertFile, "blobstore-ca-cert-file", "", "Path to blobstore CA cert PEM (with --blobstore-endpoint)")
	cmd.Flags().BoolVar(&f.insecureBlobstore, "insecure-blobstore", false,
		"Skip TLS verification for the blobstore when no CA cert is available (self-signed only; never silently applied)")

	return cmd
}

// runPVEStemcellUpload implements the stemcell upload logic.
//
// Steps:
//  1. Build RunBosh + SHA1Fetcher closures via builder.
//  2. Pre-check via IsStemcellUploaded; return early on a hit — no blobstore
//     access on this path.
//  3. On a miss: build the stemcellResolveFunc via resolverBuilder, fetch the
//     sha1 pin, resolve the tarball through the blobstore cache (upstream
//     fetch happens only on a cache miss inside the resolver), then run
//     `bosh upload-stemcell --sha1 <sha1> <blobstore-url>`.
//
// Inputs:
//   - cmd:             cobra command; stdout/stderr routed through cmd.OutOrStdout/ErrOrStderr.
//   - f:                resolved flag values; env defaults to "pve".
//   - name:             the stemcell name; cobra.ExactArgs(3) validates only
//     that 3 positional args were given, not that they are non-empty, so
//     name is validated here.
//   - version:          the stemcell version; validated non-empty here for
//     the same reason as name.
//   - url:              the upstream fetch source, used only on a blobstore
//     cache miss; validated non-empty here for the same reason as name.
//   - builder:          never nil; defaultStemcellUploadBuilder or test-injected.
//   - resolverBuilder:  never nil; defaultStemcellResolverBuilder or test-injected.
//
// Failure modes:
//   - name, version, or url empty → error naming the offending argument,
//     before any bosh or blobstore call.
//   - bosh stemcells check fails → propagated error.
//   - resolverBuilder fails (missing --bloc, blobstore lookup/credential/TLS
//     error) → propagated error; no bosh.io call or bosh upload attempted.
//   - bosh.io fetch fails (no --sha1) → propagated error.
//   - blobstore resolve fails (download error, sha1 mismatch, S3 put error) →
//     propagated error.
//   - bosh upload-stemcell fails → propagated error.
func runPVEStemcellUpload(
	cmd *cobra.Command,
	f *stemcellUploadFlags,
	name, version, url string,
	builder stemcellUploadBuilder,
	resolverBuilder stemcellResolverBuilder,
) error {
	if name == "" {
		return errors.New("pve stemcell upload: name must not be empty")
	}

	if version == "" {
		return errors.New("pve stemcell upload: version must not be empty")
	}

	if url == "" {
		return errors.New("pve stemcell upload: url must not be empty")
	}

	ctx := context.Background()

	runBosh, fetchSHA1 := builder(f.env, f.sha1)

	// Pre-check to provide a clear "already uploaded" message before any
	// blobstore access is attempted. This avoids the ambiguity of a
	// successful no-op and keeps the already-uploaded path free of blobstore
	// dependencies (bloc/credentials not required to check director state).
	uploaded, err := stemcell.IsStemcellUploaded(ctx, runBosh, name, version)
	if err != nil {
		return fmt.Errorf("pve stemcell upload: check existing stemcells: %w", err)
	}

	if uploaded {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "already uploaded; skipping  name=%s version=%s\n", name, version)

		return nil
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "uploading stemcell  name=%s version=%s env=%s\n", name, version, f.env)

	resolve, err := resolverBuilder(ctx, f)
	if err != nil {
		return fmt.Errorf("pve stemcell upload: resolving artifacts blobstore: %w", err)
	}

	sha1, err := fetchSHA1(ctx, name, version)
	if err != nil {
		return fmt.Errorf("pve stemcell upload: fetch sha1 for %s@%s: %w", name, version, err)
	}

	blobstoreURL, _, err := resolve(ctx, name, version, url, sha1, f.force)
	if err != nil {
		return fmt.Errorf("pve stemcell upload: caching stemcell in blobstore: %w", err)
	}

	if _, err := runBosh(ctx, "upload-stemcell", "--sha1", sha1, blobstoreURL); err != nil {
		return fmt.Errorf("pve stemcell upload: bosh upload-stemcell %s@%s: %w", name, version, err)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "upload complete  name=%s version=%s\n", name, version)

	return nil
}
