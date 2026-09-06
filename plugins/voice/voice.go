package voice

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/ui"
	voiceSvc "github.com/inipew/goultroid/internal/voice"
)

// Plugin provides voice chat playback and stream management.
type Plugin struct {
	svc *voiceSvc.Service
}

// New creates a new voice Plugin.
func New(svc *voiceSvc.Service) *Plugin {
	return &Plugin{svc: svc}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "voice"
}

// Description returns a short description.
func (p *Plugin) Description() string {
	return "Voice chat music and media stream player"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Commands returns the list of voice chat commands.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "play",
			Description: "Play or queue an audio track in the voice chat",
			Usage:       ".play [query|url|reply]",
			Category:    "Voice",
			Permission:  core.PermissionSudo,
			Handler:     p.handlePlay,
		},
		{
			Name:        "vplay",
			Description: "Play or queue a video stream in the voice chat",
			Usage:       ".vplay [query|url|reply]",
			Category:    "Voice",
			Permission:  core.PermissionSudo,
			Handler:     p.handleVPlay,
		},
		{
			Name:        "pause",
			Description: "Pause current voice chat playback",
			Usage:       ".pause",
			Category:    "Voice",
			Permission:  core.PermissionSudo,
			Handler:     p.handlePause,
		},
		{
			Name:        "resume",
			Description: "Resume paused voice chat playback",
			Usage:       ".resume",
			Category:    "Voice",
			Permission:  core.PermissionSudo,
			Handler:     p.handleResume,
		},
		{
			Name:        "skip",
			Aliases:     []string{"next"},
			Description: "Skip the current playing track",
			Usage:       ".skip",
			Category:    "Voice",
			Permission:  core.PermissionSudo,
			Handler:     p.handleSkip,
		},
		{
			Name:        "vcstop",
			Aliases:     []string{"vcleave", "leavevc", "stopvc"},
			Description: "Stop playback and leave the voice chat",
			Usage:       ".vcstop",
			Category:    "Voice",
			Permission:  core.PermissionSudo,
			Handler:     p.handleStop,
		},
		{
			Name:        "queue",
			Aliases:     []string{"playlist"},
			Description: "Show the current voice chat playlist and playback status",
			Usage:       ".queue",
			Category:    "Voice",
			Permission:  core.PermissionEveryone,
			Handler:     p.handleQueue,
		},
		{
			Name:        "volume",
			Aliases:     []string{"vol"},
			Description: "Adjust voice chat playback volume (0-200)",
			Usage:       ".volume [0-200]",
			Category:    "Voice",
			Permission:  core.PermissionSudo,
			Handler:     p.handleVolume,
		},
		{
			Name:        "repeat",
			Aliases:     []string{"loop"},
			Description: "Set queue repeat mode (off, track, queue)",
			Usage:       ".repeat [off|track|queue]",
			Category:    "Voice",
			Permission:  core.PermissionSudo,
			Handler:     p.handleRepeat,
		},
	}
}

func (p *Plugin) handlePlay(ctx *core.Context) error {
	return p.playTrack(ctx, voiceSvc.SourceAudio)
}

func (p *Plugin) handleVPlay(ctx *core.Context) error {
	return p.playTrack(ctx, voiceSvc.SourceVideo)
}

func (p *Plugin) playTrack(ctx *core.Context, defaultType voiceSvc.SourceType) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ Voice service is not configured.")
	}

	query := strings.TrimSpace(strings.Join(ctx.Args, " "))
	resolver := p.svc.Resolver()
	if resolver == nil {
		resolver = voiceSvc.NewResolver(nil, nil)
	}

	source, err := resolver.ResolveInput(ctx.Ctx, ctx, query)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ %v", err))
	}
	if defaultType == voiceSvc.SourceVideo && source.Type == voiceSvc.SourceAudio {
		source.Type = voiceSvc.SourceVideo
	}
	source.ChatID = ctx.ChatID()
	source.RequesterID = ctx.SenderID()

	sess, enqueued, err := p.svc.Play(ctx.Ctx, ctx.ChatID(), *source)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to start playback: %v", err))
	}

	durationStr := "Live / Unknown"
	if source.Duration > 0 {
		durationStr = (source.Duration.Round(time.Second)).String()
	}

	if enqueued {
		card := ui.NewCard("Track Enqueued").
			WithIcon("🎵").
			AddField("Title", ui.Code(source.Title)).
			AddField("Artist", source.Artist).
			AddField("Duration", durationStr).
			AddField("Position in Queue", fmt.Sprintf("#%d", sess.Queue().Len())).
			WithFooter("Use .queue to see full playlist")
		return ctx.Reply(card.Render())
	}

	card := ui.NewCard("Now Playing").
		WithIcon("▶️").
		AddField("Title", ui.Code(source.Title)).
		AddField("Artist", source.Artist).
		AddField("Type", string(source.Type)).
		AddField("Duration", durationStr).
		AddField("Volume", fmt.Sprintf("%d%%", sess.Volume())).
		WithFooter("<i>Powered by GoUltroid Voice Engine</i>")
	return ctx.Reply(card.Render())
}

