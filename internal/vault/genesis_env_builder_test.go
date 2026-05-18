package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// goldenAWSBOSHDirectorEnv is the expected YAML for WriteEnvFileV32 with
// useCreateEnv=true, bloc="ocfp-aws-us-east-1", iaas="aws", kit="bosh".
// env name: "mgmt" renders as "ocfp-aws-us-east-1-mgmt".
const goldenAWSBOSHDirectorEnv = `---
genesis:
    env: ocfp-aws-us-east-1-mgmt
    use_create_env: true
    min_version: 3.2.0
kit:
    name: bosh
    version: latest
    iaas: aws
ocfp:
    bloc: ocfp-aws-us-east-1
`

// TestGenesisEnvV32StructToYAML verifies the full struct→YAML golden output for
// a minimal AWS BOSH director env with use_create_env=true.
func TestGenesisEnvV32StructToYAML(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{
			Env:          "ocfp-aws-us-east-1-mgmt",
			UseCreateEnv: true,
			MinVersion:   "3.2.0",
		},
		Kit: KitBlockV32{
			Name:    "bosh",
			Version: "latest",
			IAAS:    "aws",
		},
		OCFP: &OCFPBlock{
			Bloc: "ocfp-aws-us-east-1",
		},
	}

	data, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)
	assert.Equal(t, goldenAWSBOSHDirectorEnv, string(data))
}

// TestGenesisEnvV32KitIAASPresent verifies kit.iaas is serialized when set.
func TestGenesisEnvV32KitIAASPresent(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{Env: "test-mgmt"},
		Kit: KitBlockV32{
			Name:    "bosh",
			Version: "latest",
			IAAS:    "aws",
		},
		OCFP: &OCFPBlock{Bloc: "test-bloc"},
	}

	data, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)
	assert.Contains(t, string(data), "iaas: aws")
}

// TestGenesisEnvV32KitIAASAbsentWhenEmpty verifies kit.iaas is omitted when empty.
func TestGenesisEnvV32KitIAASAbsentWhenEmpty(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{Env: "test-env"},
		Kit: KitBlockV32{
			Name:    "cf",
			Version: "latest",
		},
		OCFP: &OCFPBlock{Bloc: "test-bloc"},
	}

	data, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "iaas:")
}

// TestGenesisEnvV32OCFPBlocPresent verifies ocfp.bloc is serialized.
func TestGenesisEnvV32OCFPBlocPresent(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{Env: "myenv"},
		Kit:     KitBlockV32{Name: "bosh", Version: "latest", IAAS: "aws"},
		OCFP:    &OCFPBlock{Bloc: "blacksmith-us-east-1"},
	}

	data, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)
	assert.Contains(t, string(data), "bloc: blacksmith-us-east-1")
}

// TestGenesisEnvV32UseCreateEnvTrueOnlyWhenSet verifies use_create_env is
// present when useCreateEnv=true and absent when false.
func TestGenesisEnvV32UseCreateEnvTrueOnlyWhenSet(t *testing.T) {
	t.Run("true when set", func(t *testing.T) {
		env := GenesisEnvV32{
			Genesis: GenesisBlockV32{Env: "mgmt", UseCreateEnv: true},
			Kit:     KitBlockV32{Name: "bosh", Version: "latest", IAAS: "aws"},
			OCFP:    &OCFPBlock{Bloc: "test-bloc"},
		}
		data, err := marshalGenesisEnvV32(env)
		require.NoError(t, err)
		assert.Contains(t, string(data), "use_create_env: true")
	})

	t.Run("absent when false", func(t *testing.T) {
		env := GenesisEnvV32{
			Genesis: GenesisBlockV32{Env: "sandbox"},
			Kit:     KitBlockV32{Name: "cf", Version: "latest"},
			OCFP:    &OCFPBlock{Bloc: "test-bloc"},
		}
		data, err := marshalGenesisEnvV32(env)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "use_create_env")
	})
}

// TestGenesisEnvV32NoVaultPath verifies params.vault_path never appears in output.
func TestGenesisEnvV32NoVaultPath(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{Env: "mgmt", UseCreateEnv: true, MinVersion: "3.2.0"},
		Kit:     KitBlockV32{Name: "bosh", Version: "latest", IAAS: "aws"},
		OCFP:    &OCFPBlock{Bloc: "ocfp-aws-us-east-1"},
		Params:  map[string]any{"aws_region": "us-east-1"},
	}

	data, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "vault_path")
}

