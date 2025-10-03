package commands

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// SearchOptions holds search configuration.
type SearchOptions struct {
	Query         string // Search query
	CaseSensitive bool   // Case-sensitive search
	UseRegex      bool   // Use regex instead of substring matching
	compiledRegex *regexp.Regexp
}

// NewSearchOptions creates search options from a query string.
func NewSearchOptions(query string, caseSensitive bool) (*SearchOptions, error) {
	if query == "" {
		return nil, nil
	}

	opts := &SearchOptions{
		Query:         query,
		CaseSensitive: caseSensitive,
		UseRegex:      false,
	}

	// Auto-detect regex patterns (contains special regex characters)
	if containsRegexChars(query) {
		opts.UseRegex = true

		// Compile regex
		pattern := query
		if !caseSensitive {
			pattern = "(?i)" + pattern
		}

		compiled, err := regexp.Compile(pattern)
		if err != nil {
			// If regex compilation fails, fall back to substring matching
			opts.UseRegex = false
		} else {
			opts.compiledRegex = compiled
		}
	}

	return opts, nil
}

// containsRegexChars checks if a string contains regex special characters.
func containsRegexChars(s string) bool {
	regexChars := `.+*?[]{}()^$|\\`

	return strings.ContainsAny(s, regexChars)
}

// SearchResources filters resources based on search criteria.
// Searches across:
// - Resource ID
// - Resource Name
// - Resource Type
// - Resource State
// - Property values (serialized)
// - Tag values.
func SearchResources(resources map[string]*state.Resource, opts *SearchOptions) map[string]*state.Resource {
	if opts == nil || opts.Query == "" {
		return resources
	}

	filtered := make(map[string]*state.Resource)

	for key, resource := range resources {
		if opts.Matches(resource) {
			filtered[key] = resource
		}
	}

	return filtered
}

// Matches checks if a resource matches the search criteria.
func (opts *SearchOptions) Matches(resource *state.Resource) bool {
	// Search in direct fields
	if opts.matchesString(resource.ID) {
		return true
	}

	if opts.matchesString(resource.Name) {
		return true
	}

	if opts.matchesString(resource.Type) {
		return true
	}

	if opts.matchesString(resource.State) {
		return true
	}

	if opts.matchesString(resource.Provider) {
		return true
	}

	// Search in tags
	if resource.Tags != nil {
		for key, value := range resource.Tags {
			if opts.matchesString(key) || opts.matchesString(value) {
				return true
			}
		}
	}

	// Search in properties (serialize to JSON and search)
	if resource.Properties != nil {
		if opts.matchesProperties(resource.Properties) {
			return true
		}
	}

	return false
}

// matchesString checks if a string matches the search query.
func (opts *SearchOptions) matchesString(str string) bool {
	if opts.UseRegex && opts.compiledRegex != nil {
		return opts.compiledRegex.MatchString(str)
	}

	// Substring matching
	query := opts.Query
	target := str

	if !opts.CaseSensitive {
		query = strings.ToLower(query)
		target = strings.ToLower(target)
	}

	return strings.Contains(target, query)
}

// matchesProperties checks if any property value matches the search query.
func (opts *SearchOptions) matchesProperties(props map[string]interface{}) bool {
	// Serialize properties to JSON for searching
	jsonBytes, err := json.Marshal(props)
	if err != nil {
		return false
	}

	jsonStr := string(jsonBytes)

	// Search in serialized JSON
	if opts.matchesString(jsonStr) {
		return true
	}

	// Also search property keys and string values directly
	for key, value := range props {
		if opts.matchesString(key) {
			return true
		}

		// Handle different value types
		switch typedValue := value.(type) {
		case string:
			if opts.matchesString(typedValue) {
				return true
			}
		case map[string]interface{}:
			// Recursively search nested maps
			if opts.matchesProperties(typedValue) {
				return true
			}
		}
	}

	return false
}
