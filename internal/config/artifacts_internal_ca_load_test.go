package config

import "testing"

// TestValidate_Integration_Artifacts_InternalCA_NoVaultEnv_Succeeds pins the
// behavior that tls.mode=internal-ca never hard-fails config load/validate,
// even with no vault environment configured at all. The bloc CA is minted
// lazily by bootstrap/`artifacts provision` (vault.LoadOrGenerateBlocCA), so
// validate() always passes internalCAConfigured=true unconditionally
// (config.go, cfg.Artifacts.Validate call) — commands that never touch vault
// (`ssh bastion`, `lookup`, `status`, `teardown`) must stay unaffected by
// vault reachability. Any refactor that makes this conditional on a live
// vault check reintroduces the incident class this test guards against: a
// stale/misbuilt binary aside, the failure originally reported as
// "artifacts tls.mode=internal-ca requires an internal CA to be configured"
// must never be reachable from validate() alone.
func TestValidate_Integration_Artifacts_InternalCA_NoVaultEnv_Succeeds(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("OCFP_VAULT_ADDR", "")
	t.Setenv("OCFP_VAULT_TOKEN", "")
	t.Setenv("HOME", t.TempDir()) // no real ~/.saferc to fall back to either

	cfg := &Config{
		Name:        "test-pve-artifacts",
		Provider:    "pve",
		AuthToken:   "root@pam!ocfp-bosh=abc123",
		TokenSecret: "secret-uuid",
		Bastion:     Bastion{Flavor: "bastion"},
	}
	cfg.Artifacts.Enabled = true
	cfg.Artifacts.Data.DiskSizeGiB = 500
	cfg.Artifacts.Data.Filesystem = ArtifactsFilesystemExt4
	cfg.Artifacts.TLS.Mode = ArtifactsTLSModeInternalCA

	err := validate(cfg)
	if err != nil {
		t.Fatalf("validate() with tls.mode=internal-ca and no vault env: got error %v, want nil", err)
	}
}

// TestValidate_Integration_Artifacts_InternalCA_DisabledArtifacts_Succeeds is
// the control case: artifacts disabled must always pass regardless of TLS
// mode, confirming the internal-ca gate is scoped to Enabled=true.
func TestValidate_Integration_Artifacts_InternalCA_DisabledArtifacts_Succeeds(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_TOKEN", "")

	cfg := &Config{
		Name:        "test-pve-artifacts-disabled",
		Provider:    "pve",
		AuthToken:   "root@pam!ocfp-bosh=abc123",
		TokenSecret: "secret-uuid",
	}
	cfg.Artifacts.Enabled = false
	cfg.Artifacts.TLS.Mode = ArtifactsTLSModeInternalCA

	err := validate(cfg)
	if err != nil {
		t.Fatalf("validate() with artifacts disabled: got error %v, want nil", err)
	}
}
