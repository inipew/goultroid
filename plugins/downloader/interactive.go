package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/download"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
)

const (
	interactiveInlineID  = "interactive"
	interactiveTTL       = 24 * time.Hour
	maxInteractiveURLLen = 2048
	maxInteractiveState  = 2304

	actionAudio        = "select_audio"
	actionVideo        = "select_video"
	actionFormatM4A    = "format_m4a"
	actionFormatMP3    = "format_mp3"
	actionFormatMP4    = "format_mp4"
	actionFormatBest   = "format_best"
	actionDownloadFile = "download_file"
	actionCancel       = "cancel"
)

var interactiveActions = []string{
	actionAudio,
	actionVideo,
	actionFormatM4A,
	actionFormatMP3,
	actionFormatMP4,
	actionFormatBest,
	actionDownloadFile,
	actionCancel,
}

type interactivePhase string

const (
	phaseChoose      interactivePhase = "choose"
	phaseAudioFormat interactivePhase = "audio_format"
	phaseVideoFormat interactivePhase = "video_format"
	phaseRunning     interactivePhase = "running"
)

type interactiveState struct {
	URL      string               `json:"u"`
	Provider string               `json:"p"`
	Phase    interactivePhase     `json:"s"`
	Mode     download.MediaMode   `json:"m,omitempty"`
	Format   download.MediaFormat `json:"f,omitempty"`
}

type downloadPreparation struct {
	State interactiveState
}

func (p *Plugin) Description() string {
	return "Bounded media downloader with TaskEngine-backed interactive format selection"
}

func (p *Plugin) FeatureSpec() feature.Spec {
	policy := feature.SudoPolicy(execution.SurfaceInline)
	interactions := []feature.Interaction{{
		ID:          interactiveInlineID,
		Kind:        feature.InteractionInline,
		Description: "Interactive media downloader",
		Surfaces:    execution.SurfaceInline,
		Policy:      policy,
	}}
	for _, actionID := range interactiveActions {
		interactions = append(interactions, feature.Interaction{
			ID:          actionID,
			Kind:        feature.InteractionAction,
			Description: "Downloader typed action",
			Surfaces:    execution.SurfaceInline,
			Policy:      policy,
		})
	}
	return feature.Spec{
		ID:           p.Name(),
		Name:         "Downloader",
		Description:  p.Description(),
		Category:     "Media",
		Interactions: interactions,
	}
}

func (p *Plugin) InlineBindings() []inlineservice.Binding {
	return []inlineservice.Binding{{
		InteractionID: interactiveInlineID,
		Handler:       &interactiveInlineHandler{plugin: p},
		Priority:      30,
	}}
}

func (p *Plugin) AssistantFeatureID() string { return p.Name() }

