package clone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	cloneAssetRefPrefix = "asset:"
	cloneTaskTimeout    = 2 * time.Minute
)

var cloneTaskSequence atomic.Uint64

type clonePlan struct {
	targetPeer      tg.InputPeerClass
	targetFirst     string
	targetLast      string
	targetBio       string
	targetPhotoID   int64
	originalFirst   string
	originalLast    string
	originalBio     string
	originalPhotoID int64
}

type Plugin struct {
	repo       Repository
	ownerID    int64
	assetStore storage.Storage
	files      *filesystem.Scope
	tasks      tasks.Client
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
	client, err := pctx.TaskClient()
	if err != nil {
		return fmt.Errorf("clone: initialize task client: %w", err)
	}
	p.tasks = client
	return nil
}

func (p *Plugin) SetFiles(manager *filesystem.Manager) {
	if manager == nil {
		p.files = nil
		return
	}
	p.files = manager.ForOwner("clone")
}

// SetTaskClient sets the scoped TaskEngine client used for profile continuations.
func (p *Plugin) SetTaskClient(client tasks.Client) {
	p.tasks = client
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

func (p *Plugin) nextTaskID(kind string) tasks.TaskID {
	return tasks.TaskID(fmt.Sprintf(
		"clone:%s:%d:%d:%d",
		kind,
		p.ownerID,
		time.Now().UnixNano(),
		cloneTaskSequence.Add(1),
	))
}

func detachCloneContext(ctx *core.Context) *core.Context {
	if ctx == nil {
		return nil
	}
	cp := *ctx
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
	resources []tasks.ResourceRequirement,
	handler func(context.Context) error,
) error {
	if p.tasks == nil {
		return fmt.Errorf("%w: clone TaskEngine client is not configured", core.ErrUnavailable)
	}
	if admissionCtx == nil {
		admissionCtx = context.Background()
	}
	resources = append([]tasks.ResourceRequirement(nil), resources...)
	_, err := p.tasks.Submit(admissionCtx, tasks.WorkSpec{
		ID:               p.nextTaskID(kind),
		Pool:             pool,
		Class:            tasks.PriorityNormal,
		OrderingKey:      fmt.Sprintf("profile:%d", p.ownerID),
		ExecutionTimeout: cloneTaskTimeout,
		Resources:        resources,
		Handler: func(taskCtx context.Context) error {
			return handler(taskCtx)
		},
	})
	if err != nil {
		return fmt.Errorf("clone: submit %s continuation: %w", kind, err)
	}
	return nil
}

func profilePhotoID(full *tg.UsersUserFull) int64 {
	if full == nil {
		return 0
	}
	photo, ok := full.FullUser.ProfilePhoto.(*tg.Photo)
	if !ok || photo == nil {
		return 0
	}
	return photo.ID
}

func (p *Plugin) clonePlan(ctx *core.Context) (clonePlan, error) {
	targetPeer, targetID, _, targetUser, targetFull, err := p.resolveTarget(ctx)
	if err != nil {
		return clonePlan{}, err
	}
	if targetID == p.ownerID {
		return clonePlan{}, errors.New("you are already the target identity")
	}
	selfFull, selfUser, err := p.getSelf(ctx)
	if err != nil {
		return clonePlan{}, fmt.Errorf("failed to snapshot your current profile: %w", err)
	}
	return clonePlan{
		targetPeer:      targetPeer,
		targetFirst:     targetUser.FirstName,
		targetLast:      targetUser.LastName,
		targetBio:       targetFull.FullUser.About,
		targetPhotoID:   profilePhotoID(targetFull),
		originalFirst:   selfUser.FirstName,
		originalLast:    selfUser.LastName,
		originalBio:     selfFull.FullUser.About,
		originalPhotoID: profilePhotoID(selfFull),
	}, nil
}

func (p *Plugin) handleClone(ctx *core.Context) error {
	state, err := p.repo.GetCloneState(ctx.Ctx, p.ownerID)
	if err != nil {
		return ctx.Fail(err, "Failed to read clone state.")
	}
	if state != nil && state.Active {
		return ctx.Status("A clone is already active. Run <code>.revert</code> before cloning another identity.")
	}

	plan, err := p.clonePlan(ctx)
	if err != nil {
		return ctx.Fail(err, "Could not resolve the clone target. Reply to a valid user or provide a valid target.")
	}
	if plan.targetPhotoID == 0 {
		return p.executeClone(ctx, plan)
	}

	uiCtx := detachCloneContext(ctx)
	resources := []tasks.ResourceRequirement{
		{Name: "download", Amount: 1},
		{Name: "media", Amount: 1},
	}
	if err := p.submitContinuation(ctx.Ctx, "clone-photo", tasks.PoolID("download"), resources, func(taskCtx context.Context) error {
		taskCore := uiCtx.WithContext(taskCtx)
		current, stateErr := p.repo.GetCloneState(taskCtx, p.ownerID)
		if stateErr != nil {
			return taskCore.EditOrReply(fmt.Sprintf("❌ Failed to re-check clone state: %v", stateErr))
		}
		if current != nil && current.Active {
			return taskCore.EditOrReply("⚠️ A clone became active before this operation started. Revert it before cloning another identity.")
		}
		return p.executeClone(taskCore, plan)
	}); err != nil {
		return ctx.Fail(err, "Failed to queue profile-photo clone.")
	}
	return nil
}

func (p *Plugin) executeClone(ctx *core.Context, plan clonePlan) error {
	workspace := ""
	if plan.targetPhotoID != 0 {
		var err error
		workspace, err = p.createWorkspace()
		if err != nil {
			return ctx.Fail(err, "Failed to create clone workspace.")
		}
		defer func() { _ = p.files.RemoveTempDir(workspace) }()
	}

	originalPhotoRef := ""
	if plan.targetPhotoID != 0 && plan.originalPhotoID != 0 {
		path, err := p.downloadProfilePhoto(ctx, &tg.InputPeerSelf{}, plan.originalPhotoID, workspace, "original.jpg")
		if err != nil {
			return ctx.Fail(err, "Could not snapshot your current profile photo.")
		}
		originalPhotoRef, err = p.storeOriginalPhotoSnapshot(ctx.Ctx, path)
		if err != nil {
			return ctx.Fail(err, "Could not persist original profile photo.")
		}
	}

	snapshot := CloneState{
		OwnerID:       p.ownerID,
		OriginalFirst: plan.originalFirst,
		OriginalLast:  plan.originalLast,
		OriginalBio:   plan.originalBio,
		OriginalPhoto: originalPhotoRef,
		ClonedPhoto:   false,
		Active:        true,
		UpdatedAt:     time.Now().UTC(),
	}
	if err := p.repo.SaveCloneState(ctx.Ctx, snapshot); err != nil {
		_ = p.cleanupSnapshot(context.Background(), snapshot.OriginalPhoto)
		return ctx.Fail(err, "Failed to persist clone snapshot.")
	}

	firstName := sanitizeName(plan.targetFirst)
	lastName := sanitizeName(plan.targetLast)
	bio := plan.targetBio
	if firstName == "" {
		firstName = "User"
	}
	if err := ctx.UpdateProfile(&firstName, &lastName, &bio); err != nil {
		return p.cloneFailure(ctx, snapshot, false, fmt.Errorf("profile update failed: %w", err))
	}

	if plan.targetPhotoID != 0 {
		path, err := p.downloadProfilePhoto(ctx, plan.targetPeer, plan.targetPhotoID, workspace, "target.jpg")
		if err != nil {
			return p.cloneFailure(ctx, snapshot, false, fmt.Errorf("profile photo download failed: %w", err))
		}

		snapshot.ClonedPhoto = true
		snapshot.UpdatedAt = time.Now().UTC()
		if err := p.repo.SaveCloneState(ctx.Ctx, snapshot); err != nil {
			return p.cloneFailure(ctx, snapshot, false, fmt.Errorf("persist photo mutation intent: %w", err))
		}
		if err := ctx.Svc.UploadProfilePhoto(ctx.Ctx, path); err != nil {
			return p.cloneFailure(ctx, snapshot, true, fmt.Errorf("profile photo upload failed: %w", err))
		}
	}

	return ctx.Success(fmt.Sprintf("<b>Profile cloned successfully.</b>\n\n<b>Name:</b> %s\n<b>Bio:</b> %s", core.EscapeHTML(strings.TrimSpace(firstName+" "+lastName)), core.EscapeHTML(bio)))
}

func (p *Plugin) handleRevert(ctx *core.Context) error {
	state, err := p.repo.GetCloneState(ctx.Ctx, p.ownerID)
	if err != nil {
		return ctx.Fail(err, "Failed to read clone state.")
	}
	if state == nil || !state.Active {
		return ctx.Status("No active clone state exists.")
	}
	if ctx.Svc == nil {
		return ctx.Error("Telegram service is not available.")
	}

	needsPhotoRestore := state.ClonedPhoto || strings.TrimSpace(state.OriginalPhoto) != ""
	if !needsPhotoRestore {
		return p.executeRevert(ctx, *state)
	}

	uiCtx := detachCloneContext(ctx)
	resources := []tasks.ResourceRequirement{{Name: "media", Amount: 1}}
	if err := p.submitContinuation(ctx.Ctx, "revert-photo", tasks.PoolID("general"), resources, func(taskCtx context.Context) error {
		taskCore := uiCtx.WithContext(taskCtx)
		current, stateErr := p.repo.GetCloneState(taskCtx, p.ownerID)
		if stateErr != nil {
			return taskCore.EditOrReply(fmt.Sprintf("❌ Failed to re-check clone state: %v", stateErr))
		}
		if current == nil || !current.Active {
			return taskCore.EditOrReply("ℹ️ No active clone state exists.")
		}
		return p.executeRevert(taskCore, *current)
	}); err != nil {
		return ctx.Fail(err, "Failed to queue profile-photo revert.")
	}
	return nil
}

func (p *Plugin) executeRevert(ctx *core.Context, state CloneState) error {
	if state.ClonedPhoto {
		if _, err := ctx.Svc.DeleteProfilePhotos(ctx.Ctx, 1); err != nil {
			return ctx.Fail(err, "Failed to remove cloned profile photo.")
		}
	}
	if err := ctx.UpdateProfile(&state.OriginalFirst, &state.OriginalLast, &state.OriginalBio); err != nil {
		return ctx.Fail(err, "Profile photo state was handled, but profile text could not be restored.")
	}
	if strings.TrimSpace(state.OriginalPhoto) != "" {
		workspace, err := p.createWorkspace()
		if err != nil {
			return ctx.Fail(err, "Profile text was restored, but the revert workspace is unavailable.")
		}
		defer func() { _ = p.files.RemoveTempDir(workspace) }()

		path, err := p.materializeSnapshot(ctx.Ctx, workspace, state.OriginalPhoto)
		if err != nil {
			return ctx.Fail(err, "Profile text was restored, but the original photo snapshot is unavailable.")
		}
		if err := ctx.Svc.UploadProfilePhoto(ctx.Ctx, path); err != nil {
			return ctx.Fail(err, "Profile text was restored, but the original profile photo could not be restored.")
		}
	}
	if err := p.repo.ClearCloneState(ctx.Ctx, p.ownerID); err != nil {
		return ctx.Fail(err, "Profile was restored, but clone state cleanup failed. Check the logs before retrying.")
	}
	_ = p.cleanupSnapshot(context.Background(), state.OriginalPhoto)
	return ctx.Success("<b>Successfully reverted to your original profile.</b>")
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
	if registryRepo, ok := p.repo.(mediaRegistryRepository); ok {
		if err := registryRepo.RegisterCloneMediaAsset(ctx, asset.ID); err != nil {
			cleanupParent := ctx
			if cleanupParent == nil {
				cleanupParent = context.Background()
			}
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(cleanupParent), 5*time.Second)
			deleteErr := p.assetStore.Delete(cleanupCtx, asset.ID)
			cancel()
			if deleteErr != nil && !errors.Is(deleteErr, storage.ErrNotFound) {
				return "", errors.Join(err, fmt.Errorf("clone: rollback unregistered snapshot %q: %w", asset.ID, deleteErr))
			}
			return "", err
		}
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
		if id == "" {
			return nil
		}
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer cancel()
		if err := p.assetStore.Delete(ctx, id); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		if registryRepo, ok := p.repo.(mediaRegistryRepository); ok {
			if err := registryRepo.RemoveCloneMediaAsset(ctx, id); err != nil {
				return err
			}
		}
		return nil
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
		return ctx.Fail(errors.Join(cause, errors.Join(rollbackErrs...)), "Clone aborted and rollback could not be completed safely.")
	}
	return ctx.Fail(cause, "Clone aborted and rolled back safely.")
}

func sanitizeName(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), "\u2060", ""), "\u0000", "")
}
