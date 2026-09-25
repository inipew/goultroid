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
	"github.com/inipew/goultroid/internal/tasks"
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
	actionFormatOpus   = "format_opus"
	actionFormatMP4    = "format_mp4"
	actionFormatBest   = "format_best"
	actionVideo360     = "video_360"
	actionVideo480     = "video_480"
	actionVideo720     = "video_720"
	actionVideo1080    = "video_1080"
	actionVideo1440    = "video_1440"
	actionVideo2160    = "video_2160"
	actionDownloadFile = "download_file"
	actionBack         = "back"
	actionRetry        = "retry"
	actionCancel       = "cancel"
)

var interactiveActions = []string{
	actionAudio,
	actionVideo,
	actionFormatM4A,
	actionFormatMP3,
	actionFormatOpus,
	actionFormatMP4,
	actionFormatBest,
	actionVideo360,
	actionVideo480,
	actionVideo720,
	actionVideo1080,
	actionVideo1440,
	actionVideo2160,
	actionDownloadFile,
	actionBack,
	actionRetry,
	actionCancel,
}

type interactivePhase string

const (
	phaseChoose      interactivePhase = "choose"
	phaseAudioFormat interactivePhase = "audio_format"
	phaseVideoFormat interactivePhase = "video_format"
	phaseRunning     interactivePhase = "running"
	phaseFailed      interactivePhase = "failed"
)

type interactiveState struct {
	URL         string               `json:"u"`
	Provider    string               `json:"p"`
	Phase       interactivePhase     `json:"s"`
	Mode        download.MediaMode   `json:"m,omitempty"`
	Format      download.MediaFormat `json:"f,omitempty"`
	MaxHeight   int                  `json:"h,omitempty"`
	QualityMask uint16               `json:"q,omitempty"`
	TaskRoot    string               `json:"t,omitempty"`
}

type downloadPreparation struct {
	State interactiveState
}

