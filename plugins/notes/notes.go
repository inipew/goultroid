package notes

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	notesTelegramMessageRunes = 4096
	notesTaskTimeout          = 2 * time.Minute
)

var notesTaskSequence atomic.Uint64

type Plugin struct {
	db        Repository
	responses *savedresponse.Service
	delivery  *savedresponse.ResponseDelivery
	tasks     tasks.Client
}

func New(db Repository, responses ...*savedresponse.Service) *Plugin {
	responseService := savedresponse.NewService(nil)
	if len(responses) > 0 && responses[0] != nil {
		responseService = responses[0]
	}
	return &Plugin{
		db:        db,
		responses: responseService,
		delivery:  savedresponse.NewResponseDelivery(responseService),
	}
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
	if _, err := p.responses.ReconcilePersistentMedia(pctx, 32); err != nil {
		return fmt.Errorf("notes: reconcile persistent saved-response media: %w", err)
	}
	client, err := pctx.TaskClient()
	if err != nil {
		return fmt.Errorf("notes: initialize task client: %w", err)
	}
	p.tasks = client
	return nil
}

// SetTaskClient sets the scoped TaskEngine client used for media continuations.
func (p *Plugin) SetTaskClient(client tasks.Client) {
	p.tasks = client
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name: "save", Description: "Save a rich note in this chat",
			Usage:    ".save <name> <content> or reply to text/media with .save <name>",
			Category: "Notes", Permission: core.PermissionSudo,
			// Authored text and replied text are lightweight. Replied media is
			// admitted to TaskEngine with the download resource after inspection.
			Handler: p.handleSave,
		},
		{
			Name: "get", Description: "Retrieve a saved note by name",
			Usage: ".get <name>", Category: "Notes", Permission: core.PermissionSudo,
			// The DB lookup is lightweight. Only media-backed notes are admitted
			// to TaskEngine with the media resource.
			Handler: p.handleGet,
		},
		{Name: "notes", Description: "List all notes saved in this chat", Category: "Notes", Permission: core.PermissionSudo, Handler: p.handleList},
		{Name: "noteinfo", Description: "Show saved note response/media details", Usage: ".noteinfo <name>", Category: "Notes", Permission: core.PermissionSudo, Handler: p.handleInfo},
		{Name: "clear", Description: "Delete a saved note", Usage: ".clear <name>", Category: "Notes", Permission: core.PermissionSudo, Handler: p.handleClear},
	}
}

func (p *Plugin) getChatID(ctx *core.Context) int64 {
	if ctx.Chat != nil && ctx.Chat.ID != 0 {
		return ctx.Chat.ID
	}
	return ctx.SenderID()
}

func (p *Plugin) nextTaskID(kind string, chatID int64) tasks.TaskID {
	return tasks.TaskID(fmt.Sprintf(
		"notes:%s:%d:%d:%d",
		kind,
		chatID,
		time.Now().UnixNano(),
		notesTaskSequence.Add(1),
	))
}

func detachNotesContext(ctx *core.Context) *core.Context {
	if ctx == nil {
		return nil
	}
	cp := *ctx
	// The continuation gets its authoritative TaskEngine context immediately
	// before execution. Drop invocation-only references so a queued closure does
	// not retain unrelated plugin/runtime state.
	cp.Ctx = nil
	cp.Args = nil
	cp.RawArgs = ""
	cp.Album = nil
	cp.Chat = nil
	cp.Sender = nil
	cp.Perms = nil
	cp.Principal = nil
	cp.Resolver = nil
	cp.Localizer = nil
	cp.EventBus = nil
	cp.DelayedActions = nil
	return &cp
}

func (p *Plugin) submitContinuation(
	admissionCtx context.Context,
	kind string,
	pool tasks.PoolID,
	chatID int64,
	resources []tasks.ResourceRequirement,
	handler func(context.Context) error,
) error {
	if p.tasks == nil {
		return fmt.Errorf("%w: notes TaskEngine client is not configured", core.ErrUnavailable)
	}
	if admissionCtx == nil {
		admissionCtx = context.Background()
	}
	resources = append([]tasks.ResourceRequirement(nil), resources...)
	_, err := p.tasks.Submit(admissionCtx, tasks.WorkSpec{
		ID:               p.nextTaskID(kind, chatID),
		Pool:             pool,
		Class:            tasks.PriorityNormal,
		OrderingKey:      fmt.Sprintf("chat:%d", chatID),
		ExecutionTimeout: notesTaskTimeout,
		Resources:        resources,
		Handler: func(taskCtx context.Context) error {
			return handler(taskCtx)
		},
	})
	if err != nil {
		return fmt.Errorf("notes: submit %s continuation: %w", kind, err)
	}
	return nil
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
	chatID := p.getChatID(ctx)
	if len(ctx.Args) >= 2 {
		response := savedresponse.NewText(strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0])))
		return p.saveResponse(ctx, chatID, noteName, response)
	}

	reply, err := ctx.GetReply()
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("⚠️ Could not load replied response: %v", err))
		return err
	}
	return p.saveReply(ctx, chatID, noteName, reply)
}

