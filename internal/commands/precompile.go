package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/precompile"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Errors returned by the precompile subcommands.
var (
	ErrPrecompileBlocRequired = errors.New("--bloc is required for precompile commands")
	ErrCFDeploymentNotFound   = errors.New("cf-deployment.yml not found; pass --cf-deployment")
)

// precompileFlags holds the shared, parsed flags for a precompile run.
type precompileFlags struct {
	bloc       string
	force      bool
	dryRun     bool
	stemcell   precompile.Stemcell
	boshEnv    string
	outputDir  string
	cfManifest string
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

// NewPrecompileCmd builds the `ocfp precompile` command tree. It populates the
// artifacts blobstore with compiled release tarballs and emits pin ops files so
// create-env (bosh) and `genesis deploy` (cf) skip source compilation.
func NewPrecompileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "precompile <bosh|cf|all>",
		Short: "Precompile BOSH/CF releases into the artifacts blobstore",
		Long: `Resolve each release to a compiled tarball — reusing the blobstore copy,
fetching an upstream compiled build, or compiling locally (no-VM deploy +
export-release) — then emit a pin ops file the deploy consumes.

Run after 'ocfp bootstrap' + 'ocfp artifacts provision' and before the matching
'ocfp init' / 'genesis deploy'. Idempotent: a warm run is a HEAD-only no-op.`,
		Example: `  ocfp precompile bosh --bloc dev
  ocfp precompile cf --bloc dev --cf-deployment ~/deployments/dev/cf-deployment.yml
  ocfp precompile cf --bloc dev --dry-run
  ocfp precompile all --bloc dev`,
	}

	addPrecompileFlags := func(c *cobra.Command) {
		c.Flags().String("bloc", "", "Bloc name (required)")
		c.Flags().Bool("force", false, "Rebuild/re-upload even if a compiled tarball is already present")
		c.Flags().Bool("dry-run", false, "Resolve and report the per-release plan without mutating the blobstore")
		c.Flags().String("stemcell", precompile.DefaultStemcell.String(), "Stemcell as os/version")
		c.Flags().String("bosh-env", "", "BOSH environment alias (bosh -e); empty uses the ambient BOSH_* env")
		c.Flags().String("output-dir", "", "Where to write pin ops files (default ~/deployments/<bloc>)")
	}

	boshCmd := &cobra.Command{
		Use:   "bosh",
		Short: "Pin director releases (bosh, bpm) to upstream compiled tarballs",
		RunE:  func(c *cobra.Command, _ []string) error { return runPrecompileBOSH(c) },
	}
	addPrecompileFlags(boshCmd)

	addBlobstoreOverrideFlags := func(c *cobra.Command) {
		c.Flags().String("blobstore-endpoint", "", "Blobstore base URL override (e.g. https://10.0.0.5:9000); bypasses bootstrap-state lookup")
		c.Flags().String("blobstore-access-key", "", "Blobstore access key (or env OCFP_BLOBSTORE_ACCESS_KEY)")
		c.Flags().String("blobstore-secret-key", "", "Blobstore secret key (or env OCFP_BLOBSTORE_SECRET_KEY; avoids argv exposure)")
		c.Flags().String("blobstore-ca-cert-file", "", "Path to blobstore CA cert PEM (with --blobstore-endpoint)")
		c.Flags().Bool("insecure-blobstore", false, "Skip TLS verification for the blobstore when no CA cert is available (self-signed only; never silently applied)")
	}

	addCFFlags := func(c *cobra.Command) {
		c.Flags().String("cf-deployment", "", "Path to cf-deployment.yml (default ~/deployments/<bloc>/cf-deployment.yml)")
		addBlobstoreOverrideFlags(c)
	}

	cfCmd := &cobra.Command{
		Use:   "cf",
		Short: "Compile cf-deployment releases into the blobstore and pin them",
		RunE:  func(c *cobra.Command, _ []string) error { return runPrecompileCF(c) },
	}
	addPrecompileFlags(cfCmd)
	addCFFlags(cfCmd)

	allCmd := &cobra.Command{
		Use:   "all",
		Short: "Run both 'precompile bosh' and 'precompile cf'",
		RunE: func(c *cobra.Command, _ []string) error {
			err := runPrecompileBOSH(c)
			if err != nil {
				return err
			}

			return runPrecompileCF(c)
		},
	}
	addPrecompileFlags(allCmd)
	addCFFlags(allCmd)

	stemcellCmd := &cobra.Command{
		Use:   "stemcell <name> <version> <url>",
		Short: "Pre-populate the artifacts blobstore with a stemcell tarball",
		Long: `Cache a stemcell tarball in the artifacts blobstore ahead of a live
director target, so 'ocfp pve stemcell upload' (or any 'bosh upload-stemcell')
can reuse the cached copy instead of re-fetching from bosh.io/GCS every time.

Idempotent: a warm run is a HEAD-only no-op unless --force is set. --dry-run
reports whether the tarball is already cached without downloading or
uploading anything.`,
		Example: `  ocfp precompile stemcell bosh-openstack-kvm-ubuntu-noble-go_agent 1.584 \
    https://bosh.io/d/stemcells/bosh-openstack-kvm-ubuntu-noble-go_agent?v=1.584 \
    --bloc dev --sha1 abcdef0123456789abcdef0123456789abcdef01
  ocfp precompile stemcell bosh-openstack-kvm-ubuntu-noble-go_agent 1.584 \
    https://bosh.io/d/stemcells/bosh-openstack-kvm-ubuntu-noble-go_agent?v=1.584 \
    --bloc dev --dry-run`,
		Args: cobra.ExactArgs(3),
		RunE: runPrecompileStemcell,
	}
	stemcellCmd.Flags().String("bloc", "", "Bloc name (required)")
	stemcellCmd.Flags().Bool("force", false, "Re-download and re-upload even if already cached")
	stemcellCmd.Flags().Bool("dry-run", false, "Report cache status without downloading, uploading, or otherwise mutating the blobstore")
	stemcellCmd.Flags().String("sha1", "", "Expected sha1 of the stemcell tarball (e.g. from bosh.io); verified against the download before caching, skipped when empty")
	addBlobstoreOverrideFlags(stemcellCmd)

	cmd.AddCommand(boshCmd, cfCmd, allCmd, stemcellCmd)

	return cmd
}

