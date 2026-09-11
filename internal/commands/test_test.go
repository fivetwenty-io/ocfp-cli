package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedRunner is a commandRunner whose answers are keyed by the longest
// matching prefix of "name arg arg ...". Each key holds a queue of answers;
// the last answer repeats once the queue is drained, so a poll can move
// from "in progress" to "succeeded" and stay there.
type scriptedRunner struct {
	mu        sync.Mutex
	responses map[string][]scriptedResponse
	calls     []string
	missing   map[string]bool
}

type scriptedResponse struct {
	out string
	err error
}

var errScripted = errors.New("exit status 1")

func newScriptedRunner() *scriptedRunner {
	return &scriptedRunner{
		mu:        sync.Mutex{},
		responses: map[string][]scriptedResponse{},
		calls:     nil,
		missing:   map[string]bool{},
	}
}

// on queues a successful answer for commands starting with prefix.
func (s *scriptedRunner) on(prefix, out string) *scriptedRunner {
	s.responses[prefix] = append(s.responses[prefix], scriptedResponse{out: out, err: nil})

	return s
}

// fail queues a failing answer for commands starting with prefix.
func (s *scriptedRunner) fail(prefix, out string) *scriptedRunner {
	s.responses[prefix] = append(s.responses[prefix], scriptedResponse{out: out, err: errScripted})

	return s
}

func (s *scriptedRunner) lookup(name string, args []string) scriptedResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	cmd := strings.TrimSpace(name + " " + strings.Join(args, " "))
	s.calls = append(s.calls, cmd)

	best := ""

	for prefix := range s.responses {
		if strings.HasPrefix(cmd, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}

	if best == "" {
		return scriptedResponse{out: "", err: nil}
	}

	queue := s.responses[best]
	resp := queue[0]

	if len(queue) > 1 {
		s.responses[best] = queue[1:]
	}

	return resp
}

func (s *scriptedRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	resp := s.lookup(name, args)

	return []byte(resp.out), resp.err
}

func (s *scriptedRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	resp := s.lookup(name, args)

	return []byte(resp.out), resp.err
}

func (s *scriptedRunner) RunSplit(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	resp := s.lookup(name, args)

	return []byte(resp.out), nil, resp.err
}

func (s *scriptedRunner) LookPath(name string) error {
	if s.missing[name] {
		return errors.New("lookpath " + name + ": executable file not found in $PATH")
	}

	return nil
}

// called reports whether any recorded command starts with prefix.
func (s *scriptedRunner) called(prefix string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}

	return false
}

// installScriptedRunner swaps the package-level runner seam for the test's
// lifetime. Tests that call it must not run in parallel with each other.
func installScriptedRunner(t *testing.T, fake *scriptedRunner) {
	t.Helper()

	orig := runner
	runner = fake

	t.Cleanup(func() { runner = orig })
}

// emptyMarketplaceRunner answers the marketplace reads with empty lists and
// everything else with nothing.
func emptyMarketplaceRunner() *scriptedRunner {
	return newScriptedRunner().
		on("cf curl /v3/service_brokers", ccList()).
		on("cf curl /v3/service_offerings", ccList()).
		on("cf curl /v3/service_plans", ccList())
}

// ccList renders a single-page v3 list response.
func ccList(resources ...string) string {
	return `{"pagination":{"next":null},"resources":[` + strings.Join(resources, ",") + `]}`
}

// newTestRunnerForUnit returns a runner with every seam pointed at fast,
// local stand-ins and the Setup-discovered fields pre-populated.
func newTestRunnerForUnit(suite TestSuite) *TestRunner {
	return &TestRunner{ //nolint:exhaustruct // fields under test are set explicitly
		Suite:          suite,
		Timeout:        5 * time.Second,
		Retries:        0,
		ServiceTimeout: 500 * time.Millisecond,
		runID:          "abc123",
		orgName:        "lab-test-org",
		spaceName:      "lab-test-space",
		orgGUID:        "org-guid",
		spaceGUID:      "space-guid",
		apiURL:         "https://api.system.example.com",
		appsDomain:     "apps.example.com",
		domains: []cfDomain{
			{GUID: "d1", Name: "apps.example.com", Internal: false, Protocols: []string{"http"}, Shared: true},
			{GUID: "d2", Name: "apps.internal", Internal: true, Protocols: []string{"http"}, Shared: true},
		},
		routeWait: 300 * time.Millisecond,
		sleep: func(_ context.Context, _ time.Duration) error {
			time.Sleep(time.Millisecond)

			return nil
		},
		out: &bytes.Buffer{},
	}
}

func resultByName(results *TestResults, name string) (TestResult, bool) {
	for _, r := range results.Tests {
		if r.Name == name {
			return r, true
		}
	}

	return TestResult{}, false //nolint:exhaustruct // not found
}

