package commands

import "strings"

func (m *TeardownManager) isImportedNetworkResource(resource *ResourceToDelete) bool {
	if resource.Type != "network" && resource.Type != "subnet" {
		return false
	}

	if resource.Properties["imported"] == true || resource.Properties["preserve"] == true {
		return true
	}

	if m.config != nil {
		if resource.Type == "network" && importedIdentityMatches(resource.ID, m.config.Network.ID) {
			return true
		}

		for _, sub := range m.config.Network.Subnets {
			if resource.Type == "subnet" && importedIdentityMatches(resource.ID, sub.ID) {
				return true
			}
		}
	}

	if m.stateManager != nil {
		records, err := m.stateManager.GetResourcesByType(resource.Type)
		if err != nil {
			return true
		} // An unreadable ownership record must never authorize deletion.

		for _, record := range records {
			if importedIdentityMatches(resource.ID, record.ID) && (record.Properties["imported"] == true || record.Properties["preserve"] == true) {
				return true
			}
		}
	}

	return false
}
func importedIdentityMatches(a, b string) bool {
	if a == "" || b == "" {
		return false
	}

	a = strings.TrimPrefix(a, "https://www.googleapis.com/compute/v1/")
	b = strings.TrimPrefix(b, "https://www.googleapis.com/compute/v1/")

	return a == b || (!strings.Contains(a, "/") && strings.HasSuffix(b, "/"+a)) || (!strings.Contains(b, "/") && strings.HasSuffix(a, "/"+b))
}
