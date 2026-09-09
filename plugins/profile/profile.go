package profile

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/plugin"
)

// Plugin provides self-user profile and contact management commands.
type Plugin struct {
	files *filesystem.Manager
}

// New creates a new Profile plugin.
func New() *Plugin {
	return &Plugin{}
}

// InitPlugin initializes the plugin using capability-gated PluginContext.
func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	fsMgr, err := pctx.Files()
	if err != nil {
		return err
	}
	p.files = fsMgr
	return nil
}

// SetFiles sets the filesystem manager for the plugin.
func (p *Plugin) SetFiles(fs *filesystem.Manager) {
	p.files = fs
}

func (p *Plugin) getFiles() *filesystem.Manager {
	if p.files == nil {
		p.files, _ = filesystem.NewManager("data", "", "", nil)
	}
	return p.files
}

// Name returns the unique plugin identifier.
func (p *Plugin) Name() string {
	return "profile"
}

// Description returns a brief summary of the plugin.
func (p *Plugin) Description() string {
	return "Userbot self profile, blocklist, contacts, and dialogs management"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Shutdown cleans up resources.
func (p *Plugin) Shutdown() error {
	return nil
}

// Commands registers all profile-related commands.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "me",
			Aliases:     []string{"myprofile", "whoami"},
			Description: "Display current userbot profile information",
			Usage:       ".me",
			Category:    "Profile",
			Permission:  core.PermissionSudo,
			Handler:     p.handleMe,
		},
		{
			Name:        "setbio",
			Description: "Update user profile bio / about (max 70 chars)",
			Usage:       ".setbio <text>",
			Category:    "Profile",
			Permission:  core.PermissionOwner,
			Handler:     p.handleSetBio,
		},
		{
			Name:        "setname",
			Description: "Update user profile first name and optional last name",
			Usage:       ".setname <first_name> [last_name]",
			Category:    "Profile",
			Permission:  core.PermissionOwner,
			Handler:     p.handleSetName,
		},
		{
			Name:        "setpic",
			Description: "Update profile picture from replied photo or local image path",
			Usage:       ".setpic [reply to photo | /path/to/image.jpg]",
			Category:    "Profile",
			Permission:  core.PermissionOwner,
			Handler:     p.handleSetPic,
		},
		{
			Name:        "delphoto",
			Aliases:     []string{"delpfp"},
			Description: "Delete current profile photo(s)",
			Usage:       ".delphoto [count]",
			Category:    "Profile",
			Permission:  core.PermissionOwner,
			Handler:     p.handleDelPhoto,
		},
		{
			Name:        "block",
			Description: "Block a user from sending messages or calling",
			Usage:       ".block [username|id|reply]",
			Category:    "Profile",
			Permission:  core.PermissionOwner,
			Handler:     p.handleBlock,
		},
		{
			Name:        "unblock",
			Description: "Unblock a user",
			Usage:       ".unblock [username|id|reply]",
			Category:    "Profile",
			Permission:  core.PermissionOwner,
			Handler:     p.handleUnblock,
		},
		{
			Name:        "contacts",
			Description: "Display total saved contacts and preview",
			Usage:       ".contacts",
			Category:    "Profile",
			Permission:  core.PermissionSudo,
			Handler:     p.handleContacts,
		},
		{
			Name:        "dialogs",
			Aliases:     []string{"chats"},
			Description: "List recent active dialogs/chats",
			Usage:       ".dialogs [limit]",
			Category:    "Profile",
			Permission:  core.PermissionSudo,
			Handler:     p.handleDialogs,
		},
	}
}