func TestParseTestSuite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    TestSuite
		wantErr bool
	}{
		{in: "smoke", want: TestSuiteSmoke, wantErr: false},
		{in: "SMOKE", want: TestSuiteSmoke, wantErr: false},
		{in: "c2c", want: TestSuiteC2C, wantErr: false},
		{in: "blacksmith", want: TestSuiteBlacksmith, wantErr: false},
		{in: "nfs", want: TestSuiteNFS, wantErr: false},
		{in: "smb", want: TestSuiteSMB, wantErr: false},
		{in: "tcp", want: TestSuiteTCP, wantErr: false},
		{in: "acceptance", want: TestSuiteAcceptance, wantErr: false},
		{in: "all", want: TestSuiteAll, wantErr: false},
		{in: "bogus", want: TestSuiteSmoke, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			got, err := parseTestSuite(tt.in)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewTestCmdFlags(t *testing.T) {
	t.Parallel()

	cmd := NewTestCmd()

	for _, name := range []string{
		"parallel", "timeout", "verbose", "output", "skip-cleanup", "retries", "tags", "exclude",
		"api", "skip-ssl-validation", "offering", "plan", "bosh-env", "bosh-deployment",
		"tcp-domain", "nfs-share", "smb-share", "smb-username", "smb-password", "service-timeout",
	} {
		assert.NotNil(t, cmd.Flags().Lookup(name), "flag %s", name)
	}

	require.NoError(t, cmd.Flags().Parse([]string{
		"--offering", "redis", "--plan", "standalone", "--bosh-env", "ocf", "--tcp-domain", "tcp.example.com",
		"--nfs-share", "10.0.0.5:/export", "--tags", "push,route", "--exclude", "tcp", "--service-timeout", "3m",
		"--skip-ssl-validation",
	}))

	offering, err := cmd.Flags().GetString("offering")
	require.NoError(t, err)
	assert.Equal(t, "redis", offering)

	tags, err := cmd.Flags().GetStringSlice("tags")
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "route"}, tags)

	serviceTimeout, err := cmd.Flags().GetDuration("service-timeout")
	require.NoError(t, err)
	assert.Equal(t, 3*time.Minute, serviceTimeout)

	timeout, err := cmd.Flags().GetDuration("timeout")
	require.NoError(t, err)
	assert.Equal(t, DefaultTestTimeoutMinutes*time.Minute, timeout)

	retries, err := cmd.Flags().GetInt("retries")
	require.NoError(t, err)
	assert.Equal(t, 1, retries)

	assert.True(t, cmd.Flags().Changed("skip-ssl-validation"))
	assert.Contains(t, cmd.Long, "smoke")
	assert.Contains(t, cmd.Long, "blacksmith")
	assert.Contains(t, cmd.Long, "acceptance")
}

func TestSkipReason(t *testing.T) {
	t.Parallel()

	passed := map[string]TestStatus{"smoke/push_app": TestStatusPassed}
	failed := map[string]TestStatus{"smoke/push_app": TestStatusFailed}
	skipped := map[string]TestStatus{"smoke/push_app": TestStatusSkipped}

	tests := []struct {
		name   string
		runner *TestRunner
		step   testStep
		status map[string]TestStatus
		want   string
	}{
		{
			name:   "plain step runs",
			runner: &TestRunner{}, //nolint:exhaustruct // defaults
			step:   testStep{Name: "smoke/push_app", Run: nil, Needs: nil, Cleanup: false},
			status: map[string]TestStatus{},
			want:   "",
		},
		{
			name:   "excluded by tag",
			runner: &TestRunner{Exclude: []string{"tcp", "route"}}, //nolint:exhaustruct // defaults
			step:   testStep{Name: "smoke/route_https_200", Run: nil, Needs: nil, Cleanup: false},
			status: map[string]TestStatus{},
			want:   "excluded by --exclude route",
		},
		{
			name:   "not selected by tags",
			runner: &TestRunner{Tags: []string{"c2c"}}, //nolint:exhaustruct // defaults
			step:   testStep{Name: "smoke/push_app", Run: nil, Needs: nil, Cleanup: false},
			status: map[string]TestStatus{},
			want:   "not selected by --tags",
		},
		{
			name:   "selected by tags",
			runner: &TestRunner{Tags: []string{"smoke"}}, //nolint:exhaustruct // defaults
			step:   testStep{Name: "smoke/push_app", Run: nil, Needs: nil, Cleanup: false},
			status: map[string]TestStatus{},
			want:   "",
		},
		{
			name:   "prerequisite failed",
			runner: &TestRunner{}, //nolint:exhaustruct // defaults
			step:   testStep{Name: "smoke/route_https_200", Run: nil, Needs: []string{"smoke/push_app"}, Cleanup: false},
			status: failed,
			want:   "prerequisite smoke/push_app did not pass",
		},
		{
			name:   "prerequisite passed",
			runner: &TestRunner{}, //nolint:exhaustruct // defaults
			step:   testStep{Name: "smoke/route_https_200", Run: nil, Needs: []string{"smoke/push_app"}, Cleanup: false},
			status: passed,
			want:   "",
		},
		{
			name:   "cleanup runs after failure",
			runner: &TestRunner{}, //nolint:exhaustruct // defaults
			step:   testStep{Name: "smoke/delete_app", Run: nil, Needs: []string{"smoke/push_app"}, Cleanup: true},
			status: failed,
			want:   "",
		},
		{
			name:   "cleanup skipped when creation skipped",
			runner: &TestRunner{}, //nolint:exhaustruct // defaults
			step:   testStep{Name: "smoke/delete_app", Run: nil, Needs: []string{"smoke/push_app"}, Cleanup: true},
			status: skipped,
			want:   "nothing to clean up: smoke/push_app did not run",
		},
		{
			name:   "cleanup ignores exclude",
			runner: &TestRunner{Exclude: []string{"delete"}}, //nolint:exhaustruct // defaults
			step:   testStep{Name: "smoke/delete_app", Run: nil, Needs: []string{"smoke/push_app"}, Cleanup: true},
			status: passed,
			want:   "",
		},
		{
			name:   "cleanup honours skip-cleanup",
			runner: &TestRunner{SkipCleanup: true}, //nolint:exhaustruct // defaults
			step:   testStep{Name: "smoke/delete_app", Run: nil, Needs: []string{"smoke/push_app"}, Cleanup: true},
			status: passed,
			want:   "cleanup skipped by --skip-cleanup",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.runner.skipReason(tt.step, tt.status))
		})
	}
}

