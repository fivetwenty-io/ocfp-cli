package commands

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrderedServiceFQDNs_MgmtDerivesInOrder verifies the mgmt env type's
// known services are returned in vault.MgmtServices order, each carrying its
// flat derived FQDN when systemScoped is false.
func TestOrderedServiceFQDNs_MgmtDerivesInOrder(t *testing.T) {
	t.Parallel()

	base := "ocf.example.lab.internal"

	got := orderedServiceFQDNs(vault.MgmtEnvType, nil, base, false)

	want := make([]serviceFQDN, 0, len(vault.MgmtServices))
	for _, service := range vault.MgmtServices {
		want = append(want, serviceFQDN{Service: service, FQDN: service + "." + base})
	}

	assert.Equal(t, want, got)
}

// TestOrderedServiceFQDNs_TailscaleBlocGetsSystemInfixPostFix models the
// D-09 world directly: a bloc whose ingress is tailscale (not cloudflared)
// still gets systemScoped == true via config.SystemScoped, so its
// system-scoped services (e.g. concourse) still derive the .system. infix.
func TestOrderedServiceFQDNs_TailscaleBlocGetsSystemInfixPostFix(t *testing.T) {
	t.Parallel()

	base := "ocf.example.lab.internal"

	got := orderedServiceFQDNs(vault.MgmtEnvType, nil, base, true)

	var concourseFQDN string

	for _, pair := range got {
		if pair.Service == "concourse" {
			concourseFQDN = pair.FQDN
		}
	}

	assert.Equal(t, "concourse.system."+base, concourseFQDN)
}

// TestOrderedServiceFQDNs_AlphabeticalExtrasAppendedAfterKnownServices
// verifies explicit-override services absent from the known services list
// are appended, in alphabetical order, after every known-service row.
func TestOrderedServiceFQDNs_AlphabeticalExtrasAppendedAfterKnownServices(t *testing.T) {
	t.Parallel()

	explicit := map[string]string{
		"zeta-extra": "zeta.example.lab.internal",
		"alpha-extra": "alpha.example.lab.internal",
	}

	got := orderedServiceFQDNs(vault.MgmtEnvType, explicit, "ocf.example.lab.internal", false)

	knownCount := len(vault.MgmtServices)
	assert.Len(t, got, knownCount+2)
	assert.Equal(t, "alpha-extra", got[knownCount].Service)
	assert.Equal(t, "alpha.example.lab.internal", got[knownCount].FQDN)
	assert.Equal(t, "zeta-extra", got[knownCount+1].Service)
	assert.Equal(t, "zeta.example.lab.internal", got[knownCount+1].FQDN)
}

// TestOrderedServiceFQDNs_EmptyBaseReturnsBlankFQDNs verifies an empty base
// domain with no explicit overrides yields blank FQDNs for every known
// service, never an error (the function has no error return at all).
func TestOrderedServiceFQDNs_EmptyBaseReturnsBlankFQDNs(t *testing.T) {
	t.Parallel()

	got := orderedServiceFQDNs(vault.OCFEnvType, nil, "", false)

	assert.Len(t, got, len(vault.OCFServices))

	for _, pair := range got {
		assert.Empty(t, pair.FQDN)
	}
}

// TestDashIfEmpty_RendersEmDashForBlankOtherwisePassesThrough verifies the
// shared render helper: blank input renders as an em dash, non-blank input
// passes through unchanged.
func TestDashIfEmpty_RendersEmDashForBlankOtherwisePassesThrough(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "—"},
		{name: "non-empty", in: "10.64.64.20", want: "10.64.64.20"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, dashIfEmpty(tt.in))
		})
	}
}

