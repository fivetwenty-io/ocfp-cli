package vault

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newTargetTestManager builds a Manager with only the fields the target-name
// helpers read, so the tests need no live vault.
func newTargetTestManager(blocName string) *Manager {
	return &Manager{ //nolint:exhaustruct // only the fields under test are needed
		blocName: blocName,
		logger:   zap.NewNop().Sugar(),
	}
}

// TestGetProductionVaultName_PrefersOwnBlocMgmt guards the migration
// destination. Reading the global current target here means `ocfp vault
// migrate` for one bloc can copy its secrets into a sibling's vault.
func TestGetProductionVaultName_PrefersOwnBlocMgmt(t *testing.T) {
	twoBlocSaferc(t)

	mgr := newTargetTestManager("ocfp-lab-krutten")

	name, err := mgr.getProductionVaultName()
	require.NoError(t, err)

	assert.Equal(t, "ocfp-lab-krutten-mgmt", name)
	assert.NotEqual(t, "ocfp-lab-drhu-inception", name, "must not follow the global current target")
}

func TestGetProductionVaultName_NoBlocFallsBackToCurrent(t *testing.T) {
	twoBlocSaferc(t)

	mgr := newTargetTestManager("")

	name, err := mgr.getProductionVaultName()
	require.NoError(t, err)

	assert.Equal(t, "ocfp-lab-drhu-inception", name)
}

// TestGetInceptionVaultName_NeverFallsBackToSharedName guards the source side:
// the bare "inception" target is shared by every bloc, so a bloc-scoped run
// must never silently land on it.
func TestGetInceptionVaultName_NeverFallsBackToSharedName(t *testing.T) {
	twoBlocSaferc(t)

	mgr := newTargetTestManager("ocfp-lab-dbell")

	assert.Equal(t, "ocfp-lab-dbell-inception", mgr.getInceptionVaultName())
}

func TestGetInceptionVaultName_BareWhenNoBloc(t *testing.T) {
	twoBlocSaferc(t)

	mgr := newTargetTestManager("")

	assert.Equal(t, "inception", mgr.getInceptionVaultName())
}
