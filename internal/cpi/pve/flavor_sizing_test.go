package pve

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// A freshly cloned PVE VM reports its config with numeric fields that may
// arrive as int, float64 (JSON), or string depending on the API path, and a
// value can be transiently absent while the clone finalizes. flavorSizingSatisfied
// must treat all numeric encodings equally and report "not satisfied" (so the
// caller retries) when a field is missing or still at the template default.
func TestFlavorSizingSatisfied(t *testing.T) {
	flavor := &cpi.Flavor{VCPUs: 2, RAM: 8192}

	cases := map[string]struct {
		config map[string]interface{}
		want   bool
	}{
		"int values match":           {map[string]interface{}{"memory": 8192, "cores": 2}, true},
		"float64 values match":       {map[string]interface{}{"memory": float64(8192), "cores": float64(2)}, true},
		"string values match":        {map[string]interface{}{"memory": "8192", "cores": "2"}, true},
		"memory absent (clone race)": {map[string]interface{}{"cores": 2}, false},
		"memory still template 2048": {map[string]interface{}{"memory": 2048, "cores": 2}, false},
		"cores mismatch":             {map[string]interface{}{"memory": 8192, "cores": 1}, false},
		"empty config":               {map[string]interface{}{}, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := flavorSizingSatisfied(tc.config, flavor); got != tc.want {
				t.Errorf("flavorSizingSatisfied(%v) = %v, want %v", tc.config, got, tc.want)
			}
		})
	}
}

// A nil flavor means "no sizing requested" — always satisfied so the caller
// is a no-op.
func TestFlavorSizingSatisfiedNilFlavor(t *testing.T) {
	if !flavorSizingSatisfied(map[string]interface{}{}, nil) {
		t.Error("flavorSizingSatisfied with nil flavor should be true (no-op)")
	}
}

// PVE config reads can return numeric fields as strings; getIntFromMap must
// parse them rather than fall through to 0 (which previously made cloned VMs
// report custom-Nc-0m and broke sizing verification).
func TestGetIntFromMapString(t *testing.T) {
	cases := map[string]struct {
		value interface{}
		want  int
	}{
		"string int":      {"8192", 8192},
		"plain int":       {2, 2},
		"float64":         {float64(4096), 4096},
		"int64":           {int64(16384), 16384},
		"non-numeric str": {"host", 0},
		"absent":          {nil, 0},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := map[string]interface{}{}
			if tc.value != nil {
				m["k"] = tc.value
			}

			if got := getIntFromMap(m, "k"); got != tc.want {
				t.Errorf("getIntFromMap(%v) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}
