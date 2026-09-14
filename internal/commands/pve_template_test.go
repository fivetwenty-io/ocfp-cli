package commands

import (
	"strings"
	"testing"
)

// TestResolveTemplateName covers the argument handling for
// `ocfp pve template provision`.
//
// An unknown name has to fail before any provider call, with an error that
// lists what is available. The catalog is compile-time, so a typo is the most
// likely reason a name is missing and the operator needs to see the real names
// rather than a provider-side "image not found" much later.
func TestResolveTemplateName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		arg     string
		wantErr bool
	}{
		{"noble vanilla", "ubuntu-noble-template", false},
		{"noble bastion", "ubuntu-noble-bastion-template", false},
		{"resolute vanilla", "ubuntu-resolute-template", false},
		{"resolute bastion", "ubuntu-resolute-bastion-template", false},
		{"unknown name", "ubuntu-2204-cloudinit", true},
		{"empty name", "", true},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spec, err := resolveTemplateName(tc.arg)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tc.arg)
				}

				return
			}

			if err != nil {
				t.Fatalf("resolveTemplateName(%q): %v", tc.arg, err)
			}

			if spec.Name != tc.arg {
				t.Errorf("spec.Name = %q, want %q", spec.Name, tc.arg)
			}
		})
	}
}

// TestResolveTemplateName_ErrorListsCatalog asserts the failure is actionable.
func TestResolveTemplateName_ErrorListsCatalog(t *testing.T) {
	t.Parallel()

	_, err := resolveTemplateName("ubuntu-2204-cloudinit")
	if err == nil {
		t.Fatal("expected an error")
	}

	msg := err.Error()

	for _, want := range []string{"ubuntu-noble-template", "ubuntu-resolute-template"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention the available template %q", msg, want)
		}
	}
}

// TestCatalogTemplateNames asserts the listing is sorted, so the command's
// output and its error messages are stable rather than map-iteration order.
func TestCatalogTemplateNames(t *testing.T) {
	t.Parallel()

	names := catalogTemplateNames()

	if len(names) < 4 {
		t.Fatalf("catalogTemplateNames returned %d names, want at least the four shipped templates", len(names))
	}

	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("catalog names are not sorted: %q came before %q", names[i-1], names[i])
		}
	}
}
