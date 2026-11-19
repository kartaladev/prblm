# Proposal

## Why

An error whose own methods panic can still crash a prblm request in v0.2.0, though a panicking mapper, describer, enricher or hook cannot:

- **Core, status hint.** An `As` method that panics escapes `Handler.Respond`. So does an `As` method that hands back a nil status hint. The status-hint lookup is the only resolution step not run through `safeCall`. Through `Wrap`, net/http then drops the connection: the client gets no response, and the hooks never hear about the error.
- **net/http `Recover`.** A handler that panics with an error value whose `Is` method panics escapes `Recover` itself. The broken-connection check walks the panic value before `Recover`'s fallback is armed.
- **gin `Recovery`.** It has the same defect, in its own broken-connection check.

These errors usually come from dependencies, not from the consumer's own code. Reproductions: the research probes `TestPanic_A_AsMethod_Respond`, `TestPanic_A_AsMethod_NetHTTP`, `TestTypedNil_StatusErrorViaAs` (`WithoutBuiltins`), `TestRecover_HostilePanicValue` and `TestGinRecovery_HostilePanicValue`. The scrty consumer is waiting on this fix.

## What Changes

- **Core: status hint contained.** The status-hint step of resolution is contained like every other step. A panic raised while the error is inspected for a status hint is treated as "no hint", is reported to the hooks as a `*PanicError`, and the request gets the safe 500 problem.
- **Core: `Recover`'s broken-connection check contained.** A panic raised while that check inspects the panic value is treated as "not broken", and is reported to the hooks. The client gets the safe 500 problem, and the hooks receive the original panic, as for any other panic.
- **gin (`adapters/ginprblm`): the same containment in `Recovery`'s broken-connection check.**
- **No public API change and no new option.** These are patch releases: core v0.2.1 and `adapters/ginprblm` v0.2.1. The gin fix doesn't depend on the core fix. ginprblm keeps requiring core v0.2.0.

## Defaults and Customization

- **Zero configuration.** A request whose error has panicking `Is`/`As` methods, or a handler that panics with such a value under `Recover`/`Recovery`, is answered with the safe 500 problem: status 500, title "Internal Server Error", no detail. The response carries no error text, panic value or stack trace.
- **Reporting.** The panic reaches the hooks as a `*PanicError`. With no hook it is silently contained, as panics in consumer code already are.
- **Options.** Nothing new to opt into, and no safety default changes.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `error-mapping`: a new requirement. An error value whose methods panic during resolution never breaks the request. The step that met the panic counts as no match, and the panic is reported.
- `panic-recovery`: "Recovery never panics" also covers a panic raised while recovery inspects the panic value.
- `gin-adapter`: "Opt-in recovery independent of order" gains the edge case of a panic value whose `Is` method panics.

## Scope by Gall's Law

The simplest version that works end to end: put the two unguarded calls in the core under the existing `safeCall`, and guard the one call in gin. Each is a few lines on an already-tested path, and each slice ships on its own.

Deliberately left for a later change (v0.3.0):

- **Cyclic error chains.** They still hang (`Unwrap() error`) or crash the process with a stack overflow (`Unwrap() []error`).
- **A single bounded walk per error**, with a total-links budget.
- **Skipping typed nils.** A nil `*Problem` ahead of a real one still gives a 500; a nil `*http.MaxBytesError` still gives a 413.
- **A header-replace opt-in** for `Cache-Control`.
- **fiber.** An `As` method that panics answers a bare 500 without a hook call, from the adapter's `withHint`. It neither escapes nor leaks, so it waits for the bounded walk.
- **gin's own `Context.Error`.** It walks the error before prblm does. That belongs to gin, and gin's `Recovery` already answers it with the 500.

## Impact

- **Code.**
  - `handler.go`: `match` (the status-hint step) and `recoverPanic` (the broken-connection check).
  - `adapters/ginprblm/recovery.go`: `recoverPanic`.
- **Docs.** The `doc.go` package godoc (which panics are contained), `CHANGELOG.md` and `adapters/ginprblm/CHANGELOG.md`.
- **Tests.**
  - `hook_test.go` / `handler_test.go`: resolution.
  - `http_test.go`: `Recover`.
  - `adapters/ginprblm`'s recovery tests.
- **No public API, dependency or default change.** Released as core v0.2.1 and `adapters/ginprblm` v0.2.1.
