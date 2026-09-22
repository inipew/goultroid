package client

import (
	"container/list"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"go.uber.org/zap"
)

type forceSubPolicyStub struct {
	mu     sync.Mutex
	config pmrelay.ForceSubConfig
	err    error
}

func (p *forceSubPolicyStub) ForceSubConfig(context.Context) (pmrelay.ForceSubConfig, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.config, p.err
}

func (p *forceSubPolicyStub) set(config pmrelay.ForceSubConfig) {
	p.mu.Lock()
	p.config = config
	p.mu.Unlock()
}

type forceSubAPIStub struct {
	mu               sync.Mutex
	resolveCalls     int
	participantCalls int
	channelID        int64
	accessHash       int64
	member           map[int64]bool
	resolveErr       error
	resolveNil       bool
	participantErr   error
}

func (a *forceSubAPIStub) ContactsResolveUsername(
	_ context.Context,
	req *tg.ContactsResolveUsernameRequest,
) (*tg.ContactsResolvedPeer, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolveCalls++
	if a.resolveErr != nil {
		return nil, a.resolveErr
	}
	if a.resolveNil {
		return nil, nil
	}
	channelID := a.channelID
	if channelID == 0 {
		channelID = 100
	}
	accessHash := a.accessHash
	if accessHash == 0 {
		accessHash = 900
	}
	return &tg.ContactsResolvedPeer{
		Peer: &tg.PeerChannel{ChannelID: channelID},
		Chats: []tg.ChatClass{
			&tg.Channel{ID: channelID, AccessHash: accessHash, Username: req.Username},
		},
	}, nil
}

func (a *forceSubAPIStub) ChannelsGetParticipant(
	_ context.Context,
	req *tg.ChannelsGetParticipantRequest,
) (*tg.ChannelsChannelParticipant, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.participantCalls++
	if a.participantErr != nil {
		return nil, a.participantErr
	}
	user, ok := req.Participant.(*tg.InputPeerUser)
	if !ok || user.UserID <= 0 {
		return nil, errors.New("unexpected participant peer")
	}
	if !a.member[user.UserID] {
		return nil, tgerr.New(400, "USER_NOT_PARTICIPANT")
	}
	return &tg.ChannelsChannelParticipant{
		Participant: &tg.ChannelParticipant{UserID: user.UserID},
	}, nil
}

func (a *forceSubAPIStub) calls() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resolveCalls, a.participantCalls
}

func enabledForceSubConfig(revision int64, mode pmrelay.ForceSubFailureMode) pmrelay.ForceSubConfig {
	return pmrelay.ForceSubConfig{
		Enabled:         true,
		ChannelUsername: "required_channel",
		JoinURL:         "https://t.me/required_channel",
		FailureMode:     mode,
		Revision:        revision,
		UpdatedAt:       time.Now().UTC(),
	}
}

func forceSubResolver(users ...int64) *peer.DefaultResolver {
	cache := peer.NewMemoryCache()
	for _, userID := range users {
		cache.Put(peer.PeerRecord{
			ID: userID, Kind: peer.PeerKindUser, AccessHash: userID * 100,
		})
	}
	return peer.NewResolver(cache)
}

func TestForceSubGateCachesMemberAndNonMemberSeparately(t *testing.T) {
	policy := &forceSubPolicyStub{config: enabledForceSubConfig(2, pmrelay.ForceSubFailClosed)}
	api := &forceSubAPIStub{member: map[int64]bool{42: true, 43: false}}
	gate := newTelegramForceSubGate(policy, api, forceSubResolver(42, 43), zap.NewNop())
	base := time.Now().UTC()
	gate.now = func() time.Time { return base }

	member, err := gate.Check(context.Background(), 42)
	if err != nil || !member.Allowed || member.JoinRequired {
		t.Fatalf("member decision=%+v err=%v", member, err)
	}
	memberAgain, err := gate.Check(context.Background(), 42)
	if err != nil || !memberAgain.Allowed {
		t.Fatalf("cached member decision=%+v err=%v", memberAgain, err)
	}

	nonMember, err := gate.Check(context.Background(), 43)
	if err != nil || nonMember.Allowed || !nonMember.JoinRequired {
		t.Fatalf("non-member decision=%+v err=%v", nonMember, err)
	}
	nonMemberAgain, err := gate.Check(context.Background(), 43)
	if err != nil || !nonMemberAgain.JoinRequired {
		t.Fatalf("cached non-member decision=%+v err=%v", nonMemberAgain, err)
	}

	resolveCalls, participantCalls := api.calls()
	if resolveCalls != 1 || participantCalls != 2 {
		t.Fatalf("RPC calls resolve=%d participant=%d, want 1/2", resolveCalls, participantCalls)
	}
}

