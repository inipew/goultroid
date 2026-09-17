package inline

import (
	"fmt"
	"strings"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/ui"
)

// Authorizer allows resolving roles for an actor in inline discovery.
type Authorizer interface {
	IsOwner(userID int64) bool
	IsSudo(userID int64) bool
}

// CatalogHandler generates an interactive directory of available inline capabilities.
type CatalogHandler struct {
	registry    *Registry
	botUsername string
	authorizer  Authorizer
}

var _ InlineHandler = (*CatalogHandler)(nil)

// NewCatalogHandler creates a new CatalogHandler backed by the supplied capability Registry.
func NewCatalogHandler(registry *Registry, botUsername string) *CatalogHandler {
	return &CatalogHandler{
		registry:    registry,
		botUsername: botUsername,
	}
}

// SetAuthorizer attaches an Authorizer to dynamically evaluate caller roles.
func (h *CatalogHandler) SetAuthorizer(auth Authorizer) {
	h.authorizer = auth
}

func (h *CatalogHandler) Pattern() string {
	return ""
}

func (h *CatalogHandler) Description() string {
	return "Explore available inline capabilities and interactive commands"
}

func (h *CatalogHandler) HandleInline(ctx *InlineContext) ([]InlineResult, error) {
	if h.registry == nil {
		return nil, nil
	}

	search := ""
	if len(ctx.Args) > 0 {
		search = strings.Join(ctx.Args, " ")
	}

	actor := execution.NewActor(ctx.UserID, 0, false, false)
	if h.authorizer != nil {
		actor.IsOwner = h.authorizer.IsOwner(ctx.UserID)
		actor.IsSudo = h.authorizer.IsSudo(ctx.UserID)
	}
	caps := h.registry.ListCapabilities(CatalogQuery{
		Actor:    actor,
		PeerType: ctx.PeerType,
		Search:   search,
	})

	var results []InlineResult
	for _, cap := range caps {
		if cap.ID == "default" || cap.Pattern == "" {
			continue // avoid self-referencing catch-all in the list
		}

		usage := cap.Usage
		if usage == "" {
			usage = fmt.Sprintf("@%s %s", h.botUsername, cap.Pattern)
		}

		text := fmt.Sprintf("⚡ <b>%s</b>\n\n%s\n\n• <b>Pattern:</b> <code>%s</code>\n• <b>Usage:</b> <code>%s</code>",
			ui.EscapeHTML(cap.Title),
			ui.EscapeHTML(cap.Description),
			ui.EscapeHTML(cap.Pattern),
			ui.EscapeHTML(usage),
		)

		markup := ui.NewMarkup(ui.ButtonRow{
			ui.NewSwitchInlineButton("🚀 Try Feature", cap.Pattern+" ", false),
		})

		results = append(results, InlineResult{
			ID:          fmt.Sprintf("cap_%s", cap.ID),
			Title:       cap.Title,
			Description: cap.Description,
			Text:        text,
			Markup:      &markup,
		})
	}

	if len(results) == 0 {
		emptyMarkup := ui.NewMarkup(ui.ButtonRow{
			ui.NewSwitchInlineButton("🔍 Search Again", "", false),
		})
		results = append(results, InlineResult{
			ID:          "catalog_empty",
			Title:       "No Matching Features",
			Description: "No inline capabilities matched your search or authorization level",
			Text:        "ℹ️ <i>No inline features found matching your search. Try browsing all features by clearing the query.</i>",
			Markup:      &emptyMarkup,
		})
	}

	return results, nil
}
