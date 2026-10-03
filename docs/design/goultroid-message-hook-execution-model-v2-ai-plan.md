# Goultroid Message Hook Execution Model v2 — AI Implementation Plan

Status: **PLANNED — implementation not started**

Audit / design baseline:

- Branch: `test-next`
- Baseline HEAD: `cae28e9df3c5538f831dfc28a86299babd807183`
- Baseline commit: `fix afk`
- Design date: 3 October 2026
- Primary trigger: AFK correctness fix exposed message-hook latency coupling, cross-lane ordering contention, and unclear separation between synchronous semantic barriers and asynchronous presentation side effects.

This document is the authoritative implementation handoff for restructuring Goultroid message-hook execution. It is not an AFK-only patch plan.

The goal is to establish a durable execution model for AFK, PMPermit, blacklist, filters, UserLog, and future message-hook plugins so that:

> structural routing stays cheap, inactive features do not enter TaskEngine, only semantic dependencies block command admission, presentation/network side effects never accidentally become command barriers, and all finite work continues to use the single shared TaskEngine and shared Telegram RPC executor.

---

## 1. Mandatory execution discipline

The next AI session MUST obey all rules in this section.

### 1.1 Refresh HEAD before every phase

Before starting R0, and again before starting each later phase R1–R8:

1. refresh the actual `test-next` HEAD;
2. record the exact SHA;
3. record the exact commit message;
4. inspect commits that landed since the previous phase;
5. re-evaluate whether those commits overlap files or invariants owned by this plan.

Never assume the baseline SHA in this document is still current.

If HEAD moved, continue from the actual branch state rather than resetting or force-recreating the documented baseline.

### 1.2 Go formatting rule

Before **every commit that changes any Go file**, run:

```bash
gofmt -w .
```

Then inspect the resulting diff before committing.

Do not substitute `gofmt -w <selected-files>` for this rule.

A documentation-only commit does not require `gofmt`.

### 1.3 Test synchronization rule

Whenever production/main Go code changes:

- inspect all directly related tests before committing;
- update tests whose assumptions, fixtures, interfaces, mocks, ordering keys, timing semantics, or expected side effects changed;
- add regression coverage for the behavior being changed;
- do not leave production code and tests describing different execution contracts.

A phase is not complete merely because production code compiles.

The expected workflow is:

```text
production change
    -> inspect relevant existing tests
    -> update/add focused tests
    -> run focused package tests
    -> run relevant architecture/regression tests
    -> gofmt -w .
    -> git diff --check
    -> commit
```

Do not defer obvious test updates to a later phase.

### 1.4 CI rule

Do **not** inspect, poll, wait for, or report CI status unless the user explicitly requests it.

Local/focused verification is allowed and expected.

Do not use CI as a substitute for understanding local failures.

### 1.5 Preserve single execution ownership

Do not create:

- a second TaskEngine;
- a second plugin execution registry;
- a second Telegram RPC executor;
- a plugin-local retry engine;
- a new callback protocol;
- a parallel downloader/runtime;
- a new permanent worker pool for message-hook effects;
- an ad-hoc goroutine executor for work that belongs to TaskEngine.

TaskEngine remains the single authority for finite asynchronous execution.

Telegram RPC continues through the existing shared RPC executor and shared limiter.

### 1.6 Resource/lifecycle rules

All new state, queues, registries, caches, timers, and retained metadata must be bounded.

Prefer zero-idle behavior.

Plugin generation/scope cancellation must fence stale work after disable/reload/shutdown.

Do not add cleanup goroutines if bounded/lazy cleanup or existing lifecycle facilities are sufficient.

### 1.7 Commit discipline

Keep phase commits reviewable and attributable.

Do not mix unrelated feature work into this plan.

Prefer one architectural change + its tests per commit rather than large mixed rewrites.

Do not claim a phase is closed until its explicit gate is satisfied.

---

## 2. Problem statement

Commit `cae28e9d` correctly fixed two AFK correctness problems:

1. outgoing auto-unAFK must finish before subsequent handling observes stale AFK state;
2. concurrent no-argument/toggle AFK commands must serialize their read + transition.

To guarantee the first invariant, outgoing AFK processing moved from the asynchronous event lane to the synchronous decision lane.

That exposed deeper architecture issues.

### 2.1 Inactive AFK still enters synchronous TaskEngine execution

The outgoing AFK registration currently matches every stable outgoing message.

It has no `StateGate`.

Therefore even when AFK is already inactive, an outgoing command/message can still take this path:

```text
Telegram update
  -> indexed outgoing route match
  -> decision handler selected
  -> TaskEngine interactive Submit
  -> ordering key chat:<chatID>
  -> ticket.Wait()
  -> AFK handler
  -> state already inactive
  -> return
  -> dispatcher continues
```

The operation is normally cheap but is not zero-cost and can inherit queue/ordering contention.

### 2.2 AFK transition and welcome delivery are in the same synchronous barrier

Current outgoing AFK handling performs:

```text
persist AFK=false
  -> publish in-memory inactive state
  -> cleanup
  -> resolve peer
  -> SendMessage("Welcome back")
  -> only then decision handler completes
```