func TestForceSubGateUsesShorterNegativeTTL(t *testing.T) {
	policy := &forceSubPolicyStub{config: enabledForceSubConfig(2, pmrelay.ForceSubFailClosed)}
	api := &forceSubAPIStub{member: map[int64]bool{42: false}}
	gate := newTelegramForceSubGate(policy, api, forceSubResolver(42), zap.NewNop())
	base := time.Now().UTC()
	now := base
	gate.now = func() time.Time { return now }

	if decision, err := gate.Check(context.Background(), 42); err != nil || !decision.JoinRequired {
		t.Fatalf("first non-member decision=%+v err=%v", decision, err)
	}
	now = base.Add(forceSubNonMemberTTL - time.Second)
	if decision, err := gate.Check(context.Background(), 42); err != nil || !decision.JoinRequired {
		t.Fatalf("cached non-member decision=%+v err=%v", decision, err)
	}
	_, callsBeforeExpiry := api.calls()
	if callsBeforeExpiry != 1 {
		t.Fatalf("participant calls before negative TTL expiry=%d, want 1", callsBeforeExpiry)
	}

	api.mu.Lock()
	api.member[42] = true
	api.mu.Unlock()
	now = base.Add(forceSubNonMemberTTL + time.Second)
	if decision, err := gate.Check(context.Background(), 42); err != nil || !decision.Allowed {
		t.Fatalf("post-join decision=%+v err=%v", decision, err)
	}
	_, callsAfterExpiry := api.calls()
	if callsAfterExpiry != 2 {
		t.Fatalf("participant calls after negative TTL expiry=%d, want 2", callsAfterExpiry)
	}
}

func TestForceSubGateRevisionInvalidatesChannelAndMembershipCache(t *testing.T) {
	policy := &forceSubPolicyStub{config: enabledForceSubConfig(2, pmrelay.ForceSubFailClosed)}
	api := &forceSubAPIStub{member: map[int64]bool{42: true}}
	gate := newTelegramForceSubGate(policy, api, forceSubResolver(42), zap.NewNop())

	if decision, err := gate.Check(context.Background(), 42); err != nil || !decision.Allowed {
		t.Fatalf("initial decision=%+v err=%v", decision, err)
	}
	api.mu.Lock()
	api.member[42] = false
	api.mu.Unlock()
	next := enabledForceSubConfig(3, pmrelay.ForceSubFailClosed)
	next.ChannelUsername = "other_channel"
	next.JoinURL = "https://t.me/other_channel"
	policy.set(next)

	if decision, err := gate.Check(context.Background(), 42); err != nil || !decision.JoinRequired {
		t.Fatalf("revised decision=%+v err=%v", decision, err)
	}
	resolveCalls, participantCalls := api.calls()
	if resolveCalls != 2 || participantCalls != 2 {
		t.Fatalf("revision did not invalidate caches: resolve=%d participant=%d", resolveCalls, participantCalls)
	}
}

func TestForceSubGateFailureModesAreExplicit(t *testing.T) {
	verifyErr := errors.New("telegram unavailable")
	for _, tc := range []struct {
		name        string
		mode        pmrelay.ForceSubFailureMode
		wantAllowed bool
		wantBlocked bool
		wantErr     bool
	}{
		{name: "closed", mode: pmrelay.ForceSubFailClosed, wantBlocked: true, wantErr: true},
		{name: "open", mode: pmrelay.ForceSubFailOpen, wantAllowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := &forceSubPolicyStub{config: enabledForceSubConfig(2, tc.mode)}
			api := &forceSubAPIStub{
				member:         map[int64]bool{42: true},
				participantErr: verifyErr,
			}
			gate := newTelegramForceSubGate(policy, api, forceSubResolver(42), zap.NewNop())
			decision, err := gate.Check(context.Background(), 42)
			if decision.Allowed != tc.wantAllowed ||
				decision.VerificationBlocked != tc.wantBlocked ||
				(err != nil) != tc.wantErr {
				t.Fatalf("decision=%+v err=%v", decision, err)
			}
			if tc.wantErr && !errors.Is(err, pmrelay.ErrForceSubVerify) {
				t.Fatalf("fail-closed error=%v, want %v", err, pmrelay.ErrForceSubVerify)
			}
		})
	}
}

