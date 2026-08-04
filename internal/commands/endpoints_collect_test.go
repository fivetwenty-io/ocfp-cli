package commands

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
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

// TestOrderedServiceFQDNs_OCFDerivesAutoscalerFlatFQDN verifies autoscaler
// (ocf-only: member of vault.OCFServices, absent from
// vault.MgmtServices) appears in the OCF-scoped ordered pairs with a flat
// derived FQDN — it is not in vault's systemScopedServices set, so it never
// gains the .system. infix even when systemScoped is true.
func TestOrderedServiceFQDNs_OCFDerivesAutoscalerFlatFQDN(t *testing.T) {
	t.Parallel()

	base := "ocf.example.lab.internal"

	got := orderedServiceFQDNs(vault.OCFEnvType, nil, base, true)

	var (
		autoscalerFQDN string
		found          bool
	)

	for _, pair := range got {
		if pair.Service == "autoscaler" {
			autoscalerFQDN = pair.FQDN
			found = true
		}
	}

	assert.True(t, found, "autoscaler must be present in the OCF-scoped service list")
	assert.Equal(t, "autoscaler."+base, autoscalerFQDN, "autoscaler is not system-scoped, keeps the flat form")
}

// TestOrderedServiceFQDNs_TailscaleBlocGetsSystemInfixPostFix models a
// tailscale-ingress bloc directly: a bloc whose ingress is tailscale (not cloudflared)
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
		"zeta-extra":  "zeta.example.lab.internal",
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

			assert.Equal(t, tt.want, findReservedIP(tt.outputs, tt.role, "myblc", nil))
		})
	}
}

// TestFindReservedIP_PrefersPinnedIndex verifies findReservedIP consults
// the bloc's layout when given one: a role the strategy pins to a single
// workload-subnet index resolves from that index's key even when a
// lower-sorted key (e.g. a stale ocfp-0 output from a pre-migration
// strategy) also matches. When the pinned index's key is absent, or the
// role is unpinned, the sorted-first fallback still applies.
func TestFindReservedIP_PrefersPinnedIndex(t *testing.T) {
	t.Parallel()

	spanning, err := netlayout.Lookup("spanning")
	require.NoError(t, err)

	outputs := map[string]interface{}{
		"reserved_myblc-ocfp-0_doomsday_ip": "10.4.4.18",
		"reserved_myblc-ocfp-1_doomsday_ip": "10.4.8.18",
		"reserved_myblc-ocfp-0_vault_ip":    "10.4.4.5",
		"reserved_myblc-ocfp-1_vault_ip":    "10.4.8.5",
		"reserved_myblc-ocfp-0_shout_ip":    "10.4.4.19",
	}

	tests := []struct {
		name   string
		role   string
		layout netlayout.Layout
		want   string
	}{
		{name: "pinned role prefers its index", role: "doomsday", layout: spanning, want: "10.4.8.18"},
		{name: "unpinned role keeps sorted-first", role: "vault", layout: spanning, want: "10.4.4.5"},
		{name: "nil layout keeps sorted-first", role: "doomsday", layout: nil, want: "10.4.4.18"},
		{name: "pinned key absent falls back to sorted-first", role: "shout", layout: spanning, want: "10.4.4.19"},
		{name: "role absent entirely", role: "ocfp_ui", layout: spanning, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, findReservedIP(outputs, tt.role, "myblc", tt.layout))
		})
	}
}

// TestFindReservedIP_PinnedMatchIsExactKey pins the pinned-index preference
// to the exact key "reserved_<bloc>-ocfp-<idx>_<role>_ip". A bloc whose own
// name ends in "-ocfp-<n>" produces keys whose bloc segment suffix-matches a
// subnet-index pattern; such a key must not satisfy the pin — when the
// pinned index's real key is absent, the sorted-first fallback applies.
func TestFindReservedIP_PinnedMatchIsExactKey(t *testing.T) {
	t.Parallel()

	spanning, err := netlayout.Lookup("spanning")
	require.NoError(t, err)

	outputs := map[string]interface{}{
		// The real ocfp-0 key (stale under spanning's doomsday pin to 1).
		"reserved_trap-ocfp-1-ocfp-0_doomsday_ip": "10.4.4.18",
		// Foreign key whose bloc segment merely ends in "-ocfp-1".
		"reserved_trap-ocfp-1_doomsday_ip": "10.9.9.9",
	}

	// doomsday is pinned to index 1; its key is absent, so the sorted-first
	// fallback wins — never the suffix-lookalike.
	assert.Equal(t, "10.4.4.18", findReservedIP(outputs, "doomsday", "trap-ocfp-1", spanning))

	// With the real pinned-index key present, it wins outright.
	outputs["reserved_trap-ocfp-1-ocfp-1_doomsday_ip"] = "10.4.8.18"
	assert.Equal(t, "10.4.8.18", findReservedIP(outputs, "doomsday", "trap-ocfp-1", spanning))
}

