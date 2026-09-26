# P2-B — Common userbot localization refinement

Date: 2026-09-27  
Branch: `test-next`  
P2-A handoff baseline: `b948c54760e7125c756bf1fa001da2fc0cf692ac`  
P2-B HEAD discovered at refresh: `28fac5ceae86025d20a871280f83edb3275eac45`  
Final code acceptance baseline before documentation: `0fd0d08314afe0689486aecd7ca5c507475ac3f6`

## Scope

P2-B expands common userbot localization while preserving the existing settings/localization authority. It does not redesign presentation, command execution, interaction state, TaskEngine, Telegram RPC behavior, or metrics.

The branch had already advanced by thirteen P2-B commits before this continuation began. Current source was therefore audited first and treated as authoritative.

## Canonical locale authority

```text
settings.Service
    ui:locale
        ↓
localization.ResolveLocale(ctx, settingsService, userID, chatID)
        ↓
localization.CanonicalLocale
        ↓
localization.Bind(shared Localizer, locale)
        ↓
core.Context.Localizer
        ↓
ctx.T(...)
```

English is the deterministic default/fallback. Indonesian is the second built-in locale. Unsupported locale variants normalize to the bounded built-in vocabulary.

The shared `Localizer` is not mutated per invocation; `Bind` creates an immutable locale-specific translation view.

## Existing P2-B work found at refresh

The pre-existing chain already delivered:

- centralized locale binding;
- per-command userbot locale resolution;
- canonical `ui:locale` handling;
- common EN/ID catalog;
- Assistant reuse of common vocabulary;
- Help localization;
- Settings shared/native localization;
- Downloader locale binding and interactive localization;
- removal of manual Assistant locale branches;
- initial P2-B architecture fences.

Pre-existing commits:

```text
50b42ea080  refactor(localization): centralize locale binding
3c8288b58c  feat(telegram): resolve locale per command invocation
370dd50585  fix(localization): honor canonical ui locale in userbot
be3c0f85b9  fix(localization): disambiguate core locale type
c9b3f6ddba  feat(localization): add common userbot ux catalog
8b5bcdab36  refactor(assistant): reuse common localization vocabulary
86c67b7ec3  feat(help): localize common help ux
4044243dce  feat(settings): localize shared settings vocabulary
1fa5f46c4d  feat(settings): localize native interaction ux
b0f7721b74  refactor(downloader): bind canonical locale settings
40ee5eb6b0  feat(downloader): localize interactive lifecycle ux
83fe7597d7  refactor(assistant): remove manual locale branches
28fac5ceae  test(localization): fence canonical p2-b authority
```

## Residual closure in this continuation

The audit identified four remaining common-userbot surfaces with substantial hardcoded UX:

1. Settings CLI;
2. Admin/moderation;
3. Media;
4. Profile/contacts/dialogs.

They now use the invocation-local translator for user-visible common UX. Operational logic remains unchanged.

Continuation commits:

```text
27bd75d5c9  feat(localization): localize settings cli ux
f290a5f703  feat(localization): localize admin moderation ux
e9f9a0051d  feat(localization): localize media userbot ux
3484891f4e  feat(localization): localize profile userbot ux
005944d883  test(localization): bind canonical locale in plugin fixtures
0fd0d08314  test(localization): close common userbot p2-b gaps
```

## Catalog acceptance

The added userbot catalog is loaded once by the canonical localization service.

At final source acceptance:

```text
userbot EN keys: 136
userbot ID keys: 136
EN-only keys:    0
ID-only keys:    0
duplicate keys:  0
```

The broader built-in catalog parity test compares the complete English and Indonesian maps after all built-in catalogs are loaded.

Representative tests verify that Settings, Admin, Media, and Profile keys resolve in both locales and do not fall back to raw keys.

## Architecture/resource invariants

P2-B preserves these constraints:

- `internal/presentation` remains localization/settings agnostic;
- settings remains the locale selection authority;
- Assistant and native/userbot surfaces share canonical locale identity/resolution;
- no second localization registry or state authority exists;
- no locale worker, ticker, polling loop, queue, or unbounded cache was introduced;
- metrics remain localization-independent and do not use localized dynamic labels;
- per-invocation locale binding does not mutate shared global locale;
- interaction-local locale state remains bounded where interactive flows already retain it.

## Test fixture correction

Production dispatcher contexts carry an invocation-local translator. Several standalone plugin unit tests constructed `core.Context` manually and therefore would have returned raw localization keys after migration.

Admin, Media, Profile, and Settings test contexts now explicitly use:

```go
localization.New(localization.DefaultLocale)
```

This keeps unit fixtures aligned with production behavior and preserves English assertions.

## P2-C separation

P2-B does not claim that all raw/internal error exposure is fixed. For example, some Settings CLI paths still escape and display underlying error text. That is intentionally classified as **P2-C response/error modernization debt**, not localization debt.

Do not reopen P2-B merely to perform P2-C sanitization.

## Verification constraints

No CI was inspected.

The current environment still lacks a complete executable repository checkout because the container could not resolve GitHub. Therefore this continuation does **not** claim `gofmt`, `go build`, `go test`, `go vet`, race tests, or benchmarks executed.

Connector-written Go test struct literals are syntactically valid but have not been locally normalized by `gofmt`. A later executable session should format/test them before relying on toolchain acceptance.

## Closure

**P2-B: CLOSED.**

Next phase, only after explicit user confirmation:

**P2-C — repo-wide response/error modernization.**

Stop before P2-D.
