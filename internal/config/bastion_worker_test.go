package config_test

import (
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
)

// TestBastionWorkerBundleBinds pins the YAML key the operator writes to
// name the worker bundle.
func TestBastionWorkerBundleBinds(t *testing.T) {
	t.Parallel()

	var b config.Bastion
	if err := yaml.Unmarshal([]byte("worker:\n  bundle: /tmp/bloc-worker.tar.gz\n"), &b); err != nil {
		t.Fatal(err)
	}

	if b.Worker.Bundle != "/tmp/bloc-worker.tar.gz" {
		t.Errorf("Worker.Bundle = %q, want %q", b.Worker.Bundle, "/tmp/bloc-worker.tar.gz")
	}
}

// TestBastionWorkerBundleDefaultsEmpty pins that a config without the block
// names no bundle, so the worker phase does nothing.
func TestBastionWorkerBundleDefaultsEmpty(t *testing.T) {
	t.Parallel()

	var b config.Bastion
	if err := yaml.Unmarshal([]byte("flavor: bastion\n"), &b); err != nil {
		t.Fatal(err)
	}

	got, err := b.Worker.BundlePath(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}

	if got != "" {
		t.Errorf("BundlePath() = %q, want empty", got)
	}
}

func TestBastionWorkerBundlePathResolution(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	cases := []struct {
		name   string
		config string
		env    string
		want   string
	}{
		{"config only", "/srv/bundle.tar.gz", "", "/srv/bundle.tar.gz"},
		{"environment wins", "/srv/bundle.tar.gz", "/opt/other.tar.gz", "/opt/other.tar.gz"},
		{"environment alone", "", "/opt/other.tar.gz", "/opt/other.tar.gz"},
		{"tilde expands to home", "~/Downloads/bundle.tar.gz", "", filepath.Join(home, "Downloads", "bundle.tar.gz")},
		{"blank is unset", "   ", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := config.BastionWorkerConfig{Bundle: tc.config}
			getenv := func(key string) string {
				switch key {
				case config.EnvWorkerBundle:
					return tc.env
				case "HOME":
					return home
				default:
					return ""
				}
			}

			got, err := w.BundlePath(getenv)
			if err != nil {
				t.Fatal(err)
			}

			if got != tc.want {
				t.Errorf("BundlePath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBastionWorkerBundleEnvName pins the variable's name, which the docs
// and the operator's shell both spell out.
func TestBastionWorkerBundleEnvName(t *testing.T) {
	t.Parallel()

	if config.EnvWorkerBundle != "OCFP_WORKER_BUNDLE" {
		t.Errorf("EnvWorkerBundle = %q", config.EnvWorkerBundle)
	}
}

// TestBastionWorkerBundleTildeWithoutHome refuses to guess a home.
func TestBastionWorkerBundleTildeWithoutHome(t *testing.T) {
	t.Parallel()

	w := config.BastionWorkerConfig{Bundle: "~/bundle.tar.gz"}

	_, err := w.BundlePath(func(string) string { return "" })
	if err == nil {
		t.Fatal("BundlePath() with ~ and no HOME should fail")
	}
}
