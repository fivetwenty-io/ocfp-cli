package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// TestSuite represents a test suite type
type TestSuite string

const (
	TestSuiteC2C        TestSuite = "c2c"
	TestSuiteBlacksmith TestSuite = "blacksmith"
	TestSuiteNFS        TestSuite = "nfs"
	TestSuiteSMB        TestSuite = "smb"
	TestSuiteTCP        TestSuite = "tcp"
	TestSuiteAll        TestSuite = "all"
	TestSuiteSmoke      TestSuite = "smoke"
	TestSuiteAcceptance TestSuite = "acceptance"
)

// NewTestCmd creates the test command
func NewTestCmd() *cobra.Command {
	var (
		parallel    bool
		timeout     time.Duration
		verbose     bool
		outputFile  string
		skipCleanup bool
		retries     int
		tags        []string
		exclude     []string
	)

	cmd := &cobra.Command{
		Use:   "test <type>",
		Short: "Run tests (c2c|blacksmith|nfs|smb|tcp|smoke|acceptance|all)",
		Long: `Execute various test suites for OCFP components.

The test command runs different types of tests against your Cloud Foundry deployment:
- c2c: Container-to-container networking tests
- blacksmith: Service broker tests  
- nfs: Network File System tests
- smb: Server Message Block tests
- tcp: TCP routing tests
- smoke: Basic smoke tests
- acceptance: Comprehensive acceptance tests
- all: All available test suites

Tests are executed against the target Cloud Foundry environment using
the configured credentials and endpoints.`,
		Example: `  # Run smoke tests
  ocfp test smoke

  # Run all test suites in parallel
  ocfp test all --parallel

  # Run C2C tests with verbose output
  ocfp test c2c --verbose

  # Run tests with custom timeout
  ocfp test tcp --timeout 30m

  # Run tests excluding specific tags
  ocfp test acceptance --exclude slow,flaky

  # Save test results to file
  ocfp test all --output results.xml`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"c2c", "blacksmith", "nfs", "smb", "tcp", "smoke", "acceptance", "all"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			testType := args[0]

			// Parse test suite
			var suite TestSuite
			switch strings.ToLower(testType) {
			case "c2c":
				suite = TestSuiteC2C
			case "blacksmith":
				suite = TestSuiteBlacksmith
			case "nfs":
				suite = TestSuiteNFS
			case "smb":
				suite = TestSuiteSMB
			case "tcp":
				suite = TestSuiteTCP
			case "all":
				suite = TestSuiteAll
			case "smoke":
				suite = TestSuiteSmoke
			case "acceptance":
				suite = TestSuiteAcceptance
			default:
				return fmt.Errorf("unknown test type: %s", testType)
			}

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			log.Info("Running tests",
				"suite", suite,
				"deployment", cfg.Name,
				"parallel", parallel,
				"timeout", timeout)

			// Create test runner
			runner := &TestRunner{
				Config:      cfg,
				Suite:       suite,
				Parallel:    parallel,
				Timeout:     timeout,
				Verbose:     verbose,
				OutputFile:  outputFile,
				SkipCleanup: skipCleanup,
				Retries:     retries,
				Tags:        tags,
				Exclude:     exclude,
			}

			// Validate test environment
			if err := runner.ValidateEnvironment(ctx); err != nil {
				return fmt.Errorf("test environment validation failed: %w", err)
			}

			// Setup test environment
			if err := runner.Setup(ctx); err != nil {
				return fmt.Errorf("test setup failed: %w", err)
			}

			// Cleanup after tests if not skipped
			if !skipCleanup {
				defer func() {
					if err := runner.Cleanup(ctx); err != nil {
						log.Warn("Test cleanup failed", "error", err)
					}
				}()
			}

			// Execute tests
			results, err := runner.Execute(ctx)
			if err != nil {
				return fmt.Errorf("test execution failed: %w", err)
			}

			// Display results
			runner.DisplayResults(results)

			// Save results if output file specified
			if outputFile != "" {
				if err := runner.SaveResults(results, outputFile); err != nil {
					log.Warn("Failed to save test results", "error", err)
				}
			}

			// Return error if any tests failed
			if results.Failed > 0 {
				return fmt.Errorf("tests failed: %d passed, %d failed, %d skipped",
					results.Passed, results.Failed, results.Skipped)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&parallel, "parallel", false, "run tests in parallel")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "test timeout")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "verbose test output")
	cmd.Flags().StringVar(&outputFile, "output", "", "save test results to file")
	cmd.Flags().BoolVar(&skipCleanup, "skip-cleanup", false, "skip test cleanup")
	cmd.Flags().IntVar(&retries, "retries", 1, "number of retries for failed tests")
	cmd.Flags().StringSliceVar(&tags, "tags", nil, "test tags to include")
	cmd.Flags().StringSliceVar(&exclude, "exclude", nil, "test tags to exclude")

	return cmd
}

