package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
)

// TestNetworkStrategyUnmarshalAliases proves network.strategy parses from
// both the documented camelCase/lowercase "strategy" key and the
// snake_case "network_strategy" alias, matching every other aliased
// NetworkConfig field's precedence convention (camelCase wins when both are
// set).
func TestNetworkStrategyUnmarshalAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "strategy key",
			yaml: "strategy: wide",
			want: "wide",
		},
		{
			name: "network_strategy snake_case alias",
			yaml: "network_strategy: compact",
			want: "compact",
		},
		{
			name: "strategy wins over network_strategy",
			yaml: "strategy: wide\nnetwork_strategy: compact",
			want: "wide",
		},
		{
			name: "unset leaves empty string",
			yaml: "cidr: 10.0.0.0/16",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var nc config.NetworkConfig

			err := yaml.Unmarshal([]byte(tt.yaml), &nc)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if nc.Strategy != tt.want {
				t.Errorf("Strategy = %q, want %q", nc.Strategy, tt.want)
			}
		})
	}
}

// TestNetworkStrategyUnmarshalUnknown proves an unrecognized network.strategy
// value no longer fails at UnmarshalYAML time — BYO strategy names are
// unknown to bare UnmarshalYAML (they only become known once
// network.strategyPaths is loaded into a catalog), so name validation moves
// to LoadWithParams, where the bloc's catalog is built and
// ResolveReservedIPLayout is exercised eagerly. See
// TestNetworkStrategyLoadWithParamsUnknown below for that failure mode.
func TestNetworkStrategyUnmarshalUnknown(t *testing.T) {
	t.Parallel()

	var nc config.NetworkConfig

	err := yaml.Unmarshal([]byte("strategy: bogus-strategy"), &nc)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if nc.Strategy != "bogus-strategy" {
		t.Errorf("Strategy = %q, want %q (validation deferred to LoadWithParams)", nc.Strategy, "bogus-strategy")
	}
}

// TestNetworkStrategyUnmarshalUnknownAlias proves the snake_case alias
// parses identically to the camelCase key, with the same deferred
// validation as TestNetworkStrategyUnmarshalUnknown.
func TestNetworkStrategyUnmarshalUnknownAlias(t *testing.T) {
	t.Parallel()

	var nc config.NetworkConfig

	err := yaml.Unmarshal([]byte("network_strategy: bogus-strategy"), &nc)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if nc.Strategy != "bogus-strategy" {
		t.Errorf("Strategy = %q, want %q (validation deferred to LoadWithParams)", nc.Strategy, "bogus-strategy")
	}
}

// TestNetworkStrategyLoadWithParamsUnknown proves an unrecognized
// network.strategy now fails at LoadWithParams time — the point at which
// the bloc's catalog (built-ins plus any network.strategyPaths definitions)
// exists and ResolveReservedIPLayout can actually be attempted — with an
// error wrapping netlayout.ErrUnknownStrategy, so operators still get an
// actionable error instead of an unresolved strategy leaking silently into
// bootstrap.
func TestNetworkStrategyLoadWithParamsUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	yml := []byte("" +
		"blocs:\n" +
		"  test:\n" +
		"    name: test\n" +
		"    provider: stackit\n" +
		"    network:\n" +
		"      strategy: bogus-strategy\n")

	if err := os.WriteFile(cfgPath, yml, 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_, err := config.LoadWithParams(cfgPath, "test")
	if err == nil {
		t.Fatal("LoadWithParams: want error for unknown network.strategy, got nil")
	}

	if !errors.Is(err, netlayout.ErrUnknownStrategy) {
		t.Errorf("LoadWithParams error = %v, want wrapping netlayout.ErrUnknownStrategy", err)
	}
}

