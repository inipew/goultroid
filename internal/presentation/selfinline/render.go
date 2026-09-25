package selfinline

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

const (
	maxQueryBytes  = 4096
	maxOffsetBytes = 512
	maxResults     = 50
)

var (
	ErrUnavailable    = errors.New("self-inline render bridge unavailable")
	ErrInlineDisabled = errors.New("assistant inline mode is disabled; enable it with @BotFather /setinline, then restart Goultroid")
	ErrNoResults      = errors.New("self-inline query returned no selectable result")
)

// Transport is the narrow MTProto boundary required by the self-inline bridge.
// Production is implemented by telegram.Service so both operations remain
// behind the shared RPC executor and limiter.
type Transport interface {
	QueryInlineBot(context.Context, string, tg.InputPeerClass, string, string) (*tg.MessagesBotResults, error)
	SendInlineBotResult(context.Context, tg.InputPeerClass, int64, string, int64, int, int, bool, bool) error
}

// UsernameProvider is the compatibility form for callers that only need a
// username. New production wiring should use IdentityProvider so capability
// preflight errors can fail closed before querying Telegram.
type UsernameProvider func() string

// IdentityProvider returns the currently usable Assistant inline identity.
// It is evaluated for every render so readiness/capability changes never leave
// stale process-local bot identity inside feature code.
type IdentityProvider func() (string, error)

// Renderer is the feature-facing production contract used by callback-heavy
// userbot features. It deliberately exposes no raw Telegram API/client.
type Renderer interface {
	Render(context.Context, Request) (Result, error)
}

// Request describes one own-Assistant inline query followed by selection of
// exactly one returned result into Peer.
type Request struct {
	Peer        tg.InputPeerClass
	Query       string
	Offset      string
	ResultID    string
	ResultIndex int
	ReplyToID   int
	TopicID     int
	Silent      bool
	HideVia     bool
}

// Result identifies the exact Telegram inline occurrence selected by Render.
type Result struct {
	QueryID  int64
	ResultID string
	RandomID int64
}

// RenderBridge is stateless. Session ownership remains in Inline vNext / P1;
// this bridge only queries the current Assistant and inserts one result.
type RenderBridge struct {
	transport Transport
	identity  IdentityProvider
}

func New(transport Transport, username UsernameProvider) *RenderBridge {
	if username == nil {
		return NewWithIdentity(transport, nil)
	}
	return NewWithIdentity(transport, func() (string, error) {
		return username(), nil
	})
}

func NewWithIdentity(transport Transport, identity IdentityProvider) *RenderBridge {
	return &RenderBridge{transport: transport, identity: identity}
}

func (b *RenderBridge) Render(ctx context.Context, request Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || b.transport == nil || b.identity == nil {
		return Result{}, renderFailure(RenderStagePreflight, false, ErrUnavailable)
	}
	if request.Peer == nil {
		return Result{}, renderFailure(RenderStagePreflight, false, fmt.Errorf("%w: destination peer is required", core.ErrInvalidArgs))
	}
	query := strings.TrimSpace(request.Query)
	if query == "" || len(query) > maxQueryBytes {
		return Result{}, renderFailure(RenderStagePreflight, false, fmt.Errorf("%w: inline query must contain 1..%d bytes", core.ErrInvalidArgs, maxQueryBytes))
	}
	offset := strings.TrimSpace(request.Offset)
	if len(offset) > maxOffsetBytes {
		return Result{}, renderFailure(RenderStagePreflight, false, fmt.Errorf("%w: inline offset exceeds %d bytes", core.ErrInvalidArgs, maxOffsetBytes))
	}
	if request.ResultIndex < 0 || request.ResultIndex >= maxResults {
		return Result{}, renderFailure(RenderStagePreflight, false, fmt.Errorf("%w: result index must be between 0 and %d", core.ErrInvalidArgs, maxResults-1))
	}
	username, identityErr := b.identity()
	if identityErr != nil {
		return Result{}, renderFailure(RenderStagePreflight, false, identityErr)
	}
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return Result{}, renderFailure(RenderStagePreflight, false, ErrUnavailable)
	}

	results, err := b.transport.QueryInlineBot(ctx, username, request.Peer, query, offset)
	if err != nil {
		return Result{}, renderFailure(RenderStageQuery, false, normalizeQueryError(err))
	}
	selectedID, err := selectResult(results, request.ResultID, request.ResultIndex)
	if err != nil {
		return Result{}, renderFailure(RenderStageSelect, false, err)
	}
	randomID, err := randomID()
	if err != nil {
		return Result{}, renderFailure(RenderStageSelect, false, fmt.Errorf("self-inline random id: %w", err))
	}

	replyToID := request.ReplyToID
	if replyToID == 0 && request.TopicID > 0 {
		replyToID = request.TopicID
	}
	if err := b.transport.SendInlineBotResult(
		ctx,
		request.Peer,
		results.QueryID,
		selectedID,
		randomID,
		replyToID,
		request.TopicID,
		request.Silent,
		request.HideVia,
	); err != nil {
		return Result{}, renderFailure(RenderStageSend, true, normalizeSendError(err))
	}
	return Result{QueryID: results.QueryID, ResultID: selectedID, RandomID: randomID}, nil
}

func selectResult(results *tg.MessagesBotResults, resultID string, index int) (string, error) {
	if results == nil || results.QueryID == 0 || len(results.Results) == 0 {
		return "", ErrNoResults
	}
	limit := len(results.Results)
	if limit > maxResults {
		limit = maxResults
	}
	resultID = strings.TrimSpace(resultID)
	if resultID != "" {
		for i := 0; i < limit; i++ {
			if results.Results[i] != nil && results.Results[i].GetID() == resultID {
				return resultID, nil
			}
		}
		return "", fmt.Errorf("%w: result id %q not found", ErrNoResults, resultID)
	}
	if index >= limit || results.Results[index] == nil {
		return "", fmt.Errorf("%w: result index %d unavailable", ErrNoResults, index)
	}
	selected := strings.TrimSpace(results.Results[index].GetID())
	if selected == "" {
		return "", fmt.Errorf("%w: selected result has empty id", ErrNoResults)
	}
	return selected, nil
}

func randomID() (int64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	value := int64(binary.LittleEndian.Uint64(raw[:]))
	if value == 0 {
		value = 1
	}
	return value, nil
}

var _ Renderer = (*RenderBridge)(nil)