func parsePrecompileFlags(cmd *cobra.Command) (precompileFlags, error) {
	var f precompileFlags

	f.bloc, _ = cmd.Flags().GetString("bloc")
	if f.bloc == "" {
		f.bloc = viper.GetString("bloc")
	}

	if f.bloc == "" {
		return f, ErrPrecompileBlocRequired
	}

	f.force, _ = cmd.Flags().GetBool("force")
	f.dryRun, _ = cmd.Flags().GetBool("dry-run")
	f.boshEnv, _ = cmd.Flags().GetString("bosh-env")

	scStr, _ := cmd.Flags().GetString("stemcell")

	sc, err := parseStemcell(scStr)
	if err != nil {
		return f, err
	}

	f.stemcell = sc

	f.outputDir, _ = cmd.Flags().GetString("output-dir")
	if f.outputDir == "" {
		home, err := homeDir()
		if err != nil {
			return f, err
		}

		f.outputDir = filepath.Join(home, "deployments", f.bloc)
	}

	if cmd.Flags().Lookup("cf-deployment") != nil {
		f.cfManifest, _ = cmd.Flags().GetString("cf-deployment")
		if f.cfManifest == "" {
			f.cfManifest = filepath.Join(f.outputDir, "cf-deployment.yml")
		}

		f.blobEndpoint, _ = cmd.Flags().GetString("blobstore-endpoint")
		f.blobAccessKey, _ = cmd.Flags().GetString("blobstore-access-key")
		f.blobSecretKey, _ = cmd.Flags().GetString("blobstore-secret-key")
		f.blobCACertFile, _ = cmd.Flags().GetString("blobstore-ca-cert-file")
		f.insecureBlobstore, _ = cmd.Flags().GetBool("insecure-blobstore")

		// Allow secrets via env so they need not appear in argv (process list).
		if f.blobAccessKey == "" {
			f.blobAccessKey = os.Getenv("OCFP_BLOBSTORE_ACCESS_KEY")
		}

		if f.blobSecretKey == "" {
			f.blobSecretKey = os.Getenv("OCFP_BLOBSTORE_SECRET_KEY")
		}
	}

	return f, nil
}

