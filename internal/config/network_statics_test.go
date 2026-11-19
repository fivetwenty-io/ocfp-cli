package config_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/netlayout"
	"github.com/fivetwenty-io/ocfp-cli/internal/reservedip"
	"github.com/goccy/go-yaml"
)

// TestNetworkStaticsUnmarshal proves network.statics.mgmt.controlPlane
// parses as a list of offsets from the documented camelCase key or the
// snake_case control_plane alias, and that leaving both keys unset means no
// override.
func TestNetworkStaticsUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []int
	}{
		{name: "camelCase", input: "statics:\n  mgmt:\n    controlPlane: [30]\n", want: []int{30}},
		{name: "snake_case alias", input: "statics:\n  mgmt:\n    control_plane: [31]\n", want: []int{31}},
		{name: "block list", input: "statics:\n  mgmt:\n    controlPlane:\n      - 80\n", want: []int{80}},
		{name: "absent", input: "cidr: 10.0.0.0/16\n", want: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var nc config.NetworkConfig
			if err := yaml.Unmarshal([]byte(tc.input), &nc); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			got := nc.Statics.Mgmt.ControlPlane
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Statics.Mgmt.ControlPlane = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNetworkStaticsUnmarshalRejectsScalar proves a bare offset is refused
// with an error that names the key and says a list is expected, so the
// later move to several control-plane addresses never needs a config-shape
// migration.
func TestNetworkStaticsUnmarshalRejectsScalar(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"statics:\n  mgmt:\n    controlPlane: 30\n",
		"statics:\n  mgmt:\n    control_plane: 30\n",
	} {
		var nc config.NetworkConfig

		err := yaml.Unmarshal([]byte(input), &nc)
		if err == nil {
			t.Fatalf("unmarshal(%q) = nil error, want a list-shape error", input)
		}

		if !strings.Contains(err.Error(), "network.statics.mgmt.controlPlane") || !strings.Contains(err.Error(), "list") {
			t.Fatalf("unmarshal(%q) error = %q, want it to name the key and ask for a list", input, err)
		}
	}
}

// TestNetworkStaticsUnmarshalRejectsBothKeys proves setting both the
// controlPlane key and its control_plane alias is an error whatever the
// values, so an empty or agreeing value under one key can never hide or
// shadow the other.
func TestNetworkStaticsUnmarshalRejectsBothKeys(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"statics:\n  mgmt:\n    controlPlane: []\n    control_plane: [80]\n",
		"statics:\n  mgmt:\n    controlPlane: [30]\n    control_plane: [31]\n",
		"statics:\n  mgmt:\n    controlPlane: [30]\n    control_plane: [30]\n",
		"statics:\n  mgmt:\n    controlPlane:\n    control_plane: [80]\n",
	} {
		var nc config.NetworkConfig

		err := yaml.Unmarshal([]byte(input), &nc)
		if !errors.Is(err, config.ErrStaticOverrideBothKeys) {
			t.Fatalf("unmarshal(%q) error = %v, want wrapping ErrStaticOverrideBothKeys", input, err)
		}

		if !strings.Contains(err.Error(), "network.statics.mgmt.controlPlane") {
			t.Fatalf("unmarshal(%q) error %q does not name the key", input, err)
		}
	}
}

// TestNetworkStaticsUnmarshalRejectsEmptyList proves an empty or null list
// under either key is an error saying exactly one entry is required, so an
// empty list can never mean "no override".
func TestNetworkStaticsUnmarshalRejectsEmptyList(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"statics:\n  mgmt:\n    controlPlane: []\n",
		"statics:\n  mgmt:\n    control_plane: []\n",
		"statics:\n  mgmt:\n    controlPlane:\n",
	} {
		var nc config.NetworkConfig

		err := yaml.Unmarshal([]byte(input), &nc)
		if !errors.Is(err, netlayout.ErrStaticOverrideCount) {
			t.Fatalf("unmarshal(%q) error = %v, want wrapping ErrStaticOverrideCount", input, err)
		}

		if !strings.Contains(err.Error(), "network.statics.mgmt.controlPlane") ||
			!strings.Contains(err.Error(), "exactly one entry is required") {
			t.Fatalf("unmarshal(%q) error %q, want it to name the key and require exactly one entry", input, err)
		}
	}
}

// TestApplyStaticOverrides proves the shared helper every reserved-IP
// writer calls returns the layout unchanged without an override, moves the
// control-plane static with one, checks the override against the effective
// mgmt band, and names the config key in every error.
func TestApplyStaticOverrides(t *testing.T) {
	t.Parallel()

	wide, err := netlayout.Lookup("wide")
	if err != nil {
		t.Fatalf("Lookup(wide): %v", err)
	}

	const cidr = "10.64.64.0/22"

	t.Run("no override", func(t *testing.T) {
		t.Parallel()

		var nc config.NetworkConfig

		got, err := nc.ApplyStaticOverrides(wide, cidr, 0)
		if err != nil {
			t.Fatalf("ApplyStaticOverrides: %v", err)
		}

		if got != wide {
			t.Fatal("ApplyStaticOverrides without an override returned a different layout")
		}
	})

	t.Run("override", func(t *testing.T) {
		t.Parallel()

		nc := config.NetworkConfig{Statics: config.NetworkStatics{Mgmt: config.MgmtStatics{ControlPlane: []int{30}}}}

		got, err := nc.ApplyStaticOverrides(wide, cidr, 0)
		if err != nil {
			t.Fatalf("ApplyStaticOverrides: %v", err)
		}

		table, err := got.WorkloadTable(cidr)
		if err != nil {
			t.Fatalf("WorkloadTable: %v", err)
		}

		want := &reservedip.Assignment{Offset: 30}
		if a := table[netlayout.ControlPlaneRole]["mgmt"]; !reflect.DeepEqual(a, want) {
			t.Fatalf("control_plane/mgmt = %+v, want %+v", a, want)
		}
	})

	t.Run("effective band", func(t *testing.T) {
		t.Parallel()

		nc := config.NetworkConfig{
			Bands:   config.NetworkBands{Mgmt: config.Band{Start: 41, End: 63}},
			Statics: config.NetworkStatics{Mgmt: config.MgmtStatics{ControlPlane: []int{40}}},
		}

		if _, err := nc.ApplyStaticOverrides(wide, cidr, 0); err != nil {
			t.Fatalf("offset 40 beside band 41-63: %v", err)
		}

		nc.Statics.Mgmt.ControlPlane = []int{50}

		_, err := nc.ApplyStaticOverrides(wide, cidr, 0)
		if !errors.Is(err, netlayout.ErrStaticOverrideInBand) {
			t.Fatalf("offset 50 inside band 41-63: error = %v, want ErrStaticOverrideInBand", err)
		}

		if !strings.Contains(err.Error(), "network.statics.mgmt.controlPlane") {
			t.Fatalf("error %q does not name the config key", err)
		}
	})
}
