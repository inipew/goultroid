package presentation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/ui"
)

var (
	// ErrHandoffUnavailable indicates no viable presentation mode could be selected.
	ErrHandoffUnavailable = errors.New("presentation: handoff unavailable")
)

// HandoffMode indicates how presentation continuity is delivered to the user.
type HandoffMode uint8

const (
	HandoffAuto HandoffMode = iota
	HandoffDeepLink
	HandoffSwitchInline
	HandoffDirectAssistant
	HandoffRenderHere
)

// String returns the name of the HandoffMode.
func (m HandoffMode) String() string {
	switch m {
	case HandoffAuto:
		return "auto"
	case HandoffDeepLink:
		return "deep_link"
	case HandoffSwitchInline:
		return "switch_inline"
	case HandoffDirectAssistant:
		return "direct_assistant"
	case HandoffRenderHere:
		return "render_here"
	default:
		return "unknown"
	}
}

// HandoffRequest describes the intent to display or transition to a screen.
type HandoffRequest struct {
	Actor         execution.Actor
	Source        execution.Source
	ChatType      ChatType
	Screen        ScreenKey
	Input         any
	PreferredMode HandoffMode
	TTL           time.Duration
	CorrelationID string
}

// HandoffResult carries the resolved presentation handoff strategy and artifacts.
type HandoffResult struct {
	Mode        HandoffMode
	Screen      *ui.Screen
	DeepLinkURL string
	InlineQuery string
	ExpiresAt   time.Time
}

// AsScreen adapts the HandoffResult into a renderable ui.Screen for the caller surface.
func (r HandoffResult) AsScreen(title, message string) *ui.Screen {
	switch r.Mode {
	case HandoffRenderHere:
		if r.Screen != nil {
			return r.Screen
		}
		return ui.NewScreen("handoff:render", title, message)
	case HandoffDeepLink:
		text := message
		if text == "" {
			text = "Open interactive screen in the assistant bot:"
		}
		s := ui.NewScreen("handoff:deeplink", title, text)
		if r.DeepLinkURL != "" {
			s.AddRow(ui.NewURLButton("Open in Assistant", r.DeepLinkURL))
		}
		return s
	case HandoffSwitchInline:
		text := message
		if text == "" {
			text = "Tap below to search inline:"
		}
		s := ui.NewScreen("handoff:switch_inline", title, text)
		s.AddRow(ui.NewSwitchInlineButton("Search Inline", r.InlineQuery, false))
		return s
	default:
		return ui.NewScreen("handoff:unknown", title, message)
	}
}

// AsFallbackScreen adapts the HandoffResult into a renderable ui.Screen
// whose body explicitly includes the actionable hyperlink or query,
// ensuring the user can still act even when Telegram reply markup cannot be delivered.
func (r HandoffResult) AsFallbackScreen(title, message string) *ui.Screen {
	switch r.Mode {
	case HandoffDeepLink:
		text := message
		if text == "" {
			text = "Open interactive screen in the assistant bot:"
		}
		if r.DeepLinkURL != "" {
			text = fmt.Sprintf("%s\n\n👉 <a href=\"%s\">Open in Assistant</a>", text, html.EscapeString(r.DeepLinkURL))
		}
		return ui.NewScreen("handoff:deeplink:fallback", title, text)
	case HandoffSwitchInline:
		text := message
		if text == "" {
			text = "Tap below to search inline:"
		}
		if r.InlineQuery != "" {
			text = fmt.Sprintf("%s\n\n<code>@bot %s</code>", text, html.EscapeString(r.InlineQuery))
		}
		return ui.NewScreen("handoff:switch_inline:fallback", title, text)
	case HandoffRenderHere:
		if r.Screen != nil {
			return r.Screen
		}
		return ui.NewScreen("handoff:render:fallback", title, message)
	default:
		return ui.NewScreen("handoff:unknown:fallback", title, message)
	}
}

// FallbackText returns the complete safe HTML text representation of the fallback screen.
func (r HandoffResult) FallbackText(title, message string) string {
	return r.AsFallbackScreen(title, message).Text()
}

// DeepLinkIssuer allows the handoff service to request deep-link tokens without coupling to concrete persistence.
type DeepLinkIssuer interface {
	IssueStartLink(ctx context.Context, req DeepLinkRequest) (string, time.Time, error)
}

// DeepLinkRequest contains parameters for issuing a start deep link.
type DeepLinkRequest struct {
	Screen       ScreenKey
	Owner        string
	Generation   uint64
	UserID       int64
	SourceChatID int64
	PayloadType  string
	Payload      []byte
	TTL          time.Duration
}

// HandoffClient defines the interface for initiating cross-surface presentation transitions.
type HandoffClient interface {
	Handoff(ctx context.Context, req HandoffRequest) (HandoffResult, error)
}

