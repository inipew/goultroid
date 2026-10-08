# Goultroid Message Automation — A0 Baseline and Incremental Recovery

Status: **A0 BASELINE RECORDED** — baseline source inspection only; no runtime test execution claimed.
Branch: `test-next`
Baseline GitHub HEAD: `3b62276798d5c638c99059fa2657f5b763345247` — `Revert "docs(design): add message hook execution model v2 plan"`
Prior plan (historical, not current): `docs/design/goultroid-message-hook-execution-model-v2-ai-plan.md` at `a57130c3`.

## 1. Mandatory execution discipline

- **Refresh the actual GitHub `test-next` HEAD and record SHA + subject before starting each phase.**
- Format **every Go source and test file changed** using `gofmt` before committing.
- Update/add focused tests whenever production logic, signatures, routing, or lifecycle change, and format those test files too.
- Run targeted tests and race checks where an appropriate Go toolchain/dependency environment is available; report unexecuted gates as unverified.
- **Do not inspect, poll, or trigger CI** unless the user explicitly requests it.
- Commit only bounded, reviewed changes with a phase-specific acceptance statement; no mass replay of reverted R0–R8.
- Keep the existing dispatcher, TaskEngine, shared RPC executor, canonical envelope, a2 interaction and plugin lifecycle. Do not add a second runtime, registry, downloader, task queue, retry engine, or callback protocol.
- All caches and state must be bounded. Keep idle footprint close to zero. Preserve context and shutdown cancellation.
- Preserve PMPermit as **private-user-chat only**, independently of group moderation.

## 2. Verified current architecture

- `internal/telegram/dispatcher_dispatch.go`: structural routing uses a lightweight `core.MessageEnvelope`, indexed handler buckets, decision handlers before command claim, event submission through shared TaskEngine.
- `internal/telegram/dispatcher_handlers.go`: priority 10 security, 20 moderation, 50 feature, 90 observability. Priority implies failure policy; decision and event submissions currently use `chat:<chatID>` ordering keys.
- `internal/plugin/manager.go`: a multi-registration plugin supplies full `core.MessageHookRegistration` entries; no automatic state gate is attached to those entries.
- `plugins/afk/afk.go`: outgoing AFK in decision lane, incoming AFK in event lane; both registered without `StateGate`, although the owner AFK state is atomically available. Welcome send currently runs within outgoing decision handler; reply-target inspection may perform Telegram GetMessage.
- `plugins/pmpermit/pmpermit.go` and `internal/services/pmpermit/service.go`: private incoming/outgoing decision hooks; DB access, warning, block/unblock, and Telegram RPC may run inside decision barrier. Approval cache and warning maps require lifecycle/capacity audit.
- `plugins/blacklist/blacklist.go`: chat active-state gate, synchronous delete on match.
- `plugins/filters/filters.go`: chat active-state gate and compiled rules; matching in decision lane, response delivery can be submitted as task continuation.
- `plugins/userlog/userlog.go`: event lane followed by a private 256-entry queue and lazy worker. Assess whether existing TaskEngine can own effect delivery without changing backpressure semantics.
- `internal/settings/defaults.go`: PMPermit max warning default 3 vs service constructor 4; AFK cooldown default 5s vs constructor 60s. Confirm binder lifecycle before changing defaults.

## 3. Risk inventory and invariants

1. **A1 — Inactive AFK admission**. AFK non-active outgoing messages should not submit AFK decision work; disabled incoming auto-reply should not submit AFK event work. Active manual outgoing must still atomically transition AFK off, and .afk commands must not auto-unAFK.
2. **A2 — Ordering and effects**. Shared chat ordering key currently permits event/decision contention; slow welcome/warning effects must not block unrelated command-start. Keep decision semantics and fail-closed security.
3. **A3 — PMPermit correctness**. Keep unapproved PMs suppressed if approval lookup fails; handle incomplete access hashes safely; bound per-user caches; fix any race in mutable cooldown; reconcile durable status with external Telegram RPC errors.
4. **A4 — Telegram chat semantics**. Preserve private/group/supergroup/topic distinctions and sender identity; avoid channel auto-replies and avoid topic-root false positives. Unknown peer class is not evidence of membership.
5. **A5 — UX**. Implement owner-bound a2 management for AFK/PMPermit and chat-contextual group moderation without a new callback or authorization stack.
6. **A6 — Observability**. Logging cannot reopen suppressed commands or make security decisions wait; keep audit metadata scoped and minimize sensitive content.
7. **A7 — Resource/restart acceptance**. Include high-cardinality PM bursts, FloodWait injection, lifecycle reload/shutdown, concurrent auto-unAFK, DB failure, task rejection, and settling of goroutines/heap.

