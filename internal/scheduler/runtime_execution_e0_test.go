package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

type executionLifecycleDefinitionStore struct {
	mu      sync.Mutex
	defs    map[string]jobs.JobDefinition
	saveErr error
}

func newExecutionLifecycleDefinitionStore() *executionLifecycleDefinitionStore {
	return &executionLifecycleDefinitionStore{defs: make(map[string]jobs.JobDefinition)}
}

func (s *executionLifecycleDefinitionStore) SaveDefinition(_ context.Context, def *jobs.JobDefinition) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	if def == nil {
		return errors.New("nil definition")
	}
	s.mu.Lock()
	s.defs[def.ID] = *def
	s.mu.Unlock()
	return nil
}

func (s *executionLifecycleDefinitionStore) UpdateDefinitionCAS(_ context.Context, def *jobs.JobDefinition, _ uint64) error {
	if def == nil {
		return errors.New("nil definition")
	}
	s.mu.Lock()
	s.defs[def.ID] = *def
	s.mu.Unlock()
	return nil
}

func (s *executionLifecycleDefinitionStore) DeleteDefinition(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.defs, id)
	s.mu.Unlock()
	return nil
}

type executionLifecycleScheduleStore struct {
	mu                   sync.Mutex
	schedules            map[string]jobs.JobSchedule
	saveErr              error
	saveErrOnCall        int
	persistBeforeSaveErr bool
	saveCalls            int
	disableErr           error
	cutover              bool
}

func newExecutionLifecycleScheduleStore() *executionLifecycleScheduleStore {
	return &executionLifecycleScheduleStore{schedules: make(map[string]jobs.JobSchedule)}
}

