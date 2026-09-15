package scheduler

import "testing"

func TestTrackedClaimRetainsOwningDefinition(t *testing.T) {
	e := &Engine{claims: make(map[int64]*trackedClaim)}
	e.trackClaim(7, "lease", "target.job", "occ-1")
	c := e.claims[7]
	if c == nil || c.definitionID != "target.job" {
		t.Fatalf("claim lost target definition: %+v", c)
	}
}
