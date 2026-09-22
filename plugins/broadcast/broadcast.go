package broadcast

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/broadcast"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

var (
	_ execution.CapabilityProvider = (*Plugin)(nil)
)

const broadcastCaptureTimeout = 2 * time.Minute

var broadcastTaskSequence atomic.Uint64

// Plugin provides broadcast capabilities.
type Plugin struct {
	svc       *broadcast.Service
	responses *savedresponse.Service
	tasks     tasks.Client
}

// New creates a new broadcast plugin instance.
func New(svc *broadcast.Service, responses ...*savedresponse.Service) *Plugin {
	p := &Plugin{svc: svc}
	if len(responses) > 0 && responses[0] != nil {
		p.responses = responses[0]
		if svc != nil {
			svc.SetResponses(responses[0])
		}
	}
	return p
}

func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	if p.responses != nil {
		files, err := pctx.Files()
		if err != nil {
			return err
		}
		p.responses.SetFiles(files)
	}
	client, err := pctx.TaskClient()
	if err != nil {
		return fmt.Errorf("broadcast: initialize task client: %w", err)
	}
	p.tasks = client
	return nil
}

func (p *Plugin) SetTaskClient(client tasks.Client) {
	p.tasks = client
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

// Capabilities declares the capabilities provided by this plugin (§4, §28 bug16_1).
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "broadcast",
			Name:        "Broadcast",
			Description: "Mass messaging tool with target filtering",
			Category:    "Admin",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
}

// Commands returns the registered commands.
func (p *Plugin) Commands() []core.Command {
	bcastSurfaces := execution.SurfaceUserbot | execution.SurfaceAssistant
	return []core.Command{
		{
			Name:        "broadcast",
			Aliases:     []string{"gcast"},
			Description: "Broadcast a message to dialogs (-users, -groups, or -all)",
			Usage:       ".broadcast [-users|-groups|-all] <message>",
			Category:    "Admin",
			Permission:  core.PermissionOwner,
			Timeout:     30 * time.Minute,
			Surfaces:    bcastSurfaces,
			Handler:     p.handleBroadcast,
		},
		{
			Name:        "cancelbroadcast",
			Aliases:     []string{"stopbroadcast", "cancelgcast"},
			Description: "Cancel an active broadcast task",
			Usage:       ".cancelbroadcast",
			Category:    "Admin",
			Permission:  core.PermissionOwner,
			Surfaces:    bcastSurfaces,
			Handler:     p.handleCancelBroadcast,
		},
	}
}

func (p *Plugin) handleBroadcast(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ Broadcast service is not configured.")
	}

	scope := broadcast.TargetAll
	msgText := strings.TrimSpace(ctx.RawArgs)
	if len(ctx.Args) > 0 {
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
	}

	if msgText != "" {
		return p.runBroadcast(ctx, scope, savedresponse.NewText(msgText))
	}

	reply, err := ctx.GetReply()
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Could not load replied broadcast: %v", err))
	}
	if reply == nil {
		return ctx.EditOrReply("⚠️ <b>Usage:</b> <code>.broadcast [-users|-groups|-all] &lt;message&gt;</code> or reply to text/media.")
	}
	if !reply.HasMedia() {
		response := savedresponse.NewPlainText(reply.Text)
		if response.Empty() {
			return ctx.EditOrReply("⚠️ Broadcast response cannot be empty.")
		}
		return p.runBroadcast(ctx, scope, response)
	}
	if p.responses == nil || p.tasks == nil {
		return ctx.EditOrReply("⚠️ Rich broadcast media capture is unavailable.")
	}

	uiCtx := detachBroadcastContext(ctx)
	resources := []tasks.ResourceRequirement{{Name: "download", Amount: 1}}
	var response savedresponse.Response
	var captureErr error
	ticket, err := p.tasks.Submit(ctx.Ctx, tasks.WorkSpec{
		ID: tasks.TaskID(fmt.Sprintf(
			"broadcast:capture:%d:%d",
			time.Now().UnixNano(),
			broadcastTaskSequence.Add(1),
		)),
		QuotaOwner:       tasks.OwnerID("plugin:broadcast"),
		Pool:             tasks.PoolID("download"),
		Class:            tasks.PriorityNormal,
		OrderingKey:      "broadcast:capture",
		ExecutionTimeout: broadcastCaptureTimeout,
		Resources:        resources,
		Handler: func(taskCtx context.Context) error {
			taskCore := uiCtx.WithContext(taskCtx)
			response, captureErr = p.svc.CaptureReply(taskCore)
			return captureErr
		},
	})
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to queue broadcast media capture: %v", err))
	}

	result, waitErr := ticket.Wait(ctx.Ctx)
	if waitErr != nil {
		return fmt.Errorf("broadcast: wait media capture: %w", waitErr)
	}
	if captureErr != nil {
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Could not capture replied broadcast: %v", captureErr))
	}
	if !result.IsSuccess() {
		failure := strings.TrimSpace(result.Failure.Message)
		if failure == "" {
			failure = fmt.Sprintf("capture task ended with %s", result.Outcome)
		}
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Could not capture replied broadcast: %s", failure))
	}

	// TaskEngine releases download:1 before closing the ticket. Keep the
	// captured asset alive across fan-out, then reclaim it after the run.
	defer func() { _ = p.responses.DeleteMedia(context.Background(), response) }()
	return p.runBroadcast(ctx, scope, response)
}

func detachBroadcastContext(ctx *core.Context) *core.Context {
	if ctx == nil {
		return nil
	}
	cp := *ctx
	cp.Ctx = nil
	cp.Args = nil
	cp.RawArgs = ""
	cp.Album = nil
	cp.Perms = nil
	cp.Principal = nil
	cp.Resolver = nil
	cp.Localizer = nil
	cp.EventBus = nil
	cp.DelayedActions = nil
	return &cp
}

func (p *Plugin) runBroadcast(ctx *core.Context, scope broadcast.TargetType, response savedresponse.Response) error {
	if ctx.Svc == nil {
		return ctx.EditOrReply("⚠️ Telegram service is unavailable.")
	}
	if err := savedresponse.Validate(response); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Invalid broadcast response: %v", err))
	}

	_ = ctx.EditOrReply(fmt.Sprintf("📡 <i>Fetching dialogs for broadcast (scope: %s)...</i>", scope))
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
		default:
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
		Targets:  targets,
		Response: response.Clone(),
		Vars:     savedresponse.VarsFromContext(ctx, time.Now()),
		Delay:    400 * time.Millisecond,
		Progress: func(progress broadcast.BroadcastReport) {
			completed := progress.Sent + progress.Failed
			if progress.Canceled || progress.Total <= 0 || completed >= progress.Total {
				return
			}
			percent := completed * 100 / progress.Total
			_ = ctx.Edit(fmt.Sprintf(
				"🚀 <b>Broadcast in progress</b>\n\n"+
					"• <b>Progress:</b> <code>%d/%d (%d%%)</code>\n"+
					"• <b>Sent:</b> <code>%d</code>\n"+
					"• <b>Failed:</b> <code>%d</code>\n"+
					"• <b>Rate Limited:</b> <code>%d</code>\n"+
					"• <b>Elapsed:</b> <code>%v</code>",
				completed,
				progress.Total,
				percent,
				progress.Sent,
				progress.Failed,
				progress.RateLimited,
				progress.Duration.Round(time.Second),
			))
		},
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