// TestNetworkStrategyLoadWithParamsBYORoundTrip proves a bloc can select a
// BYO strategy loaded from network.strategyPaths: LoadWithParams succeeds,
// and the bloc's ResolveReservedIPLayout resolves to the BYO definition's
// own Layout (not a built-in), by name.
func TestNetworkStrategyLoadWithParamsBYORoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	strategyPath := filepath.Join(dir, "my-strategy.yaml")
	strategyYAML := []byte("" +
		"name: my-strategy\n" +
		"description: operator BYO strategy\n" +
		"scheme_version: \"byo-round-trip-v1\"\n" +
		"placement: colocated\n" +
		"min_prefix: 26\n" +
		"\n" +
		"tiers:\n" +
		"  mgmt:\n" +
		"    statics:\n" +
		"      bosh: 4\n" +
		"    available:\n" +
		"      - start: 10\n" +
		"        end: 20\n")

	if err := os.WriteFile(strategyPath, strategyYAML, 0o600); err != nil {
		t.Fatalf("write strategy file: %v", err)
	}

	cfgPath := filepath.Join(dir, "config.yml")
	yml := []byte("" +
		"blocs:\n" +
		"  test:\n" +
		"    name: test\n" +
		"    provider: stackit\n" +
		"    network:\n" +
		"      strategy: my-strategy\n" +
		"      strategyPaths:\n" +
		"        - " + strategyPath + "\n")

	if err := os.WriteFile(cfgPath, yml, 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := config.LoadWithParams(cfgPath, "test")
	if err != nil {
		t.Fatalf("LoadWithParams: unexpected error: %v", err)
	}

	layout, err := cfg.ResolveReservedIPLayout()
	if err != nil {
		t.Fatalf("ResolveReservedIPLayout: unexpected error: %v", err)
	}

	if got, want := layout.Name(), "my-strategy"; got != want {
		t.Errorf("ResolveReservedIPLayout().Name() = %q, want %q", got, want)
	}
}

// TestNetworkBandsUnmarshal proves network.bands.infra and
// network.bands.mgmt parse their start/end offsets, and that an absent
// bands block leaves every offset at its zero value (no override).
func TestNetworkBandsUnmarshal(t *testing.T) {
	t.Parallel()

	const input = `
strategy: wide
bands:
  infra:
    start: 12
    end: 99
  mgmt:
    start: 100
    end: 199
`

	var nc config.NetworkConfig

	err := yaml.Unmarshal([]byte(input), &nc)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if nc.Bands.Infra.Start != 12 || nc.Bands.Infra.End != 99 {
		t.Errorf("Bands.Infra = %+v, want {Start:12 End:99}", nc.Bands.Infra)
	}

	if nc.Bands.Mgmt.Start != 100 || nc.Bands.Mgmt.End != 199 {
		t.Errorf("Bands.Mgmt = %+v, want {Start:100 End:199}", nc.Bands.Mgmt)
	}
}

// TestNetworkBandsUnmarshalAbsent proves that omitting network.bands
// entirely leaves every band offset at zero, i.e. "no override".
func TestNetworkBandsUnmarshalAbsent(t *testing.T) {
	t.Parallel()

	var nc config.NetworkConfig

	err := yaml.Unmarshal([]byte("cidr: 10.0.0.0/16"), &nc)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := config.NetworkBands{}
	if nc.Bands != want {
		t.Errorf("Bands = %+v, want zero value %+v", nc.Bands, want)
	}
}

// TestNetworkBandsUnmarshalPartial proves setting only one tier's band
// leaves the other tier's offsets at zero rather than defaulting them from
// the configured tier.
func TestNetworkBandsUnmarshalPartial(t *testing.T) {
	t.Parallel()

	const input = `
bands:
  infra:
    start: 12
    end: 99
`

	var nc config.NetworkConfig

	err := yaml.Unmarshal([]byte(input), &nc)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if nc.Bands.Infra.Start != 12 || nc.Bands.Infra.End != 99 {
		t.Errorf("Bands.Infra = %+v, want {Start:12 End:99}", nc.Bands.Infra)
	}

	want := config.Band{}
	if nc.Bands.Mgmt != want {
		t.Errorf("Bands.Mgmt = %+v, want zero value %+v", nc.Bands.Mgmt, want)
	}
}
