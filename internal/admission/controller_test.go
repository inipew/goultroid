package admission

import (
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
