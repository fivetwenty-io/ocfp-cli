package commands

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// teardownIngressDNS must be a no-op (nil error) when the resolved ingress
// provider is not tailscale — proving teardown never blocks on ingress DNS
// cleanup for providers that don't own DNS records here.
func TestTeardownIngressDNS_SkipsWhenNotTailscale(t *testing.T) {
	t.Parallel()

	m := &TeardownManager{config: &config.Config{}}
	if err := m.teardownIngressDNS(context.Background()); err != nil {
		t.Fatalf("expected nil for non-tailscale provider, got %v", err)
	}
}