func (p *Plugin) saveReply(ctx *core.Context, chatID int64, noteName string, reply *core.Message) error {
	if reply == nil {
		_ = ctx.EditOrReply("⚠️ Reply to text/media or provide note content.")
		return savedresponse.ErrReplyNotFound
	}
	if !reply.HasMedia() {
		response := savedresponse.NewPlainText(reply.Text)
		return p.saveResponse(ctx, chatID, noteName, response)
	}

	uiCtx := detachNotesContext(ctx)
	resources := []tasks.ResourceRequirement{{Name: "download", Amount: 1}}
	if err := p.submitContinuation(ctx.Ctx, "save-media", tasks.PoolID("download"), chatID, resources, func(taskCtx context.Context) error {
		taskCore := uiCtx.WithContext(taskCtx)
		response, captureErr := p.responses.CaptureReply(taskCore)
		if captureErr != nil {
			_ = taskCore.EditOrReply(fmt.Sprintf("⚠️ Could not capture replied response: %v", captureErr))
			return captureErr
		}
		return p.saveResponse(taskCore, chatID, noteName, response)
	}); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to queue media note save: %v", err))
		return err
	}
	return nil
}

func (p *Plugin) saveResponse(ctx *core.Context, chatID int64, noteName string, response savedresponse.Response) error {
	if response.Empty() {
		_ = ctx.EditOrReply("⚠️ Saved response cannot be empty.")
		return errors.New("empty note response")
	}
	if err := savedresponse.Validate(response); err != nil {
		_ = p.responses.DeleteMedia(ctx.Ctx, response)
		_ = ctx.EditOrReply(fmt.Sprintf("⚠️ Invalid saved response: %v", err))
		return err
	}

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

	vars := savedresponse.VarsFromContext(ctx, time.Now())
	response := note.Response.Clone()
	if !response.HasMedia() {
		return p.deliverResponse(ctx.Ctx, ctx, response, vars)
	}

	uiCtx := detachNotesContext(ctx)
	resources := []tasks.ResourceRequirement{{Name: "media", Amount: 1}}
	if err := p.submitContinuation(ctx.Ctx, "get-media", tasks.PoolID("general"), chatID, resources, func(taskCtx context.Context) error {
		return p.deliverResponse(taskCtx, uiCtx.WithContext(taskCtx), response, vars)
	}); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to queue note media delivery: %v", err))
		return err
	}
	return nil
}

func (p *Plugin) deliverResponse(
	prepareCtx context.Context,
	ctx *core.Context,
	response savedresponse.Response,
	vars savedresponse.TemplateVars,
) error {
	stage, err := p.delivery.Deliver(prepareCtx, response, vars, savedresponse.DeliverySink{
		SendMedia: func(mediaType, path, caption string) error {
			_, sendErr := ctx.SendMedia(mediaType, path, caption)
			return sendErr
		},
		SendText: ctx.EditOrReply,
	})
	if err == nil {
		return nil
	}
	switch stage {
	case savedresponse.DeliveryStagePrepare:
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to prepare note: %v", err))
	case savedresponse.DeliveryStageMedia:
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to send note media: %v", err))
	default:
		return err
	}
}

func (p *Plugin) handleList(ctx *core.Context) error {
	if p.db == nil {
		return errors.New("notes: database is unavailable")
	}
	chatID := p.getChatID(ctx)
	if details, ok := p.db.(DetailRepository); ok {
		notes, err := details.ListNoteDetails(ctx.Ctx, chatID)
		if err != nil {
			_ = ctx.EditOrReply(fmt.Sprintf("❌ Error listing notes: %v", err))
			return err
		}
		if len(notes) == 0 {
			return ctx.EditOrReply("ℹ️ No notes saved in this chat.")
		}
		return deliverNoteDetailsList(ctx, notes)
	}

	names, err := p.db.ListNotes(ctx.Ctx, chatID)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Error listing notes: %v", err))
		return err
	}
	if len(names) == 0 {
		return ctx.EditOrReply("ℹ️ No notes saved in this chat.")
	}
	return deliverNotesList(ctx, names)
}

func deliverNotesList(ctx *core.Context, names []string) error {
	var current strings.Builder
	currentRunes := 0
	sent := false
	flush := func() error {
		if currentRunes == 0 {
			return nil
		}
		chunk := current.String()
		current.Reset()
		currentRunes = 0
		if !sent {
			sent = true
			return ctx.EditOrReply(chunk)
		}
		return ctx.Reply(chunk)
	}
	appendFragment := func(fragment string) error {
		for _, part := range core.SplitTelegramHTML(fragment, notesTelegramMessageRunes) {
			partRunes := utf8.RuneCountInString(part)
			if currentRunes > 0 && currentRunes+partRunes > notesTelegramMessageRunes {
				if err := flush(); err != nil {
					return err
				}
			}
			current.WriteString(part)
			currentRunes += partRunes
		}
		return nil
	}

	if err := appendFragment(fmt.Sprintf("📝 <b>Notes in this chat (%d):</b>\n\n", len(names))); err != nil {
		return err
	}
	for _, name := range names {
		if err := appendFragment(fmt.Sprintf("• <code>%s</code>\n", html.EscapeString(name))); err != nil {
			return err
		}
	}
	if err := appendFragment("\nUse <code>.get &lt;name&gt;</code> to view note."); err != nil {
		return err
	}
	return flush()
}

