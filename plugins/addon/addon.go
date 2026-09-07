package addon

import (
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/addon"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/ui"
)

// Plugin provides management commands for external GoUltroid addons.
type Plugin struct {
	mgr *addon.Manager
}

// New creates a new Addon management plugin.
func New(mgr *addon.Manager) *Plugin {
	return &Plugin{mgr: mgr}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "addon"
}

// Description returns a short description.
func (p *Plugin) Description() string {
	return "Manage external userbot addons, manifests, and capability permissions"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Commands returns the list of addon management commands.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "addon",
			Aliases:     []string{"addons"},
			Description: "Inspect, install, and manage external addons and capability permissions",
			Usage:       ".addon [list|info|install|uninstall|enable|disable]",
			Category:    "Addon",
			Permission:  core.PermissionOwner,
			Handler:     p.handleAddon,
		},
	}
}

func (p *Plugin) handleAddon(ctx *core.Context) error {
	if p.mgr == nil {
		return ctx.EditOrReply("⚠️ Addon manager is not configured.")
	}

	if len(ctx.Args) == 0 || ctx.Args[0] == "list" {
		return p.handleList(ctx)
	}

	sub := strings.ToLower(ctx.Args[0])
	switch sub {
	case "info":
		return p.handleInfo(ctx)
	case "install":
		return p.handleInstall(ctx)
	case "uninstall", "remove":
		return p.handleUninstall(ctx)
	case "enable":
		return p.handleEnable(ctx)
	case "disable":
		return p.handleDisable(ctx)
	default:
		return ctx.EditOrReply("⚠️ Unknown subcommand. Use: <code>list</code>, <code>info</code>, <code>install</code>, <code>uninstall</code>, <code>enable</code>, or <code>disable</code>.")
	}
}

func (p *Plugin) handleList(ctx *core.Context) error {
	addons, err := p.mgr.List(ctx.Ctx)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to list addons: %v", err))
	}

	if len(addons) == 0 {
		return ctx.EditOrReply("📦 <b>No external addons installed.</b>\nUse <code>.addon install &lt;manifest&gt;</code> to register an addon.")
	}

	card := ui.NewCard(fmt.Sprintf("Installed Addons (%d)", len(addons))).
		WithIcon("🧩")

	for i, a := range addons {
		statusEmoji := "🟢"
		if a.Status == string(addon.StatusDisabled) {
			statusEmoji = "🔴"
		}
		capCount := 0
		if a.Capabilities != "" {
			capCount = len(strings.Split(a.Capabilities, ","))
		}

		card.AddField(
			fmt.Sprintf("%d. %s v%s", i+1, a.Name, a.Version),
			fmt.Sprintf("%s %s | %d cap(s)", statusEmoji, a.Status, capCount),
		)
	}

	card.WithFooter("<i>Use <code>.addon info &lt;name&gt;</code> for details.</i>")
	return ctx.EditOrReply(card.Render())
}

func (p *Plugin) handleInfo(ctx *core.Context) error {
	if len(ctx.Args) < 2 {
		return ctx.EditOrReply("⚠️ Please specify the addon name: <code>.addon info &lt;name&gt;</code>")
	}

	name := ctx.Args[1]
	rec, err := p.mgr.Get(ctx.Ctx, name)
	if err != nil || rec == nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Addon <code>%s</code> not found.", core.EscapeHTML(name)))
	}

	statusEmoji := "🟢"
	if rec.Status == string(addon.StatusDisabled) {
		statusEmoji = "🔴"
	}

	authorStr := rec.Author
	if authorStr == "" {
		authorStr = "Unknown"
	}

	card := ui.NewCard(fmt.Sprintf("Addon: %s", rec.Name)).
		WithIcon("🧩").
		AddField("Version", ui.Code(rec.Version)).
		AddField("Author", authorStr).
		AddField("Status", fmt.Sprintf("%s %s", statusEmoji, rec.Status)).
		AddField("Installed", rec.InstalledAt.Format(time.RFC822))

	if rec.Description != "" {
		card.AddField("Description", rec.Description)
	}
	if rec.MinVersion != "" {
		card.AddField("Min GoUltroid", ui.Code(rec.MinVersion))
	}
	if rec.SourceURL != "" {
		card.AddField("Source", rec.SourceURL)
	}

	if rec.Capabilities != "" {
		card.AddField("Capabilities", ui.Code(strings.ReplaceAll(rec.Capabilities, ",", ", ")))
	} else {
		card.AddField("Capabilities", "<i>None (Zero Privileges)</i>")
	}

	return ctx.EditOrReply(card.Render())
}

