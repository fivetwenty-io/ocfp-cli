package azure

import (
	"testing"
)

func TestStripLabelPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"plain key unchanged", "bloc", "bloc"},
		{"label. prefix stripped", "label.bloc", "bloc"},
		{"label: prefix stripped", "label:bloc", "bloc"},
		{"tag: prefix unchanged", "tag:bloc", "tag:bloc"},
		{"nested label. key", "label.managed-by", "managed-by"},
		{"empty string unchanged", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripLabelPrefix(tt.input)
			if got != tt.expected {
				t.Errorf("stripLabelPrefix(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestMatchesInstanceFilters_LabelPrefix(t *testing.T) {
	tags := map[string]string{
		"bloc":       "520-aws-wayne",
		"managed-by": "ocfp",
		"role":       "bastion",
	}

	tests := []struct {
		name     string
		filters  map[string]string
		expected bool
	}{
		{
			"empty filters match everything",
			map[string]string{},
			true,
		},
		{
			"plain key matches",
			map[string]string{"bloc": "520-aws-wayne"},
			true,
		},
		{
			"label. prefix matches after stripping",
			map[string]string{"label.bloc": "520-aws-wayne", "label.role": "bastion"},
			true,
		},
		{
			"label: prefix matches after stripping",
			map[string]string{"label:bloc": "520-aws-wayne"},
			true,
		},
		{
			"wrong value does not match",
			map[string]string{"label.bloc": "wrong-bloc"},
			false,
		},
		{
			"missing tag does not match",
			map[string]string{"label.nonexistent": "value"},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesInstanceFilters(tags, tt.filters)
			if got != tt.expected {
				t.Errorf("matchesInstanceFilters() = %v, want %v", got, tt.expected)
			}
		})
	}
}
