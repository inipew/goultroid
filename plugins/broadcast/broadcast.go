package broadcast

import (
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/broadcast"
)

// Plugin provides broadcast capabilities.
type Plugin struct {
	svc *broadcast.Service
}

// New creates a new broadcast plugin instance.
func New(svc *broadcast.Service) *Plugin {
	return &Plugin{svc: svc}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "broadcast"
}

// Description returns the plugin description.
func (p *Plugin) Description() string {
	return "Mass messaging tool with FloodWait resilience and target filtering"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Commands returns the registered commands.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "broadcast",
			Aliases:     []string{"gcast"},
			Description: "Broadcast a message to dialogs (-users, -groups, or -all)",
			Usage:       ".broadcast [-users|-groups|-all] <message>",
			Category:    "Admin",
			Permission:  core.PermissionOwner,
			Timeout:     30 * time.Minute,
			Handler:     p.handleBroadcast,
		},
		{
			Name:        "cancelbroadcast",
			Aliases:     []string{"stopbroadcast", "cancelgcast"},
			Description: "Cancel an active broadcast task",
			Usage:       ".cancelbroadcast",
			Category:    "Admin",
			Permission:  core.PermissionOwner,
			Handler:     p.handleCancelBroadcast,
		},
	}
}

func (p *Plugin) handleBroadcast(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ Broadcast service is not configured.")
	}

	if len(ctx.Args) == 0 {
		return ctx.EditOrReply("⚠️ <b>Usage:</b> <code>.broadcast [-users|-groups|-all] <message></code>")
	}

	scope := broadcast.TargetAll
	msgText := ctx.RawArgs

	arg0 := strings.ToLower(ctx.Args[0])
	if strings.HasPrefix(arg0, "-") {
		switch arg0 {
		case "-users", "-user", "-u":
			scope = broadcast.TargetUsers
		case "-groups", "-group", "-g":
			scope = broadcast.TargetGroups
		case "-channels", "-channel", "-c":
			scope = broadcast.TargetChannels
		default:
			scope = broadcast.TargetAll
		}
		msgText = strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0]))
	}

	if msgText == "" {
		reply, err := ctx.GetReply()
		if err == nil && reply != nil && reply.Text != "" {
			msgText = reply.Text
		} else {
			return ctx.EditOrReply("⚠️ Message text cannot be empty. Specify text or reply to a message.")
		}
	}

	_ = ctx.EditOrReply(fmt.Sprintf("📡 <i>Fetching dialogs for broadcast (scope: %s)...</i>", scope))

	if ctx.Svc == nil {
		return ctx.EditOrReply("⚠️ Telegram service is unavailable.")
	}
	dialogs, err := ctx.Svc.GetDialogs(ctx.Ctx, 100)
	if err != nil {
		return ctx.Edit(fmt.Sprintf("❌ Failed to retrieve dialogs: %v", err))
	}

	var targets []tg.InputPeerClass
	for _, d := range dialogs {
		if d == nil {
			continue
		}
		switch scope {
		case broadcast.TargetUsers:
			if d.Type == "user" || d.Type == "private" {
				if peer := toInputPeer(d); peer != nil {
					targets = append(targets, peer)
				}
			}
		case broadcast.TargetGroups:
			if d.Type == "group" || d.Type == "supergroup" {
				if peer := toInputPeer(d); peer != nil {
					targets = append(targets, peer)
				}
			}
		case broadcast.TargetChannels:
			if d.Type == "channel" {
				if peer := toInputPeer(d); peer != nil {
					targets = append(targets, peer)
				}
			}
		default: // TargetAll
			if peer := toInputPeer(d); peer != nil {
				targets = append(targets, peer)
			}
		}
	}

	if len(targets) == 0 {
		return ctx.Edit(fmt.Sprintf("⚠️ No matching dialogs found for scope <code>%s</code>.", scope))
	}

	_ = ctx.Edit(fmt.Sprintf("🚀 <i>Starting broadcast to %d chats...</i>", len(targets)))

	rep, err := p.svc.Broadcast(ctx.Ctx, broadcast.BroadcastRequest{
		Targets: targets,
		Text:    msgText,
		Delay:   400 * time.Millisecond,
	})

	if err != nil && (rep == nil || !rep.Canceled) {
		return ctx.Edit(fmt.Sprintf("❌ Broadcast failed: %v", err))
	}

	status := "Completed"
	if rep != nil && rep.Canceled {
		status = "Canceled"
	}

	summary := fmt.Sprintf(
		"📊 <b>Broadcast %s</b>\n\n"+
			"• <b>Total Targets:</b> <code>%d</code>\n"+
			"• <b>Successfully Sent:</b> <code>%d</code>\n"+
			"• <b>Failed:</b> <code>%d</code>\n"+
			"• <b>Rate Limited / Delayed:</b> <code>%d</code>\n"+
			"• <b>Duration:</b> <code>%v</code>",
		status, rep.Total, rep.Sent, rep.Failed, rep.RateLimited, rep.Duration.Round(time.Second),
	)

	return ctx.Edit(summary)
}

func (p *Plugin) handleCancelBroadcast(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ Broadcast service is not configured.")
	}

	canceled := p.svc.CancelActive()
	if canceled {
		return ctx.EditOrReply("🛑 <b>Active broadcast cancelled!</b>")
	}
	return ctx.EditOrReply("ℹ️ No active broadcast task is currently running.")
}

func toInputPeer(d *core.Chat) tg.InputPeerClass {
	if d == nil {
		return nil
	}
	switch d.Type {
	case "private", "user":
		return &tg.InputPeerUser{UserID: d.ID, AccessHash: d.AccessHash}
	case "group":
		return &tg.InputPeerChat{ChatID: d.ID}
	case "supergroup", "channel":
		return &tg.InputPeerChannel{ChannelID: d.ID, AccessHash: d.AccessHash}
	default:
		return nil
	}
}
