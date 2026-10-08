# Goultroid Message Automation — A0 Baseline and Incremental Recovery

Status: **A5 CLOSED; A6-A/B ACCEPTANCE CLOSED (2026-10-08); A6-C UserLog lifecycle/privacy IMPLEMENTED (acceptance pending); A6-D and A7 OPEN**. Historical A0–A4 records remain below.
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

## 8. A3 — PMPermit State Integrity (2026-10-08)

Historical checkpoint: **A3-A and A3-B implemented**; superseded by the successful A3-C acceptance recorded in §10.

**GitHub HEAD refreshed before each code phase. CI was not inspected.**

### A3-A — Bounded state / cooldown race

Commit `eca612d1ddbb811f25a20ea3ba6a0d7e445eabc6`.

- Replaced unbounded `approvedCache sync.Map` with a bounded positive-only cache (2,048 entries; eviction rechecks authoritative SQLite rather than granting approval).
- Warning message ID cache is bounded by **1,024 users** and retains the existing 20 IDs per user cap. Evicted users fall back to persisted warning IDs.
- Cooldown state is bounded by **2,048 senders**, with expiry reclamation; saturated active entries suppress new warning sends rather than creating an unbounded Telegram RPC storm.
- Replaced unsynchronized read of `warnCooldown` with the locked `WarnCooldown()` path. Cooldown admission is atomic under its own mutex.
- Added tests for high-cardinality approval/warning caches, saturation fail-closed behavior, and concurrent live cooldown settings.

### A3-B — Durable state and RPC recovery

Commit `4434c1fdf4f117fad0c94f65791b6da5086118e2`.

- Added constant-size 128-stripe per-user status locks so approval reads, status transitions and cache publication do not interleave for the same user. No goroutine/worker/registry was added.
- PM incoming now holds that user's status lock while checking approval, blocked status, cooldown and warning decision. DB read failures on the second blocked-status check fail closed without incrementing warnings.
- Disapprove retains the old positive cache if the durable pending-state write fails; cache invalidation/cleanup follows the successful write.
- Explicit unblock / `ApproveWithPeer` / outgoing auto-approval perform Telegram unblocking **before** publishing the corresponding durable state and positive cache. A Telegram failure returns an error without granting internal approval or downgrading a blocked state.
- Block commits internal blocked status first and invalidates the approval cache; Telegram RPC failure is surfaced as an error and an unsuccessful event, while PM interception stays fail closed.
- New tests simulate DB failure, Telegram block/unblock failures, blocked-to-approved failure, outgoing auto-approve failure, a secondary incoming state read failure, and concurrent approval/block publication.
- Updated existing outgoing auto-approval fixture to carry an actual `AccessHash` (valid peer).
- All four newly authored A3 Go helper/test files were processed through local `gofmt` and compared with the exact staged Git blobs. Legacy source edits preserved standard Go formatting in the modified regions, but repository-wide/full-file gofmt and package test runs are not verified in this environment.

### Mandatory local acceptance gate

```bash
git pull --ff-only
gofmt -w internal/services/pmpermit/service.go internal/services/pmpermit/approval.go internal/services/pmpermit/state_limits.go internal/services/pmpermit/state_limits_a3_test.go internal/services/pmpermit/status_locks.go internal/services/pmpermit/status_integrity_a3_test.go internal/services/pmpermit/service_test.go
gofmt -l internal/services/pmpermit/service.go internal/services/pmpermit/approval.go internal/services/pmpermit/state_limits.go internal/services/pmpermit/state_limits_a3_test.go internal/services/pmpermit/status_locks.go internal/services/pmpermit/status_integrity_a3_test.go internal/services/pmpermit/service_test.go
go test ./internal/services/pmpermit -run '^TestA3' -count=1
go test -race ./internal/services/pmpermit -run '^TestA3' -count=1
go test ./internal/services/pmpermit ./plugins/pmpermit ./internal/telegram ./internal/plugin
go test -race ./internal/services/pmpermit ./plugins/pmpermit
git diff --check
```

Do not check/poll CI. If any test fails, repair source and affected tests, gofmt all changed Go files, and re-run focused checks before any further commit.

### A3-C follow-up blockers at the earlier checkpoint (see §9 and §10 for resolution)

- Legacy `Approve(userID)` lacks an already-resolved Telegram peer with a valid access hash; it currently has different external RPC consistency semantics from `ApproveWithPeer`. Inventory/migrate concrete callers before narrowing/removing it.
- Warning ID storage helpers still use `context.Background()` for some repository calls. Migrate their execution to the incoming command/update context or to bounded, lifecycle-aware contexts and test DB failures.
- Add focused concurrency/failure checks for real SQLite and high-cardinality PM ingress, plus cancellation/FloodWait behavior under per-user lock stripes; a blocked Telegram RPC must not stall unrelated security decisions.
- Validate that PMPermit applies **only to private user chats**, not groups, channels, forum topics, or Assistant PM relay.
- Default setting consistency (`pmpermit.max_warns` and service constructor) remains to be reconciled without changing behavior unexpectedly.

## 9. A3-C execution — warning IDs, approval peer and FloodWait isolation

Historical checkpoint: **A3-C implemented, acceptance initially pending** on 2026-10-08. Acceptance subsequently passed (§10).

The user provided passing local results for the previous A3-A/B gate before this work:
- `go test ./internal/services/pmpermit -run '^TestA3'`
- `go test -race ./internal/services/pmpermit -run '^TestA3'`
- `go test ./internal/services/pmpermit ./plugins/pmpermit ./internal/telegram ./internal/plugin`
- `go test -race ./internal/services/pmpermit ./plugins/pmpermit`

### A3-C1 — Warning-ID failure containment

Commit `8054b057d5820de3d1665683c3947fc45afecc4d`.

- The warning-ID helpers now accept the existing ingress/command `context.Context` instead of using `context.Background()` for repository I/O. Persist/lookup/clear failures are returned, not silently interpreted as empty history.
- A failed DB append retains the bounded memory marker. A failed durable clear leaves the cached marker intact for retry.
- Outgoing PMPermit must not auto-approve on an unknown warning-origin marker when SQLite lookup fails; the plugin returns the canonical intercept-handled signal.
- SQLite corrupt warning-ID JSON now returns an error. Appending to corrupt/unreadable history is refused rather than overwriting it.
- New A3-C tests cover persistence failure, cached fallback, canceled contexts, eviction reload, corrupt SQLite JSON and outgoing fail-closed behavior.

### A3-C2 — Legacy approval semantics

Commit `0fb8f74db9f189b505549359a1316ffd995c80d2`.

- `Approve(userID)` is a storage-only operation, available only when no Telegram service or provider is configured. Managed Telegram use without a resolved access-hash-bearing peer returns `ErrResolvedApprovalPeer` and does **not** publish approval.
- Production PMPermit commands already use `ApproveWithPeer`. Tests requiring Telegram side effects were migrated to `ApproveWithPeer` and valid peer credentials.
- Additional regression cases cover preexisting blocks, unavailable dynamic Telegram providers, and storage-only operation.

### A3-C3 — Independent per-user lock and bounded high-cardinality admission

Commit `f04ee0e28e3979644d0c8a8fb78e870855db1f27`.

- Replaced 128 always-hashed status lock stripes with lazily registered per-user locks, reference-counted and evicted when idle. Up to 4,096 active distinct users are handled without unrelated-user stripe contention.
- At this bound, a fixed 128-stripe overflow fallback is used for unmatched users. While overflow is active, new unmatched users remain routed to overflow; this fences against a single user concurrently entering both keyed and overflow lanes. The limit stays bounded; no goroutine, engine or executor was introduced.
- New tests cover users that collided on the old 128-stripe hash, same-user serialization, 4,096-user capacity/idle settling, and a blocked mock Telegram warning not delaying another unrelated private sender.
- A standalone isolated `go test -race` harness for this lock subsystem passed all three lock tests. This **does not** validate full-repository package or Telegram integration tests.

### Mandatory next acceptance gate

Refresh `test-next` HEAD before any fix. Run `gofmt` for all changed Go files, including test files, **before committing**. Do not check/poll CI unless explicitly requested.

```bash
git pull --ff-only
gofmt -w internal/services/pmpermit/*.go plugins/pmpermit/*.go
gofmt -l internal/services/pmpermit/*.go plugins/pmpermit/*.go
go test ./internal/services/pmpermit -run '^TestA3C' -count=1
go test -race ./internal/services/pmpermit -run '^TestA3C' -count=1
go test ./plugins/pmpermit -run '^TestA3C' -count=1
go test -race ./plugins/pmpermit -run '^TestA3C' -count=1
go test ./internal/services/pmpermit ./plugins/pmpermit ./internal/telegram ./internal/plugin
go test -race ./internal/services/pmpermit ./plugins/pmpermit
git diff --check
```

At the earlier implementation checkpoint, A3 closure required these tests. The user has now reported passing results for the entire gate (§10). CI has not been checked. New A3-C test files and the replacement status-lock source were individually gofmt-verified via matching Git blob SHA; edited existing full files need a full local gofmt run to verify.

Remaining review items: `pmpermit.max_warns` settings default is **3** while the service constructor default is **4**; decide and document the effective default explicitly before a behavioral change. Test actual SQLite high-cardinality and outbox/Telegram effects with production-like deadlines, and continue the subsequent A4 chat/topic context plan after the acceptance gate.

## 10. A3 Acceptance CLOSED — verified user-run gate (2026-10-08)

The user executed the **entire A3-C acceptance matrix** after pulling through `6b8c5400c74980f621725bb7ff63806ae1ad3cb3` and reported all commands successful:

