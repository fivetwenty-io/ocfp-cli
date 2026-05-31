package state

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// MergeOptions configures how resources are merged into state.
type MergeOptions struct {
	// Strategy determines which changes to apply
	Strategy MergeStrategy

	// PreserveDeleted keeps resources in state even if deleted from provider
	PreserveDeleted bool

	// UpdateTimestamps controls whether to update resource timestamps
	UpdateTimestamps bool
}

// MergeResult contains the outcome of a merge operation.
type MergeResult struct {
	// ResourcesAdded is the count of new resources added to state
	ResourcesAdded int

	// ResourcesUpdated is the count of resources updated in state
	ResourcesUpdated int

	// ResourcesDeleted is the count of resources removed from state
	ResourcesDeleted int

	// ResourcesSkipped is the count of resources that weren't merged due to strategy
	ResourcesSkipped int

	// Errors contains any errors encountered during merge
	Errors []error
}

// MergeResources applies a DiffSet to a State based on the merge strategy.
// This is the core state reconciliation logic that updates the state file.
func MergeResources(currentState *State, diffSet *DiffSet, opts MergeOptions) (*MergeResult, error) {
	if currentState == nil {
		return nil, ErrCurrentStateNil
	}

	if diffSet == nil {
		return nil, ErrDiffSetNil
	}

	// Initialize resources map if needed
	if currentState.Resources == nil {
		currentState.Resources = make(map[string]*Resource)
	}

	logger.Debugf("Starting merge with strategy: %s", opts.Strategy)
	logger.Debugf("Diff set: %s", diffSet.Summary())

	// Apply changes based on strategy
	var result *MergeResult

	switch opts.Strategy {
	case MergeStrategyAddOnly:
		result = mergeAddOnly(currentState, diffSet, opts)
	case MergeStrategyUpdate:
		result = mergeUpdate(currentState, diffSet, opts)
	case MergeStrategyFull:
		result = mergeFull(currentState, diffSet, opts)
	default:
		return nil, fmt.Errorf("%w: %v", ErrUnknownMergeStrategy, opts.Strategy)
	}

	// Update state metadata
	currentState.UpdatedAt = nowFn()

	logger.Infof("Merge complete: added=%d, updated=%d, deleted=%d, skipped=%d",
		result.ResourcesAdded, result.ResourcesUpdated, result.ResourcesDeleted, result.ResourcesSkipped)

	return result, nil
}

// mergeAddOnly only adds new resources, doesn't update or delete existing ones.
func mergeAddOnly(currentState *State, diffSet *DiffSet, opts MergeOptions) *MergeResult {
	result := &MergeResult{Errors: make([]error, 0)}

	logger.Debug("Applying add-only merge strategy")

	// Add new resources
	for _, diff := range diffSet.Added {
		if diff.DiscoveredResource == nil {
			logger.Warnf("Skipping added resource %s: no discovered resource data", diff.ResourceID)

			result.ResourcesSkipped++

			continue
		}

		// Add to state
		resource := copyResource(diff.DiscoveredResource)

		if opts.UpdateTimestamps {
			now := nowFn()
			resource.CreatedAt = now
			resource.UpdatedAt = now
		}

		currentState.Resources[resource.ID] = resource
		result.ResourcesAdded++

		logger.Debugf("Added resource %s (%s)", resource.ID, resource.Type)
	}

	// Skip modified and deleted resources
	result.ResourcesSkipped += len(diffSet.Modified) + len(diffSet.Deleted)
	logger.Debugf("Skipped %d modified and %d deleted resources (add-only strategy)",
		len(diffSet.Modified), len(diffSet.Deleted))

	return result
}

// mergeUpdate adds new resources and updates existing ones, but doesn't delete.
func mergeUpdate(currentState *State, diffSet *DiffSet, opts MergeOptions) *MergeResult {
	result := &MergeResult{Errors: make([]error, 0)}

	logger.Debug("Applying update merge strategy")

	// Add new resources
	mergeAddedResources(currentState, diffSet.Added, opts, result)

	// Update modified resources
	mergeModifiedResources(currentState, diffSet.Modified, opts, result)

	// Skip deleted resources
	if len(diffSet.Deleted) > 0 {
		result.ResourcesSkipped += len(diffSet.Deleted)
		logger.Debugf("Skipped %d deleted resources (update strategy preserves deleted)", len(diffSet.Deleted))
	}

	return result
}

