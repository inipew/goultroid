package clone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/storage"
)

const cloneAssetRefPrefix = "asset:"

type Plugin struct {
	repo       Repository
	ownerID    int64
	assetStore storage.Storage
	files      *filesystem.Scope
}

func New(repo Repository, ownerID int64, stores ...storage.Storage) *Plugin {
	p := &Plugin{repo: repo, ownerID: ownerID}
	if len(stores) > 0 {
		p.assetStore = stores[0]
	}
	return p
}

func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	files, err := pctx.Files()
	if err != nil {
		return err
	}
	p.files = files
	return nil
}

func (p *Plugin) SetFiles(manager *filesystem.Manager) {
	if manager == nil {
		p.files = nil
		return
	}
	p.files = manager.ForOwner("clone")
}
func (p *Plugin) Name() string { return "clone" }
func (p *Plugin) Description() string {
	return "Clone another user's public profile identity and safely revert it"
}
func (p *Plugin) Init() error {
	if p.repo == nil {
		return errors.New("clone repository is not initialized")
	}
	if p.ownerID <= 0 {
		return errors.New("clone owner ID is not configured")
	}
	if p.assetStore == nil {
		return errors.New("clone asset storage is not configured")
	}
	return nil
}
func (p *Plugin) Shutdown() error { return nil }

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{Name: "clone", Description: "Clone a user's first name, last name, bio, and profile photo", Usage: ".clone [username|id] or reply to a user's message", Category: "Profile", Permission: core.PermissionOwner, Handler: p.handleClone},
		{Name: "revert", Description: "Restore the profile identity saved before the last clone", Usage: ".revert", Category: "Profile", Permission: core.PermissionOwner, Handler: p.handleRevert},
	}
}

func (p *Plugin) handleClone(ctx *core.Context) error {
	state, err := p.repo.GetCloneState(ctx.Ctx, p.ownerID)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to read clone state: %v", err))
	}
	if state != nil && state.Active {
		return ctx.EditOrReply("⚠️ A clone is already active. Run <code>.revert</code> before cloning another identity.")
	}

	workspace, err := p.createWorkspace()
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create clone workspace: %v", err))
	}
	defer func() { _ = p.files.RemoveTempDir(workspace) }()

	targetPeer, targetID, targetInput, targetUser, targetFull, err := p.resolveTarget(ctx)
	if err != nil {
		return ctx.EditOrReply("⚠️ " + err.Error())
	}
	if targetID == p.ownerID {
		return ctx.EditOrReply("⚠️ You are already the target identity.")
	}
	_ = targetInput

	selfFull, selfUser, err := p.getSelf(ctx)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to snapshot your current profile: %v", err))
	}

	targetPhoto, targetHasPhoto := targetFull.FullUser.ProfilePhoto.(*tg.Photo)
	targetHasPhoto = targetHasPhoto && targetPhoto != nil && targetPhoto.ID != 0

	originalPhotoRef := ""
	if targetHasPhoto {
		if photo, ok := selfFull.FullUser.ProfilePhoto.(*tg.Photo); ok && photo != nil && photo.ID != 0 {
			path, downloadErr := p.downloadProfilePhoto(ctx, &tg.InputPeerSelf{}, photo.ID, workspace, "original.jpg")
			if downloadErr != nil {
				return ctx.EditOrReply(fmt.Sprintf("❌ Could not snapshot your current profile photo: %v", downloadErr))
			}
			originalPhotoRef, err = p.storeOriginalPhotoSnapshot(ctx.Ctx, path)
			if err != nil {
				return ctx.EditOrReply(fmt.Sprintf("❌ Could not persist original profile photo: %v", err))
			}
		}
	}

	snapshot := CloneState{
		OwnerID:       p.ownerID,
		OriginalFirst: selfUser.FirstName,
		OriginalLast:  selfUser.LastName,
		OriginalBio:   selfFull.FullUser.About,
		OriginalPhoto: originalPhotoRef,
		ClonedPhoto:   false,
		Active:        true,
		UpdatedAt:     time.Now().UTC(),
	}
	if err := p.repo.SaveCloneState(ctx.Ctx, snapshot); err != nil {
		_ = p.cleanupSnapshot(context.Background(), snapshot.OriginalPhoto)
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to persist clone snapshot: %v", err))
	}

	firstName, lastName, bio := sanitizeName(targetUser.FirstName), sanitizeName(targetUser.LastName), targetFull.FullUser.About
	if firstName == "" {
		firstName = "User"
	}
	if err := ctx.UpdateProfile(&firstName, &lastName, &bio); err != nil {
		return p.cloneFailure(ctx, snapshot, false, fmt.Errorf("profile update failed: %w", err))
	}

	if targetHasPhoto {
		path, downloadErr := p.downloadProfilePhoto(ctx, targetPeer, targetPhoto.ID, workspace, "target.jpg")
		if downloadErr != nil {
			return p.cloneFailure(ctx, snapshot, false, fmt.Errorf("profile photo download failed: %w", downloadErr))
		}

		// Persist mutation intent before the non-idempotent upload. If the process
		// dies after Telegram accepts the photo but before the RPC returns, .revert
		// still knows that the latest profile photo may need to be removed.
		snapshot.ClonedPhoto = true
		snapshot.UpdatedAt = time.Now().UTC()
		if err := p.repo.SaveCloneState(ctx.Ctx, snapshot); err != nil {
			return p.cloneFailure(ctx, snapshot, false, fmt.Errorf("persist photo mutation intent: %w", err))
		}
		if err := ctx.Svc.UploadProfilePhoto(ctx.Ctx, path); err != nil {
			return p.cloneFailure(ctx, snapshot, true, fmt.Errorf("profile photo upload failed: %w", err))
		}
	}

	return ctx.EditOrReply(fmt.Sprintf("✅ <b>Profile cloned successfully.</b>\n\n<b>Name:</b> %s\n<b>Bio:</b> %s", core.EscapeHTML(strings.TrimSpace(firstName+" "+lastName)), core.EscapeHTML(bio)))
}

