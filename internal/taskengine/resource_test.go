package taskengine

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type testHandlerResolver struct{}

func (testHandlerResolver) ResolveHandler(ref string, input any) (tasks.HandlerFunc, error) {
	return func(context.Context) error { return nil }, nil
}

func TestHandlerReferenceResolvedAtAdmission(t *testing.T) {
	e := NewEngine(Config{Pools: map[tasks.PoolID]PoolEngineConfig{"p": {Concurrency: 1, BacklogLimit: 1}}, ResultCapacity: 1})
	e.SetHandlerResolver(testHandlerResolver{})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{ID: "ref", QuotaOwner: "o", Pool: "p", HandlerRef: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticket.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestResourceReservationPrecedesDispatch(t *testing.T) {
	e := NewEngine(Config{
		Pools:          map[tasks.PoolID]PoolEngineConfig{"p": {Concurrency: 2, BacklogLimit: 4}},
		ResultCapacity: 4, ResourceCapacities: map[string]int64{"process": 1},
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	release := make(chan struct{})
	started := make(chan string, 2)
	submit := func(id string) tasks.Ticket {
		ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
			ID: tasks.TaskID(id), QuotaOwner: "owner", Pool: "p",
			Resources: []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
			Handler:   func(context.Context) error { started <- id; <-release; return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		return ticket
	}
	first, second := submit("first"), submit("second")
	<-started
	select {
	case id := <-started:
		t.Fatalf("second resource user started early: %s", id)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := first.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}


func TestTaskHandlerReceivesHeldResourceMarkersAfterAdmission(t *testing.T) {
	e := NewEngine(Config{
		Pools:              map[tasks.PoolID]PoolEngineConfig{"p": {Concurrency: 1, BacklogLimit: 2}},
		ResultCapacity:     2,
		ResourceCapacities: map[string]int64{"download": 1, "media": 1},
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })

	observed := make(chan error, 1)
	ticket, err := e.Submit(context.Background(), tasks.WorkSpec{
		ID:         "resource-context",
		QuotaOwner: "owner",
		Pool:       "p",
		Resources: []tasks.ResourceRequirement{
			{Name: "download", Amount: 1},
			{Name: "media", Amount: 1},
		},
		Handler: func(ctx context.Context) error {
			if !tasks.HasHeldResource(ctx, "download") {
				observed <- fmt.Errorf("download lease marker missing")
				return nil
			}
			if !tasks.HasHeldResource(ctx, "media") {
				observed <- fmt.Errorf("media lease marker missing")
				return nil
			}
			if tasks.HasHeldResource(ctx, "process") {
				observed <- fmt.Errorf("undeclared process lease marker present")
				return nil
			}
			observed <- nil
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticket.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
}