// TestGenesisEnvV32FeaturesRoundTrip verifies kit.features serializes correctly.
func TestGenesisEnvV32FeaturesRoundTrip(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{Env: "sandbox"},
		Kit: KitBlockV32{
			Name:     "cf",
			Version:  "2.8.0",
			Features: []string{"aws", "s3-blobstore"},
		},
		OCFP: &OCFPBlock{Bloc: "my-bloc"},
	}

	data, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal(data, &parsed))

	kit, ok := parsed["kit"].(map[string]any)
	require.True(t, ok)
	features, ok := kit["features"].([]any)
	require.True(t, ok)
	assert.Equal(t, []any{"aws", "s3-blobstore"}, features)
}

// TestGenesisEnvV32OCFPBlockOmittedWhenBlocEmpty verifies the ocfp block is
// omitted from output when Bloc is empty.
func TestGenesisEnvV32OCFPBlockOmittedWhenBlocEmpty(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{Env: "sandbox"},
		Kit:     KitBlockV32{Name: "cf", Version: "latest"},
		OCFP:    &OCFPBlock{Bloc: ""},
	}

	data, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "ocfp:")
}

// TestWriteEnvFileV32_GoldenAWSBOSHDirector tests the full WriteEnvFileV32 path
// end-to-end: writes file, reads back, verifies YAML structure.
func TestWriteEnvFileV32_GoldenAWSBOSHDirector(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mgmt.yml")

	err := WriteEnvFileV32(path, "mgmt", true, "ocfp-aws-us-east-1", "aws", "bosh")
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	content := string(data)

	// env name must be bloc-envName
	assert.Contains(t, content, "env: ocfp-aws-us-east-1-mgmt")
	assert.Contains(t, content, "use_create_env: true")
	assert.Contains(t, content, "min_version: 3.2.0")
	assert.Contains(t, content, "name: bosh")
	assert.Contains(t, content, "version: latest")
	assert.Contains(t, content, "iaas: aws")
	assert.Contains(t, content, "bloc: ocfp-aws-us-east-1")
	assert.NotContains(t, content, "vault_path")

	// must start with YAML document marker
	assert.True(t, strings.HasPrefix(content, "---\n"), "expected YAML document marker at start")

	// must be valid YAML
	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal(data, &parsed))
}

// TestWriteEnvFileV32_NoCreateEnv verifies use_create_env absent when false.
func TestWriteEnvFileV32_NoCreateEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sandbox.yml")

	err := WriteEnvFileV32(path, "sandbox", false, "my-bloc", "aws", "cf")
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "use_create_env")
}

// TestWriteEnvFileV32_CreatesParentDir verifies missing parent directories are
// created rather than failing.
func TestWriteEnvFileV32_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "env.yml")

	err := WriteEnvFileV32(path, "env", false, "bloc", "aws", "cf")
	require.NoError(t, err)

	_, err = os.Stat(path)
	require.NoError(t, err, "file must exist after WriteEnvFileV32")
}

// TestWriteEnvFileV32_InvalidPath verifies error on empty path.
func TestWriteEnvFileV32_InvalidPath(t *testing.T) {
	err := WriteEnvFileV32("", "env", false, "bloc", "aws", "cf")
	require.Error(t, err)
}

// TestWriteEnvFileV32_EmptyEnvName verifies error on empty env name.
func TestWriteEnvFileV32_EmptyEnvName(t *testing.T) {
	dir := t.TempDir()
	err := WriteEnvFileV32(filepath.Join(dir, "env.yml"), "", false, "bloc", "aws", "cf")
	require.Error(t, err)
}

// TestWriteEnvFileV32_EmptyBloc verifies error on empty bloc.
func TestWriteEnvFileV32_EmptyBloc(t *testing.T) {
	dir := t.TempDir()
	err := WriteEnvFileV32(filepath.Join(dir, "env.yml"), "env", false, "", "aws", "cf")
	require.Error(t, err)
}

// TestWriteEnvFileV32_EmptyKit verifies error on empty kit name.
func TestWriteEnvFileV32_EmptyKit(t *testing.T) {
	dir := t.TempDir()
	err := WriteEnvFileV32(filepath.Join(dir, "env.yml"), "env", false, "bloc", "aws", "")
	require.Error(t, err)
}