func (s *executionLifecycleScheduleStore) SaveSchedule(_ context.Context, schedule *jobs.JobSchedule) error {
	if schedule == nil {
		return errors.New("nil schedule")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCalls++
	shouldFail := s.saveErr != nil && (s.saveErrOnCall == 0 || s.saveErrOnCall == s.saveCalls)
	if shouldFail && !s.persistBeforeSaveErr {
		return s.saveErr
	}
	s.schedules[schedule.ID] = *schedule
	if shouldFail {
		return s.saveErr
	}
	return nil
}

func (s *executionLifecycleScheduleStore) DisableSchedule(_ context.Context, id string) error {
	if s.disableErr != nil {
		return s.disableErr
	}
	s.mu.Lock()
	if schedule, ok := s.schedules[id]; ok {
		schedule.Enabled = false
		s.schedules[id] = schedule
	}
	s.mu.Unlock()
	return nil
}

func (s *executionLifecycleScheduleStore) ListDueSchedules(_ context.Context, _ time.Time, _ int) ([]jobs.JobSchedule, error) {
	return nil, nil
}

func (s *executionLifecycleScheduleStore) EarliestScheduleDue(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (s *executionLifecycleScheduleStore) MaterializeDueSchedule(context.Context, string, time.Time) (*jobs.JobOccurrence, error) {
	return nil, nil
}

func (s *executionLifecycleScheduleStore) SkipDueSchedule(context.Context, string, time.Time) error {
	return nil
}

func (s *executionLifecycleScheduleStore) CutoverActive(context.Context) (bool, error) {
	return s.cutover, nil
}

func (s *executionLifecycleScheduleStore) schedule(id string) (jobs.JobSchedule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	schedule, ok := s.schedules[id]
	return schedule, ok
}

type executionLifecycleRepository struct {
	Repository

	mu          sync.Mutex
	nextID      int64
	activateErr error
	deleteErr   error
	activateIn  chan int64
	activateGo  chan struct{}
	deleted     []int64
}

func (r *executionLifecycleRepository) CreateScheduledJob(_ context.Context, job *ScheduledJob) (*ScheduledJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	copy := *job
	copy.ID = r.nextID
	return &copy, nil
}

func (r *executionLifecycleRepository) ActivateScheduledJob(ctx context.Context, id int64) error {
	if r.activateIn != nil {
		select {
		case r.activateIn <- id:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.activateGo != nil {
		select {
		case <-r.activateGo:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.activateErr
}

func (r *executionLifecycleRepository) DeleteScheduledJob(_ context.Context, id int64) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.mu.Lock()
	r.deleted = append(r.deleted, id)
	r.mu.Unlock()
	return nil
}

func newExecutionLifecycleEngine(t *testing.T, repo Repository, definitions *executionLifecycleDefinitionStore, schedules *executionLifecycleScheduleStore) (*Engine, *jobs.Manager) {
	t.Helper()
	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{
		Definitions: definitions,
		Schedules:   schedules,
	}, nil)
	if err := manager.RegisterHandler("scheduler.action", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatalf("register scheduler action handler: %v", err)
	}
	engine := NewEngine(repo, nil)
	engine.SetJobsManager(manager)
	return engine, manager
}

func TestRuntimeExecutionE0_RegisterFailureDeletesInitializingRow(t *testing.T) {
	registerErr := errors.New("definition persistence unavailable")
	definitions := newExecutionLifecycleDefinitionStore()
	definitions.saveErr = registerErr
	schedules := newExecutionLifecycleScheduleStore()
	repo := &executionLifecycleRepository{}
	engine, _ := newExecutionLifecycleEngine(t, repo, definitions, schedules)

	_, err := engine.ScheduleOnce(context.Background(), 1, "chat", 0, time.Now().Add(time.Hour), ActionMessage, "hello")
	if !errors.Is(err, registerErr) {
		t.Fatalf("schedule error = %v, want register failure", err)
	}
	if len(repo.deleted) != 1 {
		t.Fatalf("deleted rows = %v, want one initializing row cleanup", repo.deleted)
	}
	if _, ok := schedules.schedule(redesignedScheduleID(1)); ok {
		t.Fatal("schedule persisted despite definition registration failure")
	}
}

func TestRuntimeExecutionE0_ScheduleSaveFailureDoesNotRetainWrapperDefinition(t *testing.T) {
	saveErr := errors.New("schedule persistence unavailable")
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := newExecutionLifecycleScheduleStore()
	schedules.saveErr = saveErr
	repo := &executionLifecycleRepository{}
	engine, manager := newExecutionLifecycleEngine(t, repo, definitions, schedules)

	_, err := engine.ScheduleOnce(context.Background(), 2, "chat", 0, time.Now().Add(time.Hour), ActionMessage, "hello")
	if !errors.Is(err, saveErr) {
		t.Fatalf("schedule error = %v, want save failure", err)
	}
	if _, ok := manager.Definition(scheduledDefinitionID(1)); ok {
		t.Fatal("wrapper definition retained after schedule save failure")
	}
	if len(repo.deleted) != 1 {
		t.Fatalf("deleted rows = %v, want one initializing row cleanup", repo.deleted)
	}
}

func TestRuntimeExecutionE0_RedesignedScheduleIsNotPublishedBeforeActivation(t *testing.T) {
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := newExecutionLifecycleScheduleStore()
	schedules.cutover = true
	repo := &executionLifecycleRepository{
		activateIn: make(chan int64, 1),
		activateGo: make(chan struct{}),
	}
	engine, _ := newExecutionLifecycleEngine(t, repo, definitions, schedules)

	result := make(chan error, 1)
	go func() {
		_, err := engine.ScheduleOnce(context.Background(), 3, "chat", 0, time.Now().Add(-time.Second), ActionMessage, "hello")
		result <- err
	}()

	id := <-repo.activateIn
	schedule, ok := schedules.schedule(redesignedScheduleID(id))
	if !ok {
		close(repo.activateGo)
		<-result
		t.Fatal("redesigned schedule was not prepared before activation")
	}
	if schedule.Enabled {
		close(repo.activateGo)
		<-result
		t.Fatal("redesigned schedule became executable before scheduled row activation completed")
	}

	close(repo.activateGo)
	if err := <-result; err != nil {
		t.Fatalf("schedule once: %v", err)
	}
	published, ok := schedules.schedule(redesignedScheduleID(id))
	if !ok || !published.Enabled {
		t.Fatalf("published schedule = %+v, exists=%t; want enabled after successful activation", published, ok)
	}
}

func TestRuntimeExecutionE0_ActivationFailureDisablesScheduleAndCleansWrapper(t *testing.T) {
	activateErr := errors.New("activate scheduled row failed")
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := newExecutionLifecycleScheduleStore()
	schedules.cutover = true
	repo := &executionLifecycleRepository{activateErr: activateErr}
	engine, manager := newExecutionLifecycleEngine(t, repo, definitions, schedules)

	_, err := engine.ScheduleOnce(context.Background(), 4, "chat", 0, time.Now().Add(time.Hour), ActionMessage, "hello")
	if !errors.Is(err, activateErr) {
		t.Fatalf("schedule error = %v, want activation failure", err)
	}
	schedule, ok := schedules.schedule(redesignedScheduleID(1))
	if ok && schedule.Enabled {
		t.Fatal("failed schedule remained enabled after activation failure")
	}
	if _, ok := manager.Definition(scheduledDefinitionID(1)); ok {
		t.Fatal("wrapper definition retained after activation failure")
	}
}

func TestRuntimeExecutionE0_ActionJobFailureNeverDeletesTargetDefinition(t *testing.T) {
	activateErr := errors.New("activate scheduled row failed")
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := newExecutionLifecycleScheduleStore()
	schedules.cutover = true
	repo := &executionLifecycleRepository{activateErr: activateErr}
	engine, manager := newExecutionLifecycleEngine(t, repo, definitions, schedules)
	if err := manager.RegisterHandler("target.handler", func(context.Context, jobs.JobDefinition) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(jobs.JobDefinition{
		ID: "target-job", ScopeOwner: "test", QuotaOwner: "test", HandlerType: "target.handler", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := engine.ScheduleOnce(context.Background(), 5, "chat", 0, time.Now().Add(time.Hour), ActionJob, "target-job")
	if !errors.Is(err, activateErr) {
		t.Fatalf("schedule error = %v, want activation failure", err)
	}
	schedule, ok := schedules.schedule(redesignedScheduleID(1))
	if ok && schedule.Enabled {
		t.Fatal("failed ActionJob schedule remained enabled")
	}
	if _, ok := manager.Definition("target-job"); !ok {
		t.Fatal("ActionJob compensation removed the caller-owned target definition")
	}
}

func TestRuntimeExecutionE0_CompensationFailurePreservesPrimaryError(t *testing.T) {
	activateErr := errors.New("activate scheduled row failed")
	cleanupErr := errors.New("disable schedule failed")
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := newExecutionLifecycleScheduleStore()
	schedules.cutover = true
	schedules.disableErr = cleanupErr
	repo := &executionLifecycleRepository{activateErr: activateErr}
	engine, _ := newExecutionLifecycleEngine(t, repo, definitions, schedules)

	_, err := engine.ScheduleOnce(context.Background(), 6, "chat", 0, time.Now().Add(time.Hour), ActionMessage, "hello")
	if !errors.Is(err, activateErr) {
		t.Fatalf("schedule error = %v, want primary activation failure preserved", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("schedule error = %v, want cleanup failure reported", err)
	}
}

func TestRuntimeExecutionE0_AmbiguousPublishFailureRetainsWrapperForRecovery(t *testing.T) {
	publishErr := errors.New("publish acknowledgement lost")
	definitions := newExecutionLifecycleDefinitionStore()
	schedules := newExecutionLifecycleScheduleStore()
	schedules.cutover = true
	schedules.saveErr = publishErr
	schedules.saveErrOnCall = 2
	schedules.persistBeforeSaveErr = true
	repo := &executionLifecycleRepository{}
	engine, manager := newExecutionLifecycleEngine(t, repo, definitions, schedules)

	_, err := engine.ScheduleOnce(context.Background(), 7, "chat", 0, time.Now().Add(-time.Second), ActionMessage, "hello")
	if !errors.Is(err, publishErr) {
		t.Fatalf("schedule error = %v, want ambiguous publish failure", err)
	}
	schedule, ok := schedules.schedule(redesignedScheduleID(1))
	if !ok {
		t.Fatal("ambiguous publish schedule disappeared; want disabled durable recovery record")
	}
	if schedule.Enabled {
		t.Fatal("ambiguous publish schedule remained executable after compensation")
	}
	if _, ok := manager.Definition(scheduledDefinitionID(1)); !ok {
		t.Fatal("ambiguous publish removed wrapper definition needed by a possibly materialized occurrence")
	}
	if len(repo.deleted) != 1 {
		t.Fatalf("deleted compatibility rows = %v, want one", repo.deleted)
	}
}

type executionReconcileOccurrenceStore struct {
	occurrence *jobs.JobOccurrence
}

func (s *executionReconcileOccurrenceStore) MaterializeOccurrence(context.Context, *jobs.JobOccurrence) error {
	return nil
}
func (s *executionReconcileOccurrenceStore) FinalizeOccurrence(context.Context, string, jobs.OccurrenceState) error {
	return nil
}
func (s *executionReconcileOccurrenceStore) CancelOccurrence(context.Context, string, string) error {
	return nil
}
func (s *executionReconcileOccurrenceStore) GetOccurrence(_ context.Context, id string) (*jobs.JobOccurrence, error) {
	if s.occurrence != nil && s.occurrence.ID == id {
		copy := *s.occurrence
		return &copy, nil
	}
	return nil, nil
}
func (s *executionReconcileOccurrenceStore) GetOccurrenceByKey(context.Context, string) (*jobs.JobOccurrence, error) {
	return nil, nil
}
func (s *executionReconcileOccurrenceStore) DeleteTerminalOccurrences(context.Context, string, time.Time, int) (int64, error) {
	return 0, nil
}
func (s *executionReconcileOccurrenceStore) DeferOccurrence(context.Context, string, time.Time) error {
	return nil
}

type executionReconcileRepository struct {
	Repository

	row         *ScheduledJob
	readErrOnce error
	completions int
}

func (r *executionReconcileRepository) GetScheduledJob(context.Context, int64) (*ScheduledJob, error) {
	if r.readErrOnce != nil {
		err := r.readErrOnce
		r.readErrOnce = nil
		return nil, err
	}
	if r.row == nil {
		return nil, nil
	}
	copy := *r.row
	return &copy, nil
}

func (r *executionReconcileRepository) CompleteScheduledJob(context.Context, int64, string, int64, time.Time) error {
	r.completions++
	return nil
}

func TestRuntimeExecutionE0_TransientScheduledJobReadKeepsClaimTracked(t *testing.T) {
	readErr := errors.New("temporary scheduled job read failure")
	repo := &executionReconcileRepository{
		row:         &ScheduledJob{ID: 77, Status: JobStatusRunning, ClaimToken: "claim-77"},
		readErrOnce: readErr,
	}
	occurrences := &executionReconcileOccurrenceStore{occurrence: &jobs.JobOccurrence{
		ID: "occ-77", JobID: "definition-77", State: jobs.OccurrenceCancelled,
	}}
	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{Occurrences: occurrences}, nil)
	engine := NewEngine(repo, nil)
	engine.SetJobsManager(manager)
	claim := &trackedClaim{jobID: 77, claimToken: "claim-77", definitionID: "definition-77", occurrenceID: "occ-77"}
	engine.trackClaim(claim.jobID, claim.claimToken, claim.definitionID, claim.occurrenceID)

	engine.reconcileClaim(context.Background(), claim)
	if got := engine.trackedClaimCount(); got != 1 {
		t.Fatalf("tracked claims after transient read failure = %d, want 1", got)
	}
	if repo.completions != 0 {
		t.Fatalf("completions after failed read = %d, want 0", repo.completions)
	}

	engine.reconcileClaim(context.Background(), claim)
	if repo.completions != 1 {
		t.Fatalf("completions after read recovery = %d, want 1", repo.completions)
	}
	if got := engine.trackedClaimCount(); got != 0 {
		t.Fatalf("tracked claims after settlement = %d, want 0", got)
	}
}