// resolveBlobstore returns the artifacts blobstore lookup result, preferring
// explicit override flags (needed on the bastion) over bootstrap-state lookup.
func (f precompileFlags) resolveBlobstore(ctx context.Context) (*artifacts.LookupResult, error) {
	return resolveBlobstoreOverride(ctx, f.bloc, f.blobEndpoint, f.blobAccessKey, f.blobSecretKey, f.blobCACertFile)
}

// resolveBlobstoreOverride returns the artifacts blobstore lookup result,
// preferring explicit override flags (needed on the bastion, which holds no
// bootstrap state) over bootstrap-state lookup when blobEndpoint is empty.
// Shared by every precompile verb (bosh/cf/all's precompileFlags.resolveBlobstore
// and the stemcell verb) so the override-vs-state precedence lives in one place.
func resolveBlobstoreOverride(ctx context.Context, bloc, blobEndpoint, blobAccessKey, blobSecretKey, blobCACertFile string) (*artifacts.LookupResult, error) {
	if blobEndpoint == "" {
		return lookupArtifactsFromState(ctx, bloc)
	}

	if blobAccessKey == "" || blobSecretKey == "" {
		return nil, errors.New("--blobstore-endpoint requires --blobstore-access-key and --blobstore-secret-key")
	}

	lr := &artifacts.LookupResult{
		Endpoint:  blobEndpoint,
		AccessKey: blobAccessKey,
		SecretKey: blobSecretKey,
		// Manual --blobstore-endpoint overrides carry no tls.mode from state;
		// tag as self-signed so precompileS3Client's EndpointForLookup call
		// treats a missing CA cert as "needs the explicit --insecure-blobstore
		// opt-in", not as an internal-ca state inconsistency.
		TLSMode: config.ArtifactsTLSModeSelfSigned,
	}

	if blobCACertFile != "" {
		pem, err := os.ReadFile(blobCACertFile) // #nosec G304 -- operator-supplied path
		if err != nil {
			return nil, fmt.Errorf("reading blobstore CA cert: %w", err)
		}

		lr.CACert = string(pem)
	}

	return lr, nil
}

func parseStemcell(s string) (precompile.Stemcell, error) {
	os, ver, ok := strings.Cut(s, "/")
	if !ok || os == "" || ver == "" {
		return precompile.Stemcell{}, fmt.Errorf("invalid --stemcell %q: want os/version", s)
	}

	return precompile.Stemcell{OS: os, Version: ver}, nil
}

func (f precompileFlags) options() precompile.Options {
	return precompile.Options{
		Force:    f.force,
		DryRun:   f.dryRun,
		Stemcell: f.stemcell,
	}
}

func runPrecompileBOSH(cmd *cobra.Command) error {
	cmd.SilenceUsage = true
	log := logger.WithOperation("precompile-bosh")

	f, err := parsePrecompileFlags(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	workDir, cleanup, err := makeWorkDir("ocfp-precompile-bosh")
	if err != nil {
		return err
	}
	defer cleanup()

	c := &precompile.Compiler{
		Director: precompile.NewBOSHDirector(f.boshEnv),
		HTTP:     &http.Client{Timeout: 30 * time.Minute},
		WorkDir:  workDir,
		Log:      func(format string, a ...any) { log.Infof(format, a...) },
	}

	rels := precompile.BOSHReleases(f.stemcell)

	res, err := c.ResolveDirector(ctx, rels, f.stemcell, f.options())
	if err != nil {
		return fmt.Errorf("resolving director releases: %w", err)
	}

	return emitPinOps(res, f.stemcell, "ocfp precompile bosh",
		filepath.Join(f.outputDir, "manifests", "bosh"), f.dryRun, log)
}

func runPrecompileCF(cmd *cobra.Command) error {
	cmd.SilenceUsage = true
	log := logger.WithOperation("precompile-cf")

	f, err := parsePrecompileFlags(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	manifestData, err := os.ReadFile(f.cfManifest) // #nosec -- path is operator-supplied config
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrCFDeploymentNotFound, f.cfManifest)
		}

		return fmt.Errorf("reading cf-deployment manifest: %w", err)
	}

	rels, err := precompile.ParseCFReleases(manifestData, precompile.CFMinReleases)
	if err != nil {
		return err
	}

	log.Infof("parsed %d cf-deployment releases from %s", len(rels), f.cfManifest)

	lr, err := f.resolveBlobstore(ctx)
	if err != nil {
		return err
	}

	s3c, ep, err := precompileS3Client(f.bloc, lr, f.insecureBlobstore)
	if err != nil {
		return err
	}

	workDir, wdCleanup, err := makeWorkDir("ocfp-precompile-cf")
	if err != nil {
		return err
	}
	defer wdCleanup()

	c := &precompile.Compiler{
		Director:   precompile.NewBOSHDirector(f.boshEnv),
		S3:         s3c,
		HTTP:       &http.Client{Timeout: 30 * time.Minute},
		Bucket:     stemcellBucket(f.bloc),
		Endpoint:   ep,
		Deployment: f.bloc + "-precompile-cf",
		WorkDir:    workDir,
		Log:        func(format string, a ...any) { log.Infof(format, a...) },
	}

	res, err := c.ResolveBlobstore(ctx, rels, f.stemcell, f.options())
	if err != nil {
		return fmt.Errorf("resolving cf releases: %w", err)
	}

	return emitPinOps(res, f.stemcell, "ocfp precompile cf",
		filepath.Join(f.outputDir, "manifests", "cf"), f.dryRun, log)
}