func deliverNoteDetailsList(ctx *core.Context, notes []Note) error {
	var current strings.Builder
	currentRunes := 0
	sent := false
	flush := func() error {
		if currentRunes == 0 {
			return nil
		}
		chunk := current.String()
		current.Reset()
		currentRunes = 0
		if !sent {
			sent = true
			return ctx.EditOrReply(chunk)
		}
		return ctx.Reply(chunk)
	}
	appendFragment := func(fragment string) error {
		for _, part := range core.SplitTelegramHTML(fragment, notesTelegramMessageRunes) {
			partRunes := utf8.RuneCountInString(part)
			if currentRunes > 0 && currentRunes+partRunes > notesTelegramMessageRunes {
				if err := flush(); err != nil {
					return err
				}
			}
			current.WriteString(part)
			currentRunes += partRunes
		}
		return nil
	}

	if err := appendFragment(fmt.Sprintf("📝 <b>Notes in this chat (%d):</b>\n\n", len(notes))); err != nil {
		return err
	}
	for _, note := range notes {
		if err := appendFragment(fmt.Sprintf(
			"• <code>[%s]</code> <code>%s</code>\n",
			html.EscapeString(note.Response.Kind()),
			html.EscapeString(note.Name),
		)); err != nil {
			return err
		}
	}
	if err := appendFragment("\nUse <code>.get &lt;name&gt;</code> to view or <code>.noteinfo &lt;name&gt;</code> for details."); err != nil {
		return err
	}
	return flush()
}

func (p *Plugin) handleInfo(ctx *core.Context) error {
	if p.db == nil {
		return errors.New("notes: database is unavailable")
	}
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.noteinfo &lt;name&gt;</code>")
		return errors.New("missing note name")
	}
	noteName := strings.ToLower(strings.TrimSpace(ctx.Args[0]))
	note, err := p.db.GetNote(ctx.Ctx, p.getChatID(ctx), noteName)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Error fetching note info: %v", err))
		return err
	}
	if note == nil {
		_ = ctx.EditOrReply(fmt.Sprintf("ℹ️ Note <code>%s</code> not found in this chat.", html.EscapeString(noteName)))
		return errors.New("note not found")
	}
	info, err := savedresponse.Inspect(note.Response)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to inspect note: %v", err))
		return err
	}
	return ctx.EditOrReply(renderNoteInfo(note, info))
}

func renderNoteInfo(note *Note, info savedresponse.Inspection) string {
	if note == nil {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "📝 <b>Note Info</b>\n\n")
	fmt.Fprintf(&sb, "• <b>Name:</b> <code>%s</code>\n", html.EscapeString(note.Name))
	fmt.Fprintf(&sb, "• <b>Type:</b> <code>%s</code>\n", html.EscapeString(info.Kind))
	fmt.Fprintf(&sb, "• <b>Format:</b> <code>%s</code>\n", html.EscapeString(string(info.Format)))
	if info.HasText {
		fmt.Fprintf(&sb, "• <b>Text/Caption:</b> <code>yes</code>\n")
	} else {
		fmt.Fprintf(&sb, "• <b>Text/Caption:</b> <code>no</code>\n")
	}
	fmt.Fprintf(&sb, "• <b>Template variables:</b> %s\n", renderNoteTemplateVariables(info.Variables))
	if info.Kind != "text" {
		if info.StickerFormat != "" {
			fmt.Fprintf(&sb, "• <b>Sticker format:</b> <code>%s</code>\n", html.EscapeString(info.StickerFormat))
		}
		if info.MediaName != "" {
			fmt.Fprintf(&sb, "• <b>Media:</b> <code>%s</code>\n", html.EscapeString(info.MediaName))
		}
		if info.MIMEType != "" {
			fmt.Fprintf(&sb, "• <b>MIME:</b> <code>%s</code>\n", html.EscapeString(info.MIMEType))
		}
	}
	if !note.UpdatedAt.IsZero() {
		fmt.Fprintf(&sb, "• <b>Updated:</b> <code>%s</code>", note.UpdatedAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	return sb.String()
}

func renderNoteTemplateVariables(variables []string) string {
	if len(variables) == 0 {
		return "<code>none</code>"
	}
	parts := make([]string, 0, len(variables))
	for _, variable := range variables {
		parts = append(parts, fmt.Sprintf("<code>{%s}</code>", html.EscapeString(variable)))
	}
	return strings.Join(parts, ", ")
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
		_ = ctx.EditOrReply(fmt.Sprintf("ℹ️ Note <code>%s</code> not found in this chat.", html.EscapeString(noteName)))
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