// TestWriteEnvFileV32_EnvNameComposedFromBlocAndStem verifies the genesis.env
// field is "<bloc>-<envName>" when envName does not already start with bloc.
func TestWriteEnvFileV32_EnvNameComposedFromBlocAndStem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ocf.yml")

	err := WriteEnvFileV32(path, "ocf", false, "mybloc", "aws", "cf")
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "env: mybloc-ocf")
}

// TestWriteEnvFileV32_EnvNameNotDoubledWhenAlreadyPrefixed verifies the
// genesis.env field is not doubled when envName already starts with bloc.
func TestWriteEnvFileV32_EnvNameNotDoubledWhenAlreadyPrefixed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "full.yml")

	err := WriteEnvFileV32(path, "mybloc-ocf", false, "mybloc", "aws", "cf")
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	content := string(data)
	// must appear exactly once, not doubled
	count := strings.Count(content, "env: mybloc-ocf")
	assert.Equal(t, 1, count)
	assert.NotContains(t, content, "env: mybloc-mybloc-ocf")
}

// TestWriteEnvFileV32_FilePermissions verifies written file has mode 0600.
func TestWriteEnvFileV32_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "perm.yml")

	err := WriteEnvFileV32(path, "perm", false, "bloc", "aws", "cf")
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

// TestWriteEnvFileV32_MinVersionAlwaysPresent verifies min_version: 3.2.0
// appears in both create-env and non-create-env deployments.
func TestWriteEnvFileV32_MinVersionAlwaysPresent(t *testing.T) {
	t.Run("create-env path sets min_version", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "mgmt.yml")
		err := WriteEnvFileV32(path, "mgmt", true, "my-bloc", "aws", "bosh")
		require.NoError(t, err)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Contains(t, string(data), "min_version: 3.2.0")
	})

	t.Run("non-create-env path also sets min_version", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "ocf.yml")
		err := WriteEnvFileV32(path, "ocf", false, "my-bloc", "aws", "cf")
		require.NoError(t, err)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Contains(t, string(data), "min_version: 3.2.0",
			"min_version must be present even when use_create_env is false")
	})
}

// TestWriteEnvFileV32_CreateEnvRequiresIAAS verifies that use_create_env=true
// with an empty iaas returns an error.
func TestWriteEnvFileV32_CreateEnvRequiresIAAS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mgmt.yml")
	err := WriteEnvFileV32(path, "mgmt", true, "my-bloc", "", "bosh")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "iaas")
}

// TestWriteEnvFileV32_Opts_Features verifies that WriteEnvFileV32Opts.Features
// round-trips through the written YAML file.
func TestWriteEnvFileV32_Opts_Features(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mgmt.yml")
	opts := WriteEnvFileV32Opts{
		Path:         path,
		EnvName:      "mgmt",
		UseCreateEnv: true,
		Bloc:         "my-bloc",
		IAAS:         "aws",
		Kit:          "bosh",
		Features:     []string{"ocfp", "vault"},
	}
	err := WriteEnvFileV32Opts_Write(opts)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "ocfp")
	assert.Contains(t, content, "vault")
	assert.Contains(t, content, "features:")
}

// TestWriteEnvFileV32_Opts_Params verifies that WriteEnvFileV32Opts.Params
// round-trips through the written YAML file.
func TestWriteEnvFileV32_Opts_Params(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ocf.yml")
	opts := WriteEnvFileV32Opts{
		Path:    path,
		EnvName: "ocf",
		Bloc:    "my-bloc",
		IAAS:    "aws",
		Kit:     "cf",
		Params:  map[string]any{"aws_region": "us-east-1", "scale": "medium"},
	}
	err := WriteEnvFileV32Opts_Write(opts)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "aws_region: us-east-1")
	assert.Contains(t, content, "scale: medium")
}

// TestWriteEnvFileV32_NoOCFPBlockWhenBlocEmpty verifies the ocfp block is
// omitted from output when WriteEnvFileV32Opts.Bloc is empty, which is not
// a valid call (bloc is required) — tests that the explicit gate holds at
// the opts layer too.
func TestWriteEnvFileV32_Opts_NoOCFPBlockWhenBlocEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-bloc.yml")
	opts := WriteEnvFileV32Opts{
		Path:    path,
		EnvName: "env",
		Kit:     "cf",
	}
	// bloc is empty — should return an error
	err := WriteEnvFileV32Opts_Write(opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bloc")
}
