package commands

import (
	"encoding/json"
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/state"
	"gopkg.in/yaml.v3"
)

const (
	// timestampDisplayLength is the length to truncate timestamps for table display (YYYY-MM-DDTHH:MM:SS).
	timestampDisplayLength = 19
)

// FormatStateJSON formats state as JSON with optional resource filtering.
func FormatStateJSON(currentState *state.State, filter *ResourceFilter) (string, error) {
	if currentState == nil {
		return "", ErrStateIsNil
	}

	// Create a copy of state for output
	outputState := &state.State{
		Version:      currentState.Version,
		BlocName:     currentState.BlocName,
		Provider:     currentState.Provider,
		Region:       currentState.Region,
		CreatedAt:    currentState.CreatedAt,
		UpdatedAt:    currentState.UpdatedAt,
		Outputs:      currentState.Outputs,
		Dependencies: currentState.Dependencies,
		Resources:    make(map[string]*state.Resource),
	}

	// Apply filtering if filter is provided
	if filter != nil {
		outputState.Resources = filter.FilterResources(currentState.Resources)
	} else {
		outputState.Resources = currentState.Resources
	}

	// Marshal to JSON with indentation
	jsonBytes, err := json.MarshalIndent(outputState, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal state to JSON: %w", err)
	}

	return string(jsonBytes), nil
}

// FormatStateYAML formats state as YAML with optional resource filtering.
func FormatStateYAML(currentState *state.State, filter *ResourceFilter) (string, error) {
	if currentState == nil {
		return "", ErrStateIsNil
	}

	// Create a copy of state for output
	outputState := &state.State{
		Version:      currentState.Version,
		BlocName:     currentState.BlocName,
		Provider:     currentState.Provider,
		Region:       currentState.Region,
		CreatedAt:    currentState.CreatedAt,
		UpdatedAt:    currentState.UpdatedAt,
		Outputs:      currentState.Outputs,
		Dependencies: currentState.Dependencies,
		Resources:    make(map[string]*state.Resource),
	}

	// Apply filtering if filter is provided
	if filter != nil {
		outputState.Resources = filter.FilterResources(currentState.Resources)
	} else {
		outputState.Resources = currentState.Resources
	}

	// Marshal to YAML
	yamlBytes, err := yaml.Marshal(outputState)
	if err != nil {
		return "", fmt.Errorf("failed to marshal state to YAML: %w", err)
	}

	return string(yamlBytes), nil
}

// FormatSyncResultJSON formats sync result as JSON.
func FormatSyncResultJSON(result interface{}) (string, error) {
	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal sync result to JSON: %w", err)
	}

	return string(jsonBytes), nil
}

// FormatSyncResultYAML formats sync result as YAML.
func FormatSyncResultYAML(result interface{}) (string, error) {
	yamlBytes, err := yaml.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("failed to marshal sync result to YAML: %w", err)
	}

	return string(yamlBytes), nil
}

// FormatMetadataColumn formats metadata tags for compact display in table columns.
// Returns a string representation showing key metadata fields: "bloc=X, managed-by=Y, ...".
// FormatMetadataColumn formats metadata tags for compact display in table columns.
// Returns a string representation showing key metadata fields: "bloc=X, managed-by=Y, ...".
// Note: Requires strict metadata format with hyphenated keys (managed-by, created-at).
// Resources without proper metadata will not be displayed.
func FormatMetadataColumn(tags map[string]string) string {
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
		// Truncate timestamp for table display
		if len(createdAt) > timestampDisplayLength {
			createdAt = createdAt[:timestampDisplayLength]
		}

		result += "created-at=" + createdAt
	}

	if result == "" {
		return "-"
	}

	return result
}