- `gofmt -w internal/services/pmpermit/*.go plugins/pmpermit/*.go`, followed by `gofmt -l` on both paths: no output from `gofmt -l`.
- `go test ./internal/services/pmpermit -run '^TestA3C' -count=1` — **PASS**.
- `go test -race ./internal/services/pmpermit -run '^TestA3C' -count=1` — **PASS**.
- `go test ./plugins/pmpermit -run '^TestA3C' -count=1` — **PASS**.
- `go test -race ./plugins/pmpermit -run '^TestA3C' -count=1` — **PASS**.
- `go test ./internal/services/pmpermit ./plugins/pmpermit ./internal/telegram ./internal/plugin` — **PASS across all four packages**.
- `go test -race ./internal/services/pmpermit ./plugins/pmpermit` — **PASS across both packages**.
- `git diff --check` — no errors reported.

**Closure scope:** A3-A bounded caches and cooldown synchronization; A3-B DB/cache status integrity and failure containment; A3-C warning-ID context propagation and fail-closed unknown lookup, resolved-peer approval contract, bounded active per-user status locks, and FloodWait isolation regression. The targeted and package-level tests above have been reported passing by the user. **A3 acceptance is CLOSED; there is no remaining A3 code gate blocking A4.**

**Outside this closure / separately tracked:**

- Effective `pmpermit.max_warns` defaults still differ: Settings schema **3**, service constructor **4**. Do not change without explicitly determining production binder precedence and adding coverage; this is a compatibility/configuration decision, not a reason to reopen the passed A3 gate.
- Production-scale SQLite high-cardinality workload, Telegram transport integration, and resource/heap/goroutine settling remain appropriate for **A7 resource/restart acceptance**; focused A3-C concurrency and FloodWait regression tests are not a production performance benchmark.
- A4 must verify private/group/supergroup/channel/topic structural routing, sender identity, and fail-closed incomplete-peer behavior before changing code.

**Execution discipline:** Refresh HEAD before each new phase, `gofmt` all changed Go files before committing, update focused tests, and **do not inspect or poll CI unless the user explicitly asks**. CI was not checked as part of A3.

## 11. A4 — Telegram Chat/Topic Context Integrity (2026-10-08)

**Historical A4-A/B implementation checkpoint:** tests and race acceptance were pending at the time. Subsequent user-run acceptance is recorded in §12. A3 remains CLOSED.

### A4-A — Canonical chat/sender/reply consistency

Commit `d5b8d67dbbba3f842804dd14fdfae8e6e0132ce3`.

- Reconciled `Dispatcher.resolveDispatchChat` with `NormalizeMessageEnvelope`: a `tg.PeerChannel` is treated as **broadcast channel** unless its `tg.Channel.Megagroup` metadata positively establishes a supergroup. An unknown channel must never be silently assumed to be a group.
- Private incoming updates with an explicitly mismatched, anonymous or non-user `FromID` are rejected before hooks and command admission. **Only truly omitted `FromID`** may use the private dialog peer for sender identity; `invocationSenderID` obeys the same rule.
- Preserve `MessageReplyHeader.ReplyToPeerID` in canonical `MessageEnvelope.ReplyPeer` for cross-peer discussion replies. Topic ID / root-reply semantics are preserved.
- Nil typed chat peers no longer crash canonical envelope creation; unknown classification remains conservative.
- Added `internal/telegram/context_integrity_a4_test.go` with tests for channel/megagroup/unknown classification, sender mismatch and omitted FromID, dispatcher hook rejection, and topic/root/cross-chat reply metadata.

### A4-B — Plugin automation context fences

Commit `ca7d5a7aeb2650696a17121e3d28eeaec9984593`; follow-up outgoing-welcome regression commit `67ff5e052a986db11c3a3af69ba0088a84f6b107`.

- AFK incoming auto-replies require a private dialog or **verified group/supergroup**, with an actual user sender. Broadcast channel posts, unknown channel types, and anonymous channel-backed senders are ignored; group mentions and forum replies from real users still work.
- AFK reply-to-owner lookups only use a reply belonging to the same peer. A cross-chat reply does not cause an RPC lookup using the wrong channel/topic and will not provoke a personal AFK response.
- AFK manual outgoing broadcast posts still deactivate AFK state, but never schedule a welcome response into a broadcast channel.
- Userlog private/mention logging now skips broadcast/unknown channels and channel-backed anonymous senders rather than attributing them to users. Positive private and verified group paths are retained.
- PMPermit inbound private interceptor fails closed when canonical sender peer does not match the private dialog peer. The dispatcher applies the same restriction before any command execution.
- Updated existing AFK tests that previously presented `PeerChannel` as a group without `Megagroup: true`; changed anonymous-channel test expectation to suppress personal AFK replies.
- New regression files: `plugins/afk/chat_context_a4_test.go`, `plugins/userlog/chat_context_a4_test.go`, and `plugins/pmpermit/chat_context_a4_test.go`.

### A4 acceptance matrix and rules

Preserve the following:
- Private dialog: omitted `FromID` is allowed, explicit mismatch is suppressed; PMPermit applies only to private users and remains fail-closed.
- Basic group and verified megagroup: member mentions and genuine replies still route correctly, including forum topic ID, topic-root suppression and cross-chat reply safety.
- Broadcast channel and unknown `PeerChannel` classification: no AFK welcome/auto-reply, no anonymous-user userlog entry, no PMPermit private decision.
- Media, edited messages, per-chat filters/blacklist, channel posts and group moderation must not acquire a **new** private-message permission or bypass. Existing filter/blacklist moderation contracts have not been rewritten in A4-A/B.
- No extra worker, registry, rate-limiter, TaskEngine, callback protocol or background goroutine has been introduced.

**Mandatory authoritative checkout checks before A4 CLOSED:**

```bash
git pull --ff-only
gofmt -w internal/telegram/message_envelope.go internal/telegram/dispatcher_dispatch.go internal/telegram/context_integrity_a4_test.go plugins/afk/afk.go plugins/afk/afk_test.go plugins/afk/chat_context_a4_test.go plugins/userlog/userlog.go plugins/userlog/chat_context_a4_test.go plugins/pmpermit/pmpermit.go plugins/pmpermit/chat_context_a4_test.go
gofmt -l internal/telegram/message_envelope.go internal/telegram/dispatcher_dispatch.go internal/telegram/context_integrity_a4_test.go plugins/afk/afk.go plugins/afk/afk_test.go plugins/afk/chat_context_a4_test.go plugins/userlog/userlog.go plugins/userlog/chat_context_a4_test.go plugins/pmpermit/pmpermit.go plugins/pmpermit/chat_context_a4_test.go
go test ./internal/telegram -run 'TestA4|TestNormalizeMessageEnvelope|TestDispatcher_.*Routing' -count=1
go test ./plugins/afk -run 'TestA4|TestAFKPlugin' -count=1
go test ./plugins/userlog -run 'TestA4|TestUserLog' -count=1
go test ./plugins/pmpermit -run 'TestA4|TestPMPermit' -count=1
go test ./internal/telegram ./internal/core ./internal/plugin ./plugins/afk ./plugins/userlog ./plugins/pmpermit ./plugins/filters ./plugins/blacklist
go test -race ./internal/telegram ./plugins/afk ./plugins/userlog ./plugins/pmpermit
git diff --check
```

**Verification caveat:** Full Goultroid checkout / dependencies were unavailable in the execution container; source was inspected and GitHub commits were verified, but authoritative focused tests, package-level tests, race checks, and full-file `gofmt` are **not claimed**. The tests above must be executed and any failures repaired with correctly formatted Go source/tests before marking the phase closed. Do **not** poll or inspect CI unless explicitly asked.

**Next after acceptance:** A5 user-facing management UX and contextual moderation, keeping a2 as the sole interaction protocol.

### A4 user-run regression and correction (2026-10-08)

The user ran the A4 focused, package and race test commands against `cf8450ca20858788c3763e694650135c3e8597ca` and reported **three blockers**:

1. `internal/telegram/TestA4PrivateSenderRejectsMismatchedOrAnonymousFromID`: the assertion expected sender ID zero, but canonical normalization intentionally preserves the explicitly supplied `FromID=9000`; dispatcher `privateMessageSenderConsistent` rejects the mismatch separately.
2. `plugins/userlog/chat_context_a4_test.go`: unused `internal/core` import caused compilation failure.
3. `plugins/afk/TestAFKPlugin_CompoundCooldown`: Group 100/200 fixtures used `PeerChannel` without `Megagroup=true`, so A4 correctly treated them as broadcast channels and suppressed AFK replies.

Corrective **test-only** commit: `36c9b4c40957377c6fb45e9108bf4a7e4710a49f`. It fixes the expected explicit sender ID, removes the unused import, and marks the two test groups as megagroups. Runtime guards, status transitions, TaskEngine, and RPC behavior are unchanged.

**Historical regression checkpoint:** A4 acceptance was still pending directly after the test-only correction. The user has since reported passing package and race tests, recorded in §12. CI was not checked.

Next check in the user's full checkout:
```bash
git pull --ff-only
gofmt -w internal/telegram/context_integrity_a4_test.go plugins/userlog/chat_context_a4_test.go plugins/afk/afk_test.go
gofmt -l internal/telegram/context_integrity_a4_test.go plugins/userlog/chat_context_a4_test.go plugins/afk/afk_test.go
go test ./internal/telegram -run '^TestA4' -count=1
go test ./plugins/afk -run 'TestA4|TestAFKPlugin_CompoundCooldown' -count=1
go test ./plugins/userlog -run '^TestA4' -count=1
go test ./plugins/pmpermit -run '^TestA4' -count=1
go test ./internal/telegram ./internal/core ./internal/plugin ./plugins/afk ./plugins/userlog ./plugins/pmpermit ./plugins/filters ./plugins/blacklist
go test -race ./internal/telegram ./plugins/afk ./plugins/userlog ./plugins/pmpermit
git diff --check
```