func TestRunPlanAccounting(t *testing.T) {
	t.Parallel()

	r := newTestRunnerForUnit(TestSuiteSmoke)
	r.Retries = 2

	attempts := 0
	pass := func(_ context.Context, log *stepLog) (string, error) {
		log.Printf("ok")

		return "", nil
	}
	flaky := func(_ context.Context, _ *stepLog) (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("not yet")
		}

		return "", nil
	}
	skip := func(_ context.Context, _ *stepLog) (string, error) {
		return "no TCP domain on the foundation", nil
	}
	fail := func(_ context.Context, _ *stepLog) (string, error) {
		return "", errors.New("boom")
	}

	plan := suitePlan{suite: TestSuiteSmoke, steps: []testStep{
		{Name: "s/a", Run: pass, Needs: nil, Cleanup: false},
		{Name: "s/b", Run: flaky, Needs: []string{"s/a"}, Cleanup: false},
		{Name: "s/c", Run: skip, Needs: nil, Cleanup: false},
		{Name: "s/d", Run: fail, Needs: []string{"s/c"}, Cleanup: false},
		{Name: "s/e", Run: fail, Needs: nil, Cleanup: false},
		{Name: "s/f", Run: pass, Needs: []string{"s/e"}, Cleanup: false},
		{Name: "s/cleanup_e", Run: pass, Needs: []string{"s/e"}, Cleanup: true},
		{Name: "s/cleanup_c", Run: pass, Needs: []string{"s/c"}, Cleanup: true},
	}}

	results := r.initializeTestResults()
	r.runPlan(context.Background(), plan, results)

	assert.Equal(t, 3, results.Passed, "a, b (after retries), cleanup_e")
	assert.Equal(t, 1, results.Failed, "e")
	assert.Equal(t, 4, results.Skipped, "c, d, f, cleanup_c")
	assert.Len(t, results.Tests, 8)

	b, ok := resultByName(results, "s/b")
	require.True(t, ok)
	assert.Equal(t, TestStatusPassed, b.Status)
	assert.Equal(t, 2, b.Retries)
	assert.Empty(t, b.Error)

	d, ok := resultByName(results, "s/d")
	require.True(t, ok)
	assert.Equal(t, TestStatusSkipped, d.Status)
	assert.Equal(t, "prerequisite s/c did not pass", d.Reason)

	e, ok := resultByName(results, "s/e")
	require.True(t, ok)
	assert.Equal(t, TestStatusFailed, e.Status)
	assert.Equal(t, "boom", e.Error)
	assert.Equal(t, 2, e.Retries)

	cleanupC, ok := resultByName(results, "s/cleanup_c")
	require.True(t, ok)
	assert.Equal(t, TestStatusSkipped, cleanupC.Status)
	assert.Contains(t, cleanupC.Reason, "nothing to clean up")

	out := r.out.(*bytes.Buffer).String() //nolint:forcetypeassert // set by newTestRunnerForUnit
	assert.Contains(t, out, "SKIPPED s/c")
	assert.Contains(t, out, "no TCP domain on the foundation")
	assert.Contains(t, out, "FAILED  s/e")
}

func TestExecuteAllRunsEverySuiteAndCountsSkips(t *testing.T) {
	fake := emptyMarketplaceRunner()
	installScriptedRunner(t, fake)

	// The marketplace is empty and every other cf call answers with nothing,
	// so cf push "succeeds" but the app lookup finds nothing, and each suite
	// fails or skips at its first real check.
	r := newTestRunnerForUnit(TestSuiteAll)
	r.domains = r.domains[:1] // no internal domain, no TCP domain

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	suites := map[string]bool{}

	for _, res := range results.Tests {
		suite, _ := splitStepName(res.Name)
		suites[suite] = true
	}

	assert.Equal(t, map[string]bool{"smoke": true, "c2c": true, "blacksmith": true, "nfs": true, "smb": true, "tcp": true}, suites)
	assert.Positive(t, results.Skipped)
	assert.Equal(t, results.Passed+results.Failed+results.Skipped, len(results.Tests))

	push, ok := resultByName(results, "smoke/push_app")
	require.True(t, ok)
	assert.Equal(t, TestStatusFailed, push.Status, "app lookup after push finds nothing")

	route, ok := resultByName(results, "smoke/route_https_200")
	require.True(t, ok)
	assert.Equal(t, TestStatusSkipped, route.Status)
	assert.Equal(t, "prerequisite smoke/push_app did not pass", route.Reason)

	deleteApp, ok := resultByName(results, "smoke/delete_app")
	require.True(t, ok)
	assert.Equal(t, TestStatusPassed, deleteApp.Status, "cleanup still runs after a failed push")

	tcp, ok := resultByName(results, "tcp/find_tcp_domain")
	require.True(t, ok)
	assert.Equal(t, TestStatusSkipped, tcp.Status)
	assert.Equal(t, "no TCP domain on the foundation", tcp.Reason)

	nfs, ok := resultByName(results, "nfs/find_broker")
	require.True(t, ok)
	assert.Equal(t, TestStatusSkipped, nfs.Status)
	assert.Contains(t, nfs.Reason, "no nfs volume service offering")

	blacksmith, ok := resultByName(results, "blacksmith/find_offering")
	require.True(t, ok)
	assert.Equal(t, TestStatusSkipped, blacksmith.Status)
	assert.Contains(t, blacksmith.Reason, "no Blacksmith service offering")

	c2c, ok := resultByName(results, "c2c/find_internal_domain")
	require.True(t, ok)
	assert.Equal(t, TestStatusFailed, c2c.Status)
	assert.Contains(t, c2c.Error, "no internal domain")
}

