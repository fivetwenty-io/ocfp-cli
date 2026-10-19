package commands

import (
	"context"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestInitGCPParity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OCFP_HOME", dir)
	t.Setenv("OCFP_BLOC", "test-gcp")
	err := (&initFlags{}).executeInitialization(context.Background(), nil, &config.Config{Provider: "gcp", Region: "us-central1-a"}, "gcp")
	require.NoError(t, err)
	for _, deployment := range []string{"mgmt", "ocf", "prometheus"} {
		env := "ocf"
		if deployment == "mgmt" {
			env = "mgmt"
		}
		data, err := os.ReadFile(filepath.Join(dir, "test-gcp", "deployments", deployment, "test-gcp-"+env+".yml"))
		require.NoError(t, err)
		require.Contains(t, string(data), "iaas: gcp")
	}
}

func TestInitGCPPreservesExistingEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OCFP_HOME", dir)
	t.Setenv("OCFP_BLOC", "test-gcp")
	path := filepath.Join(dir, "test-gcp", "deployments", "mgmt", "test-gcp-mgmt.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("operator configuration"), 0600))
	err := (&initFlags{}).executeInitialization(context.Background(), nil, &config.Config{}, "gcp")
	require.Error(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "operator configuration", string(data))
}
