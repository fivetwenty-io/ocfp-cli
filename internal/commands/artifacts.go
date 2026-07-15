package commands

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/ocfp/ocfp-cli-go/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// artifactsLeafExpiryWarnWindow is how far ahead of a leaf's recorded/live
// NotAfter `ocfp artifacts status` starts warning (task 6.2).
const artifactsLeafExpiryWarnWindow = 30 * 24 * time.Hour

// artifactsLeafProbeTimeout bounds the best-effort live TLS dial in
// buildArtifactsLeafExpiry. Short: an unreachable endpoint must degrade to
// recorded-value-only, never hang or fail `artifacts status`.
const artifactsLeafProbeTimeout = 5 * time.Second

// Errors returned by the artifacts subcommands.
var (
	ErrArtifactsBlocRequired     = errors.New("--bloc is required for artifacts commands")
	ErrArtifactsNotFound         = errors.New("no ocfp-artifacts VM found for bloc")
	ErrUnknownArtifactsAct       = errors.New("unknown artifacts action")
	ErrArtifactsDisabled         = errors.New("artifacts feature is disabled in config; set artifacts.enabled: true")
	ErrArtifactsCAFlagConflict   = errors.New("--fingerprint and --json are mutually exclusive; choose one output mode")
	ErrArtifactsCACertPEMInvalid = errors.New("stored CA cert is not a valid PEM CERTIFICATE block")
)

// NewArtifactsCmd builds the `ocfp artifacts` command tree. Mirrors the
// shape of `ocfp bastion`: a single command that switches on the action arg.
func NewArtifactsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifacts <action>",
		Short: "ocfp-artifacts (RustFS S3) VM lifecycle",
		Long: `Manage the ocfp-artifacts VM that runs RustFS as an S3-compatible
blobstore for BOSH and Cloud Foundry deployments.

Actions:
  lookup    Print the resolved VM ID, IP, endpoint, and credentials.
  status    Show VM power state and endpoint metadata.
  provision Install/configure RustFS on the VM and create buckets.
  ca        Print the bloc's internal CA certificate (internal-ca TLS mode).
  start     Power on the VM.
  stop      Gracefully shut down the VM.
  restart   Power-cycle the VM.
  destroy   Delete the VM (requires --yes).

All actions require --bloc. The ca action reads the CA already persisted at
secret/ocfp/{bloc}/ca and never mints one unless --generate is passed.`,
		Example: `  ocfp artifacts lookup --bloc dev
  ocfp artifacts status --bloc dev --json
  ocfp artifacts provision --bloc dev
  ocfp artifacts ca --bloc dev > ca.pem
  ocfp artifacts ca --bloc dev --fingerprint
  ocfp artifacts ca --bloc dev --json
  ocfp artifacts ca --bloc dev --out /usr/local/share/ca-certificates/ocfp-dev-ca.crt
  ocfp artifacts start --bloc dev
  ocfp artifacts stop --bloc dev
  ocfp artifacts destroy --bloc dev --yes`,
		Args: cobra.MinimumNArgs(1),
		RunE: runArtifactsCmd,
	}

	cmd.Flags().String("bloc", "", "Bloc name (required)")
	cmd.Flags().Bool("json", false, "Emit output as JSON")
	cmd.Flags().Bool("yes", false, "Skip confirmation prompts (required for destroy)")
	cmd.Flags().Bool("dry-run", false, "Preview provision actions without executing")
	cmd.Flags().String("user", "ubuntu", "SSH username for the provision connection")
	cmd.Flags().String("key", "", "Path to SSH private key for the provision connection")
	cmd.Flags().Bool("no-proxy-jump", false, "Connect directly to the artifacts VM (use when running on the bastion or otherwise on the SDN)")
	cmd.Flags().Bool("fingerprint", false, "ca action: print the sha256 CA fingerprint instead of the PEM")
	cmd.Flags().String("out", "", "ca action: write the CA cert PEM to this file (0644) instead of stdout")
	cmd.Flags().Bool("generate", false, "ca action: mint a new bloc CA if one is not already configured")

	_ = viper.BindPFlag("bloc", cmd.Flags().Lookup("bloc"))
	_ = viper.BindPFlag("ssh.user", cmd.Flags().Lookup("user"))
	_ = viper.BindPFlag("ssh.key", cmd.Flags().Lookup("key"))
	_ = viper.BindPFlag("dry-run", cmd.Flags().Lookup("dry-run"))

	return cmd
}

