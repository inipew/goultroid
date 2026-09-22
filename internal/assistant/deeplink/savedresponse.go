package deeplink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

const SavedResponseKind = "savedresponse"

type savedResponseLease struct {
	Alias       string `json:"a"`
	Revision    uint64 `json:"r"`
	Incarnation string `json:"i"`
}

// SavedResponseProvider adapts persistent SurfaceDeepLink bindings into the
// canonical typed /start payload runtime.
type SavedResponseProvider struct {
	bindings *savedresponse.BindingService
	delivery *savedresponse.ResponseDelivery
}

func NewSavedResponseProvider(
	bindings *savedresponse.BindingService,
	delivery *savedresponse.ResponseDelivery,
) *SavedResponseProvider {
	return &SavedResponseProvider{bindings: bindings, delivery: delivery}
}

// Issue creates a durable opaque token for one exact binding incarnation.
// Mutation or delete+recreate after issuance intentionally stales the token.
func (p *SavedResponseProvider) Issue(
	ctx context.Context,
	router *Router,
	alias string,
	actorID int64,
	ttl time.Duration,
	singleUse bool,
) (Token, error) {
	if p == nil || p.bindings == nil || router == nil {
		return Token{}, ErrProviderUnavailable
	}
	prepared, err := p.bindings.Prepare(ctx, savedresponse.SurfaceDeepLink, alias)
	if err != nil {
		return Token{}, err
	}
	binding := prepared.Binding()
	payload, err := json.Marshal(savedResponseLease{
		Alias:       binding.Alias,
		Revision:    binding.Revision,
		Incarnation: binding.Incarnation,
	})
	if err != nil {
		return Token{}, fmt.Errorf("encode saved-response deep-link lease: %w", err)
	}
	return router.Issue(ctx, IssueRequest{
		Kind:      SavedResponseKind,
		Payload:   string(payload),
		ActorID:   actorID,
		SingleUse: singleUse,
		TTL:       ttl,
	})
}

func decodeSavedResponseLease(raw string) (savedResponseLease, error) {
	var lease savedResponseLease
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lease); err != nil {
		return savedResponseLease{}, fmt.Errorf("%w: saved-response lease: %v", ErrInvalidPayload, err)
	}
	if decoder.More() {
		return savedResponseLease{}, ErrInvalidPayload
	}
	lease.Alias = strings.ToLower(strings.TrimSpace(lease.Alias))
	lease.Incarnation = strings.TrimSpace(lease.Incarnation)
	if lease.Alias == "" || lease.Revision == 0 || lease.Incarnation == "" {
		return savedResponseLease{}, ErrInvalidPayload
	}
	return lease, nil
}

func (p *SavedResponseProvider) Prepare(
	ctx context.Context,
	payload string,
	_ int64,
) (PreparedTarget, error) {
	if p == nil || p.bindings == nil {
		return PreparedTarget{}, ErrProviderUnavailable
	}
	lease, err := decodeSavedResponseLease(payload)
	if err != nil {
		return PreparedTarget{}, err
	}
	prepared, err := p.bindings.Prepare(ctx, savedresponse.SurfaceDeepLink, lease.Alias)
	if err != nil {
		return PreparedTarget{}, err
	}
	binding := prepared.Binding()
	if binding.Revision != lease.Revision || binding.Incarnation != lease.Incarnation {
		return PreparedTarget{}, savedresponse.ErrBindingStale
	}
	resources := make([]tasks.ResourceRequirement, 0, 1)
	if prepared.HasMedia() {
		resources = append(resources, tasks.ResourceRequirement{Name: "media", Amount: 1})
	}
	return PreparedTarget{
		Scope:     prepared.Scope(),
		Resources: resources,
		State:     prepared,
	}, nil
}

func (p *SavedResponseProvider) Execute(
	ctx context.Context,
	target PreparedTarget,
	sink Delivery,
) error {
	if p == nil || p.bindings == nil || p.delivery == nil {
		return savedresponse.ErrResponseDeliveryUnavailable
	}
	prepared, ok := target.State.(savedresponse.PreparedBinding)
	if !ok {
		return savedresponse.ErrBindingStale
	}
	resolved, err := p.bindings.ResolvePrepared(ctx, prepared)
	if err != nil {
		return err
	}
	stage, err := p.delivery.Deliver(
		ctx,
		resolved.Resolved.Response,
		savedresponse.TemplateVars{
			UserID: sink.ActorID,
			ChatID: sink.ChatID,
			Now:    time.Now(),
		},
		savedresponse.DeliverySink{
			SendMedia: sink.SendMedia,
			SendText:  sink.SendText,
		},
	)
	if err != nil {
		return fmt.Errorf("saved-response deep-link delivery stage %d: %w", stage, err)
	}
	return nil
}

var _ Provider = (*SavedResponseProvider)(nil)

func isSavedResponseStale(err error) bool {
	return errors.Is(err, savedresponse.ErrBindingStale) ||
		errors.Is(err, savedresponse.ErrBindingDisabled) ||
		errors.Is(err, savedresponse.ErrBindingNotFound)
}
