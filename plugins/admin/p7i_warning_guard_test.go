package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/moderation"
	"go.uber.org/zap"
)

type p7iWarningRepo struct {
	records []*moderation.WarningRecord
}

func (r *p7iWarningRepo) AddWarning(_ context.Context, chatID, userID int64, reason string, warnedBy int64) error {
	r.records = append(r.records, &moderation.WarningRecord{
		ChatID: chatID, UserID: userID, Reason: reason, WarnedBy: warnedBy,
	})
	return nil
}

func (r *p7iWarningRepo) GetWarnings(context.Context, int64, int64) ([]*moderation.WarningRecord, error) {
	return append([]*moderation.WarningRecord(nil), r.records...), nil
}

func (r *p7iWarningRepo) GetWarningCount(_ context.Context, chatID, userID int64) (int, error) {
	count := 0
	for _, record := range r.records {
		if record.ChatID == chatID && record.UserID == userID {
			count++
		}
	}
	return count, nil
}

func (r *p7iWarningRepo) ResetWarnings(context.Context, int64, int64) error {
	r.records = nil
	return nil
}

type p7iWarningRoleResolver struct {
	calls int
}

func (r *p7iWarningRoleResolver) ResolveGroupRole(
	context.Context,
	core.GroupRoleRequest,
) (core.GroupRoleSnapshot, error) {
	return core.GroupRoleSnapshot{}, errors.New("unexpected cached role lookup")
}

func (r *p7iWarningRoleResolver) ResolveGroupRoleFresh(
	_ context.Context,
	request core.GroupRoleRequest,
) (core.GroupRoleSnapshot, error) {
	r.calls++
	role := core.GroupActorRoleMember
	if r.calls >= 2 {
		role = core.GroupActorRoleAdministrator
	}
	return core.GroupRoleSnapshot{
		Principal: core.GroupActorPrincipal{
			UserID: request.UserID,
			Role: role,
			Verified: true,
		},
	}, nil
}

func TestP7IWarningTargetPromotionBeforePersistenceIsProtected(t *testing.T) {
	repo := &p7iWarningRepo{}
	moderator := moderation.NewService(repo, nil, zap.NewNop())
	plugin := New(moderator)

	svc := &mockService{}
	ctx := newAdminTestContext(svc)
	ctx.Source = core.ExecutionAssistant
	ctx.Message = &core.Message{ID: 77}
	ctx.Args = []string{"5555", "race"}
	ctx.GroupRoles = &p7iWarningRoleResolver{}

	var warn core.Command
	for _, candidate := range plugin.Commands() {
		if candidate.Name == "warn" {
			warn = candidate
			break
		}
	}
	if warn.Handler == nil {
		t.Fatal("warn command not found")
	}

	err := warn.Handler(ctx)
	if !errors.Is(err, core.ErrGroupMutationTargetProtected) {
		t.Fatalf("warning race error=%v, want ErrGroupMutationTargetProtected", err)
	}
	if len(repo.records) != 0 {
		t.Fatalf("promoted target received %d persisted warnings", len(repo.records))
	}
	if svc.muteCalled || svc.kickCalled || svc.banCalled {
		t.Fatalf("promoted target reached punitive transport mute=%v kick=%v ban=%v",
			svc.muteCalled, svc.kickCalled, svc.banCalled)
	}
}