## 4. Acceptance strategy

- A0 gate: record exact baseline, identify the reverted historical plan as non-current, inventory production call paths, and designate a minimal reversible first change. This gate is **source-reviewed**, not benchmark-validated.
- A1 gate: add tests for both AFK registration gates (initial inactive, active, auto-reply disabled, disabled again), then verify dispatcher skips task admission for a false state gate. Re-run existing AFK outgoing ordering/toggle/auto-origin tests.
- A2 gate: deterministic decision/event contention regression and FloodWait isolation before changing execution ordering.
- A3 gate: focused PMPermit tests, `-race` when available, and deterministic failure/reconciliation tests.
- A4–A7 gates: each requires behavior matrix and scope/lifecycle/resource checks.

## 5. Starting point for the next phase

Start with A1's state-only AFK admission gate on the *existing* multi-registration contract. Avoid speculative interface enlargement before this narrow gate is measured. Revalidate the branch HEAD before making any edits, gofmt every touched Go file including tests, then run targeted checks if the environment can build this repository. Do not check CI.

## 6. Incremental execution record (2026-10-08)

- **A0 committed** at `9e368f5b04faa3d32f51a3f2c22804c3554ee7b2`: source baseline after 50-file revert; no CI checks.
- **A1 patch committed** at `18b5d7c07ce6efa74820c1d90bb9b6deafd6767a`: AFK outgoing and incoming registration `StateGate` with regression test. Repository-wide Go tests not yet verified in this environment.
- **A2-A ordering isolation committed** at `f59a6b5261b8519f8f373cb4311135be09a378a3`: decision tasks use `msg-decision:chat:<id>`; event tasks use `msg-event:<plugin-owner>:chat:<id>`. Added ordering-domain and admission-controller tests.
- **A2-A dispatcher wiring acceptance** at `c27830a3cf58147a2a3927b02e9e063b9206f3ef`: regression checks actual dispatcher submitted keys, not only helper functions.
- Go changes/new test content were checked with local `gofmt` on corresponding snippets/new files. The entire repository was not available locally; **do not claim full Go tests or race gate are green**. CI has not been checked.
- Before claiming A2-A fully CLOSED, run in an authoritative checkout: `gofmt -l internal/telegram/dispatcher_dispatch.go internal/telegram/dispatcher_message_hook_ordering.go internal/telegram/dispatcher_message_hook_ordering_a2_test.go internal/telegram/dispatcher_message_hook_integration_a2_test.go`, `go test ./internal/telegram -run '^TestA2'`, `go test -race ./internal/telegram -run '^TestA2'`, `go test ./internal/telegram ./internal/admission ./plugins/afk`, `git diff --check`.
- **A2-B pending:** move AFK welcome presentation into generation-scoped, shared-TaskEngine effect only after decision-state persistence and after verifying A2-A gate. Preserve owner-global transition correctness, cancellation, timeout, and existing AFK tests. Do not add a fallback untracked goroutine.

## 7. A2-B — AFK welcome effect separation

Status: **IMPLEMENTED, acceptance gate pending** at commit `d1c21686f661d6bdb19686f52cf0f2f09ddf66fc`.

- Outgoing AFK still commits DB-first deactivation in the synchronous decision lane. After committing the transition it submits a small, immutable `afkWelcomeEffect` with chat ID, peer reference and duration to the existing shared TaskEngine. No direct Telegram send remains in the outgoing decision hook.
- AFK declares `CapTasks` and uses the capability-gated `PluginContext.TaskClient()` scoped client, with generation-aware task ID and `plugin:afk` quota ownership. Admission is bounded by 250 ms; effect execution by 15 s. A failed admission does not restore AFK or spawn a fallback goroutine.
- The effect retrieves its Telegram provider at execution time and uses the captured plugin scope for any optional delayed deletion; disabling the plugin must fence stale work.
- Adjusted six existing AFK tests to inject a test-only TaskClient and replaced the old blocking-welcome ordering test with a DB-first / queued-effect assertion. Added four A2-specific tests: blocked welcome isolation, rejected admission, scope cancellation, and module capabilities.
- The two new Go files and touched production function fragments were formatted/parsed locally with Go 1.23 standard tools; exact staged blob SHAs were checked against formatted local new files. A complete checkout was unavailable locally, therefore **full-file gofmt, package compilation, targeted tests, race tests, and resource acceptance are not yet verified**. CI was not checked.

