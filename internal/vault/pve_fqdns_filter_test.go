package vault

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedHeldFQDNs gives both planes a hand-set shield and prometheus value, so
// a forced write has something to clobber.
func seedHeldFQDNs(t *testing.T, provider *PVEVaultProvider, safe *writeLogSafe) {
	t.Helper()

	for _, env := range []string{MgmtEnvType, OCFEnvType} {
		require.NoError(t, safe.fakeSafe.SetMultiple(provider.PathBuilder.GetFQDNsPath(env), map[string]interface{}{
			"shield":     "hand-shield.example.io",
			"prometheus": "hand-prometheus.example.io",
		}))
	}
}

func TestFQDNsFilter_PlaneLimitsWrittenPlanes(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)

	require.NoError(t, provider.ConfigureFQDNsScoped(false, FQDNFilter{Planes: []string{OCFEnvType}}, nil))

	require.Len(t, safe.sent, 1)
	assert.Contains(t, safe.sent, provider.PathBuilder.GetFQDNsPath(OCFEnvType))
	assert.NotContains(t, safe.sent, provider.PathBuilder.GetFQDNsPath(MgmtEnvType))
}

func TestFQDNsFilter_KeyLimitsWrittenKeysWithoutForce(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)

	require.NoError(t, provider.ConfigureFQDNsScoped(false, FQDNFilter{Keys: []string{"shield"}}, nil))

	for _, env := range []string{MgmtEnvType, OCFEnvType} {
		sent := safe.sent[provider.PathBuilder.GetFQDNsPath(env)]
		assert.Equal(t, map[string]interface{}{"shield": "shield.ocf.example.io"}, sent,
			"only the named key goes out, and env_type and base stay out")
	}
}

func TestFQDNsFilter_KeyWithoutForceLeavesHeldKeyAlone(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)
	seedHeldFQDNs(t, provider, safe)

	require.NoError(t, provider.ConfigureFQDNsScoped(false, FQDNFilter{Keys: []string{"shield"}}, nil))

	assert.Empty(t, safe.sent, "a held key is not added again without force")
}

func TestFQDNsFilter_ForceWithKeyOverwritesOnlyThatKey(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)
	seedHeldFQDNs(t, provider, safe)

	require.NoError(t, provider.ConfigureFQDNsScoped(true, FQDNFilter{Keys: []string{"shield"}}, nil))

	for _, env := range []string{MgmtEnvType, OCFEnvType} {
		path := provider.PathBuilder.GetFQDNsPath(env)

		assert.Equal(t, map[string]interface{}{"shield": "shield.ocf.example.io"}, safe.sent[path])
		assert.Equal(t, "hand-prometheus.example.io", safe.data[path]["prometheus"],
			"a key outside the filter keeps its hand-set value under force")
	}
}

func TestFQDNsFilter_ForceWithPlaneAndKey(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)
	seedHeldFQDNs(t, provider, safe)

	filter := FQDNFilter{Planes: []string{MgmtEnvType}, Keys: []string{"shield", "prometheus"}}
	require.NoError(t, provider.ConfigureFQDNsScoped(true, filter, nil))

	mgmt := provider.PathBuilder.GetFQDNsPath(MgmtEnvType)
	ocf := provider.PathBuilder.GetFQDNsPath(OCFEnvType)

	require.Len(t, safe.sent, 1)
	assert.Equal(t, map[string]interface{}{
		"shield":     "shield.ocf.example.io",
		"prometheus": "prometheus.ocf.example.io",
	}, safe.sent[mgmt])
	assert.Equal(t, "hand-shield.example.io", safe.data[ocf]["shield"], "the other plane is untouched")
}

func TestFQDNsFilter_ForceWithPlaneOnlyOverwritesWholePlane(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)
	seedHeldFQDNs(t, provider, safe)

	require.NoError(t, provider.ConfigureFQDNsScoped(true, FQDNFilter{Planes: []string{OCFEnvType}}, nil))

	ocf := provider.PathBuilder.GetFQDNsPath(OCFEnvType)
	mgmt := provider.PathBuilder.GetFQDNsPath(MgmtEnvType)

	assert.Equal(t, "shield.ocf.example.io", safe.data[ocf]["shield"])
	assert.Equal(t, "hand-shield.example.io", safe.data[mgmt]["shield"])
}

func TestFQDNsFilter_EmptyFilterIsEverything(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)

	plan, err := provider.PlanFQDNs(false, FQDNFilter{})
	require.NoError(t, err)
	require.Len(t, plan, 2)

	assert.Contains(t, plan[0].Adds, "env_type")
	assert.Contains(t, plan[0].Adds, "base")
}

