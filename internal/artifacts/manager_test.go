package artifacts

import (
	"net"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

func TestBuildEndpoint_TLSEnabledProducesHTTPS(t *testing.T) {
	t.Parallel()

	cfg := config.ArtifactsConfig{}
	cfg.Defaults()
	cfg.TLS.Mode = config.ArtifactsTLSModeSelfSigned

	ep := buildEndpoint(net.ParseIP("10.0.0.11"), cfg, "ca-pem")

	want := "https://10.0.0.11:9000"
	if ep.URL != want {
		t.Errorf("URL = %q, want %q", ep.URL, want)
	}

	if !ep.PathStyle {
		t.Error("PathStyle should be true for RustFS")
	}

	if ep.CACert != "ca-pem" {
		t.Errorf("CACert = %q, want ca-pem", ep.CACert)
	}

	if ep.Region != config.BlobstoreDefaultRegion {
		t.Errorf("Region = %q, want %q", ep.Region, config.BlobstoreDefaultRegion)
	}
}

func TestBuildEndpoint_TLSDisabledProducesHTTP(t *testing.T) {
	t.Parallel()

	cfg := config.ArtifactsConfig{}
	cfg.Defaults()
	cfg.TLS.Mode = config.ArtifactsTLSModeDisabled

	ep := buildEndpoint(net.ParseIP("10.0.0.11"), cfg, "")

	want := "http://10.0.0.11:9000"
	if ep.URL != want {
		t.Errorf("URL = %q, want %q", ep.URL, want)
	}
}

func TestEndpointFromResource_RoundTrip(t *testing.T) {
	t.Parallel()

	r := &state.Resource{
		Properties: map[string]interface{}{
			"endpoint":   "https://10.0.0.11:9000",
			"private_ip": "10.0.0.11",
			"ca_cert":    "ca-pem-text",
		},
	}

	cfg := config.ArtifactsConfig{}
	cfg.Defaults()

	ep := endpointFromResource(r, cfg)

	if ep.URL != "https://10.0.0.11:9000" {
		t.Errorf("URL = %q", ep.URL)
	}

	if ep.Host != "10.0.0.11" {
		t.Errorf("Host = %q", ep.Host)
	}

	if ep.CACert != "ca-pem-text" {
		t.Errorf("CACert = %q", ep.CACert)
	}
}

func TestCredsFromResource_ExtractsKeys(t *testing.T) {
	t.Parallel()

	r := &state.Resource{
		Properties: map[string]interface{}{
			"access_key": "AK1",
			"secret_key": "SK1",
		},
	}

	c := credsFromResource(r)

	if c.AccessKey != "AK1" || c.SecretKey != "SK1" {
		t.Errorf("credsFromResource = %+v", c)
	}
}
