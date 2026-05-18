package commands

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// setupPVEInitEnv prepares a temp OCFP_HOME and returns its path.
// Each test that modifies viper must reset it via t.Cleanup(viper.Reset).
func setupPVEInitEnv(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	return tmpDir
}

// TestValidatePVEBlocName_Valid verifies that well-formed PVE bloc names pass.
func TestValidatePVEBlocName_Valid(t *testing.T) {
	cases := []struct {
		name string
		bloc string
	}{
		{"single datacenter segment", "ocfp-pve-dc1"},
		{"clustered segment", "ocfp-pve-cluster-1"},
		{"geo slug", "ocfp-pve-london-east"},
		{"numeric suffix", "ocfp-pve-eu-west-1"},
		{"alphanumeric slug", "ocfp-pve-abc123"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePVEBlocName(tc.bloc)
			assert.NoError(t, err, "validatePVEBlocName must accept %q", tc.bloc)
		})
	}
}

// TestValidatePVEBlocName_Invalid verifies that malformed PVE bloc names are rejected.
func TestValidatePVEBlocName_Invalid(t *testing.T) {
	cases := []struct {
		name string
		bloc string
	}{
		{"empty string", ""},
		{"wrong iaas prefix", "ocfp-aws-us-east-1"},
		{"missing ocfp- prefix", "pve-dc1"},
		{"uppercase in slug", "ocfp-pve-DC1"},
		{"empty datacenter segment", "ocfp-pve-"},
		{"underscore not allowed", "ocfp-pve-dc1_x"},
		{"bare pve only", "pve"},
		{"space in name", "ocfp-pve-dc 1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePVEBlocName(tc.bloc)
			require.Error(t, err, "validatePVEBlocName must reject %q", tc.bloc)
			assert.True(t,
				errors.Is(err, ErrBlocFormatInvalid),
				"error must wrap ErrBlocFormatInvalid for bloc=%q; got: %v", tc.bloc, err,
			)
		})
	}
}

// TestResolveInitPVEParams_MissingBloc verifies that when no --bloc flag and no
// OCFP_BLOC env var are present, resolveInitPVEParams returns ErrBlocMissing.
func TestResolveInitPVEParams_MissingBloc(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	setupPVEInitEnv(t)
	os.Unsetenv("OCFP_BLOC")

	// cmd has no --bloc set on command line (Changed == false).
	params, err := resolveInitPVEParams(makeCmdNoBloc(t))

	require.Error(t, err, "resolveInitPVEParams must error when no bloc is provided")
	assert.Nil(t, params, "params must be nil on error")
	assert.True(t,
		errors.Is(err, ErrBlocMissing),
		"error must wrap ErrBlocMissing; got: %v", err,
	)
}

// TestResolveInitPVEParams_BlocFromEnvVar verifies that OCFP_BLOC is used when
// no --bloc flag is provided on the command line.
func TestResolveInitPVEParams_BlocFromEnvVar(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	setupPVEInitEnv(t)

	const envBloc = "ocfp-pve-dc1"
	t.Setenv("OCFP_BLOC", envBloc)

	params, err := resolveInitPVEParams(makeCmdNoBloc(t))

	require.NoError(t, err, "resolveInitPVEParams must succeed when OCFP_BLOC is set")
	require.NotNil(t, params)
	assert.Equal(t, envBloc, params.bloc, "params.bloc must equal OCFP_BLOC value")
}

// TestResolveInitPVEParams_BlobstoreEndpointIgnored verifies that blobstore-endpoint
// is not resolved by `ocfp init pve`. Blobstore endpoint belongs on `ocfp vault populate`
// (--blobstore-endpoint flag) and is not part of the init flow.
func TestResolveInitPVEParams_BlobstoreEndpointIgnored(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	setupPVEInitEnv(t)

	const (
		bloc     = "ocfp-pve-dc1"
		endpoint = "http://minio:9000"
	)

	t.Setenv("OCFP_BLOC", bloc)
	viper.Set("blobstore-endpoint", endpoint)

	params, err := resolveInitPVEParams(makeCmdNoBloc(t))

	require.NoError(t, err)
	require.NotNil(t, params)
	assert.Equal(t, bloc, params.bloc)
	// initPVEParams carries only bloc; blobstore-endpoint is a vault populate concern.
}