// precompileObjectAPI is the S3 method subset the stemcell verb needs to pass
// to internal/precompile's ResolveStemcell/HeadCompiled. Declared locally
// rather than imported: precompile's own equivalent (objectAPI) is
// unexported, but Go interface satisfaction is structural, so a value of
// this type — real *s3.Client or a test fake — is assignable to precompile's
// parameter without needing to name its type.
type precompileObjectAPI interface {
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// stemcellBucket is the artifacts blobstore bucket stemcell tarballs are
// cached in, matching the bucket the cf verb already uses for compiled CF
// releases (both are BOSH-consumable artifacts for the same bloc).
func stemcellBucket(bloc string) string {
	return bloc + "-ocf-bosh"
}

// runPrecompileStemcell implements `ocfp precompile stemcell <name> <version>
// <url>`. It resolves the artifacts blobstore (override flags or bootstrap
// state, same precedence as the cf verb), builds an S3 client, and delegates
// to runPrecompileStemcellCore.
func runPrecompileStemcell(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	name, version, url := args[0], args[1], args[2]

	bloc, _ := cmd.Flags().GetString("bloc")
	if bloc == "" {
		bloc = viper.GetString("bloc")
	}

	if bloc == "" {
		return ErrPrecompileBlocRequired
	}

	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	sha1, _ := cmd.Flags().GetString("sha1")

	blobEndpoint, _ := cmd.Flags().GetString("blobstore-endpoint")
	blobAccessKey, _ := cmd.Flags().GetString("blobstore-access-key")
	blobSecretKey, _ := cmd.Flags().GetString("blobstore-secret-key")
	blobCACertFile, _ := cmd.Flags().GetString("blobstore-ca-cert-file")
	insecureBlobstore, _ := cmd.Flags().GetBool("insecure-blobstore")

	// Allow secrets via env so they need not appear in argv (process list),
	// same precedence as the cf verb's parsePrecompileFlags.
	if blobAccessKey == "" {
		blobAccessKey = os.Getenv("OCFP_BLOBSTORE_ACCESS_KEY")
	}

	if blobSecretKey == "" {
		blobSecretKey = os.Getenv("OCFP_BLOBSTORE_SECRET_KEY")
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	lr, err := resolveBlobstoreOverride(ctx, bloc, blobEndpoint, blobAccessKey, blobSecretKey, blobCACertFile)
	if err != nil {
		return err
	}

	s3c, ep, err := precompileS3Client(bloc, lr, insecureBlobstore)
	if err != nil {
		return err
	}

	hc := &http.Client{Timeout: 30 * time.Minute}

	return runPrecompileStemcellCore(ctx, s3c, hc, cmd.OutOrStdout(), stemcellBucket(bloc), ep, name, version, url, sha1, force, dryRun)
}

// runPrecompileStemcellCore resolves (or, in dry-run, reports on) a stemcell
// tarball's cache state in the artifacts blobstore. Factored out from
// runPrecompileStemcell so it is unit-testable against a fake objectAPI/HTTP
// client, without needing a live bootstrap state or blobstore.
//
// dryRun never calls PutObject: it performs a HeadObject-only presence check
// (mirroring precompile.Compiler.ResolveBlobstore's own dry-run behavior,
// which still HEADs but never PUTs) and reports the plan, leaving the cache
// untouched. A non-dry-run call delegates entirely to
// precompile.ResolveStemcell, which owns the present/download/upload
// decision and the optional sha1 verification.
//
// Failure modes: empty name, version, or url returns an error before any
// I/O — url is required even in dry-run, since a caller with nothing to
// fetch on a cache miss cannot report a meaningful plan. A HeadObject error
// (dry-run) or any ResolveStemcell error (non-dry-run) is wrapped and
// returned; neither path partially mutates the cache on error.
func runPrecompileStemcellCore(
	ctx context.Context,
	s3c precompileObjectAPI,
	hc *http.Client,
	out io.Writer,
	bucket, endpoint, name, version, upstreamURL, expectedSHA1 string,
	force, dryRun bool,
) error {
	if name == "" {
		return errors.New("precompile stemcell: name is required")
	}

	if version == "" {
		return fmt.Errorf("precompile stemcell %s: version is required", name)
	}

	if upstreamURL == "" {
		return fmt.Errorf("precompile stemcell %s/%s: url is required", name, version)
	}

	log := logger.WithOperation("precompile-stemcell")
	key := precompile.StemcellKey(name, version)
	resolvedURL := precompile.HTTPSURL(endpoint, bucket, key)

	if dryRun {
		_, cached, err := precompile.HeadCompiled(ctx, s3c, bucket, key)
		if err != nil {
			return fmt.Errorf("precompile stemcell %s/%s: checking cache: %w", name, version, err)
		}

		switch {
		case cached && !force:
			_, _ = fmt.Fprintf(out, "stemcell %s/%s: already cached at %s (dry-run, no changes made)\n", name, version, resolvedURL)
		case cached && force:
			_, _ = fmt.Fprintf(out, "stemcell %s/%s: cached at %s, --force would re-fetch %s and overwrite it (dry-run, no changes made)\n", name, version, resolvedURL, upstreamURL)
		default:
			_, _ = fmt.Fprintf(out, "stemcell %s/%s: not cached; would fetch %s and cache at %s (dry-run, no changes made)\n", name, version, upstreamURL, resolvedURL)
		}

		log.Infof("dry-run: stemcell %s/%s plan reported, blobstore not mutated", name, version)

		return nil
	}

	resolvedURL, sha256hex, err := precompile.ResolveStemcell(ctx, s3c, hc, bucket, endpoint, name, version, upstreamURL, expectedSHA1, force)
	if err != nil {
		return fmt.Errorf("precompile stemcell %s/%s: %w", name, version, err)
	}

	_, _ = fmt.Fprintf(out, "stemcell %s/%s cached: %s (sha256=%s)\n", name, version, resolvedURL, sha256hex)
	log.Infof("stemcell %s/%s ready at %s", name, version, resolvedURL)

	return nil
}

// lookupArtifactsFromState resolves the artifacts blobstore VM from local state
// only (no cloud provider). precompile runs on the bastion, which does not hold
// cloud-provider credentials, so constructing a provider there would hang. A
// missing or un-provisioned blobstore is reported as a fast, clear error.
func lookupArtifactsFromState(ctx context.Context, bloc string) (*artifacts.LookupResult, error) {
	cfg, err := loadBlocConfiguration(viper.GetString("config"), bloc)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	if !cfg.Artifacts.Enabled {
		return nil, ErrArtifactsDisabled
	}

	sm, err := createStateManager(bloc)
	if err != nil {
		return nil, fmt.Errorf("creating state manager: %w", err)
	}

	if _, err := sm.Load(bloc); err != nil {
		return nil, fmt.Errorf("loading state for %s: %w", bloc, err)
	}

	lr, err := artifacts.Lookup(ctx, sm, nil, bloc)
	if err != nil {
		return nil, err
	}

	if lr == nil || lr.Endpoint == "" {
		return nil, fmt.Errorf("%w: %s — artifacts blobstore not provisioned; run `ocfp bootstrap --artifacts` and populate vault before `precompile cf`",
			ErrArtifactsNotFound, bloc)
	}

	return lr, nil
}

// precompileS3Client builds an artifacts S3 client + endpoint base URL from a
// lookup result. RustFS uses path-style. TLS trust is resolved by
// artifacts.EndpointForLookup, the single place that decision lives:
//
//   - a CA cert already on lr (from state, or --blobstore-ca-cert-file) pins
//     verification.
//   - otherwise, when lr.TLSMode is internal-ca, the bloc CA is recovered
//     from vault — precompile runs on the bastion, colocated with the
//     inception vault — before falling through to EndpointForLookup's error.
//   - self-signed (including manual --blobstore-endpoint overrides, which
//     resolveBlobstore tags as self-signed) requires the explicit
//     insecureBlobstore opt-in to skip verification; it is never silently
//     skipped.
//
// The returned *s3.Client satisfies the precompile package's object API
// directly.
func precompileS3Client(bloc string, lr *artifacts.LookupResult, insecureBlobstore bool) (*s3.Client, string, error) {
	caCert := lr.CACert

	if caCert == "" && lr.TLSMode == config.ArtifactsTLSModeInternalCA {
		mgr, mgrErr := vault.NewManagerFromEnv(nil, bloc)
		if mgrErr != nil {
			return nil, "", fmt.Errorf(
				"artifacts: tls.mode=internal-ca requires vault access to recover the bloc CA; set VAULT_ADDR/VAULT_TOKEN or `safe target`, or pass --blobstore-ca-cert-file: %w",
				mgrErr)
		}

		ca, caErr := vault.LoadBlocCA(mgr.GetSafe(), bloc)
		if caErr != nil {
			return nil, "", fmt.Errorf(
				"artifacts: recovering bloc %q CA from vault: %w (pass --blobstore-ca-cert-file, or run `ocfp artifacts ca --bloc %s` / `ocfp artifacts provision --bloc %s` to inspect/re-mint it)",
				bloc, caErr, bloc, bloc)
		}

		caCert = ca.CertPEM
	}

	ep, err := artifacts.EndpointForLookup(bloc, lr.Endpoint, lr.TLSMode, caCert, insecureBlobstore)
	if err != nil {
		return nil, "", fmt.Errorf("resolving artifacts blobstore TLS trust: %w", err)
	}

	cli, err := artifacts.NewS3Client(ep, artifacts.Credentials{AccessKey: lr.AccessKey, SecretKey: lr.SecretKey})
	if err != nil {
		return nil, "", fmt.Errorf("building artifacts S3 client: %w", err)
	}

	return cli, lr.Endpoint, nil
}

func makeWorkDir(prefix string) (string, func(), error) {
	dir, err := os.MkdirTemp("", prefix+"-")
	if err != nil {
		return "", nil, fmt.Errorf("creating work dir: %w", err)
	}

	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func emitPinOps(res []precompile.Resolution, sc precompile.Stemcell, generatedBy, destDir string, dryRun bool, log logger.Logger) error {
	fmt.Printf("Release resolution plan (%s):\n", sc)

	for _, r := range res {
		fmt.Printf("  %-28s %-10s %s\n", r.Name+"/"+r.Version, r.Source, r.URL)
	}

	// Dry-run resolutions carry no sha (nothing was downloaded/compiled), so the
	// pin ops file cannot be rendered; report the plan and stop.
	if dryRun {
		log.Infof("dry-run: %d releases resolved; pin ops not written", len(res))

		return nil
	}

	ops, err := precompile.RenderOpsFile(res, sc, generatedBy)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return fmt.Errorf("creating ops dir %s: %w", destDir, err)
	}

	dest := filepath.Join(destDir, "compiled-releases.yml")
	if err := os.WriteFile(dest, ops, 0o600); err != nil { // #nosec G703 -- dest derives from the config-controlled deployments dir
		return fmt.Errorf("writing pin ops %s: %w", dest, err)
	}

	log.Infof("wrote pin ops for %d releases to %s", len(res), dest)

	return nil
}
