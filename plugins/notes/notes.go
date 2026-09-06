package notes

import (
	"errors"
	"fmt"
	"html"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

// Plugin manages chat notes.
type Plugin struct {
	db database.Repository
}

// New creates a new notes plugin instance.
func New(db database.Repository) *Plugin {
	return &Plugin{db: db}
}

func (p *Plugin) Name() string {
	return "notes"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "save",
			Description: "Save a note in this chat",
			Usage:       ".save <name> <content> or reply to a message with .save <name>",
			Category:    "Notes",
			Permission:  core.PermissionSudo,
			Handler:     p.handleSave,
		},
		{
			Name:        "get",
			Description: "Retrieve a saved note by name",
			Usage:       ".get <name>",
			Category:    "Notes",
			Permission:  core.PermissionSudo,
			Handler:     p.handleGet,
		},
		{
			Name:        "notes",
			Description: "List all notes saved in this chat",
			Category:    "Notes",
			Permission:  core.PermissionSudo,
			Handler:     p.handleList,
		},
		{
			Name:        "clear",
			Description: "Delete a saved note",
			Usage:       ".clear <name>",
			Category:    "Notes",
			Permission:  core.PermissionSudo,
			Handler:     p.handleClear,
		},
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
		_ = ctx.EditOrReply("⚠️ Usage: <code>.save &lt;name&gt; &lt;content&gt;</code> or reply to a message with <code>.save &lt;name&gt;</code>")
		return errors.New("missing arguments")
	}

	noteName := strings.ToLower(ctx.Args[0])
	var content string

	if len(ctx.Args) >= 2 {
		content = strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0]))
	} else {
		reply, err := ctx.GetReply()
		if err != nil || reply == nil || reply.Text == "" {
			_ = ctx.EditOrReply("⚠️ Please provide note content or reply to a text message.")
			return errors.New("missing note content")
		}
		content = reply.Text
	}

	chatID := p.getChatID(ctx)
	if err := p.db.SaveNote(ctx.Ctx, chatID, noteName, content); err != nil {
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

	noteName := strings.ToLower(ctx.Args[0])
	chatID := p.getChatID(ctx)

	note, err := p.db.GetNote(ctx.Ctx, chatID, noteName)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Error fetching note: %v", err))
		return err
	}
	if note == nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Note <code>%s</code> not found in this chat.", html.EscapeString(noteName)))
		return fmt.Errorf("note %s not found", noteName)
	}

	return ctx.EditOrReply(note.Content)
}

func (p *Plugin) handleList(ctx *core.Context) error {
	chatID := p.getChatID(ctx)
	names, err := p.db.ListNotes(ctx.Ctx, chatID)
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

	noteName := strings.ToLower(ctx.Args[0])
	chatID := p.getChatID(ctx)

	if err := p.db.DeleteNote(ctx.Ctx, chatID, noteName); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to delete note: %v", err))
		return err
	}

	return ctx.EditOrReply(fmt.Sprintf("🗑️ Note <code>%s</code> deleted.", html.EscapeString(noteName)))
}