// TestInitPVE_MissingBlocReturnsError verifies that `ocfp init pve` fails with
// a clear error when neither --bloc nor OCFP_BLOC supplies a bloc name.
func TestInitPVE_MissingBlocReturnsError(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	setupPVEInitEnv(t)
	os.Unsetenv("OCFP_BLOC")

	err := initializePVE(makeCmdNoBloc(t))

	require.Error(t, err, "initializePVE must return an error when no bloc is provided")
	assert.True(t,
		errors.Is(err, ErrBlocMissing),
		"error must wrap ErrBlocMissing; got: %v", err,
	)
}

// TestInitPVE_StaleViperBlocBlocked verifies that a bloc value populated in
// viper by a prior-session fallback is NOT accepted when neither --bloc nor
// OCFP_BLOC was explicitly provided.
func TestInitPVE_StaleViperBlocBlocked(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	setupPVEInitEnv(t)
	os.Unsetenv("OCFP_BLOC")

	// Simulate createPreRunHandler populating viper from a stale state file.
	viper.Set("bloc", "ocfp-pve-stale-prior")

	// No --bloc flag, no OCFP_BLOC — must fail even though viper is non-empty.
	err := initializePVE(makeCmdNoBloc(t))

	require.Error(t, err, "initializePVE must error when only a stale viper bloc is present")
	assert.True(t,
		errors.Is(err, ErrBlocMissing),
		"error must wrap ErrBlocMissing for stale viper bloc; got: %v", err,
	)
}

// TestInitPVE_InvalidBlocReturnsError verifies that an improperly formatted PVE
// bloc name is rejected before any file system writes occur.
func TestInitPVE_InvalidBlocReturnsError(t *testing.T) {
	cases := []struct {
		name string
		bloc string
	}{
		{"wrong iaas", "ocfp-aws-us-east-1"},
		{"missing ocfp prefix", "pve-dc1"},
		{"uppercase", "ocfp-pve-DC1"},
		{"trailing dash", "ocfp-pve-"},
		{"underscore", "ocfp-pve-dc1_x"},
		{"empty", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)

			setupPVEInitEnv(t)
			os.Unsetenv("OCFP_BLOC")

			// Use env var path so the bloc value reaches format validation.
			// Empty string would be caught by ErrBlocMissing instead — skip env var for that case.
			if tc.bloc != "" {
				t.Setenv("OCFP_BLOC", tc.bloc)
			}

			err := initializePVE(makeCmdNoBloc(t))

			require.Error(t, err, "initializePVE must return an error for bloc=%q", tc.bloc)

			if tc.bloc == "" {
				assert.True(t, errors.Is(err, ErrBlocMissing),
					"empty bloc must yield ErrBlocMissing; got: %v", err)
			} else {
				assert.True(t, errors.Is(err, ErrBlocFormatInvalid),
					"error must wrap ErrBlocFormatInvalid for bloc=%q; got: %v", tc.bloc, err)
			}
		})
	}
}

// TestInitPVE_ValidBlocWritesEnvFile verifies that an explicit --bloc flag
// produces a Genesis env file containing `ocfp.bloc: <value>` in the expected
// location.
func TestInitPVE_ValidBlocWritesEnvFile(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupPVEInitEnv(t)

	const bloc = "ocfp-pve-dc1"
	viper.Set("bloc", bloc)

	err := initializePVE(makeBlocCmd(t, bloc))
	require.NoError(t, err)

	envPath := filepath.Join(tmpDir, bloc, "deployments", "mgmt", bloc+"-mgmt.yml")
	data, err := os.ReadFile(envPath)
	require.NoError(t, err, "env file must exist at %s", envPath)

	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &parsed))

	ocfpBlock, ok := parsed["ocfp"].(map[string]interface{})
	require.True(t, ok, "ocfp: block must be present in env file; content:\n%s", string(data))
	assert.Equal(t, bloc, ocfpBlock["bloc"],
		"ocfp.bloc must equal %q; content:\n%s", bloc, string(data))
}

// TestInitPVE_BlocFromEnvVarWhenFlagAbsent verifies that OCFP_BLOC is used when
// no --bloc flag is set on the command line.
func TestInitPVE_BlocFromEnvVarWhenFlagAbsent(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupPVEInitEnv(t)

	const envBloc = "ocfp-pve-london-east"
	t.Setenv("OCFP_BLOC", envBloc)

	err := initializePVE(makeCmdNoBloc(t))
	require.NoError(t, err)

	envPath := filepath.Join(tmpDir, envBloc, "deployments", "mgmt", envBloc+"-mgmt.yml")
	data, err := os.ReadFile(envPath)
	require.NoError(t, err, "env file must exist at %s", envPath)

	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &parsed))

	ocfpBlock, ok := parsed["ocfp"].(map[string]interface{})
	require.True(t, ok, "ocfp: block must be present; content:\n%s", string(data))
	assert.Equal(t, envBloc, ocfpBlock["bloc"])
}

