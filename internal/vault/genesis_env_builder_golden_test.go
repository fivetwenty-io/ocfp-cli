package vault

// Golden-file tests for WriteEnvFileV32.
//
// Contract reference: plan/ocfp-aws-update-plan.md §"Env File Generator — New v3.2 Schema"
// and §"Sample Output — AWS Minimal BOSH Director Env".
//
// Each test calls marshalGenesisEnvV32 with a fixed input struct and compares
// the result byte-for-byte to a fixture file in testdata/envs/. Any change to
// the struct field YAML tags or the marshal ordering will cause a mismatch,
// catching schema drift early.
//
// To regenerate fixtures after an intentional schema change:
//  1. Delete the stale fixture file(s) under testdata/envs/.
//  2. Run the test with -update flag (see updateGolden helper below).
//  3. Review the diff and commit the new fixtures alongside the code change.

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// updateGolden, when true, overwrites fixture files instead of comparing them.
// Enable with: go test ./internal/vault/... -update
var updateGolden = flag.Bool("update", false, "update golden fixture files instead of comparing")

// goldenFixturesDir is relative to the package directory.
const goldenFixturesDir = "testdata/envs"

// assertGolden compares got to the fixture file at testdata/envs/<name>.
// When -update is passed the fixture is overwritten and the test passes.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join(goldenFixturesDir, name)

	if *updateGolden {
		if err := os.MkdirAll(goldenFixturesDir, 0755); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0600); err != nil {
			t.Fatalf("update fixture %s: %v", path, err)
		}
		t.Logf("updated fixture %s", path)
		return
	}

	want, err := os.ReadFile(path)
	require.NoErrorf(t, err, "read fixture %s — regenerate with -update", path)

	if !bytes.Equal(want, got) {
		t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s",
			name, want, got)
	}
}

// TestWriteEnvFileV32_Golden_MgmtMinimal verifies the byte-for-byte output of
// marshalGenesisEnvV32 for an AWS BOSH director (create-env, mgmt) env with
// no optional fields — the minimal valid configuration.
//
// Contract: genesis.env must equal "<bloc>-mgmt"; use_create_env and
// min_version must be present; ocfp.bloc must be set; no params or
// bosh-configs blocks emitted when absent.
//
// Fixture: testdata/envs/mgmt.yml
func TestWriteEnvFileV32_Golden_MgmtMinimal(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{
			Env:          "ocfp-aws-us-east-1-mgmt",
			UseCreateEnv: true,
			MinVersion:   "3.2.0",
		},
		Kit: KitBlockV32{
			Name:    "bosh",
			Version: "latest",
			IAAS:    "aws",
		},
		OCFP: &OCFPBlock{Bloc: "ocfp-aws-us-east-1"},
	}

	got, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)

	assertGolden(t, "mgmt.yml", got)
}

// TestWriteEnvFileV32_Golden_OCFEnv verifies the byte-for-byte output for an
// AWS OCF (CF, non-create-env) env — the typical application-tier deployment.
//
// Contract: use_create_env must be absent; params block must be present when
// non-empty; ocfp.bloc must be set; iaas on kit is still set for AWS.
//
// Fixture: testdata/envs/ocf.yml
func TestWriteEnvFileV32_Golden_OCFEnv(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{
			Env:        "ocfp-aws-us-east-1-ocf",
			MinVersion: "3.2.0",
		},
		Kit: KitBlockV32{
			Name:    "cf",
			Version: "latest",
			IAAS:    "aws",
		},
		OCFP: &OCFPBlock{Bloc: "ocfp-aws-us-east-1"},
		Params: map[string]any{
			"aws_region": "us-east-1",
		},
	}

	got, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)

	assertGolden(t, "ocf.yml", got)
}

