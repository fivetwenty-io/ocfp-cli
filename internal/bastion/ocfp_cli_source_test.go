package bastion

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// resolveInputs builds resolver inputs from plain values so each case reads
// as data. existing lists the paths FileExists reports as present.
func resolveInputs(cfg config.OCFPCLIConfig, env map[string]string, operator string, existing ...string) ocfpCLIResolveInputs {
	present := map[string]bool{}
	for _, p := range existing {
		present[p] = true
	}

	return ocfpCLIResolveInputs{
		Config:          cfg,
		Getenv:          func(key string) string { return env[key] },
		FileExists:      func(p string) bool { return present[p] },
		OperatorVersion: operator,
	}
}

func TestResolveOCFPCLIInstall_DefaultsToReleaseMatchingOperator(t *testing.T) {
	t.Parallel()

	spec, err := resolveOCFPCLIInstall(resolveInputs(config.OCFPCLIConfig{}, nil, "0.1.0"))
	if err != nil {
		t.Fatalf("resolveOCFPCLIInstall: %v", err)
	}

	if spec.Source != ocfpCLISourceRelease {
		t.Errorf("Source = %q, want %q", spec.Source, ocfpCLISourceRelease)
	}

	if spec.Version != "0.1.0" {
		t.Errorf("Version = %q, want operator version 0.1.0", spec.Version)
	}
}

// TestResolveOCFPCLIInstall_NonReleaseOperatorFallsBackToLatest covers every
// stamp a non-release build can carry: the ldflags default, the Makefile's
// git-describe output in its clean, ahead, and dirty forms, its no-tag
// fallback, and a non-semver backup tag. None has a release asset, so each
// must resolve to latest rather than a guaranteed 404 or a fatal error.
func TestResolveOCFPCLIInstall_NonReleaseOperatorFallsBackToLatest(t *testing.T) {
	t.Parallel()

	for _, operator := range []string{
		"dev",
		"",
		"v0.1.0-3-gabc1234",
		"v0.1.0-dirty",
		"0.1.0-3-gabc1234-dirty",
		"dev/main/abc1234",
		"backup/main-presync-299-g60a8476",
		"abc1234",
	} {
		spec, err := resolveOCFPCLIInstall(resolveInputs(config.OCFPCLIConfig{}, nil, operator))
		if err != nil {
			t.Fatalf("operator %q: %v", operator, err)
		}

		if spec.Version != "" {
			t.Errorf("operator %q: Version = %q, want empty (latest)", operator, spec.Version)
		}
	}
}

func TestResolveOCFPCLIInstall_ReleaseOperatorVersionsMatch(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"0.1.0":       "0.1.0",
		"v0.1.0":      "0.1.0",
		"1.2.3-rc.1":  "1.2.3-rc.1",
		"1.2.3-beta2": "1.2.3-beta2",
	}

	for operator, want := range cases {
		spec, err := resolveOCFPCLIInstall(resolveInputs(config.OCFPCLIConfig{}, nil, operator))
		if err != nil {
			t.Fatalf("operator %q: %v", operator, err)
		}

		if spec.Version != want {
			t.Errorf("operator %q: Version = %q, want %q", operator, spec.Version, want)
		}
	}
}

func TestResolveOCFPCLIInstall_VersionPrecedence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cfg      config.OCFPCLIConfig
		env      map[string]string
		operator string
		want     string
	}{
		{"config beats operator", config.OCFPCLIConfig{Version: "0.2.0"}, nil, "0.1.0", "0.2.0"},
		{"env beats config", config.OCFPCLIConfig{Version: "0.2.0"}, map[string]string{"OCFP_CLI_VERSION": "0.3.0"}, "0.1.0", "0.3.0"},
		{"leading v stripped from env", config.OCFPCLIConfig{}, map[string]string{"OCFP_CLI_VERSION": "v0.3.0"}, "dev", "0.3.0"},
		{"leading v stripped from config", config.OCFPCLIConfig{Version: "v0.2.0"}, nil, "dev", "0.2.0"},
		{"explicit latest means latest", config.OCFPCLIConfig{Version: "latest"}, nil, "0.1.0", ""},
	}

	for _, tc := range cases {
		spec, err := resolveOCFPCLIInstall(resolveInputs(tc.cfg, tc.env, tc.operator))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if spec.Version != tc.want {
			t.Errorf("%s: Version = %q, want %q", tc.name, spec.Version, tc.want)
		}
	}
}

