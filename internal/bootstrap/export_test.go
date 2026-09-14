package bootstrap

import (
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// SetSleepFn replaces the package-level sleepFn. Call once from TestMain
// before parallel tests start to avoid data races on the shared variable.
func SetSleepFn(fn func(time.Duration)) {
	sleepFn = fn
}

// GetAvailabilityZone exposes getAvailabilityZone for testing.
func (m *Manager) GetAvailabilityZone(index int) string {
	return m.getAvailabilityZone(index)
}

// SlotForNamedIP exposes slotForNamedIP for testing.
func (m *Manager) SlotForNamedIP(subnetName, subnetCIDR, ipKey string, fallback int) int {
	return m.slotForNamedIP(subnetName, subnetCIDR, ipKey, fallback)
}

// UseVirtualSubnets exposes useVirtualSubnets for testing.
func (m *Manager) UseVirtualSubnets() bool {
	return m.useVirtualSubnets()
}

// UseVirtualSubnetsForPVE exposes useVirtualSubnetsForPVE for testing.
func (m *Manager) UseVirtualSubnetsForPVE() bool {
	return m.useVirtualSubnetsForPVE()
}

// SelectVirtualSubnetStrategyName exposes the selected subnet strategy's name
// for testing the factory mapping (provider/config -> strategy).
func (m *Manager) SelectVirtualSubnetStrategyName() string {
	return m.selectVirtualSubnetStrategy().name()
}

// ProviderUsesLocalKeypairs exposes providerUsesLocalKeypairs for testing.
func (m *Manager) ProviderUsesLocalKeypairs() bool {
	return m.providerUsesLocalKeypairs()
}

// ProviderDisplayName exposes providerDisplayName for testing.
func (m *Manager) ProviderDisplayName() string {
	return m.providerDisplayName()
}

// AdjustSubnetForProvider exposes adjustSubnetForProvider for testing.
func (m *Manager) AdjustSubnetForProvider(subnetID string) string {
	return m.adjustSubnetForProvider(subnetID)
}

// BastionStaticIPPrefix exposes bastionStaticIPPrefix for testing.
func (m *Manager) BastionStaticIPPrefix() int {
	return m.bastionStaticIPPrefix()
}

// HasSafe returns true when a vault Safe client is wired into the manager.
func (m *Manager) HasSafe() bool {
	return m.safe != nil
}

// StepDescriptor is a test-visible view of one bootstrap step.
type StepDescriptor struct {
	Name     string
	Category string
	Required bool
}

// StepNamesAndCategories exposes the ordered step list for testing.
func (m *Manager) StepNamesAndCategories() []StepDescriptor {
	steps := m.buildSteps()
	out := make([]StepDescriptor, 0, len(steps))

	for _, s := range steps {
		out = append(out, StepDescriptor{Name: s.name, Category: s.category, Required: s.required})
	}

	return out
}

// BastionFilteredStepNames exposes the steps `--bastion` keeps, for testing.
func (m *Manager) BastionFilteredStepNames() []string {
	steps := m.filterBastionSteps(m.buildSteps())
	out := make([]string, 0, len(steps))

	for _, s := range steps {
		out = append(out, s.name)
	}

	return out
}

// VolumeDescriptor is a test-visible view of one planned volume.
type VolumeDescriptor struct {
	Name   string
	SizeGB int
	Type   string
}

// PlannedVolumes exposes the dry-run volume preview for testing.
func (m *Manager) PlannedVolumes() []VolumeDescriptor {
	plan := &bootstrapPlan{}
	m.setupVolumesPlan(plan)

	out := make([]VolumeDescriptor, 0, len(plan.Volumes))
	for _, v := range plan.Volumes {
		out = append(out, VolumeDescriptor{Name: v.Name, SizeGB: v.SizeGB, Type: v.Type})
	}

	return out
}

// BastionDataDiskSpec exposes bastionDataDiskSpec for testing.
func (m *Manager) BastionDataDiskSpec() *cpi.DataDiskSpec {
	return m.bastionDataDiskSpec()
}
