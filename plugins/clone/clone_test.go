package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/services/storage"
)

type cloneTestRepo struct {
	state      *CloneState
	saveErr    error
	clearErr   error
	saveCalls  int
	clearCalls int
}

func (r *cloneTestRepo) GetCloneState(context.Context, int64) (*CloneState, error) {
	if r.state == nil {
		return nil, nil
	}
	cp := *r.state
	return &cp, nil
}

func (r *cloneTestRepo) SaveCloneState(_ context.Context, state CloneState) error {
	r.saveCalls++
	if r.saveErr != nil {
		return r.saveErr
	}
	cp := state
	r.state = &cp
	return nil
}

func (r *cloneTestRepo) ClearCloneState(context.Context, int64) error {
	r.clearCalls++
	if r.clearErr != nil {
		return r.clearErr
	}
	r.state = nil
	return nil
}

type cloneTestService struct {
	core.MockTelegramServicer
	deleteCalls int
	updateCalls int
	uploadCalls int
	uploaded    []byte
}

func (s *cloneTestService) DeleteProfilePhotos(context.Context, int) (int, error) {
	s.deleteCalls++
	return 1, nil
}

func (s *cloneTestService) UpdateProfile(context.Context, *string, *string, *string) error {
	s.updateCalls++
	return nil
}

func (s *cloneTestService) UploadProfilePhoto(_ context.Context, filePath string) error {
	s.uploadCalls++
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	s.uploaded = append([]byte(nil), data...)
	return nil
}

func newCloneTestPlugin(t *testing.T, repo Repository) (*Plugin, storage.Storage) {
	t.Helper()
	store, err := storage.NewFileStorage(filepath.Join(t.TempDir(), "assets"), 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filesystem.NewManager(filepath.Join(t.TempDir(), "data"), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := New(repo, 1001, store)
	p.SetFiles(files)
	return p, store
}

func storeCloneSnapshotForTest(t *testing.T, p *Plugin, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "original.jpg")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := p.storeOriginalPhotoSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestCloneFailureBeforePhotoMutationDoesNotDeletePhoto(t *testing.T) {
	repo := &cloneTestRepo{}
	p, _ := newCloneTestPlugin(t, repo)
	snapshot := CloneState{
		OwnerID:       1001,
		OriginalFirst: "Before",
		OriginalLast:  "User",
		OriginalBio:   "bio",
		Active:        true,
		UpdatedAt:     time.Now(),
	}
	repo.state = &snapshot
	svc := &cloneTestService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}

	if err := p.cloneFailure(ctx, snapshot, false, errors.New("target download failed")); err != nil {
		t.Fatalf("cloneFailure returned error: %v", err)
	}
	if svc.deleteCalls != 0 {
		t.Fatalf("rollback deleted %d profile photo(s) before mutation started", svc.deleteCalls)
	}
	if svc.updateCalls != 1 {
		t.Fatalf("profile text restore calls=%d, want 1", svc.updateCalls)
	}
	if repo.clearCalls != 1 || repo.state != nil {
		t.Fatalf("rollback did not clear clone state: clears=%d state=%+v", repo.clearCalls, repo.state)
	}
}

func TestCloneFailureAfterPhotoMutationRestoresManagedSnapshot(t *testing.T) {
	repo := &cloneTestRepo{}
	p, store := newCloneTestPlugin(t, repo)
	ref := storeCloneSnapshotForTest(t, p, "original-photo")
	snapshot := CloneState{
		OwnerID:       1001,
		OriginalFirst: "Before",
		OriginalLast:  "User",
		OriginalBio:   "bio",
		OriginalPhoto: ref,
		ClonedPhoto:   true,
		Active:        true,
		UpdatedAt:     time.Now(),
	}
	repo.state = &snapshot
	svc := &cloneTestService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}

	if err := p.cloneFailure(ctx, snapshot, true, errors.New("upload result uncertain")); err != nil {
		t.Fatalf("cloneFailure returned error: %v", err)
	}
	if svc.deleteCalls != 1 {
		t.Fatalf("photo rollback delete calls=%d, want 1", svc.deleteCalls)
	}
	if svc.uploadCalls != 1 || string(svc.uploaded) != "original-photo" {
		t.Fatalf("original snapshot was not restored: calls=%d data=%q", svc.uploadCalls, svc.uploaded)
	}
	if repo.clearCalls != 1 || repo.state != nil {
		t.Fatalf("rollback did not clear state: clears=%d state=%+v", repo.clearCalls, repo.state)
	}
	id := ref[len(cloneAssetRefPrefix):]
	if _, err := store.Stat(context.Background(), id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("managed snapshot still exists after successful rollback: %v", err)
	}
}

func TestHandleRevertRestoresManagedSnapshotAndCleansState(t *testing.T) {
	repo := &cloneTestRepo{}
	p, store := newCloneTestPlugin(t, repo)
	p.SetTaskClient(&cloneTaskClient{execute: true})
	ref := storeCloneSnapshotForTest(t, p, "saved-original")
	repo.state = &CloneState{
		OwnerID:       1001,
		OriginalFirst: "Original",
		OriginalLast:  "Name",
		OriginalBio:   "original bio",
		OriginalPhoto: ref,
		ClonedPhoto:   true,
		Active:        true,
		UpdatedAt:     time.Now(),
	}
	svc := &cloneTestService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}

	if err := p.handleRevert(ctx); err != nil {
		t.Fatalf("handleRevert returned error: %v", err)
	}
	if svc.deleteCalls != 1 {
		t.Fatalf("delete calls=%d, want 1", svc.deleteCalls)
	}
	if svc.updateCalls != 1 {
		t.Fatalf("profile text restore calls=%d, want 1", svc.updateCalls)
	}
	if svc.uploadCalls != 1 || string(svc.uploaded) != "saved-original" {
		t.Fatalf("managed snapshot restore calls=%d data=%q", svc.uploadCalls, svc.uploaded)
	}
	if repo.state != nil || repo.clearCalls != 1 {
		t.Fatalf("clone state was not cleared: state=%+v clears=%d", repo.state, repo.clearCalls)
	}
	id := ref[len(cloneAssetRefPrefix):]
	if _, err := store.Stat(context.Background(), id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("managed snapshot still exists after revert: %v", err)
	}
}

func TestMaterializeSnapshotSupportsLegacyPath(t *testing.T) {
	repo := &cloneTestRepo{}
	p, _ := newCloneTestPlugin(t, repo)
	legacy := filepath.Join(t.TempDir(), "legacy.jpg")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := p.createWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.files.RemoveTempDir(workspace) }()

	path, err := p.materializeSnapshot(context.Background(), workspace, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if path != legacy {
		t.Fatalf("legacy path=%q, want %q", path, legacy)
	}
}
