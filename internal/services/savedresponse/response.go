package savedresponse

import "strings"

type Format string

const (
	FormatHTML  Format = "html"
	FormatPlain Format = "plain"
)

type MediaRef struct {
	AssetID   string
	MediaType string
	Name      string
	MIMEType  string
}

type Response struct {
	Text   string
	Format Format
	Media  *MediaRef
}

// Inspection is bounded response metadata for management/UI surfaces.
// It deliberately omits internal asset identifiers.
type Inspection struct {
	Kind          string
	Format        Format
	HasText       bool
	Variables     []string
	MediaName     string
	MIMEType      string
	StickerFormat string
}

func NewHTML(text string) Response {
	return Response{Text: text, Format: FormatHTML}
}

// NewText preserves the historical authored-template semantics.
func NewText(text string) Response {
	return NewHTML(text)
}

func NewPlainText(text string) Response {
	return Response{Text: text, Format: FormatPlain}
}

func (r Response) EffectiveFormat() Format {
	if r.Format == "" {
		return FormatHTML
	}
	return r.Format
}

func (r Response) Empty() bool {
	return strings.TrimSpace(r.Text) == "" && !r.HasMedia()
}

func (r Response) HasMedia() bool {
	return r.Media != nil && strings.TrimSpace(r.Media.AssetID) != ""
}

func (r Response) Clone() Response {
	cloned := r
	if r.Media != nil {
		media := *r.Media
		cloned.Media = &media
	}
	return cloned
}

func (r Response) MediaAssetID() string {
	if r.Media == nil {
		return ""
	}
	return strings.TrimSpace(r.Media.AssetID)
}

func (r Response) Kind() string {
	if !r.HasMedia() {
		return "text"
	}
	kind := strings.ToLower(strings.TrimSpace(r.Media.MediaType))
	switch kind {
	case "photo", "sticker", "audio", "video", "file":
		return kind
	case "voice":
		return "audio"
	default:
		return "media"
	}
}

// Inspect returns user-facing response metadata using the canonical template
// compiler, so management surfaces report exactly the variables delivery sees.
func Inspect(response Response) (Inspection, error) {
	compiled, err := Compile(response)
	if err != nil {
		return Inspection{}, err
	}
	info := Inspection{
		Kind:      response.Kind(),
		Format:    response.EffectiveFormat(),
		HasText:   strings.TrimSpace(response.Text) != "",
		Variables: compiled.Variables(),
	}
	if response.Media != nil {
		info.MediaName = strings.TrimSpace(response.Media.Name)
		info.MIMEType = strings.TrimSpace(response.Media.MIMEType)
		if info.Kind == "sticker" {
			info.StickerFormat = stickerFormat(response.Media)
		}
	}
	return info, nil
}