If any gate fails, fix production or test according to the actual contract, format changed Go files before committing, and do not advance to A5 until the A4 gate is met.

## 12. A4 Acceptance CLOSED — User-run full package/race gate (2026-10-08)

The user fast-forwarded `test-next` from `cf8450ca...` through the A4 test-only fix `36c9b4c40957377c6fb45e9108bf4a7e4710a49f` and plan update `345e3de942071cddee734371e65845522d691ccf`. All reported checks passed:

- `gofmt -w internal/telegram/context_integrity_a4_test.go plugins/userlog/chat_context_a4_test.go plugins/afk/afk_test.go` was run. The command produced no error. No separate `gofmt -l` output was provided on this final gate; do not claim a clean working tree solely from `git diff --check`.
- `go test ./internal/telegram ./internal/core ./internal/plugin ./plugins/afk ./plugins/userlog ./plugins/pmpermit ./plugins/filters ./plugins/blacklist`: **PASS in all eight packages**. The full package runs include the A4 tests.
- `go test -race ./internal/telegram ./plugins/afk ./plugins/userlog ./plugins/pmpermit`: **PASS in all four packages**, including A4 routing and plugin regressions.
- `git diff --check`: no errors were reported.

**A4 acceptance is CLOSED within the tested scope.** The previous failures (wrong expectation for explicit sender ID, unused userlog test import, and mislabeled AFK megagroup fixtures) have been corrected and revalidated by the user's complete package tests. The A4 implementation keeps channel vs megagroup classification conservative, denies mismatched private sender updates before dispatch, retains cross-peer reply facts, and fences AFK/userlog/PMPermit automation by chat and sender identity.

**Not included in this closure:** repository-wide `go test ./...` or `go test -race ./...`, live Telegram multi-chat testing, or CI inspection. Do not present those as verified. The A7 resource/restart acceptance still owns high-cardinality production-like workloads and settling metrics.

**Next phase: A5 — owner-bound a2 management UX and contextual group moderation.** Before modifying code, refresh HEAD and inspect canonical a2 interaction/authority/presentation contracts and existing management surfaces, then implement only concrete missing routes without inventing a second callback stack. Always `gofmt` changed Go source/tests before commit, update focused regressions, keep idle and retained state bounded, and do not inspect/poll CI unless explicitly requested.

## 13. A5 — Owner a2 Management and Contextual Moderation (2026-10-08)

**Status: A5-A/B IMPLEMENTED, optional-native fallback hardening IMPLEMENTED; A5-C contextual group moderation and full acceptance PENDING. Do not mark A5 CLOSED.**

Refreshed initial `test-next` HEAD `2e2a31f6b7855ddb15c52ac9ef7ef895f4ad6e8b` before A5. Existing `internal/app/app.go` wires native a2 adapter to Plugin Manager, which already manages native feature bindings and cleanup per plugin generation. The new surfaces reuse that mechanism; no new callback protocol, session map, timer, worker, registry, TaskEngine or RPC executor was introduced.

### A5-A — PMPermit owner-bound native a2 dashboard

Commit: `ec250b5cdf8e02e419d542a7c0479c9ea9795bc8`.

- Added `plugins/pmpermit/native_interaction.go` implementing `FeatureSpec`, `NativeFeatureID`, and `BindNative` with owner-only userbot policy and `DurabilityVersion=1`.
- `.pmpermit` or `.pmpermit menu` opens a shared a2 dashboard when adapter is bound. Legacy text dashboard remains when a2 is unavailable; `.pmpermit status` remains explicit text output.
- Owner-only, session-bound actions: enable/disable, refresh status/counts, and terminate. Callback tokens and revisions belong solely to canonical a2; bindings are unregistered on plugin lifecycle cleanup.
- Factored command and callback writes through `setEnabledForActor`, keeping Settings persistence + live binder as the single managed mutation path and retaining direct service fallback only for standalone/tests.
- Added `plugins/pmpermit/native_interaction_a5_test.go`: owner a2 callback token provenance, mutation, stale-token rejection, scope-owned session, and non-owner mutation rejection.

### A5-B — AFK owner-bound native a2 menu

Commit: `e6ce8c10dca0acc4d6c3d8ceb72108e6de51df4c`.

- Added `plugins/afk/native_interaction.go` with owner-only a2 session/actions and generation-scoped cleanup.
- `.afk menu` / `.afk panel` opens controls for enable/disable, refresh, and close. Existing `.afk` with **no arguments still toggles**; `.afk status`, `.afk on|off`, and arbitrary reason commands keep their prior semantics.
- On activation or deactivation, callbacks use existing AFK persistence and atomic transition functions; the displayed status is read from existing atomic state. User-supplied reason is HTML-escaped and clipped for display.
- The unavailable-a2 case gives a textual command hint instead of creating a second UI stack.
- Added `plugins/afk/native_interaction_a5_test.go` covering a2 tokens, owner callback mutation, stale-token rejection, scope cleanup, and unavailable-runtime fallback.

For both drivers, the **new full Go source/test files were processed through local `gofmt`; exact SHA-1 Git blob hashes of the staged versions match the locally formatted copies**. Changed fragments in the existing plugin command files retain standard Go formatting. The complete repository and dependencies are unavailable in this execution container: no claim is made that complete package/race acceptance has passed. CI was not checked.

### Required A5-A/B authoritative acceptance

```bash
git pull --ff-only
gofmt -w plugins/pmpermit/pmpermit.go plugins/pmpermit/native_interaction.go plugins/pmpermit/native_interaction_a5_test.go plugins/afk/afk.go plugins/afk/native_interaction.go plugins/afk/native_interaction_a5_test.go
gofmt -l plugins/pmpermit/pmpermit.go plugins/pmpermit/native_interaction.go plugins/pmpermit/native_interaction_a5_test.go plugins/afk/afk.go plugins/afk/native_interaction.go plugins/afk/native_interaction_a5_test.go
go test ./plugins/pmpermit -run '^TestA5' -count=1
go test -race ./plugins/pmpermit -run '^TestA5' -count=1
go test ./plugins/afk -run '^TestA5' -count=1
go test -race ./plugins/afk -run '^TestA5' -count=1
go test ./internal/plugin ./internal/interaction/... ./internal/app ./plugins/afk ./plugins/pmpermit ./plugins/settings
go test -race ./internal/plugin ./plugins/afk ./plugins/pmpermit
git diff --check
```

If a package fails, repair the precise production/test contract before advancing, and run `gofmt` on every changed Go file before committing. Do not poll or inspect CI without an explicit request.

### A5-A/B native-optional startup compatibility

Commit: `afd1abeafc7eb8d6f9837af4a34860c508c70e6b`.

Review after implementing the two `FeatureDriver` interfaces found that `internal/plugin/features.go` intentionally fails registration when a native driver is declared but no a2 adapter is configured. While production `internal/app/app.go` wires the adapter, standalone and test registration of AFK/PMPermit previously had no such requirement.

- Added a narrow opt-in `nativeinteraction.OptionalFeatureDriver` extension. Only features explicitly implementing `NativeOptional() bool` can register without the adapter; strict native drivers still fail without their mandatory transport.
- AFK and PMPermit opt in because their text commands already have valid native-free fallback paths. No unbound action handlers are registered, no extra runtime is created, and feature lifecycle still owns all a2 registrations when an adapter exists.
- Added `internal/plugin/TestA5NativeDriverStandaloneFallbackDoesNotRelaxStrictDrivers` to require the optional behavior and preserve the strict default.

The native-fallback fix also requires targeted `go test ./internal/plugin` and `go test -race ./internal/plugin` in the full checkout. No CI inspection.

A5-B test lifecycle fix: `b3b6d5fa66d9a290a3387db59fefe56e50ff1374` ensures the AFK native cleanup is not invoked twice by the test when it checks session detachment. This test-only change was formatted locally before push.

### A5-C — Contextual group moderation (OPEN)

**Do not present A5-A/B as full A5 completion.** The group moderation UI needs its own narrow acceptance:

- Keep Filters and Blacklist commands in **group-only** context and preserve contextual administrator authorization; do not replace the existing `GroupAuthorizationRequirement{Level: Administrator}` by owner-only global policy or by a callback that trusts chat IDs from opaque client state.
- Before binding any a2 moderation mutation, inventory `plugins/filters`, `plugins/blacklist`, and the existing group authority resolver. Callback authorization must use the session-bound target chat/topic, a fresh contextual authority check immediately before mutation, and immutable rule identity / revision fencing.
- Disallow private/channel/unknown-class targets and stale generation/token mutations. Separate preview/list from add/remove mutation; keep per-chat caches bounded and no second task engine.
- Add deterministic regressions for two groups with the same keyword, demotion while the menu is open, forum-topic replies, stale rules, disabled/reloaded plugins, and rollback on DB failure.
- Do not change default PMPermit max warnings (settings **3**, constructor **4**) without a separate compatibility decision.

A5 will be CLOSED only after the owner management and contextual moderation acceptance gates pass. A6 observability and A7 resource/restart acceptance remain subsequent phases.

### A5 owner dashboard acceptance regression: import and fixture (2026-10-08)

The user ran the A5-A/B acceptance commands at `2905f46cb659f02fcb00a7d508ed526ba8aad7c6` and reported two blockers:

