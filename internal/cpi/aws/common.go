package aws

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// buildResourceTags converts a tag map to AWS EC2 tags, automatically adding
// a "managed-by=ocfp" tag unless the caller already supplied one.
func buildResourceTags(tags map[string]string) []types.Tag {
	awsTags := make([]types.Tag, 0, len(tags)+1)

	if _, exists := tags["managed-by"]; !exists {
		awsTags = append(awsTags, types.Tag{
			Key:   aws.String("managed-by"),
			Value: aws.String("ocfp"),
		})
	}

	for k, v := range tags {
		awsTags = append(awsTags, types.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}

	return awsTags
}

// buildNamedResourceTags is buildResourceTags with an explicit Name tag prepended.
// Used for EC2 resources where the Name tag is conventionally set.
func buildNamedResourceTags(name string, tags map[string]string) []types.Tag {
	awsTags := make([]types.Tag, 0, len(tags)+2)
	awsTags = append(awsTags, types.Tag{
		Key:   aws.String("Name"),
		Value: aws.String(name),
	})

	return append(awsTags, buildResourceTags(tags)...)
}

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

		// Strip "label." or "label:" prefix (CPI-agnostic filter convention)
		cleanKey := key

		switch {
		case strings.HasPrefix(cleanKey, "label."):
			cleanKey = strings.TrimPrefix(cleanKey, "label.")
		case strings.HasPrefix(cleanKey, "label:"):
			cleanKey = strings.TrimPrefix(cleanKey, "label:")
		}

		var filterName string

		// Check if the key already has "tag:" prefix
		switch {
		case strings.HasPrefix(cleanKey, "tag:"):
			filterName = cleanKey
		case awsSpecificKeys[cleanKey]:
			// AWS-specific filter keys are passed through as-is
			filterName = cleanKey
		default:
			// All other keys are assumed to be tags and need "tag:" prefix
			filterName = "tag:" + cleanKey
		}

		awsFilters = append(awsFilters, types.Filter{
			Name:   aws.String(filterName),
			Values: []string{value},
		})
	}

	return awsFilters
}
