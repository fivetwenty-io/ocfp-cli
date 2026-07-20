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
	fake.outputs["safe -T ocfp-lab-drgao-inception get secret/config/ocfp-lab-drgao/aws:region"] = []byte("us-east-1\n")
	fake.outputs["safe get secret/config/ocfp-lab-drgao/aws:region"] = []byte("WRONG-BLOC-VALUE\n")

	restore := installFakeRunner(fake)
	defer restore()

	out, err := safeGetForBloc(context.Background(), "ocfp-lab-drgao", "secret/config/ocfp-lab-drgao/aws:region")
	require.NoError(t, err)

	assert.Equal(t, "us-east-1", strings.TrimSpace(string(out)))
	assert.NotContains(t, string(out), "WRONG-BLOC-VALUE", "must not read the global current target")
}

// Once migration has run the inception target is gone and the bloc's secrets
// live in <bloc>-mgmt, so that target is tried next — still explicitly.
func TestSafeGetForBloc_FallsBackToMgmtTarget(t *testing.T) {
	fake := newFakeRunner()
	fake.errs["safe -T ocfp-lab-drgao-inception get secret/config/ocfp-lab-drgao/aws:region"] = errSafeTargetMissing
	fake.outputs["safe -T ocfp-lab-drgao-mgmt get secret/config/ocfp-lab-drgao/aws:region"] = []byte("eu-central-1\n")

	restore := installFakeRunner(fake)
	defer restore()

	out, err := safeGetForBloc(context.Background(), "ocfp-lab-drgao", "secret/config/ocfp-lab-drgao/aws:region")
	require.NoError(t, err)

	assert.Equal(t, "eu-central-1", strings.TrimSpace(string(out)))
}

// With no bloc there is nothing to pin to, so the call keeps its historic
// shape rather than inventing a target name.
func TestSafeGetForBloc_NoBlocUsesUnpinnedGet(t *testing.T) {
	fake := newFakeRunner()
	fake.outputs["safe get secret/handoff:token"] = []byte("tok\n")

	restore := installFakeRunner(fake)
	defer restore()

	out, err := safeGetForBloc(context.Background(), "", "secret/handoff:token")
	require.NoError(t, err)

	assert.Equal(t, "tok", strings.TrimSpace(string(out)))
}

func TestRetrieveAWSRegionFromVault_UsesBlocTarget(t *testing.T) {
	fake := newFakeRunner()
	fake.outputs["safe -T ocfp-lab-drgao-inception get secret/config/ocfp-lab-drgao/aws:region"] = []byte("us-west-2\n")
	fake.outputs["safe get secret/config/ocfp-lab-drgao/aws:region"] = []byte("WRONG-BLOC-VALUE\n")

	restore := installFakeRunner(fake)
	defer restore()

	creds := &AWSCredentials{} //nolint:exhaustruct // only Region is under test

	err := retrieveAWSRegionFromVault(context.Background(), "ocfp-lab-drgao", creds)
	require.NoError(t, err)

	assert.Equal(t, "us-west-2", creds.Region)
}
