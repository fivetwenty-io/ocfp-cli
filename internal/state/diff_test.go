package state_test

import (
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/stretchr/testify/assert"
)

func TestCompareResources_Empty(t *testing.T) {
	stateResources := make(map[string]*state.Resource)
	discoveredResources := make([]*state.Resource, 0)

	diffSet := state.CompareResources(stateResources, discoveredResources)

	assert.Equal(t, 0, len(diffSet.Added))
	assert.Equal(t, 0, len(diffSet.Modified))
	assert.Equal(t, 0, len(diffSet.Deleted))
	assert.Equal(t, 0, len(diffSet.Unchanged))
	assert.False(t, diffSet.HasChanges())
}

func TestCompareResources_AddedOnly(t *testing.T) {
	stateResources := make(map[string]*state.Resource)
	discoveredResources := []*state.Resource{
		{
			ID:   "net-001",
			Type: "network",
			Name: "test-network",
			Properties: map[string]interface{}{
				"cidr": "10.0.0.0/16",
			},
			Tags: map[string]string{"env": "test"},
		},
		{
			ID:   "inst-001",
			Type: "compute_instance",
			Name: "test-instance",
			Properties: map[string]interface{}{
				"flavor": "m1.small",
			},
			Tags: map[string]string{"role": "bastion"},
		},
	}

	diffSet := state.CompareResources(stateResources, discoveredResources)

	assert.Equal(t, 2, len(diffSet.Added))
	assert.Equal(t, 0, len(diffSet.Modified))
	assert.Equal(t, 0, len(diffSet.Deleted))
	assert.Equal(t, 0, len(diffSet.Unchanged))
	assert.True(t, diffSet.HasChanges())
	assert.Equal(t, 2, diffSet.TotalChanges())

	// Verify added resources
	assert.Equal(t, "net-001", diffSet.Added[0].ResourceID)
	assert.Equal(t, "network", diffSet.Added[0].ResourceType)
	assert.Nil(t, diffSet.Added[0].StateResource)
	assert.NotNil(t, diffSet.Added[0].DiscoveredResource)
}

func TestCompareResources_DeletedOnly(t *testing.T) {
	stateResources := map[string]*state.Resource{
		"net-001": {
			ID:   "net-001",
			Type: "network",
			Name: "old-network",
			Properties: map[string]interface{}{
				"cidr": "10.0.0.0/16",
			},
			Tags: map[string]string{"env": "prod"},
		},
	}
	discoveredResources := make([]*state.Resource, 0)

	diffSet := state.CompareResources(stateResources, discoveredResources)

	assert.Equal(t, 0, len(diffSet.Added))
	assert.Equal(t, 0, len(diffSet.Modified))
	assert.Equal(t, 1, len(diffSet.Deleted))
	assert.Equal(t, 0, len(diffSet.Unchanged))
	assert.True(t, diffSet.HasChanges())

	// Verify deleted resource
	assert.Equal(t, "net-001", diffSet.Deleted[0].ResourceID)
	assert.NotNil(t, diffSet.Deleted[0].StateResource)
	assert.Nil(t, diffSet.Deleted[0].DiscoveredResource)
}

