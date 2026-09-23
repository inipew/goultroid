# Assistant Parity P8-J — final parity freeze and closure

## Status

**P8 is CLOSED.**

P8-J is the final cleanup/freeze phase for the Assistant parity program defined by:

- `docs/bug/4.md`;
- `docs/design/assistant-parity-p8a-inventory.md`;
- phase evidence P8-B through P8-I.

P8-J does not add another Assistant feature or runtime. It reconciles the frozen inventory with the implementation that now exists, preserves the architecture/regression fences, records intentional differences, and closes the roadmap.

Baseline entering P8-J:

`13f9cd6fd1cb2b55f3d5c09fd319d886d952eed1` — `test(assistant): close P8-I resource acceptance`.

The syntax fixes reported immediately before P8-I are already contained in:

`e7297b1940b481a59c03b06213504443293c1e7d` — `fix(assistant): restore P8-E/P8-F Go syntax`.

## Final phase state

```text
P8-A  behavioral inventory + target freeze                 CLOSED
P8-B  production self-inline / RenderBridge                 CLOSED
P8-C  calculator callback-heavy canary                      CLOSED
P8-D  representative rich search/lookup inline              CLOSED
P8-E  representative interactive downloader workflow       CLOSED
P8-F  behavioral matrix + locale-aware Assistant UI         CLOSED
P8-G  reference-driven compatibility/dead-stack reclamation CLOSED
P8-H  unload/reload/generation cross-surface acceptance     CLOSED
P8-I  resource / idle / high-load acceptance                CLOSED
P8-J  final cleanup, parity freeze, and closure              CLOSED
```

No P8 capability remains deferred.

## Final behavioral matrix

| Domain | Goultroid final state |
| --- | --- |
| Assistant runtime | CLOSED |
| public `/start` | CLOSED |
| owner shell / Status / Help | CLOSED |
| Settings + bounded free-form input | CLOSED |
| locale-aware Assistant UI | CLOSED |
| canonical dual-mode commands | CLOSED |
| resource-bearing Assistant execution | CLOSED |
| SavedResponse Assistant/inline/callback/deep-link surfaces | CLOSED |
| PM relay / audience / block / force-sub / broadcast | CLOSED |
| group manager / rules / moderation | CLOSED |
| reply / topic / media context | CLOSED |
| MyXL complex a2 workflow | CLOSED |
| self-inline rendering | CLOSED |
| callback-heavy calculator | CLOSED |
| representative rich lookup | CLOSED |
| representative heavy downloader workflow | CLOSED |
| unload / reload / generation invalidation | CLOSED |
| idle / mixed-load / resource acceptance | CLOSED |
| legacy a1/menu compatibility stack | ABSENT + REGRESSION-FENCED |

## Frozen authority map

The closure preserves one authority for each concern.

### Command and feature discovery

`core.Router` remains the canonical command execution authority.

`feature.Registry` remains the canonical feature/interaction declaration authority.

There is no Assistant-specific command registry or feature registry.

### Interaction state and callbacks

`interaction.Runtime` remains the bounded a2 session/state authority.

`interaction.Dispatcher` remains the typed callback registration and validation authority.

`orchestration.Engine` remains the feature-facing transition/action boundary.

There is no global feature callback map or feature-owned session runtime.

### Inline

Inline vNext remains the inline resolution/cache/lifecycle authority.

Feature-owned inline registrations remain generation-scoped.

Interactive results use typed ActionRows compiled through a2. Feature-owned raw callback bytes remain prohibited by architecture/behavior tests.

### Heavy work and resources

TaskEngine remains the shared execution/backpressure/resource authority.

Downloader final actions reuse TaskEngine `download` and `process` resources rather than creating a downloader scheduler.

P8-I keeps combined acceptance for:

- zero-idle physical workers;
- callback/session pressure;
- bounded interaction state;
- bounded Inline vNext cache;
- managed network resource release;
- bounded RPC metric labels;
- `download/process` lease release;
- Assistant interaction restart;
- TaskEngine worker retirement/shutdown;
- goroutine/heap/RSS settle gates.

### Telegram RPC

`telegram.RPCExecutor` remains the retry/FloodWait authority.

Features do not own a retry loop, FloodWait engine, or RPC limiter.

### Self-inline

`selfinline.Renderer` remains the feature-facing userbot→own-Assistant rendering authority.

Authorization is evaluated per render against:

- current plugin enable state;
- `telegram.read`;
- `telegram.send_message`.

The bridge owns no result cache, worker, ticker, session runtime, or retry engine.

## Final representative proofs

### Calculator

P8-C remains the callback-heavy proof:

- userbot command uses self-inline renderer;
- Inline vNext creates bounded a2 state;
- fixed typed actions mutate state;
- stale revisions and binding mismatches fail closed;
- evaluator is bounded arithmetic only;
- no `eval`, shell, or remote code execution.

