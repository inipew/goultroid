package menu

// CompatibilityHost is the narrow legacy Assistant menu capability still
// required by plugin-owned a1 flows. New Assistant shell code must not depend
// on this interface; it exists only until those plugin flows migrate to P0-P3.
type CompatibilityHost interface {
	RegisterTextHandler(TextHandler)
	Instances() InstanceStore
	RegisterInstance(MenuInstance)
}

var _ CompatibilityHost = (*Controller)(nil)