**Required local acceptance before closing A2-B:**

```bash
git fetch origin test-next
git switch test-next
git pull --ff-only
gofmt -w plugins/afk/afk.go plugins/afk/welcome_effect.go plugins/afk/module.go plugins/afk/afk_test.go plugins/afk/afk_effect_a2_test.go
gofmt -l plugins/afk/afk.go plugins/afk/welcome_effect.go plugins/afk/module.go plugins/afk/afk_test.go plugins/afk/afk_effect_a2_test.go
go test ./plugins/afk -run 'TestA2AFK|TestAFKPlugin|TestAFKWelcome'
go test -race ./plugins/afk -run 'TestA2AFK|TestAFKPlugin|TestAFKWelcome'
go test ./internal/telegram ./internal/plugin ./plugins/afk
git diff --check
```

Additional acceptance still needed for managed runtime: inject a blocked Telegram/FloodWait welcome, confirm unrelated command begins after durable AFK-off and before welcome completes, and confirm actual TaskEngine cancellation on plugin disable/re-enable. Never claim final A2 closure based only on mock TaskClient tests.

### A2-B managed integration regression (2026-10-08)

Added commit `3e9068f1946756ac1c2151349b3447d3ee3c79f7` (`plugins/afk/afk_managed_effect_a2b_test.go`).

- Uses **real shared TaskEngine**, **real Plugin Manager**, canonical dispatcher, capability-gated manifest, and a deliberately blocked Telegram welcome mock.
- Verifies that the owner's next command can begin and read the durably inactive AFK state **while the welcome Telegram call is still blocked**.
- Disables the AFK plugin while welcome is in flight; verifies cancellation, enables the plugin again, asserts a new scope generation and no rollback of AFK state.
- The exact GitHub blob SHA `6e0935b5a8588bac06117a170c07eec2365a04a3` matches a local file that was processed with `gofmt -w` and has empty `gofmt -l` output.
- **Acceptance remains pending:** the full module graph is not available in the local container and GitHub network access from the container is unavailable. Syntax formatting is verified, but compilation, runtime result, race result, and CI result are not claimed. CI was not checked.

Run `go test ./plugins/afk -run '^TestA2BManagedWelcomeDoesNotDelayCommandsAndCancelsOnReload$' -count=1` and `go test -race ./plugins/afk -run '^TestA2B' -count=1` in an authoritative checkout, along with the previously recorded A2-A/A2-B gates. If a regression is found, **fix it and its tests before proceeding to A3**. Also verify that no stale generation can send after disable/re-enable.

### A2-B local acceptance failure and corrective patch (2026-10-08)

User ran:
- `go test ./plugins/afk -run '^TestA2B' -count=1`
- `go test -race ./plugins/afk -run '^TestA2B' -count=1`
- `go test ./internal/telegram ./internal/plugin ./plugins/afk`

Observed (before fix): managed welcome effect did not start; Telegram E2E plugin registration failed with `afk: initialize scoped TaskEngine client: task client not configured`.

Source-root cause: `internal/taskengine/accounting.go:payloadSize` rejects arbitrary struct inputs. A2-B's `WorkSpec.Input = afkWelcomeEffect` was `ErrUnsupportedPayload` at real TaskEngine admission, silently logged by AFK and omitted from fake-client validation. Also the existing `configureDispatcherTasks` test helper did not give the shared engine to `plugin.Manager` after AFK adopted `PluginContext.TaskClient()`.

Corrective source/test commit: `2d1e9cad4146765b0edec3fc8eb6bfc083fae2be`.
- Encodes bounded immutable TaskEngine input as a string while retaining the small immutable effect snapshot for the handler closure.
- Adjusts fake TaskClient regression to enforce compatible input.
- Wires dispatcher test fixture's shared engine into Plugin Manager.
- Removes the invalid synchronous-welcome assertion in Telegram end-to-end test; retains eventual-delivery expectation.
- No new runtime, TaskEngine, queue, callback protocol, or retry layer. CI not inspected.
- Local `gofmt` was run on both full AFK files and replacement Telegram fixture snippet; pre-commit Git blob hashes of both complete AFK files match the formatted copies.

**Status remains A2-B acceptance PENDING** until full Go package/race tests pass on the authoritative checkout. Re-run the three exact user commands above, and report any remaining failures. Do not advance to A3 on an unverified test gate.
