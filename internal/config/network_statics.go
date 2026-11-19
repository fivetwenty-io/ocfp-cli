package config

import (
	"errors"
	"fmt"

	"github.com/fivetwenty-io/ocfp-cli/internal/netlayout"
	"github.com/goccy/go-yaml"
)

// controlPlaneOverrideKey is the documented config key for the
// control-plane static override, named in every error about it.
const controlPlaneOverrideKey = "network.statics.mgmt.controlPlane"

// controlPlaneKeyCamel and controlPlaneKeySnake are the two spellings of
// the control-plane override under network.statics.mgmt.
const (
	controlPlaneKeyCamel = "controlPlane"
	controlPlaneKeySnake = "control_plane"
)

var (
	// ErrStaticOverrideNotList is returned when a static override is
	// written as a bare offset instead of a list of offsets.
	ErrStaticOverrideNotList = errors.New("static override must be a list of offsets")
	// ErrStaticOverrideBothKeys is returned when a static override is set
	// under both its camelCase key and its snake_case alias, whatever the
	// values, so neither spelling can silently hide the other.
	ErrStaticOverrideBothKeys = errors.New("static override is set under both its camelCase key and its snake_case alias")
)

// NetworkStatics groups per-tier static-offset overrides addressed by
// netlayout.Tier name. Only the mgmt tier is exposed today.
type NetworkStatics struct {
	Mgmt MgmtStatics `json:"mgmt,omitempty" mapstructure:"mgmt" yaml:"mgmt,omitempty"`
}

// MgmtStatics holds the mgmt-tier static overrides.
//
// ControlPlane moves the control-plane static (netlayout.ControlPlaneRole)
// to another offset. It is a list so a later release can accept several
// control-plane addresses under the same key without a config-shape
// migration; today it must hold exactly one offset. Leaving the key unset
// keeps the static at the strategy's offset, and an empty list is refused.
type MgmtStatics struct {
	ControlPlane []int `json:"controlPlane,omitempty" mapstructure:"controlPlane" yaml:"controlPlane,omitempty"`
}

// UnmarshalYAML accepts the documented controlPlane key or the snake_case
// control_plane alias. Setting both is an error whatever their values, a
// bare offset is refused with an error that asks for a list, and an empty
// or null list is refused with an error that requires exactly one entry.
// Leaving both keys unset means no override.
func (m *MgmtStatics) UnmarshalYAML(data []byte) error {
	var raw map[string]any

	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decoding %s: %w", controlPlaneOverrideKey, err)
	}

	camel, hasCamel := raw[controlPlaneKeyCamel]
	snake, hasSnake := raw[controlPlaneKeySnake]

	switch {
	case hasCamel && hasSnake:
		return fmt.Errorf("%s: %w, so set only %s",
			controlPlaneOverrideKey, ErrStaticOverrideBothKeys, controlPlaneKeyCamel)
	case !hasCamel && !hasSnake:
		m.ControlPlane = nil

		return nil
	}

	value := camel
	if hasSnake {
		value = snake
	}

	offsets, err := offsetList(value)
	if err != nil {
		return fmt.Errorf("%s: %w", controlPlaneOverrideKey, err)
	}

	m.ControlPlane = offsets

	return nil
}

// offsetList converts a decoded YAML value into a list of offsets. It
// refuses anything other than a non-empty sequence of integers; a null or
// empty list is refused because an unset key, not an empty one, is how a
// bloc asks for no override.
func offsetList(value any) ([]int, error) {
	if value == nil {
		return nil, fmt.Errorf("%w: exactly one entry is required, got null", netlayout.ErrStaticOverrideCount)
	}

	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%w, such as [23], got %v", ErrStaticOverrideNotList, value)
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("%w: exactly one entry is required, got an empty list", netlayout.ErrStaticOverrideCount)
	}

	offsets := make([]int, 0, len(items))

	for _, item := range items {
		switch n := item.(type) {
		case uint64:
			offsets = append(offsets, int(n)) //nolint:gosec // offsets are small; range is checked by netlayout
		case int64:
			offsets = append(offsets, int(n))
		case int:
			offsets = append(offsets, n)
		default:
			return nil, fmt.Errorf("%w of integers, got entry %v", ErrStaticOverrideNotList, item)
		}
	}

	return offsets, nil
}

// ApplyStaticOverrides returns layout with every configured static override
// applied for cidr, the workload subnet at index idx (-1 for a subnet with no
// workload position), or layout itself when none is configured. An override
// is checked against cidr only when the strategy places the static on idx. The mgmt band
// override, when both its ends are set, is passed along as the mgmt tier's
// effective band, so an override is checked against the band the bloc
// actually uses. Every reserved-IP writer and the bootstrap slot resolver
// call this one helper, so both layers place the static at the same
// address. Errors wrap the netlayout sentinel and name the config key.
func (n *NetworkConfig) ApplyStaticOverrides(
	layout netlayout.Layout, cidr string, idx int,
) (netlayout.Layout, error) {
	offsets := n.Statics.Mgmt.ControlPlane
	if len(offsets) == 0 {
		return layout, nil
	}

	var bands map[netlayout.Tier]netlayout.BandRange

	if mgmt := n.Bands.Mgmt; mgmt.Start != 0 && mgmt.End != 0 {
		bands = map[netlayout.Tier]netlayout.BandRange{
			netlayout.TierMgmt: {Start: mgmt.Start, End: mgmt.End},
		}
	}

	derived, err := layout.WithStaticOverride(netlayout.TierMgmt, netlayout.ControlPlaneRole, cidr, idx, offsets, bands)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", controlPlaneOverrideKey, err)
	}

	return derived, nil
}