The first two operations are state correctness.

The welcome message is presentation.

The command path currently cannot distinguish them.

### 2.3 FloodWait can affect latency after AFK is already inactive

The welcome send uses the shared Telegram RPC executor.

The executor correctly applies FloodWait penalties to the shared limiter dimensions, including global/account, family, method, and peer dimensions.

Consequences:

- an existing limiter penalty can keep an interactive AFK welcome waiting inline until the decision deadline;
- if welcome itself receives a server FloodWait, AFK may already be inactive while later Telegram RPCs still see the shared limiter penalty;
- command-start latency and command-output latency become difficult to distinguish.

This plan must eliminate AFK presentation work from **command-start** latency without weakening shared RPC safety.

### 2.4 Decision and event tasks share the same ordering key namespace

Decision work currently uses:

```text
chat:<chatID>
```

Event work also uses:

```text
chat:<chatID>
```

TaskEngine ordering locks are global across pools.

Therefore a long event task for a chat can block a later decision task for the same chat even though they belong to different semantic execution domains.

Moving AFK welcome from decision to event without correcting ordering domains would still permit priority inversion.

### 2.5 Registration semantics differ between single and multi registrations

Single canonical message-hook registration can gain a dynamic state gate through `MessageEventStatePlugin`.

`MessageEventRegistrationsPlugin` instead returns full registrations and does not automatically inherit the same state-gate contract.

The result is an inconsistent API model:

```text
single canonical registration
    -> manager may derive StateGate

split canonical registrations
    -> registration must remember to carry StateGate itself
```

The final model should make every registration self-contained.

### 2.6 Priority currently implies failure semantics

Decision failure behavior is derived from handler priority.

Security-priority hooks fail closed, while feature-priority hooks fail open.

Scheduling priority and failure semantics are different concerns and should become independently declared.

### 2.7 AFK owner state is global but ordering is chat-scoped

AFK state belongs to the owner, not to one chat.

Two simultaneous outgoing messages in different chats can create two AFK decision tasks with different `chat:<id>` ordering keys, then serialize later on `transitionMu`.

The true serialization domain for AFK state transition is owner-global.

---

## 3. Architecture that must not regress

The redesign must preserve the following strengths already present in Goultroid.

### 3.1 Cheap ingress classification

Ordinary Telegram traffic should continue to avoid:

- repository-wide plugin scans;
- durable database claims;
- heavy core-message construction;
- TaskEngine admission when no interested active feature exists.

### 3.2 Immutable indexed structural routing

The dispatcher currently builds an immutable fixed-class message route index and publishes it atomically.

Registration/removal is a cold path.

Ingress performs one atomic index load and class lookup.

Preserve this model.

### 3.3 State gates execute before TaskEngine admission

The dispatcher already supports a dynamic `StateGate` that can skip inactive features before task submission.

This concept should be strengthened and canonicalized, not replaced.

### 3.4 Command durability remains after cheap decision/invocation admission

Recognized commands should continue to reach durable command claims only after synchronous semantic decision/security handling.

Do not move durable idempotency to generic transport ingress.

### 3.5 Shared execution authorities remain canonical

Preserve:

- one TaskEngine;
- one shared Telegram RPC executor;
- one hierarchical RPC limiter;
- canonical plugin scopes/generations;
- bounded state;
- lifecycle cancellation;
- canonical a2 interaction runtime.

---

## 4. Target conceptual execution model

The target pipeline is:

```text
Telegram update
      |
      v
bounded ingress dedupe
      |
      v
cheap parse/classification
      |
      v
canonical MessageEnvelope / MessageHookFacts
      |
      v
indexed structural routing
      |
      v
PURE FAST GATE
      |
      v
+---------------- BARRIER / DECISION ----------------+
| only work whose result is required before command  |
| admission or message suppression may run here      |
|                                                     |
| no presentation RPC merely to finish a barrier     |
+----------------------+------------------------------+
                       |
                       v
invocation / security admission
                       |
                       v
durable command claim
                       |
                       v
shared TaskEngine command
                       |
             +---------+----------+
             |                    |
             v                    v
      feature effects       event/observability
      shared TaskEngine     shared TaskEngine
             |                    |
             +---------+----------+
                       v
              shared RPC executor
```

Core rule:

> A synchronous message-hook barrier may block downstream execution only for a semantic dependency. Presentation, notification, logging, cosmetic cleanup, and other non-essential network side effects must not be included merely to make the barrier “complete”.

---

## 5. Execution taxonomy

### 5.1 Structural routing

Purpose:

- reject irrelevant update classes with no plugin call and no TaskEngine work.

Allowed inputs:

- direction;
- peer class;
- command/plain;
- text present;
- mention;
- reply;
- media.

Requirements:

- immutable registration-time indexing;
- no plugin state lookup;
- no I/O.

### 5.2 Fast gate

Purpose:

- reject structurally relevant but currently inactive feature work before TaskEngine admission.

Target characteristics:

- lock-free or bounded constant-time;
- atomic/immutable snapshot reads only;
- no DB;
- no Telegram RPC;
- no network;
- no filesystem;
- no TaskEngine submission;
- no blocking locks on hot paths where an atomic snapshot is sufficient.

