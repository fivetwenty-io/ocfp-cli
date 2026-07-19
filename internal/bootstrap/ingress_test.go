package bootstrap

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func tailscaleIngressConfig() *config.Config {
	return &config.Config{
		Ingress:    &config.IngressConfig{Provider: config.IngressProviderTailscale},
		Tailscale:  &config.TailscaleConfig{Enabled: boolPtr(true), AuthKey: "k"},
		Cloudflare: &config.CloudflareConfig{Zone: "fivetwenty.io", APIToken: "tok", Origin: "https://10.108.20.13:443"},
	}
}

func TestBastionIngressSpec_TailscaleProvider(t *testing.T) {
	t.Parallel()
	m := &Manager{config: tailscaleIngressConfig(), options: &Options{BlocName: "ocfp-lab-wayne"}}

	spec := m.bastionIngressSpec()
	if spec == nil {
		t.Fatal("expected spec, got nil")
	}
	if spec.OriginIP != "10.108.20.13" {
		t.Errorf("OriginIP = %q", spec.OriginIP)
	}
	if len(spec.Ports) != 2 || spec.Ports[0] != 80 || spec.Ports[1] != 443 {
		t.Errorf("Ports = %v", spec.Ports)
	}
}

func TestBastionIngressSpec_NilForCloudflared(t *testing.T) {
	t.Parallel()
	cfg := tailscaleIngressConfig()
	cfg.Ingress.Provider = config.IngressProviderCloudflared
	cfg.Cloudflare.Enabled = boolPtr(true)
	m := &Manager{config: cfg, options: &Options{BlocName: "b"}}

	if m.bastionIngressSpec() != nil {
		t.Fatal("expected nil for cloudflared provider")
	}
}

func TestOriginHost(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://10.108.20.13:443": "10.108.20.13",
		"10.108.20.13:443":         "10.108.20.13",
		"10.108.20.13":             "10.108.20.13",
		"":                         "",
	}
	for in, want := range cases {
		if got := originHost(in); got != want {
			t.Errorf("originHost(%q) = %q, want %q", in, got, want)
		}
	}
}