// HandoffService coordinates cross-surface navigation and delivery.
type HandoffService struct {
	presentation *Service
	deeplink     DeepLinkIssuer
}

// NewHandoffService creates an initialized HandoffService.
func NewHandoffService(presentation *Service, deeplink DeepLinkIssuer) *HandoffService {
	return &HandoffService{
		presentation: presentation,
		deeplink:     deeplink,
	}
}

// SetDeepLinkIssuer configures or updates the deep link issuer.
func (h *HandoffService) SetDeepLinkIssuer(issuer DeepLinkIssuer) {
	h.deeplink = issuer
}

// Handoff resolves the optimal presentation channel and renders or transitions accordingly.
func (h *HandoffService) Handoff(ctx context.Context, req HandoffRequest) (HandoffResult, error) {
	if h.presentation == nil {
		return HandoffResult{}, fmt.Errorf("%w: presentation service is unconfigured", ErrHandoffUnavailable)
	}

	reg, ok := h.presentation.Registry().Resolve(req.Screen)
	if !ok {
		return HandoffResult{}, fmt.Errorf("%w: %s", ErrScreenNotFound, req.Screen)
	}

	// 1. Evaluate policy
	policyReq := PolicyRequest{
		Actor:    req.Actor,
		Source:   req.Source,
		ChatType: req.ChatType,
		Screen:   req.Screen,
	}
	decision := h.presentation.Evaluator().Evaluate(ctx, reg.Policy, policyReq)

	mode := req.PreferredMode
	if mode == HandoffAuto {
		// Rule 1: If private chat is required or screen is sensitive, or access was denied due to private chat -> DeepLink
		if reg.Policy.RequirePrivate || reg.Policy.Sensitive || (!decision.Allowed && decision.Code == DecisionDenyPrivate) {
			mode = HandoffDeepLink
		} else if !decision.Allowed {
			return HandoffResult{}, fmt.Errorf("%w: %s", ErrAccessDenied, decision.SafeMessage)
		} else if req.Source == execution.SourceAssistant && req.ChatType == ChatTypePrivate {
			mode = HandoffRenderHere
		} else {
			mode = HandoffRenderHere
		}
	}

	switch mode {
	case HandoffDeepLink:
		if h.deeplink == nil {
			return HandoffResult{}, fmt.Errorf("%w: deep link issuer is not configured", ErrHandoffUnavailable)
		}
		ttl := req.TTL
		if ttl <= 0 {
			ttl = 10 * time.Minute
		}

		var payload []byte
		var payloadType string
		if req.Input != nil {
			switch in := req.Input.(type) {
			case []byte:
				payload = in
				payloadType = "bytes"
			case string:
				payload = []byte(in)
				payloadType = "string"
			default:
				if b, err := json.Marshal(req.Input); err == nil {
					payload = b
					payloadType = "json"
				}
			}
		}

		url, expiresAt, err := h.deeplink.IssueStartLink(ctx, DeepLinkRequest{
			Screen:       req.Screen,
			Owner:        reg.Owner,
			Generation:   reg.Generation,
			UserID:       req.Actor.UserID,
			SourceChatID: req.Actor.ChatID,
			PayloadType:  payloadType,
			Payload:      payload,
			TTL:          ttl,
		})
		if err != nil {
			return HandoffResult{}, fmt.Errorf("issue deep link: %w", err)
		}
		return HandoffResult{
			Mode:        HandoffDeepLink,
			DeepLinkURL: url,
			ExpiresAt:   expiresAt,
		}, nil

	case HandoffSwitchInline:
		query := req.Screen.Name
		return HandoffResult{
			Mode:        HandoffSwitchInline,
			InlineQuery: query,
		}, nil

	case HandoffRenderHere:
		// Attempt to render the screen locally
		buildReq := BuildRequest{
			Key:           req.Screen,
			Actor:         req.Actor,
			Source:        req.Source,
			ChatType:      req.ChatType,
			CorrelationID: req.CorrelationID,
			Input:         req.Input,
		}
		res, err := h.presentation.Build(ctx, buildReq)
		if err != nil {
			// If building here failed because private is required and we have deep link, fallback to deep link
			if errors.Is(err, ErrPrivateRequired) && h.deeplink != nil {
				return h.Handoff(ctx, HandoffRequest{
					Actor:         req.Actor,
					Source:        req.Source,
					ChatType:      req.ChatType,
					Screen:        req.Screen,
					Input:         req.Input,
					PreferredMode: HandoffDeepLink,
					TTL:           req.TTL,
					CorrelationID: req.CorrelationID,
				})
			}
			return HandoffResult{}, err
		}
		return HandoffResult{
			Mode:   HandoffRenderHere,
			Screen: res.Screen,
		}, nil

	default:
		return HandoffResult{}, fmt.Errorf("%w: unsupported handoff mode %s", ErrHandoffUnavailable, mode)
	}
}
