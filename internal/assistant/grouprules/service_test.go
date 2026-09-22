package grouprules

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
)

type ruleSourceStub struct {
	interested bool
	revision   uint64
	match      bool
	matchErr   error
	apply      bool
	applyErr   error
	matchCalls int
	applyCalls int
}

func (s *ruleSourceStub) AssistantRuleInterested(int64) bool { return s.interested }
func (s *ruleSourceStub) AssistantRuleRevision(int64) uint64 { return s.revision }
func (s *ruleSourceStub) MatchAssistantRule(context.Context, *core.MessageEnvelope) (bool, error) {
	s.matchCalls++
	return s.match, s.matchErr
}
func (s *ruleSourceStub) ApplyAssistantRule(
	context.Context,
	core.TelegramServicer,
	*core.MessageEnvelope,
) (bool, error) {
	s.applyCalls++
	return s.apply, s.applyErr
}

type roleResolverStub struct {
	principal core.GroupActorPrincipal
	err       error
	fresh     int
	cached    int
	onFresh   func()
}

func (r *roleResolverStub) ResolveGroupRole(
	context.Context,
	core.GroupRoleRequest,
) (core.GroupRoleSnapshot, error) {
	r.cached++
	return core.GroupRoleSnapshot{Principal: r.principal}, r.err
}

func (r *roleResolverStub) ResolveGroupRoleFresh(
	_ context.Context,
	req core.GroupRoleRequest,
) (core.GroupRoleSnapshot, error) {
	r.fresh++
	if r.onFresh != nil {
		r.onFresh()
	}
	principal := r.principal
	if principal.UserID == 0 {
		principal.UserID = req.UserID
	}
	return core.GroupRoleSnapshot{Principal: principal}, r.err
}

type interactionStub struct{}

func (*interactionStub) Answer(context.Context, int64, string, bool) error { return nil }
func (*interactionStub) Edit(context.Context, interaction.MessageTarget, string, tg.ReplyMarkupClass) error {
	return nil
}
func (*interactionStub) EditMarkup(context.Context, interaction.MessageTarget, tg.ReplyMarkupClass) error {
	return nil
}
func (*interactionStub) Delete(context.Context, interaction.MessageTarget) error { return nil }
func (*interactionStub) GetMessage(context.Context, interaction.MessageTarget) (*tg.Message, error) {
	return nil, nil
}
func (*interactionStub) SendMessage(
	context.Context,
	tg.InputPeerClass,
	string,
	tg.ReplyMarkupClass,
) (*tg.Message, error) {
	return &tg.Message{ID: 1}, nil
}
func (*interactionStub) SendMedia(
	context.Context,
	tg.InputPeerClass,
	string,
	string,
	string,
) (*tg.Message, error) {
	return &tg.Message{ID: 1}, nil
}

func ruleMessage(senderID int64) *core.MessageEnvelope {
	return &core.MessageEnvelope{
		ID:     9,
		ChatID: 77,
		Peer: core.PeerRef{
			Kind:       core.PeerKindChannel,
			ID:         77,
			AccessHash: 177,
		},
		Chat: core.Chat{
			ID:   77,
			Type: string(core.ChatKindSupergroup),
		},
		Sender: core.User{ID: senderID},
		Text:   "spam payload",
	}
}

func verifiedRole(role core.GroupActorRole) core.GroupActorPrincipal {
	return core.GroupActorPrincipal{Role: role, Verified: true}
}

func readyRuleService(blacklist, filters Source, roles core.GroupRoleResolver) *Service {
	service := New(blacklist, filters)
	service.SetRoleResolver(roles)
	service.SetTransport(&interactionStub{})
	return service
}

func TestP7INoRuleMatchAvoidsTelegramRoleLookup(t *testing.T) {
	blacklist := &ruleSourceStub{interested: true}
	roles := &roleResolverStub{principal: verifiedRole(core.GroupActorRoleMember)}
	service := readyRuleService(blacklist, nil, roles)

	result, err := service.Evaluate(context.Background(), ruleMessage(42))
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched || result.Handled || result.Bypassed {
		t.Fatalf("result=%+v, want untouched", result)
	}
	if blacklist.matchCalls != 1 || blacklist.applyCalls != 0 {
		t.Fatalf("blacklist match/apply=%d/%d, want 1/0", blacklist.matchCalls, blacklist.applyCalls)
	}
	if roles.fresh != 0 || roles.cached != 0 {
		t.Fatalf("role resolver touched for non-match fresh=%d cached=%d", roles.fresh, roles.cached)
	}
}

func TestP7IOwnerSudoBypassPrecedesMatcherAndRoleRPC(t *testing.T) {
	blacklist := &ruleSourceStub{interested: true, match: true, apply: true}
	filters := &ruleSourceStub{interested: true, match: true, apply: true}
	roles := &roleResolverStub{principal: verifiedRole(core.GroupActorRoleMember)}
	service := readyRuleService(blacklist, filters, roles)
	service.SetPrivilegedChecker(func(userID int64) bool { return userID == 42 })

	result, err := service.Evaluate(context.Background(), ruleMessage(42))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Bypassed || result.Matched || result.Handled {
		t.Fatalf("result=%+v, want early privileged bypass", result)
	}
	if blacklist.matchCalls != 0 || filters.matchCalls != 0 ||
		blacklist.applyCalls != 0 || filters.applyCalls != 0 {
		t.Fatalf("privileged bypass touched rules blacklist=%d/%d filters=%d/%d",
			blacklist.matchCalls, blacklist.applyCalls, filters.matchCalls, filters.applyCalls)
	}
	if roles.fresh != 0 || roles.cached != 0 {
		t.Fatalf("privileged bypass touched Telegram roles fresh=%d cached=%d", roles.fresh, roles.cached)
	}
}

