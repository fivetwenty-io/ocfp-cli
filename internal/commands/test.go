package commands

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// DefaultTestTimeoutMinutes is the default per-check timeout in minutes.
	DefaultTestTimeoutMinutes = 30

	// TestRetryDelaySeconds is the delay in seconds between check retries.
	TestRetryDelaySeconds = 5

	// TestResultsFileMode is the file permission mode for test result files.
	TestResultsFileMode = 0600

	// defaultTestServiceTimeout bounds asynchronous service instance and
	// binding operations (create, bind, unbind, delete).
	defaultTestServiceTimeout = 15 * time.Minute

	// testRouteWait bounds how long a freshly mapped route, TCP route, or
	// network policy is given to start answering before a check fails.
	testRouteWait = 2 * time.Minute

	// testPollInterval is the delay between polls of an asynchronous
	// Cloud Controller operation.
	testPollInterval = 5 * time.Second

	// testRoutePollInterval is the delay between attempts to reach a route.
	testRoutePollInterval = 3 * time.Second

	// testCleanupTimeout bounds the org deletion that runs after the suites,
	// which uses its own context so an interrupted run still cleans up.
	testCleanupTimeout = 10 * time.Minute

	// testRunIDBytes is the number of random bytes behind the per-run suffix
	// that keeps app and service names unique across concurrent runs.
	testRunIDBytes = 3

	// testAppPort is the port every generated test app listens on.
	testAppPort = 8080

	// testBoshErrand is the Cloud Foundry errand the acceptance suite runs
	// when a BOSH director is available.
	testBoshErrand = "smoke-tests"

	// testDefaultBoshDeployment is the deployment the acceptance errand runs
	// against when neither --bosh-deployment nor BOSH_DEPLOYMENT is set.
	testDefaultBoshDeployment = "cf"
)

// TestSuite represents a test suite type.
type TestSuite string

const (
	// TestSuiteC2C is the container-to-container networking test suite.
	TestSuiteC2C TestSuite = "c2c"

	// TestSuiteBlacksmith is the Blacksmith service broker test suite.
	TestSuiteBlacksmith TestSuite = "blacksmith"

	// TestSuiteNFS is the NFS volume services test suite.
	TestSuiteNFS TestSuite = "nfs"

	// TestSuiteSMB is the SMB volume services test suite.
	TestSuiteSMB TestSuite = "smb"

	// TestSuiteTCP is the TCP routing test suite.
	TestSuiteTCP TestSuite = "tcp"

	// TestSuiteAll runs all available test suites.
	TestSuiteAll TestSuite = "all"

	// TestSuiteSmoke is the smoke test suite for basic platform validation.
	TestSuiteSmoke TestSuite = "smoke"

	// TestSuiteAcceptance is the full acceptance test suite.
	TestSuiteAcceptance TestSuite = "acceptance"
)

// TestStatus represents test status.
type TestStatus string

const (
	// TestStatusPassed indicates a test completed successfully.
	TestStatusPassed TestStatus = "PASSED"

	// TestStatusFailed indicates a test did not pass.
	TestStatusFailed TestStatus = "FAILED"

	// TestStatusSkipped indicates a test was skipped.
	TestStatusSkipped TestStatus = "SKIPPED"

	// TestStatusRunning indicates a test is currently in progress.
	TestStatusRunning TestStatus = "RUNNING"
)

// Test command errors.
var (
	// ErrTestAPIUnresolved is returned when neither --api, the bloc config,
	// nor the current cf target yields a Cloud Foundry API endpoint.
	ErrTestAPIUnresolved = errors.New("cannot determine the CF API endpoint: pass --api, set fqdns in the bloc config, or run cf api first")

	// ErrTestNotLoggedIn is returned when the cf CLI has no session for the
	// resolved API endpoint.
	ErrTestNotLoggedIn = errors.New("cf CLI is not logged in: run cf login against the foundation first")

	// ErrTestWaitTimeout is returned when a bounded wait expires.
	ErrTestWaitTimeout = errors.New("timed out waiting")

	// ErrTestOfferingNotFound is returned when --offering names a service
	// offering the marketplace does not have.
	ErrTestOfferingNotFound = errors.New("service offering not found in marketplace")

	// ErrTestPlanNotFound is returned when --plan names a plan the chosen
	// offering does not have.
	ErrTestPlanNotFound = errors.New("service plan not found for offering")
)

var (
	testValidOrgNamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-_])*[a-zA-Z0-9]$`)
	testValidDNSPattern     = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-.])*[a-zA-Z0-9]$`)
)