func TestForceSubGateCacheCapacityEvictsWithoutScanning(t *testing.T) {
	policy := &forceSubPolicyStub{config: enabledForceSubConfig(2, pmrelay.ForceSubFailClosed)}
	api := &forceSubAPIStub{member: map[int64]bool{1: true, 2: true, 3: true}}
	gate := newTelegramForceSubGate(policy, api, forceSubResolver(1, 2, 3), zap.NewNop())
	gate.cacheCapacity = 2
	gate.entries = make(map[int64]*list.Element, 2)

	for _, userID := range []int64{1, 2, 3, 1} {
		if decision, err := gate.Check(context.Background(), userID); err != nil || !decision.Allowed {
			t.Fatalf("Check(%d) decision=%+v err=%v", userID, decision, err)
		}
	}
	_, participantCalls := api.calls()
	if participantCalls != 4 {
		t.Fatalf("participant calls=%d, want 4 after bounded LRU eviction", participantCalls)
	}
	if len(gate.entries) != 2 {
		t.Fatalf("cache size=%d, want 2", len(gate.entries))
	}
}

func TestParticipantMembershipClassification(t *testing.T) {
	if participantIsMember(&tg.ChannelParticipantLeft{}) {
		t.Fatal("left participant counted as member")
	}
	if participantIsMember(&tg.ChannelParticipantBanned{Left: true}) {
		t.Fatal("banned+left participant counted as member")
	}
	if !participantIsMember(&tg.ChannelParticipantBanned{Left: false}) {
		t.Fatal("restricted in-channel participant should count as member")
	}
	if !participantIsMember(&tg.ChannelParticipantCreator{}) {
		t.Fatal("creator should count as member")
	}
}


func TestForceSubGateNilResolveResponseFailsClosedWithoutPanic(t *testing.T) {
	policy := &forceSubPolicyStub{config: enabledForceSubConfig(2, pmrelay.ForceSubFailClosed)}
	api := &forceSubAPIStub{
		member:     map[int64]bool{42: true},
		resolveNil: true,
	}
	gate := newTelegramForceSubGate(policy, api, forceSubResolver(42), zap.NewNop())
	decision, err := gate.Check(context.Background(), 42)
	if !decision.VerificationBlocked || decision.Allowed ||
		!errors.Is(err, pmrelay.ErrForceSubVerify) {
		t.Fatalf("nil resolve decision=%+v err=%v", decision, err)
	}
}

func TestForceSubGuidanceCooldownIsBoundedAndRevisionAware(t *testing.T) {
	policy := &forceSubPolicyStub{config: enabledForceSubConfig(2, pmrelay.ForceSubFailClosed)}
	api := &forceSubAPIStub{member: map[int64]bool{}}
	gate := newTelegramForceSubGate(policy, api, forceSubResolver(1, 2, 3), zap.NewNop())
	gate.cacheCapacity = 2
	gate.guidance = make(map[int64]*list.Element, 2)
	base := time.Now().UTC()
	now := base
	gate.now = func() time.Time { return now }

	if !gate.ClaimGuidance(1, 2) {
		t.Fatal("first guidance claim rejected")
	}
	if gate.ClaimGuidance(1, 2) {
		t.Fatal("duplicate guidance claim was not throttled")
	}
	if !gate.ClaimGuidance(2, 2) {
		t.Fatal("second visitor guidance claim rejected")
	}
	if !gate.ClaimGuidance(3, 2) {
		t.Fatal("bounded guidance cache failed to evict oldest entry")
	}
	if len(gate.guidance) != 2 {
		t.Fatalf("guidance cache size=%d, want 2", len(gate.guidance))
	}

	// User 1 was evicted by capacity and may be guided again.
	if !gate.ClaimGuidance(1, 2) {
		t.Fatal("evicted guidance entry remained throttled")
	}

	now = base.Add(forceSubGuidanceCooldown + time.Second)
	if !gate.ClaimGuidance(1, 2) {
		t.Fatal("expired guidance cooldown remained throttled")
	}

	// A config revision change invalidates all guidance throttles immediately.
	if !gate.ClaimGuidance(1, 3) {
		t.Fatal("config revision did not invalidate guidance cooldown")
	}
}


