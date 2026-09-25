package savedresponsecallback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	FeatureID     = "savedresponse_callback"
	ActionDeliver = "deliver"
	DefaultTTL    = 10 * time.Minute
)

var ErrUnavailable = errors.New("saved-response callback: runtime unavailable")

type callbackLease struct {
	Alias       string `json:"a"`
	Revision    uint64 `json:"r"`
	Incarnation string `json:"i"`
}

// BeginRequest renders one a2 button bound to an exact SurfaceCallback lease.
type BeginRequest struct {
	Alias      string
	ActorID    int64
	Target     presentation.Target
	Text       string
	ButtonText string
	TTL        time.Duration
}

// Feature is an application-owned a2 feature. Callback data contains only the
// canonical a2 session reference; SavedResponse routing identity stays in the
// bounded server-side session state.
type Feature struct {
	bindings *savedresponse.BindingService
	delivery *savedresponse.ResponseDelivery

	mu sync.RWMutex
	rt assistantinteraction.DriverRuntime
}

func New(bindings *savedresponse.BindingService, delivery *savedresponse.ResponseDelivery) *Feature {
	return &Feature{bindings: bindings, delivery: delivery}
}

func (*Feature) Name() string               { return FeatureID }
func (*Feature) Commands() []core.Command   { return nil }
func (*Feature) Init() error                { return nil }
func (*Feature) AssistantFeatureID() string { return FeatureID }
func (*Feature) HandleAssistantInput(*orchestration.Context, string) error {
	return ErrUnavailable
}

func (*Feature) FeatureSpec() feature.Spec {
	surface := execution.SurfaceAssistant
	return feature.Spec{
		ID:          FeatureID,
		Name:        "SavedResponse Callback",
		Description: "Typed a2 action surface for persistent SavedResponse callback bindings.",
		Category:    "Assistant",
		Interactions: []feature.Interaction{{
			ID:          ActionDeliver,
			Kind:        feature.InteractionAction,
			Description: "Deliver a bound SavedResponse from an a2 callback",
			Surfaces:    surface,
			Policy:      feature.PublicPolicy(surface),
		}},
	}
}

func encodeLease(binding savedresponse.SurfaceBinding) ([]byte, error) {
	return json.Marshal(callbackLease{
		Alias:       binding.Alias,
		Revision:    binding.Revision,
		Incarnation: binding.Incarnation,
	})
}

func decodeLease(raw []byte) (callbackLease, error) {
	var lease callbackLease
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lease); err != nil {
		return callbackLease{}, savedresponse.ErrBindingStale
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return callbackLease{}, savedresponse.ErrBindingStale
	}
	lease.Alias = strings.ToLower(strings.TrimSpace(lease.Alias))
	lease.Incarnation = strings.TrimSpace(lease.Incarnation)
	if lease.Alias == "" || lease.Revision == 0 || lease.Incarnation == "" {
		return callbackLease{}, savedresponse.ErrBindingStale
	}
	return lease, nil
}

func (f *Feature) BindAssistant(rt assistantinteraction.DriverRuntime) (func(), error) {
	if f == nil || f.bindings == nil || f.delivery == nil ||
		rt.Engine == nil || rt.Catalog == nil || rt.Admit == nil || rt.Service == nil {
		return nil, ErrUnavailable
	}
	scope, ok := rt.Catalog.FeatureScope(FeatureID)
	if !ok || scope.IsZero() {
		return nil, ErrUnavailable
	}

	registration, err := rt.Engine.RegisterPreparedAction(
		scope,
		FeatureID,
		ActionDeliver,
		f.prepareAction,
		func(ctx *orchestration.Context) error {
			return f.handleAction(rt, ctx)
		},
	)
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.rt = rt
	f.mu.Unlock()
	return func() {
		registration.Close()
		f.mu.Lock()
		if f.rt.Engine == rt.Engine {
			f.rt = assistantinteraction.DriverRuntime{}
		}
		f.mu.Unlock()
	}, nil
}

