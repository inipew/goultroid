package profile

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/imageguard"
	"github.com/inipew/goultroid/internal/tasks"
)

var profileImagePolicy = imageguard.Policy{
	MaxInputBytes:   20 << 20,
	MaxWidth:        8192,
	MaxHeight:       8192,
	MaxPixels:       40_000_000,
	MaxDecodedBytes: 160 << 20,
}

// Plugin provides self-user profile and contact management commands.
type Plugin struct {
	files *filesystem.Scope
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
	if fs == nil {
		p.files = nil
		return
	}
	p.files = fs.ForOwner("profile")
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
			Description: "Update profile picture from a replied Telegram image",
			Usage:       ".setpic (reply to a photo or image document)",
			Category:    "Profile",
			Permission:  core.PermissionOwner,
			Resources: []tasks.ResourceRequirement{
				{Name: "download", Amount: 1},
				{Name: "media", Amount: 1},
			},
			Handler: p.handleSetPic,
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
		return ctx.Fail(err, ctx.T("profile.me.fetch_failed"))
	}

	var selfUser *tg.User
	for _, u := range fullUser.Users {
		if usr, ok := u.(*tg.User); ok {
			selfUser = usr
			break
		}
	}

	if selfUser == nil {
		return ctx.Error(ctx.T("profile.me.parse_failed"))
	}

	var sb strings.Builder
	sb.WriteString(ctx.T("profile.me.title"))
	sb.WriteString(ctx.T("profile.me.id", selfUser.ID))
	sb.WriteString(ctx.T("profile.me.first_name", core.EscapeHTML(selfUser.FirstName)))
	if selfUser.LastName != "" {
		sb.WriteString(ctx.T("profile.me.last_name", core.EscapeHTML(selfUser.LastName)))
	}
	if selfUser.Username != "" {
		sb.WriteString(ctx.T("profile.me.username", core.EscapeHTML(selfUser.Username)))
	}
	if selfUser.Phone != "" {
		sb.WriteString(ctx.T("profile.me.phone", core.EscapeHTML(selfUser.Phone)))
	}
	if selfUser.Premium {
		sb.WriteString(ctx.T("profile.me.premium"))
	}
	if fullUser.FullUser.About != "" {
		sb.WriteString(ctx.T("profile.me.bio", core.EscapeHTML(fullUser.FullUser.About)))
	}

	return ctx.Result(sb.String())
}

// handleSetBio updates the account's bio/about.
func (p *Plugin) handleSetBio(ctx *core.Context) error {
	bio := strings.TrimSpace(ctx.RawArgs)
	if bio == "" {
		return ctx.Status(ctx.T("profile.bio.usage"))
	}
	if len([]rune(bio)) > 70 {
		return ctx.Status(ctx.T("profile.bio.too_long", len([]rune(bio))))
	}

	if err := ctx.UpdateProfile(nil, nil, &bio); err != nil {
		return ctx.Fail(err, ctx.T("profile.bio.update_failed"))
	}

	return ctx.Success(ctx.T("profile.bio.updated", core.EscapeHTML(bio)))
}

// handleSetName updates the account's first name and optional last name.
func (p *Plugin) handleSetName(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		return ctx.Status(ctx.T("profile.name.usage"))
	}

	firstName := ctx.Args[0]
	lastName := ""
	if len(ctx.Args) > 1 {
		lastName = strings.Join(ctx.Args[1:], " ")
	}

	if err := ctx.UpdateProfile(&firstName, &lastName, nil); err != nil {
		return ctx.Fail(err, ctx.T("profile.name.update_failed"))
	}

	fullName := firstName
	if lastName != "" {
		fullName += " " + lastName
	}
	return ctx.Success(ctx.T("profile.name.updated", core.EscapeHTML(fullName)))
}

func isProfileImageMedia(media *core.MediaInfo) bool {
	if media == nil || media.Location == nil {
		return false
	}
	if media.Type == "photo" {
		return true
	}
	if media.Type == "document" || media.Type == "sticker" {
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(media.MimeType)), "image/")
	}
	return false
}

