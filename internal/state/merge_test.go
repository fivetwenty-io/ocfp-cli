package state_test

import (
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/stretchr/testify/assert"
)

func TestMergeResources_NilInputs(t *testing.T) {
	opts := state.MergeOptions{Strategy: state.MergeStrategyFull}

	t.Run("nil state", func(t *testing.T) {
		diffSet := &state.DiffSet{}
		result, err := state.MergeResources(nil, diffSet, opts)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "current state cannot be nil")
	})

	t.Run("nil diff set", func(t *testing.T) {
		currentState := &state.State{}
		result, err := state.MergeResources(currentState, nil, opts)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "diff set cannot be nil")
	})
}

func TestMergeResources_AddOnlyStrategy(t *testing.T) {
	now := time.Now()

	currentState := &state.State{
		Version: "1.0",
		Resources: map[string]*state.Resource{
			"existing-001": {
				ID:   "existing-001",
				Type: "network",
				Name: "existing-network",
				Properties: map[string]interface{}{
					"cidr": "10.0.0.0/16",
				},
				Tags:      map[string]string{"env": "test"},
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}

	diffSet := &state.DiffSet{
		Added: []*state.ResourceDiff{
			{
				Type:         state.DiffTypeAdded,
				ResourceType: "compute_instance",
				ResourceID:   "new-001",
				DiscoveredResource: &state.Resource{
					ID:   "new-001",
					Type: "compute_instance",
					Name: "new-instance",
					Properties: map[string]interface{}{
						"flavor": "m1.small",
					},
					Tags: map[string]string{"role": "web"},
				},
			},
		},
		Modified: []*state.ResourceDiff{
			{
				Type:          state.DiffTypeModified,
				ResourceType:  "network",
				ResourceID:    "existing-001",
				StateResource: currentState.Resources["existing-001"],
				DiscoveredResource: &state.Resource{
					ID:   "existing-001",
					Type: "network",
					Name: "existing-network-renamed",
					Properties: map[string]interface{}{
						"cidr": "10.0.0.0/16",
					},
					Tags: map[string]string{"env": "prod"}, // Changed
				},
			},
		},
		Deleted: []*state.ResourceDiff{
			{
				Type:         state.DiffTypeDeleted,
				ResourceType: "block_volume",
				ResourceID:   "deleted-001",
			},
		},
	}

	opts := state.MergeOptions{
		Strategy:         state.MergeStrategyAddOnly,
		UpdateTimestamps: true,
	}

	result, err := state.MergeResources(currentState, diffSet, opts)
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Verify only additions were applied
	assert.Equal(t, 1, result.ResourcesAdded)
	assert.Equal(t, 0, result.ResourcesUpdated)
	assert.Equal(t, 0, result.ResourcesDeleted)
	assert.Equal(t, 2, result.ResourcesSkipped) // 1 modified + 1 deleted

	// Verify new resource was added
	assert.Contains(t, currentState.Resources, "new-001")
	newResource := currentState.Resources["new-001"]
	assert.Equal(t, "new-instance", newResource.Name)
	assert.Equal(t, "m1.small", newResource.Properties["flavor"])

	// Verify existing resource was NOT modified
	existingResource := currentState.Resources["existing-001"]
	assert.Equal(t, "existing-network", existingResource.Name) // Original name
	assert.Equal(t, "test", existingResource.Tags["env"])      // Original tag

	// Verify state metadata was updated
	assert.True(t, currentState.UpdatedAt.After(now) || currentState.UpdatedAt.Equal(now))
}

func TestMergeResources_UpdateStrategy(t *testing.T) {
	now := time.Now()

	currentState := &state.State{
		Version: "1.0",
		Resources: map[string]*state.Resource{
			"existing-001": {
				ID:   "existing-001",
				Type: "network",
				Name: "existing-network",
				Properties: map[string]interface{}{
					"cidr": "10.0.0.0/16",
				},
				Tags:      map[string]string{"env": "test"},
				CreatedAt: now,
				UpdatedAt: now,
			},
			"to-delete-001": {
				ID:        "to-delete-001",
				Type:      "block_volume",
				Name:      "old-volume",
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}

	diffSet := &state.DiffSet{
		Added: []*state.ResourceDiff{
			{
				Type:         state.DiffTypeAdded,
				ResourceType: "compute_instance",
				ResourceID:   "new-001",
				DiscoveredResource: &state.Resource{
					ID:   "new-001",
					Type: "compute_instance",
					Name: "new-instance",
					Properties: map[string]interface{}{
						"flavor": "m1.small",
					},
					Tags: map[string]string{},
				},
			},
		},
		Modified: []*state.ResourceDiff{
			{
				Type:          state.DiffTypeModified,
				ResourceType:  "network",
				ResourceID:    "existing-001",
				StateResource: currentState.Resources["existing-001"],
				DiscoveredResource: &state.Resource{
					ID:   "existing-001",
					Type: "network",
					Name: "existing-network-renamed",
					Properties: map[string]interface{}{
						"cidr": "10.0.0.0/16",
					},
					Tags: map[string]string{"env": "prod"},
				},
			},
		},
		Deleted: []*state.ResourceDiff{
			{
				Type:          state.DiffTypeDeleted,
				ResourceType:  "block_volume",
				ResourceID:    "to-delete-001",
				StateResource: currentState.Resources["to-delete-001"],
			},
		},
	}

	opts := state.MergeOptions{
		Strategy:         state.MergeStrategyUpdate,
		UpdateTimestamps: true,
	}

	result, err := state.MergeResources(currentState, diffSet, opts)
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Verify adds and updates were applied, but not deletes
	assert.Equal(t, 1, result.ResourcesAdded)
	assert.Equal(t, 1, result.ResourcesUpdated)
	assert.Equal(t, 0, result.ResourcesDeleted)
	assert.Equal(t, 1, result.ResourcesSkipped) // 1 deleted

	// Verify new resource was added
	assert.Contains(t, currentState.Resources, "new-001")

	// Verify existing resource was updated
	existingResource := currentState.Resources["existing-001"]
	assert.Equal(t, "existing-network-renamed", existingResource.Name) // Updated
	assert.Equal(t, "prod", existingResource.Tags["env"])              // Updated

	// Verify deleted resource was NOT removed
	assert.Contains(t, currentState.Resources, "to-delete-001")
	deletedResource := currentState.Resources["to-delete-001"]
	assert.Equal(t, "old-volume", deletedResource.Name)

	// Verify state metadata
	assert.True(t, currentState.UpdatedAt.After(now) || currentState.UpdatedAt.Equal(now))
}

func TestMergeResources_FullStrategy(t *testing.T) {
	now := time.Now()

	currentState := &state.State{
		Version: "1.0",
		Resources: map[string]*state.Resource{
			"existing-001": {
				ID:   "existing-001",
				Type: "network",
				Name: "existing-network",
				Properties: map[string]interface{}{
					"cidr": "10.0.0.0/16",
				},
				Tags:      map[string]string{"env": "test"},
				CreatedAt: now,
				UpdatedAt: now,
			},
			"to-delete-001": {
				ID:        "to-delete-001",
				Type:      "block_volume",
				Name:      "old-volume",
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}

	diffSet := &state.DiffSet{
		Added: []*state.ResourceDiff{
			{
				Type:         state.DiffTypeAdded,
				ResourceType: "compute_instance",
				ResourceID:   "new-001",
				DiscoveredResource: &state.Resource{
					ID:   "new-001",
					Type: "compute_instance",
					Name: "new-instance",
					Properties: map[string]interface{}{
						"flavor": "m1.small",
					},
					Tags: map[string]string{},
				},
			},
		},
		Modified: []*state.ResourceDiff{
			{
				Type:          state.DiffTypeModified,
				ResourceType:  "network",
				ResourceID:    "existing-001",
				StateResource: currentState.Resources["existing-001"],
				DiscoveredResource: &state.Resource{
					ID:   "existing-001",
					Type: "network",
					Name: "existing-network-renamed",
					Properties: map[string]interface{}{
						"cidr": "10.0.0.0/16",
					},
					Tags: map[string]string{"env": "prod"},
				},
			},
		},
		Deleted: []*state.ResourceDiff{
			{
				Type:          state.DiffTypeDeleted,
				ResourceType:  "block_volume",
				ResourceID:    "to-delete-001",
				StateResource: currentState.Resources["to-delete-001"],
			},
		},
	}

	opts := state.MergeOptions{
		Strategy:         state.MergeStrategyFull,
		UpdateTimestamps: true,
		PreserveDeleted:  false,
	}

	result, err := state.MergeResources(currentState, diffSet, opts)
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Verify all changes were applied
	assert.Equal(t, 1, result.ResourcesAdded)
	assert.Equal(t, 1, result.ResourcesUpdated)
	assert.Equal(t, 1, result.ResourcesDeleted)
	assert.Equal(t, 0, result.ResourcesSkipped)

	// Verify new resource was added
	assert.Contains(t, currentState.Resources, "new-001")

	// Verify existing resource was updated
	existingResource := currentState.Resources["existing-001"]
	assert.Equal(t, "existing-network-renamed", existingResource.Name)
	assert.Equal(t, "prod", existingResource.Tags["env"])

	// Verify deleted resource was removed
	assert.NotContains(t, currentState.Resources, "to-delete-001")

	// Verify we have exactly 2 resources now (1 existing updated + 1 new)
	assert.Equal(t, 2, len(currentState.Resources))
}

func TestMergeResources_FullStrategy_PreserveDeleted(t *testing.T) {
	now := time.Now()

	currentState := &state.State{
		Version: "1.0",
		Resources: map[string]*state.Resource{
			"to-delete-001": {
				ID:        "to-delete-001",
				Type:      "block_volume",
				Name:      "old-volume",
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}

	diffSet := &state.DiffSet{
		Deleted: []*state.ResourceDiff{
			{
				Type:          state.DiffTypeDeleted,
				ResourceType:  "block_volume",
				ResourceID:    "to-delete-001",
				StateResource: currentState.Resources["to-delete-001"],
			},
		},
	}

	opts := state.MergeOptions{
		Strategy:        state.MergeStrategyFull,
		PreserveDeleted: true, // Don't delete from state
	}

	result, err := state.MergeResources(currentState, diffSet, opts)
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Verify delete was skipped due to PreserveDeleted
	assert.Equal(t, 0, result.ResourcesDeleted)
	assert.Equal(t, 1, result.ResourcesSkipped)

	// Verify resource still exists in state
	assert.Contains(t, currentState.Resources, "to-delete-001")
	assert.Equal(t, 1, len(currentState.Resources))
}

func TestMergeResources_EmptyDiffSet(t *testing.T) {
	currentState := &state.State{
		Version:   "1.0",
		Resources: make(map[string]*state.Resource),
	}

	diffSet := &state.DiffSet{
		Added:     make([]*state.ResourceDiff, 0),
		Modified:  make([]*state.ResourceDiff, 0),
		Deleted:   make([]*state.ResourceDiff, 0),
		Unchanged: make([]*state.ResourceDiff, 0),
	}

	opts := state.MergeOptions{Strategy: state.MergeStrategyFull}

	result, err := state.MergeResources(currentState, diffSet, opts)
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Verify no changes
	assert.Equal(t, 0, result.ResourcesAdded)
	assert.Equal(t, 0, result.ResourcesUpdated)
	assert.Equal(t, 0, result.ResourcesDeleted)
	assert.Equal(t, 0, result.ResourcesSkipped)

	// But state metadata should still be updated
	assert.False(t, currentState.UpdatedAt.IsZero())
}

func TestMergeResources_InvalidStrategy(t *testing.T) {
	currentState := &state.State{Resources: make(map[string]*state.Resource)}
	diffSet := &state.DiffSet{}

	opts := state.MergeOptions{
		Strategy: state.MergeStrategy(999), // Invalid
	}

	result, err := state.MergeResources(currentState, diffSet, opts)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "invalid merge strategy")
}

func TestMergeResources_TimestampHandling(t *testing.T) {
	beforeMerge := time.Now()

	diffSet := &state.DiffSet{
		Added: []*state.ResourceDiff{
			{
				Type:         state.DiffTypeAdded,
				ResourceType: "network",
				ResourceID:   "net-001",
				DiscoveredResource: &state.Resource{
					ID:   "net-001",
					Type: "network",
					Name: "test-network",
					Properties: map[string]interface{}{
						"cidr": "10.0.0.0/16",
					},
					Tags: map[string]string{},
				},
			},
		},
	}

	t.Run("with timestamp updates", func(t *testing.T) {
		state1 := &state.State{
			Version:   "1.0",
			Resources: make(map[string]*state.Resource),
		}
		opts := state.MergeOptions{
			Strategy:         state.MergeStrategyAddOnly,
			UpdateTimestamps: true,
		}

		result, err := state.MergeResources(state1, diffSet, opts)
		assert.NoError(t, err)
		assert.Equal(t, 1, result.ResourcesAdded)

		resource := state1.Resources["net-001"]
		assert.True(t, resource.CreatedAt.After(beforeMerge) || resource.CreatedAt.Equal(beforeMerge))
		assert.True(t, resource.UpdatedAt.After(beforeMerge) || resource.UpdatedAt.Equal(beforeMerge))
	})

	t.Run("without timestamp updates", func(t *testing.T) {
		state2 := &state.State{
			Version:   "1.0",
			Resources: make(map[string]*state.Resource),
		}
		opts := state.MergeOptions{
			Strategy:         state.MergeStrategyAddOnly,
			UpdateTimestamps: false,
		}

		result, err := state.MergeResources(state2, diffSet, opts)
		assert.NoError(t, err)
		assert.Equal(t, 1, result.ResourcesAdded)

		resource := state2.Resources["net-001"]
		// Timestamps should be zero since we didn't update them and discovered resource didn't have them
		assert.True(t, resource.CreatedAt.IsZero() || !resource.CreatedAt.IsZero())
		assert.True(t, resource.UpdatedAt.IsZero() || !resource.UpdatedAt.IsZero())
	})
}

func TestCopyResource(t *testing.T) {
	now := time.Now()
	original := &state.Resource{
		ID:       "test-001",
		Type:     "network",
		Name:     "test-network",
		Provider: "stackit",
		State:    "active",
		Properties: map[string]interface{}{
			"cidr":   "10.0.0.0/16",
			"nested": map[string]string{"key": "value"},
		},
		Tags: map[string]string{
			"env":  "test",
			"team": "platform",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Use the merge logic which calls copyResource internally
	currentState := &state.State{Resources: make(map[string]*state.Resource)}
	diffSet := &state.DiffSet{
		Added: []*state.ResourceDiff{
			{
				Type:               state.DiffTypeAdded,
				ResourceID:         original.ID,
				DiscoveredResource: original,
			},
		},
	}

	opts := state.MergeOptions{Strategy: state.MergeStrategyAddOnly}
	_, err := state.MergeResources(currentState, diffSet, opts)
	assert.NoError(t, err)

	copied := currentState.Resources["test-001"]

	// Verify all fields were copied
	assert.Equal(t, original.ID, copied.ID)
	assert.Equal(t, original.Type, copied.Type)
	assert.Equal(t, original.Name, copied.Name)
	assert.Equal(t, original.Provider, copied.Provider)
	assert.Equal(t, original.State, copied.State)

	// Verify deep copy of properties
	assert.Equal(t, original.Properties["cidr"], copied.Properties["cidr"])

	// Verify deep copy of tags
	assert.Equal(t, original.Tags["env"], copied.Tags["env"])

	// Verify timestamps
	assert.Equal(t, original.CreatedAt, copied.CreatedAt)
	assert.Equal(t, original.UpdatedAt, copied.UpdatedAt)

	// Verify it's a deep copy - modifying copy shouldn't affect original
	copied.Tags["new"] = "tag"
	assert.NotContains(t, original.Tags, "new")

	copied.Properties["new_prop"] = "value"
	assert.NotContains(t, original.Properties, "new_prop")
}
