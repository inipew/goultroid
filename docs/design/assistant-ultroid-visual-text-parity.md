# Goultroid — Ultroid Visual/Text/Interaction Parity

Status: **active post-P8 product presentation work**

Repository: `inipew/goultroid`

Branch: `test-next`

Goultroid baseline inspected before this work:

`52978381b7b3fa4b8b293259774ac8ae0c2b511c`

Current branch at document creation:

`2c11ce8124357648f445d0e480d594dd17523cb4`

Ultroid reference:

`TeamUltroid/Ultroid@edd18d31eae982dff468bd8ce785fbb319e370c3`

This document is about **visible UX parity**: wording, emoji, button topology, ordering, navigation affordance, result composition, media/thumbnail presentation, and user-visible state transitions.

It does **not** reopen P8 architecture parity and does not authorize copying Ultroid's global callback/state architecture.

---

## 1. Architectural rule

The target is:

```text
Ultroid-facing presentation
        +
Goultroid-native state/lifecycle/execution
```

Frozen Goultroid authorities remain:

```text
core.Router
feature.Registry
interaction.Runtime
interaction.Dispatcher
interaction/orchestration.Engine
presentation.View
Inline vNext
TaskEngine
telegram.RPCExecutor
selfinline.Renderer
shared settings/localization/storage/media services
```

Visual parity must never introduce:

```text
raw callback_data protocols
global STUFF/CALC/BACK_BUTTON maps
per-user conversation goroutines
feature-owned Telegram retry/FloodWait
second Assistant command registry
second TaskEngine
second Inline engine
per-provider permanent worker
unbounded presentation cache
```

---

## 2. Source material audited

Ultroid source inspected in detail:

```text
assistant/start.py
assistant/callbackstuffs.py
assistant/inlinestuff.py
assistant/pmbot.py
assistant/ytdl.py
plugins/_inline.py
plugins/_help.py
plugins/calculator.py
strings/strings/en.yml
strings/strings/id.yml
```

Goultroid production counterparts inspected:

```text
internal/assistant/shell/shell.go
internal/assistant/shell/help.go
internal/assistant/shell/settings.go
internal/assistant/shell/inline.go
internal/assistant/shell/locale.go
internal/services/localization/assistant_catalog.go

plugins/calculator/calculator.go
plugins/downloader/interactive.go
plugins/wikipedia/wikipedia.go

internal/assistant/client/shell_interaction.go
internal/assistant/client/interaction_help.go

internal/interaction/*
internal/interaction/orchestration/*
internal/presentation/*
internal/presentation/selfinline/*
internal/services/inline/*
```

---

# 3. Ultroid's actual presentation grammar

Ultroid does not use one completely uniform visual system. Its strongest recurring UX conventions are nevertheless clear.

## 3.1 Action-first screens

The text is short.

The keyboard carries most navigation.

Typical screen:

```text
short title / one-line instruction

[ primary ] [ primary ]
[ secondary ] [ secondary ]
[        Back        ]
```

Ultroid generally avoids exposing architecture terms such as:

```text
canonical registry
revision fence
interaction runtime
TaskEngine
retained asset
transport boundary
generation owner
```

Those concepts belong in logs/docs, not normal user-facing text.

## 3.2 Small-caps button typography

Frequently used visual language:

```text
Sᴛᴀᴛs ✨
Bʀᴏᴀᴅᴄᴀsᴛ 📻
TɪᴍᴇZᴏɴᴇ 🌎
« Bᴀᴄᴋ
Pʀᴇᴠɪᴏᴜs
Nᴇxᴛ
Oᴘᴇɴ
Dᴇᴛᴀɪʟs
Uᴘᴅᴀᴛᴇ
Pɪɴɢ
```

Not every button is stylized. High-information configuration labels such as `API Keys`, `PM Bot`, `PMPermit`, and `Features` often remain normal text.

## 3.3 Bottom Back affordance

A large fraction of nested Ultroid screens terminate with a dedicated bottom row:

```text
[ « Bᴀᴄᴋ ]
```

This is more important than reproducing exact capitalization: users can reliably predict how to leave a nested screen.

## 3.4 Compact alerts for transient facts

Ultroid often uses callback alerts instead of opening another full screen for:

```text
Ping
Uptime
simple errors
calculator answer
small status facts
```

Goultroid may retain screen-based status where it provides more useful information, but transient actions should remain compact.

