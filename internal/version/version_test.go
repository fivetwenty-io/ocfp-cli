package version_test

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/version"
)

// Build stamps come from two places that disagree about the "v" prefix: the
// Makefile passes `git describe --tags` output ("v0.2.3") while GoReleaser
// passes a bare "0.2.3". Get normalizes both so the rendered strings never
// double the prefix.
func TestGetNormalizesStampedVersion(t *testing.T) {
	tests := []struct {
		name    string
		stamped string
		want    string
	}{
		{name: "makefile git describe", stamped: "v0.2.3", want: "0.2.3"},
		{name: "goreleaser bare", stamped: "0.2.3", want: "0.2.3"},
		{name: "dirty describe", stamped: "v0.2.3-2-gd26e1ef-dirty", want: "0.2.3-2-gd26e1ef-dirty"},
		{name: "surrounding whitespace", stamped: "  v0.2.3\n", want: "0.2.3"},
		{name: "dev default", stamped: "dev", want: "dev"},
		{name: "dev branch fallback", stamped: "dev/main/d26e1ef", want: "dev/main/d26e1ef"},
		{name: "empty", stamped: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := version.Version
			version.Version = tt.stamped
			defer func() { version.Version = restore }()

			if got := version.Get().Version; got != tt.want {
				t.Errorf("Get().Version = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStringRendersSingleVPrefix(t *testing.T) {
	restore := version.Version
	version.Version = "v0.2.3"
	defer func() { version.Version = restore }()

	got := version.Get().String()
	if !strings.HasPrefix(got, "OCFP CLI v0.2.3 (") {
		t.Errorf("String() = %q, want prefix %q", got, "OCFP CLI v0.2.3 (")
	}

	if strings.Contains(got, "vv") {
		t.Errorf("String() = %q, contains doubled v prefix", got)
	}
}

func TestShortRendersSingleVPrefix(t *testing.T) {
	restore := version.Version
	version.Version = "v0.2.3"
	defer func() { version.Version = restore }()

	if got := version.Get().Short(); got != "v0.2.3" {
		t.Errorf("Short() = %q, want %q", got, "v0.2.3")
	}
}
