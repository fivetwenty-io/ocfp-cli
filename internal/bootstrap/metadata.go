package bootstrap

import (
	"time"
)

// MetadataManager handles resource metadata generation and management.
type MetadataManager struct {
	blocName string
}

// NewMetadataManager creates a new metadata manager.
func NewMetadataManager(blocName string) *MetadataManager {
	return &MetadataManager{
		blocName: blocName,
	}
}

// BuildBaseTags generates the core metadata tags for all resources.
// Returns a map with bloc, managed-by, created-by, created-at, and updated-at fields.
func (m *MetadataManager) BuildBaseTags() map[string]string {
	now := time.Now().UTC().Format(time.RFC3339)

	return map[string]string{
		"bloc":       m.blocName,
		"managed-by": "ocfp",
		"created-by": "ocfp",
		"created-at": now,
		"updated-at": now,
	}
}

// BuildBaseLabels is an alias for BuildBaseTags to support STACKIT's label terminology.
func (m *MetadataManager) BuildBaseLabels() map[string]string {
	return m.BuildBaseTags()
}

// MergeTags combines base metadata tags with custom tags.
// Custom tags take precedence over base tags if there are conflicts.
func (m *MetadataManager) MergeTags(customTags map[string]string) map[string]string {
	baseTags := m.BuildBaseTags()

	if customTags == nil {
		return baseTags
	}

	// Merge custom tags into base tags
	for key, value := range customTags {
		baseTags[key] = value
	}

	return baseTags
}

// UpdateTimestamp updates the updated-at timestamp in the provided tags.
// Returns a new map with the updated timestamp.
func (m *MetadataManager) UpdateTimestamp(tags map[string]string) map[string]string {
	if tags == nil {
		tags = make(map[string]string)
	}

	result := make(map[string]string, len(tags))
	for k, v := range tags {
		result[k] = v
	}

	result["updated-at"] = time.Now().UTC().Format(time.RFC3339)

	return result
}

// FormatMetadataForDisplay formats metadata tags for display in tables.
// Returns a compact string representation: "key1=val1, key2=val2".
// FormatMetadataForDisplay formats metadata tags for display in tables.
// Returns a compact string representation: "key1=val1, key2=val2".
// Note: Requires strict metadata format with hyphenated keys (managed-by, created-at).
// Resources without proper metadata will not be displayed.
func FormatMetadataForDisplay(tags map[string]string) string {
	if len(tags) == 0 {
		return "-"
	}

	// Order: bloc, managed-by, created-at for consistent display
	result := ""
	if bloc, ok := tags["bloc"]; ok {
		result += "bloc=" + bloc
	}

	if managedBy, ok := tags["managed-by"]; ok && managedBy != "" {
		if result != "" {
			result += ", "
		}

		result += "managed-by=" + managedBy
	}

	if createdAt, ok := tags["created-at"]; ok && createdAt != "" {
		if result != "" {
			result += ", "
		}

		result += "created-at=" + createdAt
	}

	return result
}