## 3.5 Media-rich provider results

Where a provider naturally has imagery, Ultroid presents the image together with metadata and actions.

The clearest example is YTDL:

```text
thumbnail
title/link
description
duration
views
publisher
publish date

[ Audio ] [ Video ]
[ Search Again ] [ Share ]
```

The image is part of the product result, not decoration.

---

# 4. Owner /start flow — micro behavior

Ultroid source: `assistant/start.py`.

## 4.1 Owner root

Owner opens the Assistant:

```text
/start
  ↓
owner principal
  ↓
owner root message
  ↓
3-row action keyboard
```

Visible keyboard:

```text
[ Language 🌐 ] [ Settings ⚙️ ]
[ Sᴛᴀᴛs ✨ ] [ Bʀᴏᴀᴅᴄᴀsᴛ 📻 ]
[       TɪᴍᴇZᴏɴᴇ 🌎       ]
```

English message intent:

```text
Hey <owner>. Please browse through the options
```

Indonesian catalog carries the same short action-oriented intent.

### Important UX characteristic

The root does **not** lead with uptime, engine, callback runtime, or implementation status.

It says hello and presents choices.

### Goultroid mapping

Goultroid owner Home must remain an a2 screen.

Current desired topology, constrained by currently available canonical capabilities:

```text
[ Language 🌐 ] [ Settings ⚙️ ]
[ Sᴛᴀᴛs ✨ ] [ Help ]
[ Pɪɴɢ ]      [ Refresh ]
```

Broadcast and timezone should only become direct root buttons if there is a canonical Goultroid capability/action to invoke. A visual button must never be added merely for parity if it has no typed execution target.

---

# 5. Owner Stats

Ultroid:

```text
Sᴛᴀᴛs ✨
    ↓
callback alert

Ultroid Assistant - Stats
Total Users - N
```

It is intentionally tiny.

Goultroid currently has richer runtime status information.

Recommended parity compromise:

```text
GoUltroid Assistant - Stats

Assistant - @bot
Status - Online
Uptime - ...
Engine - ...
Callbacks - Active

[ Rᴇғʀᴇsʜ ]
[ « Bᴀᴄᴋ ]
```

This keeps Goultroid diagnostics that are genuinely useful while adopting Ultroid's compact presentation and predictable back navigation.

---

# 6. Owner Settings flow

Ultroid `/start set` or `Settings ⚙️` opens:

```text
Choose from the below options -

[ API Keys ] [ PM Bot ]
[ Alive ]    [ PMPermit ]
[ Features ] [ VC Song Bot ]
[       « Back       ]
```

The exact settings taxonomy is Ultroid-specific and must not be forced onto Goultroid if Goultroid's actual settings registry has different domains.

## 6.1 Ultroid Settings subtrees observed

### Features

Representative layout:

```text
Tᴀɢ Lᴏɢɢᴇʀ | SᴜᴘᴇʀFʙᴀɴ
Sudo Mode   | Handler
Extra Plugins | Addons
Emoji in Help | Set gDrive
Inline Pic | Sudo HNDLR
Dual Mode
« Back
```

### PMPermit

```text
Tᴜʀɴ PMPᴇʀᴍɪᴛ Oɴ
Tᴜʀɴ PMPᴇʀᴍɪᴛ Oғғ
Cᴜsᴛᴏᴍɪᴢᴇ PMPᴇʀᴍɪᴛ
« Bᴀᴄᴋ
```

Customization subtree:

```text
Pᴍ Tᴇxᴛ | Pᴍ Mᴇᴅɪᴀ
Aᴜᴛᴏ Aᴘᴘʀᴏᴠᴇ | PMLOGGER
Sᴇᴛ Wᴀʀɴs | Dᴇʟᴇᴛᴇ Pᴍ Mᴇᴅɪᴀ
PMPermit Type
« Bᴀᴄᴋ
```

### PM Bot

```text
Cʜᴀᴛ Bᴏᴛ Oɴ | Cʜᴀᴛ Bᴏᴛ Oғғ
Bᴏᴛ Wᴇʟᴄᴏᴍᴇ | Bᴏᴛ Wᴇʟᴄᴏᴍᴇ Mᴇᴅɪᴀ
Bᴏᴛ Iɴғᴏ Tᴇxᴛ
Fᴏʀᴄᴇ Sᴜʙsᴄʀɪʙᴇ
« Bᴀᴄᴋ
```

