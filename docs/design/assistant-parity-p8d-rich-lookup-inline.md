# Assistant Parity P8-D — representative rich search/lookup inline

## Status

**P8-D is CLOSED for implementation/source acceptance.**

P8-D fulfills the REPRESENTATIVE rich network lookup requirement frozen in P8-A without cloning every Ultroid provider.

Baseline:

`cfd7af505608b7ea76481df0604cbcdf7907ccb3`

Representative provider:

`plugins/wikipedia`

## Objective

P8-D proves that the existing FeatureSpec + Inline vNext path can support a real public network lookup with:

- one canonical backend shared by command and inline surfaces;
- bounded query/result cardinality;
- explicit cache policy;
- capability-gated network access;
- lifecycle-owned inline registration;
- cancellation/timeout inherited from the shared inline execution path;
- no plugin-owned search runtime or result cache.

The parity target is the interaction capability, not provider-for-provider duplication of Google, F-Droid, OrangeFox, Saavn, Twitter/X, Play Store, or TL lookup.

## Production flow

```text
inline: wiki <query>
    ↓
FeatureSpec InteractionInline
    ↓
generation-owned InlineBindings registration
    ↓
Inline vNext prepared/execution path
    ↓
bounded handler context
    ↓
Wikipedia searchPages()
    ↓
capability-gated network.Client
    ↓
one REST search request
    ↓
<= 5 rich article results
    ↓
Inline vNext generation-scoped cache/serialization
```

The existing command remains:

```text
.wiki <query>
    ↓
same searchPages(ctx, query, 1)
    ↓
summary lookup
```

There is no second search repository or Assistant-only search implementation.

## Feature ownership

Wikipedia now declares one non-command interaction:

```text
feature: wikipedia
interaction: lookup
kind: inline
surface: inline
policy: public
```

`InlineBindings()` binds that declaration to the `wiki` matcher.

The PluginManager continues to own registration through `RegisterOwned`, so disable/reload cleanup remains generation-safe without Wikipedia retaining registration handles itself.

## Query matching

The inline matcher accepts:

```text
wiki
wiki <query>
```

and rejects prefix collisions such as:

```text
wikileaks
```

This prevents the representative lookup from accidentally swallowing unrelated inline aliases.

## Bounds

P8-D hard bounds are:

| Dimension | Bound |
| --- | ---: |
| query bytes | 256 |
| results per network lookup | 5 |
| results emitted | 5 |
| local result cache owned by Wikipedia | 0 |
| plugin goroutines | 0 |
| plugin timers/tickers | 0 |
| search workers | 0 |

The existing JSON body limit remains 4 MiB, with error bodies limited to 2048 bytes.

The handler defensively truncates the returned page list even if the upstream/test provider violates the requested limit.

## Network amplification

One inline query performs exactly one Wikipedia search request.

P8-D deliberately does **not** fetch one summary per candidate result.

The REST search response already provides:

- title;
- description;
- excerpt;
- thumbnail metadata.

That is sufficient for the representative rich-result proof and avoids N+1 network amplification.

The legacy command can still fetch the first page summary because it produces one detailed message rather than a result set.

## Rich result rendering

Each result includes:

- bounded stable query-local result ID;
- article title;
- description;
- sanitized excerpt;
- canonical Wikipedia article URL;
- HTTPS thumbnail when supplied by Wikipedia.

Wikipedia search excerpts may contain HTML highlighting fragments. P8-D removes upstream tags, decodes entities, collapses whitespace, applies rune bounds, then escapes the final Telegram HTML output.

Protocol-relative thumbnails are normalized to HTTPS. Non-HTTPS thumbnail schemes are rejected rather than forwarded.

## Cache policy

The handler explicitly declares:

`CacheGlobal`

with Telegram cache time:

`60 seconds`

This is safe for the representative provider because Wikipedia search results are public and do not depend on actor/session state.

The cache is still owned by Inline vNext, not the plugin.

Generation identity participates in Inline vNext's scoped cache key through `HandlerVersion(handler)`, so a plugin reload cannot silently reuse an older generation's cached handler result.

No interactive/a2 tokens are present in these results, so the existing engine does not need to force `CacheNone`.

## Cancellation and lifecycle

The inline engine executes feature handlers under its bounded handler context.

Wikipedia passes `ctx.Ctx` directly into the capability-gated network client.

Therefore:

```text
inline request timeout/cancellation
    ↓
handler context cancelled
    ↓
network request cancelled
```

No detached goroutine or one-handler-per-query registration is created.

## Capability boundary hardening

P8-D removes an old fallback in `getJSON` that created:

`network.NewService(nil, nil)`

when the plugin HTTP client was unavailable.

That fallback bypassed the intended PluginContext capability boundary.

The production behavior is now fail-closed:

```text
CapHTTP unavailable / HTTP client not injected
    ↓
core.ErrUnavailable
    ↓
no network request
```

The module remains scoped only to:

`plugin.CapHTTP`

and gains no raw Telegram or process-execution capability.

## Public surface

Wikipedia's command was already public on userbot/Assistant surfaces.

The new inline lookup is also intentionally public through `feature.PublicPolicy(execution.SurfaceInline)`.

Abuse/backpressure remains owned by the existing Inline/TaskEngine/rate-limit architecture rather than plugin-local throttlers.

## Acceptance coverage

P8-D adds/extends tests for:

- Wikipedia capability advertising inline surface;
- valid public FeatureSpec declaration;
- generation-owned inline binding;
- exactly one lookup call;
- query forwarded correctly;
- requested result limit = 5;
- defensive result truncation;
- rich title/description/excerpt/URL/thumbnail conversion;
- HTML fragment sanitization and escaping;
- explicit global cache policy;
- help path performs no network request;
- oversized query performs no network request;
- matcher keyword boundary;
- missing HTTP capability fails closed;
- no plugin worker/ticker/search runtime;
- no `network.NewService` capability bypass;
- module remains `CapHTTP` only;
- lifecycle registration stays `RegisterOwned`;
- handler execution remains under existing timeout context.

Architecture fence:

`internal/architecture/assistant_p8d_test.go`

## Parity consequence

The P8-A REPRESENTATIVE network lookup requirement is now satisfied.

Provider-specific lookup integrations are no longer P8 closure blockers.

Future providers can reuse the same FeatureSpec/Inline vNext model as ordinary product features.

## Non-goals

P8-D does not:

- add Google search;
- add F-Droid/Play Store/OrangeFox/Saavn/Twitter providers;
- add a search daemon;
- add a plugin-local cache;
- add pagination state;
- add callback actions;
- change TaskEngine;
- change Inline vNext;
- inspect CI.

## Formatting and commit rule

All changed Go source for P8-D was processed with `gofmt` before commit.

## Next phase

Proceed to **P8-E — representative interactive downloader workflow**.
