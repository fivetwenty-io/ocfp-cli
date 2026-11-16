package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errSafeTargetMissing stands in for `safe` exiting non-zero because the
// requested target does not exist in ~/.saferc.
var errSafeTargetMissing = errors.New("exit status 1: target not found")

// TestSafeGetForBloc_PinsTargetPerInvocation guards the third cross-bloc path:
// `safe get` with no target reads from the single global current target, so a
// sibling bloc running `safe target` first silently redirects this bloc's
// credential lookup to the sibling's vault.
func TestSafeGetForBloc_PinsTargetPerInvocation(t *testing.T) {
	fake := newFakeRunner()
	fake.outputs["safe -T ocfp-lab-drgao-mgmt get secret/config/ocfp-lab-drgao/aws:region"] = []byte("us-east-1\n")
	fake.outputs["safe get secret/config/ocfp-lab-drgao/aws:region"] = []byte("WRONG-BLOC-VALUE\n")

	restore := installFakeRunner(fake)
	defer restore()

	out, err := safeGetForBloc(context.Background(), "ocfp-lab-drgao", "secret/config/ocfp-lab-drgao/aws:region")
	require.NoError(t, err)

	assert.Equal(t, "us-east-1", strings.TrimSpace(string(out)))
	assert.NotContains(t, string(out), "WRONG-BLOC-VALUE", "must not read the global current target")
}

// Pre-migration blocs have no mgmt target, so the bloc's inception vault is
// tried next — still explicitly, never the global current target.
func TestSafeGetForBloc_FallsBackToInceptionTarget(t *testing.T) {
	fake := newFakeRunner()
	fake.errs["safe -T ocfp-lab-drgao-mgmt get secret/config/ocfp-lab-drgao/aws:region"] = errSafeTargetMissing
	fake.outputs["safe -T ocfp-lab-drgao-inception get secret/config/ocfp-lab-drgao/aws:region"] = []byte("eu-central-1\n")

	restore := installFakeRunner(fake)
	defer restore()

	out, err := safeGetForBloc(context.Background(), "ocfp-lab-drgao", "secret/config/ocfp-lab-drgao/aws:region")
	require.NoError(t, err)

	assert.Equal(t, "eu-central-1", strings.TrimSpace(string(out)))
}

// Every caller names a bloc, so a read without one has no target to pin
// to. It refuses rather than reading through the operator's current target.
func TestSafeGetForBloc_NoBlocRefuses(t *testing.T) {
	fake := newFakeRunner()
	fake.outputs["safe get secret/handoff:token"] = []byte("operator-target-value\n")

	restore := installFakeRunner(fake)
	defer restore()

	out, err := safeGetForBloc(context.Background(), "", "secret/handoff:token")
	require.ErrorIs(t, err, ErrBlocFlagOrEnvVarRequired)
	assert.Empty(t, out)
}

func TestRetrieveAWSRegionFromVault_UsesBlocTarget(t *testing.T) {
	fake := newFakeRunner()
	fake.outputs["safe -T ocfp-lab-drgao-mgmt get secret/config/ocfp-lab-drgao/aws:region"] = []byte("us-west-2\n")
	fake.outputs["safe get secret/config/ocfp-lab-drgao/aws:region"] = []byte("WRONG-BLOC-VALUE\n")

	restore := installFakeRunner(fake)
	defer restore()

	creds := &AWSCredentials{} //nolint:exhaustruct // only Region is under test

	err := retrieveAWSRegionFromVault(context.Background(), "ocfp-lab-drgao", creds)
	require.NoError(t, err)

	assert.Equal(t, "us-west-2", creds.Region)
}