## 6.2 Goultroid implementation rule

Goultroid settings remain generated from the canonical Settings registry.

Do **not** create an Ultroid-shaped second settings tree.

Presentation changes that are safe:

- short root wording;
- category labels with icons;
- consistent `Back`;
- two-column choices where cardinality allows;
- remove architecture wording from footer;
- mutation result shown briefly;
- sensitive values remain masked.

Structural parity requiring direct arbitrary category buttons must use typed/session-bound actions, not callback strings.

---

# 7. Help flow

Ultroid Help root is not a diagnostic browser. It is a compact feature chooser.

Observed root topology:

```text
[ • Plugins ] [ Addons • ]
[ •• Voice Chat ] [ Inline Plugins •• ]
[ ⚙️ Owner Tools ] [ Settings ⚙️ ]
[          ••Cʟᴏꜱᴇ••          ]
```

Depending on runtime configuration, a Manager Help entry may also appear.

## 7.1 Plugin list

Plugins are paginated as a button grid.

Footer navigation:

```text
[ « Pʀᴇᴠɪᴏᴜs ] [ « Bᴀᴄᴋ » ] [ Nᴇxᴛ » ]
```

## 7.2 Plugin detail

The detail screen displays help text for the selected plugin.

Optional action:

```text
[ « Sᴇɴᴅ Pʟᴜɢɪɴ » ]
[ « Bᴀᴄᴋ ]
```

## 7.3 Owner tools from Help

```text
[ •Pɪɴɢ• ] [ •Uᴘᴛɪᴍᴇ• ]
[ •Stats• ] [ •Uᴘᴅᴀᴛᴇ• ]
[         « Bᴀᴄᴋ         ]
```

## 7.4 Close state

Ultroid can replace the Help content with a closed state exposing:

```text
[ Oᴘᴇɴ Aɢᴀɪɴ ]
```

## 7.5 Current Goultroid difference

Goultroid Help is generated from canonical command/catalog metadata and currently navigates one selected module/command through typed Previous/Open/Next actions.

That is architecture-safe but visually different from Ultroid's direct grid.

### Exact direct-grid parity requires

A typed session-bound action-slot mechanism:

```text
render visible module buttons
        ↓
each button resolves to a bounded session slot
        ↓
slot stores small module identity/index
        ↓
callback remains a2
        ↓
current generation/actor/target/revision revalidated
        ↓
open module
```

Do not encode arbitrary category names into raw callback bytes.

Until that direct-slot UI is implemented, Goultroid should keep the typed carousel but use Ultroid-facing labels:

```text
« Pʀᴇᴠɪᴏᴜs | Oᴘᴇɴ | Nᴇxᴛ »
« Bᴀᴄᴋ
```

---

# 8. Default inline flow

Ultroid no-query inline result:

```text
@assistant
   ↓
default rich result
   ↓
Ultroid Userbot presentation
   ↓
Repo / Support buttons
   ↓
switch-PM portal affordance
```

Ultroid Help inline is a separate result using the same Help menu.

## Goultroid target

Current Goultroid inline root already has a canonical root/help/ping split.

Visual target:

```text
GoUltroid Userbot

status / small hint

[ • Help • ] [ •Pɪɴɢ• ]
```

Provider/branding URL buttons should only be added if they are canonical project links/configuration, not copied blindly from TeamUltroid.

A default photo/thumbnail can be added only after deciding one stable Goultroid-owned asset and ensuring local/remote inline media handling remains bounded.

---

# 9. Calculator — exact micro flow

Ultroid source: `plugins/calculator.py`.

## 9.1 Keypad

Exact visible topology:

```text
[ AC ] [ C ] [ ⌫ ] [ % ]
[ 7  ] [ 8 ] [ 9 ] [ + ]
[ 4  ] [ 5 ] [ 6 ] [ - ]
[ 1  ] [ 2 ] [ 3 ] [ x ]
[ 00 ] [ 0 ] [ . ] [ ÷ ]
[          =          ]
```

Base message intent:

```text
• Ultroid Inline Calculator •
```

## 9.2 Interaction behavior

```text
digit/operator
    ↓
update expression

⌫
    ↓
remove last token/character

C
    ↓
clear expression

=
    ↓
evaluate
    ↓
callback alert:
Answer : <result>
```

Ultroid stores calculator mutable state in a global map and evaluates an expression dynamically.