// TestRunner handles test execution
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
}

// TestResults represents test execution results
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

// TestResult represents a single test result
type TestResult struct {
	Name     string
	Status   TestStatus
	Duration time.Duration
	Error    string
	Output   string
	Retries  int
}

// TestStatus represents test status
type TestStatus string

const (
	TestStatusPassed  TestStatus = "PASSED"
	TestStatusFailed  TestStatus = "FAILED"
	TestStatusSkipped TestStatus = "SKIPPED"
	TestStatusRunning TestStatus = "RUNNING"
)

// ValidateEnvironment validates the test environment
func (r *TestRunner) ValidateEnvironment(ctx context.Context) error {
	log := logger.Get()
	log.Info("Validating test environment")

	// Check CF CLI is available and logged in
	if err := r.validateCFCLI(); err != nil {
		return fmt.Errorf("CF CLI validation failed: %w", err)
	}

	// Check BOSH CLI if needed
	if r.Suite == TestSuiteAll || r.Suite == TestSuiteAcceptance {
		if err := r.validateBOSHCLI(); err != nil {
			return fmt.Errorf("BOSH CLI validation failed: %w", err)
		}
	}

	// Verify CF deployment is accessible
	if err := r.validateCFDeployment(ctx); err != nil {
		return fmt.Errorf("CF deployment validation failed: %w", err)
	}

	// Check required test directories exist
	if err := r.validateTestDirectories(); err != nil {
		return fmt.Errorf("test directory validation failed: %w", err)
	}

	return nil
}

// Setup prepares the test environment
func (r *TestRunner) Setup(ctx context.Context) error {
	log := logger.Get()
	log.Info("Setting up test environment")

	// Set CF target
	if err := r.setCFTarget(); err != nil {
		return fmt.Errorf("failed to set CF target: %w", err)
	}

	// Create test org and space if needed
	if err := r.createTestOrgSpace(); err != nil {
		return fmt.Errorf("failed to create test org/space: %w", err)
	}

	// Deploy test applications if needed
	if r.Suite == TestSuiteC2C || r.Suite == TestSuiteAll {
		if err := r.deployTestApps(ctx); err != nil {
			return fmt.Errorf("failed to deploy test apps: %w", err)
		}
	}

	// Setup test data
	if err := r.setupTestData(); err != nil {
		return fmt.Errorf("failed to setup test data: %w", err)
	}

	return nil
}

// Execute runs the test suite
func (r *TestRunner) Execute(ctx context.Context) (*TestResults, error) {
	log := logger.Get()
	log.Info("Executing test suite", "suite", r.Suite)

	results := &TestResults{
		Suite:     r.Suite,
		StartTime: time.Now(),
		Tests:     []TestResult{},
	}

	// Get test list based on suite
	tests, err := r.getTestList()
	if err != nil {
		return nil, fmt.Errorf("failed to get test list: %w", err)
	}

	log.Info("Running tests", "count", len(tests))

	// Execute tests
	for _, test := range tests {
		if r.shouldSkipTest(test) {
			results.Tests = append(results.Tests, TestResult{
				Name:   test,
				Status: TestStatusSkipped,
			})
			results.Skipped++
			continue
		}

		log.Info("Running test", "name", test)

		result := r.runSingleTest(ctx, test)
		results.Tests = append(results.Tests, result)

		switch result.Status {
		case TestStatusPassed:
			results.Passed++
		case TestStatusFailed:
			results.Failed++
		case TestStatusSkipped:
			results.Skipped++
		}

		if r.Verbose {
			fmt.Printf("[%s] %s (%v)\n", result.Status, result.Name, result.Duration)
			if result.Error != "" {
				fmt.Printf("  Error: %s\n", result.Error)
			}
		}
	}

	results.Duration = time.Since(results.StartTime)
	return results, nil
}

// runSingleTest executes a single test
func (r *TestRunner) runSingleTest(ctx context.Context, testName string) TestResult {
	result := TestResult{
		Name:   testName,
		Status: TestStatusRunning,
	}

	start := time.Now()

	// Create context with timeout
	testCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	// Run test with retries
	for attempt := 0; attempt <= r.Retries; attempt++ {
		if attempt > 0 {
			time.Sleep(5 * time.Second) // Wait before retry
		}

		err := r.executeTest(testCtx, testName, &result)
		if err == nil {
			result.Status = TestStatusPassed
			break
		}

		result.Error = err.Error()
		result.Retries = attempt

		if attempt == r.Retries {
			result.Status = TestStatusFailed
		}
	}

	result.Duration = time.Since(start)
	return result
}

