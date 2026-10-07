package config

// PinAZNodesEnabled reports whether PVE availability zones are pinned to their
// backing node. An unset pin_az_nodes key means true.
func (c *Config) PinAZNodesEnabled() bool {
	return c.PinAZNodes == nil || *c.PinAZNodes
}