func TestExecuteParallelProducesSameAccounting(t *testing.T) {
	installScriptedRunner(t, emptyMarketplaceRunner())

	sequential := newTestRunnerForUnit(TestSuiteAll)
	seqResults, err := sequential.Execute(context.Background())
	require.NoError(t, err)

	parallel := newTestRunnerForUnit(TestSuiteAll)
	parallel.Parallel = true
	parResults, err := parallel.Execute(context.Background())
	require.NoError(t, err)

	assert.Equal(t, len(seqResults.Tests), len(parResults.Tests))
	assert.Equal(t, seqResults.Passed, parResults.Passed)
	assert.Equal(t, seqResults.Failed, parResults.Failed)
	assert.Equal(t, seqResults.Skipped, parResults.Skipped)
}

const (
	brokersJSON = `{"guid":"b1","name":"blacksmith"}`
	nfsBroker   = `{"guid":"b2","name":"nfsbroker"}`

	redisOffering = `{"guid":"o1","name":"redis","tags":["redis"],"relationships":{"service_broker":{"data":{"guid":"b1"}}}}`
	pgOffering    = `{"guid":"o2","name":"postgresql","tags":["postgres"],"relationships":{"service_broker":{"data":{"guid":"b1"}}}}`
	nfsOffering   = `{"guid":"o3","name":"nfs","tags":["nfs"],"relationships":{"service_broker":{"data":{"guid":"b2"}}}}`

	redisAdminPlan  = `{"guid":"p1","name":"cluster","available":true,"visibility_type":"admin","relationships":{"service_offering":{"data":{"guid":"o1"}}}}`
	redisPublicPlan = `{"guid":"p2","name":"standalone","available":true,"visibility_type":"public","relationships":{"service_offering":{"data":{"guid":"o1"}}}}`
	pgOffPlan       = `{"guid":"p3","name":"small","available":false,"visibility_type":"public","relationships":{"service_offering":{"data":{"guid":"o2"}}}}`
	nfsPlan         = `{"guid":"p4","name":"Existing","available":true,"visibility_type":"public","relationships":{"service_offering":{"data":{"guid":"o3"}}}}`
)

func rawList(items ...string) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		out = append(out, json.RawMessage(item))
	}

	return out
}

func sampleMarketplace(t *testing.T) []marketplaceOffering {
	t.Helper()

	offerings, err := parseMarketplace(
		rawList(brokersJSON, nfsBroker),
		rawList(pgOffering, redisOffering, nfsOffering),
		rawList(redisAdminPlan, redisPublicPlan, pgOffPlan, nfsPlan),
	)
	require.NoError(t, err)

	return offerings
}

func TestParseMarketplace(t *testing.T) {
	t.Parallel()

	offerings := sampleMarketplace(t)
	require.Len(t, offerings, 3)

	assert.Equal(t, "postgresql", offerings[0].Name, "order follows the Cloud Controller")
	assert.Equal(t, "blacksmith", offerings[0].Broker)
	assert.Len(t, offerings[0].Plans, 1)
	assert.False(t, offerings[0].Plans[0].Available)

	assert.Equal(t, "redis", offerings[1].Name)
	assert.Len(t, offerings[1].Plans, 2)
	assert.Equal(t, "standalone", offerings[1].firstAvailablePlan().Name, "public plan preferred")

	assert.Equal(t, "nfs", offerings[2].Name)
	assert.Equal(t, "nfsbroker", offerings[2].Broker)
	assert.True(t, offerings[2].hasTag("NFS"))

	_, err := parseMarketplace(rawList(`{"guid":`), nil, nil)
	require.Error(t, err)
}