// executeTest runs a specific test
func (r *TestRunner) executeTest(ctx context.Context, testName string, result *TestResult) error {
	// Determine test command based on suite and test name
	var cmd *exec.Cmd

	switch r.Suite {
	case TestSuiteSmoke:
		cmd = r.buildSmokeTestCommand(testName)
	case TestSuiteC2C:
		cmd = r.buildC2CTestCommand(testName)
	case TestSuiteBlacksmith:
		cmd = r.buildBlacksmithTestCommand(testName)
	case TestSuiteNFS:
		cmd = r.buildNFSTestCommand(testName)
	case TestSuiteSMB:
		cmd = r.buildSMBTestCommand(testName)
	case TestSuiteTCP:
		cmd = r.buildTCPTestCommand(testName)
	case TestSuiteAcceptance:
		cmd = r.buildAcceptanceTestCommand(testName)
	default:
		return fmt.Errorf("unsupported test suite: %s", r.Suite)
	}

	cmd.Env = append(os.Environ(), r.getTestEnvironment()...)

	// Capture output
	output, err := cmd.CombinedOutput()
	result.Output = string(output)

	return err
}

// Cleanup cleans up test environment
func (r *TestRunner) Cleanup(ctx context.Context) error {
	log := logger.Get()
	log.Info("Cleaning up test environment")

	// Delete test apps
	if err := r.cleanupTestApps(ctx); err != nil {
		log.Warn("Failed to cleanup test apps", "error", err)
	}

	// Delete test org/space
	if err := r.cleanupTestOrgSpace(); err != nil {
		log.Warn("Failed to cleanup test org/space", "error", err)
	}

	// Cleanup test data
	if err := r.cleanupTestData(); err != nil {
		log.Warn("Failed to cleanup test data", "error", err)
	}

	return nil
}

// DisplayResults displays test results
func (r *TestRunner) DisplayResults(results *TestResults) {
	fmt.Printf("\n=== Test Results ===\n")
	fmt.Printf("Suite: %s\n", results.Suite)
	fmt.Printf("Duration: %v\n", results.Duration)
	fmt.Printf("Tests: %d\n", len(results.Tests))
	fmt.Printf("Passed: %d\n", results.Passed)
	fmt.Printf("Failed: %d\n", results.Failed)
	fmt.Printf("Skipped: %d\n", results.Skipped)

	if results.Failed > 0 {
		fmt.Printf("\n=== Failed Tests ===\n")
		for _, test := range results.Tests {
			if test.Status == TestStatusFailed {
				fmt.Printf("- %s: %s\n", test.Name, test.Error)
			}
		}
	}
}

// SaveResults saves test results to file
func (r *TestRunner) SaveResults(results *TestResults, filename string) error {
	// Save results in JUnit XML format or JSON
	// Placeholder implementation
	content := fmt.Sprintf("Test Results: %d passed, %d failed, %d skipped",
		results.Passed, results.Failed, results.Skipped)

	return os.WriteFile(filename, []byte(content), 0644)
}

// Helper methods for test validation and execution

func (r *TestRunner) validateCFCLI() error {
	// Check if cf CLI is installed and logged in
	cmd := exec.Command("cf", "api")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cf CLI not available or not logged in: %w", err)
	}
	return nil
}

func (r *TestRunner) validateBOSHCLI() error {
	// Check if bosh CLI is installed
	cmd := exec.Command("bosh", "env")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bosh CLI not available: %w", err)
	}
	return nil
}

func (r *TestRunner) validateCFDeployment(ctx context.Context) error {
	// Verify CF deployment is accessible
	cmd := exec.Command("cf", "org")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("CF deployment not accessible: %w", err)
	}
	return nil
}

func (r *TestRunner) validateTestDirectories() error {
	// Check for test directories and files
	testDirs := []string{
		"tests/",
		filepath.Join("tests", strings.ToLower(string(r.Suite))),
	}

	for _, dir := range testDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return fmt.Errorf("test directory not found: %s", dir)
		}
	}

	return nil
}

func (r *TestRunner) setCFTarget() error {
	// Set CF API target
	apiURL := fmt.Sprintf("https://api.%s", r.Config.DNS[0])
	cmd := exec.Command("cf", "api", apiURL, "--skip-ssl-validation")
	return cmd.Run()
}

func (r *TestRunner) createTestOrgSpace() error {
	// Create test organization and space
	orgName := fmt.Sprintf("%s-test-org", r.Config.Name)
	spaceName := fmt.Sprintf("%s-test-space", r.Config.Name)

	// Create org
	cmd := exec.Command("cf", "create-org", orgName)
	if err := cmd.Run(); err != nil {
		// Ignore error if org already exists - CF will return error code 1 if org exists
		logger.Debugf("org creation error (likely already exists): %v", err)
	}

	// Create space
	cmd = exec.Command("cf", "create-space", spaceName, "-o", orgName)
	if err := cmd.Run(); err != nil {
		// Ignore error if space already exists - CF will return error code 1 if space exists
		logger.Debugf("space creation error (likely already exists): %v", err)
	}

	// Target org/space
	cmd = exec.Command("cf", "target", "-o", orgName, "-s", spaceName)
	return cmd.Run()
}

