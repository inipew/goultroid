package jobs

import "sync"

// occurrenceGate serializes an occurrence's TaskEngine admission and durable
// cancellation without making unrelated Jobs wait on a global Manager lock.
type occurrenceGate struct {
	mu   sync.Mutex
	refs int
}

func (m *Manager) lockOccurrence(id string) func() {
	m.occurrenceGatesMu.Lock()
	if m.occurrenceGates == nil {
		m.occurrenceGates = make(map[string]*occurrenceGate)
	}
	gate := m.occurrenceGates[id]
	if gate == nil {
		gate = &occurrenceGate{}
		m.occurrenceGates[id] = gate
	}
	gate.refs++
	m.occurrenceGatesMu.Unlock()

	gate.mu.Lock()
	return func() {
		gate.mu.Unlock()
		m.occurrenceGatesMu.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(m.occurrenceGates, id)
		}
		m.occurrenceGatesMu.Unlock()
	}
}
