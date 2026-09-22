package pmrelayadmin

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/services/pmrelay"
)

const (
	FeatureID                 = "pmrelay_admin"
	blockedPageSize           = 5
	blockedReasonPreviewRunes = 96
)

type Feature struct {
	relay *pmrelay.Service
}

func New(relay *pmrelay.Service) *Feature {
	return &Feature{relay: relay}
}

func (*Feature) Name() string { return FeatureID }
func (*Feature) Init() error  { return nil }

func (f *Feature) Commands() []core.Command {
	surface := execution.SurfaceAssistant
	invocation := core.InvocationPolicy{Assistant: core.InvocationSelfOnly}
	return []core.Command{
		{
			Name:        "relay",
			Aliases:     []string{"pmrelay"},
			Description: "Inspect PM Relay status and manage visitor/force-sub policy",
			Usage:       "/relay [status|ban|unban|blocked|forcesub]",
			Category:    "Assistant",
			Permission:  core.PermissionOwner,
			Invocation:  invocation,
			Surfaces:    surface,
			PrivateOnly: true,
			Handler:     f.handleRelay,
		},
		{
			Name:        "who",
			Description: "Show the visitor behind a relayed owner message",
			Usage:       "reply to a relayed visitor message with /who",
			Category:    "Assistant",
			Permission:  core.PermissionOwner,
			Invocation:  invocation,
			Surfaces:    surface,
			PrivateOnly: true,
			ReplyOnly:   true,
			Handler:     f.handleWho,
		},
	}
}

func (*Feature) FeatureSpec() feature.Spec {
	return feature.Spec{
		ID:          FeatureID,
		Name:        "PM Relay Control",
		Description: "Owner-only canonical control plane for Assistant PM Relay.",
		Category:    "Assistant",
	}
}

func (f *Feature) handleRelay(ctx *core.Context) error {
	if f == nil || f.relay == nil {
		return pmrelay.ErrUnavailable
	}
	if ctx == nil {
		return nil
	}
	if len(ctx.Args) == 0 || strings.EqualFold(ctx.Args[0], "status") {
		return f.replyStatus(ctx)
	}

	switch strings.ToLower(strings.TrimSpace(ctx.Args[0])) {
	case "ban", "block":
		return f.handleBan(ctx)
	case "unban", "unblock":
		return f.handleUnban(ctx)
	case "blocked", "blocks":
		return f.replyBlocked(ctx)
	case "forcesub", "force-sub":
		return f.handleForceSub(ctx)
	default:
		return ctx.Reply(
			"⚠️ <b>Usage</b>\n" +
				"<code>/relay</code> — status\n" +
				"<code>/relay ban [user_id] [reason]</code> — block visitor; user_id may be omitted when replying to a relayed message\n" +
				"<code>/relay unban [user_id]</code> — unblock visitor\n" +
				"<code>/relay blocked [after_user_id]</code> — list durable blocks\n" +
				"<code>/relay forcesub</code> — force-sub status/configuration",
		)
	}
}

func (f *Feature) replyStatus(ctx *core.Context) error {
	status, err := f.relay.Status(ctx.Ctx)
	if err != nil {
		return err
	}
	forceSub, err := f.relay.ForceSubConfig(ctx.Ctx)
	if err != nil {
		return err
	}
	state := "disabled"
	if status.Enabled {
		state = "enabled"
	}
	forceSubState := "disabled"
	if forceSub.Enabled {
		forceSubState = fmt.Sprintf(
			"enabled — @%s (%s)",
			core.EscapeHTML(forceSub.ChannelUsername),
			core.EscapeHTML(string(forceSub.FailureMode)),
		)
	}
	return ctx.Reply(fmt.Sprintf(
		"📨 <b>PM Relay</b>\n\n"+
			"• <b>Status:</b> <code>%s</code>\n"+
			"• <b>Force-sub:</b> %s\n"+
			"• <b>Assistant audience:</b> <code>%d</code>\n"+
			"• <b>Blocked visitors:</b> <code>%d</code>\n"+
			"• <b>Reply mappings:</b> <code>%d</code>\n"+
			"• <b>Delivery intents:</b> <code>%d</code>\n\n"+
			"Use <code>/who</code> as a reply to a relayed visitor message.",
		state,
		forceSubState,
		status.Audience,
		status.Blocked,
		status.Mappings,
		status.Deliveries,
	))
}