func runArtifactsCmd(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	log := logger.WithOperation("artifacts")

	blocName := viper.GetString("bloc")
	if blocName == "" {
		return ErrArtifactsBlocRequired
	}

	asJSON, _ := cmd.Flags().GetBool("json")
	confirmYes, _ := cmd.Flags().GetBool("yes")

	action := args[0]

	// `ca` only needs vault access (config + safe), not the compute
	// provider/VM lookup the other actions share, so it is handled before
	// buildArtifactsContext runs provider setup and state lookup that a
	// pure vault read does not need.
	if action == "ca" {
		return artifactsCA(cmd, blocName, asJSON)
	}

	ctx, cleanup, err := buildArtifactsContext(cmd.Context(), blocName)
	if err != nil {
		return err
	}
	defer cleanup()

	switch action {
	case "lookup":
		return artifactsLookup(ctx, asJSON)
	case "status":
		return artifactsStatus(ctx, asJSON)
	case "provision":
		return artifactsProvision(cmd, ctx, log)
	case "start":
		return artifactsLifecycle(ctx, "start")
	case "stop":
		return artifactsLifecycle(ctx, "stop")
	case "restart":
		return artifactsLifecycle(ctx, "restart")
	case "destroy":
		if !confirmYes {
			return errors.New("destroy is destructive; pass --yes to confirm")
		}

		return artifactsDestroy(ctx, log)
	default:
		return fmt.Errorf("%w: %s", ErrUnknownArtifactsAct, action)
	}
}

// artifactsContext holds the resolved per-command dependencies. Built once
// per command invocation to avoid threading them through every action.
type artifactsContext struct {
	parent   context.Context //nolint:containedctx // per-invocation request scope, torn down by the returned cancel
	blocName string
	cfg      *config.Config
	provider cpi.Provider
	state    *state.Manager
	lookup   *artifacts.LookupResult
}

func buildArtifactsContext(parent context.Context, blocName string) (*artifactsContext, func(), error) {
	if parent == nil {
		parent = context.Background()
	}

	cfg, err := loadBlocConfiguration(viper.GetString("config"), blocName)
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}

	if !cfg.Artifacts.Enabled {
		return nil, nil, ErrArtifactsDisabled
	}

	iaas, region, err := determineProviderAndRegion(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving provider: %w", err)
	}

	provider, err := createProvider(iaas, buildProviderConfig(cfg, region))
	if err != nil {
		return nil, nil, fmt.Errorf("creating provider: %w", err)
	}

	cleanup := func() {
		_ = provider.Cleanup(context.Background())
	}

	sm, err := createStateManager(blocName)
	if err != nil {
		cleanup()

		return nil, nil, fmt.Errorf("creating state manager: %w", err)
	}

	_, err = sm.Load(blocName)
	if err != nil {
		// State not yet initialized is non-fatal for lookup/status; we'll
		// surface the absence as "not found" downstream.
		_, _ = fmt.Fprintf(os.Stderr, "warning: loading state failed: %v\n", err)
	}

	lr, err := artifacts.Lookup(parent, sm, provider, blocName)
	if err != nil {
		cleanup()

		return nil, nil, err
	}

	return &artifactsContext{
		parent:   parent,
		blocName: blocName,
		cfg:      cfg,
		provider: provider,
		state:    sm,
		lookup:   lr,
	}, cleanup, nil
}