// TestInitPVE_WritesBothEnvFiles verifies that initializePVE writes both the
// mgmt env file and the ocf env file for a valid PVE bloc.
func TestInitPVE_WritesBothEnvFiles(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupPVEInitEnv(t)

	const bloc = "ocfp-pve-cluster-1"
	viper.Set("bloc", bloc)

	err := initializePVE(makeBlocCmd(t, bloc))
	require.NoError(t, err)

	mgmtPath := filepath.Join(tmpDir, bloc, "deployments", "mgmt", bloc+"-mgmt.yml")
	ocfPath := filepath.Join(tmpDir, bloc, "deployments", "ocf", bloc+"-ocf.yml")

	_, err = os.Stat(mgmtPath)
	require.NoError(t, err, "mgmt env file must exist at %s", mgmtPath)

	_, err = os.Stat(ocfPath)
	require.NoError(t, err, "ocf env file must exist at %s", ocfPath)
}

// TestInitPVE_MgmtFileHasCreateEnvAndBoshKit verifies that the mgmt env file
// has use_create_env: true and kit.name: bosh.
func TestInitPVE_MgmtFileHasCreateEnvAndBoshKit(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupPVEInitEnv(t)

	const bloc = "ocfp-pve-dc1"
	viper.Set("bloc", bloc)

	require.NoError(t, initializePVE(makeBlocCmd(t, bloc)))

	mgmtPath := filepath.Join(tmpDir, bloc, "deployments", "mgmt", bloc+"-mgmt.yml")
	data, err := os.ReadFile(mgmtPath)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &parsed))

	genesisBlock, ok := parsed["genesis"].(map[string]interface{})
	require.True(t, ok, "genesis: block must be present; content:\n%s", string(data))
	assert.Equal(t, true, genesisBlock["use_create_env"],
		"mgmt file must have use_create_env: true; content:\n%s", string(data))

	kitBlock, ok := parsed["kit"].(map[string]interface{})
	require.True(t, ok, "kit: block must be present; content:\n%s", string(data))
	assert.Equal(t, "bosh", kitBlock["name"],
		"mgmt file must have kit.name: bosh; content:\n%s", string(data))
}

// TestInitPVE_OcfFileHasNoCreateEnvAndCfKit verifies that the ocf env file
// has no use_create_env key (or false) and kit.name: cf.
func TestInitPVE_OcfFileHasNoCreateEnvAndCfKit(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupPVEInitEnv(t)

	const bloc = "ocfp-pve-dc1"
	viper.Set("bloc", bloc)

	require.NoError(t, initializePVE(makeBlocCmd(t, bloc)))

	ocfPath := filepath.Join(tmpDir, bloc, "deployments", "ocf", bloc+"-ocf.yml")
	data, err := os.ReadFile(ocfPath)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &parsed))

	genesisBlock, ok := parsed["genesis"].(map[string]interface{})
	require.True(t, ok, "genesis: block must be present; content:\n%s", string(data))
	useCreateEnv, exists := genesisBlock["use_create_env"]
	assert.True(t, !exists || useCreateEnv == false,
		"ocf file must not have use_create_env: true; content:\n%s", string(data))

	kitBlock, ok := parsed["kit"].(map[string]interface{})
	require.True(t, ok, "kit: block must be present; content:\n%s", string(data))
	assert.Equal(t, "cf", kitBlock["name"],
		"ocf file must have kit.name: cf; content:\n%s", string(data))
}

