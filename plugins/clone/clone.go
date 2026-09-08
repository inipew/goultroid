package clone

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

const cloneDataDir = "data/clone"

type Plugin struct { repo database.CloneRepository; ownerID int64 }
func New(repo database.CloneRepository, ownerID int64) *Plugin { return &Plugin{repo: repo, ownerID: ownerID} }
func (p *Plugin) Name() string { return "clone" }
func (p *Plugin) Description() string { return "Clone another user's public profile identity and safely revert it" }
func (p *Plugin) Init() error { if p.repo == nil { return errors.New("clone repository is not initialized") }; if p.ownerID <= 0 { return errors.New("clone owner ID is not configured") }; return os.MkdirAll(cloneDataDir, 0700) }
func (p *Plugin) Shutdown() error { return nil }

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{Name: "clone", Description: "Clone a user's first name, last name, bio, and profile photo", Usage: ".clone [username|id] or reply to a user's message", Category: "Profile", Permission: core.PermissionOwner, Handler: p.handleClone},
		{Name: "revert", Description: "Restore the profile identity saved before the last clone", Usage: ".revert", Category: "Profile", Permission: core.PermissionOwner, Handler: p.handleRevert},
	}
}

func (p *Plugin) handleClone(ctx *core.Context) error {
	state, err := p.repo.GetCloneState(ctx.Ctx, p.ownerID)
	if err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ Failed to read clone state: %v", err)) }
	if state != nil && state.Active { return ctx.EditOrReply("⚠️ A clone is already active. Run <code>.revert</code> before cloning another identity.") }

	targetPeer, targetID, targetInput, targetUser, targetFull, err := p.resolveTarget(ctx)
	if err != nil { return ctx.EditOrReply("⚠️ " + err.Error()) }
	if targetID == p.ownerID { return ctx.EditOrReply("⚠️ You are already the target identity.") }
	_ = targetInput

	selfFull, selfUser, err := p.getSelf(ctx)
	if err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ Failed to snapshot your current profile: %v", err)) }
	snapshotPath := ""
	if photo, ok := selfFull.FullUser.ProfilePhoto.(*tg.Photo); ok && photo.ID != 0 {
		snapshotPath, err = p.downloadProfilePhoto(ctx, &tg.InputPeerSelf{}, photo.ID, "original")
		if err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ Could not snapshot your current profile photo: %v", err)) }
	}
	clonedPhoto := false
	if photo, ok := targetFull.FullUser.ProfilePhoto.(*tg.Photo); ok && photo.ID != 0 { clonedPhoto = true }

	snapshot := database.CloneState{OwnerID: p.ownerID, OriginalFirst: selfUser.FirstName, OriginalLast: selfUser.LastName, OriginalBio: selfFull.FullUser.About, OriginalPhoto: snapshotPath, ClonedPhoto: clonedPhoto, Active: true, UpdatedAt: time.Now().UTC()}
	if err := p.repo.SaveCloneState(ctx.Ctx, snapshot); err != nil { if snapshotPath != "" { _ = os.Remove(snapshotPath) }; return ctx.EditOrReply(fmt.Sprintf("❌ Failed to persist clone snapshot: %v", err)) }

	firstName, lastName, bio := sanitizeName(targetUser.FirstName), sanitizeName(targetUser.LastName), targetFull.FullUser.About
	if firstName == "" { firstName = "User" }
	if err := ctx.UpdateProfile(&firstName, &lastName, &bio); err != nil { return p.cloneFailure(ctx, snapshot, fmt.Errorf("profile update failed: %w", err)) }

	if clonedPhoto {
		photo, _ := targetFull.FullUser.ProfilePhoto.(*tg.Photo)
		path, downloadErr := p.downloadProfilePhoto(ctx, targetPeer, photo.ID, "target")
		if downloadErr != nil { return p.cloneFailure(ctx, snapshot, fmt.Errorf("profile photo download failed: %w", downloadErr)) }
		uploadErr := ctx.Svc.UploadProfilePhoto(ctx.Ctx, path); _ = os.Remove(path)
		if uploadErr != nil { return p.cloneFailure(ctx, snapshot, fmt.Errorf("profile photo upload failed: %w", uploadErr)) }
	}
	return ctx.EditOrReply(fmt.Sprintf("✅ <b>Profile cloned successfully.</b>\n\n<b>Name:</b> %s\n<b>Bio:</b> %s", core.EscapeHTML(strings.TrimSpace(firstName+" "+lastName)), core.EscapeHTML(bio)))
}