func artifactsLookup(ac *artifactsContext, asJSON bool) error {
	if ac.lookup == nil {
		return fmt.Errorf("%w: %s", ErrArtifactsNotFound, ac.blocName)
	}

	if asJSON {
		// G117: --json is an explicit operator opt-in to dump the full lookup
		// (including blobstore creds) to their own terminal; the text path
		// deliberately omits secrets.
		out, err := json.MarshalIndent(ac.lookup, "", "  ") //nolint:gosec,musttag // G117: explicit operator --json of own creds; lookup has no json tags by design
		if err != nil {
			return err
		}

		fmt.Println(string(out))

		return nil
	}

	fmt.Printf("Name:       %s\n", ac.lookup.Name)
	fmt.Printf("VM ID:      %s\n", ac.lookup.VMID)
	fmt.Printf("Private IP: %s\n", ac.lookup.PrivateIP)
	fmt.Printf("Endpoint:   %s\n", ac.lookup.Endpoint)
	fmt.Printf("TLS Mode:   %s\n", ac.lookup.TLSMode)
	fmt.Printf("Dataset:    %s\n", ac.lookup.ZFSDataset)

	return nil
}

// artifactsCA implements `ocfp artifacts ca`: it prints the bloc's internal
// CA certificate. By default it reads the CA already persisted at
// secret/ocfp/{bloc}/ca and errors if none exists, pointing the operator at
// the two commands that create one (`ocfp vault inception`, which starts
// the vault the CA lives in, and `ocfp artifacts provision`, which mints the
// CA as a side effect of provisioning). --generate opts into minting a CA
// on the spot instead of erroring.
func artifactsCA(cmd *cobra.Command, blocName string, asJSON bool) error {
	cfg, err := loadBlocConfiguration(viper.GetString("config"), blocName)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if !cfg.Artifacts.Enabled {
		return ErrArtifactsDisabled
	}

	fingerprintOnly, _ := cmd.Flags().GetBool("fingerprint")
	outPath, _ := cmd.Flags().GetString("out")
	generate, _ := cmd.Flags().GetBool("generate")

	if fingerprintOnly && asJSON {
		return ErrArtifactsCAFlagConflict
	}

	mgr, err := vault.NewManagerFromEnv(cfg, blocName)
	if err != nil {
		return fmt.Errorf(
			"artifacts ca: vault access required for bloc %q CA material: %w\n"+
				"fix: run `ocfp vault inception --bloc %s` to start the inception vault, "+
				"or export VAULT_ADDR/VAULT_TOKEN (or run `safe target <name>`) to point at a running one",
			blocName, err, blocName)
	}
	defer func() { _ = mgr.Close() }()

	mat, err := resolveArtifactsCAMaterial(mgr.GetSafe(), blocName, generate)
	if err != nil {
		return err
	}

	cert, err := parseCACertPEM(mat.CertPEM)
	if err != nil {
		return fmt.Errorf("artifacts ca: %w: %w", ErrArtifactsCACertPEMInvalid, err)
	}

	if outPath != "" {
		if writeErr := os.WriteFile(outPath, []byte(mat.CertPEM), 0o644); writeErr != nil { //nolint:gosec // G306: CA cert is public trust material, world-readable by design
			return fmt.Errorf("artifacts ca: writing CA cert to %s: %w", outPath, writeErr)
		}
	}

	return printArtifactsCA(mat, cert, asJSON, fingerprintOnly, outPath)
}

// resolveArtifactsCAMaterial loads the bloc CA, either read-only (default)
// or generate-on-demand (--generate). The not-found error is rewritten with
// the actionable remediation the plan requires; other errors (e.g. malformed
// stored material) pass through wrapped with the calling action's context.
func resolveArtifactsCAMaterial(safe vault.SafeInterface, blocName string, generate bool) (artifacts.CAMaterial, error) {
	if generate {
		mat, err := vault.LoadOrGenerateBlocCA(safe, blocName)
		if err != nil {
			return artifacts.CAMaterial{}, fmt.Errorf("artifacts ca: generate bloc CA: %w", err)
		}

		return mat, nil
	}

	mat, err := vault.LoadBlocCA(safe, blocName)
	if err != nil {
		if errors.Is(err, vault.ErrBlocCANotFound) {
			return artifacts.CAMaterial{}, fmt.Errorf(
				"artifacts ca: %w for bloc %q\n"+
					"fix: run `ocfp vault inception --bloc %s` then `ocfp artifacts provision --bloc %s` "+
					"to mint one, or re-run this command with --generate to mint it now",
				err, blocName, blocName, blocName)
		}

		return artifacts.CAMaterial{}, fmt.Errorf("artifacts ca: load bloc CA: %w", err)
	}

	return mat, nil
}

