package commands

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/state"
	"gopkg.in/yaml.v3"
)

const (
	// syncOutputMaxIDLength is the truncation length for resource IDs in sync output.
	syncOutputMaxIDLength = 40
)

// SyncOutput contains the formatted output for sync results.
type SyncOutput struct {
	Summary  SyncSummary            `json:"summary"           yaml:"summary"`
	Changes  *SyncChanges           `json:"changes,omitempty" yaml:"changes,omitempty"`
	Errors   []string               `json:"errors,omitempty"  yaml:"errors,omitempty"`
	Metadata map[string]interface{} `json:"metadata"          yaml:"metadata"`
}

// SyncSummary contains high-level statistics.
type SyncSummary struct {
	BlocName           string `json:"blocName"           yaml:"blocName"`
	Strategy           string `json:"strategy"           yaml:"strategy"`
	DryRun             bool   `json:"dryRun"             yaml:"dryRun"`
	TotalDiscovered    int    `json:"totalDiscovered"    yaml:"totalDiscovered"`
	ResourcesAdded     int    `json:"resourcesAdded"     yaml:"resourcesAdded"`
	ResourcesUpdated   int    `json:"resourcesUpdated"   yaml:"resourcesUpdated"`
	ResourcesRemoved   int    `json:"resourcesRemoved"   yaml:"resourcesRemoved"`
	ResourcesUnchanged int    `json:"resourcesUnchanged" yaml:"resourcesUnchanged"`
	Duration           string `json:"duration"           yaml:"duration"`
}

// SyncChanges contains detailed change information.
type SyncChanges struct {
	Added    []ResourceChange `json:"added,omitempty"    yaml:"added,omitempty"`
	Modified []ResourceChange `json:"modified,omitempty" yaml:"modified,omitempty"`
	Removed  []ResourceChange `json:"removed,omitempty"  yaml:"removed,omitempty"`
}

// ResourceChange represents a single resource change.
type ResourceChange struct {
	ResourceType string           `json:"resourceType"      yaml:"resourceType"`
	ResourceID   string           `json:"resourceId"        yaml:"resourceId"`
	ResourceName string           `json:"resourceName"      yaml:"resourceName"`
	Changes      []PropertyChange `json:"changes,omitempty" yaml:"changes,omitempty"`
}

// PropertyChange represents a single property change.
type PropertyChange struct {
	Property string      `json:"property"           yaml:"property"`
	OldValue interface{} `json:"oldValue,omitempty" yaml:"oldValue,omitempty"`
	NewValue interface{} `json:"newValue,omitempty" yaml:"newValue,omitempty"`
}

// FormatSyncOutput formats the reconciliation result based on the output format.
func FormatSyncOutput(result *state.ReconcileResult, diffSet *state.DiffSet, blocName, strategy string, dryRun bool, format string) (string, error) {
	output := buildSyncOutput(result, diffSet, blocName, strategy, dryRun)

	switch format {
	case OutputJSON:
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to marshal JSON: %w", err)
		}

		return string(data), nil

	case OutputFormatYAML:
		data, err := yaml.Marshal(output)
		if err != nil {
			return "", fmt.Errorf("failed to marshal YAML: %w", err)
		}

		return string(data), nil

	case OutputTable:
		return formatTableOutput(output), nil

	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedOutputFormat, format)
	}
}

// FormatSyncOutputFiltered formats the reconciliation result with resource filtering.
func FormatSyncOutputFiltered(result *state.ReconcileResult, diffSet *state.DiffSet, blocName, strategy string, dryRun bool, format string, filter *ResourceFilter) (string, error) {
	// Build the output first
	output := buildSyncOutput(result, diffSet, blocName, strategy, dryRun)

	// Apply filtering to the changes
	if filter != nil && output.Changes != nil {
		output.Changes.Added = filterResourceChanges(output.Changes.Added, filter)
		output.Changes.Modified = filterResourceChanges(output.Changes.Modified, filter)
		output.Changes.Removed = filterResourceChanges(output.Changes.Removed, filter)

		// Recalculate summary counts based on filtered changes
		output.Summary.ResourcesAdded = len(output.Changes.Added)
		output.Summary.ResourcesUpdated = len(output.Changes.Modified)
		output.Summary.ResourcesRemoved = len(output.Changes.Removed)

		// Note: TotalDiscovered and ResourcesUnchanged remain from full discovery
		// as we want to show what was found even if not displayed
	}

	switch format {
	case OutputJSON:
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to marshal JSON: %w", err)
		}

		return string(data), nil

	case OutputFormatYAML:
		data, err := yaml.Marshal(output)
		if err != nil {
			return "", fmt.Errorf("failed to marshal YAML: %w", err)
		}

		return string(data), nil

	case OutputTable:
		return formatTableOutputFiltered(output, filter), nil

	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedOutputFormat, format)
	}
}

