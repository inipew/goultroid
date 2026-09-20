package notes

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

type Plugin struct {
	db        Repository
	responses *savedresponse.Service
}

func New(db Repository, responses ...*savedresponse.Service) *Plugin {
	p := &Plugin{db: db, responses: savedresponse.NewService(nil)}
	if len(responses) > 0 && responses[0] != nil {
		p.responses = responses[0]
	}
	return p
}

func (p *Plugin) Name() string { return "notes" }

func (p *Plugin) Init() error { return nil }

func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	if p.responses == nil {
		return errors.New("notes: saved response service is not configured")
	}
	files, err := pctx.Files()
	if err != nil {
		return err
	}
	p.responses.SetFiles(files)
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name: "save", Description: "Save a rich note in this chat",
			Usage: ".save <name> <content> or reply to text/media with .save <name>",
			Category: "Notes", Permission: core.PermissionSudo,
			Resources: []tasks.ResourceRequirement{{Name: "download", Amount: 1}},
			Handler: p.handleSave,
		},
		{
			Name: "get", Description: "Retrieve a saved note by name",
			Usage: ".get <name>", Category: "Notes", Permission: core.PermissionSudo,
			Resources: []tasks.ResourceRequirement{{Name: "media", Amount: 1}},
			Handler: p.handleGet,
		},
		{Name: "notes", Description: "List all notes saved in this chat", Category: "Notes", Permission: core.PermissionSudo, Handler: p.handleList},
		{Name: "clear", Description: "Delete a saved note", Usage: ".clear <name>", Category: "Notes", Permission: core.PermissionSudo, Handler: p.handleClear},
	}
}

func (p *Plugin) getChatID(ctx *core.Context) int64 {
	if ctx.Chat != nil && ctx.Chat.ID != 0 {
		return ctx.Chat.ID
	}
	return ctx.SenderID()
}

func (p *Plugin) handleSave(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.save &lt;name&gt; &lt;content&gt;</code> or reply to text/media with <code>.save &lt;name&gt;</code>")
		return errors.New("missing arguments")
	}
	if p.responses == nil {
		return errors.New("notes: saved response service is unavailable")
	}

	noteName := strings.ToLower(strings.TrimSpace(ctx.Args[0]))
	var response savedresponse.Response
	var err error
	if len(ctx.Args) >= 2 {
		response = savedresponse.NewText(strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0])))
	} else {
		response, err = p.responses.CaptureReply(ctx)
		if err != nil {
			_ = ctx.EditOrReply(fmt.Sprintf("⚠️ Could not capture replied response: %v", err))
			return err
		}
	}
	if response.Empty() {
		_ = ctx.EditOrReply("⚠️ Saved response cannot be empty.")
		return errors.New("empty note response")
	}
	if err := savedresponse.Validate(response); err != nil {
		_ = p.responses.DeleteMedia(ctx.Ctx, response)
		_ = ctx.EditOrReply(fmt.Sprintf("⚠️ Invalid saved response: %v", err))
		return err
	}

	chatID := p.getChatID(ctx)
	previous, err := p.db.GetNote(ctx.Ctx, chatID, noteName)
	if err != nil {
		_ = p.responses.DeleteMedia(ctx.Ctx, response)
		return err
	}
	var old savedresponse.Response
	if previous != nil {
		old = previous.Response
	}
	if err := p.responses.CommitReplacement(ctx.Ctx, old, response, func() error {
		return p.db.SaveNote(ctx.Ctx, chatID, noteName, response)
	}); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to save note: %v", err))
		return err
	}
	return ctx.EditOrReply(fmt.Sprintf("📝 Note <code>%s</code> saved successfully.", html.EscapeString(noteName)))
}

func (p *Plugin) handleGet(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.get &lt;name&gt;</code>")
		return errors.New("missing note name")
	}
	if p.responses == nil {
		return errors.New("notes: saved response service is unavailable")
	}
	noteName := strings.ToLower(strings.TrimSpace(ctx.Args[0]))
	note, err := p.db.GetNote(ctx.Ctx, p.getChatID(ctx), noteName)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Error fetching note: %v", err))
		return err
	}
	if note == nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Note <code>%s</code> not found in this chat.", html.EscapeString(noteName)))
		return fmt.Errorf("note %s not found", noteName)
	}

	prepared, err := p.responses.Prepare(ctx.Ctx, note.Response, savedresponse.VarsFromContext(ctx, time.Now()))
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to prepare note: %v", err))
	}
	defer prepared.Cleanup()

	if prepared.MediaPath != "" {
		if _, err := ctx.SendMedia(prepared.MediaType, prepared.MediaPath, prepared.Caption); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to send note media: %v", err))
		}
	}
	if prepared.Text != "" {
		return ctx.EditOrReply(prepared.Text)
	}
	return nil
}

func (p *Plugin) handleList(ctx *core.Context) error {
	names, err := p.db.ListNotes(ctx.Ctx, p.getChatID(ctx))
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Error listing notes: %v", err))
		return err
	}
	if len(names) == 0 {
		return ctx.EditOrReply("ℹ️ No notes saved in this chat.")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "📝 <b>Notes in this chat (%d):</b>\n\n", len(names))
	for _, name := range names {
		fmt.Fprintf(&sb, "• <code>%s</code>\n", html.EscapeString(name))
	}
	sb.WriteString("\nUse <code>.get &lt;name&gt;</code> to view note.")
	return ctx.EditOrReply(sb.String())
}

func (p *Plugin) handleClear(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.clear &lt;name&gt;</code>")
		return errors.New("missing note name")
	}
	if p.responses == nil {
		return errors.New("notes: saved response service is unavailable")
	}
	noteName := strings.ToLower(strings.TrimSpace(ctx.Args[0]))
	chatID := p.getChatID(ctx)
	note, err := p.db.GetNote(ctx.Ctx, chatID, noteName)
	if err != nil {
		return err
	}
	if note == nil {
		return errors.New("note not found")
	}
	if err := p.responses.CommitDelete(ctx.Ctx, note.Response, func() error {
		return p.db.DeleteNote(ctx.Ctx, chatID, noteName)
	}); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to delete note: %v", err))
		return err
	}
	return ctx.EditOrReply(fmt.Sprintf("🗑️ Note <code>%s</code> deleted.", html.EscapeString(noteName)))
}