// handleMe displays the userbot's own detailed profile.
func (p *Plugin) handleMe(ctx *core.Context) error {
	fullUser, err := ctx.GetFullUser(&tg.InputUserSelf{})
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to fetch self info: %v", err))
	}

	var selfUser *tg.User
	for _, u := range fullUser.Users {
		if usr, ok := u.(*tg.User); ok {
			selfUser = usr
			break
		}
	}

	if selfUser == nil {
		return ctx.EditOrReply("❌ Could not parse self profile details.")
	}

	var sb strings.Builder
	sb.WriteString("🤖 <b>My Profile</b>\n\n")
	sb.WriteString(fmt.Sprintf("• <b>ID</b>: <code>%d</code>\n", selfUser.ID))
	sb.WriteString(fmt.Sprintf("• <b>First Name</b>: %s\n", core.EscapeHTML(selfUser.FirstName)))
	if selfUser.LastName != "" {
		sb.WriteString(fmt.Sprintf("• <b>Last Name</b>: %s\n", core.EscapeHTML(selfUser.LastName)))
	}
	if selfUser.Username != "" {
		sb.WriteString(fmt.Sprintf("• <b>Username</b>: @%s\n", core.EscapeHTML(selfUser.Username)))
	}
	if selfUser.Phone != "" {
		sb.WriteString(fmt.Sprintf("• <b>Phone</b>: <code>+%s</code>\n", core.EscapeHTML(selfUser.Phone)))
	}
	if selfUser.Premium {
		sb.WriteString("• <b>Premium</b>: Yes ⭐\n")
	}
	if fullUser.FullUser.About != "" {
		sb.WriteString(fmt.Sprintf("• <b>Bio</b>: <i>%s</i>\n", core.EscapeHTML(fullUser.FullUser.About)))
	}

	return ctx.EditOrReply(sb.String())
}

// handleSetBio updates the account's bio/about.
func (p *Plugin) handleSetBio(ctx *core.Context) error {
	bio := strings.TrimSpace(ctx.RawArgs)
	if bio == "" {
		return ctx.EditOrReply("⚠️ Please provide bio text: <code>.setbio <text></code>")
	}
	if len([]rune(bio)) > 70 {
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Bio text is too long (%d/70 characters).", len([]rune(bio))))
	}

	if err := ctx.UpdateProfile(nil, nil, &bio); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to update bio: %v", err))
	}

	return ctx.EditOrReply(fmt.Sprintf("✅ <b>Bio updated successfully</b>:\n<i>%s</i>", core.EscapeHTML(bio)))
}

// handleSetName updates the account's first name and optional last name.
func (p *Plugin) handleSetName(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		return ctx.EditOrReply("⚠️ Please provide a name: <code>.setname <first_name> [last_name]</code>")
	}

	firstName := ctx.Args[0]
	lastName := ""
	if len(ctx.Args) > 1 {
		lastName = strings.Join(ctx.Args[1:], " ")
	}

	if err := ctx.UpdateProfile(&firstName, &lastName, nil); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to update name: %v", err))
	}

	fullName := firstName
	if lastName != "" {
		fullName += " " + lastName
	}
	return ctx.EditOrReply(fmt.Sprintf("✅ <b>Name updated successfully to</b>: %s", core.EscapeHTML(fullName)))
}

// handleSetPic sets a new profile photo from replied photo/document or file path.
func (p *Plugin) handleSetPic(ctx *core.Context) error {
	var filePath string
	var cleanupTemp bool

	reply, err := ctx.GetReply()
	if err == nil && reply != nil && reply.HasMedia() {
		files := p.getFiles()
		tempDir, err := files.CreateTempDir("profile", "goultroid-pfp-*")
		if err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create temporary directory: %v", err))
		}
		defer func() {
			if cleanupTemp {
				_ = files.RemoveTempDir(tempDir)
			}
		}()
		cleanupTemp = true

		destDir := tempDir
		downloadedPath, err := ctx.DownloadMedia(destDir)
		if err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to download replied media: %v", err))
		}
		filePath = downloadedPath
	} else if len(ctx.Args) > 0 {
		filePath = ctx.Args[0]
		if _, err := os.Stat(filePath); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ File does not exist: <code>%s</code>", core.EscapeHTML(filePath)))
		}
	} else {
		return ctx.EditOrReply("⚠️ Reply to a photo or specify a valid image file path: <code>.setpic</code>")
	}

	if ctx.Svc == nil {
		return ctx.EditOrReply("❌ Telegram service not available.")
	}

	if err := ctx.Svc.UploadProfilePhoto(ctx.Ctx, filePath); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to set profile photo: %v", err))
	}

	return ctx.EditOrReply("✅ <b>Profile photo updated successfully!</b>")
}