const testCmdLong = `Run validation suites against the bloc's Cloud Foundry foundation.

Every suite drives the cf CLI and plain HTTP checks, so it works from any
machine that is logged in to the foundation (on the labs, the bastion). The
API endpoint comes from the bloc config (fqdns.ocf.system, or system.<base>)
unless --api is given, and the apps, internal, and TCP domains are discovered
from the foundation itself. Each run creates <bloc>-test-org and
<bloc>-test-space, runs its checks there, and deletes the org afterwards
unless --skip-cleanup is set.

Suites:
  smoke       Push a static app, confirm its HTTPS route answers 200, read
              its recent logs with cf logs --recent, and delete it.
  c2c         Push a backend and a frontend, map an internal route on the
              backend, confirm the frontend cannot reach it, add a network
              policy, and confirm the frontend then fetches the backend over
              the internal route.
  blacksmith  Pick a Blacksmith offering and plan from the marketplace (or
              --offering/--plan), create a service instance, wait for the
              asynchronous create, create a service key and check it carries
              credentials, bind the instance to a test app, then unbind,
              delete the key, and delete the instance.
  tcp         Push the static app with a TCP route on the foundation's TCP
              domain and connect to it. Skips when there is no TCP domain.
  nfs, smb    Find the volume service broker in the marketplace, create an
              instance for --nfs-share or --smb-share, bind it to a test
              app, and confirm the app restarts with the volume mounted.
              Skips when there is no broker or no share is given.
  acceptance  Run the Cloud Foundry smoke-tests errand through the BOSH
              director named by --bosh-env or BOSH_ENVIRONMENT. When no
              director is reachable it runs smoke, c2c, and blacksmith
              through the cf CLI instead and says so in its output.
  all         smoke, c2c, blacksmith, nfs, smb, and tcp.

Checks are named <suite>/<check>. --tags keeps only checks whose name
contains one of the tags, --exclude drops checks whose name contains one of
the tags, and a check whose prerequisite did not pass is skipped. Skipped
checks are counted as skipped, never as passed, and each skip reports its
reason. The command exits non-zero when any check fails.`

const testCmdExample = `  # Run smoke tests
  ocfp test smoke

  # Run all test suites in parallel
  ocfp test all --parallel

  # Run C2C tests with verbose output
  ocfp test c2c --verbose

  # Exercise a specific Blacksmith offering and plan
  ocfp test blacksmith --offering redis --plan standalone

  # Run the CF smoke-tests errand through the director aliased "ocf"
  ocfp test acceptance --bosh-env ocf

  # Run an NFS volume check against an existing export
  ocfp test nfs --nfs-share 10.0.0.5:/export/cf

  # Run tests excluding specific checks
  ocfp test all --exclude tcp,smb

  # Save JUnit results to a file
  ocfp test all --output results.xml`

// testOptions holds the test command options.
type testOptions struct {
	parallel       bool
	timeout        time.Duration
	verbose        bool
	outputFile     string
	skipCleanup    bool
	retries        int
	tags           []string
	exclude        []string
	api            string
	skipSSL        bool
	skipSSLSet     bool
	offering       string
	plan           string
	boshEnv        string
	boshDeployment string
	tcpDomain      string
	nfsShare       string
	smbShare       string
	smbUsername    string
	smbPassword    string
	serviceTimeout time.Duration
}

// NewTestCmd creates the test command.
func NewTestCmd() *cobra.Command {
	opts := &testOptions{} //nolint:exhaustruct // flags populate the fields

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:       "test <suite>",
		Short:     "Run validation suites (smoke|c2c|blacksmith|tcp|nfs|smb|acceptance|all)",
		Long:      testCmdLong,
		Example:   testCmdExample,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"c2c", "blacksmith", "nfs", "smb", "tcp", "smoke", "acceptance", "all"},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.skipSSLSet = cmd.Flags().Changed("skip-ssl-validation")

			return runTestCommand(args[0], opts)
		},
	}

	addTestFlags(cmd, opts)

	return cmd
}

