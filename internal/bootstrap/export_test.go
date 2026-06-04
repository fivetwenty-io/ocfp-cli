package bootstrap

import "time"

// SetSleepFn replaces the package-level sleepFn. Call once from TestMain
// before parallel tests start to avoid data races on the shared variable.
func SetSleepFn(fn func(time.Duration)) {
	sleepFn = fn
}

// GetAvailabilityZone exposes getAvailabilityZone for testing.
func (m *Manager) GetAvailabilityZone(index int) string {
	return m.getAvailabilityZone(index)
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