// parseCACertPEM decodes and parses the stored CA cert PEM so ca action can
// report not_before/not_after. Returns an error if the PEM block is missing,
// is not a CERTIFICATE block, or the DER payload does not parse — this is a
// distinct failure mode from ErrBlocCAMalformed (which covers the vault
// secret shape, not certificate well-formedness).
func parseCACertPEM(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no CERTIFICATE PEM block found")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing DER: %w", err)
	}

	return cert, nil
}

// printArtifactsCA renders the resolved CA material to stdout per the
// requested mode. Precedence: --json > --fingerprint > default PEM. When
// --out already wrote the PEM to a file and no other mode was requested,
// stdout is left silent to avoid duplicating the cert on both file and
// terminal.
func printArtifactsCA(mat artifacts.CAMaterial, cert *x509.Certificate, asJSON, fingerprintOnly bool, outPath string) error {
	switch {
	case asJSON:
		out := map[string]any{
			"cert":        mat.CertPEM,
			"fingerprint": mat.Fingerprint,
			"not_before":  cert.NotBefore.UTC().Format(time.RFC3339),
			"not_after":   cert.NotAfter.UTC().Format(time.RFC3339),
		}

		enc, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return fmt.Errorf("artifacts ca: marshaling json: %w", err)
		}

		fmt.Println(string(enc))
	case fingerprintOnly:
		fmt.Println(mat.Fingerprint)
	case outPath != "":
		// PEM was already written to outPath; do not also dump it to stdout.
	default:
		fmt.Print(mat.CertPEM)
	}

	return nil
}

// buildArtifactsStatusReport assembles the `artifacts status` field set.
// Binary version + build time (vinfo) are included alongside the VM/state
// fields so support transcripts (bug reports, `artifacts status` pasted
// into a ticket) reveal whether the operator ran a stale binary — the exact
// failure class that produced the "internal-ca requires an internal CA to
// be configured" incident, where internal-ca support was already wired at
// HEAD but the running binary predated it.
//
// expiry (task 6.2) is folded in as optional keys — tls_leaf_not_after (state
// recorded at issuance), tls_leaf_not_after_live (this invocation's TLS
// probe), tls_leaf_expiry_warning (present only when either is within 30
// days of expiry or already expired), tls_fingerprint_drift +
// tls_fingerprint_drift_warning (present only when the pinned and live
// fingerprints are both known and differ) — so both the text and JSON
// renderers surface the same fields without duplicating the presence checks.
func buildArtifactsStatusReport(lr *artifacts.LookupResult, instanceState string, vinfo version.Info, expiry artifactsLeafExpiry) map[string]any {
	report := map[string]any{
		"name":           lr.Name,
		"vm_id":          lr.VMID,
		"state":          instanceState,
		"private_ip":     lr.PrivateIP,
		"endpoint":       lr.Endpoint,
		"tls_mode":       lr.TLSMode,
		"dataset":        lr.ZFSDataset,
		"cli_version":    vinfo.Version,
		"cli_git_commit": vinfo.GitCommit,
		"cli_build_time": vinfo.BuildTime,
	}

	if expiry.RecordedNotAfter != "" {
		report["tls_leaf_not_after"] = expiry.RecordedNotAfter
	}

	if expiry.LiveNotAfter != "" {
		report["tls_leaf_not_after_live"] = expiry.LiveNotAfter
	}

	if expiry.Warning != "" {
		report["tls_leaf_expiry_warning"] = expiry.Warning
	}

	if expiry.FingerprintDrift {
		report["tls_fingerprint_drift"] = true
		report["tls_fingerprint_drift_warning"] = expiry.DriftWarning
	}

	return report
}