// filterResourceChanges filters resource changes based on the filter.
func filterResourceChanges(changes []ResourceChange, filter *ResourceFilter) []ResourceChange {
	if filter == nil {
		return changes
	}

	filtered := make([]ResourceChange, 0, len(changes))
	for _, change := range changes {
		if filter.ShouldDisplay(change.ResourceType) {
			filtered = append(filtered, change)
		}
	}

	return filtered
}

// formatTableOutputFiltered formats the output as a table with emoji icons per resource type.
func formatTableOutputFiltered(output *SyncOutput, filter *ResourceFilter) string {
	var builder strings.Builder

	writeOutputHeader(&builder, output, filter)
	writeOutputStatistics(&builder, output)
	writeDetailedChanges(&builder, output)
	writeOutputErrors(&builder, output)
	writeOutputFooter(&builder, output)

	return builder.String()
}

// writeOutputHeader writes the reconciliation summary header.
func writeOutputHeader(builder *strings.Builder, output *SyncOutput, filter *ResourceFilter) {
	builder.WriteString("\n")
	builder.WriteString("=== Reconciliation Summary ===\n")
	fmt.Fprintf(builder, "Bloc:               %s\n", output.Summary.BlocName)
	fmt.Fprintf(builder, "Strategy:           %s\n", output.Summary.Strategy)

	if output.Summary.DryRun {
		builder.WriteString("Mode:               DRY RUN (no changes will be made)\n")
	}

	writeFilterInfo(builder, filter)
	builder.WriteString("\n")
}

// writeFilterInfo writes active filter information.
func writeFilterInfo(builder *strings.Builder, filter *ResourceFilter) {
	if filter == nil || filter.flags.All {
		return
	}

	builder.WriteString("Filter:             ")

	activeFlags := []string{}

	for flagName, enabled := range filter.flags.GetEnabledFlags() {
		if enabled {
			config := GetDisplayConfig(flagName)
			activeFlags = append(activeFlags, config.DisplayName)
		}
	}

	builder.WriteString(strings.Join(activeFlags, ", "))
	builder.WriteString("\n")
}

// writeOutputStatistics writes reconciliation statistics.
func writeOutputStatistics(builder *strings.Builder, output *SyncOutput) {
	fmt.Fprintf(builder, "Resources discovered: %d\n", output.Summary.TotalDiscovered)
	fmt.Fprintf(builder, "Resources added:      %d\n", output.Summary.ResourcesAdded)
	fmt.Fprintf(builder, "Resources updated:    %d\n", output.Summary.ResourcesUpdated)
	fmt.Fprintf(builder, "Resources removed:    %d\n", output.Summary.ResourcesRemoved)
	fmt.Fprintf(builder, "Resources unchanged:  %d\n", output.Summary.ResourcesUnchanged)
	fmt.Fprintf(builder, "Duration:             %s\n", output.Summary.Duration)
}

// writeDetailedChanges writes detailed change information grouped by resource type.
func writeDetailedChanges(builder *strings.Builder, output *SyncOutput) {
	if output.Changes == nil {
		return
	}

	writeAddedResources(builder, output.Changes.Added)
	writeModifiedResources(builder, output.Changes.Modified)
	writeRemovedResources(builder, output.Changes.Removed)
}

// writeAddedResources writes added resources section.
func writeAddedResources(builder *strings.Builder, added []ResourceChange) {
	if len(added) == 0 {
		return
	}

	builder.WriteString("\n=== Added Resources ===\n")

	groupedAdded := groupChangesByFlag(added)

	for _, flagName := range getSortedFlagNames(groupedAdded) {
		changes := groupedAdded[flagName]
		config := GetDisplayConfig(flagName)
		fmt.Fprintf(builder, "\n%s %s (%d):\n", config.Emoji, config.DisplayName, len(changes))

		for _, change := range changes {
			fmt.Fprintf(builder, "  + %s (%s)\n",
				change.ResourceName,
				truncate(change.ResourceID, syncOutputMaxIDLength))
		}
	}
}

// writeModifiedResources writes modified resources section.
func writeModifiedResources(builder *strings.Builder, modified []ResourceChange) {
	if len(modified) == 0 {
		return
	}

	builder.WriteString("\n=== Modified Resources ===\n")

	groupedModified := groupChangesByFlag(modified)

	for _, flagName := range getSortedFlagNames(groupedModified) {
		changes := groupedModified[flagName]
		config := GetDisplayConfig(flagName)
		fmt.Fprintf(builder, "\n%s %s (%d):\n", config.Emoji, config.DisplayName, len(changes))

		for _, change := range changes {
			fmt.Fprintf(builder, "  ~ %s (%s)\n",
				change.ResourceName,
				truncate(change.ResourceID, syncOutputMaxIDLength))

			for _, pc := range change.Changes {
				fmt.Fprintf(builder, "      %s: %v → %v\n",
					pc.Property,
					formatValue(pc.OldValue),
					formatValue(pc.NewValue))
			}
		}
	}
}

