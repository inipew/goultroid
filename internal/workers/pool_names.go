package workers

// PoolInteractive isolates latency-sensitive command/callback work from the
// general pool. Keep the identifier stable while the legacy worker manager is
// still part of the execution compatibility surface.
const PoolInteractive = "interactive"
