package presentation

import "strings"

// ResponseKind describes the semantic intent of user-facing text independently
// from the Telegram surface that ultimately renders it.
type ResponseKind uint8

const (
	ResponseStatus ResponseKind = iota
	ResponseSuccess
	ResponseError
	ResponseProgress
	ResponseResult
)

// Response is a transport-neutral semantic response. Text may contain the
// repository's supported Telegram HTML because escaping remains the caller's
// responsibility at the content boundary.
type Response struct {
	Kind ResponseKind
	Text string
}

func Status(text string) Response   { return Response{Kind: ResponseStatus, Text: text} }
func Success(text string) Response  { return Response{Kind: ResponseSuccess, Text: text} }
func Error(text string) Response    { return Response{Kind: ResponseError, Text: text} }
func Progress(text string) Response { return Response{Kind: ResponseProgress, Text: text} }
func Result(text string) Response   { return Response{Kind: ResponseResult, Text: text} }

// Render returns one canonical visual treatment for each semantic intent. The
// result intent deliberately preserves caller-owned rich presentation exactly.
func (r Response) Render() string {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return ""
	}
	switch r.Kind {
	case ResponseStatus:
		return "ℹ️ <b>Status:</b> " + text
	case ResponseSuccess:
		return "✅ <b>Success:</b> " + text
	case ResponseError:
		return "❌ <b>Error:</b> " + text
	case ResponseProgress:
		return "⏳ <b>Processing:</b> " + text
	case ResponseResult:
		return text
	default:
		return text
	}
}

// View projects the same semantic response into the existing a2/inline
// presentation model without creating another UI runtime.
func (r Response) View(rows ...Row) View {
	return View{Text: r.Render(), Rows: rows}
}