func (p *Plugin) BindAssistant(rt assistantinteraction.DriverRuntime) (func(), error) {
	if p == nil || rt.Engine == nil || rt.Catalog == nil || rt.Admit == nil {
		return nil, orchestration.ErrInvalidEngine
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope.IsZero() {
		return nil, fmt.Errorf("downloader: feature scope unavailable")
	}

	registrations := make([]interface{ Close() }, 0, len(interactiveActions))
	register := func(actionID string, prepared bool) error {
		handler := func(ctx *orchestration.Context) error {
			if ctx == nil {
				return orchestration.ErrInvalidEngine
			}
			session := ctx.Session()
			if err := rt.Admit(p.Name(), feature.InteractionAction, actionID, session.Binding.ActorID, ctx.Target()); err != nil {
				return err
			}
			return p.handleInteractiveAction(ctx, actionID)
		}

		var (
			registration interface{ Close() }
			err          error
		)
		if prepared {
			registration, err = rt.Engine.RegisterPreparedAction(
				scope,
				p.Name(),
				actionID,
				func(_ context.Context, action rootinteraction.Action) (rootinteraction.ActionAdmission, error) {
					state, err := decodeInteractiveState(action.Session.State)
					if err != nil {
						return rootinteraction.ActionAdmission{}, err
					}
					if err := p.validateFinalAction(state, actionID); err != nil {
						return rootinteraction.ActionAdmission{}, err
					}
					return rootinteraction.ActionAdmission{
						Scope: scope,
						State: downloadPreparation{State: state},
					}, nil
				},
				handler,
			)
		} else {
			registration, err = rt.Engine.RegisterAction(scope, p.Name(), actionID, handler)
		}
		if err != nil {
			return err
		}
		registrations = append(registrations, registration)
		return nil
	}

	for _, actionID := range []string{actionAudio, actionVideo, actionCancel} {
		if err := register(actionID, false); err != nil {
			for i := len(registrations) - 1; i >= 0; i-- {
				registrations[i].Close()
			}
			return nil, fmt.Errorf("downloader: register action %s: %w", actionID, err)
		}
	}
	for _, actionID := range []string{actionFormatM4A, actionFormatMP3, actionFormatMP4, actionFormatBest, actionDownloadFile} {
		if err := register(actionID, true); err != nil {
			for i := len(registrations) - 1; i >= 0; i-- {
				registrations[i].Close()
			}
			return nil, fmt.Errorf("downloader: register prepared action %s: %w", actionID, err)
		}
	}

	return func() {
		for i := len(registrations) - 1; i >= 0; i-- {
			registrations[i].Close()
		}
	}, nil
}

func (*Plugin) HandleAssistantInput(*orchestration.Context, string) error { return nil }

func (p *Plugin) handleInteractiveAction(ctx *orchestration.Context, actionID string) error {
	state, err := decodeInteractiveState(ctx.State())
	if err != nil {
		ctx.Cancel()
		return ctx.Answer("Downloader session is invalid. Reopen it.", true)
	}

	switch actionID {
	case actionAudio:
		if state.Provider != "extractor" || state.Phase != phaseChoose {
			return ctx.Answer("This source does not support audio selection.", true)
		}
		state.Phase = phaseAudioFormat
		state.Mode = download.MediaModeAudio
		state.Format = ""
		encoded, err := encodeInteractiveState(state)
		if err != nil {
			return err
		}
		return ctx.Transition(encoded, interactiveTTL, audioFormatView(state.URL))
	case actionVideo:
		if state.Provider != "extractor" || state.Phase != phaseChoose {
			return ctx.Answer("This source does not support video selection.", true)
		}
		state.Phase = phaseVideoFormat
		state.Mode = download.MediaModeVideo
		state.Format = ""
		encoded, err := encodeInteractiveState(state)
		if err != nil {
			return err
		}
		return ctx.Transition(encoded, interactiveTTL, videoFormatView(state.URL))
	case actionCancel:
		if err := ctx.Edit(cancelledView()); err != nil {
			return err
		}
		ctx.Cancel()
		return nil
	default:
		return p.executeInteractiveDownload(ctx, state, actionID)
	}
}

func (p *Plugin) validateFinalAction(state interactiveState, actionID string) error {
	if _, err := normalizeInteractiveURL(state.URL); err != nil {
		return err
	}
	p.ensureRegistry()
	if p.registry == nil {
		return fmt.Errorf("%w: downloader registry unavailable", core.ErrUnavailable)
	}
	provider := p.registry.Resolve(state.URL)
	if provider == nil || provider.Name() != state.Provider {
		return fmt.Errorf("%w: downloader provider changed or disappeared", core.ErrUnavailable)
	}
	switch actionID {
	case actionDownloadFile:
		if state.Provider != "http" || state.Phase != phaseChoose {
			return fmt.Errorf("%w: direct file action is not valid for this source", core.ErrInvalidArgs)
		}
	case actionFormatM4A, actionFormatMP3:
		if state.Provider != "extractor" || state.Phase != phaseAudioFormat || state.Mode != download.MediaModeAudio {
			return fmt.Errorf("%w: audio format action is stale or invalid", core.ErrInvalidArgs)
		}
	case actionFormatMP4, actionFormatBest:
		if state.Provider != "extractor" || state.Phase != phaseVideoFormat || state.Mode != download.MediaModeVideo {
			return fmt.Errorf("%w: video format action is stale or invalid", core.ErrInvalidArgs)
		}
	default:
		return fmt.Errorf("%w: unknown downloader final action", core.ErrInvalidArgs)
	}
	return nil
}

func (p *Plugin) executeInteractiveDownload(ctx *orchestration.Context, state interactiveState, actionID string) error {
	if err := p.validateFinalAction(state, actionID); err != nil {
		return ctx.Answer(err.Error(), true)
	}
	prepared, ok := ctx.Preparation().(downloadPreparation)
	if !ok || prepared.State.URL != state.URL || prepared.State.Provider != state.Provider || prepared.State.Phase != state.Phase {
		return fmt.Errorf("%w: downloader prepared state is stale", core.ErrUnavailable)
	}

	mode, format, err := finalSelection(state, actionID)
	if err != nil {
		return err
	}
	state.Phase = phaseRunning
	state.Mode = mode
	state.Format = format
	encoded, err := encodeInteractiveState(state)
	if err != nil {
		return err
	}
	if err := ctx.Transition(encoded, interactiveTTL, runningView(state)); err != nil {
		ctx.Cancel()
		return err
	}

	delivery, err := ctx.PrepareMediaDelivery()
	if err != nil {
		_ = ctx.Edit(failedView(err))
		ctx.Cancel()
		return err
	}
	progressEdit, err := ctx.PrepareTextEdit()
	if err != nil {
		_ = ctx.Edit(failedView(err))
		ctx.Cancel()
		return err
	}
	downloadFailure, err := ctx.PrepareStaticEdit(failedView(nil))
	if err != nil {
		ctx.Cancel()
		return err
	}
	deliveryFailure, err := ctx.PrepareStaticEdit(deliveryFailedView())
	if err != nil {
		ctx.Cancel()
		return err
	}
	delivered, err := ctx.PrepareStaticEdit(deliveredView())
	if err != nil {
		ctx.Cancel()
		return err
	}
	targetKind := ""
	if target := ctx.Target(); target != nil {
		targetKind = target.PresentationTargetKind()
	}
	if err := p.submitInteractivePipeline(
		context.WithoutCancel(ctx.Context()),
		state,
		mode,
		format,
		delivery,
		progressEdit,
		downloadFailure,
		deliveryFailure,
		delivered,
		targetKind,
	); err != nil {
		_ = ctx.Edit(failedView(err))
		ctx.Cancel()
		return err
	}
	ctx.Cancel()
	return nil
}

func finalSelection(state interactiveState, actionID string) (download.MediaMode, download.MediaFormat, error) {
	switch actionID {
	case actionDownloadFile:
		return download.MediaModeDefault, download.MediaFormatDefault, nil
	case actionFormatM4A:
		return download.MediaModeAudio, download.MediaFormatM4A, nil
	case actionFormatMP3:
		return download.MediaModeAudio, download.MediaFormatMP3, nil
	case actionFormatMP4:
		return download.MediaModeVideo, download.MediaFormatMP4, nil
	case actionFormatBest:
		return download.MediaModeVideo, download.MediaFormatBest, nil
	default:
		return "", "", fmt.Errorf("%w: unknown downloader selection", core.ErrInvalidArgs)
	}
}

type interactiveInlineHandler struct {
	plugin *Plugin
}

func (*interactiveInlineHandler) Pattern() string     { return "dl" }
func (*interactiveInlineHandler) Description() string { return "Interactive media downloader" }

type downloaderMatcher struct{}

func (downloaderMatcher) Match(query string) ([]string, bool) {
	if args, ok := matchDownloaderKeyword(query, "dl"); ok {
		return args, true
	}
	return matchDownloaderKeyword(query, "yt")
}

func (*interactiveInlineHandler) Matcher() inlineservice.InlineMatcher {
	return downloaderMatcher{}
}
func (*interactiveInlineHandler) AccessPolicy() inlineservice.InlineAccessPolicy {
	return inlineservice.InlineAccessPolicy{SudoOnly: true}
}
func (*interactiveInlineHandler) CachePolicy() inlineservice.CachePolicy {
	return inlineservice.CacheNone
}

func (h *interactiveInlineHandler) HandleInline(ctx *inlineservice.InlineContext) ([]inlineservice.InlineResult, error) {
	response, err := h.HandleInlineV2(ctx)
	if err != nil {
		return nil, err
	}
	return response.Results, nil
}

func (h *interactiveInlineHandler) HandleInlineV2(ctx *inlineservice.InlineContext) (*inlineservice.InlineResponse, error) {
	if ctx == nil || h == nil || h.plugin == nil {
		return nil, core.ErrInvalidArgs
	}
	if isYouTubeSearchQuery(ctx.RawQuery) {
		return h.handleYouTubeSearch(ctx)
	}
	rawURL := strings.TrimSpace(strings.Join(ctx.Args, " "))
	if rawURL == "" {
		return interactiveInlineResponse([]inlineservice.InlineResult{{
			ID:          "downloader-help",
			Type:        inlineservice.ResultArticle,
			Title:       "Interactive downloader",
			Description: "Type: dl <https://media-url>",
			Text:        "<b>Interactive downloader</b>\nType <code>dl &lt;https://media-url&gt;</code> in inline mode.",
		}}), nil
	}
	normalized, err := normalizeInteractiveURL(rawURL)
	if err != nil {
		return nil, err
	}
	h.plugin.ensureRegistry()
	if h.plugin.registry == nil {
		return nil, fmt.Errorf("%w: downloader registry unavailable", core.ErrUnavailable)
	}
	provider := h.plugin.registry.Resolve(normalized)
	if provider == nil {
		return nil, download.ErrNoMatchingProvider
	}
	state := interactiveState{URL: normalized, Provider: provider.Name(), Phase: phaseChoose}
	encoded, err := encodeInteractiveState(state)
	if err != nil {
		return nil, err
	}
	view := directDownloadView(normalized)
	if provider.Name() == "extractor" {
		view = extractorChoiceView(normalized)
	}
	return interactiveInlineResponse([]inlineservice.InlineResult{{
		ID:               "downloader",
		Type:             inlineservice.ResultArticle,
		Title:            "Download media",
		Description:      providerDescription(provider.Name()),
		Text:             view.Text,
		ActionRows:       view.Rows,
		InteractionState: encoded,
		InteractionTTL:   interactiveTTL,
	}}), nil
}

func interactiveInlineResponse(results []inlineservice.InlineResult) *inlineservice.InlineResponse {
	return &inlineservice.InlineResponse{
		Results:   results,
		Cache:     inlineservice.CacheNone,
		CacheTime: 0,
		Private:   true,
	}
}

func normalizeInteractiveURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxInteractiveURLLen {
		return "", fmt.Errorf("%w: downloader URL must contain 1..%d bytes", core.ErrInvalidArgs, maxInteractiveURLLen)
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", fmt.Errorf("%w: downloader URL must be absolute HTTP(S)", core.ErrInvalidArgs)
	}
	return parsed.String(), nil
}