The gate should receive enough already-known facts to avoid submitting work solely to rediscover obvious exclusions.

Possible target shape:

```go
type MessageHookFacts struct {
    ChatID      int64
    Outgoing    bool
    IsCommand   bool
    CommandName string
    Origin      ExecutionSource
}
```

Exact API names may change after R0 inventory.

Do not expose raw MTProto objects merely to make the gate convenient.

### 5.3 Barrier / decision lane

Purpose:

- synchronous semantic dependencies only.

Examples:

- security allow/deny;
- moderation suppression;
- state transition that must be visible before command execution.

Non-examples:

- welcome message;
- logging;
- analytics;
- cosmetic edit;
- notification;
- delayed cleanup;
- ordinary auto-reply delivery.

Barrier code should be short and bounded.

### 5.4 Feature-effect/event lane

Purpose:

- work that can occur after the semantic decision has been committed.

Examples:

- AFK welcome;
- UserLog delivery;
- non-blocking notifications;
- filter response delivery where suppression decision is already known;
- post-transition presentation.

All finite work remains in shared TaskEngine.

### 5.5 Observability lane

Observability must never hold the same ordering lock as security/decision work unless there is a demonstrated semantic requirement.

Logging backlog or Telegram FloodWait must not delay later command/security admission.

---

## 6. Target registration model

The final registration contract should be self-contained.

A conceptual target:

```go
type MessageHookRegistration struct {
    Scope     tasks.ScopeIdentity
    Priority  int
    Routing   MessageHookRouting

    FastGate  MessageHookFastGate
    Execution MessageHookExecutionPolicy

    Handler   CanonicalMessageHookHandler
}
```

And conceptually:

```go
type MessageHookExecutionPolicy struct {
    Lane          MessageHookLane
    Timeout       time.Duration
    FailurePolicy MessageHookFailurePolicy
    Ordering      MessageHookOrderingPolicy
}
```

Exact names are not mandated. Semantics are.

### 6.1 Registration invariants

Every canonical registration must explicitly or canonically resolve:

- structural interests;
- fast/state gate;
- execution lane;
- failure policy;
- timeout/budget;
- ordering domain;
- plugin scope;
- handler.

The plugin manager should validate + attach scope + register.

Avoid different hidden rules for single vs multi registration.

### 6.2 No empty multi-registration success

A multi-registration plugin returning zero registrations should not silently appear successfully hooked unless the contract explicitly documents that state.

Prefer fail-fast validation.

### 6.3 Partial registration rollback must remain exact

If registration N succeeds and N+1 fails:

- all prior registrations from that plugin activation must be cleaned up;
- cleanup order should remain deterministic;
- no partial live hook set may survive failed plugin activation.

Add explicit regression coverage.

---

## 7. Ordering-domain redesign

This must be solved before moving AFK welcome into asynchronous event execution.

### 7.1 Current problem

Both decision and event tasks can claim:

```text
chat:<chatID>
```

TaskEngine ordering locks are global.

A general/event worker therefore can block interactive decision work.

### 7.2 Target principle

Ordering keys must represent the actual serialization domain, not merely the chat identity.

Conceptually:

```text
decision/barrier:
    msg-decision:chat:<chatID>

feature event:
    msg-event:<plugin-owner>:chat:<chatID>

observability:
    msg-observe:<plugin-owner>:chat:<chatID>

AFK owner transition:
    afk-transition:<ownerID>

AFK welcome/effect:
    afk-effect:<ownerID or chatID>
```

Exact string format may be different.

Prefer a centralized helper/policy that generates ordering keys so plugins do not manually create arbitrary collision-prone strings.

### 7.3 Required property

A stalled event/observability task must not make a later security/decision task ineligible solely because both happen in the same chat.

### 7.4 Preserve ordering where it is actually required

Do not remove ordering entirely.

Examples:

- sequential revisions/actions that require same-target order;
- owner-global AFK transition;
- same plugin/chat side effects where order is user-visible.

---

## 8. AFK target design

AFK becomes the first migration proving the new execution model.

### 8.1 Outgoing transition registration

Target semantics:

```text
lane:
    barrier / decision

structural interests:
    outgoing
    stable peer

fast gate:
    AFK active
    manual/non-automation origin
    command != afk

ordering:
    owner-global AFK transition domain

handler responsibilities:
    transition only
```

Allowed transition work:

- acquire local transition fence if still needed;
- re-read current AFK state;
- persist inactive state;
- atomically publish inactive in-memory state;
- bounded local state cleanup required for consistency.

Forbidden from the transition barrier:

- Telegram `SendMessage`;
- welcome delivery;
- unrelated peer-resolution network work;
- logging delivery;
- cosmetic cleanup;
- delayed deletion.

### 8.2 Incoming AFK registration

Target semantics:

```text
lane:
    event

structural interests:
    incoming private
    incoming mentioned group/channel
    incoming reply group/channel

fast gate:
    AFK active && auto-reply enabled

ordering:
    plugin-chat event domain
```