// writeRemovedResources writes removed resources section.
func writeRemovedResources(builder *strings.Builder, removed []ResourceChange) {
	if len(removed) == 0 {
		return
	}

	builder.WriteString("\n=== Removed Resources ===\n")

	groupedRemoved := groupChangesByFlag(removed)

	for _, flagName := range getSortedFlagNames(groupedRemoved) {
		changes := groupedRemoved[flagName]
		config := GetDisplayConfig(flagName)
		fmt.Fprintf(builder, "\n%s %s (%d):\n", config.Emoji, config.DisplayName, len(changes))

		for _, change := range changes {
			fmt.Fprintf(builder, "  - %s (%s)\n",
				change.ResourceName,
				truncate(change.ResourceID, syncOutputMaxIDLength))
		}
	}
}

// writeOutputErrors writes error section if any errors exist.
func writeOutputErrors(builder *strings.Builder, output *SyncOutput) {
	if len(output.Errors) == 0 {
		return
	}

	fmt.Fprintf(builder, "\n=== Errors (%d) ===\n", len(output.Errors))

	for i, err := range output.Errors {
		fmt.Fprintf(builder, "  %d. %s\n", i+1, err)
	}
}

// writeOutputFooter writes the output footer.
func writeOutputFooter(builder *strings.Builder, output *SyncOutput) {
	if output.Summary.DryRun {
		builder.WriteString("\nDRY RUN - No changes were made to state file\n")
	}
}

// groupChangesByFlag groups resource changes by their display flag.
func groupChangesByFlag(changes []ResourceChange) map[string][]ResourceChange {
	grouped := make(map[string][]ResourceChange)

	for _, change := range changes {
		flagName := GetFlagForResourceType(change.ResourceType)
		if flagName == "" {
			// Unknown resource type
			continue
		}

		grouped[flagName] = append(grouped[flagName], change)
	}

	return grouped
}

// getSortedFlagNames returns sorted flag names based on display order.
func getSortedFlagNames(grouped map[string][]ResourceChange) []string {
	flagNames := make([]string, 0, len(grouped))
	for flagName := range grouped {
		flagNames = append(flagNames, flagName)
	}

	sort.Slice(flagNames, func(i, j int) bool {
		configI := GetDisplayConfig(flagNames[i])
		configJ := GetDisplayConfig(flagNames[j])

		return configI.SortOrder < configJ.SortOrder
	})

	return flagNames
}

// buildSyncOutput constructs the SyncOutput structure from the result.
func buildSyncOutput(result *state.ReconcileResult, diffSet *state.DiffSet, blocName, strategy string, dryRun bool) *SyncOutput {
	output := &SyncOutput{
		Summary: SyncSummary{
			BlocName:           blocName,
			Strategy:           strategy,
			DryRun:             dryRun,
			TotalDiscovered:    result.TotalDiscovered,
			ResourcesAdded:     result.ResourcesAdded,
			ResourcesUpdated:   result.ResourcesUpdated,
			ResourcesRemoved:   result.ResourcesRemoved,
			ResourcesUnchanged: result.ResourcesUnchanged,
			Duration:           result.Duration.String(),
		},
		Metadata: map[string]interface{}{
			"version": "1.0",
		},
	}

	// Add errors if any
	if len(result.Errors) > 0 {
		output.Errors = make([]string, len(result.Errors))
		for i, err := range result.Errors {
			output.Errors[i] = err.Error()
		}
	}

	// Add detailed changes if diffSet is provided
	if diffSet != nil && (len(diffSet.Added) > 0 || len(diffSet.Modified) > 0 || len(diffSet.Deleted) > 0) {
		output.Changes = &SyncChanges{
			Added:    convertDiffToChanges(diffSet.Added),
			Modified: convertDiffToChanges(diffSet.Modified),
			Removed:  convertDiffToChanges(diffSet.Deleted),
		}
	}

	return output
}

// convertDiffToChanges converts DiffSet entries to ResourceChange entries.
func convertDiffToChanges(diffs []*state.ResourceDiff) []ResourceChange {
	changes := make([]ResourceChange, len(diffs))

	for diffIndex, diff := range diffs {
		change := ResourceChange{
			ResourceType: diff.ResourceType,
			ResourceID:   diff.ResourceID,
			ResourceName: diff.ResourceName,
		}

		// Add property changes for modified resources
		if len(diff.PropertyChanges) > 0 {
			change.Changes = make([]PropertyChange, len(diff.PropertyChanges))
			for j, pc := range diff.PropertyChanges {
				change.Changes[j] = PropertyChange{
					Property: pc.Property,
					OldValue: pc.OldValue,
					NewValue: pc.NewValue,
				}
			}
		}

		changes[diffIndex] = change
	}

	// Sort by resource type, then by ID
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].ResourceType != changes[j].ResourceType {
			return changes[i].ResourceType < changes[j].ResourceType
		}

		return changes[i].ResourceID < changes[j].ResourceID
	})

	return changes
}