func encodeInteractiveState(state interactiveState) ([]byte, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxInteractiveState {
		return nil, fmt.Errorf("%w: downloader interaction state exceeds %d bytes", core.ErrResourceLimit, maxInteractiveState)
	}
	return encoded, nil
}

func decodeInteractiveState(raw []byte) (interactiveState, error) {
	if len(raw) == 0 || len(raw) > maxInteractiveState {
		return interactiveState{}, fmt.Errorf("%w: invalid downloader interaction state size", core.ErrInvalidArgs)
	}
	var state interactiveState
	if err := json.Unmarshal(raw, &state); err != nil {
		return interactiveState{}, fmt.Errorf("%w: invalid downloader interaction state", core.ErrInvalidArgs)
	}
	if _, err := normalizeInteractiveURL(state.URL); err != nil {
		return interactiveState{}, err
	}
	if state.Provider != "http" && state.Provider != "extractor" {
		return interactiveState{}, fmt.Errorf("%w: invalid downloader provider", core.ErrInvalidArgs)
	}
	return state, nil
}

func providerDescription(provider string) string {
	if provider == "extractor" {
		return "Choose audio/video and a bounded output format"
	}
	return "Download this direct HTTP(S) file"
}

func extractorChoiceView(rawURL string) presentation.View {
	return presentation.View{
		Text: "<b>GoUltroid Media Downloader</b>\n\n<code>" + core.EscapeHTML(rawURL) + "</code>",
		Rows: []presentation.Row{
			{{Text: "Audio", ActionID: actionAudio}, {Text: "Video", ActionID: actionVideo}},
			{{Text: "✖ Cᴀɴᴄᴇʟ", ActionID: actionCancel}},
		},
	}
}