// TestFindReservedIP_MatchesInfraAndOcfpSubnets verifies findReservedIP
// matches reserved_<any-subnet-shape>_<role>_ip regardless of the middle
// subnet-name segment (the "-ocfp-" vs "-infra-" shapes ResolveReservedIP's
// own hardcoded key format misses), sorted, first match wins.
func TestFindReservedIP_MatchesInfraAndOcfpSubnets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outputs map[string]interface{}
		role    string
		want    string
	}{
		{
			name:    "ocfp subnet shape",
			outputs: map[string]interface{}{"reserved_myblc-ocfp-0_bastion_ip": "10.1.1.5"},
			role:    "bastion",
			want:    "10.1.1.5",
		},
		{
			name:    "infra subnet shape",
			outputs: map[string]interface{}{"reserved_myblc-infra-0_haproxy_ip": "10.2.2.9"},
			role:    "haproxy",
			want:    "10.2.2.9",
		},
		{
			name: "multiple matches picks first sorted key",
			outputs: map[string]interface{}{
				"reserved_myblc-ocfp-1_bosh_ip": "10.3.3.2",
				"reserved_myblc-ocfp-0_bosh_ip": "10.3.3.1",
			},
			role: "bosh",
			want: "10.3.3.1",
		},
		{
			name:    "no matching role",
			outputs: map[string]interface{}{"reserved_myblc-ocfp-0_bastion_ip": "10.1.1.5"},
			role:    "haproxy",
			want:    "",
		},
		{
			name:    "nil outputs",
			outputs: nil,
			role:    "bastion",
			want:    "",
		},
		{
			name:    "non-string value ignored",
			outputs: map[string]interface{}{"reserved_myblc-ocfp-0_bastion_ip": 42},
			role:    "bastion",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, findReservedIP(tt.outputs, tt.role))
		})
	}
}

// TestLoadReservedOutputs_ReturnsNilOnEmptyBlocName verifies loadReservedOutputs
// never surfaces an error to its caller: an empty bloc name (state.GetStateDir's
// own hard-error case) degrades to a nil map instead.
func TestLoadReservedOutputs_ReturnsNilOnEmptyBlocName(t *testing.T) {
	t.Parallel()

	assert.Nil(t, loadReservedOutputs(""))
}

// TestLoadReservedOutputs_ReturnsEmptyMapForNewBloc verifies a bloc with no
// existing state file yields a non-nil, empty map rather than nil — state's
// own Load() seeds a fresh, empty Outputs map in that case, and
// loadReservedOutputs passes it through unchanged.
func TestLoadReservedOutputs_ReturnsEmptyMapForNewBloc(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	got := loadReservedOutputs("brand-new-bloc-endpoints-test")
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// TestLoadReservedOutputs_ReturnsPersistedOutputs verifies loadReservedOutputs
// returns exactly the outputs map persisted by a real state.Manager save,
// under an isolated OCFP_HOME so this test never touches a real bloc's state.
func TestLoadReservedOutputs_ReturnsPersistedOutputs(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	blocName := "reserved-outputs-fixture-bloc"

	stateDir, err := state.GetStateDir(blocName)
	require.NoError(t, err)

	mgr, err := state.NewManager(stateDir)
	require.NoError(t, err)

	_, err = mgr.Load(blocName)
	require.NoError(t, err)

	require.NoError(t, mgr.SetOutput("reserved_"+blocName+"-ocfp-0_bastion_ip", "10.9.9.9"))
	require.NoError(t, mgr.Save())

	got := loadReservedOutputs(blocName)
	assert.Equal(t, "10.9.9.9", got["reserved_"+blocName+"-ocfp-0_bastion_ip"])
}

// TestExpectedIPForService_ReservedStateOrBastion covers the three cases in
// expectedIPForService's precedence: reserved-state present wins for a
// non-bastion role, absent yields blank, and bastion always uses
// cfg.BastionIP over any reserved-state entry for "bastion" (config wins,
// matching every provider's own precedence: pve.go, aws.go, stackit.go,
// gcp.go all check config.BastionIP before any reserved-IP fallback).
func TestExpectedIPForService_ReservedStateOrBastion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		service   string
		reserved  map[string]interface{}
		bastionIP string
		want      string
	}{
		{
			name:     "service present in reserved outputs",
			service:  "haproxy",
			reserved: map[string]interface{}{"reserved_bloc-ocfp-0_haproxy_ip": "10.5.5.5"},
			want:     "10.5.5.5",
		},
		{
			name:     "service absent from reserved outputs",
			service:  "haproxy",
			reserved: map[string]interface{}{},
			want:     "",
		},
		{
			name:      "bastion prefers cfg.BastionIP over reserved-state entry",
			service:   "bastion",
			reserved:  map[string]interface{}{"reserved_bloc-ocfp-0_bastion_ip": "10.6.6.6"},
			bastionIP: "10.7.7.7",
			want:      "10.7.7.7",
		},
		{
			name:     "bastion with no cfg.BastionIP falls back to reserved-state",
			service:  "bastion",
			reserved: map[string]interface{}{"reserved_bloc-ocfp-0_bastion_ip": "10.6.6.6"},
			want:     "10.6.6.6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, expectedIPForService(tt.service, tt.reserved, tt.bastionIP))
		})
	}
}