- `plugins/afk/native_interaction.go`: an unused `context` import prevented AFK and `internal/app` compilation.
- `plugins/pmpermit/TestPMPermitPlugin`: the direct handler test fixture passed no `Sender`; `core.Context.SenderID()` returned zero, correctly failing the new owner-authorization guard. Runtime authorization must not be weakened to make this test pass.

Correction committed as `3b1bb3d5bdc210b3c365309dd2be6ca55af52530`:
- Remove the unused AFK import.
- Supply actual owner `Sender` and `Message.SenderID` on the PMPermit text toggle fixture.
- Both edited regions were `gofmt`-verified; the full AFK source Git blob matches the local formatted file.

**Status remains A5-A/B implementation complete, authoritative package/race acceptance PENDING, A5-C OPEN.** The corrected commit has not yet been verified with the full Go tests. Do not inspect CI.

Re-run:
```bash
git pull --ff-only
gofmt -w plugins/afk/native_interaction.go plugins/pmpermit/pmpermit_test.go
gofmt -l plugins/afk/native_interaction.go plugins/pmpermit/pmpermit_test.go
go test ./plugins/afk ./plugins/pmpermit ./internal/plugin
go test -race ./plugins/afk ./plugins/pmpermit ./internal/plugin
go test ./internal/interaction/... ./internal/app ./plugins/settings
git diff --check
```

## 14. A5-A/B acceptance and A5-C0 contextual action fence (2026-10-08)

### A5-A/B acceptance — PASSED

The user fast-forwarded to `5ce4d0c50bbb447a9afd859500659fadf8f1a065`, formatted the AFK and PMPermit fix files, and reported all requested commands passing:

- `go test ./plugins/afk ./plugins/pmpermit ./internal/plugin` — **PASS** (three packages).
- `go test -race ./plugins/afk ./plugins/pmpermit ./internal/plugin` — **PASS** (three packages).
- `go test ./internal/interaction/... ./internal/app ./plugins/settings` — **PASS** across all listed interaction packages, app, and Settings.
- `git diff --check` — no errors were reported.

**A5-A/B owner-only a2 management acceptance is CLOSED.** No CI inspection was requested or performed. This acceptance does **not** close A5-C.

### A5-C0 — Strict fresh group mutation authorization prerequisite

Commit `067ed383f826d250663f4a648548c78a709f583a`.

Audit of `plugins/filters`, `plugins/blacklist`, `internal/core/group_role.go`, `internal/assistant/command/router.go`, and `internal/interaction/native/adapter.go` established:

- Filters/Blacklist Assistant commands already declare contextual Telegram administrator authorization; Blacklist adds `DeleteMessages` for mutations. The Assistant router revalidates the group role **fresh** inside TaskEngine execution.
- Native a2 callbacks currently revalidate global owner/sudo interaction policy and bound actor/message, **but do not intrinsically fetch contextual Telegram group authority**. A native group admin button is unsafe if implemented as a generic Sudo/Public action without its own fresh group-role guard.
- The authoritative Assistant role resolver is instantiated inside `internal/assistant/client` at Assistant start, not exposed through the native Plugin Manager driver runtime. Do not invent a second group role RPC resolver or trust cached role state in session bytes.

`internal/interaction/native/group_authorization_a5.go` provides `AuthorizeFreshGroupAction`, a pure fail-closed helper for use immediately before a future group rule write inside the existing TaskEngine callback handler. It:
- Requires an explicitly administrator-or-creator group policy (optionally including Telegram rights like `DeleteMessages`).
- Checks actor, chat, message, and target binding against immutable session scope; rejects private/broadcast/unknown chats, inconsistent group peer identity, missing supergroup access hash, and invalid topic ID.
- Derives `GroupRoleRequest.UserID` and `Peer` from the **bound a2 session/callback target**, never from arbitrary client-supplied user IDs.
- Calls `GroupRoleResolver.ResolveGroupRoleFresh` and rejects missing/failed lookups, mismatched/unverified principals, revoked administrator role, or rights demotion. Global owner/sudo does not satisfy missing Telegram group authority.

`internal/interaction/native/group_authorization_a5_test.go` adds four deterministic top-level tests for:
1. Fresh-role-only acceptance and subsequent demotion / removed delete rights.
2. Cross-chat/session-target, private/channel, incomplete peer, absent actor/message and invalid topic denial.
3. RPC outage, nil resolver, principal mismatch and unverified role.
4. A missing authorization policy failing closed.

Both Go files were passed through local `gofmt`; their exact Git blob SHA matches the committed version. The full Goultroid dependencies are unavailable in this execution environment; these new tests have **not** yet been run against the complete repository, and no CI was checked.

### Remaining A5-C work — OPEN, no live mutation callbacks enabled

The authorization helper is an infrastructure prerequisite only. **No new native Filters/Blacklist buttons or moderation mutations have been exposed by this commit.** Before doing so:

- Reuse the existing authoritative Assistant `GroupRoleResolver` via a capability-safe lifecycle-bound provider, or keep the mutation UI disabled when no resolver is available. Do not introduce a duplicate RPC executor or cached authority substitute.
- Bind group/type and topic facts when the a2 session is created; verify callback actor/chat/message and freshly resolved Telegram role immediately before the DB write.
- Add scoped read/list → preview → mutation actions with saved rule IDs/keyword and DB revision checks, plus reload/shutdown and cross-group regression tests. Never authorize a write by using only a chat ID read from JSON callback state.
- Preserve existing userbot text command and Assistant command paths (no backwards-incompatible mandatory native callback requirement).
- Keep memory/state bounded and unloaded session state inaccessible.

Next focused gate for A5-C0:

```bash
git pull --ff-only
gofmt -w internal/interaction/native/group_authorization_a5.go internal/interaction/native/group_authorization_a5_test.go
gofmt -l internal/interaction/native/group_authorization_a5.go internal/interaction/native/group_authorization_a5_test.go
go test ./internal/interaction/native -run '^TestA5C' -count=1
go test -race ./internal/interaction/native -run '^TestA5C' -count=1
go test ./internal/interaction/... ./internal/plugin ./plugins/filters ./plugins/blacklist ./internal/assistant/command
go test -race ./internal/interaction/native ./plugins/filters ./plugins/blacklist
git diff --check
```

**A5 remains PARTIAL:** A5-A/B accepted, A5-C0 implemented but test gate pending, A5-C UI/permission integration OPEN. Do not declare overall A5 CLOSED until integration and test gates pass.

## 15. A5-C0 user-run acceptance and A5-C1 native role provider (2026-10-08)

### A5-C0 acceptance — CLOSED

The user fast-forwarded `test-next` to `d6b8a3224c1f53936d5525ff05d6b222786c58ba` and reported these gates passing:

- `go test ./internal/interaction/native -run '^TestA5C' -count=1`: **PASS**
- `go test -race ./internal/interaction/native -run '^TestA5C' -count=1`: **PASS**
- `go test ./internal/interaction/... ./internal/plugin ./plugins/filters ./plugins/blacklist ./internal/assistant/command`: **PASS**
- `go test -race ./internal/interaction/native ./plugins/filters ./plugins/blacklist`: **PASS**
- `gofmt -w internal/interaction/native/group_authorization_a5*.go` and `git diff --check`: no errors reported.

**A5-C0's fail-closed contextual authorization fence is accepted.** No CI was inspected.

### A5-C1 — Lifecycle-bound authoritative group role provider (implemented; test gate pending)

Commit `0289cc2d08ba0071145f2eb328a2e3287ccf396d`.

The only current production authoritative `GroupRoleResolver` is constructed in `internal/assistant/client` from the managed bot Telegram API, with its existing bounded cache and shared RPC execution path. The userbot/native adapter cannot safely synthesize a Telegram group role from local owner/sudo identity.

- `AssistantClient` now exposes its **currently running** role resolver via `GroupRoleResolver()`; it is cleared when the Assistant run stops. The resolver is not exposed during startup/shutdown.
- `AssistantApp` forwards the provider rather than returning an unavailable type assertion through the Assistant wrapper.
- Native a2 `Adapter.SetGroupRoleProvider` and `Adapter.GroupRoleResolver` install/read a synchronized dynamic provider. There is **no second resolver, RPC executor, worker, cache, or callback stack**.
- The application composition root attaches a closure borrowing the live Assistant resolver. With BOT_TOKEN missing, Assistant unavailable/stopped, or role lookup failing, the provider returns nil or the mutation-time role guard fails closed.
- New test `internal/interaction/native/group_role_provider_a5_test.go` requires nil-default, live provider, shutdown/detach, and nil-adapter behavior. `internal/assistant/role_bridge_a5_test.go` requires a stopped Assistant to expose no role authority.

**Important scope:** this commit wires the prerequisite role provider only. It does **not** expose a new Filters/Blacklist mutation callback. Existing text commands retain the original behavior. Do not declare A5-C1 complete as a full moderation UI or A5 CLOSED.

### Required acceptance for A5-C1

```bash
git pull --ff-only
gofmt -w internal/interaction/native/adapter.go internal/interaction/native/group_role_provider_a5_test.go internal/assistant/client/client.go internal/assistant/app.go internal/assistant/role_bridge_a5_test.go internal/app/app.go
gofmt -l internal/interaction/native/adapter.go internal/interaction/native/group_role_provider_a5_test.go internal/assistant/client/client.go internal/assistant/app.go internal/assistant/role_bridge_a5_test.go internal/app/app.go
go test ./internal/interaction/native -run '^TestA5C' -count=1
go test -race ./internal/interaction/native -run '^TestA5C' -count=1
go test ./internal/assistant/... ./internal/interaction/... ./internal/app ./internal/plugin ./plugins/filters ./plugins/blacklist
go test -race ./internal/assistant/client ./internal/interaction/native ./internal/plugin
git diff --check
```