This handler may perform Telegram operations because it is not a command barrier.

### 8.3 AFK command behavior

`.afk`, `.afk on`, `.afk off`, and `.afk toggle` remain command-owned explicit state transitions.

The outgoing auto-unAFK hook must skip the AFK command before TaskEngine admission if possible.

This prevents:

```text
.afk
 -> auto-unAFK decision task
 -> AFK command task
```

when only the command should own the transition.

### 8.4 Command-triggered auto-unAFK UX

Preferred target:

- a normal owner command may auto-deactivate AFK;
- command execution begins immediately after the AFK state transition commits;
- standalone welcome is not required to precede command execution;
- preferably normal commands do not generate a separate welcome at all because command output already demonstrates activity.

If product behavior requires a welcome for commands, schedule it as a post-transition effect that never blocks command start.

### 8.5 Plain outgoing message UX

For a plain manual outgoing message:

```text
active AFK
  -> transition inactive
  -> continue message pipeline
  -> schedule bounded AFK welcome effect
```

Welcome failure never rolls AFK back to active.

### 8.6 Welcome effect ownership

Welcome effect must:

- use shared TaskEngine;
- use plugin scope/generation;
- use bounded timeout;
- use shared Telegram RPC executor through existing service;
- be cancelled/fenced on plugin reload/disable/shutdown;
- not create plugin-owned permanent workers;
- not use raw `scope.Go()` as a replacement executor.

---

## 9. Persistence semantics

Initial redesign must preserve DB-first AFK correctness.

Current desired transition ordering:

```text
persist AFK=false
  -> publish in-memory inactive state
```

Do not casually switch to:

```text
publish inactive
  -> async DB write
```

because process crash could resurrect AFK on restart.

If synchronous SQLite latency becomes unacceptable after effect separation, treat durable transition journaling/write-behind as a separate future design that reuses existing durable machinery.

Do not build an AFK-specific persistence retry loop.

---

## 10. FloodWait semantics

This plan must separate two latency questions.

### 10.1 Command-start latency

Define:

```text
L1 = Telegram ingress -> command handler started
```

AFK welcome or other presentation RPC FloodWait must not materially extend L1 after the required AFK transition has committed.

### 10.2 Command-output latency

Define:

```text
L2 = command handler start -> required Telegram RPC output completed
```

The shared RPC limiter may still delay/fail output according to real Telegram FloodWait policy.

Do not weaken global/shared limiter penalties as part of the AFK migration unless a separate RPC-scope audit proves the current limiter semantics wrong.

### 10.3 Required regression

A test must prove:

```text
AFK active
 -> owner sends .ping
 -> AFK transition commits inactive
 -> welcome/effect experiences FLOOD_WAIT_30
 -> .ping handler can still start without waiting 30 seconds
```

The command's own Telegram response may still encounter the shared limiter. That is L2 and must be reported separately.

---

## 11. Other plugin audit targets

AFK is the first migration. The architecture must then be validated against other hook users.

### 11.1 Blacklist

Current strengths:

- decision lane;
- active-chat feature state gate;
- incoming/plain/text structural interests.

Audit target:

- keep matching/suppression in the barrier;
- identify Telegram deletion/response work that can be effect-side without weakening moderation semantics;
- document any deletion that truly must be synchronous.

### 11.2 PMPermit

PMPermit is a real security/decision feature.

Target:

```text
barrier:
    permission/security decision
    suppression flags

effect:
    warnings / presentation / other non-essential delivery
```

If a Telegram mutation is truly necessary before security state is committed, document it explicitly as an exception.

### 11.3 Filters

Separate:

- filter match / command suppression decision;
- response delivery.

Do not keep response RPC in a command-critical barrier unless feature semantics require it.

### 11.4 UserLog

UserLog is correctly event/observability-oriented, but currently has an additional plugin-owned queue/lazy worker behind dispatcher TaskEngine event execution.

Audit whether this is still necessary.

Preferred long-term shape:

```text
dispatcher
 -> shared TaskEngine
 -> UserLog operation
```

Do not remove the existing queue until lifecycle/backpressure semantics are understood and covered by tests.

### 11.5 Future hook registration rule

New hooks must justify use of the barrier lane.

Default feature work should not be synchronous merely because it is easy to register there.

---

## 12. Implementation phases

Each phase begins by refreshing and recording current `test-next` HEAD.

### R0 — Baseline, reproducer, and invariant freeze

Status: **IMPLEMENTED / EXECUTION VERIFICATION PENDING**

Phase baseline:

- Refreshed branch: `test-next`
- Phase-start HEAD: `7e4f959e04e595fee83f76d9c37a263eead7180a`
- Phase-start commit: `docs(design): add message hook execution model v2 plan`
- R0 regression commit: `3ed53366772c0226e191bd009318435f019bc1e0` — `test(telegram): freeze message hook execution r0 baseline`
- Production semantics changed: **no**
- CI inspected/polled: **no**

Purpose:

Prove current behavior before changing the execution model.

#### R0.1 Production hook inventory

A scan of all production Go files under `plugins/` found exactly five plugin message-hook domains.