// TestWriteEnvFileV32_Golden_MgmtWithFeatures verifies the byte-for-byte
// output for a BOSH director env that carries a kit.features list.
//
// Contract: features must be a YAML sequence under kit:; key order must be
// name, version, iaas, features; ocfp block present; use_create_env present.
//
// Fixture: testdata/envs/mgmt-with-features.yml
func TestWriteEnvFileV32_Golden_MgmtWithFeatures(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{
			Env:          "ocfp-aws-us-east-1-mgmt",
			UseCreateEnv: true,
			MinVersion:   "3.2.0",
		},
		Kit: KitBlockV32{
			Name:     "bosh",
			Version:  "latest",
			IAAS:     "aws",
			Features: []string{"ocfp", "vault"},
		},
		OCFP: &OCFPBlock{Bloc: "ocfp-aws-us-east-1"},
	}

	got, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)

	assertGolden(t, "mgmt-with-features.yml", got)
}

// TestWriteEnvFileV32_Golden_PVEMgmt verifies the byte-for-byte output of
// marshalGenesisEnvV32 for a PVE BOSH director (create-env, mgmt) env with
// no optional fields — the minimal valid PVE configuration.
//
// Contract: genesis.env must equal "ocfp-pve-dc1-mgmt"; use_create_env and
// min_version must be present; kit.iaas must be "pve"; ocfp.bloc must be
// "ocfp-pve-dc1"; no params or bosh-configs blocks emitted when absent.
//
// Fixture: testdata/envs/pve-mgmt.yml
func TestWriteEnvFileV32_Golden_PVEMgmt(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{
			Env:          "ocfp-pve-dc1-mgmt",
			UseCreateEnv: true,
			MinVersion:   "3.2.0",
		},
		Kit: KitBlockV32{
			Name:    "bosh",
			Version: "latest",
			IAAS:    "pve",
		},
		OCFP: &OCFPBlock{Bloc: "ocfp-pve-dc1"},
	}

	got, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)

	assertGolden(t, "pve-mgmt.yml", got)
}

// TestWriteEnvFileV32_Golden_PVEOCFEnv verifies the byte-for-byte output for a
// PVE OCF (CF, non-create-env) env — the application-tier deployment on PVE.
//
// Contract: use_create_env must be absent; kit.iaas must be "pve";
// params.pve_datacenter must equal "dc1" (derived from the bloc's third segment);
// ocfp.bloc must be "ocfp-pve-dc1".
//
// Fixture: testdata/envs/pve-ocf.yml
func TestWriteEnvFileV32_Golden_PVEOCFEnv(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{
			Env:        "ocfp-pve-dc1-ocf",
			MinVersion: "3.2.0",
		},
		Kit: KitBlockV32{
			Name:    "cf",
			Version: "latest",
			IAAS:    "pve",
		},
		OCFP: &OCFPBlock{Bloc: "ocfp-pve-dc1"},
		Params: map[string]any{
			"pve_datacenter": "dc1",
		},
	}

	got, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)

	assertGolden(t, "pve-ocf.yml", got)
}

// TestWriteEnvFileV32_Golden_MgmtNoBloc verifies that when OCFPBlock.Bloc is
// empty the ocfp: top-level key is entirely absent from the output.
//
// Contract: marshalGenesisEnvV32 omits the ocfp block when Bloc == "".
// The fixture must contain no "ocfp:" line at all.
//
// Fixture: testdata/envs/mgmt-no-bloc.yml
func TestWriteEnvFileV32_Golden_MgmtNoBloc(t *testing.T) {
	env := GenesisEnvV32{
		Genesis: GenesisBlockV32{
			Env:          "ocfp-aws-us-east-1-mgmt",
			UseCreateEnv: true,
			MinVersion:   "3.2.0",
		},
		Kit: KitBlockV32{
			Name:    "bosh",
			Version: "latest",
			IAAS:    "aws",
		},
		OCFP: &OCFPBlock{Bloc: ""},
	}

	got, err := marshalGenesisEnvV32(env)
	require.NoError(t, err)

	// Structural assertion: ocfp block must not appear.
	require.NotContains(t, string(got), "ocfp:")

	assertGolden(t, "mgmt-no-bloc.yml", got)
}
