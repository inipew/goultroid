package clone

import (
	"context"
	"os"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
)

type cloneTaskClient struct {
	tasks.Client
	execute bool
	specs   []tasks.WorkSpec
}

func (c *cloneTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.specs = append(c.specs, spec)
	if c.execute && spec.Handler != nil {
		return nil, spec.Handler(tasks.WithHeldResources(ctx, spec.Resources))
	}
	return nil, nil
}

func (c *cloneTaskClient) count() int {
	return len(c.specs)
}

func (c *cloneTaskClient) lastSpec(t *testing.T) tasks.WorkSpec {
	t.Helper()
	if len(c.specs) == 0 {
		t.Fatal("expected submitted task")
	}
	return c.specs[len(c.specs)-1]
}

type clonePlanningService struct {
	cloneTestService
	selfFull            *tg.UsersUserFull
	targetFull          *tg.UsersUserFull
	downloadPayload     []byte
	downloadCalls       int
	sawDownloadLease    bool
	sawUploadMediaLease bool
	sawDeleteMediaLease bool
}

func (s *clonePlanningService) GetFullUser(_ context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	switch user.(type) {
	case *tg.InputUserSelf:
		return s.selfFull, nil
	default:
		return s.targetFull, nil
	}
}

func (s *clonePlanningService) DownloadFile(ctx context.Context, _ tg.InputFileLocationClass, dstPath string) error {
	s.downloadCalls++
	s.sawDownloadLease = s.sawDownloadLease || tasks.HasHeldResource(ctx, "download")
	return os.WriteFile(dstPath, s.downloadPayload, 0o600)
}

func (s *clonePlanningService) UploadProfilePhoto(ctx context.Context, filePath string) error {
	s.sawUploadMediaLease = s.sawUploadMediaLease || tasks.HasHeldResource(ctx, "media")
	return s.cloneTestService.UploadProfilePhoto(ctx, filePath)
}

func (s *clonePlanningService) DeleteProfilePhotos(ctx context.Context, limit int) (int, error) {
	s.sawDeleteMediaLease = s.sawDeleteMediaLease || tasks.HasHeldResource(ctx, "media")
	return s.cloneTestService.DeleteProfilePhotos(ctx, limit)
}

func cloneFullUser(id int64, self bool, first, last, bio string, photoID int64) *tg.UsersUserFull {
	full := tg.UserFull{ID: id, About: bio}
	if photoID != 0 {
		full.ProfilePhoto = &tg.Photo{ID: photoID}
	}
	return &tg.UsersUserFull{
		FullUser: full,
		Users: []tg.UserClass{&tg.User{
			ID: id, Self: self, FirstName: first, LastName: last,
		}},
	}
}

func cloneCommandContext(svc core.TelegramServicer, targetID int64) *core.Context {
	return &core.Context{
		Ctx:      context.Background(),
		Args:     []string{"target"},
		Message:  &core.Message{ID: 10, IsOutgoing: true},
		Svc:      svc,
		PeerID:   &tg.InputPeerSelf{},
		Resolver: &core.MockPeerResolver{UserPeer: &tg.InputPeerUser{UserID: targetID, AccessHash: 9001}, UserID: targetID},
	}
}

func hasCloneResource(resources []tasks.ResourceRequirement, name string) bool {
	for _, resource := range resources {
		if resource.Name == name && resource.Amount > 0 {
			return true
		}
	}
	return false
}

func TestCloneCommandsHaveNoStaticHeavyResources(t *testing.T) {
	p := New(nil, 1001)
	for _, command := range p.Commands() {
		if (command.Name == "clone" || command.Name == "revert") && len(command.Resources) != 0 {
			t.Fatalf("%s declares static resources: %+v", command.Name, command.Resources)
		}
	}
}

func TestCloneWithoutTargetPhotoStaysLightweight(t *testing.T) {
	repo := &cloneTestRepo{}
	p, _ := newCloneTestPlugin(t, repo)
	client := &cloneTaskClient{}
	p.SetTaskClient(client)
	svc := &clonePlanningService{
		selfFull:   cloneFullUser(1001, true, "Owner", "Before", "owner bio", 55),
		targetFull: cloneFullUser(2002, false, "Target", "User", "target bio", 0),
	}
	ctx := cloneCommandContext(svc, 2002)

	if err := p.handleClone(ctx); err != nil {
		t.Fatal(err)
	}
	if client.count() != 0 {
		t.Fatalf("text-only clone submitted %d tasks", client.count())
	}
	if svc.downloadCalls != 0 || svc.uploadCalls != 0 {
		t.Fatalf("text-only clone touched photo I/O: downloads=%d uploads=%d", svc.downloadCalls, svc.uploadCalls)
	}
	if repo.state == nil || !repo.state.Active || repo.state.ClonedPhoto {
		t.Fatalf("unexpected lightweight clone state: %+v", repo.state)
	}
}

