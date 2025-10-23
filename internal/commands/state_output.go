package commands

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
)

const (
	// Truncation lengths for state output columns.
	outputMaxIDLength   = 40
	outputMaxTypeLength = 25
	outputMaxNameLength = 50
)

// buildStateSummary builds the summary section for state output.
func buildStateSummary(currentState *state.State) string {
	summary := "Provider:     " + currentState.Provider
	if currentState.Region != "" {
		summary += "\nRegion:       " + currentState.Region
	}

	if !currentState.CreatedAt.IsZero() {
		summary += "\nCreated:      " + currentState.CreatedAt.Format(time.RFC3339)
	}

	if !currentState.UpdatedAt.IsZero() {
		summary += "\nLast Updated: " + currentState.UpdatedAt.Format(time.RFC3339)
	}

	return summary
}

// addCategorySectionToTable adds a category section with resources to the table.
func addCategorySectionToTable(table *ui.Table, category state.ResourceCategory, resources []*state.Resource) {
	// Sort resources by type and then by name
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Type != resources[j].Type {
			return resources[i].Type < resources[j].Type
		}

		return resources[i].Name < resources[j].Name
	})

	// Create section for this category
	sectionTitle := fmt.Sprintf("%s Resources (%d)", categoryName(category), len(resources))
	table.AddSection(sectionTitle)

	// Get current section and set headers
	currentSection := &table.Sections[len(table.Sections)-1]
	currentSection.Headers = []string{"ID", "Type", "Name", "State"}

	// Add resource rows
	for _, resource := range resources {
		resourceID := truncate(resource.ID, outputMaxIDLength)
		resType := truncate(resource.Type, outputMaxTypeLength)
		name := truncate(resource.Name, outputMaxNameLength)
		resState := getResourceState(resource)

		currentSection.Rows = append(currentSection.Rows, []string{
			resourceID, resType, name, resState,
		})
	}
}

// FormatStateOutput formats the state for display in table format.
func FormatStateOutput(currentState *state.State) *ui.Table {
	// Create table with title
	title := "State for Bloc: " + currentState.BlocName
	table := ui.NewTable(title)

	// Build and set summary section
	table.Summary = buildStateSummary(currentState)

	// Group resources by category
	resourcesByCategory := groupResourcesByCategory(currentState.Resources)

	// Display each category
	categories := []state.ResourceCategory{
		state.CategoryNetwork,
		state.CategoryCompute,
		state.CategoryStorage,
	}

	totalResources := 0

	for _, category := range categories {
		resources, exists := resourcesByCategory[category]
		if !exists || len(resources) == 0 {
			continue
		}

		totalResources += len(resources)
		addCategorySectionToTable(table, category, resources)
	}

	// Add summary section at the end (always add, even if 0 resources)
	table.AddSection(fmt.Sprintf("Total Resources: %d", totalResources))

	return table
}

// groupResourcesByCategory groups resources by their category.
func groupResourcesByCategory(resources map[string]*state.Resource) map[state.ResourceCategory][]*state.Resource {
	grouped := make(map[state.ResourceCategory][]*state.Resource)

	for _, resource := range resources {
		info, ok := state.GetResourceTypeInfo(resource.Type)
		if !ok {
			// Unknown resource type, skip or put in a default category
			continue
		}

		grouped[info.Category] = append(grouped[info.Category], resource)
	}

	return grouped
}

// categoryName returns a human-readable name for a category.
func categoryName(category state.ResourceCategory) string {
	switch category {
	case state.CategoryNetwork:
		return "Network"
	case state.CategoryCompute:
		return "Compute"
	case state.CategoryStorage:
		return "Storage"
	default:
		return "Other"
	}
}

// FormatStateOutputFiltered formats the state for display with resource filtering.
func FormatStateOutputFiltered(currentState *state.State, filter *ResourceFilter) *ui.Table {
	// Create table with title
	title := "State for Bloc: " + currentState.BlocName
	table := ui.NewTable(title)

	// Build summary section
	summary := buildBlocSummary(currentState)
	table.Summary = summary

	// Filter resources
	filteredResources := filter.FilterResources(currentState.Resources)

	// Group resources by display category
	grouped := filter.GroupByDisplayCategory(filteredResources)

	// Get sorted display categories
	categories := GetSortedDisplayCategories(grouped)

	// Create section for each category
	totalResources := 0

	for _, category := range categories {
		resources := grouped[category]
		if len(resources) == 0 {
			continue
		}

		totalResources += len(resources)

		// Sort resources within category by name
		sort.Slice(resources, func(i, j int) bool {
			return resources[i].Name < resources[j].Name
		})

		// Create section with emoji heading
		heading := GetDisplayHeading(category, len(resources))
		table.AddSection(heading)

		// Get current section and set headers
		currentSection := &table.Sections[len(table.Sections)-1]
		currentSection.Headers = GetHeadersForCategory(category)

		// Add resource rows
		for _, resource := range resources {
			row := FormatResourceRow(resource, category)
			currentSection.Rows = append(currentSection.Rows, row)
		}
	}

	// Add summary section at the end
	table.AddSection(fmt.Sprintf("Total Resources: %d", totalResources))

	return table
}

// buildBlocSummary creates the summary section for a bloc's state.
func buildBlocSummary(currentState *state.State) string {
	var parts []string

	parts = append(parts, "Provider:     "+currentState.Provider)

	if currentState.Region != "" {
		parts = append(parts, "Region:       "+currentState.Region)
	}

	if !currentState.CreatedAt.IsZero() {
		parts = append(parts, "Created:      "+currentState.CreatedAt.Format(time.RFC3339))
	}

	if !currentState.UpdatedAt.IsZero() {
		parts = append(parts, "Last Updated: "+currentState.UpdatedAt.Format(time.RFC3339))
	}

	return strings.Join(parts, "\n")
}