// addTestFlags adds all test command flags.
func addTestFlags(cmd *cobra.Command, opts *testOptions) {
	cmd.Flags().BoolVar(&opts.parallel, "parallel", false, "run the suites of all or acceptance concurrently")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", DefaultTestTimeoutMinutes*time.Minute, "timeout for each check")
	cmd.Flags().BoolVar(&opts.verbose, "verbose", false, "print each check's command output")
	cmd.Flags().StringVar(&opts.outputFile, "output", "", "save results to file (JUnit XML, or JSON when the name ends in .json)")
	cmd.Flags().BoolVar(&opts.skipCleanup, "skip-cleanup", false, "leave the test org, apps, and service instances in place")
	cmd.Flags().IntVar(&opts.retries, "retries", 1, "number of retries for failed checks")
	cmd.Flags().StringSliceVar(&opts.tags, "tags", nil, "only run checks whose name contains one of these tags")
	cmd.Flags().StringSliceVar(&opts.exclude, "exclude", nil, "skip checks whose name contains one of these tags")
	cmd.Flags().StringVar(&opts.api, "api", "", "CF API endpoint (default: derived from the bloc config, then the current cf target)")
	cmd.Flags().BoolVar(&opts.skipSSL, "skip-ssl-validation", false,
		"skip TLS verification for cf api and HTTPS checks (default: the current cf CLI setting)")
	cmd.Flags().StringVar(&opts.offering, "offering", "", "blacksmith: service offering to test (default: first Blacksmith offering)")
	cmd.Flags().StringVar(&opts.plan, "plan", "", "blacksmith: service plan to test (default: first available plan)")
	cmd.Flags().StringVar(&opts.boshEnv, "bosh-env", "", "acceptance: BOSH environment alias for the smoke-tests errand (default: $BOSH_ENVIRONMENT)")
	cmd.Flags().StringVar(&opts.boshDeployment, "bosh-deployment", "",
		"acceptance: BOSH deployment that owns the smoke-tests errand (default: $BOSH_DEPLOYMENT, then cf)")
	cmd.Flags().StringVar(&opts.tcpDomain, "tcp-domain", "", "tcp: TCP domain to route through (default: first TCP domain on the foundation)")
	cmd.Flags().StringVar(&opts.nfsShare, "nfs-share", "", "nfs: NFS export to mount, as host:/path")
	cmd.Flags().StringVar(&opts.smbShare, "smb-share", "", "smb: SMB share to mount, as //host/share")
	cmd.Flags().StringVar(&opts.smbUsername, "smb-username", "", "smb: username for the share")
	cmd.Flags().StringVar(&opts.smbPassword, "smb-password", "", "smb: password for the share (or env OCFP_TEST_SMB_PASSWORD)")
	cmd.Flags().DurationVar(&opts.serviceTimeout, "service-timeout", defaultTestServiceTimeout,
		"bound for asynchronous service instance and binding operations")
}

// runTestCommand executes the test command logic.
func runTestCommand(testType string, opts *testOptions) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := logger.Get()

	suite, err := parseTestSuite(testType)
	if err != nil {
		return err
	}

	cfg, err := loadTestConfig()
	if err != nil {
		return err
	}

	log.Infow("Running tests", "suite", suite, "deployment", cfg.Name, "parallel", opts.parallel, "timeout", opts.timeout)

	runner := createTestRunner(cfg, suite, opts)

	return executeTestSuite(ctx, runner, opts, log)
}

// parseTestSuite parses the test suite from the test type string.
func parseTestSuite(testType string) (TestSuite, error) {
	switch strings.ToLower(testType) {
	case "c2c":
		return TestSuiteC2C, nil
	case "blacksmith":
		return TestSuiteBlacksmith, nil
	case "nfs":
		return TestSuiteNFS, nil
	case "smb":
		return TestSuiteSMB, nil
	case "tcp":
		return TestSuiteTCP, nil
	case "all":
		return TestSuiteAll, nil
	case "smoke":
		return TestSuiteSmoke, nil
	case "acceptance":
		return TestSuiteAcceptance, nil
	default:
		return TestSuiteSmoke, ErrUnknownTestType(testType)
	}
}

// loadTestConfig loads the configuration for testing.
func loadTestConfig() (*config.Config, error) {
	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, nil
}

// createTestRunner creates a new test runner with the given configuration.
func createTestRunner(cfg *config.Config, suite TestSuite, opts *testOptions) *TestRunner {
	smbPassword := opts.smbPassword
	if smbPassword == "" {
		smbPassword = os.Getenv("OCFP_TEST_SMB_PASSWORD")
	}

	return &TestRunner{ //nolint:exhaustruct // runtime fields are populated by Setup
		Config:            cfg,
		Suite:             suite,
		Parallel:          opts.parallel,
		Timeout:           opts.timeout,
		Verbose:           opts.verbose,
		OutputFile:        opts.outputFile,
		SkipCleanup:       opts.skipCleanup,
		Retries:           opts.retries,
		Tags:              opts.tags,
		Exclude:           opts.exclude,
		API:               opts.api,
		SkipSSLValidation: opts.skipSSL,
		skipSSLExplicit:   opts.skipSSLSet,
		Offering:          opts.offering,
		Plan:              opts.plan,
		BoshEnv:           opts.boshEnv,
		BoshDeployment:    opts.boshDeployment,
		TCPDomain:         opts.tcpDomain,
		NFSShare:          opts.nfsShare,
		SMBShare:          opts.smbShare,
		SMBUsername:       opts.smbUsername,
		SMBPassword:       smbPassword,
		ServiceTimeout:    opts.serviceTimeout,
	}
}

