package state

import (
	"fmt"
	"reflect"
)

// DiffType represents the type of change detected.
type DiffType string

const (
	// DiffTypeAdded indicates a resource exists in provider but not in state.
	DiffTypeAdded DiffType = "added"

	// DiffTypeModified indicates a resource exists in both but has changed.
	DiffTypeModified DiffType = "modified"

	// DiffTypeDeleted indicates a resource exists in state but not in provider.
	DiffTypeDeleted DiffType = "deleted"

	// DiffTypeUnchanged indicates a resource exists in both and hasn't changed.
	DiffTypeUnchanged DiffType = "unchanged"
)

// ResourceDiff represents the difference between state and discovered resources.
type ResourceDiff struct {
	// Type of change detected
	Type DiffType

	// ResourceType is the type of resource (network, instance, etc.)
	ResourceType string

	// ResourceID uniquely identifies the resource
	ResourceID string

	// ResourceName is the human-readable name
	ResourceName string

	// StateResource is the resource from state (nil for added resources)
	StateResource *Resource

	// DiscoveredResource is the resource from provider (nil for deleted resources)
	DiscoveredResource *Resource

	// PropertyChanges lists which properties changed (for modified resources)
	PropertyChanges []PropertyChange
}

// PropertyChange represents a single property that changed.
type PropertyChange struct {
	// Property name that changed
	Property string

	// OldValue from state
	OldValue interface{}

	// NewValue from provider
	NewValue interface{}
}

// DiffSet contains all differences between state and discovered resources.
type DiffSet struct {
	// Added resources (in provider, not in state)
	Added []*ResourceDiff

	// Modified resources (in both, but changed)
	Modified []*ResourceDiff

	// Deleted resources (in state, not in provider)
	Deleted []*ResourceDiff

	// Unchanged resources (in both, no changes)
	Unchanged []*ResourceDiff

	// TotalStateResources is the count of resources in state
	TotalStateResources int

	// TotalDiscoveredResources is the count of resources discovered
	TotalDiscoveredResources int
}

// Summary returns a human-readable summary of the diff set.
func (ds *DiffSet) Summary() string {
	return fmt.Sprintf(
		"Added: %d, Modified: %d, Deleted: %d, Unchanged: %d (State: %d, Discovered: %d)",
		len(ds.Added),
		len(ds.Modified),
		len(ds.Deleted),
		len(ds.Unchanged),
		ds.TotalStateResources,
		ds.TotalDiscoveredResources,
	)
}

// HasChanges returns true if there are any additions, modifications, or deletions.
func (ds *DiffSet) HasChanges() bool {
	return len(ds.Added) > 0 || len(ds.Modified) > 0 || len(ds.Deleted) > 0
}

// TotalChanges returns the total number of changes (added + modified + deleted).
func (ds *DiffSet) TotalChanges() int {
	return len(ds.Added) + len(ds.Modified) + len(ds.Deleted)
}

// compareProperties detects changes in resource properties.
func compareProperties(stateRes, discoveredRes *Resource) []PropertyChange {
	changes := make([]PropertyChange, 0)

	// Compare basic fields
	if stateRes.Name != discoveredRes.Name {
		changes = append(changes, PropertyChange{
			Property: "name",
			OldValue: stateRes.Name,
			NewValue: discoveredRes.Name,
		})
	}

	if stateRes.State != discoveredRes.State {
		changes = append(changes, PropertyChange{
			Property: "state",
			OldValue: stateRes.State,
			NewValue: discoveredRes.State,
		})
	}

	// Compare properties map
	// Find properties that changed or were added
	for key, newVal := range discoveredRes.Properties {
		oldVal, exists := stateRes.Properties[key]
		if !exists {
			changes = append(changes, PropertyChange{
				Property: "properties." + key,
				OldValue: nil,
				NewValue: newVal,
			})
		} else if !reflect.DeepEqual(oldVal, newVal) {
			changes = append(changes, PropertyChange{
				Property: "properties." + key,
				OldValue: oldVal,
				NewValue: newVal,
			})
		}
	}

	// Find properties that were removed
	for key, oldVal := range stateRes.Properties {
		if _, exists := discoveredRes.Properties[key]; !exists {
			changes = append(changes, PropertyChange{
				Property: "properties." + key,
				OldValue: oldVal,
				NewValue: nil,
			})
		}
	}

	// Compare tags
	if !reflect.DeepEqual(stateRes.Tags, discoveredRes.Tags) {
		changes = append(changes, PropertyChange{
			Property: "tags",
			OldValue: stateRes.Tags,
			NewValue: discoveredRes.Tags,
		})
	}

	return changes
}

