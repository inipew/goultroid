# Goultroid Message Automation — A0 Baseline and Incremental Recovery

Status: **A4 ACCEPTANCE CLOSED (2026-10-08); A5 pending**. Historical A0–A3 records remain below.
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

### A5-C — Contextual group moderation (OPEN)

**Do not present A5-A/B as full A5 completion.** The group moderation UI needs its own narrow acceptance:

- Keep Filters and Blacklist commands in **group-only** context and preserve contextual administrator authorization; do not replace the existing `GroupAuthorizationRequirement{Level: Administrator}` by owner-only global policy or by a callback that trusts chat IDs from opaque client state.
- Before binding any a2 moderation mutation, inventory `plugins/filters`, `plugins/blacklist`, and the existing group authority resolver. Callback authorization must use the session-bound target chat/topic, a fresh contextual authority check immediately before mutation, and immutable rule identity / revision fencing.
- Disallow private/channel/unknown-class targets and stale generation/token mutations. Separate preview/list from add/remove mutation; keep per-chat caches bounded and no second task engine.
- Add deterministic regressions for two groups with the same keyword, demotion while the menu is open, forum-topic replies, stale rules, disabled/reloaded plugins, and rollback on DB failure.
- Do not change default PMPermit max warnings (settings **3**, constructor **4**) without a separate compatibility decision.

A5 will be CLOSED only after the owner management and contextual moderation acceptance gates pass. A6 observability and A7 resource/restart acceptance remain subsequent phases.