// executeTestSuite executes the test suite using the runner.
func executeTestSuite(ctx context.Context, runner *TestRunner, opts *testOptions, log logger.Logger) error {
	err := prepareTestEnvironment(ctx, runner)
	if err != nil {
		return err
	}

	if !opts.skipCleanup {
		defer cleanupTestEnvironment(runner, log)
	}

	results, err := runTests(ctx, runner)
	if err != nil {
		return err
	}

	return handleTestResults(results, opts, runner, log)
}

// prepareTestEnvironment validates and sets up the test environment.
func prepareTestEnvironment(ctx context.Context, runner *TestRunner) error {
	err := runner.ValidateEnvironment(ctx)
	if err != nil {
		return fmt.Errorf("test environment validation failed: %w", err)
	}

	err = runner.Setup(ctx)
	if err != nil {
		return fmt.Errorf("test setup failed: %w", err)
	}

	return nil
}

// cleanupTestEnvironment performs cleanup after test execution. It runs on
// its own context so an interrupted run still removes the test org.
func cleanupTestEnvironment(runner *TestRunner, log logger.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), testCleanupTimeout)
	defer cancel()

	err := runner.Cleanup(ctx)
	if err != nil {
		log.Warnw("Test cleanup failed", "error", err)
	}
}

// runTests executes the tests and returns results.
func runTests(ctx context.Context, runner *TestRunner) (*TestResults, error) {
	results, err := runner.Execute(ctx)
	if err != nil {
		return nil, fmt.Errorf("test execution failed: %w", err)
	}

	return results, nil
}

// handleTestResults processes and reports test results.
func handleTestResults(results *TestResults, opts *testOptions, runner *TestRunner, log logger.Logger) error {
	runner.DisplayResults(results)

	if opts.outputFile != "" {
		err := runner.SaveResults(results, opts.outputFile)
		if err != nil {
			log.Warnw("Failed to save test results", "error", err)
		}
	}

	if results.Failed > 0 {
		return ErrTestsFailed(results.Passed, results.Failed, results.Skipped)
	}

	return nil
}

// TestRunner handles test execution.
type TestRunner struct {
	Config      *config.Config
	Suite       TestSuite
	Parallel    bool
	Timeout     time.Duration
	Verbose     bool
	OutputFile  string
	SkipCleanup bool
	Retries     int
	Tags        []string
	Exclude     []string

	// API overrides the CF API endpoint derived from the bloc config.
	API string
	// SkipSSLValidation mirrors cf api --skip-ssl-validation for both the cf
	// target and the HTTPS route checks. When skipSSLExplicit is false the
	// value is read from the cf CLI's own config during validation.
	SkipSSLValidation bool
	skipSSLExplicit   bool
	// Offering and Plan pin the blacksmith suite to a marketplace entry.
	Offering string
	Plan     string
	// BoshEnv and BoshDeployment locate the smoke-tests errand for the
	// acceptance suite.
	BoshEnv        string
	BoshDeployment string
	// TCPDomain pins the tcp suite to a specific TCP domain.
	TCPDomain string
	// NFSShare, SMBShare, SMBUsername, and SMBPassword feed the volume suites.
	NFSShare    string
	SMBShare    string
	SMBUsername string
	SMBPassword string
	// ServiceTimeout bounds asynchronous service operations.
	ServiceTimeout time.Duration

	runID      string
	orgName    string
	spaceName  string
	orgGUID    string
	spaceGUID  string
	apiURL     string
	appsDomain string
	domains    []cfDomain
	tempDirs   []string
	routeWait  time.Duration
	httpClient *http.Client
	dial       func(ctx context.Context, network, address string) (net.Conn, error)
	sleep      func(ctx context.Context, d time.Duration) error
	out        io.Writer
	mu         sync.Mutex
}

// TestResults represents test execution results.
type TestResults struct {
	Suite     TestSuite
	Passed    int
	Failed    int
	Skipped   int
	Duration  time.Duration
	Tests     []TestResult
	Output    string
	StartTime time.Time
}

// TestResult represents a single test result.
type TestResult struct {
	Name     string
	Status   TestStatus
	Duration time.Duration
	Error    string
	Reason   string
	Output   string
	Retries  int
}

// stepLog collects the informational output of one check for verbose display
// and for the system-out element of the JUnit report.
type stepLog struct {
	buf strings.Builder
}

