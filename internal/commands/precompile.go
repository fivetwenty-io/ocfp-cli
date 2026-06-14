package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/precompile"
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

	addCFFlags := func(c *cobra.Command) {
		c.Flags().String("cf-deployment", "", "Path to cf-deployment.yml (default ~/deployments/<bloc>/cf-deployment.yml)")
		c.Flags().String("blobstore-endpoint", "", "Blobstore base URL override (e.g. https://10.0.0.5:9000); bypasses bootstrap-state lookup")
		c.Flags().String("blobstore-access-key", "", "Blobstore access key (or env OCFP_BLOBSTORE_ACCESS_KEY)")
		c.Flags().String("blobstore-secret-key", "", "Blobstore secret key (or env OCFP_BLOBSTORE_SECRET_KEY; avoids argv exposure)")
		c.Flags().String("blobstore-ca-cert-file", "", "Path to blobstore CA cert PEM (with --blobstore-endpoint)")
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

	cmd.AddCommand(boshCmd, cfCmd, allCmd)

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
	if f.blobEndpoint == "" {
		return lookupArtifactsFromState(ctx, f.bloc)
	}

	if f.blobAccessKey == "" || f.blobSecretKey == "" {
		return nil, errors.New("--blobstore-endpoint requires --blobstore-access-key and --blobstore-secret-key")
	}

	lr := &artifacts.LookupResult{
		Endpoint:  f.blobEndpoint,
		AccessKey: f.blobAccessKey,
		SecretKey: f.blobSecretKey,
	}
	if f.blobCACertFile != "" {
		pem, err := os.ReadFile(f.blobCACertFile) //nolint:gosec // operator-supplied path
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

	manifestData, err := os.ReadFile(f.cfManifest) //nolint:gosec // path is operator-supplied config
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

	s3c, ep, err := precompileS3Client(lr)
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
		Bucket:     f.bloc + "-ocf-bosh",
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
// lookup result. RustFS uses path-style; a CA cert pins TLS, otherwise TLS
// verification is skipped (self-signed RustFS). The returned *s3.Client
// satisfies the precompile package's object API directly.
func precompileS3Client(lr *artifacts.LookupResult) (*s3.Client, string, error) {
	ep := artifacts.Endpoint{
		URL:           lr.Endpoint,
		Region:        "us-east-1",
		PathStyle:     true,
		CACert:        lr.CACert,
		SkipTLSVerify: lr.CACert == "" && strings.HasPrefix(lr.Endpoint, "https://"),
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
	if err := os.WriteFile(dest, ops, 0o600); err != nil {
		return fmt.Errorf("writing pin ops %s: %w", dest, err)
	}

	log.Infof("wrote pin ops for %d releases to %s", len(res), dest)

	return nil
}