type probePreparation struct {
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
						Scope:     scope,
						State:     downloadPreparation{State: state},
						AckPolicy: rootinteraction.AckImmediate,
					}, nil
				},
				handler,
			)
		} else if actionID == actionVideo {
			registration, err = rt.Engine.RegisterPreparedAction(
				scope,
				p.Name(),
				actionID,
				func(_ context.Context, action rootinteraction.Action) (rootinteraction.ActionAdmission, error) {
					state, err := decodeInteractiveState(action.Session.State)
					if err != nil {
						return rootinteraction.ActionAdmission{}, err
					}
					if state.Provider != "extractor" || state.Phase != phaseChoose {
						return rootinteraction.ActionAdmission{}, fmt.Errorf("%w: video probe action is stale or invalid", core.ErrInvalidArgs)
					}
					return rootinteraction.ActionAdmission{
						Scope: scope,
						Profile: tasks.ExecutionProfile{
							Resources: []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
						},
						State:     probePreparation{State: state},
						AckPolicy: rootinteraction.AckImmediate,
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

	for _, actionID := range []string{actionAudio, actionVideo, actionBack, actionCancel} {
		if err := register(actionID, false); err != nil {
			for i := len(registrations) - 1; i >= 0; i-- {
				registrations[i].Close()
			}
			return nil, fmt.Errorf("downloader: register action %s: %w", actionID, err)
		}
	}
	for _, actionID := range []string{
		actionRetry,
		actionFormatM4A,
		actionFormatMP3,
		actionFormatOpus,
		actionFormatMP4,
		actionFormatBest,
		actionVideo360,
		actionVideo480,
		actionVideo720,
		actionVideo1080,
		actionVideo1440,
		actionVideo2160,
		actionDownloadFile,
	} {
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
		prepared, ok := ctx.Preparation().(probePreparation)
		if !ok || prepared.State.URL != state.URL || prepared.State.Provider != state.Provider || prepared.State.Phase != state.Phase {
			err := fmt.Errorf("%w: downloader probe preparation is stale", core.ErrUnavailable)
			_ = ctx.Edit(failedView(err))
			ctx.Cancel()
			return err
		}
		p.ensureRegistry()
		if p.registry == nil {
			err := fmt.Errorf("%w: downloader registry unavailable", core.ErrUnavailable)
			_ = ctx.Edit(failedView(err))
			ctx.Cancel()
			return err
		}
		probe, err := p.registry.Probe(ctx.Context(), state.URL, download.ProbeOptions{Timeout: download.DefaultProbeTimeout})
		if err != nil {
			_ = ctx.Edit(failedView(err))
			ctx.Cancel()
			return err
		}
		state.Phase = phaseVideoFormat
		state.Mode = download.MediaModeVideo
		state.Format = ""
		state.QualityMask = qualityMask(probe.VideoQualities)
		encoded, err := encodeInteractiveState(state)
		if err != nil {
			return err
		}
		return ctx.Transition(encoded, interactiveTTL, videoFormatView(state.URL, probe))
	case actionBack:
		if state.Provider != "extractor" || (state.Phase != phaseAudioFormat && state.Phase != phaseVideoFormat) {
			return ctx.Answer("There is no previous downloader step.", true)
		}
		state.Phase = phaseChoose
		state.Mode = download.MediaModeDefault
		state.Format = download.MediaFormatDefault
		state.MaxHeight = 0
		state.QualityMask = 0
		encoded, err := encodeInteractiveState(state)
		if err != nil {
			return err
		}
		return ctx.Transition(encoded, interactiveTTL, extractorChoiceView(state.URL))
	case actionCancel:
		if err := ctx.Edit(cancelledView()); err != nil {
			return err
		}
		ctx.Cancel()
		if state.Phase == phaseRunning && strings.TrimSpace(state.TaskRoot) != "" && p.tasks != nil {
			_ = p.cancelInteractivePipeline(state.TaskRoot)
		}
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
	case actionRetry:
		if state.Phase != phaseFailed {
			return fmt.Errorf("%w: retry action is stale or invalid", core.ErrInvalidArgs)
		}
		if state.Provider == "http" {
			if state.Mode != download.MediaModeDefault || state.Format != download.MediaFormatDefault || state.MaxHeight != 0 {
				return fmt.Errorf("%w: direct download retry selection is invalid", core.ErrInvalidArgs)
			}
		} else if state.Mode == download.MediaModeDefault || state.Format == download.MediaFormatDefault {
			return fmt.Errorf("%w: extractor retry selection is invalid", core.ErrInvalidArgs)
		}
		if state.MaxHeight > 0 && !qualityMaskHas(state.QualityMask, state.MaxHeight) {
			return fmt.Errorf("%w: retry video quality is no longer available", core.ErrInvalidArgs)
		}
	case actionDownloadFile:
		if state.Provider != "http" || state.Phase != phaseChoose {
			return fmt.Errorf("%w: direct file action is not valid for this source", core.ErrInvalidArgs)
		}
	case actionFormatM4A, actionFormatMP3, actionFormatOpus:
		if state.Provider != "extractor" || state.Phase != phaseAudioFormat || state.Mode != download.MediaModeAudio {
			return fmt.Errorf("%w: audio format action is stale or invalid", core.ErrInvalidArgs)
		}
	case actionFormatMP4, actionFormatBest, actionVideo360, actionVideo480, actionVideo720, actionVideo1080, actionVideo1440, actionVideo2160:
		if state.Provider != "extractor" || state.Phase != phaseVideoFormat || state.Mode != download.MediaModeVideo {
			return fmt.Errorf("%w: video format action is stale or invalid", core.ErrInvalidArgs)
		}
		if height := actionVideoHeight(actionID); height > 0 && !qualityMaskHas(state.QualityMask, height) {
			return fmt.Errorf("%w: selected video quality is no longer available", core.ErrInvalidArgs)
		}
	default:
		return fmt.Errorf("%w: unknown downloader final action", core.ErrInvalidArgs)
	}
	return nil
}

func (p *Plugin) executeInteractiveDownload(ctx *orchestration.Context, state interactiveState, actionID string) error {
	if err := p.validateFinalAction(state, actionID); err != nil {
		_ = ctx.Edit(failedView(err))
		ctx.Cancel()
		return err
	}
	prepared, ok := ctx.Preparation().(downloadPreparation)
	if !ok || prepared.State.URL != state.URL || prepared.State.Provider != state.Provider || prepared.State.Phase != state.Phase {
		err := fmt.Errorf("%w: downloader prepared state is stale", core.ErrUnavailable)
		_ = ctx.Edit(failedView(err))
		ctx.Cancel()
		return err
	}

	mode, format, maxHeight := state.Mode, state.Format, state.MaxHeight
	if actionID != actionRetry {
		mode, format, maxHeight, err = finalSelection(actionID)
		if err != nil {
			return err
		}
	}
	state.Phase = phaseRunning
	state.Mode = mode
	state.Format = format
	state.MaxHeight = maxHeight
	state.TaskRoot = string(p.nextTaskID("interactive"))
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
	terminalTransition, err := ctx.PrepareTransition()
	if err != nil {
		ctx.Cancel()
		return err
	}
	cancelSession, err := ctx.PrepareCancel()
	if err != nil {
		ctx.Cancel()
		return err
	}
	failedState := state
	failedState.Phase = phaseFailed
	failedState.TaskRoot = ""
	failedEncoded, err := encodeInteractiveState(failedState)
	if err != nil {
		ctx.Cancel()
		return err
	}
	downloadFailure := func(editCtx context.Context) error {
		return terminalTransition(editCtx, failedEncoded, interactiveTTL, failedRetryView(failedState))
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
		ctx.Context(),
		state,
		mode,
		format,
		delivery,
		progressEdit,
		downloadFailure,
		deliveryFailure,
		delivered,
		cancelSession,
		targetKind,
	); err != nil {
		_ = ctx.Edit(failedView(err))
		ctx.Cancel()
		return err
	}
	return nil
}

func finalSelection(actionID string) (download.MediaMode, download.MediaFormat, int, error) {
	switch actionID {
	case actionDownloadFile:
		return download.MediaModeDefault, download.MediaFormatDefault, 0, nil
	case actionFormatM4A:
		return download.MediaModeAudio, download.MediaFormatM4A, 0, nil
	case actionFormatMP3:
		return download.MediaModeAudio, download.MediaFormatMP3, 0, nil
	case actionFormatOpus:
		return download.MediaModeAudio, download.MediaFormatOpus, 0, nil
	case actionFormatMP4:
		return download.MediaModeVideo, download.MediaFormatMP4, 0, nil
	case actionVideo360:
		return download.MediaModeVideo, download.MediaFormatMP4, 360, nil
	case actionVideo480:
		return download.MediaModeVideo, download.MediaFormatMP4, 480, nil
	case actionVideo720:
		return download.MediaModeVideo, download.MediaFormatMP4, 720, nil
	case actionVideo1080:
		return download.MediaModeVideo, download.MediaFormatMP4, 1080, nil
	case actionVideo1440:
		return download.MediaModeVideo, download.MediaFormatMP4, 1440, nil
	case actionVideo2160:
		return download.MediaModeVideo, download.MediaFormatMP4, 2160, nil
	case actionFormatBest:
		return download.MediaModeVideo, download.MediaFormatBest, 0, nil
	default:
		return "", "", 0, fmt.Errorf("%w: unknown downloader selection", core.ErrInvalidArgs)
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
		Text: "<b>Choose audio format</b>\n\n<code>" + core.EscapeHTML(rawURL) + "</code>",
		Rows: []presentation.Row{
			{{Text: "MP3", ActionID: actionFormatMP3}, {Text: "M4A", ActionID: actionFormatM4A}, {Text: "Opus", ActionID: actionFormatOpus}},
			{{Text: "‹ Back", ActionID: actionBack}, {Text: "✖ Cancel", ActionID: actionCancel}},
		},
	}
}

func videoFormatView(rawURL string, probe download.ProbeResult) presentation.View {
	lines := []string{"<b>Choose video quality</b>"}
	if title := strings.TrimSpace(probe.Title); title != "" {
		lines = append(lines, "", "<b>Title:</b> "+core.EscapeHTML(title))
	}
	if performer := strings.TrimSpace(probe.Performer); performer != "" {
		lines = append(lines, "<b>Channel:</b> "+core.EscapeHTML(performer))
	}
	if probe.DurationSeconds > 0 {
		lines = append(lines, "<b>Duration:</b> <code>"+formatSearchDuration(probe.DurationSeconds)+"</code>")
	}
	lines = append(lines, "", "<code>"+core.EscapeHTML(rawURL)+"</code>", "", "Available MP4 qualities:")

	rows := make([]presentation.Row, 0, 6)
	current := presentation.Row{}
	for _, quality := range probe.VideoQualities {
		actionID := videoHeightAction(quality.Height)
		if actionID == "" {
			continue
		}
		label := fmt.Sprintf("%dp", quality.Height)
		if quality.Size > 0 {
			label += " • ~" + formatBytes(quality.Size)
		}
		current = append(current, presentation.Button{Text: label, ActionID: actionID})
		if len(current) == 2 {
			rows = append(rows, current)
			current = presentation.Row{}
		}
	}
	if len(current) > 0 {
		rows = append(rows, current)
	}
	if len(probe.VideoQualities) > 0 {
		rows = append(rows, presentation.Row{{Text: "MP4 Auto", ActionID: actionFormatMP4}, {Text: "⭐ Best (native)", ActionID: actionFormatBest}})
	} else {
		rows = append(rows, presentation.Row{{Text: "⭐ Best (native)", ActionID: actionFormatBest}})
	}
	rows = append(rows, presentation.Row{{Text: "‹ Back", ActionID: actionBack}, {Text: "✖ Cancel", ActionID: actionCancel}})
	return presentation.View{Text: strings.Join(lines, "\n"), Rows: rows}
}

func videoHeightAction(height int) string {
	switch height {
	case 360:
		return actionVideo360
	case 480:
		return actionVideo480
	case 720:
		return actionVideo720
	case 1080:
		return actionVideo1080
	case 1440:
		return actionVideo1440
	case 2160:
		return actionVideo2160
	default:
		return ""
	}
}

func actionVideoHeight(actionID string) int {
	switch actionID {
	case actionVideo360:
		return 360
	case actionVideo480:
		return 480
	case actionVideo720:
		return 720
	case actionVideo1080:
		return 1080
	case actionVideo1440:
		return 1440
	case actionVideo2160:
		return 2160
	default:
		return 0
	}
}

func qualityBit(height int) uint16 {
	switch height {
	case 360:
		return 1 << 0
	case 480:
		return 1 << 1
	case 720:
		return 1 << 2
	case 1080:
		return 1 << 3
	case 1440:
		return 1 << 4
	case 2160:
		return 1 << 5
	default:
		return 0
	}
}

func qualityMask(qualities []download.VideoQuality) uint16 {
	var mask uint16
	for _, quality := range qualities {
		mask |= qualityBit(quality.Height)
	}
	return mask
}

func qualityMaskHas(mask uint16, height int) bool {
	bit := qualityBit(height)
	return bit != 0 && mask&bit != 0
}

func runningView(state interactiveState) presentation.View {
	label := "file"
	if state.Mode != download.MediaModeDefault {
		label = string(state.Mode) + " / " + string(state.Format)
		if state.MaxHeight > 0 {
			label += fmt.Sprintf(" / ≤%dp", state.MaxHeight)
		}
	}
	return presentation.View{
		Text: "⬇️ <b>Downloading...</b>\n\n<b>Format:</b> <code>" + core.EscapeHTML(label) + "</code>",
		Rows: []presentation.Row{{{Text: "✖ Cancel", ActionID: actionCancel}}},
	}
}

func failedRetryView(state interactiveState) presentation.View {
	label := "file"
	if state.Mode != download.MediaModeDefault {
		label = string(state.Mode) + " / " + string(state.Format)
		if state.MaxHeight > 0 {
			label += fmt.Sprintf(" / ≤%dp", state.MaxHeight)
		}
	}
	return presentation.View{
		Text: "❌ <b>Download failed.</b>\n\n<b>Selection:</b> <code>" + core.EscapeHTML(label) + "</code>\nRetry the same selection or close and reopen the downloader.",
		Rows: []presentation.Row{
			{{Text: "↻ Retry", ActionID: actionRetry}, {Text: "✖ Close", ActionID: actionCancel}},
		},
	}
}

func failedView(err error) presentation.View {
	return presentation.View{Text: "❌ <b>Error downloading media.</b>\nReopen the downloader and try again."}
}

func cancelledView() presentation.View {
	return presentation.View{Text: "✖ <b>Download cancelled.</b>"}
}

var _ assistantinteraction.FeatureDriver = (*Plugin)(nil)
var _ inlineservice.InlineHandlerV2 = (*interactiveInlineHandler)(nil)