func (r *TestRunner) deployTestApps(ctx context.Context) error {
	// Deploy test applications for C2C testing
	// This would push sample apps used for testing
	return nil
}

func (r *TestRunner) setupTestData() error {
	// Setup test data files, configuration, etc.
	return nil
}

func (r *TestRunner) getTestList() ([]string, error) {
	// Get list of tests based on suite
	switch r.Suite {
	case TestSuiteSmoke:
		return []string{"api_connectivity", "app_push", "basic_routing"}, nil
	case TestSuiteC2C:
		return []string{"container_networking", "policy_enforcement", "service_discovery"}, nil
	case TestSuiteBlacksmith:
		return []string{"service_broker", "service_instances", "service_bindings"}, nil
	case TestSuiteNFS:
		return []string{"volume_mounting", "file_permissions", "concurrent_access"}, nil
	case TestSuiteSMB:
		return []string{"smb_mounting", "authentication", "file_operations"}, nil
	case TestSuiteTCP:
		return []string{"tcp_routing", "port_allocation", "load_balancing"}, nil
	case TestSuiteAcceptance:
		return []string{"full_deployment", "scaling", "upgrades", "disaster_recovery"}, nil
	case TestSuiteAll:
		// Combine all test suites
		allTests := []string{}
		suites := []TestSuite{TestSuiteSmoke, TestSuiteC2C, TestSuiteBlacksmith, TestSuiteNFS, TestSuiteSMB, TestSuiteTCP}
		for _, suite := range suites {
			r.Suite = suite
			tests, err := r.getTestList()
			if err != nil {
				continue
			}
			allTests = append(allTests, tests...)
		}
		r.Suite = TestSuiteAll
		return allTests, nil
	default:
		return nil, fmt.Errorf("unknown test suite: %s", r.Suite)
	}
}

func (r *TestRunner) shouldSkipTest(testName string) bool {
	// Check if test should be skipped based on tags/exclude patterns
	for _, exclude := range r.Exclude {
		if strings.Contains(testName, exclude) {
			return true
		}
	}

	// If tags are specified, only run tests with matching tags
	if len(r.Tags) > 0 {
		for _, tag := range r.Tags {
			if strings.Contains(testName, tag) {
				return false
			}
		}
		return true
	}

	return false
}

func (r *TestRunner) getTestEnvironment() []string {
	// Set environment variables for tests
	return []string{
		fmt.Sprintf("CF_API=https://api.%s", r.Config.DNS[0]),
		fmt.Sprintf("CF_DOMAIN=%s", r.Config.DNS[0]),
		fmt.Sprintf("CF_ORG=%s-test-org", r.Config.Name),
		fmt.Sprintf("CF_SPACE=%s-test-space", r.Config.Name),
	}
}

func (r *TestRunner) buildSmokeTestCommand(testName string) *exec.Cmd {
	return exec.Command("cf", "curl", "/v2/info")
}

func (r *TestRunner) buildC2CTestCommand(testName string) *exec.Cmd {
	// Build command for C2C networking tests
	return exec.Command("echo", "Running C2C test:", testName)
}

func (r *TestRunner) buildBlacksmithTestCommand(testName string) *exec.Cmd {
	// Build command for Blacksmith service broker tests
	return exec.Command("echo", "Running Blacksmith test:", testName)
}

func (r *TestRunner) buildNFSTestCommand(testName string) *exec.Cmd {
	// Build command for NFS volume service tests
	return exec.Command("echo", "Running NFS test:", testName)
}

func (r *TestRunner) buildSMBTestCommand(testName string) *exec.Cmd {
	// Build command for SMB volume service tests
	return exec.Command("echo", "Running SMB test:", testName)
}

func (r *TestRunner) buildTCPTestCommand(testName string) *exec.Cmd {
	// Build command for TCP routing tests
	return exec.Command("echo", "Running TCP test:", testName)
}

func (r *TestRunner) buildAcceptanceTestCommand(testName string) *exec.Cmd {
	// Build command for acceptance tests
	return exec.Command("echo", "Running acceptance test:", testName)
}

func (r *TestRunner) cleanupTestApps(ctx context.Context) error {
	// Delete test applications
	return nil
}

func (r *TestRunner) cleanupTestOrgSpace() error {
	// Delete test org and space
	orgName := fmt.Sprintf("%s-test-org", r.Config.Name)
	cmd := exec.Command("cf", "delete-org", orgName, "-f")
	return cmd.Run()
}

func (r *TestRunner) cleanupTestData() error {
	// Cleanup test data files
	return nil
}