func TestCloneWithTargetPhotoPlansDownloadAndMedia(t *testing.T) {
	repo := &cloneTestRepo{}
	p, _ := newCloneTestPlugin(t, repo)
	client := &cloneTaskClient{}
	p.SetTaskClient(client)
	svc := &clonePlanningService{
		selfFull:   cloneFullUser(1001, true, "Owner", "Before", "owner bio", 55),
		targetFull: cloneFullUser(2002, false, "Target", "User", "target bio", 77),
	}
	ctx := cloneCommandContext(svc, 2002)

	if err := p.handleClone(ctx); err != nil {
		t.Fatal(err)
	}
	if client.count() != 1 {
		t.Fatalf("photo clone submitted %d tasks, want 1", client.count())
	}
	spec := client.lastSpec(t)
	if spec.Pool != tasks.PoolID("download") || !hasCloneResource(spec.Resources, "download") || !hasCloneResource(spec.Resources, "media") {
		t.Fatalf("photo clone plan=%+v, want download+media resources", spec)
	}
	if spec.OrderingKey != "profile:1001" || spec.ExecutionTimeout != cloneTaskTimeout {
		t.Fatalf("unexpected clone scheduling contract: %+v", spec)
	}
	if svc.downloadCalls != 0 || svc.uploadCalls != 0 {
		t.Fatalf("photo I/O ran before TaskEngine continuation: downloads=%d uploads=%d", svc.downloadCalls, svc.uploadCalls)
	}
}

func TestClonePhotoIOExecutesUnderPlannedLeases(t *testing.T) {
	repo := &cloneTestRepo{}
	p, _ := newCloneTestPlugin(t, repo)
	client := &cloneTaskClient{execute: true}
	p.SetTaskClient(client)
	svc := &clonePlanningService{
		selfFull:        cloneFullUser(1001, true, "Owner", "Before", "owner bio", 55),
		targetFull:      cloneFullUser(2002, false, "Target", "User", "target bio", 77),
		downloadPayload: []byte("profile-photo"),
	}
	ctx := cloneCommandContext(svc, 2002)

	if err := p.handleClone(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.downloadCalls != 2 {
		t.Fatalf("profile photo downloads=%d, want original+target", svc.downloadCalls)
	}
	if !svc.sawDownloadLease {
		t.Fatal("profile photo download ran without download resource")
	}
	if svc.uploadCalls != 1 || !svc.sawUploadMediaLease {
		t.Fatalf("profile photo upload contract: calls=%d mediaLease=%v", svc.uploadCalls, svc.sawUploadMediaLease)
	}
	if repo.state == nil || !repo.state.Active || !repo.state.ClonedPhoto || repo.state.OriginalPhoto == "" {
		t.Fatalf("unexpected committed photo clone state: %+v", repo.state)
	}
}

func TestRevertPlansMediaOnlyWhenPhotoStateMustChange(t *testing.T) {
	lightRepo := &cloneTestRepo{state: &CloneState{
		OwnerID: 1001, OriginalFirst: "Owner", Active: true,
	}}
	light, _ := newCloneTestPlugin(t, lightRepo)
	lightTasks := &cloneTaskClient{}
	light.SetTaskClient(lightTasks)
	lightSvc := &clonePlanningService{}
	lightCtx := &core.Context{Ctx: context.Background(), Svc: lightSvc, PeerID: &tg.InputPeerSelf{}, Message: &core.Message{ID: 1, IsOutgoing: true}}
	if err := light.handleRevert(lightCtx); err != nil {
		t.Fatal(err)
	}
	if lightTasks.count() != 0 {
		t.Fatalf("metadata-only revert submitted %d tasks", lightTasks.count())
	}
	if lightSvc.deleteCalls != 0 || lightSvc.uploadCalls != 0 {
		t.Fatalf("metadata-only revert touched photo state: deletes=%d uploads=%d", lightSvc.deleteCalls, lightSvc.uploadCalls)
	}

	photoRepo := &cloneTestRepo{state: &CloneState{
		OwnerID: 1001, OriginalFirst: "Owner", ClonedPhoto: true, Active: true,
	}}
	photo, _ := newCloneTestPlugin(t, photoRepo)
	photoTasks := &cloneTaskClient{}
	photo.SetTaskClient(photoTasks)
	photoSvc := &clonePlanningService{}
	photoCtx := &core.Context{Ctx: context.Background(), Svc: photoSvc, PeerID: &tg.InputPeerSelf{}, Message: &core.Message{ID: 2, IsOutgoing: true}}
	if err := photo.handleRevert(photoCtx); err != nil {
		t.Fatal(err)
	}
	if photoTasks.count() != 1 {
		t.Fatalf("photo revert submitted %d tasks, want 1", photoTasks.count())
	}
	spec := photoTasks.lastSpec(t)
	if len(spec.Resources) != 1 || !hasCloneResource(spec.Resources, "media") || hasCloneResource(spec.Resources, "download") {
		t.Fatalf("photo revert resources=%+v, want media only", spec.Resources)
	}
}

func TestRevertPhotoIOExecutesUnderMediaLease(t *testing.T) {
	repo := &cloneTestRepo{}
	p, _ := newCloneTestPlugin(t, repo)
	p.SetTaskClient(&cloneTaskClient{execute: true})
	ref := storeCloneSnapshotForTest(t, p, "original-photo")
	repo.state = &CloneState{
		OwnerID: 1001, OriginalFirst: "Owner", OriginalPhoto: ref, ClonedPhoto: true, Active: true,
	}
	svc := &clonePlanningService{}
	ctx := &core.Context{Ctx: context.Background(), Svc: svc, PeerID: &tg.InputPeerSelf{}, Message: &core.Message{ID: 3, IsOutgoing: true}}

	if err := p.handleRevert(ctx); err != nil {
		t.Fatal(err)
	}
	if !svc.sawDeleteMediaLease || !svc.sawUploadMediaLease {
		t.Fatalf("revert media lease missing: delete=%v upload=%v", svc.sawDeleteMediaLease, svc.sawUploadMediaLease)
	}
}