The complete repository was not available for Go package execution here; **full Go tests/race after this commit are not claimed**. No CI was checked.

### A5-C2 — Scoped Blacklist/Filters management actions (OPEN)

Implementation must use canonical a2 generation-scoped actions and the live role provider above. Enter only from a verified manager group; bind the actor/chat/message and retain topic identity, never trust a client-provided chat ID. A mutating callback must fetch the fresh Telegram role *inside the TaskEngine execution handler* and compare the affected rule identity against a current DB snapshot under the per-chat rule lock before deletion. Confirmations and refreshed screens must use a2 session revisions so old buttons cannot commit.

First safe slice: a paged, bounded Blacklist list/preview/confirm-remove UI. Adding rules remains on the existing text command until an input-bound workflow is tested. Follow with Filters read/list management separately. Test demotion, wrong group, DB changes between preview/confirmation, missing BOT_TOKEN, plugin unload/reload, invalid peer metadata and DB failures. Do not create a new rule registry or executor.

**Status:** A5-A/B CLOSED; A5-C0 CLOSED; A5-C1 wiring implemented with acceptance pending; A5-C2 OPEN. A5 overall **NOT CLOSED**.

## 16. A5-C1 accepted; A5-C2 Blacklist a2 scoped manager (2026-10-08)

### A5-C1 — authoritative acceptance CLOSED

The user fast-forwarded `test-next` from `d6b8a322...` to `dec5b71d...` and executed the prescribed formatter, package tests, and race checks. Results:

- `go test ./internal/interaction/native -run '^TestA5C' -count=1`: **PASS**.
- `go test -race ./internal/interaction/native -run '^TestA5C' -count=1`: **PASS**.
- `go test ./internal/assistant/... ./internal/interaction/... ./internal/app ./internal/plugin ./plugins/filters ./plugins/blacklist`: **PASS** across all listed packages.
- `go test -race ./internal/assistant/client ./internal/interaction/native ./internal/plugin`: **PASS**.
- `gofmt -w` on modified files and `git diff --check`: completed without errors.

The user subsequently ran `gofmt -w .` and force-pushed with lease. The new authoritative branch baseline when A5-C2 began was **`187be451aeae7a07a22fae168af7365fd45bd82e`**, not the previous docs-only `dec5b71d...`. This history rewrite also included unrelated formatted/changed files; A5-C2 was built strictly on the new HEAD so these edits were preserved.

### A5-C2-A — scoped Blacklist native a2 UX IMPLEMENTED, acceptance PENDING

Commits:
- `fb538c011a74ef1719083860dba75a45775f5ed9` — group-bound Blacklist manager, database snapshot confirmation, test fixtures.
- `566375c602aea1396557c68f59cdb35e0f0677fc` — integrated canonical a2 callback regression test (demotion and wrong-chat protection).

Scope:

- `.blacklists` on the **native userbot surface** opens a canonical a2 manager only when the existing native adapter has an active authoritative Assistant group role provider. Otherwise it retains its historical text result. Assistant commands keep their existing managed group routing and do not enter the userbot a2 presentation.
- The group menu has fixed action slot registrations (9), page size 5, a maximum of 9 per-screen choices, a 10-minute TTL, and generation-scoped cleanup through `BindNative`. It adds no new session registry, worker, cache, RPC executor or callback protocol.
- Creation checks that the chat is a verified group/supergroup and queries the existing authoritative Telegram role resolver freshly. Callbacks use the A5-C0 `AuthorizeFreshGroupAction` boundary with the **bound actor, original chat, message ID and peer**, not a user-supplied chat ID.
- Read/preview actions require a verified Telegram administrator role; the confirmed **remove** action additionally requires `DeleteMessages` rights and a fresh lookup in the actual execution handler.
- The user selects a persisted rule, sees a separate confirmation screen, and can cancel/refresh or page. Actual deletion uses the existing per-chat rule lock and compares a canonical SHA-256 digest of the current persisted rule list against the preview snapshot; mismatches fail closed with a refresh hint.
- The a2 runtime owns session revision and stale-token rejection; the persisted database remains the authoritative rule store, with no retained duplicate rule cache in the menu.
- Existing `.blacklist` and `.unblacklist` commands and Assistant moderation are unchanged.

Regression coverage:

1. Paginated menu is bounded and preserves chat/topic coordinates in a2 state.
2. Same keyword in two different chats cannot be deleted from the wrong repository scope.
3. Rules altered since preview cause a conflict rather than stale deletion.
4. Canceled SQLite context prevents mutation without deleting a rule.
5. Private, broadcast channel and unknown chat scopes are rejected; absent a2 runtime preserves text fallback.
6. Integrated a2 flow tests callback token handling, demotion before confirmation, wrong-chat callback coordinates, and authorized deletion.

**Limitations / open risks:** Digest checks are content-based, not a cross-process SQLite compare-and-swap version; a remove/re-add ABA cycle with the identical rule set may not be detected. The existing rule lock protects one-process concurrency only. Do not claim distributed or multi-instance atomicity. Filters menu/add editing remains separate A5-C2-B work. No new a2 group mutation test has yet run in the authoritative user's Go checkout; CI was not checked.

### Required A5-C2-A local gate

```bash
git pull --ff-only
gofmt -w plugins/blacklist/blacklist.go plugins/blacklist/native_interaction.go plugins/blacklist/native_interaction_a5_test.go plugins/blacklist/native_interaction_flow_a5_test.go
gofmt -l plugins/blacklist/blacklist.go plugins/blacklist/native_interaction.go plugins/blacklist/native_interaction_a5_test.go plugins/blacklist/native_interaction_flow_a5_test.go
go test ./plugins/blacklist -run '^TestA5C2' -count=1
go test -race ./plugins/blacklist -run '^TestA5C2' -count=1
go test ./plugins/blacklist ./plugins/filters ./internal/plugin ./internal/interaction/native ./internal/assistant/... ./internal/app
go test -race ./plugins/blacklist ./internal/interaction/native ./internal/plugin
git diff --check
```

If any test fails, fix that precise code or fixture and run `gofmt` before committing. **Do not inspect/poll CI unless explicitly requested.**

**A5 overall status:** A5-A/B CLOSED; A5-C0 CLOSED; A5-C1 CLOSED; A5-C2-A implemented/acceptance pending; A5-C2-B Filters manager and final contextual group UX gate OPEN. **Do not mark A5 CLOSED.**

## 17. A5-C2-A focused acceptance and durable inventory repair (2026-10-08)

### A5-C2-A focused and package acceptance — PASSED

The user fast-forwarded `test-next` from `187be451` to `4aeacf884903b58a7445ea8a216b732b588b973d`, then reported:

- `gofmt -w plugins/blacklist/*.go`, `gofmt -l plugins/blacklist/*.go`, and `git diff --check`: no errors or outstanding format output.
- `go test ./plugins/blacklist -run '^TestA5C2' -count=1`: **PASS**.
- `go test -race ./plugins/blacklist -run '^TestA5C2' -count=1`: **PASS**.
- `go test ./plugins/blacklist ./plugins/filters ./internal/plugin ./internal/interaction/native ./internal/assistant/... ./internal/app`: **PASS**, all listed packages.
- `go test -race ./plugins/blacklist ./internal/interaction/native ./internal/plugin`: **PASS**.

**A5-C2-A's focused acceptance is CLOSED.** The subsequently executed repository-wide `go test -race ./...` ran through the application and plugin packages but failed **only** `internal/architecture/TestDurableFeatureInventory`. The failure is a deliberate durability-inventory gate, not a reported race or a failure of Blacklist's focused tests.

### Durable inventory blocker and repair — implementation pushed, rerun PENDING

The architecture guard at `internal/architecture/durable_feature_inventory_test.go` lists every nonempty `DurabilityVersion` declaration and requires a corresponding owner-maintained semantic restart test. A5 previously added durable a2 specifications to AFK, PMPermit, and Blacklist without updating that acceptance contract.

Commit `969e8e1414ba1846b3b6e3b2f0fee9e94201db50` corrects this **without weakening or removing the guard**:

1. `plugins/afk/durable_restart_test.go`: owner opens the real AFK a2 dashboard, captures the enable callback, preserves session in SQLite, restores across plugin generation, executes the old callback, verifies AFK activation, and rejects replay as stale.
2. `plugins/pmpermit/durable_restart_test.go`: same real dashboard/durable restore lifecycle, requiring the old toggle callback to mutate the owner PMPermit service and its replay to be stale.
3. `plugins/blacklist/durable_restart_test.go`: create a group rule, open the real group-bound menu, select it to reach the confirmation revision, preserve/restore the SQLite session, then execute the old confirmation callback against freshly verified group role and check persisted deletion plus stale replay.
4. Update the inventory's expected declaration map and per-feature proof paths; additionally require every expected declaration to have a proof entry.

All three new Go test files were formatted through `gofmt` locally **before the commit**; their Git blob SHA-1 hashes match byte-for-byte with the formatted files. This repair has **not yet been verified by the user's checkout**, and the repository-wide race suite must not be called green yet. CI was not inspected.

Next gate:

