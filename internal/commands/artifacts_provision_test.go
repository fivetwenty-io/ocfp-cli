package commands

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func testArtifactsCfg() *config.Config {
	cfg := &config.Config{Name: "ocfp-lab-wayne"}
	cfg.Artifacts.Enabled = true
	cfg.Artifacts.Rustfs.Version = "1.0.0-beta.3"
	cfg.Artifacts.Rustfs.S3Port = 9000
	cfg.Artifacts.Rustfs.ConsolePort = 9001
	cfg.Artifacts.Data.Mountpoint = "/data"

	return cfg
}

func TestArtifactsProvisionBuckets_FollowsNamingConvention(t *testing.T) {
	t.Parallel()

	got := artifactsProvisionBuckets("ocfp-lab-wayne")
	want := []string{
		"ocfp-lab-wayne-mgmt-bosh",
		"ocfp-lab-wayne-ocf-cf-droplets",
		"ocfp-lab-wayne-ocf-cf-packages",
		"ocfp-lab-wayne-ocf-cf-buildpacks",
		"ocfp-lab-wayne-ocf-cf-resource-pool",
	}

	if len(got) != len(want) {
		t.Fatalf("bucket count = %d, want %d", len(got), len(want))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bucket[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildArtifactsProvisionEnv_TLSEnabled(t *testing.T) {
	t.Parallel()

	cfg := testArtifactsCfg()
	lr := &artifacts.LookupResult{
		AccessKey:  "AKIAEXAMPLE",
		SecretKey:  "secret/value",
		ZFSDataset: "rpool/ocfp-lab-wayne",
		TLSMode:    config.ArtifactsTLSModeInternalCA,
		PrivateIP:  "10.64.64.11",
	}

	env := buildArtifactsProvisionEnv(cfg, lr, "CERTPEM", "KEYPEM")

	checks := map[string]string{
		"RUSTFS_ACCESS_KEY":   "AKIAEXAMPLE",
		"RUSTFS_SECRET_KEY":   "secret/value",
		"RUSTFS_S3_PORT":      "9000",
		"RUSTFS_CONSOLE_PORT": "9001",
		"RUSTFS_VOLUMES":      "/data",
		"RUSTFS_ZFS_DATASET":  "rpool/ocfp-lab-wayne",
		"RUSTFS_TLS_ENABLED":  "true",
		"RUSTFS_TLS_CERT":     "CERTPEM",
		"RUSTFS_TLS_KEY":      "KEYPEM",

		// filesystem is unset in testArtifactsCfg, so the ext4 default applies
		"ARTIFACTS_DATA_FILESYSTEM": "ext4",
	}

	for k, want := range checks {
		if env[k] != want {
			t.Errorf("env[%q] = %q, want %q", k, env[k], want)
		}
	}

	if !strings.Contains(env["RUSTFS_DOWNLOAD_URL"], "1.0.0-beta.3") {
		t.Errorf("RUSTFS_DOWNLOAD_URL = %q, want it to contain the version", env["RUSTFS_DOWNLOAD_URL"])
	}

	if !strings.Contains(env["ARTIFACTS_BUCKETS"], "ocfp-lab-wayne-mgmt-bosh") {
		t.Errorf("ARTIFACTS_BUCKETS = %q, missing expected bucket", env["ARTIFACTS_BUCKETS"])
	}
}

func TestBuildArtifactsProvisionEnv_TLSDisabledOmitsCertKey(t *testing.T) {
	t.Parallel()

	cfg := testArtifactsCfg()
	lr := &artifacts.LookupResult{
		AccessKey: "AK",
		SecretKey: "SK",
		TLSMode:   config.ArtifactsTLSModeDisabled,
		PrivateIP: "10.64.64.11",
	}

	env := buildArtifactsProvisionEnv(cfg, lr, "", "")

	if env["RUSTFS_TLS_ENABLED"] != "false" {
		t.Errorf("RUSTFS_TLS_ENABLED = %q, want false", env["RUSTFS_TLS_ENABLED"])
	}

	if _, ok := env["RUSTFS_TLS_CERT"]; ok {
		t.Errorf("RUSTFS_TLS_CERT should be absent when TLS disabled")
	}

	if _, ok := env["RUSTFS_TLS_KEY"]; ok {
		t.Errorf("RUSTFS_TLS_KEY should be absent when TLS disabled")
	}
}

func TestBuildArtifactsProvisionEnv_FilesystemOverride(t *testing.T) {
	t.Parallel()

	cfg := testArtifactsCfg()
	cfg.Artifacts.Data.Filesystem = "ZFS" // case-insensitive
	lr := &artifacts.LookupResult{AccessKey: "AK", SecretKey: "SK", PrivateIP: "10.64.64.11"}

	env := buildArtifactsProvisionEnv(cfg, lr, "", "")

	if env["ARTIFACTS_DATA_FILESYSTEM"] != "zfs" {
		t.Errorf("ARTIFACTS_DATA_FILESYSTEM = %q, want zfs", env["ARTIFACTS_DATA_FILESYSTEM"])
	}
}

func TestRenderEnvAssignments_SortedAndQuoteSafe(t *testing.T) {
	t.Parallel()

	out := renderEnvAssignments(map[string]string{
		"B_KEY": "plain",
		"A_KEY": "has'quote",
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out)
	}

	if lines[0] != `A_KEY='has'\''quote'` {
		t.Errorf("line 0 = %q, want quote-escaped A_KEY first (sorted)", lines[0])
	}

	if lines[1] != `B_KEY='plain'` {
		t.Errorf("line 1 = %q, want B_KEY='plain'", lines[1])
	}
}