A separate scan of production `internal/plugin/`, `internal/telegram/`, `internal/app/`, and `internal/module/` found the canonical registrar/dispatcher implementation but no additional production plugin/direct hook caller outside that path.

Current production registration flow is therefore:

```text
plugin implementation
  -> plugin.Manager registerMessageHook
  -> Dispatcher.RegisterMessageHook
  -> immutable dispatcher route index
  -> decision/event TaskEngine admission
```

The privileged raw hook interfaces and direct `Add*MessageHandler` APIs remain compatibility surfaces, but R0 found no production plugin implementing the raw message-hook contract.

| Domain | Registration | Structural route | Priority / failure | Dynamic gate | Work currently inside hook | Current dispatcher ordering |
| --- | --- | --- | --- | --- | --- | --- |
| AFK outgoing | split canonical | outgoing + stable peer | 50 / fail-open | **none** | bot-origin checks, AFK DB deactivate, state publish, cleanup, peer resolution, welcome Telegram send | `chat:<chatID>` |
| AFK incoming | split canonical | private incoming OR group/channel mention/reply | 50 / event | **none** | AFK snapshot checks, optional replied-message Telegram lookup, cooldown, Telegram auto-reply | `chat:<chatID>` |
| Blacklist | single canonical | incoming + stable peer + plain text | 10 / fail-closed | `featureState.Interested(chatID)` | compiled-rule lookup; DB load on cache miss; synchronous Telegram delete; suppression decision | `chat:<chatID>` |
| Filters | single canonical | incoming + stable peer + plain text | 20 / fail-open | `featureState.Interested(chatID)` | compiled-filter lookup; DB load on cache miss; matched delivery is submitted to plugin TaskEngine client when available; suppression metadata | `chat:<chatID>` for hook task |
| PMPermit | single canonical | incoming/outgoing private | 10 / fail-closed | enabled-state gate | approval/status DB reads+writes, optional resolver work, warning/block/unblock/delete/send Telegram RPC, suppression | `chat:<chatID>` |
| UserLog | single canonical | incoming private OR mentioned group/channel | 90 / event | none | enqueue to plugin-owned bounded lazy queue, then log Telegram delivery | `chat:<chatID>` for dispatcher event task |

Important R0 observations:

1. AFK is the only current split canonical registration.
2. AFK split registrations do not supply `StateGate`; the manager's automatic `MessageEventStatePlugin` wiring only applies to the single-registration branch.
3. Blacklist, Filters, and PMPermit already prove that pre-TaskEngine dynamic feature-state gating is an established production pattern.
4. Blacklist and PMPermit currently perform Telegram RPC from the synchronous decision lane; those are R6 audit targets, not R0 changes.
5. Filters already separates response delivery into another TaskEngine submission when its task client is available, but matching/cache/database work still occurs in the decision task.
6. UserLog is event-oriented but has a second plugin-owned queue/lazy-worker layer after dispatcher TaskEngine admission; R7 owns that audit.

#### R0.2 Ordering-key inventory

In the dispatcher message-hook path, there are two authoritative task-key constructions:

```text
executeDecisionHandlersEnvelope
    OrderingKey = chat:<chatID>

dispatchEventHandlersEnvelope
    OrderingKey = chat:<chatID>
```

TaskEngine ordering locks are global rather than pool-local, so these identical keys create a real cross-lane serialization domain.

For owner-global AFK transition state, different chats instead produce different keys:

```text
chat:41
chat:42
```

so TaskEngine does not express the real AFK serialization domain; serialization happens later inside `transitionMu`.

R1 must correct both properties without removing ordering where it is semantically required.

#### R0.3 Added deterministic reproducers

R0 added:

`internal/telegram/dispatcher_message_hook_execution_r0_test.go`

The file freezes four current behaviors:

- `TestR0InactiveAFKStillAdmitsDecisionTask`
  - proves an ordinary outgoing message while AFK is already inactive still admits an AFK task to the interactive TaskEngine lane;
  - records the current scope/quota owner and `chat:<chatID>` ordering key.

- `TestR0DecisionAndEventTasksShareChatOrderingKey`
  - proves a decision task and event task for the same chat receive the exact same ordering key even though they use different TaskEngine pools;
  - this is the deterministic reproducer for the cross-lane collision R1 must remove.

- `TestR0DecisionOrderingIsChatScopedAcrossUpdates`
  - proves two decision updates in different chats receive distinct chat-scoped keys;
  - combined with AFK's owner-global state + `transitionMu`, this freezes the mismatch between TaskEngine ordering and AFK's real serialization domain.

- `TestR0AFKCommandStartWaitsForWelcomeRateLimitPath`
  - activates AFK, sends an ordinary owner command, blocks the welcome transport, and returns a structured 30-second rate-limit error when released;
  - proves DB state is already inactive while the command handler is still prevented from starting;
  - proves command admission resumes only after the synchronous welcome/rate-limit path returns.

Existing tests that also form part of the R0 evidence:

