package vault

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverGenesisEnvironmentCandidatesFallsBackToWhich(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	envDir := filepath.Join(tmpDir, "ocfp", "deployments", "bosh")
	require.NoError(t, os.MkdirAll(filepath.Join(envDir, ".genesis"), 0o755))

	binPath := filepath.Join(tmpDir, "bin", "genesis")
	require.NoError(t, os.MkdirAll(filepath.Dir(binPath), 0o755))

	responses := map[string]struct {
		output string
		err    error
	}{
		"genesis envs --json":    {"", fmt.Errorf("missing genesis")},
		"genesis envs":           {"", fmt.Errorf("missing genesis")},
		"which genesis":          {binPath + "\n", nil},
		binPath + " envs --json": {fmt.Sprintf(`{"environments":[{"path":"%s"}]}`, envDir), nil},
	}

	var commands []string

	originalExecutor := commandExecutor
	commandExecutor = func(name string, args ...string) (string, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		commands = append(commands, key)
		if resp, ok := responses[key]; ok {
			return resp.output, resp.err
		}
		return "", fmt.Errorf("unexpected command: %s", key)
	}
	t.Cleanup(func() { commandExecutor = originalExecutor })

	gi := NewGenesisIntegration(&config.Config{}, "test-bloc")

	paths := gi.discoverGenesisEnvironmentCandidates(context.Background())

	require.Contains(t, paths, envDir)
	require.Equal(t, []string{
		"genesis envs --json",
		"genesis envs",
		"which genesis",
		binPath + " envs --json",
	}, commands)
}

func TestDiscoverGenesisEnvironmentCandidatesUsesShellFallback(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	envDir := filepath.Join(tmpDir, "workspace", "test-bloc")
	require.NoError(t, os.MkdirAll(filepath.Join(envDir, ".genesis"), 0o755))

	envFile := filepath.Join(envDir, "test-bloc-mgmt.yml")
	require.NoError(t, os.WriteFile(envFile, []byte("---\nname: test-bloc-mgmt\n"), 0o644))

	responses := map[string]struct {
		output string
		err    error
	}{
		"genesis envs --json":            {"", fmt.Errorf("missing genesis")},
		"genesis envs":                   {"", fmt.Errorf("missing genesis")},
		"which genesis":                  {"", fmt.Errorf("not found")},
		"sh -c genesis envs --json 2>&1": {fmt.Sprintf("found env %s", envFile), nil},
	}

	var commands []string

	originalExecutor := commandExecutor
	commandExecutor = func(name string, args ...string) (string, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		commands = append(commands, key)
		if resp, ok := responses[key]; ok {
			return resp.output, resp.err
		}
		return "", fmt.Errorf("unexpected command: %s", key)
	}
	t.Cleanup(func() { commandExecutor = originalExecutor })

	gi := NewGenesisIntegration(&config.Config{}, "test-bloc")

	paths := gi.discoverGenesisEnvironmentCandidates(context.Background())

	require.Contains(t, paths, envDir)
	require.Equal(t, []string{
		"genesis envs --json",
		"genesis envs",
		"which genesis",
		"sh -c genesis envs --json 2>&1",
	}, commands)
}

func TestFindGenesisDirectoryUsesDiscoveryFallback(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	blocName := "bloc-" + strings.ReplaceAll(t.Name(), "/", "-")

	envDir := filepath.Join(tmpDir, blocName)
	require.NoError(t, os.MkdirAll(filepath.Join(envDir, ".genesis"), 0o755))

	jsonOutput := fmt.Sprintf(`{"environments":[{"path":"%s"}]}`, envDir)

	originalExecutor := commandExecutor
	commandExecutor = func(name string, args ...string) (string, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		switch key {
		case "genesis envs --json", "genesis envs", "which genesis":
			return "", fmt.Errorf("not found")
		case "sh -c genesis envs --json 2>&1":
			return jsonOutput, nil
		default:
			return "", fmt.Errorf("unexpected command: %s", key)
		}
	}
	t.Cleanup(func() { commandExecutor = originalExecutor })

	gi := NewGenesisIntegration(&config.Config{}, blocName)

	dir, err := gi.findGenesisDirectory()
	require.NoError(t, err)
	require.Equal(t, envDir, dir)
}

func TestUpdateEnvironmentSecretsUsesDiscoveredDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	blocName := "bloc-" + strings.ReplaceAll(t.Name(), "/", "-")

	genesisDir := filepath.Join(tmpDir, blocName)
	require.NoError(t, os.MkdirAll(filepath.Join(genesisDir, ".genesis"), 0o755))

	jsonOutput := fmt.Sprintf(`{"environments":[{"path":"%s"}]}`, genesisDir)

	originalExecutor := commandExecutor
	commandExecutor = func(name string, args ...string) (string, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		switch key {
		case "genesis envs --json", "genesis envs", "which genesis":
			return "", fmt.Errorf("not found")
		case "sh -c genesis envs --json 2>&1":
			return jsonOutput, nil
		default:
			return "", fmt.Errorf("unexpected command: %s", key)
		}
	}
	t.Cleanup(func() { commandExecutor = originalExecutor })

	cfg := &config.Config{}
	gi := NewGenesisIntegration(cfg, blocName)

	err := gi.UpdateEnvironmentSecrets("https://vault.example", "token")
	require.NoError(t, err)

	// ensure mgmt environment file created in discovered directory
	mgmtFile := filepath.Join(genesisDir, fmt.Sprintf("%s-mgmt.yml", blocName))
	_, statErr := os.Stat(mgmtFile)
	require.NoError(t, statErr)
}

func TestCreateEnvironmentFileIncludesOCFPBloc(t *testing.T) {
	tmpDir := t.TempDir()
	blocName := "520-aws-wayne"
	cfg := &config.Config{}
	gi := NewGenesisIntegration(cfg, blocName)

	mgmtFile := filepath.Join(tmpDir, "mgmt.yml")
	err := gi.createEnvironmentFile(mgmtFile, MgmtEnvType, "https://vault.example")
	require.NoError(t, err)

	env, err := gi.readEnvironmentFile(mgmtFile)
	require.NoError(t, err)

	require.NotNil(t, env.OCFP, "OCFP config should be present in generated env file")
	assert.Equal(t, blocName, env.OCFP.Bloc, "OCFP bloc should match blocName")
	assert.Equal(t, fmt.Sprintf("%s-mgmt", blocName), env.Name)
}

func TestCreateEnvironmentFileOCFType(t *testing.T) {
	tmpDir := t.TempDir()
	blocName := "520-aws-wayne"
	cfg := &config.Config{}
	gi := NewGenesisIntegration(cfg, blocName)

	ocfFile := filepath.Join(tmpDir, "ocf.yml")
	err := gi.createEnvironmentFile(ocfFile, OCFEnvType, "https://vault.example")
	require.NoError(t, err)

	env, err := gi.readEnvironmentFile(ocfFile)
	require.NoError(t, err)

	require.NotNil(t, env.OCFP, "OCFP config should be present in generated env file")
	assert.Equal(t, blocName, env.OCFP.Bloc)
	assert.Equal(t, fmt.Sprintf("%s-ocf", blocName), env.Name)
}