// handleSetPic sets a new profile photo from replied Telegram media.
// Host filesystem paths are deliberately unsupported: the profile plugin only
// owns its scoped temporary workspace and must not read arbitrary local files.
func (p *Plugin) handleSetPic(ctx *core.Context) error {
	reply, err := ctx.GetReply()
	if err != nil {
		return ctx.Fail(err, ctx.T("profile.photo.reply_failed"))
	}
	if reply == nil || !reply.HasMedia() {
		return ctx.Status(ctx.T("profile.photo.usage"))
	}
	if !isProfileImageMedia(reply.Media) {
		return ctx.Status(ctx.T("profile.photo.invalid_media"))
	}
	if err := imageguard.ValidateKnown(reply.Media.Size, reply.Media.Width, reply.Media.Height, profileImagePolicy); err != nil {
		return ctx.Fail(err, ctx.T("profile.photo.rejected"))
	}

	if p.files == nil {
		return ctx.Error(ctx.T("profile.photo.filesystem_unavailable"))
	}
	tempDir, err := p.files.CreateTempDir("goultroid-pfp-*")
	if err != nil {
		return ctx.Fail(err, ctx.T("profile.photo.temp_failed"))
	}
	defer func() { _ = p.files.RemoveTempDir(tempDir) }()

	filePath, err := ctx.DownloadMedia(tempDir)
	if err != nil {
		return ctx.Fail(err, ctx.T("profile.photo.download_failed"))
	}
	if _, err := imageguard.Inspect(filePath, profileImagePolicy); err != nil {
		return ctx.Fail(err, ctx.T("profile.photo.invalid_download"))
	}

	profileSvc := ctx.ProfileService()
	if profileSvc == nil {
		return ctx.Error(ctx.T("profile.telegram_unavailable"))
	}
	if err := profileSvc.UploadProfilePhoto(ctx.Ctx, filePath); err != nil {
		return ctx.Fail(err, ctx.T("profile.photo.set_failed"))
	}
	return ctx.Success(ctx.T("profile.photo.updated"))
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

	profileSvc := ctx.ProfileService()
	if profileSvc == nil {
		return ctx.Error(ctx.T("profile.telegram_unavailable"))
	}

	deleted, err := profileSvc.DeleteProfilePhotos(ctx.Ctx, limit)
	if err != nil {
		return ctx.Fail(err, ctx.T("profile.photo.delete_failed"))
	}

	if deleted == 0 {
		return ctx.Status(ctx.T("profile.photo.none"))
	}

	return ctx.Success(ctx.T("profile.photo.deleted", deleted))
}

// handleBlock blocks a user from PM/contacts.
func (p *Plugin) handleBlock(ctx *core.Context) error {
	peer, uid, err := ctx.Peer().ResolveTargetUser()
	if err != nil {
		return ctx.Fail(err, ctx.T("profile.user.resolve_failed"))
	}

	if err := ctx.BlockUser(peer); err != nil {
		return ctx.Fail(err, ctx.T("profile.user.block_failed"))
	}

	return ctx.Success(ctx.T("profile.user.blocked", ctx.DisplayUser(peer, uid)))
}

// handleUnblock unblocks a user.
func (p *Plugin) handleUnblock(ctx *core.Context) error {
	peer, uid, err := ctx.Peer().ResolveTargetUser()
	if err != nil {
		return ctx.Fail(err, ctx.T("profile.user.resolve_failed"))
	}

	if err := ctx.UnblockUser(peer); err != nil {
		return ctx.Fail(err, ctx.T("profile.user.unblock_failed"))
	}

	return ctx.Success(ctx.T("profile.user.unblocked", ctx.DisplayUser(peer, uid)))
}

// handleContacts lists saved contacts.
func (p *Plugin) handleContacts(ctx *core.Context) error {
	profileSvc := ctx.ProfileService()
	if profileSvc == nil {
		return ctx.Error(ctx.T("profile.telegram_unavailable"))
	}

	contacts, err := profileSvc.GetContacts(ctx.Ctx)
	if err != nil {
		return ctx.Fail(err, ctx.T("profile.contacts.fetch_failed"))
	}

	var sb strings.Builder
	sb.WriteString(ctx.T("profile.contacts.title", len(contacts)))

	if len(contacts) == 0 {
		sb.WriteString(ctx.T("profile.contacts.none"))
		return ctx.Result(sb.String())
	}

	displayLimit := 15
	if len(contacts) < displayLimit {
		displayLimit = len(contacts)
	}

	for i := 0; i < displayLimit; i++ {
		c := contacts[i]
		name := strings.TrimSpace(c.FirstName + " " + c.LastName)
		if name == "" {
			name = ctx.T("profile.common.unknown")
		}
		uname := ""
		if c.Username != "" {
			uname = fmt.Sprintf(" (@%s)", core.EscapeHTML(c.Username))
		}
		sb.WriteString(ctx.T("profile.contacts.item", i+1, core.EscapeHTML(name), uname, c.ID))
	}

	if len(contacts) > displayLimit {
		sb.WriteString(ctx.T("profile.contacts.more", len(contacts)-displayLimit))
	}

	return ctx.Result(sb.String())
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

	profileSvc := ctx.ProfileService()
	if profileSvc == nil {
		return ctx.Error(ctx.T("profile.telegram_unavailable"))
	}

	dialogs, err := profileSvc.GetDialogs(ctx.Ctx, limit)
	if err != nil {
		return ctx.Fail(err, ctx.T("profile.dialogs.fetch_failed"))
	}

	var sb strings.Builder
	sb.WriteString(ctx.T("profile.dialogs.title", len(dialogs)))

	if len(dialogs) == 0 {
		sb.WriteString(ctx.T("profile.dialogs.none"))
		return ctx.Result(sb.String())
	}

	for i, d := range dialogs {
		title := d.Title
		if title == "" {
			title = ctx.T("profile.dialogs.untitled")
		}
		sb.WriteString(ctx.T(
			"profile.dialogs.item",
			i+1,
			core.EscapeHTML(title),
			core.EscapeHTML(d.Type),
			d.ID,
		))
	}

	return ctx.Result(sb.String())
}
