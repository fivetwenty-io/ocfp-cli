package bootstrap

// GetAvailabilityZone exposes getAvailabilityZone for testing.
func (m *Manager) GetAvailabilityZone(index int) string {
	return m.getAvailabilityZone(index)
}
