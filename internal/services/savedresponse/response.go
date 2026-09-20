package savedresponse

import "strings"

type Format string

const (
	FormatHTML Format = "html"
)

type MediaRef struct {
	AssetID  string
	MediaType string
	Name     string
	MIMEType string
}

type Response struct {
	Text   string
	Format Format
	Media  *MediaRef
}

func NewText(text string) Response {
	return Response{Text: text, Format: FormatHTML}
}

func (r Response) Empty() bool {
	return strings.TrimSpace(r.Text) == "" && (r.Media == nil || strings.TrimSpace(r.Media.AssetID) == "")
}

func (r Response) MediaAssetID() string {
	if r.Media == nil {
		return ""
	}
	return strings.TrimSpace(r.Media.AssetID)
}
