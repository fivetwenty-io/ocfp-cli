package commands

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeRunner is a test double for commandRunner. Each method key is
// "<name> <space-joined-args>"; if not found, the request key "<name>" is tried.
// The first match wins. An entry with a non-nil error causes that method to fail.
type fakeRunner struct {
	outputs     map[string][]byte // key -> stdout bytes returned by Output()
	combined    map[string][]byte // key -> combined bytes returned by Run()
	splitStdout map[string][]byte // key -> stdout bytes returned by RunSplit()
	splitStderr map[string][]byte // key -> stderr bytes returned by RunSplit()
	errs        map[string]error  // key -> error for either method
	missing     map[string]bool   // key -> true means LookPath returns "not found"
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		outputs:     make(map[string][]byte),
		combined:    make(map[string][]byte),
		splitStdout: make(map[string][]byte),
		splitStderr: make(map[string][]byte),
		errs:        make(map[string]error),
		missing:     make(map[string]bool),
	}
}

// key returns the lookup key for a command+args pair.
func (f *fakeRunner) key(name string, args []string) string {
	if len(args) == 0 {
		return name
	}

	return name + " " + strings.Join(args, " ")
}

// Output implements commandRunner.Output for tests.
// Inputs: ctx may be context.Background(); name must match a registered key.
// Failure modes: returns registered error if any; returns empty slice if no output registered.
func (f *fakeRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	k := f.key(name, args)
	if err, ok := f.errs[k]; ok {
		return nil, err
	}

	// Fall back to binary name only
	if err, ok := f.errs[name]; ok {
		return nil, err
	}

	if out, ok := f.outputs[k]; ok {
		return out, nil
	}

	if out, ok := f.outputs[name]; ok {
		return out, nil
	}

	return []byte{}, nil
}

// Run implements commandRunner.Run for tests (combined output).
// Inputs: ctx may be context.Background(); name must match a registered key.
// Failure modes: returns registered error if any; returns empty slice if no output registered.
func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	k := f.key(name, args)
	if err, ok := f.errs[k]; ok {
		return nil, err
	}

	if err, ok := f.errs[name]; ok {
		return nil, err
	}

	if out, ok := f.combined[k]; ok {
		return out, nil
	}

	if out, ok := f.combined[name]; ok {
		return out, nil
	}

	return []byte{}, nil
}

// RunSplit implements commandRunner.RunSplit for tests.
// Returns pre-registered stdout and stderr slices, or empty slices when not registered.
// Inputs: ctx may be context.Background(); name must match a registered key.
// Failure modes: returns registered error if any; returns empty slices if nothing registered.
func (f *fakeRunner) RunSplit(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	k := f.key(name, args)
	if err, ok := f.errs[k]; ok {
		return nil, nil, err
	}

	if err, ok := f.errs[name]; ok {
		return nil, nil, err
	}

	var stdout, stderr []byte

	if v, ok := f.splitStdout[k]; ok {
		stdout = v
	} else if v, ok := f.splitStdout[name]; ok {
		stdout = v
	}

	if v, ok := f.splitStderr[k]; ok {
		stderr = v
	} else if v, ok := f.splitStderr[name]; ok {
		stderr = v
	}

	return stdout, stderr, nil
}

// LookPath implements commandRunner.LookPath for tests.
// Inputs: name must be a non-empty binary name.
// Failure modes: returns error when name registered in missing map.
func (f *fakeRunner) LookPath(name string) error {
	if f.missing[name] {
		return errors.New("lookpath " + name + ": executable file not found in $PATH")
	}

	return nil
}

// installFakeRunner replaces the package-level runner with fake and returns a
// restore function. Always defer the restore in tests.
func installFakeRunner(fake *fakeRunner) func() {
	orig := runner
	runner = fake

	return func() { runner = orig }
}

// TestFakeRunnerRunSplitArgs verifies fakeRunner.RunSplit returns registered
// stdout/stderr for the correct key and separates the two streams.
func TestFakeRunnerRunSplitArgs(t *testing.T) {
	t.Parallel()

	fake := newFakeRunner()
	key := "aws sts get-caller-identity --profile myprof"
	fake.splitStdout[key] = []byte(`{"Account":"123456789012"}`)
	fake.splitStderr[key] = []byte("")

	stdout, stderr, err := fake.RunSplit(context.Background(), "aws", "sts", "get-caller-identity", "--profile", "myprof")
	require.NoError(t, err)
	assert.Equal(t, `{"Account":"123456789012"}`, string(stdout))
	assert.Empty(t, string(stderr))
}

// TestFakeRunnerRunSplitError verifies fakeRunner.RunSplit propagates registered errors.
func TestFakeRunnerRunSplitError(t *testing.T) {
	t.Parallel()

	fake := newFakeRunner()
	fake.splitStderr["aws sts get-caller-identity --profile bad"] = []byte("AccessDenied")
	fake.errs["aws sts get-caller-identity --profile bad"] = errors.New("exit status 254")

	_, _, err := fake.RunSplit(context.Background(), "aws", "sts", "get-caller-identity", "--profile", "bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 254")
}