func TestFQDNsFilter_PlanFiltersInsidePlanFQDNs(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)

	plan, err := provider.PlanFQDNs(false, FQDNFilter{Planes: []string{MgmtEnvType}, Keys: []string{"shield"}})
	require.NoError(t, err)
	require.Len(t, plan, 1)

	assert.Equal(t, MgmtEnvType, plan[0].EnvType)
	assert.Equal(t, map[string]string{"shield": "shield.ocf.example.io"}, plan[0].Adds)
}

func TestFQDNsFilter_UnknownPlaneIsRejectedBeforeAnyRead(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)

	_, err := provider.PlanFQDNs(false, FQDNFilter{Planes: []string{"prod"}})
	require.ErrorIs(t, err, ErrUnknownFQDNPlane)
	assert.Contains(t, err.Error(), `"prod"`)
	assert.Contains(t, err.Error(), "mgmt")
	assert.Contains(t, err.Error(), "ocf")

	err = provider.ConfigureFQDNsScoped(true, FQDNFilter{Planes: []string{MgmtEnvType, "prod"}}, nil)
	require.ErrorIs(t, err, ErrUnknownFQDNPlane)
	assert.Empty(t, safe.sent)
}

func TestFQDNsFilter_UnknownKeyNamesKeyAndListsValidOnes(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)

	err := provider.ConfigureFQDNsScoped(true, FQDNFilter{Keys: []string{"shield", "nonesuch"}}, nil)
	require.ErrorIs(t, err, ErrUnknownFQDNKey)
	assert.Contains(t, err.Error(), `"nonesuch"`)
	assert.Contains(t, err.Error(), "prometheus")
	assert.Contains(t, err.Error(), "shield")
	assert.NotContains(t, err.Error(), "env_type")
	assert.Empty(t, safe.sent, "nothing is written when any key is invalid")
}

func TestFQDNsFilter_EnvTypeAndBaseAreNotSelectable(t *testing.T) {
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), newWriteLogSafe())

	for _, key := range []string{"env_type", "base"} {
		_, err := provider.PlanFQDNs(true, FQDNFilter{Keys: []string{key}})
		require.ErrorIs(t, err, ErrUnknownFQDNKey, key)
		assert.Contains(t, err.Error(), `"`+key+`"`)
	}
}

func TestFQDNsFilter_KeyMustExistInASelectedPlane(t *testing.T) {
	cfg := fqdnPhaseConfig()
	provider := newFQDNPhaseProvider(cfg, newWriteLogSafe())

	mgmtKeys := selectableFQDNKeys(provider.derivedFQDNRecord(MgmtEnvType))
	ocfKeys := selectableFQDNKeys(provider.derivedFQDNRecord(OCFEnvType))

	var onlyOCF string

	for _, key := range ocfKeys {
		found := false

		for _, mk := range mgmtKeys {
			found = found || mk == key
		}

		if !found {
			onlyOCF = key

			break
		}
	}

	if onlyOCF == "" {
		t.Skip("every ocf key is also a mgmt key, so no plane-specific key to test")
	}

	_, err := provider.PlanFQDNs(false, FQDNFilter{Planes: []string{MgmtEnvType}, Keys: []string{onlyOCF}})
	require.ErrorIs(t, err, ErrUnknownFQDNKey)

	_, err = provider.PlanFQDNs(false, FQDNFilter{Planes: []string{OCFEnvType}, Keys: []string{onlyOCF}})
	require.NoError(t, err)
}

func TestFQDNsFilter_DryRunListsOnlyFilteredKeysAndWritesNothing(t *testing.T) {
	safe := newWriteLogSafe()
	mgr := newReservedIPsScopeTestManager(fqdnPhaseConfig(), safe)
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)
	seedHeldFQDNs(t, provider, safe)

	opts := &PopulateOptions{ //nolint:exhaustruct
		Subcommand: PhaseFQDNs,
		DryRun:     true,
		Force:      true,
		FQDNPlanes: []string{MgmtEnvType},
		FQDNKeys:   []string{"shield"},
	}

	var out bytes.Buffer

	require.NoError(t, mgr.populateDryRun(opts, safe, "target", &out))

	assert.Empty(t, safe.sent, "a dry run must write nothing")
	assert.Contains(t, out.String(), "shield: hand-shield.example.io -> shield.ocf.example.io")
	assert.Contains(t, out.String(), "1 key(s)")
	assert.NotContains(t, out.String(), "prometheus")
	assert.NotContains(t, out.String(), "env_type")
	assert.NotContains(t, out.String(), "ocf (")
}