func (p *Plugin) handleRevert(ctx *core.Context) error {
	state, err := p.repo.GetCloneState(ctx.Ctx, p.ownerID)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to read clone state: %v", err))
	}
	if state == nil || !state.Active {
		return ctx.EditOrReply("ℹ️ No active clone state exists.")
	}
	if ctx.Svc == nil {
		return ctx.EditOrReply("❌ Telegram service is not available.")
	}

	workspace, err := p.createWorkspace()
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to create revert workspace: %v", err))
	}
	defer func() { _ = p.files.RemoveTempDir(workspace) }()

	if state.ClonedPhoto {
		if _, err := ctx.Svc.DeleteProfilePhotos(ctx.Ctx, 1); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to remove cloned profile photo: %v", err))
		}
	}
	if err := ctx.UpdateProfile(&state.OriginalFirst, &state.OriginalLast, &state.OriginalBio); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Profile photo state handled, but profile text restore failed: %v", err))
	}
	if state.OriginalPhoto != "" {
		path, err := p.materializeSnapshot(ctx.Ctx, workspace, state.OriginalPhoto)
		if err != nil {
			return ctx.EditOrReply(fmt.Sprintf("⚠️ Profile text restored, but original photo snapshot is unavailable: %v", err))
		}
		if err := ctx.Svc.UploadProfilePhoto(ctx.Ctx, path); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("⚠️ Profile text restored, but original photo restore failed: %v", err))
		}
	}
	if err := p.repo.ClearCloneState(ctx.Ctx, p.ownerID); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Profile restored, but clone state cleanup failed: %v", err))
	}
	_ = p.cleanupSnapshot(context.Background(), state.OriginalPhoto)
	return ctx.EditOrReply("✅ <b>Successfully reverted to your original profile.</b>")
}

func (p *Plugin) resolveTarget(ctx *core.Context) (tg.InputPeerClass, int64, tg.InputUserClass, *tg.User, *tg.UsersUserFull, error) {
	peer, id, err := ctx.Peer().ResolveTargetUser()
	if err != nil {
		return nil, 0, nil, nil, nil, err
	}
	inputPeer, ok := peer.(*tg.InputPeerUser)
	if !ok || inputPeer == nil || inputPeer.AccessHash == 0 {
		return nil, 0, nil, nil, nil, errors.New("resolved target does not contain a usable user access hash")
	}
	inputUser := &tg.InputUser{UserID: inputPeer.UserID, AccessHash: inputPeer.AccessHash}
	full, err := ctx.Peer().GetFullUser(inputUser)
	if err != nil {
		return nil, 0, nil, nil, nil, fmt.Errorf("failed to fetch target profile: %w", err)
	}
	var user *tg.User
	for _, item := range full.Users {
		if u, ok := item.(*tg.User); ok && u.ID == id {
			user = u
			break
		}
	}
	if user == nil {
		return nil, 0, nil, nil, nil, errors.New("target user was not returned by Telegram")
	}
	return peer, id, inputUser, user, full, nil
}

func (p *Plugin) getSelf(ctx *core.Context) (*tg.UsersUserFull, *tg.User, error) {
	full, err := ctx.Peer().GetFullUser(&tg.InputUserSelf{})
	if err != nil {
		return nil, nil, err
	}
	for _, item := range full.Users {
		if u, ok := item.(*tg.User); ok && u.Self {
			return full, u, nil
		}
	}
	for _, item := range full.Users {
		if u, ok := item.(*tg.User); ok {
			return full, u, nil
		}
	}
	return nil, nil, errors.New("telegram did not return the current user")
}

func (p *Plugin) createWorkspace() (string, error) {
	if p == nil || p.files == nil {
		return "", errors.New("clone filesystem scope is not initialized")
	}
	return p.files.CreateTempDir("goultroid-clone-*")
}