// artifactsLeafExpiry (task 6.2) bundles the state-recorded and live-probed
// leaf certificate expiry for `artifacts status`, plus a human-readable
// warning when either is within 30 days of expiry or already expired, plus
// fingerprint-drift detection (gap 2): the pinned tls_fingerprint_sha256
// from state compared against the live-probed leaf's own fingerprint.
// Fingerprint remains metadata-only throughout — the drift check is a
// warning surfaced to the operator, never a trust decision, and never
// affects the command's exit code (see vault.ArtifactsWriter doc comment).
type artifactsLeafExpiry struct {
	RecordedNotAfter string // RFC3339; "" when state never recorded one (TLS disabled, or state predates task 6.2)
	LiveNotAfter     string // RFC3339; "" when the endpoint could not be probed (unreachable, TLS disabled)
	Warning          string // human-readable remediation message; "" when nothing is near/past expiry

	PinnedFingerprint string // sha256 hex from state's tls_fingerprint_sha256; "" when never recorded
	LiveFingerprint   string // sha256 hex of the actually-served leaf; "" when the endpoint could not be probed
	FingerprintDrift  bool   // true only when both fingerprints are known and they differ
	DriftWarning      string // human-readable message naming both fingerprints; "" when no drift (or either is unknown)
}

// buildArtifactsLeafExpiry assembles the recorded state values (expiry +
// pinned fingerprint) plus a best-effort live TLS probe. The live probe is
// never allowed to fail `artifacts status`: an unreachable endpoint, a
// non-TLS endpoint, or a probe error all degrade silently to empty
// Live* fields, falling back to whatever the recorded/pinned values have.
func buildArtifactsLeafExpiry(ac *artifactsContext) artifactsLeafExpiry {
	var expiry artifactsLeafExpiry

	res, err := ac.state.GetResource(artifacts.ResourceType, ac.lookup.Name)
	if err == nil && res != nil {
		if v, ok := res.Properties["tls_leaf_not_after"].(string); ok {
			expiry.RecordedNotAfter = v
		}

		if v, ok := res.Properties["tls_fingerprint_sha256"].(string); ok {
			expiry.PinnedFingerprint = v
		}
	}

	if ac.lookup.TLSMode != config.ArtifactsTLSModeDisabled && strings.HasPrefix(ac.lookup.Endpoint, "https://") {
		live, probeErr := probeArtifactsLiveLeaf(ac.parent, ac.lookup.Endpoint, artifactsLeafProbeTimeout)
		if probeErr != nil {
			// Best-effort only (task 6.2): an unreachable endpoint or transient
			// network issue must never fail `artifacts status`, which is often
			// exactly the command an operator runs to diagnose that endpoint.
			logger.Debugf("artifacts status: live leaf probe for %s: %v", ac.lookup.Endpoint, probeErr)
		} else {
			expiry.LiveNotAfter = live.NotAfter.UTC().Format(time.RFC3339)
			expiry.LiveFingerprint = live.FingerprintSHA256
		}
	}

	expiry.Warning = artifactsLeafExpiryWarning(expiry.RecordedNotAfter, expiry.LiveNotAfter)
	expiry.DriftWarning = artifactsFingerprintDriftWarning(expiry.PinnedFingerprint, expiry.LiveFingerprint)
	expiry.FingerprintDrift = expiry.DriftWarning != ""

	return expiry
}

// artifactsLiveLeafProbe is what a live TLS dial against the artifacts
// endpoint reveals about the leaf certificate actually being served.
type artifactsLiveLeafProbe struct {
	NotAfter time.Time

	// FingerprintSHA256 is lowercase hex, colon-free — the same format as
	// artifacts.TLSMaterial.Fingerprint — so it compares directly against
	// the pinned tls_fingerprint_sha256 state/vault value.
	FingerprintSHA256 string
}

