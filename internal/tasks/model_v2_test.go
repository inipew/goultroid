package tasks

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func validWorkSpec(t *testing.T) WorkSpec {
	t.Helper()
	scope, err := NewScopeIdentity("plugin:test", 7)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerRef("test.handler", 1)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := NewPayloadRef("test.payload", 1, []byte("input"))
	if err != nil {
		t.Fatal(err)
	}
	resource, err := NewResourceRequest("cpu", 1)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := NewWorkSpec(WorkSpecParams{
		ID: "task-1", Scope: scope, QuotaOwner: "user:42", Pool: "general",
		Class: PriorityNormal, Cause: CauseManual, ExecutionTimeout: time.Second,
		Handler: handler, Input: payload, Resources: []ResourceRequest{resource},
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestWorkSpec_DefensivelyCopiesPayloadAndResources(t *testing.T) {
	data := []byte("input")
	payload, err := NewPayloadRef("test.payload", 1, data)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	if got := payload.Data(); !bytes.Equal(got, []byte("input")) {
		t.Fatalf("payload changed after source mutation: %q", got)
	}

	scope, _ := NewScopeIdentity("plugin:test", 1)
	handler, _ := NewHandlerRef("test.handler", 1)
	resource, _ := NewResourceRequest("cpu", 1)
	resources := []ResourceRequest{resource}
	spec, err := NewWorkSpec(WorkSpecParams{
		ID: "task-1", Scope: scope, QuotaOwner: "user:42", Pool: "general",
		Class: PriorityNormal, Cause: CauseManual, Handler: handler, Input: payload,
		Resources: resources,
	})
	if err != nil {
		t.Fatal(err)
	}
	resources[0] = ResourceRequest{}
	copyResources := spec.Resources()
	copyResources[0] = ResourceRequest{}
	if got := spec.Resources()[0]; got.Name() != "cpu" || got.Units() != 1 {
		t.Fatalf("spec resources are mutable through caller copy: %+v", got)
	}
	copyInput := spec.Input().Data()
	copyInput[0] = 'Y'
	if got := spec.Input().Data(); !bytes.Equal(got, []byte("input")) {
		t.Fatalf("spec input is mutable through getter: %q", got)
	}
}

func TestWorkSpec_RejectsInvalidIdentityAndPolicy(t *testing.T) {
	handler, _ := NewHandlerRef("test.handler", 1)
	payload, _ := NewPayloadRef("test.payload", 1, nil)
	_, err := NewWorkSpec(WorkSpecParams{ID: "task-1", Handler: handler, Input: payload})
	if err == nil {
		t.Fatal("expected invalid work spec to be rejected")
	}
}

func TestTaskResult_Validation(t *testing.T) {
	now := time.Now().UTC()
	if _, err := NewTaskResult(TaskResultParams{
		TaskID: "task-1", Outcome: OutcomeFailed, Cause: ResultCauseNone, FinishedAt: now,
	}); err == nil {
		t.Fatal("expected failed result without cause to be rejected")
	}
	result, err := NewTaskResult(TaskResultParams{
		TaskID: "task-1", Outcome: OutcomeSucceeded, Cause: ResultCauseNone,
		StartedAt: now.Add(-time.Second), FinishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome() != OutcomeSucceeded {
		t.Fatalf("unexpected outcome: %v", result.Outcome())
	}
}

func TestAdmissionError_IsStructured(t *testing.T) {
	err := &AdmissionError{Reason: RejectResultBackpress}
	if !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("expected errors.Is to match admission rejection: %v", err)
	}
	if err.Reason != RejectResultBackpress {
		t.Fatalf("unexpected rejection reason: %q", err.Reason)
	}
}

func TestWorkerAssignment_BindsPermitToTaskAndPool(t *testing.T) {
	spec := validWorkSpec(t)
	permit, err := NewPhysicalPermit("general", "worker-1", 2, spec.ID(), 9)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkerAssignment(permit, spec); err != nil {
		t.Fatalf("valid assignment rejected: %v", err)
	}
	wrong, _ := NewPhysicalPermit("scheduler", "worker-2", 2, spec.ID(), 10)
	if _, err := NewWorkerAssignment(wrong, spec); err == nil {
		t.Fatal("expected pool-mismatched permit to be rejected")
	}
}