// Printf appends a formatted line to the check's output.
func (l *stepLog) Printf(format string, args ...any) {
	fmt.Fprintf(&l.buf, format, args...)
	l.buf.WriteByte('\n')
}

// String returns the collected output.
func (l *stepLog) String() string {
	return l.buf.String()
}

// stepFunc runs one check. A non-empty skip reason marks the check skipped;
// an error marks it failed (after retries); otherwise it passed.
type stepFunc func(ctx context.Context, log *stepLog) (skipReason string, err error)

// testStep is one named check inside a suite.
type testStep struct {
	Name string
	Run  stepFunc
	// Needs lists checks that must have passed before this one runs. For a
	// cleanup step it lists the checks that must at least have been
	// attempted, so cleanup still runs after a failure but not after a skip.
	Needs []string
	// Cleanup marks a step that tears down what an earlier step created. It
	// ignores --tags and --exclude and is skipped only by --skip-cleanup.
	Cleanup bool
}

// suitePlan is one suite's ordered checks together with the state they share.
type suitePlan struct {
	suite TestSuite
	steps []testStep
}

// ensureDefaults fills in the runtime seams (HTTP client, dialer, sleeper,
// output writer, run ID) that production runs take from the standard library
// and unit tests replace.
func (r *TestRunner) ensureDefaults() error {
	if r.out == nil {
		r.out = os.Stdout
	}

	if r.sleep == nil {
		r.sleep = sleepContext
	}

	if r.dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second} //nolint:exhaustruct // defaults are fine
		r.dial = dialer.DialContext
	}

	if r.ServiceTimeout == 0 {
		r.ServiceTimeout = defaultTestServiceTimeout
	}

	if r.Timeout == 0 {
		r.Timeout = DefaultTestTimeoutMinutes * time.Minute
	}

	if r.routeWait == 0 {
		r.routeWait = testRouteWait
	}

	if r.httpClient == nil {
		r.httpClient = newTestHTTPClient(r.SkipSSLValidation)
	}

	if r.runID != "" {
		return nil
	}

	buf := make([]byte, testRunIDBytes)

	_, err := rand.Read(buf)
	if err != nil {
		return fmt.Errorf("failed to generate run id: %w", err)
	}

	r.runID = hex.EncodeToString(buf)

	return nil
}

// newTestHTTPClient builds the client used for HTTPS route checks. TLS
// verification follows the operator's cf api choice.
func newTestHTTPClient(insecure bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // DefaultTransport is always *http.Transport
	transport.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: insecure, // #nosec G402 -- mirrors the operator's cf api --skip-ssl-validation choice
		MinVersion:         tls.VersionTLS12,
	}

	return &http.Client{ //nolint:exhaustruct // defaults are fine
		Transport: transport,
		Timeout:   30 * time.Second,
	}
}

// sleepContext waits for d or until ctx is done, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ValidateEnvironment confirms the cf CLI is present, targets the bloc's API
// endpoint, and confirms the CLI holds a session for it.
func (r *TestRunner) ValidateEnvironment(ctx context.Context) error {
	if !r.skipSSLExplicit {
		r.SkipSSLValidation = readCFSSLDisabled(cfConfigPath())
	}

	err := r.ensureDefaults()
	if err != nil {
		return err
	}

	err = runner.LookPath("cf")
	if err != nil {
		return fmt.Errorf("cf CLI is required: %w", err)
	}

	r.apiURL, err = r.resolveAPIURL(ctx)
	if err != nil {
		return err
	}

	err = r.ensureCFTarget(ctx)
	if err != nil {
		return err
	}

	return r.ensureLoggedIn(ctx)
}

// systemDomain derives the CF system domain from the bloc config: the
// explicit fqdns.ocf.system entry, then system.<fqdns.base>, then the legacy
// dns list.
func (r *TestRunner) systemDomain() string {
	if r.Config == nil {
		return ""
	}

	if r.Config.FQDNs != nil {
		domain := vault.GetFQDN("system", r.Config.FQDNs.OCF, r.Config.FQDNs.Base, false)
		if domain != "" {
			return domain
		}
	}

	if len(r.Config.DNS) > 0 {
		return r.Config.DNS[0]
	}

	return ""
}

// configAppsDomain derives the apps domain hint from the bloc config. The
// foundation's own default domain wins when it can be read.
func (r *TestRunner) configAppsDomain() string {
	if r.Config == nil || r.Config.FQDNs == nil {
		return ""
	}

	return vault.GetFQDN("apps", r.Config.FQDNs.OCF, r.Config.FQDNs.Base, false)
}