// probeArtifactsLiveLeaf dials the artifacts endpoint's TLS port and reports
// what the server actually presents: the leaf's NotAfter (task 6.2, leaf
// expiry) and its SHA-256 fingerprint (task 6.2 gap 2, drift detection
// against the pinned tls_fingerprint_sha256). This is a status/expiry read,
// not a trust decision — InsecureSkipVerify is deliberate here (the caller
// only wants to know what's being served, never makes an authorization
// decision based on this connection), so it is intentionally distinct from
// the CA-pinned S3 clients built via artifacts.EndpointForLookup. parent
// bounds the dial together with timeout (whichever is shorter wins); callers
// pass context.Background() when no caller-scoped context applies.
func probeArtifactsLiveLeaf(parent context.Context, endpoint string, timeout time.Duration) (artifactsLiveLeafProbe, error) {
	if parent == nil {
		parent = context.Background()
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return artifactsLiveLeafProbe{}, fmt.Errorf("parsing endpoint %q: %w", endpoint, err)
	}

	if u.Scheme != "https" {
		return artifactsLiveLeafProbe{}, fmt.Errorf("endpoint %q is not https; no TLS leaf to probe", endpoint) //nolint:err113 // descriptive, not caller-testable
	}

	host := u.Host
	if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
		host = net.JoinHostPort(host, "443")
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{},
		Config:    &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // G402: expiry/fingerprint probe only, never a trust decision — see doc comment above
	}

	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return artifactsLiveLeafProbe{}, fmt.Errorf("dialing %s: %w", host, err)
	}
	defer func() { _ = conn.Close() }()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return artifactsLiveLeafProbe{}, fmt.Errorf("dialing %s: connection is not TLS", host) //nolint:err113 // descriptive, not caller-testable
	}

	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return artifactsLiveLeafProbe{}, fmt.Errorf("no certificates presented by %s", host) //nolint:err113 // descriptive, not caller-testable
	}

	sum := sha256.Sum256(certs[0].Raw)

	return artifactsLiveLeafProbe{
		NotAfter:          certs[0].NotAfter,
		FingerprintSHA256: hex.EncodeToString(sum[:]),
	}, nil
}

// artifactsFingerprintDriftWarning (task 6.2 gap 2) compares the pinned
// (state-recorded) leaf fingerprint against the live-probed one and returns
// a human-readable warning naming both when they differ. Returns "" when
// either fingerprint is unknown (nothing to compare) or they match — a
// mismatch just means state/vault's pin is stale relative to what the VM is
// actually serving (e.g. an out-of-band reissue, or a state/vault write that
// never completed); it is never treated as a trust failure and never
// changes `artifacts status`'s exit code.
func artifactsFingerprintDriftWarning(pinned, live string) string {
	if pinned == "" || live == "" {
		return ""
	}

	if strings.EqualFold(pinned, live) {
		return ""
	}

	return fmt.Sprintf(
		"served leaf fingerprint (%s) does not match the pinned tls_fingerprint_sha256 in state (%s); "+
			"fingerprint is operator metadata only, not a trust anchor — this likely means state is stale "+
			"after an out-of-band reissue; run `ocfp artifacts provision` to refresh the pin, or investigate "+
			"if unexpected",
		live, pinned,
	)
}

// artifactsLeafExpiryWarning produces a human-readable warning when the live
// probe (preferred — it reflects the cert actually being served) or,
// failing that, the recorded state value is within artifactsLeafExpiryWarnWindow
// of expiry or already past it. Returns "" when both are empty (no TLS, or
// state predates task 6.2 and the endpoint could not be probed) or both are
// comfortably in the future.
func artifactsLeafExpiryWarning(recordedRFC3339, liveRFC3339 string) string {
	if w := singleLeafExpiryWarning("served", liveRFC3339); w != "" {
		return w
	}

	return singleLeafExpiryWarning("recorded", recordedRFC3339)
}

func singleLeafExpiryWarning(label, rfc3339 string) string {
	if rfc3339 == "" {
		return ""
	}

	notAfter, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return ""
	}

	remaining := time.Until(notAfter)

	switch {
	case remaining < 0:
		return fmt.Sprintf(
			"%s leaf certificate EXPIRED %s ago (not_after=%s); run `ocfp artifacts provision` to reissue",
			label, remaining.Abs().Round(time.Hour), rfc3339,
		)
	case remaining < artifactsLeafExpiryWarnWindow:
		return fmt.Sprintf(
			"%s leaf certificate expires in %d day(s) (not_after=%s); run `ocfp artifacts provision` to reissue before it expires",
			label, int(remaining.Hours()/24), rfc3339,
		)
	default:
		return ""
	}
}