func TestFQDNsFilter_RealRunSummaryReflectsFilteredPlan(t *testing.T) {
	safe := newWriteLogSafe()
	mgr := newReservedIPsScopeTestManager(fqdnPhaseConfig(), safe)
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)
	seedHeldFQDNs(t, provider, safe)

	opts := &PopulateOptions{ //nolint:exhaustruct
		Subcommand: PhaseFQDNs,
		Force:      true,
		FQDNPlanes: []string{OCFEnvType},
		FQDNKeys:   []string{"prometheus"},
	}

	var out bytes.Buffer

	require.NoError(t, mgr.populateFQDNsPhase(opts, &out))

	ocf := provider.PathBuilder.GetFQDNsPath(OCFEnvType)

	assert.Equal(t, map[string]interface{}{"prometheus": "prometheus.ocf.example.io"}, safe.sent[ocf])
	assert.Len(t, safe.sent, 1)
	assert.Contains(t, out.String(), "prometheus")
	assert.NotContains(t, out.String(), "shield")
	assert.NotContains(t, out.String(), "mgmt")
}

func TestFQDNsFilter_RejectedOnOtherPhases(t *testing.T) {
	for _, phase := range []string{"", PhasePublicIPs, PhaseReservedIPs} {
		for _, opts := range []*PopulateOptions{
			{Subcommand: phase, FQDNPlanes: []string{MgmtEnvType}},              //nolint:exhaustruct
			{Subcommand: phase, FQDNKeys: []string{"shield"}},                   //nolint:exhaustruct
			{Subcommand: phase, DryRun: true, FQDNKeys: []string{"shield"}},     //nolint:exhaustruct
			{Subcommand: phase, DryRun: true, FQDNPlanes: []string{OCFEnvType}}, //nolint:exhaustruct
		} {
			safe := newWriteLogSafe()
			mgr := newReservedIPsScopeTestManager(fqdnPhaseConfig(), safe)

			var err error
			if opts.DryRun {
				err = mgr.populateDryRun(opts, safe, "target", &bytes.Buffer{})
			} else {
				err = mgr.populate(opts)
			}

			require.ErrorIs(t, err, ErrFQDNFilterWrongPhase, "phase %q", phase)
			assert.Empty(t, safe.sent)
		}
	}
}

func TestFQDNFilter_ValidateScope(t *testing.T) {
	require.NoError(t, FQDNFilter{}.ValidateScope(""))
	require.NoError(t, FQDNFilter{}.ValidateScope(PhasePublicIPs))
	require.NoError(t, FQDNFilter{Planes: []string{"mgmt"}, Keys: []string{"x"}}.ValidateScope(PhaseFQDNs))
	require.ErrorIs(t, FQDNFilter{Planes: []string{"mgmt"}}.ValidateScope(""), ErrFQDNFilterWrongPhase)
	require.ErrorIs(t, FQDNFilter{Keys: []string{"x"}}.ValidateScope(PhaseReservedIPs), ErrFQDNFilterWrongPhase)
	require.ErrorIs(t, FQDNFilter{Planes: []string{"prod"}}.ValidateScope(PhaseFQDNs), ErrUnknownFQDNPlane)
}

func TestFQDNsFilter_KeyWithNoFQDNsBlockIsRejected(t *testing.T) {
	cfg := fqdnPhaseConfig()
	cfg.FQDNs = nil

	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(cfg, safe)

	_, err := provider.PlanFQDNs(true, FQDNFilter{Keys: []string{"bogus"}})
	require.ErrorIs(t, err, ErrFQDNKeysUncheckable)
	assert.Contains(t, err.Error(), "no fqdns configuration")

	err = provider.ConfigureFQDNsScoped(true, FQDNFilter{Keys: []string{"bogus"}}, nil)
	require.ErrorIs(t, err, ErrFQDNKeysUncheckable)
	assert.Empty(t, safe.sent)

	plan, err := provider.PlanFQDNs(true, FQDNFilter{Planes: []string{MgmtEnvType}})
	require.NoError(t, err, "a plane-only filter still gets today's notice path")
	assert.Empty(t, plan)
}

func TestValidateFQDNFlagValues(t *testing.T) {
	tests := []struct {
		name    string
		given   bool
		values  []string
		wantErr bool
	}{
		{"flag not given", false, nil, false},
		{"flag given with a value", true, []string{"shield"}, false},
		{"flag given with several values", true, []string{"shield", "prometheus"}, false},
		{"flag given an empty string", true, []string{}, true},
		{"flag given an empty value", true, []string{""}, true},
		{"flag given only whitespace", true, []string{" \t"}, true},
		{"trailing comma leaves an empty item", true, []string{"a", ""}, true},
		{"leading comma leaves an empty item", true, []string{"", "a"}, true},
		{"nil slice from a given flag", true, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFQDNFlagValues("key", tt.given, tt.values)
			if !tt.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrFQDNFilterEmptyValue)
			assert.Contains(t, err.Error(), "--key")
		})
	}
}
