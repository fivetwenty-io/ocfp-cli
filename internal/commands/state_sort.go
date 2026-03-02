package commands

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/state"
)

const (
	// sortDirectionParts is the expected number of parts when parsing sort-by (field or field:direction).
	sortDirectionParts = 2
	// maxSortDirectionParts is the maximum allowed parts in a sort-by string.
	maxSortDirectionParts = 2
)

// Sort field constants.
const (
	SortByName  = "name"
	SortByDate  = "date"
	SortByState = "state"
	SortByType  = "type"
)

// Sort direction constants.
const (
	SortDirectionAsc  = "asc"
	SortDirectionDesc = "desc"
)

// SortOptions holds sorting configuration.
type SortOptions struct {
	Field     string // Field to sort by (name, date, state, type)
	Direction string // Sort direction (asc, desc)
}

// ParseSortBy parses a sort-by string into SortOptions.
// Supported formats:
//   - "name"          (ascending by default)
//   - "name:asc"      (explicitly ascending)
//   - "name:desc"     (explicitly descending)
//   - "date:desc"
func ParseSortBy(sortBy string) (*SortOptions, error) {
	if sortBy == "" {
		return nil, ErrSortByEmpty
	}

	parts := strings.Split(sortBy, ":")
	field := strings.TrimSpace(parts[0])
	direction := SortDirectionAsc // default

	if len(parts) > maxSortDirectionParts {
		return nil, fmt.Errorf("%w: %q (expected field or field:direction)", ErrInvalidSortByFormat, sortBy)
	}

	if len(parts) == sortDirectionParts {
		direction = strings.ToLower(strings.TrimSpace(parts[1]))
	}

	// Validate field
	validFields := map[string]bool{
		SortByName:  true,
		SortByDate:  true,
		SortByState: true,
		SortByType:  true,
	}

	if !validFields[field] {
		return nil, fmt.Errorf("%w %q: must be name, date, state, or type", ErrInvalidSortField, field)
	}

	// Validate direction
	if direction != SortDirectionAsc && direction != SortDirectionDesc {
		return nil, fmt.Errorf("%w %q: must be asc or desc", ErrInvalidSortDirection, direction)
	}

	return &SortOptions{
		Field:     field,
		Direction: direction,
	}, nil
}

// SortResources sorts a slice of resources based on sort options.
func SortResources(resources []*state.Resource, opts *SortOptions) {
	if opts == nil {
		return
	}

	sort.Slice(resources, func(firstIndex, secondIndex int) bool {
		var less bool

		switch opts.Field {
		case SortByName:
			less = resources[firstIndex].Name < resources[secondIndex].Name

		case SortByType:
			less = resources[firstIndex].Type < resources[secondIndex].Type

		case SortByState:
			less = resources[firstIndex].State < resources[secondIndex].State

		case SortByDate:
			// Sort by UpdatedAt, fall back to CreatedAt if UpdatedAt is zero
			dateI := resources[firstIndex].UpdatedAt
			if dateI.IsZero() {
				dateI = resources[firstIndex].CreatedAt
			}

			dateJ := resources[secondIndex].UpdatedAt
			if dateJ.IsZero() {
				dateJ = resources[secondIndex].CreatedAt
			}

			less = dateI.Before(dateJ)

		default:
			// Fallback to name sorting
			less = resources[firstIndex].Name < resources[secondIndex].Name
		}

		// Reverse if descending
		if opts.Direction == SortDirectionDesc {
			less = !less
		}

		return less
	})
}

// SortResourceMap converts a resource map to sorted slice.
func SortResourceMap(resources map[string]*state.Resource, opts *SortOptions) []*state.Resource {
	// Convert map to slice
	resourceList := make([]*state.Resource, 0, len(resources))
	for _, resource := range resources {
		resourceList = append(resourceList, resource)
	}

	// Sort the slice
	SortResources(resourceList, opts)

	return resourceList
}

// GetResourceDate returns the most recent date for a resource (UpdatedAt or CreatedAt).
func GetResourceDate(resource *state.Resource) time.Time {
	if !resource.UpdatedAt.IsZero() {
		return resource.UpdatedAt
	}

	return resource.CreatedAt
}
