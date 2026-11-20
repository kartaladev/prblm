# Proposal

## Why

The archive of `bounded-error-walk` merged five requirements whose text is over 500 characters, so `openspec validate --specs --strict` now fails on four main specs. Each of the five states several behaviors in one paragraph. That makes the requirement hard to check against its scenarios, and the warning hides any new one.

## What Changes

Every requirement ends up stating one behavior in 500 characters or fewer, and each scenario moves, unchanged, with the behavior it tests. No rule changes meaning.

- `error-mapping` / "Error values with panicking methods never break resolution" and `validation-error-mapping` / "Translator panics are contained once": the text is tightened below 500 characters (MODIFIED). Each stays one requirement with its scenarios, because a split would have to move Examples rows between scenarios.
- `fiber-adapter` / "Fiber errors are status hints without their message" is split into "Fiber error codes are status hints" and "Errors the adapter cannot walk get no fiber hint".
- `fiber-adapter` / "Opt-in recovery returns panics to the error handler" is split into "Opt-in recovery turns panics into errors" and "A recovered panic is never a route-not-found error".
- `problem-response` / "Response headers" is split into "Default response headers", "Replaced headers win over existing ones" and "Every spelling of a header name is that header".

A split requirement is REMOVED and its parts ADDED under new names. OpenSpec does not allow the alternative: a MODIFIED block cannot drop scenarios, even into an ADDED requirement, and one name cannot be both REMOVED and ADDED. So the three split requirements change names.

No behavior, API, default or code changes. Scenario names are kept, so tests citing `<capability> / Scenario: <name>` still resolve. One test comment cites a split requirement by name (`adapters/fiberprblm/handler_test.go`, "Fiber errors are status hints without their message"), and it is updated to the new name.

## Defaults and Customization

Unchanged. With zero configuration a consumer still gets the safe 500 problem for an error that cannot be walked or whose methods panic, with no error text on a 5xx, no stack trace and no database internals in the response. No option is added, and no safety default is loosened.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

Only the wording and structure of requirements change, not the behavior they describe. A delta spec is how a change edits the main specs, so each capability below has one.

- `error-mapping`: "Error values with panicking methods never break resolution" is tightened.
- `fiber-adapter`: two requirements are split into four, under new names.
- `problem-response`: "Response headers" is split into three, under new names.
- `validation-error-mapping`: "Translator panics are contained once" is tightened.

## Scope by Gall's Law

The simplest version that works: the five flagged requirements become requirements of one behavior each, and `openspec validate --specs --strict` passes. Other long but unflagged requirements, and any rewording beyond what the split needs, are left alone.

## Impact

`openspec/specs/` (through the archive's sync), this change's artifacts, and one comment in `adapters/fiberprblm/handler_test.go`. No Go code, test logic, module, dependency or public API changes, and no module is released.