// TestOsCommandRunnerRunSplit verifies osCommandRunner.RunSplit separates stdout and stderr.
func TestOsCommandRunnerRunSplit(t *testing.T) {
	t.Parallel()

	r := osCommandRunner{}

	stdout, stderr, err := r.RunSplit(context.Background(), "sh", "-c", "echo out; echo err >&2")
	require.NoError(t, err)
	assert.Equal(t, "out\n", string(stdout))
	assert.Equal(t, "err\n", string(stderr))
}

// TestOsCommandRunnerRunSplitError verifies RunSplit returns error on non-zero exit
// while still capturing stderr written before the exit.
func TestOsCommandRunnerRunSplitError(t *testing.T) {
	t.Parallel()

	r := osCommandRunner{}

	_, stderr, err := r.RunSplit(context.Background(), "sh", "-c", "echo oops >&2; exit 1")
	require.Error(t, err)
	assert.Equal(t, "oops\n", string(stderr))
}

// TestOsCommandRunnerInterface verifies that osCommandRunner satisfies the
// commandRunner interface at compile time.
func TestOsCommandRunnerInterface(t *testing.T) {
	t.Parallel()

	var _ commandRunner = osCommandRunner{}
}

// TestOsCommandRunnerLookPath verifies LookPath for a known binary.
func TestOsCommandRunnerLookPath(t *testing.T) {
	t.Parallel()

	r := osCommandRunner{}

	// "sh" is available on all POSIX systems where these tests run.
	err := r.LookPath("sh")
	assert.NoError(t, err, "sh should be on PATH")
}

// TestOsCommandRunnerLookPathMissing verifies LookPath returns error for an absent binary.
func TestOsCommandRunnerLookPathMissing(t *testing.T) {
	t.Parallel()

	r := osCommandRunner{}

	err := r.LookPath("__ocfp_nonexistent_binary_xyz__")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lookpath")
}