// formatTableOutput formats the output as a human-readable table.
func formatTableOutput(output *SyncOutput) string {
	var builder strings.Builder

	// Header
	builder.WriteString("\n")
	builder.WriteString("=== Reconciliation Summary ===\n")
	builder.WriteString(fmt.Sprintf("Bloc:               %s\n", output.Summary.BlocName))
	builder.WriteString(fmt.Sprintf("Strategy:           %s\n", output.Summary.Strategy))

	if output.Summary.DryRun {
		builder.WriteString("Mode:               DRY RUN (no changes will be made)\n")
	}

	builder.WriteString("\n")

	// Statistics
	builder.WriteString(fmt.Sprintf("Resources discovered: %d\n", output.Summary.TotalDiscovered))
	builder.WriteString(fmt.Sprintf("Resources added:      %d\n", output.Summary.ResourcesAdded))
	builder.WriteString(fmt.Sprintf("Resources updated:    %d\n", output.Summary.ResourcesUpdated))
	builder.WriteString(fmt.Sprintf("Resources removed:    %d\n", output.Summary.ResourcesRemoved))
	builder.WriteString(fmt.Sprintf("Resources unchanged:  %d\n", output.Summary.ResourcesUnchanged))
	builder.WriteString(fmt.Sprintf("Duration:             %s\n", output.Summary.Duration))

	// Detailed changes
	if output.Changes != nil {
		// Added resources
		if len(output.Changes.Added) > 0 {
			builder.WriteString("\n=== Added Resources ===\n")

			for _, change := range output.Changes.Added {
				builder.WriteString(fmt.Sprintf("  + %s: %s (%s)\n",
					change.ResourceType,
					change.ResourceName,
					change.ResourceID))
			}
		}

		// Modified resources
		if len(output.Changes.Modified) > 0 {
			builder.WriteString("\n=== Modified Resources ===\n")

			for _, change := range output.Changes.Modified {
				builder.WriteString(fmt.Sprintf("  ~ %s: %s (%s)\n",
					change.ResourceType,
					change.ResourceName,
					change.ResourceID))

				for _, pc := range change.Changes {
					builder.WriteString(fmt.Sprintf("      %s: %v → %v\n",
						pc.Property,
						formatValue(pc.OldValue),
						formatValue(pc.NewValue)))
				}
			}
		}

		// Removed resources
		if len(output.Changes.Removed) > 0 {
			builder.WriteString("\n=== Removed Resources ===\n")

			for _, change := range output.Changes.Removed {
				builder.WriteString(fmt.Sprintf("  - %s: %s (%s)\n",
					change.ResourceType,
					change.ResourceName,
					change.ResourceID))
			}
		}
	}

	// Errors
	if len(output.Errors) > 0 {
		builder.WriteString(fmt.Sprintf("\n=== Errors (%d) ===\n", len(output.Errors)))

		for i, err := range output.Errors {
			builder.WriteString(fmt.Sprintf("  %d. %s\n", i+1, err))
		}
	}

	// Footer
	if output.Summary.DryRun {
		builder.WriteString("\nDRY RUN - No changes were made to state file\n")
	}

	return builder.String()
}

// formatValue formats a value for display.
func formatValue(value interface{}) string {
	if value == nil {
		return "<nil>"
	}

	switch typedValue := value.(type) {
	case string:
		return fmt.Sprintf("%q", typedValue)
	case map[string]string:
		if len(typedValue) == 0 {
			return "{}"
		}

		parts := make([]string, 0, len(typedValue))
		for k, val := range typedValue {
			parts = append(parts, fmt.Sprintf("%s=%q", k, val))
		}

		sort.Strings(parts)

		return "{" + strings.Join(parts, ", ") + "}"
	case map[string]interface{}:
		if len(typedValue) == 0 {
			return "{}"
		}

		return fmt.Sprintf("{%d fields}", len(typedValue))
	default:
		return fmt.Sprintf("%v", typedValue)
	}
}

// GroupChangesByCategory groups resource changes by their category.
func GroupChangesByCategory(changes []ResourceChange) map[state.ResourceCategory][]ResourceChange {
	grouped := make(map[state.ResourceCategory][]ResourceChange)

	for _, change := range changes {
		info, ok := state.GetResourceTypeInfo(change.ResourceType)
		if !ok {
			// Unknown resource type, group under "unknown"
			continue
		}

		grouped[info.Category] = append(grouped[info.Category], change)
	}

	return grouped
}