func (f *Feature) handleForceSub(ctx *core.Context) error {
	if ctx == nil {
		return nil
	}
	args := ctx.Args[1:]
	if len(args) == 0 || strings.EqualFold(args[0], "status") {
		config, err := f.relay.ForceSubConfig(ctx.Ctx)
		if err != nil {
			return err
		}
		if !config.Enabled {
			return ctx.Reply(
				"🔓 <b>Force-sub disabled</b>\n\n" +
					"Configure with <code>/relay forcesub set @channel [closed|open] [join_url]</code>.",
			)
		}
		return ctx.Reply(fmt.Sprintf(
			"🔒 <b>Force-sub enabled</b>\n\n"+
				"• <b>Channel:</b> @%s\n"+
				"• <b>Join URL:</b> %s\n"+
				"• <b>Verification errors:</b> <code>fail-%s</code>\n"+
				"• <b>Revision:</b> <code>%d</code>",
			core.EscapeHTML(config.ChannelUsername),
			core.EscapeHTML(config.JoinURL),
			core.EscapeHTML(string(config.FailureMode)),
			config.Revision,
		))
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "off", "disable", "disabled":
		config, err := f.relay.ConfigureForceSub(
			ctx.Ctx,
			false,
			"",
			"",
			pmrelay.ForceSubFailClosed,
		)
		if err != nil {
			return err
		}
		return ctx.Reply(fmt.Sprintf(
			"🔓 <b>Force-sub disabled</b>\n• <b>Revision:</b> <code>%d</code>",
			config.Revision,
		))

	case "set", "on", "enable":
		if len(args) < 2 {
			return ctx.Reply(
				"⚠️ Usage: <code>/relay forcesub set @channel [closed|open] [join_url]</code>",
			)
		}
		username := args[1]
		mode := pmrelay.ForceSubFailClosed
		if len(args) >= 3 {
			switch strings.ToLower(strings.TrimSpace(args[2])) {
			case "closed", "fail-closed":
				mode = pmrelay.ForceSubFailClosed
			case "open", "fail-open":
				mode = pmrelay.ForceSubFailOpen
			default:
				return ctx.Reply("⚠️ Failure mode must be <code>closed</code> or <code>open</code>.")
			}
		}
		var joinURL string
		if len(args) >= 4 {
			joinURL = strings.TrimSpace(args[3])
		}
		config, err := f.relay.ConfigureForceSub(
			ctx.Ctx,
			true,
			username,
			joinURL,
			mode,
		)
		if err != nil {
			if errors.Is(err, pmrelay.ErrInvalidForceSubConfig) {
				return ctx.Reply(
					"⚠️ Invalid force-sub config. Use a public Telegram channel username and optional <code>https://t.me/...</code> join URL.",
				)
			}
			return err
		}
		return ctx.Reply(fmt.Sprintf(
			"🔒 <b>Force-sub configured</b>\n"+
				"• <b>Channel:</b> @%s\n"+
				"• <b>Verification errors:</b> <code>fail-%s</code>\n"+
				"• <b>Revision:</b> <code>%d</code>",
			core.EscapeHTML(config.ChannelUsername),
			core.EscapeHTML(string(config.FailureMode)),
			config.Revision,
		))

	default:
		return ctx.Reply(
			"⚠️ <b>Force-sub usage</b>\n" +
				"<code>/relay forcesub</code> — status\n" +
				"<code>/relay forcesub set @channel [closed|open] [join_url]</code> — enable/configure\n" +
				"<code>/relay forcesub off</code> — disable",
		)
	}
}

func (f *Feature) resolveVisitorTarget(ctx *core.Context, args []string) (int64, int, error) {
	if len(args) > 0 {
		if id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64); err == nil && id > 0 {
			return id, 1, nil
		}
	}
	if ctx != nil && ctx.Message != nil && ctx.Message.ReplyToID > 0 {
		details, err := f.relay.VisitorDetails(ctx.Ctx, ctx.Message.ReplyToID)
		if err != nil {
			return 0, 0, err
		}
		return details.Mapping.VisitorUserID, 0, nil
	}
	return 0, 0, pmrelay.ErrMappingNotFound
}

func (f *Feature) handleBan(ctx *core.Context) error {
	visitorID, consumed, err := f.resolveVisitorTarget(ctx, ctx.Args[1:])
	if err != nil {
		if errors.Is(err, pmrelay.ErrMappingNotFound) || errors.Is(err, pmrelay.ErrMappingExpired) {
			return ctx.Reply("⚠️ Reply to a live relayed visitor message or provide <code>user_id</code>.")
		}
		return err
	}
	reasonArgs := ctx.Args[1+consumed:]
	reason := strings.TrimSpace(strings.Join(reasonArgs, " "))
	block, err := f.relay.BlockVisitor(ctx.Ctx, visitorID, reason)
	if err != nil {
		return err
	}
	reasonLine := ""
	if block.Reason != "" {
		reasonLine = fmt.Sprintf("\n• <b>Reason:</b> %s", core.EscapeHTML(block.Reason))
	}
	return ctx.Reply(fmt.Sprintf(
		"🚫 <b>Visitor blocked</b>\n• <b>User ID:</b> <code>%d</code>%s",
		block.VisitorUserID,
		reasonLine,
	))
}

