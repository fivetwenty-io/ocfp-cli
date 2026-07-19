package config

import (
	"errors"
	"testing"
)

func TestResolveIngressProvider_ExplicitWins(t *testing.T) {
	t.Parallel()
	cfg := &Config{Ingress: &IngressConfig{Provider: IngressProviderTailscale}}
	if got := ResolveIngressProvider(cfg); got != IngressProviderTailscale {
		t.Fatalf("expected tailscale, got %q", got)
	}
}

func TestResolveIngressProvider_DefaultsToCloudflaredWhenEnabled(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cloudflare: &CloudflareConfig{Enabled: boolPtr(true)}}
	if got := ResolveIngressProvider(cfg); got != IngressProviderCloudflared {
		t.Fatalf("expected cloudflared, got %q", got)
	}
}

func TestResolveIngressProvider_EmptyWhenNothingEnabled(t *testing.T) {
	t.Parallel()
	if got := ResolveIngressProvider(&Config{}); got != "" {
		t.Fatalf("expected empty provider, got %q", got)
	}
	if got := ResolveIngressProvider(nil); got != "" {
		t.Fatalf("expected empty provider for nil config, got %q", got)
	}
}

func TestValidateIngress_RejectsUnknownProvider(t *testing.T) {
	t.Parallel()
	cfg := &Config{Ingress: &IngressConfig{Provider: "wireguard"}}
	if err := ValidateIngress(cfg); !errors.Is(err, ErrIngressProviderInvalid) {
		t.Fatalf("expected ErrIngressProviderInvalid, got %v", err)
	}
}

func TestValidateIngress_TailscaleRequiresTailscaleEnabled(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Ingress:    &IngressConfig{Provider: IngressProviderTailscale},
		Cloudflare: &CloudflareConfig{Zone: "example.com", APIToken: "tok"},
	}
	if err := ValidateIngress(cfg); !errors.Is(err, ErrIngressTailscaleDisabled) {
		t.Fatalf("expected ErrIngressTailscaleDisabled, got %v", err)
	}
}

func TestValidateIngress_TailscaleRequiresZoneAndToken(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Ingress:   &IngressConfig{Provider: IngressProviderTailscale},
		Tailscale: &TailscaleConfig{Enabled: boolPtr(true), AuthKey: "k"},
	}
	if err := ValidateIngress(cfg); !errors.Is(err, ErrIngressTailscaleDNS) {
		t.Fatalf("expected ErrIngressTailscaleDNS, got %v", err)
	}
}

func TestValidateIngress_TailscaleHappyPath(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Ingress:    &IngressConfig{Provider: IngressProviderTailscale},
		Tailscale:  &TailscaleConfig{Enabled: boolPtr(true), AuthKey: "k"},
		Cloudflare: &CloudflareConfig{Zone: "example.com", APIToken: "tok"},
	}
	if err := ValidateIngress(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateIngress_CloudflaredRequiresEnabled(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Ingress:    &IngressConfig{Provider: IngressProviderCloudflared},
		Cloudflare: &CloudflareConfig{Enabled: boolPtr(false)},
	}
	if err := ValidateIngress(cfg); !errors.Is(err, ErrIngressCloudflaredDisabled) {
		t.Fatalf("expected ErrIngressCloudflaredDisabled, got %v", err)
	}
}

func TestMergeIngressDefaults(t *testing.T) {
	t.Parallel()
	bloc := &Config{}
	mergeIngressDefaults(bloc, &IngressConfig{Provider: IngressProviderTailscale})
	if bloc.Ingress == nil || bloc.Ingress.Provider != IngressProviderTailscale {
		t.Fatalf("expected defaults copied, got %+v", bloc.Ingress)
	}

	bloc2 := &Config{Ingress: &IngressConfig{Provider: IngressProviderCloudflared}}
	mergeIngressDefaults(bloc2, &IngressConfig{Provider: IngressProviderTailscale})
	if bloc2.Ingress.Provider != IngressProviderCloudflared {
		t.Fatalf("bloc value must win, got %q", bloc2.Ingress.Provider)
	}
}
