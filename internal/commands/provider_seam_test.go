package commands

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestHomeDirReturnsSomething verifies homeDir() returns a non-empty path
// under normal conditions.
func TestHomeDirReturnsSomething(t *testing.T) {
	t.Parallel()

	dir, err := homeDir()
	require.NoError(t, err)
	assert.NotEmpty(t, dir)
}

// TestHomeDirFnOverride verifies the homeDirFn seam can be replaced.
func TestHomeDirFnOverride(t *testing.T) {
	orig := homeDirFn
	homeDirFn = func() (string, error) { return "/fake/home", nil }
	defer func() { homeDirFn = orig }()

	dir, err := homeDir()
	require.NoError(t, err)
	assert.Equal(t, "/fake/home", dir)
}

// TestLoginOpenStackNoError verifies loginOpenStack returns nil (not yet implemented
// but should not panic or error out).
func TestLoginOpenStackNoError(t *testing.T) {
	err := loginOpenStack(testLogger(t))
	assert.NoError(t, err)
}

// TestLoginAzureFromServicePrincipalMissingEnvVars verifies that when Azure
// service principal env vars are absent, the function returns false.
func TestLoginAzureFromServicePrincipalMissingEnvVars(t *testing.T) {
	t.Setenv("AZURE_SUBSCRIPTION_ID", "")
	t.Setenv("AZURE_CLIENT_ID", "")
	t.Setenv("AZURE_TENANT_ID", "")
	t.Setenv("AZURE_CLIENT_SECRET", "")

	result := loginAzureFromServicePrincipal(zap.NewNop())
	assert.False(t, result)
}

// TestLoginAzureFromServicePrincipalAllEnvVarsSet verifies that when all four
// Azure service principal env vars are set, the function returns true.
func TestLoginAzureFromServicePrincipalAllEnvVarsSet(t *testing.T) {
	t.Setenv("AZURE_SUBSCRIPTION_ID", "sub-123")
	t.Setenv("AZURE_CLIENT_ID", "client-456")
	t.Setenv("AZURE_TENANT_ID", "tenant-789")
	t.Setenv("AZURE_CLIENT_SECRET", "s3cr3t")

	result := loginAzureFromServicePrincipal(zap.NewNop())
	assert.True(t, result)
}

// TestLoginAzureFromManagedIdentityNotEnabled verifies that when
// AZURE_USE_MANAGED_IDENTITY is absent, the function returns false.
func TestLoginAzureFromManagedIdentityNotEnabled(t *testing.T) {
	t.Setenv("AZURE_USE_MANAGED_IDENTITY", "")

	result := loginAzureFromManagedIdentity(zap.NewNop())
	assert.False(t, result)
}

// TestLoginAzureFromManagedIdentityEnabled verifies that AZURE_USE_MANAGED_IDENTITY=true
// causes the function to return true.
func TestLoginAzureFromManagedIdentityEnabled(t *testing.T) {
	t.Setenv("AZURE_USE_MANAGED_IDENTITY", "true")
	t.Setenv("AZURE_CLIENT_ID", "")

	result := loginAzureFromManagedIdentity(zap.NewNop())
	assert.True(t, result)
}

// TestLoginAzureFromManagedIdentityEnabledWithClientID verifies user-assigned
// managed identity branch (AZURE_CLIENT_ID set).
func TestLoginAzureFromManagedIdentityEnabledWithClientID(t *testing.T) {
	t.Setenv("AZURE_USE_MANAGED_IDENTITY", "1")
	t.Setenv("AZURE_CLIENT_ID", "mi-client-id")

	result := loginAzureFromManagedIdentity(zap.NewNop())
	assert.True(t, result)
}

// TestRetrieveAWSAccessKeyFromVaultValidationError verifies that an invalid
// vault path returns a validation error without invoking the runner.
func TestRetrieveAWSAccessKeyFromVaultValidationError(t *testing.T) {
	fake := newFakeRunner()
	restore := installFakeRunner(fake)
	defer restore()

	// Inject an invalid bloc name with a character outside the allowed set.
	// The path becomes "secret/config/bad bloc/aws:access_key_id" which
	// contains a space and fails security.ValidateInput.
	creds := &AWSCredentials{}

	err := retrieveAWSAccessKeyFromVault(t.Context(), "bad bloc", creds)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid access key path")
}

// TestRetrieveAWSSecretKeyFromVaultValidationError verifies path validation
// for the secret key retrieval function.
func TestRetrieveAWSSecretKeyFromVaultValidationError(t *testing.T) {
	fake := newFakeRunner()
	restore := installFakeRunner(fake)
	defer restore()

	creds := &AWSCredentials{}

	err := retrieveAWSSecretKeyFromVault(t.Context(), "bad bloc", creds)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid secret key path")
}

// TestGetSTACKITCredentialsFromVaultInvalidBlocName verifies that a bloc name
// containing invalid characters is rejected before any vault call.
func TestGetSTACKITCredentialsFromVaultInvalidBlocName(t *testing.T) {
	fake := newFakeRunner()
	restore := installFakeRunner(fake)
	defer restore()

	// safe is present; the invalid bloc name triggers ValidateInput error on
	// the token path before Output() is called.
	_, _, err := getSTACKITCredentialsFromVault("bad bloc", testLogger(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token path")
}

// TestGetAWSCredentialsFromVaultEmptyResult verifies that when vault returns
// empty values for both access key and secret, nil credentials are returned.
func TestGetAWSCredentialsFromVaultEmptyResult(t *testing.T) {
	fake := newFakeRunner()
	// All paths return empty — mimic vault miss
	restore := installFakeRunner(fake)
	defer restore()

	creds, err := getAWSCredentialsFromVault("test-bloc", testLogger(t))
	require.NoError(t, err)
	assert.Nil(t, creds)
}

// TestLoginGCPWithCredPathAndGcloudPresent verifies that when a credential file
// exists and gcloud is available, loginGCP invokes runner.Run for gcloud.
func TestLoginGCPWithCredPathAndGcloudPresent(t *testing.T) {
	fake := newFakeRunner()
	// gcloud is present and activation succeeds
	fake.combined["gcloud auth activate-service-account --key-file"] = []byte("Activated service account")
	restore := installFakeRunner(fake)
	defer restore()

	tmp, err := os.CreateTemp(t.TempDir(), "gcp-creds-*.json")
	require.NoError(t, err)
	_ = tmp.Close()

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", tmp.Name())

	err = loginGCP(testLogger(t))
	assert.NoError(t, err)
}