// TestFindReservedIP_PinnedResolvesCustomSubnetName verifies the pinned-index
// preference resolves through bootstrap's subnet_<name>_index outputs when the
// bloc's subnets carry operator names: the reserved key on the subnet recorded
// at the pinned index wins, even though no key follows the
// "<bloc>-ocfp-<idx>" shape the legacy construction expects.
func TestFindReservedIP_PinnedResolvesCustomSubnetName(t *testing.T) {
	t.Parallel()

	spanning, err := netlayout.Lookup("spanning")
	require.NoError(t, err)

	outputs := map[string]interface{}{
		"subnet_east-workload-a_index":         "0",
		"subnet_east-workload-b_index":         "1",
		"reserved_east-workload-a_doomsday_ip": "10.4.4.18",
		"reserved_east-workload-b_doomsday_ip": "10.4.8.18",
	}

	// spanning pins doomsday to index 1: the recorded index outputs identify
	// east-workload-b as that subnet.
	assert.Equal(t, "10.4.8.18", findReservedIP(outputs, "doomsday", "myblc", spanning))

	// Two subnets claiming one index is corrupt state; resolution stays
	// deterministic (sorted-first name) rather than map-order dependent.
	outputs["subnet_east-workload-z_index"] = "1"
	outputs["reserved_east-workload-z_doomsday_ip"] = "10.4.12.18"
	assert.Equal(t, "10.4.8.18", findReservedIP(outputs, "doomsday", "myblc", spanning))
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

			assert.Equal(t, tt.want, expectedIPForService(tt.service, tt.reserved, tt.bastionIP, "myblc", nil))
		})
	}
}

// TestOriginForService_ExactMatchOnly covers originForService's
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
			// naming-independence example, unrelated to systemScoped.
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

func boolPtr(b bool) *bool { return &b }

// findFQDNRow locates the one row for a given (env, service) pair in
// collectServiceFQDNSection's output, failing the test immediately if none
// matches — every assertion below needs a specific row, not the whole set.
func findFQDNRow(t *testing.T, rows [][]string, envType, service string) []string {
	t.Helper()

	for _, row := range rows {
		if row[0] == envType && row[1] == service {
			return row
		}
	}

	t.Fatalf("no row found for envType=%s service=%s", envType, service)

	return nil
}

// TestCollectServiceFQDNSection_MgmtAndOCF exercises the full Section 1
// builder against a fixture with a base domain, one exact Cloudflare
// service route, and a full apps/system wildcard configuration, with no
// reserved-IP state.
func TestCollectServiceFQDNSection_MgmtAndOCF(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	base := "ocf.example.lab.internal"
	exactRouteHostname := "api." + base

	cfg := &config.Config{
		Name:  "collect-fqdn-section-fixture-bloc",
		FQDNs: &config.FQDNConfig{Base: base},
		Cloudflare: &config.CloudflareConfig{
			Enabled:      boolPtr(true),
			Origin:       "https://10.64.64.20",
			AppsDomain:   "apps." + base,
			SystemDomain: "system." + base,
			Services: []config.ServiceIngress{
				{Hostname: exactRouteHostname, Service: "https://10.20.30.40"},
			},
		},
	}

	section, resolveKeys := collectServiceFQDNSection(cfg)

	assert.Equal(t, "Derived Service FQDNs", section.Title)
	assert.Equal(t, []string{"ENV", "SERVICE", "FQDN", "EXPECTED IP", "ORIGIN", "RESOLVED IP"}, section.Headers)
	// One row per vault.MgmtServices entry plus one per vault.OCFServices
	// entry, counted from the lists themselves rather than pinned to a literal.
	assert.Len(t, section.Rows, len(vault.MgmtServices)+len(vault.OCFServices))
	assert.NotEmpty(t, resolveKeys)

	apiRow := findFQDNRow(t, section.Rows, vault.OCFEnvType, "api")
	assert.Equal(t, exactRouteHostname, apiRow[2])
	assert.Equal(t, "10.20.30.40", apiRow[4], "exact route wins over cf.Origin")

	concourseOCFRow := findFQDNRow(t, section.Rows, vault.OCFEnvType, "concourse")
	assert.Equal(t, "10.64.64.20", concourseOCFRow[4], "no exact route, falls through to wildcard origin")

	var doomsdayCount, grafanaCount int

	for _, row := range section.Rows {
		switch row[1] {
		case "doomsday":
			doomsdayCount++
			assert.Equal(t, vault.MgmtEnvType, row[0])
		case "grafana":
			grafanaCount++
			assert.Equal(t, vault.MgmtEnvType, row[0])
		}

		if row[0] == vault.MgmtEnvType {
			assert.Equal(t, "—", row[4], "mgmt-tier service %s never gets wildcard fallthrough", row[1])
		}
	}

	assert.Equal(t, 1, doomsdayCount, "doomsday is mgmt-only")
	assert.Equal(t, 1, grafanaCount, "grafana is mgmt-only, alongside the prometheus kit's other FQDN keys")

	grafanaRow := findFQDNRow(t, section.Rows, vault.MgmtEnvType, "grafana")
	assert.Equal(t, "grafana.system."+base, grafanaRow[2], "grafana is system-scoped")

	alertmanagerRow := findFQDNRow(t, section.Rows, vault.MgmtEnvType, "alertmanager")
	assert.Equal(t, "alertmanager."+base, alertmanagerRow[2], "alertmanager is not system-scoped")
}