// TestInitPVE_OcfFileHasPVEDatacenterParam verifies that the ocf env file
// contains params.pve_datacenter set to the datacenter segment of the bloc
// (everything after "ocfp-pve-"), and that the mgmt env file has no params block.
func TestInitPVE_OcfFileHasPVEDatacenterParam(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupPVEInitEnv(t)

	const bloc = "ocfp-pve-dc1"
	viper.Set("bloc", bloc)

	require.NoError(t, initializePVE(makeBlocCmd(t, bloc)))

	// ocf file must have params.pve_datacenter: dc1
	ocfPath := filepath.Join(tmpDir, bloc, "deployments", "ocf", bloc+"-ocf.yml")
	ocfData, err := os.ReadFile(ocfPath)
	require.NoError(t, err, "ocf env file must exist at %s", ocfPath)

	var ocfParsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(ocfData, &ocfParsed))

	paramsBlock, ok := ocfParsed["params"].(map[string]interface{})
	require.True(t, ok, "ocf file must have a params: block; content:\n%s", string(ocfData))
	assert.Equal(t, "dc1", paramsBlock["pve_datacenter"],
		"params.pve_datacenter must equal \"dc1\"; content:\n%s", string(ocfData))

	// mgmt file must have no params block
	mgmtPath := filepath.Join(tmpDir, bloc, "deployments", "mgmt", bloc+"-mgmt.yml")
	mgmtData, err := os.ReadFile(mgmtPath)
	require.NoError(t, err, "mgmt env file must exist at %s", mgmtPath)

	var mgmtParsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(mgmtData, &mgmtParsed))

	_, hasParams := mgmtParsed["params"]
	assert.False(t, hasParams,
		"mgmt file must not have a params: block; content:\n%s", string(mgmtData))
}

// TestInitPVE_BothFilesHaveBlocAndPVEIAAS verifies that both env files carry
// ocfp.bloc: <bloc> and kit.iaas: pve.
func TestInitPVE_BothFilesHaveBlocAndPVEIAAS(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupPVEInitEnv(t)

	const bloc = "ocfp-pve-cluster-1"
	viper.Set("bloc", bloc)

	require.NoError(t, initializePVE(makeBlocCmd(t, bloc)))

	paths := []struct {
		label string
		path  string
	}{
		{"mgmt", filepath.Join(tmpDir, bloc, "deployments", "mgmt", bloc+"-mgmt.yml")},
		{"ocf", filepath.Join(tmpDir, bloc, "deployments", "ocf", bloc+"-ocf.yml")},
	}

	for _, p := range paths {
		t.Run(p.label, func(t *testing.T) {
			data, err := os.ReadFile(p.path)
			require.NoError(t, err, "env file must exist at %s", p.path)

			var parsed map[string]interface{}
			require.NoError(t, yaml.Unmarshal(data, &parsed))

			ocfpBlock, ok := parsed["ocfp"].(map[string]interface{})
			require.True(t, ok, "ocfp: block must be present; content:\n%s", string(data))
			assert.Equal(t, bloc, ocfpBlock["bloc"],
				"ocfp.bloc must equal %q; content:\n%s", bloc, string(data))

			kitBlock, ok := parsed["kit"].(map[string]interface{})
			require.True(t, ok, "kit: block must be present; content:\n%s", string(data))
			assert.Equal(t, "pve", kitBlock["iaas"],
				"kit.iaas must equal pve; content:\n%s", string(data))
		})
	}
}

// TestInitPVE_FlagOverridesEnvVar verifies that when both --bloc (explicit flag)
// and OCFP_BLOC are set, the flag value wins.
func TestInitPVE_FlagOverridesEnvVar(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupPVEInitEnv(t)

	const (
		envBloc  = "ocfp-pve-london-east"
		flagBloc = "ocfp-pve-dc1"
	)

	t.Setenv("OCFP_BLOC", envBloc)
	viper.Set("bloc", flagBloc) // mirrors what cobra/viper would set from the flag

	// cmd has --bloc=flagBloc explicitly on the command line (Changed == true).
	err := initializePVE(makeBlocCmd(t, flagBloc))
	require.NoError(t, err)

	// env file must be under flagBloc, not envBloc.
	envPath := filepath.Join(tmpDir, flagBloc, "deployments", "mgmt", flagBloc+"-mgmt.yml")
	data, err := os.ReadFile(envPath)
	require.NoError(t, err, "env file must exist at %s (flag bloc wins)", envPath)

	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &parsed))

	ocfpBlock, ok := parsed["ocfp"].(map[string]interface{})
	require.True(t, ok, "ocfp: block must be present; content:\n%s", string(data))
	assert.Equal(t, flagBloc, ocfpBlock["bloc"],
		"ocfp.bloc must equal flag value %q, not env var %q", flagBloc, envBloc)

	// Env-bloc path must NOT have been created.
	_, statErr := os.Stat(filepath.Join(tmpDir, envBloc))
	assert.True(t, os.IsNotExist(statErr),
		"directory for env-bloc %q must not exist when flag overrides it", envBloc)
}
