package bootstrap

import "testing"

func TestShouldAutoProvisionTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		image    string
		want     bool
	}{
		{"pve + known catalog name → true", "pve", "ubuntu-noble-template", true},
		{"PVE (uppercase) + known catalog name → true", "PVE", "ubuntu-noble-template", true},
		{"pve + unknown name → false", "pve", "some-random-template", false},
		{"non-pve provider + known name → false", "stackit", "ubuntu-noble-template", false},
		{"aws + known name → false", "aws", "ubuntu-noble-template", false},
		{"empty provider + known name → false", "", "ubuntu-noble-template", false},
		{"pve + empty name → false", "pve", "", false},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := shouldAutoProvisionTemplate(tc.provider, tc.image)
			if got != tc.want {
				t.Errorf("shouldAutoProvisionTemplate(%q, %q) = %v, want %v", tc.provider, tc.image, got, tc.want)
			}
		})
	}
}