func TestForceSubGateVerificationCapacityUsesConfiguredFailureMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        pmrelay.ForceSubFailureMode
		wantAllowed bool
		wantBlocked bool
		wantErr     bool
	}{
		{name: "closed", mode: pmrelay.ForceSubFailClosed, wantBlocked: true, wantErr: true},
		{name: "open", mode: pmrelay.ForceSubFailOpen, wantAllowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := &forceSubPolicyStub{config: enabledForceSubConfig(2, tc.mode)}
			api := &forceSubAPIStub{member: map[int64]bool{42: true}}
			gate := newTelegramForceSubGate(policy, api, forceSubResolver(42), zap.NewNop())
			gate.verificationSlots = make(chan struct{}, 1)
			gate.verificationSlots <- struct{}{}

			decision, err := gate.Check(context.Background(), 42)
			if decision.Allowed != tc.wantAllowed ||
				decision.VerificationBlocked != tc.wantBlocked ||
				(err != nil) != tc.wantErr {
				t.Fatalf("saturated verification decision=%+v err=%v", decision, err)
			}
			if tc.wantErr && !errors.Is(err, pmrelay.ErrForceSubVerify) {
				t.Fatalf("saturated fail-closed error=%v, want %v", err, pmrelay.ErrForceSubVerify)
			}
			if _, participantCalls := api.calls(); participantCalls != 0 {
				t.Fatalf("saturated verifier issued participant RPCs=%d, want 0", participantCalls)
			}
		})
	}
}

func TestForceSubGuidanceDueDoesNotMutateCooldown(t *testing.T) {
	policy := &forceSubPolicyStub{config: enabledForceSubConfig(2, pmrelay.ForceSubFailClosed)}
	gate := newTelegramForceSubGate(
		policy,
		&forceSubAPIStub{member: map[int64]bool{}},
		forceSubResolver(42),
		zap.NewNop(),
	)
	base := time.Now().UTC()
	now := base
	gate.now = func() time.Time { return now }

	if !gate.GuidanceDue(42, 2) || !gate.GuidanceDue(42, 2) {
		t.Fatal("GuidanceDue mutated cooldown before execution claim")
	}
	if !gate.ClaimGuidance(42, 2) {
		t.Fatal("first ClaimGuidance rejected")
	}
	if gate.GuidanceDue(42, 2) {
		t.Fatal("guidance remained due inside cooldown")
	}
	now = base.Add(forceSubGuidanceCooldown + time.Second)
	if !gate.GuidanceDue(42, 2) {
		t.Fatal("guidance did not become due after cooldown")
	}
}


func TestForceSubGuidanceCarriesExplicitJoinButton(t *testing.T) {
	api := &mockTelegramAPI{}
	transport := &telegramRelayVisitorTransport{
		resolver:    forceSubResolver(42),
		interaction: interaction.NewClientInteraction(api, zap.NewNop()),
	}
	config := enabledForceSubConfig(2, pmrelay.ForceSubFailClosed)

	if err := transport.SendForceSubGuidance(
		context.Background(),
		42,
		config,
		false,
	); err != nil {
		t.Fatalf("SendForceSubGuidance() error=%v", err)
	}
	req := api.sendMsgReq
	if req == nil {
		t.Fatal("guidance did not send a Telegram message")
	}
	if !strings.Contains(req.Message, "Membership required") ||
		!strings.Contains(req.Message, "@required_channel") ||
		!strings.Contains(req.Message, "send your message again") {
		t.Fatalf("guidance message=%q", req.Message)
	}
	markup, ok := req.ReplyMarkup.(*tg.ReplyInlineMarkup)
	if !ok || markup == nil || len(markup.Rows) != 1 ||
		len(markup.Rows[0].Buttons) != 1 {
		t.Fatalf("guidance markup=%T %+v", req.ReplyMarkup, req.ReplyMarkup)
	}
	button, ok := markup.Rows[0].Buttons[0].(*tg.KeyboardButtonURL)
	if !ok || button.Text != "Join channel" || button.URL != config.JoinURL {
		t.Fatalf("guidance button=%T %+v", markup.Rows[0].Buttons[0], markup.Rows[0].Buttons[0])
	}
}

func TestForceSubFailClosedGuidanceExplainsVerificationOutage(t *testing.T) {
	api := &mockTelegramAPI{}
	transport := &telegramRelayVisitorTransport{
		resolver:    forceSubResolver(42),
		interaction: interaction.NewClientInteraction(api, zap.NewNop()),
	}
	if err := transport.SendForceSubGuidance(
		context.Background(),
		42,
		enabledForceSubConfig(2, pmrelay.ForceSubFailClosed),
		true,
	); err != nil {
		t.Fatalf("SendForceSubGuidance(unavailable) error=%v", err)
	}
	if api.sendMsgReq == nil ||
		!strings.Contains(api.sendMsgReq.Message, "verification unavailable") ||
		!strings.Contains(api.sendMsgReq.Message, "fail-closed") {
		t.Fatalf("verification guidance=%+v", api.sendMsgReq)
	}
}