```bash
git pull --ff-only
gofmt -w internal/architecture/durable_feature_inventory_test.go plugins/afk/durable_restart_test.go plugins/pmpermit/durable_restart_test.go plugins/blacklist/durable_restart_test.go
gofmt -l internal/architecture/durable_feature_inventory_test.go plugins/afk/durable_restart_test.go plugins/pmpermit/durable_restart_test.go plugins/blacklist/durable_restart_test.go
go test ./internal/architecture -run '^TestDurableFeatureInventory$' -count=1
go test ./plugins/afk -run '^TestD4AFKOwnerCallbackSurvivesDurableRestart$' -count=1
go test ./plugins/pmpermit -run '^TestD4PMPermitOwnerToggleSurvivesDurableRestart$' -count=1
go test ./plugins/blacklist -run '^TestD4BlacklistConfirmedRemovalSurvivesDurableRestart$' -count=1
go test -race ./internal/architecture ./plugins/afk ./plugins/pmpermit ./plugins/blacklist
go test -race ./...
git diff --check
```

If a new restart test fails, fix the exact feature contract or fixture before changing inventory again. Do not poll or check CI unless explicitly requested. A5-C2-B Filters management remains OPEN; overall A5 is **not CLOSED**.

## 18. A5-C2-A full race accepted; A5-C2-B Filters native manager (2026-10-08)

### A5-C2-A full-repository race and semantic restart acceptance — CLOSED

The user fast-forwarded `test-next` from `4aeacf88` to `14a9b06ac10ff9c4a0ed4ac4923b4c5bc65ec6e1`, formatted the four touched Go proof files, and ran:

- `go test ./internal/architecture -run '^TestDurableFeatureInventory$' -count=1`: **PASS**.
- `go test ./plugins/afk ./plugins/pmpermit ./plugins/blacklist -run '^TestD4.*SurvivesDurableRestart$' -count=1`: **PASS**, all three packages.
- `go test -race ./...`: **PASS across the repository** (packages without tests correctly show `[no test files]`).
- `git diff --check`: no reported error.

This closes the earlier durability inventory blocker. The A5-A/B, A5-C0, A5-C1 and A5-C2-A acceptance gates are CLOSED. No CI was inspected.

### A5-C2-B — Filters contextual management, implementation pushed, acceptance PENDING

Commit `1918e6fc584c784a287708a6261421d5202ebd90`:

- `plugins/filters/native_interaction.go` declares a native userbot-only, group-scoped, Sudo-policy a2 manager with a 10-minute TTL, five rules per page, at most nine action slots, and plugin-generation cleanup. A live Assistant-managed `GroupRoleResolver` is required; without it the existing text command continues unchanged.
- `.filters` on userbot opens the menu only in a verified group/supergroup. Assistant routing, `.filter`, `.stop`, and `.filterinfo` retain their existing semantics. Users can list, page, preview truncated escaped response text/media type, confirm deletion, go back, refresh, or close.
- The session contains only canonical chat/topic coordinates, a content-based SHA-256 digest and up to nine bounded choices; **no rule cache, callback registry, worker, or RPC executor** is created.
- Every non-mutating callback revalidates the current Telegram group administrator role. Confirmed removal is serialized by the existing per-chat rule lock, reads the current database rules, verifies the snapshot includes **the full response content and media metadata** (not only keyword), revalidates the Telegram role **fresh under that lock immediately before commit**, then calls the existing `savedresponse.Service.CommitDelete` path so media cleanup ownership is preserved.
- Stale revisions are owned by canonical a2. Changes to the rule set, including a replacement of the response under an unchanged keyword, cause snapshot conflict instead of deleting an unintended rule. Same-keyword rules in different groups are isolated by the bound chat.
- `plugins/filters/native_interaction_a5_test.go`: deterministic pagination and bounded-state, keyword-content replacement, two groups with identical keyword, demotion, canceled DB operation, private/channel scope denial, a2 callback provenance, cross-group spoof and replay tests.
- `plugins/filters/durable_restart_test.go`: real SQLite session persistence across plugin generations followed by confirmation of a pre-restart callback, persisted deletion, and stale replay rejection.
- Updated `internal/architecture/durable_feature_inventory_test.go` to declare Filters' durability version 1 and point to the owning restart proof in the same change. No skipped inventory gate.

Three new Go files were locally passed through `gofmt` before commit, and the staged GitHub blobs match their exact locally formatted SHA-1 hashes. The two existing-file modifications were limited to the Filters native field/command dispatch and the inventory entries. **The user's authoritative Go build, unit tests, and race suite for this new commit have not been run or observed yet. Do not declare A5-C2-B or A5 CLOSED on the basis of staging alone.** CI was not inspected.

**Known limitation:** the snapshot/lock validation protects same-process writers that share the existing per-chat lock; it is not a cross-process SQLite compare-and-swap and cannot prove multi-instance or ABA safety. That requires a dedicated transactional revision design rather than an ad hoc second cache.

### Required acceptance gate for A5-C2-B

```bash
git pull --ff-only
gofmt -w plugins/filters/filters.go plugins/filters/native_interaction.go plugins/filters/native_interaction_a5_test.go plugins/filters/durable_restart_test.go internal/architecture/durable_feature_inventory_test.go
gofmt -l plugins/filters/filters.go plugins/filters/native_interaction.go plugins/filters/native_interaction_a5_test.go plugins/filters/durable_restart_test.go internal/architecture/durable_feature_inventory_test.go
go test ./plugins/filters -run '^TestA5C2' -count=1
go test -race ./plugins/filters -run '^TestA5C2' -count=1
go test ./plugins/filters -run '^TestD4FiltersConfirmedRemovalSurvivesDurableRestart$' -count=1
go test ./internal/architecture -run '^TestDurableFeatureInventory$' -count=1
go test ./plugins/filters ./plugins/blacklist ./internal/plugin ./internal/interaction/native ./internal/assistant/... ./internal/app
go test -race ./...
git diff --check
```

If a failure is reported, repair the precise contract or test fixture and format every touched Go file before committing. Do not check/poll CI unless explicitly requested. **A5-C2-B acceptance pending; A5 final contextual moderation UX gate remains OPEN.**

## 19. A5-C2-B accepted and final contextual moderation security gate (2026-10-08)

### User-run A5-C2-B full acceptance — CLOSED

The user fast-forwarded to `68a099bd0cb6302adff508351e35700c4e5d311e` and reported:

- `gofmt -w plugins/filters/*.go internal/architecture/durable_feature_inventory_test.go` and `git diff --check`: no errors.
- `go test ./plugins/filters -run '^TestA5C2' -count=1`: **PASS**.
- `go test -race ./plugins/filters -run '^TestA5C2' -count=1`: **PASS**.
- `go test ./plugins/filters -run '^TestD4FiltersConfirmedRemovalSurvivesDurableRestart$' -count=1`: **PASS**.
- `go test ./internal/architecture -run '^TestDurableFeatureInventory$' -count=1`: **PASS**.
- `go test -race ./...`: **PASS across the full repository**, including `internal/architecture`, Filters, Blacklist, AFK and PMPermit.

This **closes A5-C2-B's acceptance**. It does not imply live Telegram service/permissions or multi-instance CAS was tested. CI was not checked.

### Final A5 security audit — Blacklist demotion-to-delete window (corrected; gate PENDING)

Audit of `plugins/blacklist/native_interaction.go` against `plugins/filters/native_interaction.go` identified a concrete inconsistency. Blacklist formerly revalidated the administrator's Telegram `DeleteMessages` right **before** entering the per-chat write critical section; a demotion while waiting for the lock could leave a stale grant at delete time. Filters already checked fresh role **under** the lock.

Correction commit: `2f282de9b40850c67f99a89cd2fa041e2b66b8f4`.

- The Blacklist deletion function now **requires** the canonical a2 `interaction.Session`, callback `presentation.Target`, `GroupActionScope`, and the live Assistant-provided `core.GroupRoleResolver`. No authorization-free mutation overload remains in production.
- After locking the existing chat rule stripe and verifying the fresh database snapshot/target keyword, the function calls `nativeinteraction.AuthorizeFreshGroupAction` with administrator **and DeleteMessages** requirements **immediately before** marking state unknown and writing SQLite. There is no new resolver, lock, goroutine, callback system, or cache.
- Non-mutating menu actions still authorize on entry; the confirm action delegates its authorization to the DB write function to avoid a misleading stale preflight grant.
- Existing snapshot/DB cancellation tests were updated to pass a verified synthetic group callback identity (rather than bypassing contextual authorization). New `plugins/blacklist/native_authorization_a5_test.go` requires a `ResolveGroupRoleFresh` invocation while the actual write lock is held, denial after role demotion or removal of DeleteMessages, no cross-group deletion, rejection when the Assistant provider is absent, and snapshot conflict before any fresh-role RPC.
- All three Go files were processed through local `gofmt -w` before commit, with `gofmt -l` empty. Exact Git blob SHA-1 hashes match the formatted files. **Package and race tests for this corrective commit have not yet run in an authoritative Goultroid checkout**.

This narrows the demotion race to the final fresh-authority check before the DB mutation, but Telegram permission changes are inherently distributed; no claim of atomicity between a Telegram role RPC and SQLite commit across external processes is made.

### Final A5 acceptance gate — PENDING

```bash
git pull --ff-only

gofmt -w plugins/blacklist/native_interaction.go \
  plugins/blacklist/native_interaction_a5_test.go \
  plugins/blacklist/native_authorization_a5_test.go
gofmt -l plugins/blacklist/native_interaction.go \
  plugins/blacklist/native_interaction_a5_test.go \
  plugins/blacklist/native_authorization_a5_test.go

go test ./plugins/blacklist -run '^TestA5(Final|C2)' -count=1
go test -race ./plugins/blacklist -run '^TestA5(Final|C2)' -count=1

go test ./plugins/afk ./plugins/pmpermit ./plugins/blacklist \
  ./plugins/filters ./internal/plugin ./internal/interaction/native \
  ./internal/architecture ./internal/app
go test -race ./...
git diff --check
```

