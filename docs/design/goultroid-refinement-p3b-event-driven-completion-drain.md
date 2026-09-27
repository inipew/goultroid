# Goultroid — P3-B Event-Driven TaskEngine Completion Drain Closure

Date: 2026-09-27  
Branch: `test-next`  
Implementation baseline: `440c9ba977ecaa95da4cf9ce232305915f2a3936` — `refactor(taskengine): make completion drain event-driven`

## Status

**P3-B — CLOSED.**

## Source finding

Current TaskEngine already had an event-driven admitted-task drain:

```text
activeTasks -> 0 while quiesced
  -> close Engine.drainDone
```

The remaining historical polling debt was narrower:

```text
completionDelivery.drain()
  -> time.NewTicker(1ms)
  -> repeatedly inspect pending / active / queue
```

That meant graceful shutdown could wake every millisecond solely to discover whether completion callbacks had finished.

## Implementation

Completion delivery now owns a generation-scoped drain broadcast:

```text
drained state
  -> drainCh is closed

enqueue first callback of next generation
  -> markBusy()
  -> replace with a fresh open drainCh

callback settlement
  -> pending/active counters transition
  -> signalDrained() when pending == 0 && active == 0 && queue == 0
  -> close drainCh

Drain(ctx)
  -> await drainCh
     OR ctx.Done()
```

Channel close is intentionally used instead of a single buffered wake token so all concurrent drain waiters observe the same transition.

## Preserved invariants

The change does not alter:

- callback FIFO queue ownership;
- configured delivery concurrency;
- callback reservation/backpressure capacity;
- lazy completion worker startup/retirement;
- callback execution semantics;
- TaskEngine result settlement ordering;
- graceful/forced shutdown authority;
- global caller deadline behavior.

No new permanent worker, ticker, runtime, registry, executor, or unbounded state was introduced.

## Regression coverage

`internal/taskengine/delivery_drain_test.go` covers:

1. drain waits while a completion callback is active;
2. completion broadcasts to multiple concurrent drain waiters;
3. context deadline wins against a blocked callback;
4. drain succeeds after the callback is released.

`internal/architecture/taskengine_completion_drain_p3b_test.go`:

- locates `completionDelivery.drain()` with Go AST parsing;
- rejects `time.NewTicker`, `time.Sleep`, and `time.After` in that method;
- requires the drain generation channel and event-driven transition helpers.

## Formatting and verification

All changed Go files were passed through local `gofmt` before commit.

CI was not inspected.

The current environment still lacks a complete executable Goultroid checkout/module cache, so this session does not claim execution of:

```text
go test ./internal/taskengine
go test -race ./internal/taskengine
go vet ./...
go build -o bin/goultroid ./cmd/goultroid
```

Those commands remain desirable when a real checkout is available, but the source-level P3-B architecture requirement is closed.

## Relationship to P3-A / P3-C

P3-B can close independently because it removes an explicitly confirmed polling mechanism rather than optimizing from benchmark suspicion.

P3-A benchmark measurements remain OPEN.

Therefore:

```text
P3-B CLOSED
P3-A measurements OPEN
P3-C BLOCKED until P3-A evidence exists
```
