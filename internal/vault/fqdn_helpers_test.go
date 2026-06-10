package vault

import "testing"

func TestDeriveFQDN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		service      string
		base         string
		systemScoped bool
		want         string
	}{
		{"flat infra UI when not scoped", "concourse", "ocf.example.io", false, "concourse.ocf.example.io"},
		{"scoped infra UI gains system segment", "concourse", "ocf.example.io", true, "concourse.system.ocf.example.io"},
		{"scoped shield", "shield", "ocf.example.io", true, "shield.system.ocf.example.io"},
		{"non-infra service never scoped", "bosh", "ocf.example.io", true, "bosh.ocf.example.io"},
		{"non-infra service flat", "vault", "ocf.example.io", false, "vault.ocf.example.io"},
		{"system service itself not double-scoped", "system", "ocf.example.io", true, "system.ocf.example.io"},
		{"empty base yields empty", "concourse", "", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := DeriveFQDN(tt.service, tt.base, tt.systemScoped); got != tt.want {
				t.Errorf("DeriveFQDN(%q, %q, %v) = %q, want %q", tt.service, tt.base, tt.systemScoped, got, tt.want)
			}
		})
	}
}

func TestIsSystemScopedService(t *testing.T) {
	t.Parallel()

	for _, svc := range []string{"concourse", "shield", "prometheus", "blacksmith", "doomsday"} {
		if !IsSystemScopedService(svc) {
			t.Errorf("IsSystemScopedService(%q) = false, want true", svc)
		}
	}

	for _, svc := range []string{"bosh", "vault", "system", "apps", "api", "uaa"} {
		if IsSystemScopedService(svc) {
			t.Errorf("IsSystemScopedService(%q) = true, want false", svc)
		}
	}
}

func TestGetFQDNExplicitOverrideWinsOverSystemScope(t *testing.T) {
	t.Parallel()

	explicit := map[string]string{"concourse": "ci.custom.example.io"}

	got := GetFQDN("concourse", explicit, "ocf.example.io", true)
	if want := "ci.custom.example.io"; got != want {
		t.Errorf("GetFQDN explicit override = %q, want %q", got, want)
	}

	// No explicit entry: derive with system scope.
	got = GetFQDN("concourse", explicit, "ocf.example.io", true)
	_ = got

	derived := GetFQDN("shield", explicit, "ocf.example.io", true)
	if want := "shield.system.ocf.example.io"; derived != want {
		t.Errorf("GetFQDN derived scoped = %q, want %q", derived, want)
	}
}

func TestPopulateFQDNsForEnvSystemScoped(t *testing.T) {
	t.Parallel()

	fqdns := PopulateFQDNsForEnv(MgmtEnvType, nil, "ocf.example.io", true)

	if got, want := fqdns["concourse"], "concourse.system.ocf.example.io"; got != want {
		t.Errorf("concourse fqdn = %v, want %q", got, want)
	}
	if got, want := fqdns["prometheus"], "prometheus.system.ocf.example.io"; got != want {
		t.Errorf("prometheus fqdn = %v, want %q", got, want)
	}
	// Non-infra mgmt service stays flat.
	if got, want := fqdns["bosh"], "bosh.ocf.example.io"; got != want {
		t.Errorf("bosh fqdn = %v, want %q", got, want)
	}
}

func TestPopulateFQDNsForEnvFlatWhenNotScoped(t *testing.T) {
	t.Parallel()

	fqdns := PopulateFQDNsForEnv(MgmtEnvType, nil, "ocf.example.io", false)

	if got, want := fqdns["concourse"], "concourse.ocf.example.io"; got != want {
		t.Errorf("concourse fqdn (unscoped) = %v, want %q", got, want)
	}
}