// resolveAPIURL picks the CF API endpoint: --api, then the bloc config's
// system domain, then whatever the cf CLI already targets.
func (r *TestRunner) resolveAPIURL(ctx context.Context) (string, error) {
	if r.API != "" {
		return strings.TrimRight(r.API, "/"), nil
	}

	domain := r.systemDomain()
	if domain != "" {
		err := security.ValidateInput(domain, testValidDNSPattern)
		if err != nil {
			return "", fmt.Errorf("invalid system domain %q: %w", domain, err)
		}

		return "https://api." + domain, nil
	}

	out, _ := runner.Run(ctx, "cf", "api")

	current := parseCFAPIEndpoint(string(out))
	if current != "" {
		return current, nil
	}

	return "", ErrTestAPIUnresolved
}

// ensureCFTarget points the cf CLI at the resolved API endpoint when it is
// not already there. Retargeting the same endpoint would drop the session,
// so an existing matching target is left alone.
func (r *TestRunner) ensureCFTarget(ctx context.Context) error {
	out, _ := runner.Run(ctx, "cf", "api")

	current := parseCFAPIEndpoint(string(out))
	if strings.EqualFold(strings.TrimRight(current, "/"), r.apiURL) {
		return nil
	}

	args := []string{"api", r.apiURL}
	if r.SkipSSLValidation {
		args = append(args, "--skip-ssl-validation")
	}

	r.printf("Targeting %s\n", r.apiURL)

	_, err := r.cf(ctx, args...)
	if err != nil {
		return fmt.Errorf("failed to set CF target: %w", err)
	}

	return nil
}

// ensureLoggedIn confirms the cf CLI holds a session for the target.
func (r *TestRunner) ensureLoggedIn(ctx context.Context) error {
	out, err := runner.Run(ctx, "cf", "target")
	if err != nil {
		return fmt.Errorf("%w (%s): %s", ErrTestNotLoggedIn, r.apiURL, lastLines(string(out), 3))
	}

	return nil
}

// Setup creates and targets the test org and space, then discovers the
// org, space, and domain details the suites need.
func (r *TestRunner) Setup(ctx context.Context) error {
	err := r.createTestOrgSpace(ctx)
	if err != nil {
		return fmt.Errorf("failed to create test org/space: %w", err)
	}

	err = r.discoverSpace(ctx)
	if err != nil {
		return fmt.Errorf("failed to inspect the test space: %w", err)
	}

	r.printf("Using org %s, space %s, apps domain %s (run %s)\n", r.orgName, r.spaceName, r.appsDomain, r.runID)

	return nil
}

func (r *TestRunner) createTestOrgSpace(ctx context.Context) error {
	err := security.ValidateInput(r.Config.Name, testValidOrgNamePattern)
	if err != nil {
		return fmt.Errorf("invalid config name: %w", err)
	}

	r.orgName = r.Config.Name + "-test-org"
	r.spaceName = r.Config.Name + "-test-space"

	_, err = r.cf(ctx, "create-org", r.orgName)
	if err != nil {
		return err
	}

	_, err = r.cf(ctx, "create-space", r.spaceName, "-o", r.orgName)
	if err != nil {
		return err
	}

	_, err = r.cf(ctx, "target", "-o", r.orgName, "-s", r.spaceName)
	if err != nil {
		return err
	}

	return nil
}

// discoverSpace records the org and space GUIDs, the foundation's domains,
// and the apps domain the test routes will use.
func (r *TestRunner) discoverSpace(ctx context.Context) error {
	var err error

	r.orgGUID, err = r.lookupGUID(ctx, "/v3/organizations", map[string]string{"names": r.orgName})
	if err != nil {
		return err
	}

	r.spaceGUID, err = r.lookupGUID(ctx, "/v3/spaces", map[string]string{"names": r.spaceName, "organization_guids": r.orgGUID})
	if err != nil {
		return err
	}

	r.domains, err = r.loadDomains(ctx)
	if err != nil {
		return err
	}

	r.appsDomain = r.pickAppsDomain(ctx)
	if r.appsDomain == "" {
		return errors.New("no shared HTTP domain found on the foundation") //nolint:err113 // one-off failure
	}

	return nil
}

// pickAppsDomain prefers the org's default domain, then the bloc config's
// apps FQDN when the foundation has it, then the first shared HTTP domain.
func (r *TestRunner) pickAppsDomain(ctx context.Context) string {
	var def ccResource

	err := r.cfCurl(ctx, "/v3/organizations/"+r.orgGUID+"/domains/default", &def)
	if err == nil && def.Name != "" {
		return def.Name
	}

	hint := r.configAppsDomain()
	for _, d := range r.domains {
		if d.Name == hint {
			return hint
		}
	}

	return firstHTTPDomain(r.domains)
}