Goultroid must **not** copy either mechanism.

Goultroid equivalent:

```text
same keypad
    ↓
a2 typed ActionID
    ↓
bounded interaction session state
    ↓
safe recursive-descent evaluator
    ↓
revision-fenced transition
```

`00` is represented by a typed `key_00` action.

`AC` and `C` may share the same safe clear transition unless a later UX requirement justifies a separate typed semantic.

---

# 10. YouTube / interactive downloader — exact flow

Ultroid source: `assistant/ytdl.py`.

This is the clearest example where visual parity and missing product behavior overlap.

## 10.1 Empty query

```text
yt
  ↓
result title:
Search Something

text:
YᴏᴜTᴜʙᴇ Sᴇᴀʀᴄʜ
You didn't search anything

[ Sᴇᴀʀᴄʜ Aɢᴀɪɴ ]
```

## 10.2 Search query

```text
yt <query>
  ↓
search provider
  ↓
up to bounded result list
  ↓
each result contains:
    thumbnail
    title + URL
    description
    duration
    views
    publisher
    published date

[ Audio ] [ Video ]
[ Sᴇᴀʀᴄʜ Aɢᴀɪɴ ] [ Sʜᴀʀᴇ ]
```

Ultroid can emit up to 50 inline results.

Goultroid should use a much smaller explicit product bound unless a measured UX requirement proves 50 necessary.

## 10.3 Format selection

```text
Audio/Video
   ↓
resolve formats
   ↓
edit current inline message

Select Your Format.

[ format buttons ... ]
```

## 10.4 Final delivery

Ultroid downloads and then sends/uploads the selected media.

Final message contains media plus provider metadata such as:

```text
Title
Description
Duration
Artist
Views
Likes
Size

[ Search More ]
```

## 10.5 Back behavior

Ultroid uses a process-global `BACK_BUTTON` map to restore old search presentation.

Goultroid must not copy this.

Goultroid equivalent should store only bounded workflow identity/state in the existing a2 session.

## 10.6 Current Goultroid state

P8-E currently starts from:

```text
dl <URL>
   ↓
provider resolution
   ↓
Audio / Video
   ↓
format
   ↓
TaskEngine
   ↓
retained asset
```

Current visual work can safely match:

- Audio / Video labels;
- compact `Select Your Format.` screen;
- compact downloading state;
- non-technical completion text;
- consistent Cancel/Back typography.

The following cannot be achieved by cosmetic work alone:

```text
yt <search query>
search result thumbnail
provider metadata
Search Again
Share
automatic delivered media
```

Those belong to the search-first downloader and canonical media-delivery product phases.

---

# 11. PM Bot / relay flow

Ultroid `assistant/pmbot.py` behavior:

```text
visitor DM
   ↓
optional force-sub gate
   ↓
forward/copy to owner
   ↓
owner-side marker:
From <user> [id]
   ↓
mapping retained
   ↓
owner replies
   ↓
/who or normal reply
   ↓
Assistant sends to visitor
```

Owner controls also include ban/unban semantics.

Visible user text is deliberately short.

Force-sub failure returns join guidance/buttons.

Ban/unban feedback uses terse state-oriented text.

## Goultroid mapping

P6 already provides durable relay mapping, audience registry, block policy, force-sub, and bounded delivery.

Visual work should not modify that domain model.

Parity work should focus on:

- concise relay headers;
- clear visitor identity;
- compact block/unblock confirmation;
- join guidance buttons;
- no persistence/recovery terminology exposed to users.

---

# 12. Visual parity classification

Every visual difference must be assigned to one of these classes.

## Class A — exact-safe presentation parity

Can be changed without new behavior/state.

Examples:

```text
button wording
emoji
small-caps style
row ordering
Back placement
screen title
shorter user-facing copy
remove architecture jargon
calculator keypad topology
downloader Audio/Video labels
format prompt
completion wording
```

These should be implemented directly.

## Class B — structural parity using existing typed state

Same behavior exists but current Goultroid screen shape differs.

Examples:

```text
Help module direct-grid vs carousel
Settings category direct-grid vs navigator
Close → Open Again state
owner-tools shortcut screen
```

Implement only through a2/session-bound typed actions.

## Class C — blocked by missing product capability

Cannot be solved by text/layout.

Examples:

```text
YouTube search-first results
provider thumbnail metadata
Search Again / Share
automatic media upload after downloader completion
Broadcast root action if no canonical root action binding exists
Timezone root action if no canonical typed interaction exists
public Owner Info button if no public owner-info capability exists
```