func TestP7IAdministratorBypassUsesFreshRoleOnlyAfterMatch(t *testing.T) {
	blacklist := &ruleSourceStub{interested: true, match: true, apply: true}
	roles := &roleResolverStub{principal: verifiedRole(core.GroupActorRoleAdministrator)}
	service := readyRuleService(blacklist, nil, roles)

	result, err := service.Evaluate(context.Background(), ruleMessage(42))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || !result.Bypassed || result.Handled {
		t.Fatalf("result=%+v, want matched admin bypass", result)
	}
	if roles.fresh != 1 || roles.cached != 0 {
		t.Fatalf("role lookup fresh=%d cached=%d, want 1/0", roles.fresh, roles.cached)
	}
	if blacklist.applyCalls != 0 {
		t.Fatalf("admin bypass applied blacklist %d times", blacklist.applyCalls)
	}
}

func TestP7IVerificationFailureFailsSafeWithoutAutomaticAction(t *testing.T) {
	blacklist := &ruleSourceStub{interested: true, match: true, apply: true}
	roles := &roleResolverStub{err: errors.New("telegram verification unavailable")}
	service := readyRuleService(blacklist, nil, roles)

	result, err := service.Evaluate(context.Background(), ruleMessage(42))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || !result.Bypassed || result.Handled {
		t.Fatalf("result=%+v, want safe bypass", result)
	}
	if roles.fresh != 1 || blacklist.applyCalls != 0 {
		t.Fatalf("verification failure fresh=%d apply=%d, want 1/0", roles.fresh, blacklist.applyCalls)
	}
}

func TestP7IBlacklistOutranksFilterForOrdinaryMember(t *testing.T) {
	blacklist := &ruleSourceStub{interested: true, match: true, apply: true, revision: 2}
	filters := &ruleSourceStub{interested: true, match: true, apply: true, revision: 3}
	roles := &roleResolverStub{principal: verifiedRole(core.GroupActorRoleMember)}
	service := readyRuleService(blacklist, filters, roles)

	result, err := service.Evaluate(context.Background(), ruleMessage(42))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || !result.Handled || result.Bypassed || result.Domain != "blacklist" {
		t.Fatalf("result=%+v, want blacklist handled", result)
	}
	if blacklist.applyCalls != 1 || filters.applyCalls != 0 {
		t.Fatalf("apply calls blacklist=%d filters=%d, want 1/0",
			blacklist.applyCalls, filters.applyCalls)
	}
	if roles.fresh != 1 || roles.cached != 0 {
		t.Fatalf("role lookup fresh=%d cached=%d, want 1/0", roles.fresh, roles.cached)
	}
	if result.Revision != (Revision{Blacklist: 2, Filters: 3}) {
		t.Fatalf("revision=%+v", result.Revision)
	}
}

func TestP7IDisabledPluginDoesNotContributeInterestOrActions(t *testing.T) {
	blacklist := &ruleSourceStub{interested: true, match: true, apply: true}
	service := readyRuleService(
		blacklist,
		nil,
		&roleResolverStub{principal: verifiedRole(core.GroupActorRoleMember)},
	)
	service.SetEnabledChecker(func(name string) bool { return name != "blacklist" })

	if service.Interested(77) {
		t.Fatal("disabled blacklist contributed Assistant interest")
	}
	result, err := service.Evaluate(context.Background(), ruleMessage(42))
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched || result.Handled || result.Bypassed ||
		blacklist.matchCalls != 0 || blacklist.applyCalls != 0 {
		t.Fatalf("disabled source leaked into evaluation result=%+v match=%d apply=%d",
			result, blacklist.matchCalls, blacklist.applyCalls)
	}
}

func TestP7IRuleRevisionRefreshesAcrossFreshRoleWindow(t *testing.T) {
	blacklist := &ruleSourceStub{interested: true, match: true, apply: true, revision: 5}
	roles := &roleResolverStub{principal: verifiedRole(core.GroupActorRoleMember)}
	roles.onFresh = func() { blacklist.revision = 6 }
	service := readyRuleService(blacklist, nil, roles)

	result, err := service.Evaluate(context.Background(), ruleMessage(42))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Handled || result.Revision.Blacklist != 6 {
		t.Fatalf("result=%+v, want latest blacklist revision 6", result)
	}
	if blacklist.applyCalls != 1 {
		t.Fatalf("apply calls=%d, want 1 current-state re-match", blacklist.applyCalls)
	}
}

func TestP7KTopicDeliveryFailsClosedWithoutContextualTransport(t *testing.T) {
	svc := &interactionServicer{inter: &interactionStub{}}
	_, err := svc.SendMessageContext(
		context.Background(),
		&tg.InputPeerChannel{ChannelID: 77, AccessHash: 177},
		"topic reply",
		nil,
		core.MessageSendContext{ReplyToID: 9, TopicID: 5},
	)
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("topic fallback error=%v want ErrUnavailable", err)
	}
}