// mergeFull applies all changes: adds new, updates existing, deletes removed.
func mergeFull(currentState *State, diffSet *DiffSet, opts MergeOptions) *MergeResult {
	result := &MergeResult{Errors: make([]error, 0)}

	logger.Debug("Applying full merge strategy")

	// Add new resources
	mergeAddedResources(currentState, diffSet.Added, opts, result)

	// Update modified resources
	mergeModifiedResources(currentState, diffSet.Modified, opts, result)

	// Delete removed resources (unless PreserveDeleted is set)
	mergeDeletedResources(currentState, diffSet.Deleted, opts, result)

	return result
}

// mergeAddedResources adds new resources to the current state.
func mergeAddedResources(currentState *State, added []*ResourceDiff, opts MergeOptions, result *MergeResult) {
	for _, diff := range added {
		if diff.DiscoveredResource == nil {
			logger.Warnf("Skipping added resource %s: no discovered resource data", diff.ResourceID)

			result.ResourcesSkipped++

			continue
		}

		resource := copyResource(diff.DiscoveredResource)

		if opts.UpdateTimestamps {
			now := nowFn()
			resource.CreatedAt = now
			resource.UpdatedAt = now
		}

		currentState.Resources[resource.ID] = resource
		result.ResourcesAdded++

		logger.Debugf("Added resource %s (%s)", resource.ID, resource.Type)
	}
}

// mergeModifiedResources updates existing resources in the current state.
func mergeModifiedResources(currentState *State, modified []*ResourceDiff, opts MergeOptions, result *MergeResult) {
	for _, diff := range modified {
		if diff.DiscoveredResource == nil || diff.StateResource == nil {
			logger.Warnf("Skipping modified resource %s: missing resource data", diff.ResourceID)

			result.ResourcesSkipped++

			continue
		}

		// Get existing resource from state
		stateResource, exists := currentState.Resources[diff.ResourceID]
		if !exists {
			logger.Warnf("Modified resource %s not found in state, adding instead", diff.ResourceID)
			resource := copyResource(diff.DiscoveredResource)

			if opts.UpdateTimestamps {
				now := nowFn()
				resource.CreatedAt = now
				resource.UpdatedAt = now
			}

			currentState.Resources[resource.ID] = resource
			result.ResourcesAdded++

			continue
		}

		// Update the resource with discovered data
		UpdateResourceFromDiscovered(stateResource, diff.DiscoveredResource)

		if opts.UpdateTimestamps {
			stateResource.UpdatedAt = nowFn()
		}

		result.ResourcesUpdated++

		logger.Debugf("Updated resource %s (%s) with %d property changes",
			diff.ResourceID, diff.ResourceType, len(diff.PropertyChanges))
	}
}

// mergeDeletedResources removes deleted resources from the current state.
func mergeDeletedResources(currentState *State, deleted []*ResourceDiff, opts MergeOptions, result *MergeResult) {
	if !opts.PreserveDeleted {
		for _, diff := range deleted {
			if _, exists := currentState.Resources[diff.ResourceID]; exists {
				delete(currentState.Resources, diff.ResourceID)

				result.ResourcesDeleted++

				logger.Debugf("Deleted resource %s (%s) from state", diff.ResourceID, diff.ResourceType)
			}
		}
	} else {
		result.ResourcesSkipped += len(deleted)
		logger.Debugf("Preserved %d deleted resources (PreserveDeleted=true)", len(deleted))
	}
}

// copyResource creates a deep copy of a resource to avoid mutation issues.
func copyResource(src *Resource) *Resource {
	if src == nil {
		return nil
	}

	dst := &Resource{
		ID:         src.ID,
		Type:       src.Type,
		Name:       src.Name,
		Provider:   src.Provider,
		State:      src.State,
		CreatedAt:  src.CreatedAt,
		UpdatedAt:  src.UpdatedAt,
		Properties: make(map[string]interface{}),
		Tags:       make(map[string]string),
	}

	// Deep copy properties
	for k, v := range src.Properties {
		dst.Properties[k] = v
	}

	// Deep copy tags
	for k, v := range src.Tags {
		dst.Tags[k] = v
	}

	return dst
}