func TestSelectOffering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		offering     string
		plan         string
		what         string
		match        func(marketplaceOffering) bool
		wantOffering string
		wantPlan     string
		wantSkip     string
		wantErr      error
	}{
		{
			name: "first blacksmith offering with an available plan", offering: "", plan: "", what: "Blacksmith", match: isBlacksmithOffering,
			wantOffering: "redis", wantPlan: "standalone", wantSkip: "", wantErr: nil,
		},
		{
			name: "explicit offering and plan", offering: "redis", plan: "cluster", what: "Blacksmith", match: isBlacksmithOffering,
			wantOffering: "redis", wantPlan: "cluster", wantSkip: "", wantErr: nil,
		},
		{
			name: "explicit offering missing", offering: "mysql", plan: "", what: "Blacksmith", match: isBlacksmithOffering,
			wantOffering: "", wantPlan: "", wantSkip: "", wantErr: ErrTestOfferingNotFound,
		},
		{
			name: "explicit plan missing", offering: "redis", plan: "huge", what: "Blacksmith", match: isBlacksmithOffering,
			wantOffering: "", wantPlan: "", wantSkip: "", wantErr: ErrTestPlanNotFound,
		},
		{
			name: "explicit offering without available plan", offering: "postgresql", plan: "", what: "Blacksmith", match: isBlacksmithOffering,
			wantOffering: "", wantPlan: "", wantSkip: "", wantErr: ErrTestPlanNotFound,
		},
		{
			name: "volume matcher by name", offering: "", plan: "", what: "nfs volume", match: volumeOfferingMatcher("nfs"),
			wantOffering: "nfs", wantPlan: "Existing", wantSkip: "", wantErr: nil,
		},
		{
			name: "nothing matches is a skip", offering: "", plan: "", what: "smb volume", match: volumeOfferingMatcher("smb"),
			wantOffering: "", wantPlan: "", wantSkip: "no smb volume service offering with an available plan in the marketplace", wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			offering, plan, skip, err := selectOffering(sampleMarketplace(t), tt.offering, tt.plan, tt.what, tt.match)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantSkip, skip)

			if tt.wantSkip != "" {
				assert.Nil(t, offering)

				return
			}

			require.NotNil(t, offering)
			require.NotNil(t, plan)
			assert.Equal(t, tt.wantOffering, offering.Name)
			assert.Equal(t, tt.wantPlan, plan.Name)
		})
	}
}

func TestParseCFDomainsAndFinders(t *testing.T) {
	t.Parallel()

	domains, err := parseCFDomains(rawList(
		`{"guid":"d1","name":"apps.example.com","internal":false,"supported_protocols":["http"],"relationships":{"organization":{"data":null}}}`,
		`{"guid":"d2","name":"apps.internal","internal":true,"supported_protocols":["http"],"relationships":{"organization":{"data":null}}}`,
		`{"guid":"d3","name":"tcp.example.com","internal":false,"supported_protocols":["tcp"],"relationships":{"organization":{"data":null}}}`,
		`{"guid":"d4","name":"private.example.com","internal":false,"supported_protocols":["http"],"relationships":{"organization":{"data":{"guid":"org"}}}}`,
	))
	require.NoError(t, err)
	require.Len(t, domains, 4)

	assert.True(t, domains[0].Shared)
	assert.False(t, domains[3].Shared)
	assert.True(t, domains[2].isTCP())
	assert.False(t, domains[2].isHTTP())

	assert.Equal(t, "apps.example.com", firstHTTPDomain(domains))
	assert.Equal(t, "apps.internal", findInternalDomain(domains))

	tcp, ok := findTCPDomain(domains, "")
	require.True(t, ok)
	assert.Equal(t, "tcp.example.com", tcp.Name)

	_, ok = findTCPDomain(domains, "apps.example.com")
	assert.False(t, ok, "override must route tcp")

	_, ok = findTCPDomain(domains[:2], "")
	assert.False(t, ok)

	assert.Empty(t, findInternalDomain(domains[:1]))

	_, err = parseCFDomains(rawList(`nope`))
	require.Error(t, err)
}

func TestParseCFAPIEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "targeted", in: "API endpoint:   https://api.system.example.com/\nAPI version:    3.220.0\n", want: "https://api.system.example.com"},
		{name: "not targeted", in: "No API endpoint set. Use 'cf api' or 'cf login' to target an endpoint.\n", want: ""},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, parseCFAPIEndpoint(tt.in))
		})
	}
}

func TestReadCFSSLDisabled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	assert.False(t, readCFSSLDisabled(path), "missing file")

	require.NoError(t, os.WriteFile(path, []byte(`{"Target":"https://api.example.com","SSLDisabled":true}`), 0o600))
	assert.True(t, readCFSSLDisabled(path))

	require.NoError(t, os.WriteFile(path, []byte(`{"SSLDisabled":false}`), 0o600))
	assert.False(t, readCFSSLDisabled(path))

	require.NoError(t, os.WriteFile(path, []byte(`not json`), 0o600))
	assert.False(t, readCFSSLDisabled(path))
}

func TestCFConfigPathHonoursCFHome(t *testing.T) {
	t.Setenv("CF_HOME", "/tmp/cfhome")

	assert.Equal(t, filepath.Join("/tmp/cfhome", ".cf", "config.json"), cfConfigPath())
}

func TestHrefToPath(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/v3/domains?page=2&per_page=200", hrefToPath("https://api.example.com/v3/domains?page=2&per_page=200"))
	assert.Equal(t, "/v3/domains", hrefToPath("https://api.example.com/v3/domains"))
	assert.Empty(t, hrefToPath("://bad"))
}

func TestCFCurlAllFollowsPagination(t *testing.T) {
	fake := newScriptedRunner().
		on("cf curl /v3/domains?per_page=200", `{"pagination":{"next":{"href":"https://api.example.com/v3/domains?page=2&per_page=200"}},"resources":[{"guid":"a"}]}`).
		on("cf curl /v3/domains?page=2&per_page=200", `{"pagination":{"next":null},"resources":[{"guid":"b"}]}`)
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteSmoke)

	resources, err := r.cfCurlAll(context.Background(), ccQuery("/v3/domains", nil))
	require.NoError(t, err)
	assert.Len(t, resources, 2)
}

