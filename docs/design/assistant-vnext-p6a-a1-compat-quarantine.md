# Assistant vNext P6-A — legacy a1 compatibility quarantine

## Status

P5-G completed normal-path Assistant shell parity on a2. P6-A begins global a1 reclamation without deleting compatibility infrastructure that is still owned by real plugin workflows.

The audit result is explicit:

- the current Assistant Home, Status, detailed Help/module/command browser, Settings, free-form Settings input, Close, and public `/start` paths are a2-native;
- new shell views no longer emit Classic-menu buttons;
- among all built-in modules, **MyXL is the only remaining consumer of `Runtime.LegacyAssistantMenu`**;
- MyXL still owns a substantial `a1:myxl:*` callback surface, menu-instance ownership checks, and multi-step text wizards.

Therefore P6-A quarantines the legacy shell instead of deleting `internal/assistant/menu`.

## No new legacy shell sessions

The technical `/start` fallback no longer renders `BuildStartScreen` and no longer registers a `MenuInstance`.

If the a2 foundation is unavailable, the fallback sends a static recovery message asking the user to retry `/start` after recovery.

This means infrastructure failure cannot create a fresh legacy shell that extends the lifetime of a1.

## Old a2 Classic-menu tokens

`ActionLegacy` remains registered temporarily because already-issued a2 messages may still contain that token.

It no longer performs an a2 -> a1 handoff.

Instead it transitions the existing P1 session back to the current a2 Home screen and acknowledges that the Classic menu has retired.

A regression test verifies this path does not create an a1 `MenuInstance`.

## Old a1 shell callbacks

The legacy `assistant` callback namespace remains registered only so already-visible old buttons fail gracefully.

Navigation actions such as:

```text
assistant:start
assistant:settings
assistant:help
assistant:help_page
assistant:help_module
assistant:help_module_page
assistant:help_command
assistant:status
assistant:ping
```

now validate the original menu ownership/session and render one retirement tombstone directing the user to `/start`.

They no longer rebuild the legacy shell navigation tree.

`assistant:close` remains functional because plugin-owned legacy surfaces, notably MyXL, still use it to close a compatibility message.

## Narrow compatibility capability

Application/module composition no longer exports concrete `*menu.Controller`, and the transitional runtime field is explicitly named `LegacyAssistantMenu`.

It exposes only:

```go
type CompatibilityHost interface {
    RegisterTextHandler(TextHandler)
    Instances() InstanceStore
    RegisterInstance(MenuInstance)
}
```

Those are the capabilities still required by MyXL:

- register the MyXL wizard text handler;
- validate menu-instance ownership for a1 callbacks;
- register messages produced by MyXL screens.

New modules must not depend on this compatibility interface.

The Assistant new-message hot path similarly depends on a private narrow `legacyTextInputDispatcher` interface rather than the complete legacy menu compatibility host.

## MyXL is the remaining blocker

A full audit of built-in module registration found `Runtime.LegacyAssistantMenu` used only by `plugins/myxl/module.go`.

MyXL cannot be removed from a1 mechanically because its current implementation still includes:

- OTP login wizard;
- alias/family/option-code/custom-price text input;
- menu instance ownership validation;
- callback state for package selection and purchase flows;
- many `a1:myxl:*` screens and actions.

Deleting the controller/router before migrating those flows would be a functional regression.

## Reclamation sequence

After P6-A the safe sequence is:

1. migrate MyXL interaction declarations into P0 FeatureSpec;
2. move MyXL retained/wizard state into bounded P1 input/session state;
3. express MyXL buttons as typed P2 action IDs;
4. execute MyXL callbacks and text input through P3 orchestration;
5. remove `Runtime.LegacyAssistantMenu` / `CompatibilityHost`;
6. remove legacy text dispatch from Assistant `OnNewMessage`;
7. expire/remove old a1 callback routes and menu instance store;
8. finally delete dead shell renderer/builders and the legacy Assistant callback package if no plugin references remain.

Reclamation remains reference-driven. P6-A intentionally does not mass-delete `internal/assistant/menu`.
