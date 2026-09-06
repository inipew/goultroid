package inline

import (
	"context"
	"errors"
	"regexp"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/ui"
)

var (
	// ErrNoMatchingHandler indicates no registered inline handler matched the incoming query.
	ErrNoMatchingHandler = errors.New("no matching inline handler found")
	// ErrEmptyResults indicates the handler produced zero inline results.
	ErrEmptyResults = errors.New("no inline results available")
)

// InlineResultType defines Telegram inline result types.
type InlineResultType string

const (
	ResultArticle  InlineResultType = "article"
	ResultPhoto    InlineResultType = "photo"
	ResultDocument InlineResultType = "document"
	ResultVideo    InlineResultType = "video"
	ResultGif      InlineResultType = "gif"
	ResultAudio    InlineResultType = "audio"
	ResultVenue    InlineResultType = "venue"
	ResultGeo      InlineResultType = "geo"
	ResultContact  InlineResultType = "contact"
	ResultGame     InlineResultType = "game"
)

// CachePolicy controls per-handler inline cache scoping.
type CachePolicy int

const (
	CacheGlobal CachePolicy = iota
	CachePerUser
	CachePerChat
	CacheNone
)

// InlineContext contains query input, requester identity, and execution context.
type InlineContext struct {
	Ctx           context.Context
	QueryID       int64
	UserID        int64
	RawQuery      string
	Pattern       string
	Args          []string
	Offset        string
	CorrelationID string
	Locale        string
	PeerType      tg.InlineQueryPeerTypeClass
}

// InlineResult represents a high-level inline result. Type defaults to Article if empty.
type InlineResult struct {
	ID          string
	Type        InlineResultType
	Title       string
	Description string
	Text        string
	Markup      *ui.Markup
	ThumbURL    string
	URL         string

	// Media fields for non-article types
	MediaURL      string // photo/document/video/gif/audio media URL or file id
	MediaMimeType string
	Width         int
	Height        int
	Duration      int

	// Game result
	GameShortName string

	// Geo/Venue
	Latitude  float64
	Longitude float64
	Address   string

	// Contact
	PhoneNumber string
	FirstName   string
	LastName    string
	VCard       string
}

// InlineResponse is returned by handlers; allows native pagination and response policy.
type InlineResponse struct {
	Results    []InlineResult
	NextOffset string
	Cache      CachePolicy
	CacheTime  int // seconds; 0 uses engine default; negative disables
	Gallery    bool
	Private    bool
	SwitchPM   *SwitchPM
	SwitchWebView *SwitchWebView
}

type SwitchPM struct {
	Text  string
	Query string
}

type SwitchWebView struct {
	Text string
	URL  string
}

// InlineChatType identifies the chat category reported by Telegram in inline queries.
type InlineChatType string

const (
	ChatTypePrivate    InlineChatType = "private"
	ChatTypeGroup      InlineChatType = "group"
	ChatTypeSupergroup InlineChatType = "supergroup"
	ChatTypeChannel    InlineChatType = "channel"
)

// InlineAccessPolicy declares handler authorization.
type InlineAccessPolicy struct {
	OwnerOnly        bool
	SudoOnly         bool
	AllowedUsers     []int64
	AllowedChats     []int64
	AllowedChatTypes []InlineChatType
}

// InlineMatcher abstracts query matching.
type InlineMatcher interface {
	Match(query string) (args []string, ok bool)
}

type exactMatcher struct{ keyword string }

func (m *exactMatcher) Match(query string) ([]string, bool) {
	// exact handled via Registry directly; this is fallback for interface users
	return nil, false
}

type prefixMatcher struct{ prefix string }

func (m *prefixMatcher) Match(query string) ([]string, bool) {
	if len(query) >= len(m.prefix) && query[:len(m.prefix)] == m.prefix {
		rest := query[len(m.prefix):]
		if rest == "" {
			return nil, true
		}
		// trim leading space and split
		args := splitArgs(rest)
		return args, true
	}
	return nil, false
}

type regexMatcher struct{ re *regexp.Regexp }

func (m *regexMatcher) Match(query string) ([]string, bool) {
	matches := m.re.FindStringSubmatch(query)
	if matches == nil {
		return nil, false
	}
	if len(matches) > 1 {
		return matches[1:], true
	}
	return nil, true
}

func NewPrefixMatcher(prefix string) InlineMatcher { return &prefixMatcher{prefix: prefix} }
func NewRegexMatcher(pattern string) (InlineMatcher, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return &regexMatcher{re: re}, nil
}

// InlineHandler processes an inline search query.
type InlineHandler interface {
	Pattern() string
	Description() string
	HandleInline(ctx *InlineContext) ([]InlineResult, error)
}

// InlineHandlerV2 is extended handler supporting InlineResponse, matching and policy.
// Existing handlers implementing InlineHandler still work via adapter.
type InlineHandlerV2 interface {
	InlineHandler
	Matcher() InlineMatcher
	AccessPolicy() InlineAccessPolicy
	CachePolicy() CachePolicy
	HandleInlineV2(ctx *InlineContext) (*InlineResponse, error)
}

func splitArgs(s string) []string {
	var out []string
	start := -1
	for i, r := range s {
		if r == ' ' || r == '\t' {
			if start != -1 {
				out = append(out, s[start:i])
				start = -1
			}
		} else {
			if start == -1 {
				start = i
			}
		}
	}
	if start != -1 {
		out = append(out, s[start:])
	}
	return out
}
