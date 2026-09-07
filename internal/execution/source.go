package execution

// Source represents the execution transport origin.
type Source int

const (
	// SourceUserbot indicates execution originating from the user session.
	SourceUserbot Source = iota
	// SourceAssistant indicates execution originating from the assistant bot session.
	SourceAssistant
	// SourceInline indicates execution originating from an inline query context.
	SourceInline
)

// String returns the name of the execution source.
func (s Source) String() string {
	switch s {
	case SourceUserbot:
		return "userbot"
	case SourceAssistant:
		return "assistant"
	case SourceInline:
		return "inline"
	default:
		return "unknown"
	}
}

// SurfaceMask is a bitmask describing the surfaces that support a given capability or command.
type SurfaceMask uint8

const (
	// SurfaceUserbot allows execution on the userbot surface.
	SurfaceUserbot SurfaceMask = 1 << iota
	// SurfaceAssistant allows execution on the assistant bot surface.
	SurfaceAssistant
	// SurfaceInline allows execution on the inline query surface.
	SurfaceInline

	// SurfaceAll permits execution across all supported surfaces.
	SurfaceAll = SurfaceUserbot | SurfaceAssistant | SurfaceInline
	// SurfaceBotAndUser allows execution on both userbot and assistant bot surfaces.
	SurfaceBotAndUser = SurfaceUserbot | SurfaceAssistant
)

// Supports reports whether the mask enables the given source.
func (m SurfaceMask) Supports(s Source) bool {
	switch s {
	case SourceUserbot:
		return m&SurfaceUserbot != 0
	case SourceAssistant:
		return m&SurfaceAssistant != 0
	case SourceInline:
		return m&SurfaceInline != 0
	default:
		return false
	}
}
