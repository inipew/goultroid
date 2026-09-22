package admission

import (
	"fmt"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestAdmission_QuotaAndBacklog(t *testing.T) {
	poolConfigs := map[tasks.PoolID]PoolConfig{
		"general": {BacklogLimit: 2, PayloadBudget: 1000},
	}
	ctrl := NewController(poolConfigs)
	ctrl.SetOwnerLimits("user-1", OwnerLimits{MaxWaiting: 1, MaxActive: 1, Weight: 1, MaxPayloadByte: 500})

	spec1 := tasks.WorkSpec{
		ID:         "task-1",
		QuotaOwner: "user-1",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
	}

	if err := ctrl.CanAdmit(spec1, 100); err != nil {
		t.Fatalf("expected spec1 to be admitted, got: %v", err)
	}
	ctrl.Enqueue(&QueueEntry{Spec: spec1, PayloadSize: 100})

	// Second task from user-1 should exceed MaxWaiting = 1
	spec2 := tasks.WorkSpec{
		ID:         "task-2",
		QuotaOwner: "user-1",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
	}
	if err := ctrl.CanAdmit(spec2, 100); err == nil {
		t.Fatalf("expected spec2 to be rejected due to owner MaxWaiting")
	}

	// Task from user-2 should be admitted
	spec3 := tasks.WorkSpec{
		ID:         "task-3",
		QuotaOwner: "user-2",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
	}
	if err := ctrl.CanAdmit(spec3, 100); err != nil {
		t.Fatalf("expected spec3 to be admitted for user-2, got: %v", err)
	}
	ctrl.Enqueue(&QueueEntry{Spec: spec3, PayloadSize: 100})

	// Third task overall should exceed BacklogLimit = 2
	spec4 := tasks.WorkSpec{
		ID:         "task-4",
		QuotaOwner: "user-3",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
	}
	if err := ctrl.CanAdmit(spec4, 100); err == nil {
		t.Fatalf("expected spec4 to be rejected due to pool BacklogLimit")
	}
}

func TestAdmission_DRR_OrderingKeyAndMaxActive(t *testing.T) {
	poolConfigs := map[tasks.PoolID]PoolConfig{
		"general": {BacklogLimit: 10, PayloadBudget: 10000},
	}
	ctrl := NewController(poolConfigs)
	ctrl.SetOwnerLimits("user-1", OwnerLimits{MaxWaiting: 10, MaxActive: 1, Weight: 1, MaxPayloadByte: 10000})
	ctrl.SetOwnerLimits("user-2", OwnerLimits{MaxWaiting: 10, MaxActive: 2, Weight: 1, MaxPayloadByte: 10000})

	// User-1 submits two tasks with same ordering key
	spec1 := tasks.WorkSpec{ID: "u1-t1", QuotaOwner: "user-1", Pool: "general", Class: tasks.PriorityNormal, OrderingKey: "chat:100"}
	spec2 := tasks.WorkSpec{ID: "u1-t2", QuotaOwner: "user-1", Pool: "general", Class: tasks.PriorityNormal, OrderingKey: "chat:100"}
	ctrl.Enqueue(&QueueEntry{Spec: spec1})
	ctrl.Enqueue(&QueueEntry{Spec: spec2})

	// User-2 submits task with different ordering key
	spec3 := tasks.WorkSpec{ID: "u2-t1", QuotaOwner: "user-2", Pool: "general", Class: tasks.PriorityNormal, OrderingKey: "chat:200"}
	ctrl.Enqueue(&QueueEntry{Spec: spec3})

	// Candidate 1 should be u1-t1
	c1, err := ctrl.SelectCandidate("general")
	if err != nil || c1.Spec.ID != "u1-t1" {
		t.Fatalf("expected candidate u1-t1, got %v (err: %v)", c1, err)
	}

	// Candidate 2 cannot be u1-t2 because:
	// 1. user-1 has MaxActive = 1 (already active)
	// 2. ordering key "chat:100" is locked by u1-t1
	// So Candidate 2 must be u2-t1!
	c2, err := ctrl.SelectCandidate("general")
	if err != nil || c2.Spec.ID != "u2-t1" {
		t.Fatalf("expected candidate u2-t1, got %v (err: %v)", c2, err)
	}

	// Candidate 3 should return ErrNoEligibleTask
	_, err = ctrl.SelectCandidate("general")
	if err != ErrNoEligibleTask {
		t.Fatalf("expected ErrNoEligibleTask, got %v", err)
	}

	// Finish u1-t1
	ctrl.OnTaskTerminal(c1.Spec)

	// Now u1-t2 is eligible!
	c3, err := ctrl.SelectCandidate("general")
	if err != nil || c3.Spec.ID != "u1-t2" {
		t.Fatalf("expected candidate u1-t2, got %v (err: %v)", c3, err)
	}
}

func TestAdmission_DeadlineExpiry(t *testing.T) {
	poolConfigs := map[tasks.PoolID]PoolConfig{
		"general": {BacklogLimit: 10, PayloadBudget: 10000},
	}
	ctrl := NewController(poolConfigs)

	now := time.Now()
	expiredSpec := tasks.WorkSpec{
		ID:            "exp-1",
		QuotaOwner:    "user-1",
		Pool:          "general",
		Class:         tasks.PriorityNormal,
		QueueDeadline: now.Add(-time.Second),
	}
	validSpec := tasks.WorkSpec{
		ID:            "valid-1",
		QuotaOwner:    "user-1",
		Pool:          "general",
		Class:         tasks.PriorityNormal,
		QueueDeadline: now.Add(time.Minute),
	}

	ctrl.Enqueue(&QueueEntry{Spec: expiredSpec})
	ctrl.Enqueue(&QueueEntry{Spec: validSpec})

	expired := ctrl.PopExpired("general", now)
	if len(expired) != 1 || expired[0].Spec.ID != "exp-1" {
		t.Fatalf("expected 1 expired entry 'exp-1', got %v", expired)
	}

	if ctrl.WaitingCount("general") != 1 {
		t.Errorf("expected 1 remaining task, got %d", ctrl.WaitingCount("general"))
	}

	cand, err := ctrl.SelectCandidate("general")
	if err != nil || cand.Spec.ID != "valid-1" {
		t.Fatalf("expected 'valid-1' candidate, got %v (err: %v)", cand, err)
	}
}

func TestAdmission_RemoveTask(t *testing.T) {
	poolConfigs := map[tasks.PoolID]PoolConfig{
		"general": {BacklogLimit: 10, PayloadBudget: 10000},
	}
	ctrl := NewController(poolConfigs)

	spec := tasks.WorkSpec{
		ID:         "cancel-me",
		QuotaOwner: "user-1",
		Pool:       "general",
		Class:      tasks.PriorityNormal,
	}
	ctrl.Enqueue(&QueueEntry{Spec: spec, PayloadSize: 100})

	entry, ok := ctrl.RemoveTask("cancel-me")
	if !ok || entry.Spec.ID != "cancel-me" {
		t.Fatalf("expected successful removal of 'cancel-me'")
	}

	if ctrl.WaitingCount("general") != 0 {
		t.Errorf("expected 0 waiting tasks, got %d", ctrl.WaitingCount("general"))
	}

	_, err := ctrl.SelectCandidate("general")
	if err != ErrNoEligibleTask {
		t.Errorf("expected ErrNoEligibleTask after removal, got: %v", err)
	}
}

func TestControllerWeightedClassesDoNotStarve(t *testing.T) {
	c := NewController(map[tasks.PoolID]PoolConfig{"general": {}})
	classes := []tasks.PriorityClass{tasks.PriorityInteractive, tasks.PriorityNormal, tasks.PriorityBackground, tasks.PriorityMaintenance}
	for _, class := range classes {
		for i := 0; i < 100; i++ {
			c.Enqueue(&QueueEntry{Spec: tasks.WorkSpec{ID: tasks.TaskID(fmt.Sprintf("%s-%d", class, i)), Pool: "general", Class: class, QuotaOwner: "owner"}})
		}
	}
	counts := map[tasks.PriorityClass]int{}
	for i := 0; i < 60; i++ {
		entry, err := c.SelectCandidate("general")
		if err != nil {
			t.Fatal(err)
		}
		counts[entry.Spec.Class]++
		c.OnTaskTerminal(entry.Spec)
	}
	for class, want := range map[tasks.PriorityClass]int{tasks.PriorityInteractive: 32, tasks.PriorityNormal: 16, tasks.PriorityBackground: 8, tasks.PriorityMaintenance: 4} {
		if counts[class] != want {
			t.Errorf("%s dispatches = %d, want %d", class, counts[class], want)
		}
	}
}

func TestControllerWeightedOwners(t *testing.T) {
	c := NewController(map[tasks.PoolID]PoolConfig{"general": {}})
	c.SetOwnerLimits("heavy", OwnerLimits{Weight: 3})
	for _, owner := range []tasks.OwnerID{"light", "heavy"} {
		for i := 0; i < 100; i++ {
			c.Enqueue(&QueueEntry{Spec: tasks.WorkSpec{ID: tasks.TaskID(fmt.Sprintf("%s-%d", owner, i)), Pool: "general", Class: tasks.PriorityNormal, QuotaOwner: owner}})
		}
	}
	counts := map[tasks.OwnerID]int{}
	for i := 0; i < 40; i++ {
		entry, err := c.SelectCandidate("general")
		if err != nil {
			t.Fatal(err)
		}
		counts[entry.Spec.QuotaOwner]++
		c.OnTaskTerminal(entry.Spec)
	}
	if counts["heavy"] != 30 || counts["light"] != 10 {
		t.Fatal(counts)
	}
}


func TestP7LAdmissionIndependentTopicsDoNotHeadOfLineBlock(t *testing.T) {
	ctrl := NewController(map[tasks.PoolID]PoolConfig{
		"interactive": {BacklogLimit: 16, PayloadBudget: 1 << 20},
	})
	ctrl.SetOwnerLimits("telegram:chat:77", OwnerLimits{
		MaxWaiting: 16, MaxActive: 4, Weight: 1, MaxPayloadByte: 1 << 20,
	})

	specA1 := tasks.WorkSpec{
		ID: "a1", QuotaOwner: "telegram:chat:77", Pool: "interactive",
		Class: tasks.PriorityInteractive, OrderingKey: "chat:77:topic:10",
	}
	specA2 := tasks.WorkSpec{
		ID: "a2", QuotaOwner: "telegram:chat:77", Pool: "interactive",
		Class: tasks.PriorityInteractive, OrderingKey: "chat:77:topic:10",
	}
	specB1 := tasks.WorkSpec{
		ID: "b1", QuotaOwner: "telegram:chat:77", Pool: "interactive",
		Class: tasks.PriorityInteractive, OrderingKey: "chat:77:topic:20",
	}
	ctrl.Enqueue(&QueueEntry{Spec: specA1})
	ctrl.Enqueue(&QueueEntry{Spec: specA2})
	ctrl.Enqueue(&QueueEntry{Spec: specB1})

	first, err := ctrl.SelectCandidate("interactive")
	if err != nil || first.Spec.ID != "a1" {
		t.Fatalf("first candidate=%v err=%v, want a1", first, err)
	}

	second, err := ctrl.SelectCandidate("interactive")
	if err != nil || second.Spec.ID != "b1" {
		t.Fatalf("second candidate=%v err=%v, want b1 while topic 10 is locked", second, err)
	}
	if second.Spec.OrderingKey == first.Spec.OrderingKey {
		t.Fatalf("independent topic reused locked ordering key %q", second.Spec.OrderingKey)
	}

	ctrl.OnTaskTerminal(first.Spec)
	third, err := ctrl.SelectCandidate("interactive")
	if err != nil || third.Spec.ID != "a2" {
		t.Fatalf("third candidate=%v err=%v, want a2 after topic 10 unlock", third, err)
	}
}
