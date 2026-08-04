package vault

import "testing"

// TestOCFServicesIncludesAutoscaler ensures the autoscaler service is
// pre-populated as an OCF FQDN, matching its ocf-only routing default
// in manager.go's resolveKitEnvTypes (kit not in mgmtKits or bothKits).
func TestOCFServicesIncludesAutoscaler(t *testing.T) {
	found := false

	for _, svc := range OCFServices {
		if svc == "autoscaler" {
			found = true

			break
		}
	}

	if !found {
		t.Errorf("OCFServices missing %q; want it present alongside cf/blacksmith/scheduler", "autoscaler")
	}
}

// TestMgmtServicesExcludesAutoscaler ensures autoscaler is NOT in
// MgmtServices, since it deploys only to the ocf environment.
func TestMgmtServicesExcludesAutoscaler(t *testing.T) {
	for _, svc := range MgmtServices {
		if svc == "autoscaler" {
			t.Errorf("MgmtServices unexpectedly contains %q; autoscaler is ocf-only", "autoscaler")
		}
	}
}
