package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateLoadHome points every home the loader reads at a temporary
// directory and clears viper, whose flag values override config settings.
func isolateLoadHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	viper.Reset()
	t.Cleanup(viper.Reset)

	return home
}

// fakePVEAuth is the PVE token pair validation requires, at the top level of
// a flat file.
const fakePVEAuth = "auth_token: FAKE-TOKEN-DO-NOT-USE\ntoken_secret: FAKE-SECRET-DO-NOT-USE\n"

// fakeMappedPVEAuth is fakePVEAuth indented for a blocs: map entry.
const fakeMappedPVEAuth = "    auth_token: FAKE-TOKEN-DO-NOT-USE\n    token_secret: FAKE-SECRET-DO-NOT-USE\n"

func writeLoadConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "bloc.yml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

func TestLoadBlocFromFile_LoadsAFlatFileWithDefaults(t *testing.T) {
	isolateLoadHome(t)

	path := writeLoadConfig(t, "name: flat\nprovider: pve\nregion: lab\n"+fakePVEAuth)

	cfg, err := LoadBlocFromFile(path, "flat")
	require.NoError(t, err)
	assert.Equal(t, "flat", cfg.Name)
	assert.Equal(t, "pve", cfg.Provider)
	assert.Equal(t, "pve", cfg.IaaS, "validation fills iaas from provider")
	assert.Equal(t, "lab", cfg.Region)
	assert.Equal(t, "ubuntu", cfg.Bastion.SSHUser, "provider defaults are applied")
	assert.NotNil(t, cfg.NetworkCatalog, "the network catalog is built")
}

func TestLoadBlocFromFile_LoadsABlocFromABlocsMap(t *testing.T) {
	isolateLoadHome(t)

	path := writeLoadConfig(t, "blocs:\n  mapped:\n    provider: pve\n"+fakeMappedPVEAuth)

	cfg, err := LoadBlocFromFile(path, "mapped")
	require.NoError(t, err)
	assert.Equal(t, "mapped", cfg.Name)
	assert.Equal(t, "ubuntu", cfg.Bastion.SSHUser)
}

func TestLoadBlocFromFile_ABlocsMapEntryWinsOverTheFlatBloc(t *testing.T) {
	isolateLoadHome(t)

	path := writeLoadConfig(t, "name: both\nprovider: aws\nblocs:\n  both:\n    provider: pve\n"+fakeMappedPVEAuth)

	cfg, err := LoadBlocFromFile(path, "both")
	require.NoError(t, err)
	assert.Equal(t, "pve", cfg.Provider)
}

func TestLoadBlocFromFile_ValidatesAFlatFile(t *testing.T) {
	isolateLoadHome(t)

	path := writeLoadConfig(t, "name: flat\nprovider: nonesuch\n")

	_, err := LoadBlocFromFile(path, "flat")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrBlocNotDefined)
	assert.Contains(t, err.Error(), "invalid configuration")
}

func TestLoadBlocFromFile_AFlatFileForAnotherBlocIsNotFound(t *testing.T) {
	isolateLoadHome(t)

	cases := map[string]string{
		"another name": "name: other\nprovider: pve\n",
		"no provider":  "name: flat\niaas: pve\n",
		"no name":      "provider: pve\n",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadBlocFromFile(writeLoadConfig(t, body), "flat")
			require.ErrorIs(t, err, ErrBlocNotDefined)
		})
	}
}

func TestLoadBlocFromFile_ANullBlocDefinitionIsNotReplacedByTheFlatBloc(t *testing.T) {
	isolateLoadHome(t)

	path := writeLoadConfig(t, "name: bare\nprovider: pve\nblocs:\n  bare: null\n")

	_, err := LoadBlocFromFile(path, "bare")
	require.ErrorIs(t, err, ErrBlocDefinitionEmpty)
}

func TestLoadBlocFromFile_RequiresAFileAndABloc(t *testing.T) {
	isolateLoadHome(t)

	_, err := LoadBlocFromFile("", "flat")
	require.ErrorIs(t, err, ErrNoConfigFile)

	_, err = LoadBlocFromFile(writeLoadConfig(t, "name: flat\nprovider: pve\n"), "")
	require.ErrorIs(t, err, ErrNoBlocName)
}

func TestLoadBlocFromFile_DoesNotChangeWhatLoadWithParamsReturns(t *testing.T) {
	isolateLoadHome(t)

	path := writeLoadConfig(t, "name: flat\nprovider: pve\n"+fakePVEAuth)

	_, err := LoadBlocFromFile(path, "flat")
	require.NoError(t, err)

	_, err = LoadWithParams(path, "flat")
	require.ErrorIs(t, err, ErrBlocNotDefined, "the blocs-map loader still reads only the blocs map")
}
