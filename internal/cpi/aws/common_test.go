package aws

import (
	"sort"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func TestBuildAWSTagFilters(t *testing.T) {
	tests := []struct {
		name     string
		filters  map[string]string
		expected []types.Filter
	}{
		{
			name:     "nil filters returns nil",
			filters:  nil,
			expected: nil,
		},
		{
			name:     "empty filters returns nil",
			filters:  map[string]string{},
			expected: nil,
		},
		{
			name: "tag keys get tag: prefix",
			filters: map[string]string{
				"bloc":       "520-aws-wayne",
				"managed-by": "ocfp",
			},
			expected: []types.Filter{
				{Name: aws.String("tag:bloc"), Values: []string{"520-aws-wayne"}},
				{Name: aws.String("tag:managed-by"), Values: []string{"ocfp"}},
			},
		},
		{
			name: "AWS-specific keys pass through as-is",
			filters: map[string]string{
				"vpc-id":    "vpc-123",
				"subnet-id": "subnet-456",
				"name":      "my-resource",
			},
			expected: []types.Filter{
				{Name: aws.String("name"), Values: []string{"my-resource"}},
				{Name: aws.String("subnet-id"), Values: []string{"subnet-456"}},
				{Name: aws.String("vpc-id"), Values: []string{"vpc-123"}},
			},
		},
		{
			name: "already prefixed tag: keys pass through",
			filters: map[string]string{
				"tag:Name":       "my-instance",
				"tag:managed-by": "ocfp",
			},
			expected: []types.Filter{
				{Name: aws.String("tag:Name"), Values: []string{"my-instance"}},
				{Name: aws.String("tag:managed-by"), Values: []string{"ocfp"}},
			},
		},
		{
			name: "empty values are skipped",
			filters: map[string]string{
				"bloc":       "520-aws-wayne",
				"managed-by": "",
				"empty-tag":  "",
			},
			expected: []types.Filter{
				{Name: aws.String("tag:bloc"), Values: []string{"520-aws-wayne"}},
			},
		},
		{
			name: "mixed tag and AWS-specific keys",
			filters: map[string]string{
				"bloc":       "520-aws-wayne",
				"managed-by": "ocfp",
				"vpc-id":     "vpc-123",
				"tag:Name":   "my-instance",
			},
			expected: []types.Filter{
				{Name: aws.String("tag:Name"), Values: []string{"my-instance"}},
				{Name: aws.String("tag:bloc"), Values: []string{"520-aws-wayne"}},
				{Name: aws.String("tag:managed-by"), Values: []string{"ocfp"}},
				{Name: aws.String("vpc-id"), Values: []string{"vpc-123"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildAWSTagFilters(tt.filters)

			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d filters, got %d: %v", len(tt.expected), len(result), result)
			}

			if len(result) == 0 {
				return
			}

			// Sort both slices by filter name for deterministic comparison
			sort.Slice(result, func(i, j int) bool {
				return aws.ToString(result[i].Name) < aws.ToString(result[j].Name)
			})
			sort.Slice(tt.expected, func(i, j int) bool {
				return aws.ToString(tt.expected[i].Name) < aws.ToString(tt.expected[j].Name)
			})

			for i := range tt.expected {
				gotName := aws.ToString(result[i].Name)
				wantName := aws.ToString(tt.expected[i].Name)

				if gotName != wantName {
					t.Errorf("filter[%d] name = %q, want %q", i, gotName, wantName)
				}

				if len(result[i].Values) != len(tt.expected[i].Values) {
					t.Errorf("filter[%d] values count = %d, want %d", i, len(result[i].Values), len(tt.expected[i].Values))
					continue
				}

				for j := range tt.expected[i].Values {
					if result[i].Values[j] != tt.expected[i].Values[j] {
						t.Errorf("filter[%d] value[%d] = %q, want %q", i, j, result[i].Values[j], tt.expected[i].Values[j])
					}
				}
			}
		})
	}
}
