package callback

// state.go is retained as a wiring index for the callback state subsystem.
// Actual implementation is now split across:
//   - scope.go     -> StateScope, StateEntry, stateItem
//   - store.go     -> StateStore, Store, Get, Consume
//   - lifecycle.go -> Prune, Start, Stop
//
// Keep this file small; do not add new logic here.
