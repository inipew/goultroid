# P3-B Shared Media Inventory

This inventory is based on `test-next` after P3-A and the initial bounded-storage-enumeration change. It records the actual production paths that can create or consume data in the shared `data/storage` backend before global ownership migration begins.

## Composition boundary

`internal/app/wiring_services.go` creates one `storage.Storage` backed by `data/storage` (10 GiB quota) and injects the same instance into the media service and builtin module runtime. If FileStorage initialization fails, the app falls back to process-local MemoryStorage.

Ownership metadata must therefore live above `storage.Storage`. `Storage.Put` remains a physical persistence primitive and does not infer domain ownership.

## Current producers and consumers

| Subsystem | Physical write path | Durable reference today | Actual lifecycle | P3-B implication |
| --- | --- | --- | --- | --- |
| Notes / SavedResponse | `savedresponse.captureMedia -> Storage.Put` | `notes.media_asset_id`; compatibility ledger `saved_response_media_assets` | Persistent while note references it | First persistent consumer to migrate to the global registry. |
| Filters / SavedResponse | `savedresponse.captureMedia -> Storage.Put` | `filters.media_asset_id`; compatibility ledger `saved_response_media_assets` | Persistent while filter references it | Same ownership domain as Notes, separate reference source. |
| Broadcast | No independent persistent write; materializes SavedResponse media for delivery | Reads SavedResponse reference | Read/materialize only | Consumer, not an ownership authority. |
| Clone | `clone.persistSnapshot -> Storage.Put` | `clone_state.original_photo_path` with `asset:<id>` encoding | Persistent only while clone rollback/revert needs the snapshot | Must be onboarded as its own owner/reference kind before global reclamation. |
| Media transcoder | `FFmpegTranscoder.Run -> Storage.Put` | No durable DB reference | Transient; plugin deletes output after send through `withTransientAsset` | Zero references are expected. Lifecycle must prevent a global reconciler from treating an in-flight transient output as an orphan. |
| Downloader URL | HTTP/extractor provider -> `Storage.Put` | No DB reference | Retained user download | Zero references do **not** mean orphan; owner/lifecycle metadata is required. |
| Downloader Telegram media | `ctx.DownloadMedia(Storage.BasePath())` | No registry/DB reference | Retained user download | Bypasses the managed `<asset-id>/meta.json` layout and can leave regular files directly in `data/storage`; enumeration must report them as unmanaged legacy entries and never delete them by inference. |

The Media and Downloader plugins contain standalone fallback storage paths (`data/media` and `data/downloads`) for non-production/manual construction. Normal application wiring supplies the shared runtime services, so the production inventory above is centered on `data/storage`.

## Stage-1 registry model

The global registry uses two independent tables:

- `media_assets`: asset provenance (`producer`), lifecycle authority (`owner`), and lifecycle policy.
- `media_asset_references`: one row per `asset_id + subsystem + reference_kind + reference_key`.

Reference rows are the source of truth. There is intentionally no mutable `ref_count` column. References are allowed to exist before an asset registration so migration can represent and diagnose the `untracked + referenced` state rather than hiding it.

Initial lifecycle values are `persistent`, `retained`, `transient`, and `legacy`. A zero-reference asset is only an observation; it is not a delete authorization.

## Observe-only physical classification

Bounded storage enumeration and the global registry observer classify physical entries as:

1. `tracked_referenced`
2. `tracked_unreferenced`
3. `untracked_referenced`
4. `legacy_untracked`
5. `malformed`

Stage 1 performs no physical deletion and does not replace `saved_response_media_assets` or `saved_response_media_cleanup`. Those compatibility structures remain authoritative for the existing P3-A SavedResponse lifecycle until later P3-B migration stages explicitly move each producer/reference source.


## Stage 2 — SavedResponse compatibility migration

Notes and Filters now mirror persistent SavedResponse media into the global registry while retaining the P3-A ledger and cleanup journal.

For databases where the global registry schema is present, a Notes/Filters media mutation uses one SQL transaction for:

- the feature row mutation;
- ensuring the asset remains present in `saved_response_media_assets`;
- registering/updating the global `media_assets` ownership record;
- adding the exact Notes/Filters reference; and
- removing the previous global reference on replacement or delete.

The canonical reference identities are `notes/note/<chat_id>:<name>` and `filters/filter/<chat_id>:<lowercase-keyword>`. An ownership conflict in the global registry rolls the whole domain mutation back. A schema-inspection error also fails closed; a genuinely absent global registry keeps the legacy repository behavior for standalone/compatibility environments.

Startup reconciliation still runs the P3-A SavedResponse reconciliation first. It then performs a bounded, idempotent compatibility pass that backfills old ledger assets and Notes/Filters references into the global registry, removes only stale **registry metadata** owned by this compatibility layer, and runs bounded consistency checks for:

- P3-A ledger assets missing from the global registry;
- SavedResponse global assets with incorrect producer/owner/lifecycle metadata;
- Notes/Filters media references missing from the P3-A ledger;
- Notes/Filters media references missing from the global reference registry;
- SavedResponse-owned global assets no longer represented by the old ledger; and
- stale global Notes/Filters references whose source row no longer exists.

The verifier reports whether findings were truncated by the configured batch bound so large legacy datasets can converge across bounded passes without unbounded startup work.

### Authority boundary remains unchanged

Stage 2 does **not** transfer deletion authority. `saved_response_media_cleanup` and the existing P3-A lifecycle remain responsible for physical SavedResponse reclamation. The global compatibility reconciler never calls `Storage.Delete`; stale global rows can only be removed as metadata, and an asset metadata row is removed only when the old ledger no longer contains it and the global reference registry has zero references.

Global physical reclamation therefore remains a later P3-C concern, after the registry has been stabilized and the remaining persistent/retained/transient producers have been onboarded.
