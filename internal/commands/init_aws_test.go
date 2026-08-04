package commands

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAWSInitEnv prepares a temp OCFP_HOME and returns a cleanup func.
// The testmain_test.go sets OCFP_HOME to a temp dir, but individual tests
// that modify viper must reset it themselves.
func setupAWSInitEnv(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	return tmpDir
}

// makeBlocCmd builds a minimal cobra root + child command tree with the
// persistent --bloc flag defined and parsed to the given value.
// This simulates an explicit `--bloc <value>` on the command line so that
// cmd.Root().PersistentFlags().Changed("bloc") returns true.
func makeBlocCmd(t *testing.T, blocValue string) *cobra.Command {
	t.Helper()

	root := &cobra.Command{Use: "ocfp"} //nolint:exhaustruct
	root.PersistentFlags().String("bloc", "", "bloc name")

	child := &cobra.Command{Use: "init"} //nolint:exhaustruct
	root.AddCommand(child)

	err := root.ParseFlags([]string{"--bloc", blocValue})
	require.NoError(t, err, "makeBlocCmd: ParseFlags must not error for value %q", blocValue)

	return child
}

// makeCmdNoBloc builds a minimal cobra root + child command tree with the
// persistent --bloc flag defined but NOT set on the command line.
// cmd.Root().PersistentFlags().Changed("bloc") returns false.
// This simulates the case where createPreRunHandler populated viper["bloc"]
// via state-file / config fallback — without the user passing --bloc.
func makeCmdNoBloc(t *testing.T) *cobra.Command {
	t.Helper()

	root := &cobra.Command{Use: "ocfp"} //nolint:exhaustruct
	root.PersistentFlags().String("bloc", "", "bloc name")

	child := &cobra.Command{Use: "init"} //nolint:exhaustruct
	root.AddCommand(child)

	return child
}

// TestInitAWS_MissingBlocReturnsError verifies that `ocfp init aws` fails with
// a clear error when neither --bloc nor OCFP_BLOC supplies a bloc name.
func TestInitAWS_MissingBlocReturnsError(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	setupAWSInitEnv(t)
	os.Unsetenv("OCFP_BLOC")

	err := initializeAWS(makeCmdNoBloc(t))

	require.Error(t, err, "initializeAWS must return an error when no bloc is provided")
	assert.True(t,
		errors.Is(err, ErrBlocMissing),
		"error must wrap ErrBlocMissing; got: %v", err,
	)
}

// TestInitAWS_StaleViperBlocBlocked verifies that a bloc value populated in
// viper by a prior-session fallback (state file / config) is NOT accepted by
// `ocfp init aws` when neither --bloc nor OCFP_BLOC was explicitly provided.
// This is the key regression guard for the MED-1 fix.
func TestInitAWS_StaleViperBlocBlocked(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	setupAWSInitEnv(t)
	os.Unsetenv("OCFP_BLOC")

	// Simulate createPreRunHandler populating viper from a stale state file.
	viper.Set("bloc", "stale-prior-bloc")

	// No --bloc flag, no OCFP_BLOC — must fail even though viper is non-empty.
	err := initializeAWS(makeCmdNoBloc(t))

	require.Error(t, err, "initializeAWS must error when only a stale viper bloc is present")
	assert.True(t,
		errors.Is(err, ErrBlocMissing),
		"error must wrap ErrBlocMissing for stale viper bloc; got: %v", err,
	)
}

// TestInitAWS_ValidBlocWritesEnvFile verifies that an explicit --bloc flag
// produces a Genesis env file containing `ocfp.bloc: <value>` in the expected
// location.
func TestInitAWS_ValidBlocWritesEnvFile(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupAWSInitEnv(t)

	const bloc = "valid-bloc"
	viper.Set("bloc", bloc)

	err := initializeAWS(makeBlocCmd(t, bloc))
	require.NoError(t, err)

	// Verify env file path.
	envPath := filepath.Join(tmpDir, bloc, "deployments", "mgmt", bloc+"-mgmt.yml")
	data, err := os.ReadFile(envPath)
	require.NoError(t, err, "env file must exist at %s", envPath)

	// Parse and assert ocfp.bloc.
	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &parsed))

	ocfpBlock, ok := parsed["ocfp"].(map[string]interface{})
	require.True(t, ok, "ocfp: block must be present in env file; content:\n%s", string(data))
	assert.Equal(t, bloc, ocfpBlock["bloc"],
		"ocfp.bloc must equal %q; content:\n%s", bloc, string(data))
}

// TestInitAWS_InvalidBlocReturnsError verifies that an improperly formatted bloc
// name is rejected before any file system writes occur.
func TestInitAWS_InvalidBlocReturnsError(t *testing.T) {
	cases := []struct {
		name string
		bloc string
	}{
		{"uppercase letter", "INVALID"},
		{"spaces", "invalid name"},
		{"exclamation mark", "bad!name"},
		{"leading dash", "-badbloc"},
		{"trailing dash", "badbloc-"},
		{"single char", "a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)

			setupAWSInitEnv(t)
			os.Unsetenv("OCFP_BLOC")

			viper.Set("bloc", tc.bloc)

			err := initializeAWS(makeBlocCmd(t, tc.bloc))

			require.Error(t, err, "initializeAWS must return an error for bloc=%q", tc.bloc)
			assert.True(t,
				errors.Is(err, ErrBlocFormatInvalid),
				"error must wrap ErrBlocFormatInvalid for bloc=%q; got: %v", tc.bloc, err,
			)
		})
	}
}

