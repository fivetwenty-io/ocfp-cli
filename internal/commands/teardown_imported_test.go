package commands

import (
	"context"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTeardownPreservesImportedPrivateNetworks(t *testing.T) {
	sm, err := state.NewManager(t.TempDir())
	require.NoError(t, err)
	_, err = sm.Load("test")
	require.NoError(t, err)
	require.NoError(t, sm.AddResource(&state.Resource{Type: "subnet", ID: "external-sub", Name: "original", Properties: map[string]any{"imported": true, "preserve": true}}))
	mgr := NewTeardownManager(&config.Config{Network: config.NetworkConfig{ID: "external-net"}}, nil, sm, &TeardownOptions{BlocName: "test", Network: true})
	for _, r := range []*ResourceToDelete{{Type: "network", ID: "external-net", FromState: true}, {Type: "subnet", ID: "external-sub", Name: "discovered-different-name"}} {
		require.Empty(t, mgr.filterResources([]*ResourceToDelete{r}))
		require.ErrorContains(t, mgr.DeleteResource(context.Background(), r), "imported")
	}
}
