package provision

import (
	"context"
	"encoding/base64"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGCPCredentialScriptIsOwnerOnlyAndNotSystemEnv(t *testing.T) {
	cfg := &config.Config{Name: "prod", ServiceAccountJSON: `{"private_key":"secret"}`}
	m := NewEnvironmentManager("gcp", cfg)
	script := m.GenerateSystemEnvironmentScript(context.Background())
	if !strings.Contains(script, "OCFP_GCP_CREDENTIALS_B64") || !strings.Contains(script, "umask 077") || !strings.Contains(script, "GOOGLE_APPLICATION_CREDENTIALS") {
		t.Fatal("missing safe credential installation")
	}
	if strings.Contains(script, cfg.ServiceAccountJSON) {
		t.Fatal("raw credentials in generated system script")
	}
	for k := range m.GetSystemEnvironmentVarsForPreview() {
		if strings.Contains(k, "CREDENTIALS") {
			t.Fatal("credentials exposed in world-readable system environment")
		}
	}
}

func TestGCPCredentialScriptPreservesArbitraryJSON(t *testing.T) {
	home := t.TempDir()
	raw := `{"private_key":"apostrophe ' and $(touch SHOULD_NOT_EXIST) and PROFILE_EOF"}`
	encoded := base64.StdEncoding.EncodeToString([]byte(raw))
	script := GenerateGCPCredentialsScript()
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+home, "OCFP_PROVIDER=gcp", "OCFP_BLOC=prod", "OCFP_GCP_CREDENTIALS_B64="+encoded)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("credential script failed: %v %s", err, out)
	}
	path := filepath.Join(home, ".config", "ocfp", "prod", "gcp-credentials.json")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != raw {
		t.Fatalf("credential content changed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential mode: %v %v", info, err)
	}
	if strings.Contains(string(out), raw) || strings.Contains(string(out), encoded) {
		t.Fatal("credentials leaked to output")
	}
	if strings.Contains(logger.RedactSecrets("export OCFP_GCP_CREDENTIALS_B64='"+encoded+"'"), encoded) {
		t.Fatal("encoded credentials evade redaction")
	}
}
func TestGCPCredentialScriptRefusesSymlink(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "ocfp", "prod")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "keep")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "gcp-credentials.json")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", GenerateGCPCredentialsScript())
	cmd.Env = append(os.Environ(), "HOME="+home, "OCFP_PROVIDER=gcp", "OCFP_BLOC=prod", "OCFP_GCP_CREDENTIALS_B64=e30=")
	if err := cmd.Run(); err == nil {
		t.Fatal("symlink accepted")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "keep" {
		t.Fatal("linked file overwritten")
	}
}
