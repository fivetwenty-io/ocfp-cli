package gcp

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

func TestMatchesLabelFilters_LabelPrefix(t *testing.T) {
	labels := map[string]string{
		"bloc":       "520-gcp-wayne",
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
			map[string]string{"bloc": "520-gcp-wayne"},
			true,
		},
		{
			"label. prefix matches after stripping",
			map[string]string{"label.bloc": "520-gcp-wayne", "label.role": "bastion"},
			true,
		},
		{
			"label: prefix matches after stripping",
			map[string]string{"label:bloc": "520-gcp-wayne"},
			true,
		},
		{
			"wrong value does not match",
			map[string]string{"label.bloc": "wrong-bloc"},
			false,
		},
		{
			"missing label does not match",
			map[string]string{"label.nonexistent": "value"},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesLabelFilters(labels, tt.filters)
			if got != tt.expected {
				t.Errorf("matchesLabelFilters() = %v, want %v", got, tt.expected)
			}
		})
	}
}