// handleDelPhoto deletes current profile photo(s).
func (p *Plugin) handleDelPhoto(ctx *core.Context) error {
	limit := 1
	if len(ctx.Args) > 0 {
		if parsed, err := strconv.Atoi(ctx.Args[0]); err == nil && parsed > 0 {
			limit = parsed
			if limit > 10 {
				limit = 10
			}
		}
	}

	if ctx.Svc == nil {
		return ctx.EditOrReply("❌ Telegram service not available.")
	}

	deleted, err := ctx.Svc.DeleteProfilePhotos(ctx.Ctx, limit)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to delete profile photo: %v", err))
	}

	if deleted == 0 {
		return ctx.EditOrReply("⚠️ No profile photos found to delete.")
	}

	return ctx.EditOrReply(fmt.Sprintf("🗑️ <b>Successfully deleted %d profile photo(s).</b>", deleted))
}

// handleBlock blocks a user from PM/contacts.
func (p *Plugin) handleBlock(ctx *core.Context) error {
	peer, uid, err := ctx.Peer().ResolveTargetUser()
	if err != nil {
		return ctx.EditOrReply("⚠️ " + err.Error())
	}

	if err := ctx.BlockUser(peer); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to block user: %v", err))
	}

	return ctx.EditOrReply(fmt.Sprintf("🚫 <b>User blocked:</b> <code>%d</code>", uid))
}

// handleUnblock unblocks a user.
func (p *Plugin) handleUnblock(ctx *core.Context) error {
	peer, uid, err := ctx.Peer().ResolveTargetUser()
	if err != nil {
		return ctx.EditOrReply("⚠️ " + err.Error())
	}

	if err := ctx.UnblockUser(peer); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to unblock user: %v", err))
	}

	return ctx.EditOrReply(fmt.Sprintf("✅ <b>User unblocked:</b> <code>%d</code>", uid))
}

// handleContacts lists saved contacts.
func (p *Plugin) handleContacts(ctx *core.Context) error {
	if ctx.Svc == nil {
		return ctx.EditOrReply("❌ Telegram service not available.")
	}

	contacts, err := ctx.Svc.GetContacts(ctx.Ctx)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to fetch contacts: %v", err))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📖 <b>Contacts List (%d total)</b>\n\n", len(contacts)))

	if len(contacts) == 0 {
		sb.WriteString("<i>No saved contacts found.</i>")
		return ctx.EditOrReply(sb.String())
	}

	displayLimit := 15
	if len(contacts) < displayLimit {
		displayLimit = len(contacts)
	}

	for i := 0; i < displayLimit; i++ {
		c := contacts[i]
		name := strings.TrimSpace(c.FirstName + " " + c.LastName)
		if name == "" {
			name = "Unknown"
		}
		uname := ""
		if c.Username != "" {
			uname = fmt.Sprintf(" (@%s)", core.EscapeHTML(c.Username))
		}
		sb.WriteString(fmt.Sprintf("%d. <b>%s</b>%s — <code>%d</code>\n", i+1, core.EscapeHTML(name), uname, c.ID))
	}

	if len(contacts) > displayLimit {
		sb.WriteString(fmt.Sprintf("\n<i>...and %d more contacts.</i>", len(contacts)-displayLimit))
	}

	return ctx.EditOrReply(sb.String())
}

// handleDialogs lists active conversations.
func (p *Plugin) handleDialogs(ctx *core.Context) error {
	limit := 10
	if len(ctx.Args) > 0 {
		if parsed, err := strconv.Atoi(ctx.Args[0]); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 30 {
		limit = 30
	}

	if ctx.Svc == nil {
		return ctx.EditOrReply("❌ Telegram service not available.")
	}

	dialogs, err := ctx.Svc.GetDialogs(ctx.Ctx, limit)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to fetch dialogs: %v", err))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("💬 <b>Recent Dialogs (%d fetched)</b>\n\n", len(dialogs)))

	if len(dialogs) == 0 {
		sb.WriteString("<i>No active dialogs found.</i>")
		return ctx.EditOrReply(sb.String())
	}

	for i, d := range dialogs {
		title := d.Title
		if title == "" {
			title = "Untitled Chat"
		}
		sb.WriteString(fmt.Sprintf("%d. <b>%s</b> [<code>%s</code>]\n   ID: <code>%d</code>\n",
			i+1, core.EscapeHTML(title), core.EscapeHTML(d.Type), d.ID))
	}

	return ctx.EditOrReply(sb.String())
}
