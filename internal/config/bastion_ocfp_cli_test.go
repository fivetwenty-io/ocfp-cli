package config_test

import (
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestBastionOCFPCLIAcceptsBothKeyStyles guards the ocfpCli block against the
// same snake_case binding gap that once swallowed ssh_user.
func TestBastionOCFPCLIAcceptsBothKeyStyles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		doc  string
	}{
		{"camelCase", "ocfpCli:\n  source: local\n  version: 0.1.0\n"},
		{"snake_case", "ocfp_cli:\n  source: local\n  version: 0.1.0\n"},
	}

	for _, tc := range cases {
		var b config.Bastion
		if err := yaml.Unmarshal([]byte(tc.doc), &b); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if b.OCFPCLI.Source != "local" {
			t.Errorf("%s: OCFPCLI.Source = %q, want %q", tc.name, b.OCFPCLI.Source, "local")
		}

		if b.OCFPCLI.Version != "0.1.0" {
			t.Errorf("%s: OCFPCLI.Version = %q, want %q", tc.name, b.OCFPCLI.Version, "0.1.0")
		}
	}
}

// TestBastionOCFPCLIDefaultsEmpty pins that an absent block leaves both
// fields empty, so the resolver's defaults (release, operator version) apply.
func TestBastionOCFPCLIDefaultsEmpty(t *testing.T) {
	t.Parallel()

	var b config.Bastion
	if err := yaml.Unmarshal([]byte("flavor: bastion\n"), &b); err != nil {
		t.Fatal(err)
	}

	if b.OCFPCLI.Source != "" || b.OCFPCLI.Version != "" {
		t.Errorf("OCFPCLI = %+v, want zero value", b.OCFPCLI)
	}
}