func artifactsStatus(ac *artifactsContext, asJSON bool) error {
	if ac.lookup == nil {
		return fmt.Errorf("%w: %s", ErrArtifactsNotFound, ac.blocName)
	}

	inst, err := ac.provider.ComputeManager().GetInstance(ac.parent, ac.lookup.VMID)
	if err != nil {
		return fmt.Errorf("getting instance %s: %w", ac.lookup.VMID, err)
	}

	expiry := buildArtifactsLeafExpiry(ac)
	report := buildArtifactsStatusReport(ac.lookup, string(inst.State), version.Get(), expiry)

	if asJSON {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}

		fmt.Println(string(out))

		return nil
	}

	for _, k := range []string{
		"name", "vm_id", "state", "private_ip", "endpoint", "tls_mode", "dataset",
		"cli_version", "cli_git_commit", "cli_build_time",
	} {
		fmt.Printf("%-15s %v\n", k+":", report[k])
	}

	for _, k := range []string{"tls_leaf_not_after", "tls_leaf_not_after_live"} {
		if v, ok := report[k]; ok {
			fmt.Printf("%-15s %v\n", k+":", v)
		}
	}

	if w, ok := report["tls_leaf_expiry_warning"]; ok {
		fmt.Printf("WARNING:        %v\n", w)
	}

	if w, ok := report["tls_fingerprint_drift_warning"]; ok {
		fmt.Printf("WARNING:        %v\n", w)
	}

	return nil
}

func artifactsLifecycle(ac *artifactsContext, op string) error {
	if ac.lookup == nil {
		return fmt.Errorf("%w: %s", ErrArtifactsNotFound, ac.blocName)
	}

	cm := ac.provider.ComputeManager()

	switch op {
	case "start":
		err := cm.StartInstance(ac.parent, ac.lookup.VMID)
		if err != nil {
			return fmt.Errorf("starting %s: %w", ac.lookup.VMID, err)
		}

		fmt.Printf("✓ start requested on %s (%s)\n", ac.lookup.Name, ac.lookup.VMID)
	case "stop":
		err := cm.StopInstance(ac.parent, ac.lookup.VMID)
		if err != nil {
			return fmt.Errorf("stopping %s: %w", ac.lookup.VMID, err)
		}

		fmt.Printf("✓ stop requested on %s (%s)\n", ac.lookup.Name, ac.lookup.VMID)
	case "restart":
		err := cm.RebootInstance(ac.parent, ac.lookup.VMID)
		if err != nil {
			return fmt.Errorf("rebooting %s: %w", ac.lookup.VMID, err)
		}

		fmt.Printf("✓ restart requested on %s (%s)\n", ac.lookup.Name, ac.lookup.VMID)
	default:
		return fmt.Errorf("%w: %s", ErrUnknownArtifactsAct, op)
	}

	return nil
}

func artifactsDestroy(ac *artifactsContext, log logger.Logger) error {
	if ac.lookup == nil {
		return fmt.Errorf("%w: %s", ErrArtifactsNotFound, ac.blocName)
	}

	log.Infof("destroying artifacts VM %s (id=%s)", ac.lookup.Name, ac.lookup.VMID)

	err := ac.provider.ComputeManager().DeleteInstance(ac.parent, ac.lookup.VMID)
	if err != nil {
		return fmt.Errorf("deleting instance %s: %w", ac.lookup.VMID, err)
	}

	// Delete the attached data volume so we don't leak storage on the PVE
	// node. Best-effort: the volume may already be cleaned up by the same
	// DeleteInstance call on some providers.
	if ac.lookup.DataVolumeID != "" {
		err = ac.provider.StorageManager().DeleteVolume(ac.parent, ac.lookup.DataVolumeID)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "warning: deleting data volume %s: %v\n", ac.lookup.DataVolumeID, err)
		}
	}

	// Remove resource entry from state. Best-effort: a missing state file is
	// acceptable here because the VM is already gone.
	err = ac.state.RemoveResource("artifacts", ac.lookup.Name)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: clearing artifacts state entry: %v\n", err)
	}

	_ = ac.state.Save()

	fmt.Printf("✓ destroyed %s\n", ac.lookup.Name)

	return nil
}