func TestDecodeCCResponse(t *testing.T) {
	t.Parallel()

	var res ccResource

	err := decodeCCResponse("/v3/apps/x", []byte(`{"errors":[{"title":"CF-ResourceNotFound","detail":"App not found"}]}`), &res)
	require.ErrorIs(t, err, ErrTestCCResponse)
	assert.Contains(t, err.Error(), "App not found")

	err = decodeCCResponse("/v3/apps/x", []byte(`{"guid":"g","name":"n"}`), &res)
	require.NoError(t, err)
	assert.Equal(t, "g", res.GUID)

	err = decodeCCResponse("/v3/apps/x", []byte(`FAILED\nNot logged in`), &res)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Not logged in")
}

func TestCountLogLines(t *testing.T) {
	t.Parallel()

	out := "Retrieving logs for app x in org o / space s as admin...\n\n" +
		"   2026-09-10T10:00:00.00+0000 [RTR/0] OUT x.apps.example.com - [2026-09-10T10:00:00.000000000Z] \"GET / HTTP/1.1\" 200\n" +
		"   2026-09-10T10:00:01.00+0000 [APP/PROC/WEB/0] OUT 10.0.0.1 - - GET / HTTP/1.1 200\n"

	assert.Equal(t, 2, countLogLines(out))
	assert.Equal(t, 0, countLogLines("Retrieving logs for app x in org o / space s as admin...\n"))
}

func TestRedactCFArgs(t *testing.T) {
	t.Parallel()

	args := []string{"bind-service", "app", "si", "-c", `{"username":"u","password":"secret"}`}
	got := redactCFArgs(args)

	assert.Equal(t, "<redacted>", got[4])
	assert.Equal(t, `{"username":"u","password":"secret"}`, args[4], "input untouched")
	assert.Equal(t, []string{"create-service", "nfs", "Existing", "si", "-c", `{"share":"h:/e"}`},
		redactCFArgs([]string{"create-service", "nfs", "Existing", "si", "-c", `{"share":"h:/e"}`}))
}

func TestOperationSettled(t *testing.T) {
	t.Parallel()

	done, err := operationSettled(ccLastOperation{Type: "create", State: "succeeded", Description: ""}, "x")
	require.NoError(t, err)
	assert.True(t, done)

	done, err = operationSettled(ccLastOperation{Type: "create", State: "in progress", Description: ""}, "x")
	require.NoError(t, err)
	assert.False(t, done)

	_, err = operationSettled(ccLastOperation{Type: "create", State: "failed", Description: "quota"}, "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota")
}

func TestRenderAppManifest(t *testing.T) {
	t.Parallel()

	static := &testApp{Name: "a", Kind: testAppKindStatic, Dir: "", Route: "a.apps.example.com", Marker: "m", Env: nil, GUID: ""}

	out, err := renderAppManifest(static)
	require.NoError(t, err)
	assert.Contains(t, string(out), "name: a")
	assert.Contains(t, string(out), "staticfile_buildpack")
	assert.Contains(t, string(out), "route: a.apps.example.com")
	assert.NotContains(t, string(out), "no-route")

	proxy := &testApp{
		Name: "f", Kind: testAppKindProxy, Dir: "", Route: "", Marker: "m",
		Env: map[string]string{"BACKEND_URL": "http://b.apps.internal:8080/"}, GUID: "",
	}

	out, err = renderAppManifest(proxy)
	require.NoError(t, err)
	assert.Contains(t, string(out), "python_buildpack")
	assert.Contains(t, string(out), "no-route: true")
	assert.Contains(t, string(out), "BACKEND_URL: http://b.apps.internal:8080/")
}

func TestWriteTestApp(t *testing.T) {
	t.Parallel()

	static := &testApp{Name: "a", Kind: testAppKindStatic, Dir: t.TempDir(), Route: "a.apps.example.com", Marker: "ocfp-test a", Env: nil, GUID: ""}
	require.NoError(t, writeTestApp(static))

	page, err := os.ReadFile(filepath.Join(static.Dir, "index.html"))
	require.NoError(t, err)
	assert.Contains(t, string(page), "ocfp-test a")

	for _, name := range []string{"Staticfile", "manifest.yml"} {
		_, err := os.Stat(filepath.Join(static.Dir, name))
		require.NoError(t, err, name)
	}

	proxy := &testApp{Name: "f", Kind: testAppKindProxy, Dir: t.TempDir(), Route: "f.apps.example.com", Marker: "m", Env: nil, GUID: ""}
	require.NoError(t, writeTestApp(proxy))

	for _, name := range []string{"server.py", "Procfile", "requirements.txt", "manifest.yml"} {
		_, err := os.Stat(filepath.Join(proxy.Dir, name))
		require.NoError(t, err, name)
	}

	bogus := &testApp{Name: "x", Kind: "docker", Dir: t.TempDir(), Route: "", Marker: "", Env: nil, GUID: ""}
	require.Error(t, writeTestApp(bogus))
}