// Cleanup deletes the test org, which removes every app, route, service
// instance, and binding the suites created, and removes generated app dirs.
func (r *TestRunner) Cleanup(ctx context.Context) error {
	log := logger.Get()
	log.Info("Cleaning up test environment")

	r.mu.Lock()
	dirs := r.tempDirs
	r.mu.Unlock()

	for _, dir := range dirs {
		_ = os.RemoveAll(dir)
	}

	if r.orgName == "" {
		return nil
	}

	_, err := r.cf(ctx, "delete-org", r.orgName, "-f")
	if err != nil {
		return fmt.Errorf("failed to delete test org: %w", err)
	}

	return nil
}

// Execute runs the selected suite (or suites) and returns the results.
func (r *TestRunner) Execute(ctx context.Context) (*TestResults, error) {
	err := r.ensureDefaults()
	if err != nil {
		return nil, err
	}

	results := r.initializeTestResults()

	plans, note, err := r.buildPlans(ctx)
	if err != nil {
		return nil, err
	}

	if note != "" {
		results.Output = note

		r.printf("%s\n", note)
	}

	if r.Parallel && len(plans) > 1 {
		r.runPlansParallel(ctx, plans, results)
	} else {
		for _, plan := range plans {
			r.runPlan(ctx, plan, results)
		}
	}

	results.Duration = time.Since(results.StartTime)

	return results, nil
}

// buildPlans expands the selected suite into one plan per concrete suite.
// The note describes the acceptance mode when that suite is selected.
func (r *TestRunner) buildPlans(ctx context.Context) ([]suitePlan, string, error) {
	switch r.Suite {
	case TestSuiteAll:
		suites := []TestSuite{TestSuiteSmoke, TestSuiteC2C, TestSuiteBlacksmith, TestSuiteNFS, TestSuiteSMB, TestSuiteTCP}

		return r.plansFor(suites), "", nil
	case TestSuiteAcceptance:
		return r.acceptancePlans(ctx)
	case TestSuiteSmoke, TestSuiteC2C, TestSuiteBlacksmith, TestSuiteNFS, TestSuiteSMB, TestSuiteTCP:
		return r.plansFor([]TestSuite{r.Suite}), "", nil
	default:
		return nil, "", ErrUnknownTestSuite(string(r.Suite))
	}
}

// plansFor builds one plan per suite, each with its own state.
func (r *TestRunner) plansFor(suites []TestSuite) []suitePlan {
	plans := make([]suitePlan, 0, len(suites))

	for _, suite := range suites {
		plans = append(plans, suitePlan{suite: suite, steps: r.suiteSteps(suite)})
	}

	return plans
}

// acceptancePlans runs the CF smoke-tests errand when a director is
// reachable and otherwise falls back to the cf CLI suites, saying which.
func (r *TestRunner) acceptancePlans(ctx context.Context) ([]suitePlan, string, error) {
	alias, reason := r.directorAvailability(ctx)
	if alias != "" {
		note := fmt.Sprintf("Acceptance mode: BOSH errand %s/%s through director alias %q", r.boshDeployment(), testBoshErrand, alias)

		return []suitePlan{{suite: TestSuiteAcceptance, steps: r.acceptanceErrandSteps(alias)}}, note, nil
	}

	note := "Acceptance mode: cf CLI suites smoke, c2c, and blacksmith, because " + reason

	return r.plansFor([]TestSuite{TestSuiteSmoke, TestSuiteC2C, TestSuiteBlacksmith}), note, nil
}

func (r *TestRunner) runPlansParallel(ctx context.Context, plans []suitePlan, results *TestResults) {
	var wg sync.WaitGroup

	for _, plan := range plans {
		wg.Add(1)

		go func(plan suitePlan) {
			defer wg.Done()

			r.runPlan(ctx, plan, results)
		}(plan)
	}

	wg.Wait()
}

// runPlan executes a suite's checks in order, tracking which passed so later
// checks can be skipped when a prerequisite did not.
func (r *TestRunner) runPlan(ctx context.Context, plan suitePlan, results *TestResults) {
	status := make(map[string]TestStatus, len(plan.steps))

	for _, step := range plan.steps {
		result := r.runStep(ctx, step, status)
		status[step.Name] = result.Status

		r.record(results, result)
	}
}

// runStep applies the skip rules, then runs the check with retries.
func (r *TestRunner) runStep(ctx context.Context, step testStep, status map[string]TestStatus) TestResult {
	reason := r.skipReason(step, status)
	if reason != "" {
		return TestResult{ //nolint:exhaustruct // zero values are correct
			Name:   step.Name,
			Status: TestStatusSkipped,
			Reason: reason,
		}
	}

	return r.runSingleTest(ctx, step)
}

