# Assistant vNext P5-C — Settings migration foundation

## Status

P5-C migrates the Settings **read-only navigation foundation** onto the P0-P4 interaction stack without moving mutation or free-form input yet.

The production owner/private shell now supports:

```text
Home
  ↓
Settings categories
  ↓
Selected category
  ↓
Selected setting
  ↓
Read-only detail
```

All screens stay inside the same bounded P1 session and use typed P2 actions plus P3 `Transition`.

## Source of truth

P5-C does not introduce a second settings registry.

Schema navigation reads from:

```text
settings.Service.Registry()
```

Effective values read from:

```text
settings.Service.Resolve(ctx, userID, chatID, namespace, key)
```

Detail source information is resolved from explicit Chat, User, and Global overrides before falling back to the schema default.

The legacy a1 Settings implementation remains active for mutations and compatibility.

## Typed navigation without callback payloads

P2 buttons intentionally carry only a declared `ActionID`; arbitrary callback state is not allowed.

Settings schema is dynamic, so P5-C does **not** generate callback IDs containing category names or `namespace:key`.

Instead the shell uses a fixed-size session navigator:

```text
version
screen
category index
setting index
refresh counter
```

The encoded state is exactly 16 bytes.

Static actions move the cursor:

- `settings`
- `settings_prev`
- `settings_next`
- `settings_open`
- `setting_prev`
- `setting_next`
- `setting_open`
- `setting_back`

This preserves the typed callback boundary while still allowing dynamic registry contents.

## Backward state compatibility

P5/P5-B sessions used an 8-byte refresh counter.

The P5-C decoder accepts that legacy shape and interprets it as:

```text
screen = Home
category = 0
setting = 0
refreshes = legacy uint64
```

Any subsequent transition emits the new 16-byte versioned state.

Unknown/malformed state fails closed to the zero Home navigator rather than retaining arbitrary bytes.

## Read-only categories

The Settings root shows one selected category at a time with bounded controls:

```text
Previous | Open | Next
```

Category order comes directly from `settings.Registry.Categories()`.

The selected category screen shows one selected setting and its effective value. Only the selected value is resolved from persistence; P5-C does not fan out one DB query per setting just to render a page.

## Read-only detail

The setting detail screen displays:

- stable schema key (`namespace:key`);
- effective current value;
- effective source;
- type;
- default;
- widget hint;
- bounded enum options;
- optional range and step.

There are deliberately no Change, Reset, Increment, Toggle, Select, or text-input controls in P5-C.

Sensitive current/default values are masked as `••••` before presentation.

## Dynamic registry semantics

Category and setting indexes are navigation cursors only.

A registry change can alter which item occupies an index. This is safe for P5-C because navigation is read-only.

**Future mutation code must not authorize a write from an index alone.** Before any mutation it must resolve a stable `namespace:key` identity from the current schema and validate that identity again immediately before persistence.

That invariant is intentionally documented now so the mutation phase cannot accidentally turn a read-only cursor into write authority.

## Admission

Settings screens and actions remain:

- Assistant-only;
- owner-only;
- private-chat-only.

Action admission and destination-screen admission are both checked at callback time, preserving the P5-B live-authorization behavior.

## Resource behavior

P5-C adds:

- no goroutine;
- no timer;
- no polling loop;
- no settings cache;
- no global navigation map;
- no callback payload registry.

Navigation reuses the existing P1 session state and the central Settings service cache.

## Compatibility

The a1 Settings routes remain unchanged and continue to own:

- bool/enum mutation;
- int/duration stepping;
- reset;
- string-input prompts;
- pending input TTL;
- persistence/error UX.

The a2 shell still exposes Classic menu so mutation workflows remain reachable while P5-C is read-only.

## Tests

P5-C covers:

- new Settings screen/action declarations;
- fixed-size state encoding;
- decoding legacy 8-byte shell state;
- category/setting cursor wrapping and clamping;
- read-only Settings view validation;
- sensitive value masking;
- effective value resolution through `settings.Service`;
- explicit source display;
- Settings navigation remaining one P1 session;
- stale-token behavior after entering Settings;
- absence of mutation controls in a2 detail.

## Deferred mutation phase

The next phase should add mutation only after defining:

1. stable setting identity binding (`namespace:key`);
2. optimistic revision semantics for UI state;
3. write authorization immediately before `Service.Set/Reset`;
4. typed mutation results and transport-error behavior;
5. free-form input session ownership, cancellation, timeout, and reload semantics;
6. failure recovery when persistence succeeds but presentation edit fails.

P5-C intentionally does none of those writes.
