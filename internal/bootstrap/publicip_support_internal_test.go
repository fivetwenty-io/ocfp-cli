package bootstrap

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// declaringNet reports public IP support explicitly, the way a provider whose
// public IP methods are unimplemented stubs must.
type declaringNet struct {
	cpi.NetworkManager

	supports bool
}

func (d declaringNet) SupportsPublicIPs() bool { return d.supports }

// supportsPublicIPs backs both the bootstrap step and the plan preview, so a
// provider that declares no support must be filtered out at this one predicate.
func TestSupportsPublicIPsHonorsDeclaredCapability(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		netMgr   cpi.NetworkManager
		expected bool
	}{
		{name: "no network manager", netMgr: nil, expected: false},
		{name: "declares no support", netMgr: declaringNet{supports: false}, expected: false},
		{name: "declares support", netMgr: declaringNet{supports: true}, expected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			manager := &Manager{provider: &capabilityProv{n: tc.netMgr}}

			if got := manager.supportsPublicIPs(); got != tc.expected {
				t.Errorf("supportsPublicIPs() = %v, want %v", got, tc.expected)
			}
		})
	}
}

// A manager whose network manager is silent on the question keeps the previous
// behaviour: support is assumed.
func TestSupportsPublicIPsDefaultsToSupportedWhenUndeclared(t *testing.T) {
	t.Parallel()

	manager := &Manager{provider: &capabilityProv{n: silentNet{}}}

	if !manager.supportsPublicIPs() {
		t.Error("a network manager that does not declare a capability should be treated as supporting public IPs")
	}
}

type silentNet struct{ cpi.NetworkManager }

// capabilityProv exposes only the NetworkManager the predicate consults.
type capabilityProv struct {
	cpi.Provider

	n cpi.NetworkManager
}

func (p *capabilityProv) NetworkManager() cpi.NetworkManager { return p.n }