// createDiff creates a ResourceDiff for a given change type.
func createDiff(diffType DiffType, stateRes, discoveredRes *Resource) *ResourceDiff {
	diff := &ResourceDiff{
		Type:               diffType,
		StateResource:      stateRes,
		DiscoveredResource: discoveredRes,
	}

	// Set resource identification from whichever resource is available
	if discoveredRes != nil {
		diff.ResourceType = discoveredRes.Type
		diff.ResourceID = discoveredRes.ID
		diff.ResourceName = discoveredRes.Name
	} else if stateRes != nil {
		diff.ResourceType = stateRes.Type
		diff.ResourceID = stateRes.ID
		diff.ResourceName = stateRes.Name
	}

	// For modified resources, detect property changes
	if diffType == DiffTypeModified && stateRes != nil && discoveredRes != nil {
		diff.PropertyChanges = compareProperties(stateRes, discoveredRes)
	}

	return diff
}

// CompareResources compares state resources with discovered resources and returns a DiffSet.
func CompareResources(stateResources map[string]*Resource, discoveredResources []*Resource) *DiffSet {
	diffSet := &DiffSet{
		Added:                    make([]*ResourceDiff, 0),
		Modified:                 make([]*ResourceDiff, 0),
		Deleted:                  make([]*ResourceDiff, 0),
		Unchanged:                make([]*ResourceDiff, 0),
		TotalStateResources:      len(stateResources),
		TotalDiscoveredResources: len(discoveredResources),
	}

	// Create index of discovered resources by ID for fast lookup
	discoveredIndex := make(map[string]*Resource)
	for _, res := range discoveredResources {
		discoveredIndex[res.ID] = res
	}

	// Track which discovered resources we've seen
	seenDiscovered := make(map[string]bool)

	// Compare state resources with discovered resources
	for stateID, stateRes := range stateResources {
		discoveredRes, exists := discoveredIndex[stateID]

		if !exists {
			// Resource in state but not discovered = deleted
			diff := createDiff(DiffTypeDeleted, stateRes, nil)
			diffSet.Deleted = append(diffSet.Deleted, diff)
		} else {
			// Resource exists in both - check if modified
			seenDiscovered[stateID] = true

			changes := compareProperties(stateRes, discoveredRes)
			if len(changes) > 0 {
				// Resource modified
				diff := createDiff(DiffTypeModified, stateRes, discoveredRes)
				diffSet.Modified = append(diffSet.Modified, diff)
			} else {
				// Resource unchanged
				diff := createDiff(DiffTypeUnchanged, stateRes, discoveredRes)
				diffSet.Unchanged = append(diffSet.Unchanged, diff)
			}
		}
	}

	// Find resources that are discovered but not in state = added
	for _, discoveredRes := range discoveredResources {
		if !seenDiscovered[discoveredRes.ID] {
			diff := createDiff(DiffTypeAdded, nil, discoveredRes)
			diffSet.Added = append(diffSet.Added, diff)
		}
	}

	return diffSet
}

// UpdateResourceFromDiscovered updates a state resource with discovered resource data.
func UpdateResourceFromDiscovered(stateRes *Resource, discoveredRes *Resource) {
	// Update basic fields
	stateRes.Name = discoveredRes.Name
	stateRes.State = discoveredRes.State
	stateRes.Provider = discoveredRes.Provider

	// Update properties (deep copy)
	stateRes.Properties = make(map[string]interface{})
	for k, v := range discoveredRes.Properties {
		stateRes.Properties[k] = v
	}

	// Update tags (deep copy)
	stateRes.Tags = make(map[string]string)
	for k, v := range discoveredRes.Tags {
		stateRes.Tags[k] = v
	}

	// Update timestamp
	stateRes.UpdatedAt = nowFn()
}