// TestCollectServiceFQDNSection_GateAffectedServiceOriginPopulated is the
// builder-level counterpart to originForService's gate-affected case: no
// explicit override for concourse, a cf.Services[] entry carrying its
// .system.-infixed derived hostname, and ingress.provider set (the
// tailscale systemScoped signal) rather than cloudflare.enabled — confirming the
// wire-up, not re-testing originForService's own logic.
func TestCollectServiceFQDNSection_GateAffectedServiceOriginPopulated(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	base := "ocf.example.lab.internal"
	concourseHostname := "concourse.system." + base

	cfg := &config.Config{
		Name:    "gate-affected-fixture-bloc",
		FQDNs:   &config.FQDNConfig{Base: base},
		Ingress: &config.IngressConfig{Provider: config.IngressProviderTailscale},
		Cloudflare: &config.CloudflareConfig{
			Services: []config.ServiceIngress{
				{Hostname: concourseHostname, Service: "https://10.0.0.11"},
			},
		},
	}

	section, _ := collectServiceFQDNSection(cfg)

	row := findFQDNRow(t, section.Rows, vault.MgmtEnvType, "concourse")
	assert.Equal(t, concourseHostname, row[2])
	assert.Equal(t, "10.0.0.11", row[4])
}

// TestCollectServiceFQDNSection_NoBaseDomain verifies a bloc with no
// fqdns.base configured still produces the full, structured row set — every
// cell dashed — rather than an empty or partial section.
func TestCollectServiceFQDNSection_NoBaseDomain(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	cfg := &config.Config{Name: "no-base-domain-fixture-bloc"}

	section, resolveKeys := collectServiceFQDNSection(cfg)

	assert.Len(t, section.Rows, len(vault.MgmtServices)+len(vault.OCFServices))
	assert.Empty(t, resolveKeys)

	for _, row := range section.Rows {
		assert.Equal(t, "—", row[2], "FQDN should be dashed")
		assert.Equal(t, "—", row[3], "EXPECTED should be dashed")
		assert.Equal(t, "—", row[4], "ORIGIN should be dashed")
		assert.Equal(t, "—", row[5], "RESOLVED placeholder should be dashed")
	}
}

// TestCollectServiceFQDNSection_BlankFQDNRendersDash verifies a bloc with no
// fqdns.base renders its FQDN cells as an em dash, the same convention the
// EXPECTED IP and ORIGIN cells in the very same row already follow. Without
// it every Section 1 row shows an empty gap in one column and a dash in the
// next two, which reads as a rendering bug rather than as "nothing to show".
func TestCollectServiceFQDNSection_BlankFQDNRendersDash(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	cfg := &config.Config{Name: "no-base-bloc"}

	section, resolveKeys := collectServiceFQDNSection(cfg)

	require.NotEmpty(t, section.Rows)
	assert.Empty(t, resolveKeys, "a blank FQDN is never a resolvable host")

	for _, row := range section.Rows {
		assert.Equal(t, "—", row[2], "service %q", row[1])
	}
}
