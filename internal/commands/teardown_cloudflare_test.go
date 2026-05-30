package commands

import "testing"

// teardownCloudflare must be a no-op (nil error) when cloudflare is disabled
// or no tunnel id is stored — proving teardown never blocks on a missing tunnel.
func TestTeardownCloudflare_NoopWhenDisabled(t *testing.T) {
	m := &TeardownManager{options: &TeardownOptions{BlocName: "ocfp-lab-test"}}
	if err := m.teardownCloudflare(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}