func TestResolveOCFPCLIInstall_SourcePrecedence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cfg      config.OCFPCLIConfig
		env      map[string]string
		existing []string
		want     ocfpCLISource
	}{
		{"config local", config.OCFPCLIConfig{Source: "local"}, nil, nil, ocfpCLISourceLocal},
		{"config release", config.OCFPCLIConfig{Source: "release"}, nil, nil, ocfpCLISourceRelease},
		{"env local beats config release", config.OCFPCLIConfig{Source: "release"}, map[string]string{"OCFP_CLI_SOURCE": "local"}, nil, ocfpCLISourceLocal},
		{"env release beats config local", config.OCFPCLIConfig{Source: "local"}, map[string]string{"OCFP_CLI_SOURCE": "release"}, nil, ocfpCLISourceRelease},
		{"existing binary path implies local", config.OCFPCLIConfig{}, map[string]string{"OCFP_BINARY_PATH": "/tmp/ocfp"}, []string{"/tmp/ocfp"}, ocfpCLISourceLocal},
		{"stale binary path does not imply local", config.OCFPCLIConfig{}, map[string]string{"OCFP_BINARY_PATH": "/tmp/gone"}, nil, ocfpCLISourceRelease},
		{"env source beats binary path", config.OCFPCLIConfig{}, map[string]string{"OCFP_BINARY_PATH": "/tmp/ocfp", "OCFP_CLI_SOURCE": "release"}, []string{"/tmp/ocfp"}, ocfpCLISourceRelease},
		{"case insensitive", config.OCFPCLIConfig{Source: "Local"}, nil, nil, ocfpCLISourceLocal},
	}

	for _, tc := range cases {
		spec, err := resolveOCFPCLIInstall(resolveInputs(tc.cfg, tc.env, "0.1.0", tc.existing...))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if spec.Source != tc.want {
			t.Errorf("%s: Source = %q, want %q", tc.name, spec.Source, tc.want)
		}
	}
}

// TestResolveOCFPCLIInstall_LocalIgnoresVersion pins that the local source
// never touches version resolution, so an unpinnable operator stamp cannot
// break the development escape hatch.
func TestResolveOCFPCLIInstall_LocalIgnoresVersion(t *testing.T) {
	t.Parallel()

	spec, err := resolveOCFPCLIInstall(resolveInputs(
		config.OCFPCLIConfig{Version: "not a version at all"},
		map[string]string{"OCFP_CLI_SOURCE": "local"},
		"dev/main/abc1234",
	))
	if err != nil {
		t.Fatalf("local source must not validate versions, got %v", err)
	}

	if spec.Source != ocfpCLISourceLocal || spec.Version != "" {
		t.Errorf("spec = %+v, want local with empty version", spec)
	}
}

func TestResolveOCFPCLIInstall_RejectsUnknownSource(t *testing.T) {
	t.Parallel()

	if _, err := resolveOCFPCLIInstall(resolveInputs(config.OCFPCLIConfig{Source: "ftp"}, nil, "0.1.0")); err == nil {
		t.Error("config source ftp: expected error, got nil")
	}

	if _, err := resolveOCFPCLIInstall(resolveInputs(config.OCFPCLIConfig{}, map[string]string{"OCFP_CLI_SOURCE": "nope"}, "0.1.0")); err == nil {
		t.Error("env source nope: expected error, got nil")
	}
}

// TestResolveOCFPCLIInstall_RejectsUnsafePins keeps shell metacharacters out
// of the generated script when the pin comes from an operator.
func TestResolveOCFPCLIInstall_RejectsUnsafePins(t *testing.T) {
	t.Parallel()

	for _, pin := range []string{"0.1.0; rm -rf /", "$(id)", "0.1.0'", "../x"} {
		if _, err := resolveOCFPCLIInstall(resolveInputs(config.OCFPCLIConfig{Version: pin}, nil, "0.1.0")); err == nil {
			t.Errorf("pin %q: expected error, got nil", pin)
		}
	}
}