func (f *Feature) handleUnban(ctx *core.Context) error {
	visitorID, _, err := f.resolveVisitorTarget(ctx, ctx.Args[1:])
	if err != nil {
		if errors.Is(err, pmrelay.ErrMappingNotFound) || errors.Is(err, pmrelay.ErrMappingExpired) {
			return ctx.Reply("⚠️ Reply to a live relayed visitor message or provide <code>user_id</code>.")
		}
		return err
	}
	removed, err := f.relay.UnblockVisitor(ctx.Ctx, visitorID)
	if err != nil {
		return err
	}
	if !removed {
		return ctx.Reply(fmt.Sprintf(
			"ℹ️ Visitor <code>%d</code> is not blocked.",
			visitorID,
		))
	}
	return ctx.Reply(fmt.Sprintf(
		"✅ <b>Visitor unblocked</b>\n• <b>User ID:</b> <code>%d</code>",
		visitorID,
	))
}

func (f *Feature) replyBlocked(ctx *core.Context) error {
	var after int64
	if len(ctx.Args) > 1 {
		value, err := strconv.ParseInt(strings.TrimSpace(ctx.Args[1]), 10, 64)
		if err != nil || value < 0 {
			return ctx.Reply("⚠️ <code>after_user_id</code> must be a non-negative numeric Telegram user ID.")
		}
		after = value
	}
	blocks, err := f.relay.ListVisitorBlocks(ctx.Ctx, after, blockedPageSize)
	if err != nil {
		return err
	}
	if len(blocks) == 0 {
		return ctx.Reply("ℹ️ No blocked PM Relay visitors in this page.")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🚫 <b>Blocked PM Relay Visitors</b>\n\n")
	for _, block := range blocks {
		fmt.Fprintf(&sb, "• <code>%d</code> — %s", block.VisitorUserID, block.BlockedAt.UTC().Format(time.RFC3339))
		if block.Reason != "" {
			fmt.Fprintf(&sb, " — %s", core.EscapeHTML(previewReason(block.Reason)))
		}
		sb.WriteByte('\n')
	}
	if len(blocks) == blockedPageSize {
		fmt.Fprintf(&sb, "\nNext page: <code>/relay blocked %d</code>", blocks[len(blocks)-1].VisitorUserID)
	}
	return ctx.Reply(sb.String())
}

func previewReason(reason string) string {
	reason = strings.TrimSpace(reason)
	runes := []rune(reason)
	if len(runes) <= blockedReasonPreviewRunes {
		return reason
	}
	return string(runes[:blockedReasonPreviewRunes]) + "…"
}

func (f *Feature) handleWho(ctx *core.Context) error {
	if f == nil || f.relay == nil {
		return pmrelay.ErrUnavailable
	}
	if ctx == nil || ctx.Message == nil || ctx.Message.ReplyToID <= 0 {
		return ctx.Reply("⚠️ Reply to a relayed visitor message with <code>/who</code>.")
	}
	details, err := f.relay.VisitorDetails(ctx.Ctx, ctx.Message.ReplyToID)
	if err != nil {
		switch {
		case errors.Is(err, pmrelay.ErrMappingNotFound):
			return ctx.Reply("⚠️ That message is not a PM Relay visitor mapping.")
		case errors.Is(err, pmrelay.ErrMappingExpired):
			return ctx.Reply("⚠️ That PM Relay mapping has expired.")
		default:
			return err
		}
	}

	state := "allowed"
	var policyDetails string
	if details.Block != nil {
		state = "blocked"
		policyDetails = fmt.Sprintf(
			"\n• <b>Blocked at:</b> %s",
			details.Block.BlockedAt.UTC().Format(time.RFC3339),
		)
		if details.Block.Reason != "" {
			policyDetails += fmt.Sprintf("\n• <b>Block reason:</b> %s", core.EscapeHTML(details.Block.Reason))
		}
	}

	audienceDetails := ""
	if details.Audience != nil {
		audienceDetails = fmt.Sprintf(
			"\n• <b>First seen:</b> %s\n• <b>Last seen:</b> %s",
			details.Audience.FirstSeenAt.UTC().Format(time.RFC3339),
			details.Audience.LastSeenAt.UTC().Format(time.RFC3339),
		)
	}

	return ctx.Reply(fmt.Sprintf(
		"👤 <b>PM Relay Visitor</b>\n\n"+
			"• <b>User ID:</b> <code>%d</code>\n"+
			"• <b>Visitor message:</b> <code>%d</code>\n"+
			"• <b>Owner relay message:</b> <code>%d</code>\n"+
			"• <b>Policy:</b> <code>%s</code>%s%s",
		details.Mapping.VisitorUserID,
		details.Mapping.VisitorMessageID,
		details.Mapping.OwnerMessageID,
		state,
		policyDetails,
		audienceDetails,
	))
}

var _ interface{ FeatureSpec() feature.Spec } = (*Feature)(nil)
