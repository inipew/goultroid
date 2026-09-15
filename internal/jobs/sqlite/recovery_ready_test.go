package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
)

func TestListUnresolvedOccurrencesIncludesReadyIntent(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := InitSchema(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db.DB)
	if err := store.SaveDefinition(context.Background(), &jobs.JobDefinition{
		ID:          "job:ready",
		ScopeOwner:  "plugin:test",
		QuotaOwner:  "user:test",
		HandlerType: "test",
		Version:     1,
		Pool:        "general",
		Class:       "normal",
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}
	occ := &jobs.JobOccurrence{
		ID:            "occ:ready",
		JobID:         "job:ready",
		OccurrenceKey: "key:ready",
		ScheduledFor:  time.Now().UTC(),
		ReadyAt:       time.Now().UTC(),
		State:         jobs.OccurrenceReady,
	}
	if err := store.MaterializeOccurrence(context.Background(), occ); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListUnresolvedOccurrences(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != occ.ID || got[0].State != jobs.OccurrenceReady {
		t.Fatalf("recoverable occurrences=%#v, want ready occurrence %q", got, occ.ID)
	}
}
