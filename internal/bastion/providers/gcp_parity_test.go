package providers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	sshkey "golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"testing"
)

func TestGCPBastionUsesRecordedStateAndBootstrapKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("OCFP_HOME", "")
	for _, k := range []string{"GCP_BASTION_IP", "OCFP_BASTION_IP", "OCFP_SSH_KEY"} {
		t.Setenv(k, "")
	}
	dir, err := state.GetStateDir("prod")
	if err != nil {
		t.Fatal(err)
	}
	sm, err := state.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sm.Load("prod"); err != nil {
		t.Fatal(err)
	}
	if err = sm.SetOutput("bastion_public_ip", "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	if err = sm.Save(); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := sshkey.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	keyDir := config.OcfpSSHKeyDir("prod")
	if err = os.MkdirAll(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(keyDir, "id_ed25519")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := NewGCPBastionInit(&config.Config{Name: "prod"}).GetConnectionDetails(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "203.0.113.10" || got.PrivateKeyPath != keyPath {
		t.Fatalf("connection=%+v", got)
	}
}
func TestGCPBastionEnvironmentCarriesGenesisAndCredentials(t *testing.T) {
	t.Setenv("GOOGLE_PROJECT", "project-a")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GOOGLE_ZONE", "us-central1-a")
	cfg := &config.Config{Name: "prod", ServiceAccountJSON: `{"type":"service_account","private_key":"x'$(touch /tmp/nope)"}`}
	cfg.Bastion.Genesis.Enabled = true
	cfg.Bastion.Genesis.Branch = "dev"
	g := NewGCPBastionInit(cfg)
	env := g.PrepareEnvironment()
	if env["GENESIS_ENVIRONMENT"] != "prod" || env["GENESIS_BRANCH"] != "dev" || env["GOOGLE_PROJECT"] != "project-a" || env["GOOGLE_ZONE"] != "us-central1-a" {
		t.Fatalf("missing provider/Genesis environment: keys=%v", env)
	}
	raw, err := base64.StdEncoding.DecodeString(env["OCFP_GCP_CREDENTIALS_B64"])
	if err != nil || string(raw) != cfg.ServiceAccountJSON {
		t.Fatal("inline credentials not transported safely")
	}
}