func sampleResults() *TestResults {
	return &TestResults{
		Suite:    TestSuiteAll,
		Passed:   1,
		Failed:   1,
		Skipped:  1,
		Duration: 3 * time.Second,
		Tests: []TestResult{
			{Name: "smoke/push_app", Status: TestStatusPassed, Duration: time.Second, Error: "", Reason: "", Output: "pushed", Retries: 0},
			{Name: "smoke/route_https_200", Status: TestStatusFailed, Duration: time.Second, Error: "unexpected HTTP status\nmore", Reason: "", Output: "", Retries: 1},
			{Name: "tcp/find_tcp_domain", Status: TestStatusSkipped, Duration: 0, Error: "", Reason: "no TCP domain on the foundation", Output: "", Retries: 0},
		},
		Output:    "Acceptance mode: x",
		StartTime: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}
}

func TestSaveResultsJUnit(t *testing.T) {
	t.Parallel()

	r := newTestRunnerForUnit(TestSuiteAll)
	path := filepath.Join(t.TempDir(), "results.xml")

	require.NoError(t, r.SaveResults(sampleResults(), path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	xmlText := string(data)
	assert.True(t, strings.HasPrefix(xmlText, "<?xml"))
	assert.Contains(t, xmlText, `<testsuites name="ocfp test all" tests="3" failures="1" skipped="1"`)
	assert.Contains(t, xmlText, `<testsuite name="smoke" tests="2" failures="1" skipped="0"`)
	assert.Contains(t, xmlText, `<testsuite name="tcp" tests="1" failures="0" skipped="1"`)
	assert.Contains(t, xmlText, `<testcase name="route_https_200" classname="ocfp.test.smoke"`)
	assert.Contains(t, xmlText, `<failure message="unexpected HTTP status">`)
	assert.Contains(t, xmlText, `<skipped message="no TCP domain on the foundation">`)
	assert.Contains(t, xmlText, `<system-out>pushed</system-out>`)
}

func TestSaveResultsJSON(t *testing.T) {
	t.Parallel()

	r := newTestRunnerForUnit(TestSuiteAll)
	path := filepath.Join(t.TempDir(), "results.json")

	require.NoError(t, r.SaveResults(sampleResults(), path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var report jsonResults

	require.NoError(t, json.Unmarshal(data, &report))
	assert.Equal(t, TestSuiteAll, report.Suite)
	assert.Equal(t, 1, report.Skipped)
	assert.Len(t, report.Tests, 3)
	assert.Equal(t, "no TCP domain on the foundation", report.Tests[2].Reason)
	assert.Equal(t, "Acceptance mode: x", report.Note)
}

func TestDisplayResultsListsFailedAndSkipped(t *testing.T) {
	t.Parallel()

	r := newTestRunnerForUnit(TestSuiteAll)
	r.DisplayResults(sampleResults())

	out := r.out.(*bytes.Buffer).String() //nolint:forcetypeassert // set by newTestRunnerForUnit
	assert.Contains(t, out, "Acceptance mode: x")
	assert.Contains(t, out, "Passed: 1")
	assert.Contains(t, out, "=== Failed Tests ===")
	assert.Contains(t, out, "- smoke/route_https_200: unexpected HTTP status")
	assert.Contains(t, out, "=== Skipped Tests ===")
	assert.Contains(t, out, "- tcp/find_tcp_domain: no TCP domain on the foundation")
}

func TestDirectorAvailability(t *testing.T) {
	tests := []struct {
		name       string
		flag       string
		env        string
		missing    bool
		envFails   bool
		wantAlias  string
		wantReason string
	}{
		{name: "nothing named", flag: "", env: "", missing: false, envFails: false, wantAlias: "", wantReason: "no BOSH director was named"},
		{name: "flag wins", flag: "ocf", env: "other", missing: false, envFails: false, wantAlias: "ocf", wantReason: ""},
		{name: "env fallback", flag: "", env: "lab", missing: false, envFails: false, wantAlias: "lab", wantReason: ""},
		{name: "bosh missing", flag: "ocf", env: "", missing: true, envFails: false, wantAlias: "", wantReason: "bosh CLI is not on PATH"},
		{name: "director unreachable", flag: "ocf", env: "", missing: false, envFails: true, wantAlias: "", wantReason: "bosh -e ocf env failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BOSH_ENVIRONMENT", tt.env)

			fake := newScriptedRunner()
			fake.missing["bosh"] = tt.missing

			if tt.envFails {
				fake.fail("bosh -e ocf env", "Fetching info:\n  connection refused")
			}

			installScriptedRunner(t, fake)

			r := newTestRunnerForUnit(TestSuiteAcceptance)
			r.BoshEnv = tt.flag

			alias, reason := r.directorAvailability(context.Background())
			assert.Equal(t, tt.wantAlias, alias)

			if tt.wantReason == "" {
				assert.Empty(t, reason)
			} else {
				assert.Contains(t, reason, tt.wantReason)
			}
		})
	}
}

func TestAcceptancePlansErrandAndFallback(t *testing.T) {
	t.Setenv("BOSH_ENVIRONMENT", "")
	t.Setenv("BOSH_DEPLOYMENT", "")

	fake := newScriptedRunner().
		on("bosh -e ocf -d cf run-errand smoke-tests", "Task 42 done\nSucceeded")
	installScriptedRunner(t, fake)

	// Errand mode.
	r := newTestRunnerForUnit(TestSuiteAcceptance)
	r.BoshEnv = "ocf"

	results, err := r.Execute(context.Background())
	require.NoError(t, err)
	assert.Contains(t, results.Output, `BOSH errand cf/smoke-tests through director alias "ocf"`)
	require.Len(t, results.Tests, 1)
	assert.Equal(t, "acceptance/bosh_smoke_tests_errand", results.Tests[0].Name)
	assert.Equal(t, TestStatusPassed, results.Tests[0].Status)
	assert.Contains(t, results.Tests[0].Output, "Succeeded")
	assert.True(t, fake.called("bosh -e ocf -d cf run-errand smoke-tests"))

	// Errand failure is a failed check.
	failing := newScriptedRunner().fail("bosh -e ocf -d cf run-errand smoke-tests", "Errand 'smoke-tests' completed with error (exit code 1)")
	installScriptedRunner(t, failing)

	r = newTestRunnerForUnit(TestSuiteAcceptance)
	r.BoshEnv = "ocf"

	results, err = r.Execute(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, results.Failed)
	assert.Contains(t, results.Tests[0].Error, "completed with error")

	// Fallback mode.
	installScriptedRunner(t, newScriptedRunner())

	r = newTestRunnerForUnit(TestSuiteAcceptance)

	results, err = r.Execute(context.Background())
	require.NoError(t, err)
	assert.Contains(t, results.Output, "Acceptance mode: cf CLI suites smoke, c2c, and blacksmith, because no BOSH director was named")

	suites := map[string]bool{}
	for _, res := range results.Tests {
		suite, _ := splitStepName(res.Name)
		suites[suite] = true
	}

	assert.Equal(t, map[string]bool{"smoke": true, "c2c": true, "blacksmith": true}, suites)
}

func TestBoshDeploymentDefault(t *testing.T) {
	t.Setenv("BOSH_DEPLOYMENT", "")

	r := newTestRunnerForUnit(TestSuiteAcceptance)
	assert.Equal(t, "cf", r.boshDeployment())

	t.Setenv("BOSH_DEPLOYMENT", "cf-lab")
	assert.Equal(t, "cf-lab", r.boshDeployment())

	r.BoshDeployment = "flag"
	assert.Equal(t, "flag", r.boshDeployment())
}

func TestSystemDomainAndAPIResolution(t *testing.T) {
	t.Parallel()

	r := newTestRunnerForUnit(TestSuiteSmoke)
	r.Config = testConfigWithFQDNs("ocf.example.com", nil)

	assert.Equal(t, "system.ocf.example.com", r.systemDomain())
	assert.Equal(t, "apps.ocf.example.com", r.configAppsDomain())

	r.Config = testConfigWithFQDNs("ocf.example.com", map[string]string{"system": "sys.example.com", "apps": "run.example.com"})
	assert.Equal(t, "sys.example.com", r.systemDomain())
	assert.Equal(t, "run.example.com", r.configAppsDomain())

	api, err := r.resolveAPIURL(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://api.sys.example.com", api)

	r.API = "https://api.override.example.com/"

	api, err = r.resolveAPIURL(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://api.override.example.com", api)
}

func TestResolveAPIURLFallsBackToCurrentTarget(t *testing.T) {
	fake := newScriptedRunner().on("cf api", "API endpoint:   https://api.current.example.com\nAPI version:    3.220.0\n")
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteSmoke)
	r.Config = testConfigWithFQDNs("", nil)

	api, err := r.resolveAPIURL(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://api.current.example.com", api)

	installScriptedRunner(t, newScriptedRunner().on("cf api", "No API endpoint set.\n"))

	_, err = r.resolveAPIURL(context.Background())
	require.ErrorIs(t, err, ErrTestAPIUnresolved)
}

func TestEnsureCFTargetOnlyRetargetsWhenNeeded(t *testing.T) {
	fake := newScriptedRunner().on("cf api", "API endpoint:   https://api.system.example.com\n")
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteSmoke)
	require.NoError(t, r.ensureCFTarget(context.Background()))
	assert.False(t, fake.called("cf api https://"), "matching target must not be re-set")

	other := newScriptedRunner().on("cf api", "API endpoint:   https://api.other.example.com\n")
	installScriptedRunner(t, other)

	r.SkipSSLValidation = true
	require.NoError(t, r.ensureCFTarget(context.Background()))
	assert.True(t, other.called("cf api https://api.system.example.com --skip-ssl-validation"))
}

func TestEnsureLoggedIn(t *testing.T) {
	installScriptedRunner(t, newScriptedRunner().fail("cf target", "Not logged in. Use 'cf login' or 'cf login --sso' to log in.\nFAILED"))

	r := newTestRunnerForUnit(TestSuiteSmoke)

	err := r.ensureLoggedIn(context.Background())
	require.ErrorIs(t, err, ErrTestNotLoggedIn)
	assert.Contains(t, err.Error(), "Not logged in")
}

func TestHelpers(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "b\nc", lastLines("a\nb\n\nc\n", 2))
	assert.Equal(t, "first", firstLine("first\nsecond"))
	assert.Equal(t, "  a\n  b\n", indentLines("a\nb\n", "  "))
	assert.Equal(t, "8080", portString(8080))
	assert.Equal(t, "none", valueOr("", "none"))
	assert.Equal(t, `{"share":"h:/e"}`, mustJSON(map[string]string{"share": "h:/e"}))

	suite, check := splitStepName("smoke/push_app")
	assert.Equal(t, "smoke", suite)
	assert.Equal(t, "push_app", check)

	tag, ok := matchesAnyTag("smoke/push_app", []string{" push ", ""})
	assert.True(t, ok)
	assert.Equal(t, "push", tag)
}