// skipReason returns why a check will not run, or "" when it should.
func (r *TestRunner) skipReason(step testStep, status map[string]TestStatus) string {
	if step.Cleanup {
		if r.SkipCleanup {
			return "cleanup skipped by --skip-cleanup"
		}

		for _, need := range step.Needs {
			if s, ok := status[need]; !ok || s == TestStatusSkipped {
				return "nothing to clean up: " + need + " did not run"
			}
		}

		return ""
	}

	if tag, excluded := matchesAnyTag(step.Name, r.Exclude); excluded {
		return "excluded by --exclude " + tag
	}

	if len(r.Tags) > 0 {
		if _, selected := matchesAnyTag(step.Name, r.Tags); !selected {
			return "not selected by --tags"
		}
	}

	for _, need := range step.Needs {
		if status[need] != TestStatusPassed {
			return "prerequisite " + need + " did not pass"
		}
	}

	return ""
}

// matchesAnyTag reports the first tag contained in name.
func matchesAnyTag(name string, tags []string) (string, bool) {
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && strings.Contains(name, tag) {
			return tag, true
		}
	}

	return "", false
}

// runSingleTest executes a check, retrying failures up to r.Retries times.
func (r *TestRunner) runSingleTest(ctx context.Context, step testStep) TestResult {
	result := TestResult{ //nolint:exhaustruct // populated below
		Name:   step.Name,
		Status: TestStatusRunning,
	}

	start := time.Now()
	log := &stepLog{} //nolint:exhaustruct // zero value is ready to use

	for attempt := 0; attempt <= r.Retries; attempt++ {
		if attempt > 0 {
			log.Printf("retrying after failure: %s", result.Error)

			err := r.sleep(ctx, TestRetryDelaySeconds*time.Second)
			if err != nil {
				break
			}
		}

		skip, err := r.runAttempt(ctx, step, log)
		result.Retries = attempt

		if skip != "" {
			result.Status = TestStatusSkipped
			result.Reason = skip
			result.Error = ""

			break
		}

		if err == nil {
			result.Status = TestStatusPassed
			result.Error = ""

			break
		}

		result.Error = err.Error()
		result.Status = TestStatusFailed
	}

	result.Duration = time.Since(start)
	result.Output = log.String()

	return result
}

// runAttempt runs one attempt of a check under the per-check timeout.
func (r *TestRunner) runAttempt(ctx context.Context, step testStep, log *stepLog) (string, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	return step.Run(attemptCtx, log)
}

func (r *TestRunner) initializeTestResults() *TestResults {
	return &TestResults{
		Suite:     r.Suite,
		Passed:    0,
		Failed:    0,
		Skipped:   0,
		Duration:  0,
		Tests:     []TestResult{},
		Output:    "",
		StartTime: time.Now(),
	}
}

// record appends a result, updates the counters, and prints the status line.
func (r *TestRunner) record(results *TestResults, result TestResult) {
	r.mu.Lock()
	defer r.mu.Unlock()

	results.Tests = append(results.Tests, result)

	switch result.Status {
	case TestStatusPassed:
		results.Passed++
	case TestStatusFailed:
		results.Failed++
	case TestStatusSkipped:
		results.Skipped++
	case TestStatusRunning:
	}

	r.printResultLine(result)
}

// printResultLine writes one status line per check, with the output and
// error when --verbose is set.
func (r *TestRunner) printResultLine(result TestResult) {
	suffix := ""

	switch result.Status {
	case TestStatusSkipped:
		suffix = " - " + result.Reason
	case TestStatusFailed:
		suffix = " - " + firstLine(result.Error)
	case TestStatusPassed, TestStatusRunning:
	}

	r.printf("%-7s %s (%s)%s\n", result.Status, result.Name, result.Duration.Round(time.Millisecond), suffix)

	if !r.Verbose {
		return
	}

	if result.Output != "" {
		r.printf("%s", indentLines(result.Output, "    "))
	}

	if result.Error != "" {
		r.printf("%s", indentLines(result.Error+"\n", "    "))
	}
}

// printf writes to the runner's output; write errors on stdout are not
// actionable for a test report.
func (r *TestRunner) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.out, format, args...)
}

// firstLine returns the first line of s.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")

	return line
}

// lastLines returns the last n non-empty lines of s joined by newlines.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")

	kept := make([]string, 0, n)

	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			kept = append([]string{lines[i]}, kept...)
		}
	}

	return strings.Join(kept, "\n")
}

// indentLines prefixes every line of s with prefix.
func indentLines(s, prefix string) string {
	var b strings.Builder

	for line := range strings.SplitSeq(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteByte('\n')
	}

	return b.String()
}