func (p *Plugin) handleInstall(ctx *core.Context) error {
	var rawManifest string
	var sourceURL string

	// Check if manifest provided in arguments
	if len(ctx.Args) > 1 {
		rawManifest = strings.Join(ctx.Args[1:], " ")
	}

	// Check if replied message has text
	if rawManifest == "" {
		reply, err := ctx.GetReply()
		if err == nil && reply != nil && reply.Text != "" {
			rawManifest = reply.Text
		}
	}

	rawManifest = strings.TrimSpace(rawManifest)
	if rawManifest == "" {
		return ctx.EditOrReply("⚠️ Please provide manifest YAML content or reply to a manifest message:\n<code>.addon install name: example\nversion: 1.0.0\ncommands: [foo]</code>")
	}

	manifest, err := p.mgr.Install(ctx.Ctx, []byte(rawManifest), sourceURL)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to install addon: %v", err))
	}

	capStr := "None"
	if len(manifest.Capabilities) > 0 {
		var caps []string
		for _, c := range manifest.Capabilities {
			caps = append(caps, string(c))
		}
		capStr = strings.Join(caps, ", ")
	}

	card := ui.NewCard("Addon Installed Successfully").
		WithIcon("✅").
		AddField("Name", ui.Code(manifest.Name)).
		AddField("Version", ui.Code(manifest.Version)).
		AddField("Commands", strings.Join(manifest.Commands, ", ")).
		AddField("Capabilities", ui.Code(capStr)).
		WithFooter("Addon is active and capability permissions have been granted")

	return ctx.EditOrReply(card.Render())
}

func (p *Plugin) handleUninstall(ctx *core.Context) error {
	if len(ctx.Args) < 2 {
		return ctx.EditOrReply("⚠️ Please specify the addon name: <code>.addon uninstall &lt;name&gt;</code>")
	}

	name := ctx.Args[1]
	if err := p.mgr.Uninstall(ctx.Ctx, name); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to uninstall addon: %v", err))
	}

	return ctx.EditOrReply(fmt.Sprintf("🗑️ <b>Uninstalled addon:</b> <code>%s</code> (all capability permissions revoked)", core.EscapeHTML(name)))
}

func (p *Plugin) handleEnable(ctx *core.Context) error {
	if len(ctx.Args) < 2 {
		return ctx.EditOrReply("⚠️ Please specify the addon name: <code>.addon enable &lt;name&gt;</code>")
	}

	name := ctx.Args[1]
	if err := p.mgr.Enable(ctx.Ctx, name); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to enable addon: %v", err))
	}

	return ctx.EditOrReply(fmt.Sprintf("🟢 <b>Enabled addon:</b> <code>%s</code> (capability permissions restored)", core.EscapeHTML(name)))
}

func (p *Plugin) handleDisable(ctx *core.Context) error {
	if len(ctx.Args) < 2 {
		return ctx.EditOrReply("⚠️ Please specify the addon name: <code>.addon disable &lt;name&gt;</code>")
	}

	name := ctx.Args[1]
	if err := p.mgr.Disable(ctx.Ctx, name); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to disable addon: %v", err))
	}

	return ctx.EditOrReply(fmt.Sprintf("🔴 <b>Disabled addon:</b> <code>%s</code> (capability permissions revoked)", core.EscapeHTML(name)))
}
