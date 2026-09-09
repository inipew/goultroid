# ADR 0004: Plugin Capability and Platform Boundaries

- Status: Accepted
- Date: 2026-09-09
- Source: Blueprint §17–19 and §44–52; roadmap Phase 6

## Context

Plugins currently receive selected services but lack an enforceable declaration
for network, filesystem, process, secret, raw Telegram, storage, worker, and
scheduler access. Direct `http.Client`, `os/exec`, filesystem paths, or raw
MTProto bypass timeout, quota, security, ownership, and cleanup policy.

User permission determines who may invoke an action. Plugin capability
determines what the plugin implementation may access; these controls differ.

## Decision

The plugin manifest declares stable ID, API version, dependencies, conflicts,
capabilities, config schema, quotas, storage namespace, and migration metadata.
`plugin.Context` exposes only capability-approved, scope-bound services.

```text
telegram.read / telegram.send_message / telegram.raw
storage.read / storage.write
events / scheduler / tasks / workers
network.http / network.websocket
filesystem.data / filesystem.cache / filesystem.temp
process.execute / secret.read
```

`telegram.raw`, `process.execute`, `secret.read`, and raw network transport are
privileged: explicit manifest declaration plus runtime allowlist is required.
Gate checks happen when a service is created and whenever an operation runs, so
a stopped/revoked plugin cannot keep using an unrestricted cached client.

Platform owns policy:

- network: HTTP/WebSocket, SSRF, timeout, retry, quota, metrics, cleanup;
- filesystem: per-plugin roots, safe paths, disk limits, temp/orphan cleanup;
- process: executable allowlists, output limits, cancellation, tracking;
- secrets: config/environment references and redacted access metadata;
- Telegram: normalized abstractions; raw Gotd types remain adapter-only.

Architecture tests migrate to prohibit feature imports of `database/sql`,
`net/http`, `os/exec`, and unrestricted filesystem APIs outside approved
adapters.

## Consequences

- Security-sensitive implementation access is reviewable and denyable.
- HTTP/process/temp resources inherit scope ownership and diagnostics.
- Existing plugins require adapters, manifest updates, and fake platform tests.
- Capability interfaces must be supplied before default-deny enforcement.

## Alternatives Rejected

- Documentation-only convention: cannot prevent bypasses.
- All-powerful plugin context: prevents isolation and least privilege.
- Manifest-load-only validation: cannot stop a retained client after disable.

## Rollout and Acceptance

Introduce audit-only manifests and platform interfaces, migrate privileged
downloader/media/OCR/addon paths, add static guards and runtime denial tests,
then default-deny new plugins and remove legacy adapters. Tests must prove
denial, scoped path confinement, process/network/temp cleanup, secret
redaction, and continued independence of plugin capabilities from user
permissions.