### Wikipedia

P8-D remains the representative rich network lookup proof:

- one canonical backend shared with command behavior;
- managed HTTP capability;
- bounded query/results/body size;
- explicit Inline vNext cache policy;
- no plugin-owned query cache/worker.

P8 does not require provider-for-provider copies such as Google, F-Droid, Play Store, OrangeFox, Twitter/X, Saavn, or similar catalogs.

### Downloader

P8-E remains the representative heavy interactive workflow:

```text
inline source
  ↓
typed media/format selection
  ↓
prepared a2 action
  ↓
TaskEngine admission
  ↓
download:1
  └ extractor only → process:1
  ↓
canonical downloader registry/storage/media ownership
```

Direct HTTP work does not require `process`.

## Final lifecycle acceptance

P8-H freezes the generation contract:

```text
load generation N
  ↓
command / inline / screen / action / deep-link visible
  ↓
disable
  ├ TaskEngine scope cancelled
  ├ command removed
  ├ inline removed
  ├ a2 sessions/input cancelled
  ├ old prepared action invalid
  └ old prepared deep-link lease invalid
  ↓
enable generation N+1
  ├ fresh surfaces visible
  ├ fresh scoped TaskEngine client
  └ generation N remains dead
```

A durable deep-link token may survive reload, but execution must re-prepare against the current generation.

## Final resource acceptance

P8-I freezes combined acceptance rather than relying only on isolated subsystem tests.

The acceptance workload includes:

- 10,000 inline queries;
- 512 typed calculator callbacks;
- per-actor interaction capacity pressure;
- bounded Wikipedia cache/network load;
- concurrent downloader `download+process` ownership;
- shared RPC metrics cardinality pressure;
- active-work scope cancellation;
- Assistant interaction restart;
- zero-idle worker retirement;
- bounded shutdown;
- process goroutine/heap/RSS settle checks.

Inline cache diagnostics added by P8-I are read-only views of the existing cache. They do not create a second metrics store.

## Legacy and compatibility freeze

`internal/assistant/legacy_stack_test.go` remains mandatory.

The following retired Assistant constructs remain forbidden:

- `LegacyAssistantMenu`;
- `CompatibilityHost`;
- `MenuInstanceStore`;
- legacy Assistant menu/presentation/callback packages;
- `a1:` callback envelopes;
- `NewBotClient`;
- `legacyChatForPeer`;
- old Router compatibility dispatch wrappers.

P8-G's reclamation rule remains in force: compatibility code belonging to independent storage/media migrations is not classified as Assistant legacy merely because its filename contains `compat`.

## Intentional differences from Ultroid

These are final product/architecture choices, not P8 defects.

1. Goultroid uses typed scoped registries and shared execution authorities instead of Ultroid-style decorators/global handlers.
2. Canonical locale support is intentionally bounded to English and Indonesian; all Ultroid language packs are not required.
3. Wikipedia is the representative network lookup proof; every provider catalog is not cloned.
4. Arbitrary remote code execution/Piston-style inline execution remains excluded.
5. Downloader URL assets remain owned by the canonical downloader/media path; P8 does not add an Assistant-only uploader.
6. Exact button text, emoji, provider thumbnails, and screen ordering are not byte-for-byte parity requirements.

## Regression fence set

P8-J keeps all phase fences as part of the closure:

- `internal/architecture/assistant_p8b_test.go`;
- `internal/architecture/assistant_p8c_test.go`;
- `internal/architecture/assistant_p8d_test.go`;
- `internal/architecture/assistant_p8e_test.go`;
- `internal/architecture/assistant_p8f_test.go`;
- `internal/architecture/assistant_p8g_reclamation_test.go`;
- `internal/architecture/assistant_p8h_test.go`;
- `internal/architecture/assistant_p8i_test.go`;
- `internal/assistant/legacy_stack_test.go`;
- `internal/architecture/assistant_p8j_test.go`.

P8-J does not replace the phase-specific tests with one giant test. The final fence only ensures the evidence remains present and the canonical documents remain reconciled to CLOSED.

## Verification boundary

The container available for this session cannot resolve `github.com`, so a full checkout cannot be materialized to execute `go build`, `go vet`, or `go test ./...` here.

P8-J therefore does not claim runtime test execution in this session. Source-level acceptance, explicit architecture fences, and gofmt for changed Go source are recorded accurately.

CI was not inspected.

## Closure rule

P8 is now a frozen parity baseline.

Future work should not reopen P8 merely to add ordinary Assistant features or additional providers. Reopen the closure only for a regression against the frozen behavioral/resource/lifecycle matrix; otherwise start a new phase/feature plan on top of the P8 architecture.