- `plugins/afk.TestAFKPlugin_OutgoingTransitionPreservesWelcomeOrder` — outgoing AFK handler does not complete while welcome send is blocked;
- `internal/telegram.TestDispatcher_AFK_EndToEnd` — current E2E invariant explicitly requires welcome before the update continues;
- RPC executor FloodWait tests — short waits may remain inline and server FloodWait penalties are recorded in the shared limiter;
- dispatcher feature-state tests — a false state gate skips TaskEngine admission before decision execution.

#### R0.4 Formatting and verification state

Before the Go-changing R0 commit, the generated test file was processed with the required command:

```bash
gofmt -w .
```

A staged whitespace check equivalent to `git diff --check` also passed in the available working directory.

This tool runtime does **not** expose a full Goultroid checkout and direct shell clone/network access is unavailable. Therefore focused package execution could not honestly be performed here without using CI, and CI inspection/polling is explicitly forbidden by this plan.

Do not claim the R0 tests have executed successfully yet.

Before starting R1 in an executable checkout, run at minimum:

```bash
go test ./internal/telegram -run '^TestR0'
go test -race ./internal/telegram -run '^TestR0'
go test ./plugins/afk -run 'TestAFKPlugin_OutgoingTransitionPreservesWelcomeOrder'
gofmt -w .
git diff --check
```

If any R0 test does not compile or does not reproduce the recorded baseline, repair the R0 test fixture first. Do **not** start R1 by changing production behavior to make a broken baseline test pass.

#### R0 gate

Audit/inventory: **complete**.

Regression code: **implemented**.

Execution gate: **pending local focused-test execution**.

R1 must not start until the focused R0 execution gate above passes.

### R1 — Introduce explicit ordering domains

Status: **PENDING**

Purpose:

Eliminate cross-lane ordering-key collision before moving effects.

Tasks:

- introduce centralized ordering-key/domain helpers or an equivalent policy representation;
- separate decision/barrier ordering from event/observability ordering;
- preserve same-domain ordering guarantees;
- migrate dispatcher-created message-hook task keys;
- update all tests that assert literal ordering keys;
- add regression proving a stalled event task does not block a same-chat decision task;
- verify owner quotas/resources remain unchanged.

Do not move AFK welcome yet.

Gate:

- decision and event work cannot contend solely because both use the same chat ID;
- ordering remains deterministic inside each intended domain.

### R2 — Canonicalize registration execution policy

Status: **PENDING**

Purpose:

Make one self-contained canonical registration model.

Tasks:

- define canonical fast/state-gate handling for both single and split registrations;
- make execution lane, failure policy, timeout, and ordering semantics explicit data rather than implicit priority magic where practical;
- validate zero/invalid registrations;
- preserve plugin scope attachment;
- add partial-registration rollback test;
- update manager/architecture fences;
- keep compatibility adapters only where required by current callers.

Gate:

- single and multi registrations obey the same semantic contract;
- no split registration silently bypasses state-gating capability;
- failure semantics are testable without inferring them only from priority.

### R3 — Add pure fast-gate facts

Status: **PENDING**

Purpose:

Avoid TaskEngine admission for exclusions already known at ingress.

Tasks:

- determine the minimum canonical facts required by current plugins;
- expose those facts without raw Telegram types;
- preserve immutable indexed structural routing;
- ensure fast gate is called before TaskEngine admission;
- document and test fail-open behavior if a fast gate panics;
- architecture-fence the gate against obvious I/O-capable contracts where feasible.

AFK fast gate must be able to skip at least:

- inactive AFK;
- automation/bot-origin outgoing messages;
- AFK commands.

Gate:

- inactive AFK outgoing commands/messages create zero AFK TaskEngine submissions;
- bot-origin messages create zero AFK transition submissions;
- `.afk` does not pay the auto-transition task.

### R4 — Split AFK transition from presentation effects

Status: **PENDING**

Purpose:

Make AFK state correctness synchronous and presentation asynchronous.

Tasks:

- refactor AFK outgoing handling into a transition-only barrier;
- keep DB-first persistence;
- move welcome delivery to a scoped shared-TaskEngine effect path;
- introduce owner-global AFK transition ordering;
- preserve local transition fence if still needed for direct command transitions;
- ensure welcome failure does not alter inactive state;
- decide/document command-vs-plain welcome UX;
- update AFK and dispatcher end-to-end tests.

Critical test changes:

Tests introduced by `cae28e9d` currently assert that welcome was sent before the next update proceeds.

That is no longer the target invariant.

Replace with:

```text
AFK transition must be committed before downstream command execution

welcome delivery may complete independently and must not be required
for the transition barrier to finish
```

Gate:

- state inactive before command handler starts;
- welcome completion not required for command handler start;
- no duplicate welcome under concurrent transition;
- plugin reload cancels/fences stale welcome work.

### R5 — FloodWait and latency acceptance

Status: **PENDING**

Purpose:

Prove the redesign solves the original latency coupling without weakening RPC safety.

Tests/benchmarks:

- inactive AFK command latency;
- active -> inactive transition latency;
- transition under SQLite contention;
- welcome with short limiter wait;
- welcome with server FloodWait above inline threshold;
- event backlog followed by decision arrival;
- same-chat cross-plugin event pressure;
- two-chat simultaneous AFK transition;
- command handler start vs command Telegram output completion.

