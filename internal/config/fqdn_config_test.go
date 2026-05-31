package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestFQDNConfigUnmarshalBase verifies FQDNConfig.Base handles both string and
// sequence YAML values, coercing single-element lists into a plain string.
func TestFQDNConfigUnmarshalBase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		want    string
		wantErr bool
	}{
		{
			name: "string value",
			yaml: `base: "example.com"`,
			want: "example.com",
		},
		{
			name: "single-element list",
			yaml: "base:\n  - \"aws.drhu.ocfp.fivetwenty.io\"",
			want: "aws.drhu.ocfp.fivetwenty.io",
		},
		{
			name: "empty list",
			yaml: "base: []",
			want: "",
		},
		{
			name: "multi-element list takes first",
			yaml: "base:\n  - \"first.example.com\"\n  - \"second.example.com\"",
			want: "first.example.com",
		},
		{
			name: "omitted base",
			yaml: "mgmt:\n  shield: \"shield.example.com\"",
			want: "",
		},
		{
			name: "null base",
			yaml: "base: null",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var fc config.FQDNConfig
			err := yaml.Unmarshal([]byte(tt.yaml), &fc)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fc.Base != tt.want {
				t.Errorf("Base = %q, want %q", fc.Base, tt.want)
			}
		})
	}
}

// TestFQDNConfigUnmarshalBasePreservesMgmtOCF ensures the Mgmt and OCF maps
// still populate correctly after adding custom unmarshal logic.
func TestFQDNConfigUnmarshalBasePreservesMgmtOCF(t *testing.T) {
	t.Parallel()

	input := `
base: "example.com"
mgmt:
  shield: "shield.example.com"
  concourse: "ci.example.com"
ocf:
  system: "sys.example.com"
  apps: "apps.example.com"
`

	var fc config.FQDNConfig
	if err := yaml.Unmarshal([]byte(input), &fc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fc.Base != "example.com" {
		t.Errorf("Base = %q, want %q", fc.Base, "example.com")
	}

	wantMgmt := map[string]string{
		"shield":    "shield.example.com",
		"concourse": "ci.example.com",
	}
	for k, want := range wantMgmt {
		if got := fc.Mgmt[k]; got != want {
			t.Errorf("Mgmt[%q] = %q, want %q", k, got, want)
		}
	}

	wantOCF := map[string]string{
		"system": "sys.example.com",
		"apps":   "apps.example.com",
	}
	for k, want := range wantOCF {
		if got := fc.OCF[k]; got != want {
			t.Errorf("OCF[%q] = %q, want %q", k, got, want)
		}
	}
}

// TestFQDNConfigRoundtripAfterSequenceCoercion exercises the full LoadWithParams
// path with a config file containing fqdns.base as a YAML list, the exact
// pattern that causes the original "cannot unmarshal !!seq into string" error.
func TestFQDNConfigRoundtripAfterSequenceCoercion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	yml := []byte("" +
		"blocs:\n" +
		"  test-bloc:\n" +
		"    name: test-bloc\n" +
		"    provider: aws\n" +
		"    region: us-east-1\n" +
		"    fqdns:\n" +
		"      base:\n" +
		"        - \"aws.test.ocfp.example.com\"\n" +
		"      mgmt:\n" +
		"        shield: \"shield.test.example.com\"\n" +
		"      ocf:\n" +
		"        system: \"system.test.example.com\"\n")

	if err := os.WriteFile(cfgPath, yml, 0o600); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := config.LoadWithParams(cfgPath, "test-bloc")
	if err != nil {
		t.Fatalf("LoadWithParams failed: %v", err)
	}

	if got, want := cfg.FQDNs.Base, "aws.test.ocfp.example.com"; got != want {
		t.Errorf("FQDNs.Base = %q, want %q", got, want)
	}

	if got, want := cfg.FQDNs.Mgmt["shield"], "shield.test.example.com"; got != want {
		t.Errorf("FQDNs.Mgmt[shield] = %q, want %q", got, want)
	}

	if got, want := cfg.FQDNs.OCF["system"], "system.test.example.com"; got != want {
		t.Errorf("FQDNs.OCF[system] = %q, want %q", got, want)
	}
}