func (p *Plugin) handlePause(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ Voice service is not configured.")
	}
	if err := p.svc.Pause(ctx.Ctx, ctx.ChatID()); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ %v", err))
	}
	return ctx.Reply("⏸️ <b>Voice playback paused.</b> Use <code>.resume</code> to continue.")
}

func (p *Plugin) handleResume(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ Voice service is not configured.")
	}
	if err := p.svc.Resume(ctx.Ctx, ctx.ChatID()); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ %v", err))
	}
	return ctx.Reply("▶️ <b>Voice playback resumed.</b>")
}

func (p *Plugin) handleSkip(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ Voice service is not configured.")
	}
	next, err := p.svc.Skip(ctx.Ctx, ctx.ChatID())
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ %v", err))
	}

	if next == nil {
		return ctx.Reply("⏹️ <b>Queue finished.</b> Playback stopped.")
	}

	return ctx.Reply(fmt.Sprintf("⏭️ <b>Skipped to:</b> <code>%s</code>", next.Title))
}

func (p *Plugin) handleStop(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ Voice service is not configured.")
	}
	if err := p.svc.Leave(ctx.Ctx, ctx.ChatID()); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ %v", err))
	}
	return ctx.Reply("⏹️ <b>Playback stopped and left voice chat.</b>")
}

func (p *Plugin) handleQueue(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ Voice service is not configured.")
	}

	sess, err := p.svc.GetSession(ctx.ChatID())
	if err != nil || sess == nil {
		return ctx.Reply("ℹ️ No active voice chat session in this chat.")
	}

	snap := sess.Snapshot()
	card := ui.NewCard("Voice Chat Status & Queue").
		WithIcon("📻").
		AddField("Status", string(snap.State)).
		AddField("Volume", fmt.Sprintf("%d%%", snap.Volume)).
		AddField("Repeat", string(snap.RepeatMode))

	if snap.Current != nil {
		elapsedStr := snap.Elapsed.Round(time.Second).String()
		totalStr := "Live"
		if snap.Current.Duration > 0 {
			totalStr = snap.Current.Duration.Round(time.Second).String()
		}
		card.AddField("Now Playing", fmt.Sprintf("<b>%s</b> (%s / %s)", snap.Current.Title, elapsedStr, totalStr))
	} else {
		card.AddField("Now Playing", "<i>None</i>")
	}

	pending := sess.Queue().List()
	if len(pending) == 0 {
		card.AddField("Upcoming", "<i>Queue is empty.</i>")
	} else {
		var sb strings.Builder
		limit := 5
		if len(pending) < limit {
			limit = len(pending)
		}
		for i := 0; i < limit; i++ {
			t := pending[i]
			dStr := ""
			if t.Duration > 0 {
				dStr = fmt.Sprintf(" (%s)", t.Duration.Round(time.Second).String())
			}
			sb.WriteString(fmt.Sprintf("<b>%d.</b> %s%s\n", i+1, t.Title, dStr))
		}
		if len(pending) > limit {
			sb.WriteString(fmt.Sprintf("<i>... and %d more track(s)</i>", len(pending)-limit))
		}
		card.AddField("Upcoming", sb.String())
	}

	return ctx.Reply(card.Render())
}

func (p *Plugin) handleVolume(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ Voice service is not configured.")
	}

	if len(ctx.Args) == 0 {
		sess, err := p.svc.GetSession(ctx.ChatID())
		if err != nil {
			return ctx.Reply("ℹ️ No active voice chat session.")
		}
		return ctx.Reply(fmt.Sprintf("🔊 Current volume: <code>%d%%</code>", sess.Volume()))
	}

	vol, err := strconv.Atoi(ctx.Args[0])
	if err != nil || vol < 0 || vol > 200 {
		return ctx.Reply("⚠️ Please specify a valid volume between <code>0</code> and <code>200</code>.")
	}

	if err := p.svc.SetVolume(ctx.Ctx, ctx.ChatID(), vol); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ %v", err))
	}

	return ctx.Reply(fmt.Sprintf("🔊 <b>Volume set to:</b> <code>%d%%</code>", vol))
}

func (p *Plugin) handleRepeat(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ Voice service is not configured.")
	}

	if len(ctx.Args) == 0 {
		sess, err := p.svc.GetSession(ctx.ChatID())
		if err != nil {
			return ctx.Reply("ℹ️ No active voice chat session.")
		}
		return ctx.Reply(fmt.Sprintf("🔁 Current repeat mode: <code>%s</code>", sess.RepeatMode()))
	}

	mode := strings.ToLower(ctx.Args[0])
	var repMode voiceSvc.RepeatMode
	switch mode {
	case "off":
		repMode = voiceSvc.RepeatOff
	case "track", "one":
		repMode = voiceSvc.RepeatTrack
	case "queue", "all":
		repMode = voiceSvc.RepeatQueue
	default:
		return ctx.Reply("⚠️ Invalid mode. Choose: <code>off</code>, <code>track</code>, or <code>queue</code>.")
	}

	if err := p.svc.SetRepeatMode(ctx.Ctx, ctx.ChatID(), repMode); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ %v", err))
	}

	return ctx.Reply(fmt.Sprintf("🔁 <b>Repeat mode set to:</b> <code>%s</code>", repMode))
}