Review the resulting test output rather than assuming green. If anything fails, repair the exact code/test contract and rerun `gofmt` before committing. Never inspect/poll CI without explicit user instruction.

Final A5 behavior matrix (not all exercised in live Telegram): owner-only AFK/PMPermit enable/disable and fallback; Blacklist/Filters admin-only per-group listing and confirmed removal; chat/topic-aware callback bindings; revocation/demotion and DeleteMessages rights; same-keyword rules in separate groups; stale-token rejection and durable restoration across plugin generations; SQLite failure/replacement/media-cleanup paths; adapter unavailable and plugin unload/reload; no parallel workers/executors/state registries.

**Status: A5-A/B CLOSED; A5-C0 CLOSED; A5-C1 CLOSED; A5-C2-A/B CLOSED; final corrective security gate PENDING. Overall A5 NOT CLOSED until the above gate is green.** A6 observability is next, followed by A7 resource/restart/FloodWait acceptance.

## 20. Final A5 acceptance CLOSED and A6-A logging privacy hardening (2026-10-08)

### A5 final security acceptance — CLOSED

The user fast-forwarded `test-next` to `bdd5ffd2f68ca6f999bfdcd088034d4e0db63375` and ran:

- `gofmt -w plugins/blacklist/native_interaction.go plugins/blacklist/native_interaction_a5_test.go plugins/blacklist/native_authorization_a5_test.go`: completed without an error.
- `go test ./plugins/blacklist -run '^TestA5(Final|C2)' -count=1`: **PASS**.
- `go test -race ./plugins/blacklist -run '^TestA5(Final|C2)' -count=1`: **PASS**.
- `go test -race ./...`: **PASS across all repository packages**.
- `git diff --check`: no errors reported.

**A5-A/B, A5-C0, A5-C1, A5-C2-A/B, and the final Blacklist mutation-time contextual authorization gate are CLOSED by user-run tests. The A5 acceptance scope is code-level, fake Telegram transport, SQLite and Go race validation—not a claim of live Telegram operational or multi-instance distributed atomicity.** CI was not checked.

### A6 audit — verified source boundaries

- `internal/telegram/dispatcher_dispatch.go` previously logged the entire Telegram `msg.Message` as `text` alongside parser errors. Badly quoted commands and malformed escapes could disclose private messages and tokens into persistent structured logs.
- `internal/telegram/dispatcher_handlers.go` previously logged arbitrary hook panic values and raw plugin errors. `internal/telegram/dispatcher_dispatch.go` likewise logged the raw state-gate panic. Errors and panic values may embed content from a private PM or a sensitive command. Security hooks must preserve existing fail-closed behavior; feature/observer hooks must preserve fail-open behavior.
- AFK already emits mostly structured error categories and actor/chat IDs; UserLog intentionally delivers private PM/mention text to a configured Telegram log destination, separate from structured server logs. Do not silently remove the documented UserLog feature or start forwarding PM text into application logs.
- `internal/services/pmpermit/service.go` publishes `PMPermitEvent` with reason/error text to EventBus when subscribers exist. `internal/platform/audit/audit.go` allows arbitrary `AuditEvent.Details` values to be logged and retains map references in its bounded ring. These require caller inventory, disclosure policy and regression tests before deciding whether to redact or snapshot.
- Message hook decision execution already has the shared TaskEngine admission/failure policy; A6 should not add its own logging worker/queue or make security decisions wait for observability.

### A6-A — no raw PM/command/hook payload in dispatcher logs

Implemented in `0494d88e0516c158a3fc17ab603e79ffcbe9b645`:

- `internal/telegram/dispatcher_dispatch.go`: parser diagnostics now emit only a bounded category (`unclosed_quote`, `trailing_escape`, or `invalid_syntax`) without the message text or raw error string. State-gate panic logs only the static Go panic type.
- `internal/telegram/dispatcher_handlers.go`: hook panic logs the Go type, not its value; hook error logs only the static error type, canceled/deadline flags and existing fail-closed flag, never the arbitrary `err.Error()` message.
- `internal/telegram/dispatcher_privacy_a6_test.go`: observable Zap log assertions for two malformed private commands, a security vs observer hook error and panic under both failure policies, and a panicking state gate. Tests require no private marker in message/fields and preserve the admission decision semantics.
- The newly added regression test was formatted using local `gofmt`; its Git blob SHA matches the formatted source. Production changes were confined to small import/logging branches and their replacement expressions were checked with local Go formatting snippets. **Complete-repo package/race tests for the new A6-A code are NOT yet verified.**

Required A6-A acceptance:

```bash
git pull --ff-only
gofmt -w internal/telegram/dispatcher_dispatch.go \
  internal/telegram/dispatcher_handlers.go \
  internal/telegram/dispatcher_privacy_a6_test.go
gofmt -l internal/telegram/dispatcher_dispatch.go \
  internal/telegram/dispatcher_handlers.go \
  internal/telegram/dispatcher_privacy_a6_test.go

go test ./internal/telegram -run '^TestA6' -count=1
go test -race ./internal/telegram -run '^TestA6' -count=1
go test ./internal/telegram ./internal/core ./plugins/afk ./plugins/pmpermit \
  ./plugins/blacklist ./plugins/filters ./plugins/userlog ./internal/app
go test -race ./...
git diff --check
```

If any test fails, correct the actual root cause and update the relevant test before commit. Never inspect, poll or trigger CI without explicit user instruction.

### Remaining A6 execution plan

- **A6-B — Audit and PMPermit event disclosure contract.** Inventory actual subscriber/caller paths. Use a bounded, typed allowlist for structured audit metadata if privacy issues are demonstrated; protect the audit ring from caller mutation and accidental retention of mutable secret values. Preserve valid owner-visible audit signals. Do not allow untrusted Telegram text, arbitrary error values or tokens to become structured log fields.
- **A6-C — UserLog and automation observability acceptance.** Verify configured destination and category toggles, self-recursion suppression, no callback/command leakage, event-vs-decision isolation during slow Telegram I/O, capacity/backpressure behavior, and plugin unload/reload cancellation with the existing TaskEngine. Preserve intentional message-content forwarding in UserLog while minimizing unrelated server-side logging.
- **A6-D — Final acceptance matrix.** Cover PMPermit fail-closed state and warning logging under DB/RPC failure, AFK transition/welcome failure, Blacklist/Filters stale/demotion, Assistant/a2 callback errors, high-cardinality message pressure and zero/low idle overhead. Require focused tests and race. Then proceed to **A7 — resource/restart/FloodWait acceptance**.

**Status:** A5 CLOSED; A6-A implemented with test gate pending; A6-B/C/D and A7 OPEN. Do not claim full A6 security or resource closure yet.

## 21. A6-A accepted; A6-B audit and PMPermit event privacy implementation (2026-10-08)

### A6-A logging privacy acceptance — CLOSED

The user fast-forwarded to `604dc87b16f999084e7cba0ee5767e28063a38f3`, ran `gofmt` on the three modified dispatcher files, and supplied successful outputs for:

- `go test ./internal/telegram -run '^TestA6' -count=1`: **PASS**.
- `go test -race ./internal/telegram -run '^TestA6' -count=1`: **PASS**.
- `go test ./internal/telegram ./internal/core ./plugins/afk ./plugins/pmpermit ./plugins/blacklist ./plugins/filters ./plugins/userlog ./internal/app`: **PASS**.
- `go test -race ./...`: **PASS across repository**.
- `git diff --check`: no reported error.

A6-A is CLOSED by user-run acceptance; no CI was checked.

### A6-B caller audit and scope

The existing `internal/platform/audit` service is wired to the process, secret, storage, plugin manager and capability gate. High-impact production callers reviewed include:

- `internal/platform/process/manager.go`: previously sent raw `args []string` and `owner string` into arbitrary audit metadata for denied **and** allowed binary execution. Command arguments may include passwords, tokens, URLs and PM data.
- `internal/platform/secret/manager.go`: previously sent `Redact(val)` in audit metadata for secret writes. That helper reveals a short prefix/suffix and is inappropriate for durable or structured audit logs. Secret reads expose only `found`.
- `internal/plugin/manager.go`: uses action and target metadata for enable/disable without arbitrary `Details`, unaffected by the new scalar policy.
- `internal/platform/audit/audit.go`: previously retained caller-owned `Details map[string]any` and returned shallow event copies from `Recent`, allowing mutation after record or mutation of ring-held metadata by a caller. It also logged arbitrary nested values with `zap.Any`.
- `internal/services/pmpermit/service.go`: publishes `PMPermitEvent` into the existing EventBus only when subscribed. Owner-provided reason strings and raw errors from Telegram block/unblock could be copied into downstream observability handlers. The event bus already has its own bounded executor; no parallel observability runtime is needed.

### A6-B implemented — focused acceptance pending

Commit `1e7c37df9953e61cc93a15f7e040b0e5c373551c`:

- Audit `Record` now sanitizes `Details` at its shared boundary, retaining only a strict bounded allowlist of typed scalars: `found bool`, `secret_present bool`, `owner_present bool`, `arg_count int` (nonnegative). Unknown keys, maps, slices, error values, strings and incorrect types are dropped. The sanitized metadata are snapshotted on record; `Recent` makes a fresh map copy on return, so callers cannot mutate the retained ring contents.
- Process audit now emits `owner_present` and `arg_count` instead of owner content or raw `args`. The actual allowed process execution still receives the unchanged full argument list. Denied and successful attempts remain separately identifiable by their original action strings.
- Secret write audit now emits `secret_present` rather than a partially unmasked credential; secret lookups and stored values remain unchanged.
- PMPermit event `Action`, `UserID`, `WarnCount`, `Success` and timestamp remain present. `Reason` becomes a fixed categorical reason derived from action, `Error` becomes `operation_failed` when a failure is reported, and `TargetName` is omitted rather than carrying arbitrary text. Caller-facing returned errors and persistent PM status/reason semantics are unchanged.
- New tests in `internal/platform/audit/audit_test.go`, `internal/platform/process/manager_test.go`, `internal/platform/secret/manager_test.go`, and `internal/services/pmpermit/privacy_events_a6_test.go` check raw argument/credential payload disclosure, typed allowlist and ring snapshot immutability, and subscribed PMPermit events with secret-bearing reasons and Telegram errors.

