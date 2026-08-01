package config_test

import (
	"errors"
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
// value fails at UnmarshalYAML time with an error wrapping
// netlayout.ErrUnknownStrategy, so operators get an actionable error instead
// of an unresolved strategy leaking silently into bootstrap.
func TestNetworkStrategyUnmarshalUnknown(t *testing.T) {
	t.Parallel()

	var nc config.NetworkConfig

	err := yaml.Unmarshal([]byte("strategy: bogus-strategy"), &nc)
	if err == nil {
		t.Fatal("expected error for unknown strategy, got nil")
	}

	if !errors.Is(err, netlayout.ErrUnknownStrategy) {
		t.Errorf("error = %v, want wrapping netlayout.ErrUnknownStrategy", err)
	}
}

// TestNetworkStrategyUnmarshalUnknownAlias proves the snake_case alias is
// validated identically to the camelCase key.
func TestNetworkStrategyUnmarshalUnknownAlias(t *testing.T) {
	t.Parallel()

	var nc config.NetworkConfig

	err := yaml.Unmarshal([]byte("network_strategy: bogus-strategy"), &nc)
	if err == nil {
		t.Fatal("expected error for unknown strategy, got nil")
	}

	if !errors.Is(err, netlayout.ErrUnknownStrategy) {
		t.Errorf("error = %v, want wrapping netlayout.ErrUnknownStrategy", err)
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
// entirely leaves every band offset at zero, i.e. "no override" — the same
// convention used by AvailableBandStart/AvailableBandEnd.
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
