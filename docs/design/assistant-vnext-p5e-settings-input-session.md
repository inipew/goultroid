# Assistant vNext P5-E — Free-form Settings input session

## Status

P5-E migrates free-form string Settings input from the legacy a1 pending-input map onto the P0-P5 interaction stack.

The a2 path is now:

```text
bound string setting detail
        ↓
setting_input action
        ↓
P1 ArmInput(session, actor+chat, TTL)
        ↓
revision advances before prompt render
        ↓
next ordinary Telegram text update
        ↓
P1 TakeInput(actor+chat)
        ↓
revision advances before handler/persistence
        ↓
stable namespace:key + schema revision revalidation
        ↓
SetRegisteredResult
        ↓
detail render / recovery
```

No long-lived Telegram conversation goroutine, waiter goroutine, polling loop, or feature-local pending-input map is introduced.

## Ownership

Pending input is owned by `interaction.Runtime`.

Each live interaction session may own at most one pending input claim, and each actor+chat pair may have at most one live claim.

The input index is therefore bounded by the already-bounded P1 session population.

A claim contains only:

- actor/chat lookup identity;
- owning session ID;
- expiration deadline.

The free-form value itself is never retained in the interaction runtime.

## Lifecycle

Input claims are removed when:

- the next matching input is taken;
- input TTL expires;
- any ordinary session state transition occurs;
- the session is canceled;
- the owning plugin generation is canceled/reloaded;
- the session expires;
- the interaction runtime shuts down;
- prompt presentation fails after arming.

There is no expiry goroutine. Expiry is checked lazily on input lookup and diagnostics, matching the zero-idle P1 design.

## Optimistic revision fences

Input uses two revision boundaries.

### Entering input mode

`Context.AwaitInput` calls `Runtime.ArmInput`, which atomically:

1. validates the current session revision;
2. advances opaque state to the input screen;
3. advances the P1 session revision;
4. installs the actor+chat claim.

The old detail buttons are stale before the input prompt is visible.

If prompt editing fails, the claim is released. The revision is not rolled back.

### Consuming text

`Runtime.TakeInput` atomically:

1. finds the actor+chat claim;
2. removes the claim;
3. advances the P1 session revision;
4. returns the current generation-bound session.

Concurrent text updates therefore cannot both reach persistence, and the visible Cancel button is already stale before the accepted text is processed.

## Transport ingress

Assistant `OnNewMessage` checks a2 pending input before the legacy menu pending-input path.

The a2 input ingress only claims a message when the P1 runtime reports an active claim for that exact actor+chat.

Slash commands other than `/cancel` are not consumed and continue through the normal command router while an input claim is pending.

`/cancel` is consumed only when a matching a2 input claim exists.

The original Settings message target is reconstructed from the P1 session binding; no transport target or access-hash object is retained in the pending-input claim.

## Stable setting authority

The input screen carries the same P5-D detail binding:

- normalized `namespace:key` fingerprint;
- exact per-definition schema revision.

The input state does not carry the new user value.

When text arrives, the handler:

1. requires the session to still be on `setting_input`;
2. validates actor/private admission again;
3. validates the stable setting fingerprint;
4. validates the bound schema revision;
5. validates the value against the bound definition;
6. revalidates the same binding again immediately before persistence;
7. uses `SetRegisteredResult`.

A registry reorder or schema replacement therefore fails closed and never turns the old cursor into write authority.

## Bounded input

The a2 Settings free-form input contract uses:

```text
TTL:       2 minutes
max bytes: 4096
```

Empty/whitespace-only input is rejected.

Oversized or invalid input does not require the user to press Change again. The same session is re-armed with a new revision and a fresh bounded claim.

No rejected input text is retained.

## Persistence and recovery

### Successful write

After `SetRegisteredResult`:

- actual persistence `Changed` decides the typed mutation outcome;
- effective/source values are re-read;
- the session returns to the bound detail screen;
- the original Settings message is edited with the fresh revision.

### Persistence no-op

If the explicit user scope already contains the canonical submitted value:

- no repository write is reported;
- outcome is `noop`;
- the session still advances and renders fresh buttons.

### Persistence failure

If persistence fails:

- the consumed claim is not reused;
- a fresh claim is armed with a new revision;
- the input screen reports a generic save failure;
- the user may resend the value.

If that recovery render also fails, the claim is released and the transport tells the user to reopen Settings.

### Persistence success, render failure

If persistence commits and the detail edit fails:

- the committed value remains authoritative;
- input is not re-armed;
- the old input UI cannot replay the write;
- the returned `MutationError` has `Committed=true`;
- Assistant ingress sends a separate safe message telling the user the setting was saved and Settings should be reopened.

The update handler does not return this handled error to Telegram update processing, avoiding unsafe automatic replay of a consumed free-form mutation.

## Sensitive values

Sensitive input is never:

- echoed in the input prompt;
- included in recovery notices;
- logged by the P5-E handler;
- exposed through typed `MutationResult` fields.

`Previous`, `Persisted`, and `Effective` are redacted before they leave the handler.

## Compatibility

Legacy a1 Settings input remains available through Classic menu as a compatibility fallback.

The new a2 claim path has precedence only when a matching P1 input claim exists. Otherwise the existing legacy text handler and normal command router behave as before.

## Resource behavior

P5-E adds:

- no background goroutine;
- no ticker;
- no polling;
- no feature-global conversation map;
- no retained input value;
- no callback payload containing user text.

The additional runtime index contains at most one entry per live session.

## Tests

P5-E covers:

- bounded one-shot actor+chat claim;
- revision consumption on arm and take;
- duplicate actor+chat claim failing closed;
- lazy input TTL expiry;
- state transition clearing claims;
- explicit release after prompt-render failure;
- plugin-generation cleanup removing claims;
- orchestration target reattachment for separate text updates;
- string input success end-to-end;
- slash commands not consuming the claim;
- invalid/empty input re-arming the same session;
- `/cancel` without persistence;
- persistence failure re-arming;
- schema replacement while input is pending failing closed;
- render failure after commit not re-arming;
- sensitive result redaction.

## Deferred work

P5-E intentionally does not migrate arbitrary feature conversations.

A later generic feature-input phase may build typed multi-step forms on the same runtime claim primitive, but should not introduce another conversation subsystem.