func (p *Plugin) workspacePath(workspace, name string) (string, error) {
	if p == nil || p.files == nil {
		return "", errors.New("clone filesystem scope is not initialized")
	}
	return p.files.SafePath(workspace, name)
}

func (p *Plugin) downloadProfilePhoto(ctx *core.Context, peer tg.InputPeerClass, photoID int64, workspace, name string) (string, error) {
	if ctx == nil || ctx.Svc == nil {
		return "", errors.New("telegram service is not initialized")
	}
	if photoID == 0 {
		return "", errors.New("profile photo has no ID")
	}
	path, err := p.workspacePath(workspace, name)
	if err != nil {
		return "", err
	}
	location := &tg.InputPeerPhotoFileLocation{Peer: peer, PhotoID: photoID, Big: true}
	if err := ctx.Svc.DownloadFile(ctx.Ctx, location, path); err != nil {
		return "", err
	}
	return path, nil
}

func (p *Plugin) storeOriginalPhotoSnapshot(ctx context.Context, path string) (string, error) {
	if p == nil || p.assetStore == nil {
		return "", errors.New("clone asset storage is not initialized")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	asset, err := p.assetStore.Put(ctx, f, storage.Metadata{
		Name: fmt.Sprintf("clone-original-%d.jpg", p.ownerID),
		MIME: "image/jpeg",
	})
	if err != nil {
		return "", err
	}
	return cloneAssetRefPrefix + asset.ID, nil
}

func (p *Plugin) materializeSnapshot(ctx context.Context, workspace, ref string) (string, error) {
	if strings.HasPrefix(ref, cloneAssetRefPrefix) {
		if p.assetStore == nil {
			return "", errors.New("clone asset storage is not initialized")
		}
		id := strings.TrimPrefix(ref, cloneAssetRefPrefix)
		if id == "" {
			return "", errors.New("clone photo snapshot reference is empty")
		}
		r, err := p.assetStore.Open(ctx, id)
		if err != nil {
			return "", err
		}
		defer r.Close()
		path, err := p.workspacePath(workspace, "original-restore.jpg")
		if err != nil {
			return "", err
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(out, r)
		closeErr := out.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		return path, nil
	}

	// Backward compatibility for active clone states created before managed
	// storage references were introduced.
	if _, err := os.Stat(ref); err != nil {
		return "", err
	}
	return ref, nil
}

func (p *Plugin) cleanupSnapshot(parent context.Context, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return nil
	}
	if strings.HasPrefix(ref, cloneAssetRefPrefix) {
		if p.assetStore == nil {
			return errors.New("clone asset storage is not initialized")
		}
		id := strings.TrimPrefix(ref, cloneAssetRefPrefix)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer cancel()
		err := p.assetStore.Delete(ctx, id)
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}
		return err
	}
	err := os.Remove(ref)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (p *Plugin) cloneFailure(ctx *core.Context, snapshot CloneState, photoMutationStarted bool, cause error) error {
	var rollbackErrs []error
	if ctx == nil || ctx.Svc == nil {
		rollbackErrs = append(rollbackErrs, errors.New("telegram service is unavailable for rollback"))
	} else {
		if photoMutationStarted {
			if _, err := ctx.Svc.DeleteProfilePhotos(ctx.Ctx, 1); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("remove cloned photo: %w", err))
			}
		}
		if err := ctx.UpdateProfile(&snapshot.OriginalFirst, &snapshot.OriginalLast, &snapshot.OriginalBio); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore profile text: %w", err))
		}
		if photoMutationStarted && snapshot.OriginalPhoto != "" {
			workspace, err := p.createWorkspace()
			if err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("create photo rollback workspace: %w", err))
			} else {
				path, materializeErr := p.materializeSnapshot(ctx.Ctx, workspace, snapshot.OriginalPhoto)
				if materializeErr != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("materialize original photo: %w", materializeErr))
				} else if uploadErr := ctx.Svc.UploadProfilePhoto(ctx.Ctx, path); uploadErr != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("restore original photo: %w", uploadErr))
				}
				_ = p.files.RemoveTempDir(workspace)
			}
		}
	}

	if len(rollbackErrs) == 0 {
		if err := p.repo.ClearCloneState(ctx.Ctx, p.ownerID); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("clear clone state: %w", err))
		} else {
			_ = p.cleanupSnapshot(context.Background(), snapshot.OriginalPhoto)
		}
	}

	if len(rollbackErrs) > 0 {
		return ctx.EditOrReply(fmt.Sprintf("❌ Clone aborted: %v. Rollback incomplete: %v", cause, errors.Join(rollbackErrs...)))
	}
	return ctx.EditOrReply(fmt.Sprintf("❌ Clone aborted and rolled back safely: %v", cause))
}

func sanitizeName(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), "\u2060", ""), "\u0000", "")
}
