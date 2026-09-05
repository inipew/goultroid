package admin

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

// Plugin provides group administration and moderation commands.
type Plugin struct{}

// New creates a new admin plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) Name() string {
	return "admin"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "ban",
			Description: "Ban a user from the group",
			Usage:       ".ban <user_id / reply> [reason]",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleBan,
		},
		{
			Name:        "unban",
			Description: "Unban a user in the group",
			Usage:       ".unban <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleUnban,
		},
		{
			Name:        "kick",
			Description: "Kick a user from the group",
			Usage:       ".kick <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleKick,
		},
		{
			Name:        "mute",
			Description: "Mute a user in the group (optional duration, e.g. 10m, 2h, 1d)",
			Usage:       ".mute <user_id / reply> [duration]",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleMute,
		},
		{
			Name:        "unmute",
			Description: "Unmute a user in the group",
			Usage:       ".unmute <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleUnmute,
		},
		{
			Name:        "purge",
			Description: "Safely delete messages between replied message and this command (topic-aware)",
			Usage:       "reply to a message with .purge",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			ReplyOnly:   true,
			Handler:     p.handlePurge,
		},
	}
}

func (p *Plugin) handleBan(ctx *core.Context) error {
	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.Reply("⚠️ Cannot ban the owner!")
		return errors.New("cannot ban owner")
	}

	if err := ctx.Ban(targetPeer, 0); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to ban user: %v", err))
		return err
	}

	reason := ""
	if len(ctx.Args) > 1 {
		reason = fmt.Sprintf("\n<b>Reason:</b> %s", strings.Join(ctx.Args[1:], " "))
	}

	return ctx.Reply(fmt.Sprintf("🔨 Banned user <code>%d</code>.%s", targetID, reason))
}

func (p *Plugin) handleUnban(ctx *core.Context) error {
	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	if err := ctx.Unban(targetPeer); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to unban user: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("✅ Unbanned user <code>%d</code>.", targetID))
}

func (p *Plugin) handleKick(ctx *core.Context) error {
	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.Reply("⚠️ Cannot kick the owner!")
		return errors.New("cannot kick owner")
	}

	if err := ctx.Kick(targetPeer); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to kick user: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("👢 Kicked user <code>%d</code>.", targetID))
}

func (p *Plugin) handleMute(ctx *core.Context) error {
	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.Reply("⚠️ Cannot mute the owner!")
		return errors.New("cannot mute owner")
	}

	var untilDate int
	durStr := ""

	// Check for duration in args
	for _, arg := range ctx.Args {
		// skip numeric targetID arg
		if arg == strconv.FormatInt(targetID, 10) {
			continue
		}
		if d, err := parseDuration(arg); err == nil && d > 0 {
			untilDate = int(time.Now().Add(d).Unix())
			durStr = fmt.Sprintf(" for <code>%s</code>", d.String())
			break
		}
	}

	if err := ctx.Mute(targetPeer, untilDate); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to mute user: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("🔇 Muted user <code>%d</code>%s.", targetID, durStr))
}

func (p *Plugin) handleUnmute(ctx *core.Context) error {
	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	if err := ctx.Unmute(targetPeer); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to unmute user: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("🔊 Unmuted user <code>%d</code>.", targetID))
}

func (p *Plugin) handlePurge(ctx *core.Context) error {
	count, err := ctx.Purge()
	if err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to purge messages: %v", err))
		return err
	}

	topicMsg := ""
	if ctx.TopicID() > 0 {
		topicMsg = fmt.Sprintf(" in topic <code>%d</code>", ctx.TopicID())
	}

	return ctx.Reply(fmt.Sprintf("🗑️ <b>Purged %d messages successfully%s!</b>", count, topicMsg))
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