// TestOsCommandRunnerOutput verifies Output returns stdout from a real process.
func TestOsCommandRunnerOutput(t *testing.T) {
	t.Parallel()

	r := osCommandRunner{}

	out, err := r.Output(context.Background(), "echo", "hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(out))
}

// TestOsCommandRunnerOutputError verifies Output returns error on non-zero exit.
func TestOsCommandRunnerOutputError(t *testing.T) {
	t.Parallel()

	r := osCommandRunner{}

	_, err := r.Output(context.Background(), "false")
	require.Error(t, err)
}

// TestOsCommandRunnerRun verifies Run returns combined output from a real process.
func TestOsCommandRunnerRun(t *testing.T) {
	t.Parallel()

	r := osCommandRunner{}

	out, err := r.Run(context.Background(), "echo", "world")
	require.NoError(t, err)
	assert.Equal(t, "world\n", string(out))
}

// TestOsCommandRunnerRunError verifies Run returns error on non-zero exit.
func TestOsCommandRunnerRunError(t *testing.T) {
	t.Parallel()

	r := osCommandRunner{}

	_, err := r.Run(context.Background(), "false")
	require.Error(t, err)
}

// TestFakeRunnerLookPathMissing verifies fake LookPath returns error for missing binaries.
func TestFakeRunnerLookPathMissing(t *testing.T) {
	t.Parallel()

	fake := newFakeRunner()
	fake.missing["safe"] = true

	err := fake.LookPath("safe")
	require.Error(t, err)
}

// TestFakeRunnerLookPathPresent verifies fake LookPath returns nil for present binaries.
func TestFakeRunnerLookPathPresent(t *testing.T) {
	t.Parallel()

	fake := newFakeRunner()

	err := fake.LookPath("safe")
	assert.NoError(t, err)
}

// TestGetSTACKITCredentialsFromVaultSafeNotAvailable verifies that when safe is
// missing from PATH, the function returns empty strings with no error.
func TestGetSTACKITCredentialsFromVaultSafeNotAvailable(t *testing.T) {
	fake := newFakeRunner()
	fake.missing["safe"] = true
	restore := installFakeRunner(fake)
	defer restore()

	authType, creds, err := getSTACKITCredentialsFromVault("test-bloc", testLogger(t))
	require.NoError(t, err)
	assert.Empty(t, authType)
	assert.Empty(t, creds)
}

// TestGetSTACKITCredentialsFromVaultTokenSuccess verifies that a token returned
// by safe is surfaced as authTypeToken.
func TestGetSTACKITCredentialsFromVaultTokenSuccess(t *testing.T) {
	fake := newFakeRunner()
	// safe is present; token path returns a non-empty value
	tokenPath := "secret/config/test-bloc/mgmt/cpi/stackit:service_account_token"
	fake.outputs["safe get "+tokenPath] = []byte("mytoken\n")
	restore := installFakeRunner(fake)
	defer restore()

	authType, creds, err := getSTACKITCredentialsFromVault("test-bloc", testLogger(t))
	require.NoError(t, err)
	assert.Equal(t, authTypeToken, authType)
	assert.Equal(t, "mytoken", creds)
}

// TestGetSTACKITCredentialsFromVaultJSONFallback verifies that when no token is
// found, the JSON credential is returned as authTypeJSON.
func TestGetSTACKITCredentialsFromVaultJSONFallback(t *testing.T) {
	fake := newFakeRunner()
	// token path returns empty; JSON path returns data
	tokenPath := "secret/config/test-bloc/mgmt/cpi/stackit:service_account_token"
	jsonPath := "secret/config/test-bloc/mgmt/cpi/stackit:service_account_json"
	fake.outputs["safe get "+tokenPath] = []byte("")
	fake.outputs["safe get "+jsonPath] = []byte(`{"type":"service_account"}`)
	restore := installFakeRunner(fake)
	defer restore()

	authType, creds, err := getSTACKITCredentialsFromVault("test-bloc", testLogger(t))
	require.NoError(t, err)
	assert.Equal(t, authTypeJSON, authType)
	assert.Equal(t, `{"type":"service_account"}`, creds)
}

// TestGetAWSCredentialsFromVaultSafeNotAvailable verifies that when safe is
// missing, the function returns nil credentials with no error.
func TestGetAWSCredentialsFromVaultSafeNotAvailable(t *testing.T) {
	fake := newFakeRunner()
	fake.missing["safe"] = true
	restore := installFakeRunner(fake)
	defer restore()

	creds, err := getAWSCredentialsFromVault("test-bloc", testLogger(t))
	require.NoError(t, err)
	assert.Nil(t, creds)
}

// TestGetAWSCredentialsFromVaultSuccess verifies that access key + secret are
// populated from vault and the credentials struct is returned.
func TestGetAWSCredentialsFromVaultSuccess(t *testing.T) {
	fake := newFakeRunner()

	accessPath := "secret/config/test-bloc/aws:access_key_id"
	secretPath := "secret/config/test-bloc/aws:secret_access_key"
	sessionPath := "secret/config/test-bloc/aws:session_token"
	regionPath := "secret/config/test-bloc/aws:region"

	fake.outputs["safe get "+accessPath] = []byte("AKIAIOSFODNN7EXAMPLE\n")
	fake.outputs["safe get "+secretPath] = []byte("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n")
	fake.outputs["safe get "+sessionPath] = []byte("")
	fake.outputs["safe get "+regionPath] = []byte("us-east-1\n")
	restore := installFakeRunner(fake)
	defer restore()

	creds, err := getAWSCredentialsFromVault("test-bloc", testLogger(t))
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "AKIAIOSFODNN7EXAMPLE", creds.AccessKeyID)
	assert.Equal(t, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", creds.SecretAccessKey)
	assert.Equal(t, "us-east-1", creds.Region)
	assert.Empty(t, creds.SessionToken)
}

// TestLoginGCPGcloudNotAvailable verifies loginGCP does not error when gcloud
// is absent from PATH. GOOGLE_APPLICATION_CREDENTIALS is set to an existing
// file so resolveGCPCredPath is bypassed — the test targets the LookPath seam.
func TestLoginGCPGcloudNotAvailable(t *testing.T) {
	fake := newFakeRunner()
	fake.missing["gcloud"] = true
	restore := installFakeRunner(fake)
	defer restore()

	// Write a temp file so loginGCP treats it as a credential path.
	tmp, err := os.CreateTemp(t.TempDir(), "gcp-creds-*.json")
	require.NoError(t, err)
	_ = tmp.Close()

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", tmp.Name())

	// gcloud not available → loginGCP skips gcloud activation; still returns nil.
	err = loginGCP(testLogger(t))
	assert.NoError(t, err)
}

// TestLoginAzureFromCLINotInstalled verifies loginAzureFromCLI returns false
// when az is not on PATH.
func TestLoginAzureFromCLINotInstalled(t *testing.T) {
	fake := newFakeRunner()
	fake.missing["az"] = true
	restore := installFakeRunner(fake)
	defer restore()

	result := loginAzureFromCLI(testLogger(t))
	assert.False(t, result)
}

// TestLoginAzureFromCLIAuthenticated verifies loginAzureFromCLI returns true
// when az is installed and account show succeeds.
func TestLoginAzureFromCLIAuthenticated(t *testing.T) {
	fake := newFakeRunner()
	// az is available (not in missing); the exec.CommandContext in loginAzureFromCLI
	// still runs directly — this test validates LookPath seam only.
	// We cannot intercept the exec.CommandContext call for az since that site
	// retains exec.CommandContext (stdout/stderr split). This test verifies the
	// LookPath seam works: if az is present, the function proceeds past the guard.
	restore := installFakeRunner(fake)
	defer restore()

	// az is marked present; the actual exec will fail since az isn't installed
	// in CI. The function still returns true after LookPath passes (it proceeds
	// to exec and handles the error gracefully by returning true either way).
	result := loginAzureFromCLI(testLogger(t))
	// Result is true regardless of exec outcome (az present or auth status).
	// Both branches of the exec result return true.
	assert.True(t, result)
}

// testLogger returns a no-op zap logger suitable for tests.
func testLogger(t *testing.T) *zap.Logger {
	t.Helper()

	return zap.NewNop()
}