func TestCompareResources_UnchangedOnly(t *testing.T) {
	now := time.Now()

	stateResources := map[string]*state.Resource{
		"net-001": {
			ID:       "net-001",
			Type:     "network",
			Name:     "test-network",
			Provider: "stackit",
			State:    "active",
			Properties: map[string]interface{}{
				"cidr":   "10.0.0.0/16",
				"region": "eu-west-1",
			},
			Tags:      map[string]string{"env": "test"},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	discoveredResources := []*state.Resource{
		{
			ID:       "net-001",
			Type:     "network",
			Name:     "test-network",
			Provider: "stackit",
			State:    "active",
			Properties: map[string]interface{}{
				"cidr":   "10.0.0.0/16",
				"region": "eu-west-1",
			},
			Tags:      map[string]string{"env": "test"},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	diffSet := state.CompareResources(stateResources, discoveredResources)

	assert.Equal(t, 0, len(diffSet.Added))
	assert.Equal(t, 0, len(diffSet.Modified))
	assert.Equal(t, 0, len(diffSet.Deleted))
	assert.Equal(t, 1, len(diffSet.Unchanged))
	assert.False(t, diffSet.HasChanges())
}

func TestCompareResources_ModifiedProperties(t *testing.T) {
	now := time.Now()

	stateResources := map[string]*state.Resource{
		"inst-001": {
			ID:       "inst-001",
			Type:     "compute_instance",
			Name:     "test-instance",
			Provider: "stackit",
			State:    "active",
			Properties: map[string]interface{}{
				"flavor":  "m1.small",
				"image":   "ubuntu-20.04",
				"old_key": "old_value",
			},
			Tags:      map[string]string{"env": "dev"},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	discoveredResources := []*state.Resource{
		{
			ID:       "inst-001",
			Type:     "compute_instance",
			Name:     "test-instance-renamed",
			Provider: "stackit",
			State:    "running",
			Properties: map[string]interface{}{
				"flavor":  "m1.medium", // Changed
				"image":   "ubuntu-20.04",
				"new_key": "new_value", // Added
				// old_key removed
			},
			Tags:      map[string]string{"env": "prod"}, // Changed
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	diffSet := state.CompareResources(stateResources, discoveredResources)

	assert.Equal(t, 0, len(diffSet.Added))
	assert.Equal(t, 1, len(diffSet.Modified))
	assert.Equal(t, 0, len(diffSet.Deleted))
	assert.Equal(t, 0, len(diffSet.Unchanged))
	assert.True(t, diffSet.HasChanges())

	// Verify modified resource has property changes
	modified := diffSet.Modified[0]
	assert.Equal(t, "inst-001", modified.ResourceID)
	assert.NotNil(t, modified.StateResource)
	assert.NotNil(t, modified.DiscoveredResource)
	assert.Greater(t, len(modified.PropertyChanges), 0)

	// Verify specific changes
	changes := modified.PropertyChanges
	hasNameChange := false
	hasStateChange := false
	hasFlavorChange := false
	hasTagChange := false

	for _, change := range changes {
		switch change.Property {
		case "name":
			hasNameChange = true
			assert.Equal(t, "test-instance", change.OldValue)
			assert.Equal(t, "test-instance-renamed", change.NewValue)
		case "state":
			hasStateChange = true
			assert.Equal(t, "active", change.OldValue)
			assert.Equal(t, "running", change.NewValue)
		case "properties.flavor":
			hasFlavorChange = true
			assert.Equal(t, "m1.small", change.OldValue)
			assert.Equal(t, "m1.medium", change.NewValue)
		case "tags":
			hasTagChange = true
		}
	}

	assert.True(t, hasNameChange, "Should detect name change")
	assert.True(t, hasStateChange, "Should detect state change")
	assert.True(t, hasFlavorChange, "Should detect flavor property change")
	assert.True(t, hasTagChange, "Should detect tag change")
}

func TestCompareResources_MixedChanges(t *testing.T) {
	now := time.Now()

	stateResources := map[string]*state.Resource{
		"net-001": {
			ID:         "net-001",
			Type:       "network",
			Name:       "existing-network",
			Properties: map[string]interface{}{"cidr": "10.0.0.0/16"},
			Tags:       map[string]string{},
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		"inst-001": {
			ID:         "inst-001",
			Type:       "compute_instance",
			Name:       "existing-instance",
			State:      "active",
			Properties: map[string]interface{}{"flavor": "m1.small"},
			Tags:       map[string]string{},
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		"vol-001": {
			ID:         "vol-001",
			Type:       "block_volume",
			Name:       "old-volume",
			Properties: map[string]interface{}{"size": 100},
			Tags:       map[string]string{},
			CreatedAt:  now,
			UpdatedAt:  now,
		},
	}

	discoveredResources := []*state.Resource{
		// Unchanged
		{
			ID:         "net-001",
			Type:       "network",
			Name:       "existing-network",
			Properties: map[string]interface{}{"cidr": "10.0.0.0/16"},
			Tags:       map[string]string{},
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		// Modified
		{
			ID:         "inst-001",
			Type:       "compute_instance",
			Name:       "existing-instance",
			State:      "running", // Changed
			Properties: map[string]interface{}{"flavor": "m1.small"},
			Tags:       map[string]string{},
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		// Added
		{
			ID:         "bucket-001",
			Type:       "object_storage_bucket",
			Name:       "new-bucket",
			Properties: map[string]interface{}{},
			Tags:       map[string]string{},
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		// vol-001 is deleted (in state but not discovered)
	}

	diffSet := state.CompareResources(stateResources, discoveredResources)

	assert.Equal(t, 1, len(diffSet.Added), "Should have 1 added resource")
	assert.Equal(t, 1, len(diffSet.Modified), "Should have 1 modified resource")
	assert.Equal(t, 1, len(diffSet.Deleted), "Should have 1 deleted resource")
	assert.Equal(t, 1, len(diffSet.Unchanged), "Should have 1 unchanged resource")
	assert.True(t, diffSet.HasChanges())
	assert.Equal(t, 3, diffSet.TotalChanges())

	// Verify counts
	assert.Equal(t, 3, diffSet.TotalStateResources)
	assert.Equal(t, 3, diffSet.TotalDiscoveredResources)
}

func TestDiffSet_Summary(t *testing.T) {
	diffSet := &state.DiffSet{
		Added:                    make([]*state.ResourceDiff, 2),
		Modified:                 make([]*state.ResourceDiff, 1),
		Deleted:                  make([]*state.ResourceDiff, 3),
		Unchanged:                make([]*state.ResourceDiff, 5),
		TotalStateResources:      9,
		TotalDiscoveredResources: 8,
	}

	summary := diffSet.Summary()
	assert.Contains(t, summary, "Added: 2")
	assert.Contains(t, summary, "Modified: 1")
	assert.Contains(t, summary, "Deleted: 3")
	assert.Contains(t, summary, "Unchanged: 5")
	assert.Contains(t, summary, "State: 9")
	assert.Contains(t, summary, "Discovered: 8")
}

func TestUpdateResourceFromDiscovered(t *testing.T) {
	fixedNow := time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC)
	t.Cleanup(state.SetNowFn(func() time.Time { return fixedNow }))

	stateRes := &state.Resource{
		ID:       "net-001",
		Type:     "network",
		Name:     "old-name",
		Provider: "old-provider",
		State:    "inactive",
		Properties: map[string]interface{}{
			"old_key": "old_value",
		},
		Tags: map[string]string{
			"old_tag": "old_tag_value",
		},
	}

	discoveredRes := &state.Resource{
		ID:       "net-001",
		Type:     "network",
		Name:     "new-name",
		Provider: "new-provider",
		State:    "active",
		Properties: map[string]interface{}{
			"new_key": "new_value",
		},
		Tags: map[string]string{
			"new_tag": "new_tag_value",
		},
	}

	state.UpdateResourceFromDiscovered(stateRes, discoveredRes)

	// Verify updates
	assert.Equal(t, "new-name", stateRes.Name)
	assert.Equal(t, "new-provider", stateRes.Provider)
	assert.Equal(t, "active", stateRes.State)
	assert.Equal(t, map[string]interface{}{"new_key": "new_value"}, stateRes.Properties)
	assert.Equal(t, map[string]string{"new_tag": "new_tag_value"}, stateRes.Tags)
	assert.Equal(t, fixedNow, stateRes.UpdatedAt)
}