These belong to separate product feature work.

---

# 13. First implementation wave applied

The first production wave intentionally targets Class A.

Commits created during this work include:

```text
57409cae
feat(assistant): align shell presentation with Ultroid UX

cd10177f
feat(calculator): match Ultroid inline keypad presentation

cc512fd6
feat(downloader): align interactive presentation with Ultroid UX

f5e8b5d6
feat(assistant): adopt Ultroid-facing labels and navigation text
```

Presentation regression tests were updated in subsequent focused commits.

## 13.1 Owner Home

Changed from a diagnostic card to short greeting + action grid.

Current target:

```text
GoUltroid Assistant

Hey @bot. Please browse through the options

Language / Settings
Stats / Help
Ping / Refresh
```

Indonesian greeting:

```text
Haloo @bot. Silakan telusuri opsi
```

## 13.2 Stats

Changed from a card-heavy diagnostic surface to compact lines plus:

```text
Rᴇғʀᴇsʜ
« Bᴀᴄᴋ
```

## 13.3 Shared labels

English adopts Ultroid-facing labels where meaningful:

```text
Settings ⚙️
Hᴇʟᴘ 📚
Sᴛᴀᴛs ✨
Rᴇғʀᴇsʜ 🔄
Pɪɴɢ 🌋
Language 🌐
« Bᴀᴄᴋ
« Pʀᴇᴠɪᴏᴜs
Nᴇxᴛ »
Oᴘᴇɴ
Dᴇᴛᴀɪʟs
```

Indonesian keeps translated semantics but adopts the same navigation hierarchy.

## 13.4 Calculator

Changed to the Ultroid keypad topology, including typed `key_00`.

No global calculator state was added.

## 13.5 Downloader

User-facing presentation no longer says:

```text
TaskEngine owns ...
Retained asset: /filesystem/path
```

The downloader now presents compact media choices, format selection, downloading state, and completion metadata while keeping the same TaskEngine/storage backend.

---

# 14. Required next visual work

## V2 — public /start presentation — COMPLETE

Implemented:

- compact visitor greeting aligned with Ultroid's public `/start` intent;
- Assistant username is HTML-escaped before presentation;
- PM relay guidance is shown only when the configured relay is live-enabled;
- relay enable/disable changes are reflected without rebuilding the Assistant shell;
- audience-touch semantics remain unchanged;
- plain visitor `/start` still creates zero a2 sessions;
- public start remains stateless text-only presentation;
- owner-info button remains intentionally absent because Goultroid has no canonical public owner-info capability yet.

Current flow:

```text
visitor /start
    ↓
owner-shell admission denied normally
    ↓
public start path
    ↓
read current locale
    ↓
read relay availability
    ↓
render compact greeting
    ↓
optional relay guidance
    ↓
touch audience source=start
```

No new worker, ticker, poller, global map, session, or callback protocol was introduced.

## V3 — Help direct-grid parity — COMPLETE

Implemented as a bounded typed-slot projection over the canonical Assistant command catalog.

Visible topology:

```text
GoUltroid Help Menu

[ Module 1 ] [ Module 2 ]
[ Module 3 ] [ Module 4 ]
[ Module 5 ] [ Module 6 ]
[ Module 7 ] [ Module 8 ]

[ « Previous ] [ « Back ] [ Next » ]
```

Opening a module uses the same bounded shape for commands:

```text
Module

[ /cmd1 ] [ /cmd2 ]
[ /cmd3 ] [ /cmd4 ]
[ /cmd5 ] [ /cmd6 ]
[ /cmd7 ] [ /cmd8 ]

[ « Previous ] [ « Back » ] [ Next » ]
```

Exact implementation constraints:

- module page size = 8;
- command page size = 8;
- maximum grid width = 2;
- fixed action vocabulary = 8 module slot IDs + 8 command slot IDs;
- no command/module identity is embedded into Telegram callback bytes;
- no dynamic callback registration;
- no second command/help registry;
- source data remains `core.Router` / canonical shell command projection;
- existing a2 actor/target/revision/generation validation remains authoritative.

The former carousel-only actions:

```text
help_open
help_cmd_open
```

were removed from the production interaction vocabulary. Existing Previous/Next actions now mean page navigation.

### Session-bound slot protection