func (f *Feature) prepareAction(ctx context.Context, action rootinteraction.Action) (rootinteraction.ActionAdmission, error) {
	if f == nil || f.bindings == nil {
		return rootinteraction.ActionAdmission{}, ErrUnavailable
	}
	lease, err := decodeLease(action.Session.State)
	if err != nil {
		return rootinteraction.ActionAdmission{}, err
	}
	prepared, err := f.bindings.Prepare(ctx, savedresponse.SurfaceCallback, lease.Alias)
	if err != nil {
		return rootinteraction.ActionAdmission{}, err
	}
	binding := prepared.Binding()
	if binding.Revision != lease.Revision || binding.Incarnation != lease.Incarnation {
		return rootinteraction.ActionAdmission{}, savedresponse.ErrBindingStale
	}
	profile := tasks.ExecutionProfile{}
	if prepared.HasMedia() {
		profile = tasks.ExecutionProfile{
			Pool:             tasks.PoolID("general"),
			Class:            tasks.PriorityInteractive,
			ExecutionTimeout: 2 * time.Minute,
			Resources:        []tasks.ResourceRequirement{{Name: "media", Amount: 1}},
		}
	}
	return rootinteraction.ActionAdmission{
		Scope:   prepared.Scope(),
		Profile: profile,
		State:   prepared,
	}, nil
}

func (f *Feature) handleAction(rt assistantinteraction.DriverRuntime, ctx *orchestration.Context) error {
	if f == nil || ctx == nil || f.bindings == nil || f.delivery == nil {
		return ErrUnavailable
	}
	session := ctx.Session()
	if err := rt.Admit(FeatureID, feature.InteractionAction, ActionDeliver, session.Binding.ActorID, ctx.Target()); err != nil {
		return err
	}
	target, ok := ctx.Target().(presentationtelegram.MessageTarget)
	if !ok || target.Peer == nil || target.ChatID == 0 {
		return presentationtelegram.ErrInvalidTarget
	}
	prepared, ok := ctx.Preparation().(savedresponse.PreparedBinding)
	if !ok {
		return savedresponse.ErrBindingStale
	}
	resolved, err := f.bindings.ResolvePrepared(ctx.Context(), prepared)
	if err != nil {
		return err
	}
	stage, err := f.delivery.Deliver(
		ctx.Context(),
		resolved.Resolved.Response,
		savedresponse.TemplateVars{
			UserID: session.Binding.ActorID,
			ChatID: target.ChatID,
			Now:    time.Now(),
		},
		savedresponse.DeliverySink{
			SendMedia: func(mediaType, path, caption string) error {
				_, sendErr := rt.Service.SendMedia(ctx.Context(), target.Peer, mediaType, path, caption)
				return sendErr
			},
			SendText: func(text string) error {
				_, sendErr := rt.Service.SendMessageWithMarkup(ctx.Context(), target.Peer, text, nil)
				return sendErr
			},
		},
	)
	if err != nil {
		return fmt.Errorf("saved-response callback delivery stage %d: %w", stage, err)
	}
	return nil
}

func (f *Feature) Begin(ctx context.Context, request BeginRequest) (*orchestration.Context, error) {
	if f == nil || f.bindings == nil {
		return nil, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if request.ActorID == 0 || request.Target == nil {
		return nil, ErrUnavailable
	}
	prepared, err := f.bindings.Prepare(ctx, savedresponse.SurfaceCallback, request.Alias)
	if err != nil {
		return nil, err
	}
	state, err := encodeLease(prepared.Binding())
	if err != nil {
		return nil, err
	}

	f.mu.RLock()
	rt := f.rt
	f.mu.RUnlock()
	if rt.Engine == nil || rt.Admit == nil {
		return nil, ErrUnavailable
	}
	if err := rt.Admit(FeatureID, feature.InteractionAction, ActionDeliver, request.ActorID, request.Target); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(request.Text)
	if text == "" {
		text = "Saved response action"
	}
	buttonText := strings.TrimSpace(request.ButtonText)
	if buttonText == "" {
		buttonText = prepared.Binding().Alias
	}
	ttl := request.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return rt.Engine.Begin(ctx, orchestration.BeginRequest{
		FeatureID: FeatureID,
		ActorID:   request.ActorID,
		State:     state,
		TTL:       ttl,
		Target:    request.Target,
		View: presentation.View{
			Text: text,
			Rows: []presentation.Row{{
				{Text: buttonText, ActionID: ActionDeliver},
			}},
		},
	})
}

var _ assistantinteraction.FeatureDriver = (*Feature)(nil)
var _ interface{ FeatureSpec() feature.Spec } = (*Feature)(nil)