Record separate L1 and L2 measurements.

Gate:

- welcome FloodWait cannot hold command-start barrier;
- shared limiter remains authoritative;
- no second retry path exists.

### R6 — Audit/migrate Blacklist, PMPermit, Filters

Status: **PENDING**

Purpose:

Apply the same semantic separation to existing decision hooks.

For each plugin:

1. classify structural routing;
2. classify fast feature-state gating;
3. identify actual barrier decision;
4. identify presentation/network effects;
5. choose real ordering domain;
6. choose explicit failure policy;
7. update tests before commit.

Do not mass-convert all plugins in one commit.

Gate per plugin:

- only necessary semantic decision remains synchronous;
- inactive feature work is rejected before TaskEngine;
- effect RPC cannot introduce unrelated command-start latency.

### R7 — Audit observability/UserLog execution

Status: **PENDING**

Purpose:

Ensure event/observability work uses shared execution ownership without priority inversion.

Tasks:

- audit dispatcher event task -> UserLog queue -> lazy worker double scheduling;
- document what backpressure/lifecycle behavior the inner queue currently provides;
- remove or simplify it only if TaskEngine already provides equivalent guarantees;
- verify log FloodWait cannot block decision domain;
- preserve bounded queue/state and zero-idle behavior.

Gate:

- no new permanent worker;
- no unbounded observability backlog;
- no decision priority inversion;
- lifecycle/reload tests pass.

### R8 — Final cleanup, fences, and acceptance matrix

Status: **PENDING**

Purpose:

Retire compatibility paths only after production callers migrate.

Tasks:

- remove dead registration adapters/interfaces whose exit condition is satisfied;
- add architecture fences preventing new implicit decision-lane effects;
- document final registration/execution contract;
- verify unload/reload/shutdown;
- verify state/caches/task workers settle after burst;
- verify no stale generation work survives plugin reload;
- update this document with final commit SHAs and CLOSED status.

Gate:

- all acceptance scenarios in section 13 pass;
- no known production hook relies on undocumented cross-lane ordering;
- no second execution/retry authority was introduced.

---

## 13. Required acceptance matrix

The final redesign is not complete until the following matrix is covered.

| Scenario | Required result |
| --- | --- |
| AFK inactive + `.ping` | zero AFK TaskEngine admission |
| AFK inactive + plain outgoing | zero AFK TaskEngine admission |
| AFK inactive + bot-generated outgoing | zero AFK TaskEngine admission |
| AFK active + `.afk off` | auto-transition hook skipped; command owns transition |
| AFK active + `.ping` | inactive state committed before command handler starts |
| AFK active + `.ping`, welcome FloodWait 30s | command handler start does not wait 30s |
| AFK active + plain outgoing | exactly one active -> inactive transition |
| two chats send simultaneously while AFK active | exactly one successful transition; no duplicate semantic transition |
| AFK persistence fails | in-memory/persistent state follow documented DB-first failure policy |
| welcome delivery fails | AFK remains inactive |
| plugin reload while welcome queued | old-generation welcome is cancelled/fenced |
| stalled AFK effect | later decision work remains eligible |
| stalled UserLog/event task | later decision work remains eligible |
| same-chat event burst | no cross-lane ordering priority inversion |
| Blacklist inactive chat | zero blacklist TaskEngine decision admission |
| Filters inactive chat | zero filter TaskEngine admission |
| PMPermit barrier failure | explicit registered failure policy is honored |
| idle system | no new permanent workers/goroutines |
| post-burst settling | TaskEngine workers/caches/state return to documented bounded steady state |

---

## 14. Test and verification strategy

### 14.1 Focused tests first

After each change, run the smallest relevant package tests first.

Likely packages include:

```text
./plugins/afk
./internal/plugin
./internal/telegram
./internal/admission
./internal/taskengine
./plugins/blacklist
./plugins/pmpermit
./plugins/filters
./plugins/userlog
./internal/architecture
```

Expand only as the phase touches additional packages.

### 14.2 Race tests

Run focused `-race` tests for concurrency-sensitive phases, especially:

- R1 ordering;
- R3 fast gate publication;
- R4 AFK transition;
- R6 stateful moderation hooks;
- R7 lifecycle/queue changes.

If broader `go test -race ./...` is run, do not attribute unrelated existing failures to this work without reproducing causality.

### 14.3 Formatting and diff checks

Before every Go-changing commit:

```bash
gofmt -w .
git diff --check
```

Then re-run focused tests affected by formatting/fixture changes if necessary.

### 14.4 Do not use CI

No CI polling unless explicitly requested.

---

## 15. Failure and rollback strategy

### 15.1 Do not combine ordering redesign and AFK effect migration in one commit

R1 must make ordering domains safe first.

Only then may R4 move welcome out of the barrier.

This keeps regressions attributable and prevents “async but still ordering-blocked” behavior from being mistaken for success.

### 15.2 Preserve compatibility until callers migrate

Do not delete old interfaces/helpers merely because a new registration model exists.

Add explicit fences and remove compatibility only in R8 when caller inventory proves the exit condition.

### 15.3 Fail closed only where intended