// TestInitAWS_BlocFromEnvVarWhenFlagAbsent verifies that OCFP_BLOC is used when
// no --bloc flag is set on the command line.
func TestInitAWS_BlocFromEnvVarWhenFlagAbsent(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupAWSInitEnv(t)

	const envBloc = "env-bloc"
	t.Setenv("OCFP_BLOC", envBloc)
	// No --bloc flag on command line.

	err := initializeAWS(makeCmdNoBloc(t))
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

// TestInitAWS_WritesBothEnvFiles verifies that initializeAWS writes both the
// mgmt env file and the ocf env file for a valid bloc.
func TestInitAWS_WritesBothEnvFiles(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupAWSInitEnv(t)

	const bloc = "test-bloc"
	viper.Set("bloc", bloc)

	err := initializeAWS(makeBlocCmd(t, bloc))
	require.NoError(t, err)

	mgmtPath := filepath.Join(tmpDir, bloc, "deployments", "mgmt", bloc+"-mgmt.yml")
	ocfPath := filepath.Join(tmpDir, bloc, "deployments", "ocf", bloc+"-ocf.yml")

	_, err = os.Stat(mgmtPath)
	require.NoError(t, err, "mgmt env file must exist at %s", mgmtPath)

	_, err = os.Stat(ocfPath)
	require.NoError(t, err, "ocf env file must exist at %s", ocfPath)
}

// TestInitAWS_MgmtFileHasCreateEnvAndBoshKit verifies that the mgmt env file
// has use_create_env: true and kit.name: bosh.
func TestInitAWS_MgmtFileHasCreateEnvAndBoshKit(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupAWSInitEnv(t)

	const bloc = "test-bloc"
	viper.Set("bloc", bloc)

	require.NoError(t, initializeAWS(makeBlocCmd(t, bloc)))

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

// TestInitAWS_OcfFileHasNoCreateEnvAndCfKit verifies that the ocf env file
// has no use_create_env key (or false) and kit.name: cf.
func TestInitAWS_OcfFileHasNoCreateEnvAndCfKit(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupAWSInitEnv(t)

	const bloc = "test-bloc"
	viper.Set("bloc", bloc)

	require.NoError(t, initializeAWS(makeBlocCmd(t, bloc)))

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

// TestInitAWS_BothFilesHaveBlocAndAWSIAAS verifies that both env files carry
// ocfp.bloc: <bloc> and kit.iaas: aws.
func TestInitAWS_BothFilesHaveBlocAndAWSIAAS(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupAWSInitEnv(t)

	const bloc = "test-bloc"
	viper.Set("bloc", bloc)

	require.NoError(t, initializeAWS(makeBlocCmd(t, bloc)))

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
			assert.Equal(t, "aws", kitBlock["iaas"],
				"kit.iaas must equal aws; content:\n%s", bloc, string(data))
		})
	}
}

// TestInitAWS_FlagOverridesEnvVar verifies that when both --bloc (explicit flag)
// and OCFP_BLOC are set, the flag value wins.
func TestInitAWS_FlagOverridesEnvVar(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	tmpDir := setupAWSInitEnv(t)

	const (
		envBloc  = "env-bloc"
		flagBloc = "cli-bloc"
	)

	t.Setenv("OCFP_BLOC", envBloc)
	viper.Set("bloc", flagBloc) // mirrors what cobra/viper would set from the flag

	// cmd has --bloc=flagBloc explicitly on the command line (Changed == true).
	err := initializeAWS(makeBlocCmd(t, flagBloc))
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

// TestInitAWS_UsesXDGDataHomeWhenOCFPHomeUnset verifies that with OCFP_HOME
// unset, the generated deployment env files land under the XDG data root
// (config.OcfpBlocDir(bloc)/deployments/...), not a hardcoded legacy path.
// A fake HOME is set so that, were the implementation to fall back to the
// legacy ~/.ocfp layout, it would land in a throwaway temp dir rather than
// the real developer home directory.
func TestInitAWS_UsesXDGDataHomeWhenOCFPHomeUnset(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	// testSafetyGuard (TestMain sets OCFP_TEST_SAFETY_GUARD=1 package-wide)
	// compares OcfpHome() against a real-home-derived legacy root computed
	// from the SAME os.UserHomeDir() call, so it panics whenever OCFP_HOME
	// is unset regardless of a faked HOME. Disable it for this test only;
	// the faked HOME below still protects the real home directory.
	t.Setenv("OCFP_TEST_SAFETY_GUARD", "")
	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", t.TempDir())

	xdgDataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdgDataHome)

	const bloc = "xdg-bloc"
	viper.Set("bloc", bloc)

	// Resolve the expected path BEFORE any write happens: at this point
	// neither the new XDG path nor the legacy path exists, so
	// config.OcfpBlocDir's dual-read returns the new XDG location. Resolving
	// it after the write would let dual-read mask a legacy write as a pass.
	wantDeploymentsDir := filepath.Join(config.OcfpBlocDir(bloc), "deployments")

	err := initializeAWS(makeBlocCmd(t, bloc))
	require.NoError(t, err)

	envPath := filepath.Join(wantDeploymentsDir, "mgmt", bloc+"-mgmt.yml")
	data, err := os.ReadFile(envPath)
	require.NoError(t, err, "env file must exist under the XDG data root at %s", envPath)

	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &parsed))

	ocfpBlock, ok := parsed["ocfp"].(map[string]interface{})
	require.True(t, ok, "ocfp: block must be present; content:\n%s", string(data))
	assert.Equal(t, bloc, ocfpBlock["bloc"])

	// Regression guard: the legacy ~/.ocfp layout must not have been used.
	legacyDir := filepath.Join(os.Getenv("HOME"), ".ocfp")
	_, statErr := os.Stat(legacyDir)
	assert.True(t, os.IsNotExist(statErr),
		"legacy ~/.ocfp dir must not be created when XDG_DATA_HOME is set; found %s", legacyDir)
}
