package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"strings"
)

// buildAWSTagFilters converts a filter map to AWS EC2 filters.
// For tag-based filters (like "bloc", "managed-by"), it automatically adds the "tag:" prefix.
// For AWS-specific filters (like "vpc-id", "network-id", "name"), it passes them through as-is.
//
// This ensures that tag filtering works correctly with AWS APIs, which require the "tag:" prefix
// for tag-based filtering.
//
// Example:
//
//	input:  {"bloc": "my-bloc", "managed-by": "ocfp", "vpc-id": "vpc-123"}
//	output: [
//	  {Name: "tag:bloc", Values: ["my-bloc"]},
//	  {Name: "tag:managed-by", Values: ["ocfp"]},
//	  {Name: "vpc-id", Values: ["vpc-123"]}
//	]
func buildAWSTagFilters(filters map[string]string) []types.Filter {
	if len(filters) == 0 {
		return nil
	}

	awsFilters := make([]types.Filter, 0, len(filters))

	// AWS-specific filter keys that should NOT have "tag:" prefix
	awsSpecificKeys := map[string]bool{
		"vpc-id":              true,
		"network-id":          true,
		"name":                true,
		"group-name":          true,
		"description":         true,
		"subnet-id":           true,
		"availability-zone":   true,
		"state":               true,
		"attachment.state":    true,
		"attachment.instance": true,
		"owner-id":            true,
	}

	for key, value := range filters {
		// Skip empty values
		if value == "" {
			continue
		}

		var filterName string

		// Check if the key already has "tag:" prefix
		if strings.HasPrefix(key, "tag:") {
			filterName = key
		} else if awsSpecificKeys[key] {
			// AWS-specific filter keys are passed through as-is
			filterName = key
		} else {
			// All other keys are assumed to be tags and need "tag:" prefix
			filterName = "tag:" + key
		}

		awsFilters = append(awsFilters, types.Filter{
			Name:   aws.String(filterName),
			Values: []string{value},
		})
	}

	return awsFilters
}
