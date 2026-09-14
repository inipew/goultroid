package taskengine

import (
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func registeredCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := NewCatalog(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := tasks.NewHandlerRef("telegram.command", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterHandler(HandlerDescriptor{
		Ref:             handler,
		PayloadKind:     "command",
		PayloadVersions: []uint16{1},
		MaxPayloadBytes: 4096,
		AllowedPools:    []tasks.PoolID{"default"},
		AllowedClasses:  []tasks.PriorityClass{tasks.PriorityInteractive, tasks.PriorityNormal},
	}); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func catalogSpec(t *testing.T, handler tasks.HandlerRef, payload tasks.PayloadRef, resources []tasks.ResourceRequest) tasks.WorkSpec {
	t.Helper()
	scope, err := tasks.NewScopeIdentity("plugin:test", 1)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := tasks.NewWorkSpec(tasks.WorkSpecParams{
		ID:               "task-1",
		Scope:            scope,
		QuotaOwner:       "actor:1",
		Pool:             "default",
		Class:            tasks.PriorityInteractive,
		Cause:            tasks.CauseInteractiveCommand,
		ExecutionTimeout: time.Second,
		Handler:          handler,
		Input:            payload,
		Resources:        resources,
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func requireValidationCode(t *testing.T, err error, code ValidationCode) {
	t.Helper()
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || validationErr.Code != code {
		t.Fatalf("expected validation code %q, got %v", code, err)
	}
}

func TestCatalogValidateSpec_RejectsUnknownHandler(t *testing.T) {
	catalog := registeredCatalog(t)
	handler, _ := tasks.NewHandlerRef("unknown", 1)
	payload, _ := tasks.NewPayloadRef("command", 1, []byte(".ping"))
	err := catalog.ValidateSpec(catalogSpec(t, handler, payload, nil))
	requireValidationCode(t, err, ValidationUnknownHandler)
}

func TestCatalogValidateSpec_RejectsUnknownPayloadVersion(t *testing.T) {
	catalog := registeredCatalog(t)
	handler, _ := tasks.NewHandlerRef("telegram.command", 1)
	payload, _ := tasks.NewPayloadRef("command", 2, []byte(".ping"))
	err := catalog.ValidateSpec(catalogSpec(t, handler, payload, nil))
	requireValidationCode(t, err, ValidationUnknownPayload)
}

func TestCatalogValidateSpec_RejectsUnknownResource(t *testing.T) {
	catalog := registeredCatalog(t)
	handler, _ := tasks.NewHandlerRef("telegram.command", 1)
	payload, _ := tasks.NewPayloadRef("command", 1, []byte(".ping"))
	resource, _ := tasks.NewResourceRequest("gpu", 1)
	err := catalog.ValidateSpec(catalogSpec(t, handler, payload, []tasks.ResourceRequest{resource}))
	requireValidationCode(t, err, ValidationUnknownResource)
}

func TestCatalogValidateSpec_AcceptsRegisteredReferences(t *testing.T) {
	catalog := registeredCatalog(t)
	handler, _ := tasks.NewHandlerRef("telegram.command", 1)
	payload, _ := tasks.NewPayloadRef("command", 1, []byte(".ping"))
	resource, _ := tasks.NewResourceRequest("media", 1)
	if err := catalog.ValidateSpec(catalogSpec(t, handler, payload, []tasks.ResourceRequest{resource})); err != nil {
		t.Fatalf("registered spec rejected: %v", err)
	}
}