Security/moderation fail-closed behavior must be explicit.

Feature/presentation failures should not accidentally suppress commands because TaskEngine admission or Telegram RPC failed.

### 15.4 Persistence remains authoritative

If AFK DB persistence fails during deactivation:

- do not silently publish inactive state and pretend durability succeeded;
- keep behavior consistent with the chosen DB-first invariant;
- log/return according to current fail-open message-hook policy without inventing a retry worker.

---

## 16. Non-goals

This plan does not authorize:

- replacing TaskEngine;
- replacing the hierarchical RPC limiter;
- removing Telegram FloodWait penalties;
- redesigning a2 interaction;
- changing command idempotency format;
- adding a new generic event bus/runtime;
- rewriting all plugins at once;
- changing unrelated MyXL/downloader features;
- introducing a second plugin worker system.

RPC penalty-scope changes require a separate audit if later evidence shows the current global/family/method/peer semantics are incorrect.

---

## 17. Review checklist before every phase commit

Before committing, answer all of these:

1. Did I refresh and record current `test-next` HEAD for this phase?
2. Did production Go code change?
3. If yes, did I inspect and update all directly affected tests?
4. Did I add a regression for the exact behavior changed?
5. Did I run `gofmt -w .`?
6. Did I run `git diff --check`?
7. Did I avoid CI inspection/polling?
8. Did I keep all finite work on the shared TaskEngine?
9. Did I keep all Telegram RPC on the shared executor?
10. Is every new queue/cache/state structure bounded?
11. Does reload/unload cancellation remain correct?
12. Did I accidentally couple presentation RPC to a semantic barrier?
13. Did I accidentally create an ordering-key collision across execution domains?
14. Is failure policy explicit rather than inferred accidentally from scheduling priority?
15. Is the commit limited to the current phase?

If any answer is unclear, do not close the phase.

---

## 18. Recommended next action

The next AI session should start with **R0 only**.

Do not immediately edit AFK production code.

Required first actions:

1. refresh `test-next` HEAD and record SHA + commit message;
2. read this document completely;
3. inspect current:
   - `internal/core/message_hook.go`;
   - `internal/plugin/plugin.go`;
   - `internal/plugin/manager.go`;
   - `internal/telegram/dispatcher_handlers.go`;
   - `internal/telegram/dispatcher_dispatch.go`;
   - `internal/admission/controller.go`;
   - `plugins/afk/afk.go`;
4. inspect tests around message hook state, failure policy, AFK E2E, ordering, and TaskEngine admission;
5. build the R0 inventory and deterministic repro tests;
6. make no semantic production change until R0 evidence is complete.

The first production redesign phase after R0 is **R1 — Introduce explicit ordering domains**.

Do not move AFK welcome asynchronously before R1 is closed.

---

## 19. Fast-start context for a new AI session

Use the following condensed context when opening a fresh session:

> Continue Goultroid on branch `test-next` using `docs/design/goultroid-message-hook-execution-model-v2-ai-plan.md`.
>
> Mandatory: refresh current `test-next` HEAD first and record exact SHA + commit message. Start from the next OPEN phase; do not restart the audit from zero.
>
> The redesign was triggered by AFK commit `cae28e9df3c5538f831dfc28a86299babd807183` (`fix afk`). That commit correctly made outgoing auto-unAFK synchronous and made AFK toggle atomic, but it exposed broader execution-model problems: inactive AFK still enters synchronous TaskEngine, AFK welcome/RPC is coupled to the decision barrier, decision and event tasks collide on global `chat:<id>` ordering keys, split registrations do not inherit the same state-gate semantics as single registrations, and priority currently doubles as failure policy.
>
> Target architecture: indexed structural routing -> pure fast gate -> semantic barrier only -> command admission -> asynchronous feature/effect/observability work on the same shared TaskEngine -> shared Telegram RPC executor. Presentation/network side effects must not block command start unless there is a documented semantic requirement.
>
> First phase is R0 baseline/reproducer only. R1 ordering-domain separation must close before moving AFK welcome out of the decision barrier.
>
> Rules:
> - before every Go-changing commit run `gofmt -w .`;
> - do not inspect/poll CI unless explicitly requested;
> - whenever production code changes, inspect and update affected tests in the same phase/commit;
> - no second TaskEngine/RPC executor/retry engine/worker runtime;
> - keep queues/caches/state bounded and lifecycle-scoped.

---

## 20. Completion definition

This redesign is complete only when:

- inactive features are skipped before TaskEngine;
- semantic barriers contain only work required for downstream correctness;
- AFK transition correctness no longer depends on welcome delivery;
- event/observability work cannot block decision work via shared ordering keys;
- registration execution/failure/ordering policy is explicit and consistent for single and split registrations;
- AFK, Blacklist, PMPermit, Filters, and UserLog have been audited against the model;
- FloodWait in optional feature effects cannot inflate command-start latency;
- lifecycle/reload/resource-settling acceptance passes;
- tests and architecture fences describe the final model;
- compatibility paths have explicit status and are removed only when their exit conditions are satisfied.

Until those conditions are met, the plan remains OPEN.