func directDownloadView(rawURL string) presentation.View {
	return presentation.View{
		Text: "<b>GoUltroid Media Downloader</b>\n\n<code>" + core.EscapeHTML(rawURL) + "</code>",
		Rows: []presentation.Row{
			{{Text: "Dᴏᴡɴʟᴏᴀᴅ Fɪʟᴇ", ActionID: actionDownloadFile}},
			{{Text: "✖ Cᴀɴᴄᴇʟ", ActionID: actionCancel}},
		},
	}
}

func audioFormatView(rawURL string) presentation.View {
	return presentation.View{
		Text: "<code>Select Your Format.</code>",
		Rows: []presentation.Row{
			{{Text: "M4A", ActionID: actionFormatM4A}, {Text: "MP3", ActionID: actionFormatMP3}},
			{{Text: "✖ Cᴀɴᴄᴇʟ", ActionID: actionCancel}},
		},
	}
}

func videoFormatView(rawURL string) presentation.View {
	return presentation.View{
		Text: "<code>Select Your Format.</code>",
		Rows: []presentation.Row{
			{{Text: "MP4", ActionID: actionFormatMP4}, {Text: "Best", ActionID: actionFormatBest}},
			{{Text: "✖ Cᴀɴᴄᴇʟ", ActionID: actionCancel}},
		},
	}
}

func runningView(state interactiveState) presentation.View {
	label := "file"
	if state.Mode != download.MediaModeDefault {
		label = string(state.Mode) + " / " + string(state.Format)
	}
	return presentation.View{
		Text: "⬇️ <b>Downloading...</b>\n\n<b>Format:</b> <code>" + core.EscapeHTML(label) + "</code>",
	}
}

func failedView(err error) presentation.View {
	return presentation.View{Text: "❌ <b>Error downloading media.</b>\nTry again."}
}

func cancelledView() presentation.View {
	return presentation.View{Text: "✖ <b>Download cancelled.</b>"}
}

var _ assistantinteraction.FeatureDriver = (*Plugin)(nil)
var _ inlineservice.InlineHandlerV2 = (*interactiveInlineHandler)(nil)
