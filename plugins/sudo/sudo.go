package sudo

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

type Plugin struct {
	db    database.Repository
	perms *core.Permissions
}

func New(db database.Repository, perms *core.Permissions) *Plugin {
	return &Plugin{db: db, perms: perms}
}

func (p *Plugin) Name() string { return "sudo" }

func (p *Plugin) Init() error {
	if p.db == nil {
		return fmt.Errorf("sudo: database is unavailable")
	}
	if p.perms == nil {
		return fmt.Errorf("sudo: permissions are unavailable")
	}

	users, err := p.db.GetSudoUsers(context.Background())
	if err != nil {
		return fmt.Errorf("sudo: load persisted users: %w", err)
	}

	persisted := make(map[int64]struct{}, len(users))
	for _, u := range users {
		if u.UserID != 0 && !p.perms.IsOwner(u.UserID) {
			persisted[u.UserID] = struct{}{}
		}
	}

	// Database is the persistent authority; the in-memory permission set is its projection.
	for _, id := range p.perms.ListSudo() {
		if _, ok := persisted[id]; !ok {
			p.perms.RemoveSudo(id)
		}
	}
	for id := range persisted {
		p.perms.AddSudo(id)
	}
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "addsudo",
			Description: "Add a user to the sudo users list",
			Usage:       ".addsudo <user_id> or reply to a message",
			Category:    "Admin",
			Permission:  core.PermissionOwner,
			Handler:     p.handleAddSudo,
		},
		{
			Name:        "delsudo",
			Description: "Remove a user from the sudo users list",
			Usage:       ".delsudo <user_id> or reply to a message",
			Category:    "Admin",
			Permission:  core.PermissionOwner,
			Handler:     p.handleDelSudo,
		},
		{
			Name:        "sudolist",
			Aliases:     []string{"sudos"},
			Description: "List all active sudo users",
			Category:    "Admin",
			Permission:  core.PermissionOwner,
			Handler:     p.handleSudoList,
		},
	}
}

func (p *Plugin) resolveTargetUser(ctx *core.Context) (int64, error) {
	if len(ctx.Args) > 0 {
		id, err := strconv.ParseInt(ctx.Args[0], 10, 64)
		if err == nil && id != 0 {
			return id, nil
		}
		_, targetID, err := ctx.ResolveUser(ctx.Args[0])
		if err == nil && targetID != 0 {
			return targetID, nil
		}
	}

	reply, err := ctx.GetReply()
	if err == nil && reply != nil && reply.SenderID != 0 {
		return reply.SenderID, nil
	}
	return 0, fmt.Errorf("please provide a valid user ID or reply to a user's message")
}

func (p *Plugin) handleAddSudo(ctx *core.Context) error {
	if p.db == nil || p.perms == nil {
		return ctx.EditOrReply("❌ Sudo service is not configured.")
	}

	targetID, err := p.resolveTargetUser(ctx)
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}
	if p.perms.IsOwner(targetID) {
		_ = ctx.EditOrReply("⚠️ User is already the owner!")
		return fmt.Errorf("user %d is owner", targetID)
	}
	if p.perms.IsSudo(targetID) {
		return ctx.EditOrReply(fmt.Sprintf("ℹ️ User <code>%d</code> is already a sudo user.", targetID))
	}
	if err := p.db.AddSudoUser(ctx.Ctx, targetID, ctx.SenderID()); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to add sudo user: %v", err))
		return err
	}
	p.perms.AddSudo(targetID)
	return ctx.EditOrReply(fmt.Sprintf("✅ User <code>%d</code> added to sudo users.", targetID))
}

func (p *Plugin) handleDelSudo(ctx *core.Context) error {
	if p.db == nil || p.perms == nil {
		return ctx.EditOrReply("❌ Sudo service is not configured.")
	}

	targetID, err := p.resolveTargetUser(ctx)
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}
	if p.perms.IsOwner(targetID) {
		_ = ctx.EditOrReply("⚠️ Cannot remove owner from permissions!")
		return fmt.Errorf("cannot remove owner %d", targetID)
	}
	if err := p.db.RemoveSudoUser(ctx.Ctx, targetID); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to remove sudo user: %v", err))
		return err
	}
	p.perms.RemoveSudo(targetID)
	return ctx.EditOrReply(fmt.Sprintf("🗑️ User <code>%d</code> removed from sudo users.", targetID))
}

func (p *Plugin) handleSudoList(ctx *core.Context) error {
	if p.db == nil {
		return ctx.EditOrReply("❌ Sudo database is unavailable.")
	}
	users, err := p.db.GetSudoUsers(ctx.Ctx)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to get sudo users: %v", err))
		return err
	}
	if len(users) == 0 {
		return ctx.EditOrReply("ℹ️ No dynamic sudo users registered.")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("👑 <b>Sudo Users (%d):</b>\n\n", len(users)))
	for idx, u := range users {
		sb.WriteString(fmt.Sprintf("%d. <code>%d</code> (added by <code>%d</code> on %s)\n", idx+1, u.UserID, u.AddedBy, u.AddedAt.Format("2006-01-02 15:04:05")))
	}
	return ctx.EditOrReply(sb.String())
}
