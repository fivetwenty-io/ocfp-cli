package bootstrap_test

import (
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// TestSelectVirtualSubnetStrategy_Mapping verifies the factory maps
// provider/params.subnet_strategy to the right strategy: PVE always uses the
// pve strategy; otherwise "single" -> stackit-single and anything else
// (including the canonical "ocfp-triple" and empty) -> stackit-triple.
func TestSelectVirtualSubnetStrategy_Mapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		provider string
		strategy string
		want     string
	}{
		{"pve_ignores_strategy", "pve", "ocfp-triple", "pve"},
		{"pve_single_still_pve", "pve", "single", "pve"},
		{"stackit_default_triple", "stackit", "", "stackit-triple"},
		{"stackit_triple", "stackit", "ocfp-triple", "stackit-triple"},
		{"stackit_single", "stackit", "single", "stackit-single"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tmp := t.TempDir()
			sm, err := state.NewManager(filepath.Join(tmp, ".state"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = sm.Load("prod"); err != nil {
				t.Fatal(err)
			}

			cfg := createTestConfig()
			cfg.Network.SubnetStrategy = tc.strategy

			manager := bootstrap.NewManager(cfg, &fakeProv{n: &fakeNet{}, c: &fakeCompute{}}, sm, &bootstrap.Options{
				BlocName: "prod",
				Provider: tc.provider,
				Region:   tc.provider,
			})

			if got := manager.SelectVirtualSubnetStrategyName(); got != tc.want {
				t.Errorf("strategy for provider=%q strategy=%q = %q, want %q",
					tc.provider, tc.strategy, got, tc.want)
			}
		})
	}
}
