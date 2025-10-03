package commands

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/state"
)

const (
	// filterOperatorParts is the expected number of parts when splitting a filter query by operator.
	filterOperatorParts = 2
)

// PropertyFilter represents a filter condition for resource properties.
type PropertyFilter struct {
	Key      string // Property key (supports nested: "tags.env")
	Value    string // Value to match (supports glob patterns)
	IsGlob   bool   // Whether value contains glob patterns
	Operator string // Comparison operator (=, !=, ~=)
}

// ParseFilterQuery parses a filter query string into a PropertyFilter.
// Supported formats:
//   - "key=value"          (exact match)
//   - "key!=value"         (not equal)
//   - "key~=pattern"       (glob match)
//   - "name=web-*"         (glob pattern auto-detected)
//   - "tags.env=prod"      (nested property)
//   - "state=running"      (property match)
func ParseFilterQuery(query string) (*PropertyFilter, error) {
	if query == "" {
		return nil, ErrEmptyFilterQuery
	}

	// Determine operator (check in order of specificity: != and ~= before =)
	var key, value, operator string

	switch {
	case strings.Contains(query, "!="):
		parts := strings.SplitN(query, "!=", filterOperatorParts)
		if len(parts) != filterOperatorParts {
			return nil, fmt.Errorf("%w: %q", ErrInvalidFilterSyntax, query)
		}

		key = strings.TrimSpace(parts[0])
		value = strings.TrimSpace(parts[1])
		operator = "!="
	case strings.Contains(query, "~="):
		parts := strings.SplitN(query, "~=", filterOperatorParts)
		if len(parts) != filterOperatorParts {
			return nil, fmt.Errorf("%w: %q", ErrInvalidFilterSyntax, query)
		}

		key = strings.TrimSpace(parts[0])
		value = strings.TrimSpace(parts[1])
		operator = "~="
	case strings.Contains(query, "="):
		parts := strings.SplitN(query, "=", filterOperatorParts)
		if len(parts) != filterOperatorParts {
			return nil, fmt.Errorf("%w: %q", ErrInvalidFilterSyntax, query)
		}

		key = strings.TrimSpace(parts[0])
		value = strings.TrimSpace(parts[1])
		operator = "="
	default:
		return nil, fmt.Errorf("%w (=, !=, ~=): %q", ErrFilterMissingOperator, query)
	}

	if key == "" {
		return nil, fmt.Errorf("%w: %q", ErrFilterKeyEmpty, query)
	}

	// Auto-detect glob patterns
	isGlob := operator == "~=" || strings.ContainsAny(value, "*?[]")

	return &PropertyFilter{
		Key:      key,
		Value:    value,
		IsGlob:   isGlob,
		Operator: operator,
	}, nil
}

// Matches checks if a resource matches this filter.
func (f *PropertyFilter) Matches(resource *state.Resource) bool {
	// Extract value from resource based on key
	resourceValue := f.extractValue(resource)

	// Handle different operators
	switch f.Operator {
	case "!=":
		if f.IsGlob {
			matched, _ := filepath.Match(f.Value, resourceValue)

			return !matched
		}

		return resourceValue != f.Value

	case "~=":
		matched, _ := filepath.Match(f.Value, resourceValue)

		return matched

	case "=":
		fallthrough
	default:
		if f.IsGlob {
			matched, _ := filepath.Match(f.Value, resourceValue)

			return matched
		}

		return resourceValue == f.Value
	}
}

// extractValue extracts a value from a resource based on the filter key.
// Supports:
//   - Direct fields: id, type, name, provider, state
//   - Nested properties: tags.env, properties.size
func (f *PropertyFilter) extractValue(resource *state.Resource) string {
	const (
		minDirectFieldParts = 1
		minNestedFieldParts = 2
	)

	// Handle nested keys (e.g., "tags.env")
	keyParts := strings.Split(f.Key, ".")

	// Check direct fields first
	if len(keyParts) == minDirectFieldParts {
		switch f.Key {
		case "id":
			return resource.ID
		case "type":
			return resource.Type
		case "name":
			return resource.Name
		case "provider":
			return resource.Provider
		case "state":
			return resource.State
		}
	}

	// Handle nested properties
	if len(keyParts) >= minNestedFieldParts {
		rootKey := keyParts[0]
		nestedPath := keyParts[1:]

		switch rootKey {
		case "tags":
			if resource.Tags != nil {
				return f.getNestedStringValue(resource.Tags, nestedPath)
			}

		case "properties":
			if resource.Properties != nil {
				return f.getNestedValue(resource.Properties, nestedPath)
			}
		}
	}

	// Try as a direct property key
	if resource.Properties != nil {
		if val, ok := resource.Properties[f.Key]; ok {
			return fmt.Sprintf("%v", val)
		}
	}

	return ""
}

// getNestedStringValue retrieves a nested value from a string map.
func (f *PropertyFilter) getNestedStringValue(properties map[string]string, path []string) string {
	if len(path) == 0 {
		return ""
	}

	if len(path) == 1 {
		return properties[path[0]]
	}

	// For deeper nesting, we'd need to check if the value is itself a map
	// For now, only support one level of nesting in tags
	return properties[path[0]]
}

// getNestedValue retrieves a nested value from an interface{} map.
func (f *PropertyFilter) getNestedValue(properties map[string]interface{}, path []string) string {
	if len(path) == 0 {
		return ""
	}

	current := properties

	for pathIndex, key := range path {
		val, exists := current[key]
		if !exists {
			return ""
		}

		// If this is the last key, return the value
		if pathIndex == len(path)-1 {
			return fmt.Sprintf("%v", val)
		}

		// Otherwise, try to descend into nested map
		if nestedMap, ok := val.(map[string]interface{}); ok {
			current = nestedMap
		} else {
			return ""
		}
	}

	return ""
}

// PropertyFilterSet represents a collection of filters (AND logic).
type PropertyFilterSet struct {
	Filters []*PropertyFilter
}

// NewPropertyFilterSet creates a filter set from query strings.
func NewPropertyFilterSet(queries []string) (*PropertyFilterSet, error) {
	filters := make([]*PropertyFilter, 0, len(queries))

	for _, query := range queries {
		filter, err := ParseFilterQuery(query)
		if err != nil {
			return nil, err
		}

		filters = append(filters, filter)
	}

	return &PropertyFilterSet{
		Filters: filters,
	}, nil
}

// Matches checks if a resource matches ALL filters (AND logic).
func (fs *PropertyFilterSet) Matches(resource *state.Resource) bool {
	for _, filter := range fs.Filters {
		if !filter.Matches(resource) {
			return false
		}
	}

	return true
}

// FilterResources filters resources using the filter set.
func (fs *PropertyFilterSet) FilterResources(resources map[string]*state.Resource) map[string]*state.Resource {
	if len(fs.Filters) == 0 {
		return resources
	}

	filtered := make(map[string]*state.Resource)

	for key, resource := range resources {
		if fs.Matches(resource) {
			filtered[key] = resource
		}
	}

	return filtered
}
