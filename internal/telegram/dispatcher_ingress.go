package telegram

// admitIngress is the single admission gate for Telegram update handlers.
// Quiesce holds the same mutex while closing acceptingUpdates, so no new
// positive WaitGroup Add can race with Drain starting Wait(). The returned
// release must be called exactly once for every accepted ingress operation.
func (d *Dispatcher) admitIngress() (release func(), accepted bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.acceptingUpdates.Load() || d.stopping.Load() {
		return nil, false
	}
	d.inFlight.Add(1)
	return d.inFlight.Done, true
}