Each rendered Help page stores a 128-bit fingerprint of the visible identities in the existing shell state binding bytes while `ScreenHelp` is active.

Flow:

```text
canonical commands
    ↓
deterministic sort/group
    ↓
bounded page
    ↓
fingerprint visible module/command identities
    ↓
store fingerprint in current a2 session state
    ↓
compile fixed typed slot ActionIDs
    ↓
callback
    ↓
normal a2 token validation
    ↓
rebuild canonical catalog
    ↓
recompute page fingerprint
    ↓
match?
    ├─ yes → slot may resolve
    └─ no  → ErrShellHelpSelectionStale
```

This prevents a button rendered for one module/command from silently selecting another item if the plugin/command catalog changes before the user clicks it.

No extra session bytes were added: Help reuses binding bytes that are otherwise unused on `ScreenHelp`; Settings mutation binding authority is cleared before the Help fingerprint is installed.

### Acceptance evidence

Added/updated source tests cover:

- deterministic two-column module grid;
- deterministic two-column command grid;
- module pagination beyond 8 entries;
- command pagination beyond 8 entries;
- direct command-slot detail transition;
- Back preserving the command page;
- stale old a2 token rejection after transition;
- catalog-remap rejection even when the a2 revision itself is still current;
- userbot-only commands remaining excluded from the Assistant projection;
- one bounded a2 session for the entire Help navigation flow;
- all 16 slot ActionIDs declared as owner/private Assistant actions.

Production commits:

```text
dbb0e65a852ccf30578ab05a89fab6efd5ad114c
feat(assistant): add bounded direct-grid help slots

94a4403f2e67db74638ee0ea04b11c6207aa52e5
perf(assistant): avoid duplicate help catalog sorting
```

`Close/Open Again` is not required for direct-grid closure and remains optional presentation work; it was not implemented by introducing a delete/reopen state machine merely for visual imitation.

## V4 — Settings direct-grid parity

Render visible categories/settings as direct buttons using typed slots.

Do not duplicate the Settings registry.

## V5 — inline root rich presentation

Decide a Goultroid-owned image/thumbnail asset and optional project/support links.

Do not hard-code TeamUltroid URLs.

## V6 — search-first downloader

Implement the actual missing provider/search flow.

This is not cosmetic.

## V7 — canonical downloaded-media delivery

Deliver retained asset via shared Telegram media delivery and remove filesystem-oriented completion UX entirely.

---

# 15. Acceptance matrix

## Home

- greeting compact;
- owner-only policy unchanged;
- Language before Settings;
- Stats visible;
- predictable Back from nested screens;
- no runtime architecture jargon.

## Help

- canonical feature catalog remains authority;
- direct visible actions are typed;
- stale revision rejected;
- old generation rejected;
- no arbitrary callback payload;
- module cardinality bounded.

## Settings

- central Settings registry remains authority;
- sensitive values remain masked;
- direct category/setting buttons use typed slots;
- mutation revalidation remains unchanged.

## Calculator

- keypad visual topology matches target;
- `key_00` bounded;
- no eval;
- no global map;
- stale/cross-user callbacks fail closed.

## Downloader

- Audio/Video and format flow compact;
- no TaskEngine/storage internals in normal user text;
- direct HTTP/extractor resource planning unchanged;
- cancellation unchanged;
- retained asset ownership unchanged.

## Inline

- no global result map;
- cache policy unchanged by cosmetics;
- thumbnail use must respect media bounds.

---

# 16. Resource and lifecycle invariant

Presentation parity must have approximately zero resource delta at idle.

Expected:

```text
new worker      = 0
new ticker      = 0
new poller      = 0
new retry loop  = 0
new global map  = 0
new session map = 0
```

Any future direct-grid Help/Settings implementation must use existing bounded interaction state/action-slot machinery.

---

# 17. Final implementation principle

Do not interpret “exact visual parity” as permission to copy Ultroid internals.

Correct transformation:

```text
Ultroid button/text/flow
        ↓
extract visible UX contract
        ↓
map to existing Goultroid capability
        ↓
if capability exists:
    render through presentation.View + typed actions
else:
    mark as product gap
        ↓
never emulate missing behavior with ad-hoc callback/global state
```

The goal is that a user familiar with Ultroid recognizes the Assistant's flow, wording hierarchy, navigation, and rich-result style, while Goultroid retains its stronger lifecycle, resource, security, and execution guarantees.