func (p *Plugin) handleRevert(ctx *core.Context) error {
	state, err := p.repo.GetCloneState(ctx.Ctx, p.ownerID)
	if err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ Failed to read clone state: %v", err)) }
	if state == nil || !state.Active { return ctx.EditOrReply("ℹ️ No active clone state exists.") }
	if ctx.Svc == nil { return ctx.EditOrReply("❌ Telegram service is not available.") }
	if state.ClonedPhoto { if _, err := ctx.Svc.DeleteProfilePhotos(ctx.Ctx, 1); err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ Failed to remove cloned profile photo: %v", err)) } }
	if err := ctx.UpdateProfile(&state.OriginalFirst, &state.OriginalLast, &state.OriginalBio); err != nil { return ctx.EditOrReply(fmt.Sprintf("⚠️ Profile photo state handled, but profile text restore failed: %v", err)) }
	if state.OriginalPhoto != "" {
		if _, err := os.Stat(state.OriginalPhoto); err != nil { return ctx.EditOrReply(fmt.Sprintf("⚠️ Profile text restored, but original photo snapshot is missing: %v", err)) }
		if err := ctx.Svc.UploadProfilePhoto(ctx.Ctx, state.OriginalPhoto); err != nil { return ctx.EditOrReply(fmt.Sprintf("⚠️ Profile text restored, but original photo restore failed: %v", err)) }
		_ = os.Remove(state.OriginalPhoto)
	}
	if err := p.repo.ClearCloneState(ctx.Ctx, p.ownerID); err != nil { return ctx.EditOrReply(fmt.Sprintf("⚠️ Profile restored, but clone state cleanup failed: %v", err)) }
	return ctx.EditOrReply("✅ <b>Successfully reverted to your original profile.</b>")
}

func (p *Plugin) resolveTarget(ctx *core.Context) (tg.InputPeerClass, int64, tg.InputUserClass, *tg.User, *tg.UsersUserFull, error) {
	peer, id, err := ctx.Peer().ResolveTargetUser(); if err != nil { return nil, 0, nil, nil, nil, err }
	inputPeer, ok := peer.(*tg.InputPeerUser); if !ok || inputPeer == nil || inputPeer.AccessHash == 0 { return nil, 0, nil, nil, nil, errors.New("resolved target does not contain a usable user access hash") }
	inputUser := &tg.InputUser{UserID: inputPeer.UserID, AccessHash: inputPeer.AccessHash}
	full, err := ctx.Peer().GetFullUser(inputUser); if err != nil { return nil, 0, nil, nil, nil, fmt.Errorf("failed to fetch target profile: %w", err) }
	var user *tg.User; for _, item := range full.Users { if u, ok := item.(*tg.User); ok && u.ID == id { user = u; break } }
	if user == nil { return nil, 0, nil, nil, nil, errors.New("target user was not returned by Telegram") }
	return peer, id, inputUser, user, full, nil
}

func (p *Plugin) getSelf(ctx *core.Context) (*tg.UsersUserFull, *tg.User, error) {
	full, err := ctx.Peer().GetFullUser(&tg.InputUserSelf{}); if err != nil { return nil, nil, err }
	for _, item := range full.Users { if u, ok := item.(*tg.User); ok && u.Self { return full, u, nil } }
	for _, item := range full.Users { if u, ok := item.(*tg.User); ok { return full, u, nil } }
	return nil, nil, errors.New("Telegram did not return the current user")
}

func (p *Plugin) downloadProfilePhoto(ctx *core.Context, peer tg.InputPeerClass, photoID int64, prefix string) (string, error) {
	if ctx.Svc == nil { return "", errors.New("telegram service is not initialized") }; if photoID == 0 { return "", errors.New("profile photo has no ID") }
	if err := os.MkdirAll(cloneDataDir, 0700); err != nil { return "", err }
	path := filepath.Join(cloneDataDir, fmt.Sprintf("%s-%d.jpg", prefix, time.Now().UnixNano()))
	location := &tg.InputPeerPhotoFileLocation{Peer: peer, PhotoID: photoID, Big: true}; if err := ctx.Svc.DownloadFile(ctx.Ctx, location, path); err != nil { _ = os.Remove(path); return "", err }
	return path, nil
}

func (p *Plugin) cloneFailure(ctx *core.Context, snapshot database.CloneState, cause error) error {
	if ctx.Svc != nil { if snapshot.ClonedPhoto { _, _ = ctx.Svc.DeleteProfilePhotos(ctx.Ctx, 1) }; _ = ctx.UpdateProfile(&snapshot.OriginalFirst, &snapshot.OriginalLast, &snapshot.OriginalBio); if snapshot.OriginalPhoto != "" { _ = ctx.Svc.UploadProfilePhoto(ctx.Ctx, snapshot.OriginalPhoto) } }
	return ctx.EditOrReply(fmt.Sprintf("❌ Clone aborted and rollback attempted: %v", cause))
}

func sanitizeName(s string) string { return strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), "\u2060", ""), "\u0000", "") }
