package commands

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/stretchr/testify/assert"
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
