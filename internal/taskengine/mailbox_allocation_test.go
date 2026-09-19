package taskengine

import "testing"

func TestZeroIdleDefaultDoesNotPreallocateWorkerMailboxes(t *testing.T) {
	e := NewEngine(NewDefaultConfig())
	for pool, mailboxes := range e.workerMailboxes {
		for slot, mailbox := range mailboxes {
			if mailbox != nil {
				t.Fatalf("pool %s slot %d preallocated mailbox while zero-idle", pool, slot)
			}
		}
	}
}