// TestOriginForService_ExactMatchOnly covers originForService's D-05
// exact-match-only precedence: rung O1 (cfExact) always wins when present;
// rung O2 (the OCF-tier-only wildcard-suffix fallthrough against cf.Origin)
// only fires for unmatched OCF-tier services; mgmt-tier never reaches O2;
// and no heuristic or service-name-based matching bridges a naming-scheme
// mismatch — a blank return is the common, correct outcome, not a gap.
func TestOriginForService_ExactMatchOnly(t *testing.T) {
	t.Parallel()

	base := "ocf.example.lab.internal"
	systemFQDN := "shield.system." + base

	cf := &config.CloudflareConfig{
		Origin:       "https://10.64.64.20",
		AppsDomain:   "apps." + base,
		SystemDomain: "system." + base,
	}

	tests := []struct {
		name    string
		envType string
		fqdn    string
		cfExact map[string]string
		cf      *config.CloudflareConfig
		want    string
	}{
		{
			// "Naming kept consistent" case: fqdn also suffix-matches
			// SystemDomain, but the exact cfExact entry (rung O1) wins over
			// the wildcard fallthrough (rung O2) unconditionally.
			name:    "TestOriginForService_ExactRouteWinsOverWildcard",
			envType: vault.OCFEnvType,
			fqdn:    systemFQDN,
			cfExact: map[string]string{systemFQDN: "10.0.0.9"},
			cf:      cf,
			want:    "10.0.0.9",
		},
		{
			// "Naming has drifted" case, shield: an explicit .util. override
			// FQDN disjoint from both the cfExact key (a different, .system.
			// hostname for the same service) and the wildcard-suffix rung
			// (the override string does not end in .system.<base>) — a pure
			// D-05 naming-independence example, unrelated to systemScoped.
			name:    "TestOriginForService_DisjointNamingSchemesAllBlank",
			envType: vault.MgmtEnvType,
			fqdn:    "shield.util." + base,
			cfExact: map[string]string{"shield.system." + base: "10.0.0.9"},
			cf:      cf,
			want:    "",
		},
		{
			// "Naming has drifted" case, gate-affected services: no explicit
			// override on the real bloc, so concourse's derived FQDN is
			// subject to the (now-fixed) systemScoped gate and carries the
			// .system. infix, exactly matching its cfExact entry.
			name:    "TestOriginForService_GateAffectedServicesPopulateOriginPostFix",
			envType: vault.MgmtEnvType,
			fqdn:    "concourse.system." + base,
			cfExact: map[string]string{"concourse.system." + base: "10.0.0.11"},
			cf:      cf,
			want:    "10.0.0.11",
		},
		{
			name:    "TestOriginForService_MgmtTierNeverGetsWildcardFallthrough",
			envType: vault.MgmtEnvType,
			fqdn:    "shield.system." + base,
			cfExact: map[string]string{},
			cf:      cf,
			want:    "",
		},
		{
			name:    "OCF-tier wildcard fallthrough fires when unmatched",
			envType: vault.OCFEnvType,
			fqdn:    "cf.apps." + base,
			cfExact: map[string]string{},
			cf:      cf,
			want:    "10.64.64.20",
		},
		{
			name:    "empty fqdn returns blank immediately",
			envType: vault.OCFEnvType,
			fqdn:    "",
			cfExact: map[string]string{"anything": "10.0.0.1"},
			cf:      cf,
			want:    "",
		},
		{
			name:    "nil cloudflare config, no exact match",
			envType: vault.OCFEnvType,
			fqdn:    "cf.apps." + base,
			cfExact: map[string]string{},
			cf:      nil,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := originForService(tt.envType, tt.fqdn, tt.cfExact, tt.cf)
			assert.Equal(t, tt.want, got)
		})
	}
}