**Gate discipline:** these changes were pushed using GitHub file/blob operations in an execution environment without the full Goultroid checkout. Full-file Go formatting and package/race tests for A6-B were **not executed here**; they must be run by the repository owner before declaring CLOSED. Do not claim CI or local tests passing. No CI was inspected.

### Required A6-B gate

```bash
git pull --ff-only
gofmt -w internal/platform/audit/audit.go internal/platform/audit/audit_test.go \
  internal/platform/process/manager.go internal/platform/process/manager_test.go \
  internal/platform/secret/manager.go internal/platform/secret/manager_test.go \
  internal/services/pmpermit/service.go internal/services/pmpermit/privacy_events_a6_test.go

go test ./internal/platform/audit ./internal/platform/process ./internal/platform/secret \
  ./internal/services/pmpermit -run '^TestA6' -count=1
go test -race ./internal/platform/audit ./internal/platform/process ./internal/platform/secret \
  ./internal/services/pmpermit -run '^TestA6' -count=1
go test ./internal/platform/... ./internal/services/pmpermit ./plugins/pmpermit ./internal/app
go test -race ./...
git diff --check
```

Correct test or code failures precisely and re-run `gofmt` before committing. Do not check/poll CI unless explicitly requested.

### A6-B boundaries, known limitations, and next work

- `AuditEvent.Target`, `Action`, and `CorrelationID` remain string fields. They were not universally redacted because known production callers use targets for plugin, binary and secret-key identification; a broader classification policy would require a separate caller-by-caller contract and backward-compatibility tests. These strings must not be used for arbitrary private message payloads.
- The audit ring's capacity is bounded by its configured size (1000 at current app wiring); event production still logs synchronously using the supplied Zap logger. Do not add a secondary audit worker or expand hot-path memory retention.
- The original human-readable PMPermit reason persists in PM storage when explicitly requested; only the **broadcast EventBus payload** is sanitized. The intentionally configured UserLog delivery of PM contents is a separate owner-controlled feature.
- **Next A6-C:** audit actual UserLog destination/privacy/category lifecycle; event/decision ordering; bounded queue/backpressure; and plugin reload/shutdown cancellation under existing TaskEngine. Then A6-D final logging/observability/security acceptance and A7 resource/FloodWait validation.

**Status: A5 CLOSED; A6-A CLOSED; A6-B implemented with test gate pending; A6-C/D and A7 OPEN.**

## 22. A6-B full race accepted; A6-C UserLog privacy and lifecycle (2026-10-08)

### A6-B acceptance — CLOSED

User fast-forwarded `test-next` from `604dc87b` to `e21f5e7d5e3fbba9a047cab4bf7427f6592935f5`, ran `gofmt -w` on eight modified A6-B Go files and `git diff --check`, and supplied the following successful results:

- `go test ./internal/platform/audit ./internal/platform/process ./internal/platform/secret ./internal/services/pmpermit -run '^TestA6' -count=1`: **PASS** across all four.
- `go test -race` with the same package and test selections: **PASS**.
- `go test ./internal/platform/... ./internal/services/pmpermit ./plugins/pmpermit ./internal/app`: **PASS**.
- `go test -race ./...`: **PASS across the entire repository**.

**A6-B CLOSED** by user-run acceptance. The privacy contract is specific to AuditService metadata and PMPermit EventBus payloads; it does not remove owner-configured UserLog message forwarding or claim remote observability is anonymized. CI was not checked.

### A6-C audit findings — UserLog

Review of `plugins/userlog/userlog.go`, `internal/services/userlog/service.go`, related userlog tests, EventBus registration, and `plugin.Scope.Go` found:

- The message observer runs in the existing `core.MessageHookEvent` observability lane (priority 90), not as a synchronous security decision. PM and mention text are intentionally sent to the owner's configured Telegram log destination when their category is enabled. The plugin rejects destination self-recursion, owner/bot messages, anonymous channel senders and broadcast channels.
- The plugin already owns a bounded `queueCapacity=256` and one lazy worker that retires after inactivity. It drops work on full queue, has lifecycle-owned EventBus subscriptions for admin and PMPermit events, and uses the existing shared Telegram RPC path without a second retry engine.
- `Service.recordFailure` previously retained `err.Error()` verbatim as `lastErrorMsg`, subsequently surfaced in owner-facing `.log` health display via `Stats.LastError`. Error strings from Telegram may contain private text or credentials. The delivery and settings lookup logs also used raw `zap.Error(err)`.
- During `ShutdownContext`, cancellation formerly occurred only after `wg.Wait()`, while the worker continued draining a potentially full 256-entry queue. A single pending Telegram send could run until its timeout; draining many sequential jobs could make plugin unload slow.
- The first lifecycle patch canceled the UserLog child context at shutdown, but an additional audit verified `plugin.Scope.Go` invokes its callback with the **parent scope context**, not that cancelable child context. The follow-up patch passes the actual UserLog lifecycle context to the worker so both queued deliveries and loop termination respond to shutdown.

### A6-C1 changes pushed — acceptance pending

Commits:
- `38f3155c7f510c9eb06525bf6bc4f3cb6223a1c6` — redact UserLog health/structured delivery errors, cancel before waiting and prevent stale queue execution, plus privacy and capacity/lifecycle regression tests.
- `dba80e319a2029270f475b56fa11b60d9a82b27c` — correct `Scope.Go` vs child context wiring (mandatory for prompt cancellation).

Changes:
- `internal/services/userlog/service.go`: `LastError` retains only fixed error categories (`canceled`, `deadline_exceeded`, `delivery_failed`); the owner-facing error returned from `LogPM/LogMention/LogAction` remains the original error. Raw Zap error fields for destination lookup, delivery, and category settings are removed in favor of bounded codes. Health counters/timestamps remain unchanged.
- `plugins/userlog/userlog.go`: shutdown closes new admission and cancels the UserLog child context *before* waiting for worker teardown. Worker receives that child context even though `Scope.Go` supplies the parent scope context. Worker checks cancellation before taking another queued task. This intentionally discards unsent notifications on unload instead of serially draining old-generation RPCs. Queue remains at 256; there is still only one lazy worker and no new retry executor or callback path.
- `internal/services/userlog/privacy_a6_test.go`: malicious Telegram error text cannot appear in Zap logs or `Stats.LastError` while caller retains the full original error; a deadline error is classified correctly.
- `plugins/userlog/lifecycle_a6_test.go`: a context-blocked Telegram sender holds the lazy worker while over 3× queue capacity of messages arrives. The test verifies bounded queue/drop behavior, quick shutdown, at most the in-flight send, no post-shutdown queued RPC sends, and scope goroutine settling.

The two new test files were formatted using local `gofmt`, with exact Git blob SHA-1 hashes equal to their staged GitHub blobs. Production edits were limited to small logging/context expressions; **full-repository compilation, formatting and race acceptance for A6-C have not yet been run in the user's authoritative checkout**. Do not declare A6-C or A6 CLOSED yet. No CI was inspected.

### Required A6-C acceptance gate

```bash
git pull --ff-only
gofmt -w internal/services/userlog/service.go internal/services/userlog/privacy_a6_test.go \
  plugins/userlog/userlog.go plugins/userlog/lifecycle_a6_test.go
gofmt -l internal/services/userlog/service.go internal/services/userlog/privacy_a6_test.go \
  plugins/userlog/userlog.go plugins/userlog/lifecycle_a6_test.go
go test ./internal/services/userlog ./plugins/userlog -run '^TestA6' -count=1
go test -race ./internal/services/userlog ./plugins/userlog -run '^TestA6' -count=1
go test ./internal/services/userlog ./plugins/userlog ./internal/telegram ./internal/core \
  ./plugins/afk ./plugins/pmpermit ./plugins/blacklist ./plugins/filters ./internal/app
go test -race ./...
git diff --check
```

If the new lifecycle test fails, re-check that the worker uses `p.ctx` rather than the `plugin.Scope.Go` callback's parent context, and check whether an in-flight transport respects cancellation. Fix the actual contract or fixture and gofmt all Go changes before commit. No CI checking unless explicitly requested.

### A6-C2 / A6-D next acceptance requirements

- A6-C2: verify category toggles for PM, mentions and admin actions; owner configured destination isolation and title/id validation, self-recursion, disabled/absent destination, and subscriber teardown/rebind on repeated unload/reload. Reuse the existing UserLog service and its lazy worker; no new logging queue.
- A6-D: acceptance matrix joining A6-A/B/C (private payload redaction, audit ring immutability, PMPermit state event categories, UserLog backpressure/shutdown), error behavior with security hook fail-closed vs observer fail-open, and high-cardinality/no-idle-goroutine settling. No new central observability runtime. Only mark A6 CLOSED when all required tests are green.
- A7 remains pending for resource/soak/restart/FloodWait acceptance. All external Telegram network I/O retains its shared RPC executor semantics.

**Status: A5 CLOSED; A6-A/B CLOSED; A6-C1 implemented and pending user test gate; A6-C2/D and A7 OPEN.**
