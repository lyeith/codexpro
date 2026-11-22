package config

// LegacyAuthEnabled reports whether the static-token lane is available. A
// configured named policy disables it unless the operator enables the bridge.
func (c *Config) LegacyAuthEnabled() bool {
	return c.String("principalsFile", "") == "" || c.Bool("legacyBridge", false)
}
