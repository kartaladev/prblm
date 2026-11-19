# Design

## Context

`Handler.match` (handler.go:134) runs these resolution steps:

1. the Describer lookup;
2. scoped mappers;
3. consumer mappers;
4. built-in mappers;
5. the status hint.

Steps 1–4 run through `safeCall` (panic.go:36), which recovers a panic, reports it to the hooks as a `*PanicError` with a nil problem, and returns the zero value: "no match". Step 5, `hinted(err)` (handler.go:151), calls `errors.AsType[*statusError]` (:885) directly. An `As` method that panics, or one that reports a match with a nil `*statusError`, therefore escapes `Respond`.

net/http `Recover` (`recoverPanic`, handler.go:510) calls `isBrokenConnection(pe)` (conn.go:13: `errors.Is` on the panic value, through `PanicError.Unwrap`) at :531. That call comes before it defers `writeFallback` at :539, so a panic from the value's `Is` method escapes `Recover`. gin's `recoverPanic` (adapters/ginprblm/recovery.go:69) calls its own `isBrokenConnection(err.pe)` after `respond`, outside any recover, with the same result.

See proposal.md for the motivation and the scope.

## Goals / Non-Goals

**Goals:**

- No panic raised by an error value's own methods escapes `Handler.Respond`, `Handler.Wrap`/`Write`, `Handler.Recover` or ginprblm's `Recovery`.
- Reuse the existing containment rule: the step that met the panic counts as no match, and the panic is reported as a `*PanicError`.

**Non-Goals:**

- Cycles and bounding the walk; typed-nil skipping; fiber's bare 500. These are proposal.md's later-change list.
- Making the hook call sequences identical across adapters. Today they already differ in order: gin responds before its broken-connection check, net/http after it.

## Slices

1. **Core: the status hint is contained.**
   - **What works at the end:** `match` runs the hint step through `safeCall`. Every row of `error-mapping` "Error methods panic" and "Error methods panic without a hook" passes through `Handler.Wrap`/`Write`.
   - **Hot path:** error resolution, status-hint branch. This adds its panic branch.
   - **Releasable alone.**
2. **Core: `Recover` contains a hostile panic value.**
   - **What works at the end:** `recoverPanic` runs the broken-connection check through `safeCall`. A panic there counts as "not broken" and the normal answer follows. Every row of `panic-recovery` "Hostile panic value" and "…without a hook" passes.
   - **Hot path:** panic recovery, the broken-connection branch.
   - **Releasable with slice 1 as core v0.2.1.**
3. **gin: `Recovery` contains a hostile panic value.**
   - **What works at the end:** ginprblm's `recoverPanic` guards its broken-connection check. A panic there counts as "not broken", is reported to the hooks through `respond` (as `guardWrite` already does), and the problem is written. Every row of `gin-adapter` "Hostile panic value under gin" passes.
   - **Hot path:** gin panic recovery.
   - **Independent of slices 1–2:** ginprblm keeps requiring core v0.2.0, and releases as `adapters/ginprblm` v0.2.1.

## Decisions

1. **Reuse `safeCall` in the core, rather than a new recover.**
   - **Rationale:** `safeCall` already carries the documented rule (no match, reported as `*PanicError`, stack captured only when a hook will read it). One rule for every step keeps resolution predictable.
   - **Alternative considered:** one `recover` around the whole of `match`. Rejected, because it would abort resolution at the first panic, before later steps such as the hint could still match. The spec row with `WithStatus` over a hostile error expects 404.
   - **Failure modes:**
     - A hook that panics while handling the report: already contained by `callOne`.
     - No hook: the panic is contained silently, as for consumer code (spec row "without a hook").
     - The hint step panics after an earlier step already panicked: each panic is reported separately ("one or more panic errors" in the spec).
2. **In `Recover`, a panicking broken-connection check counts as "not broken".**
   - **Rationale:** failing closed means answering. If the connection really is gone, the write fails harmlessly; if it isn't, the client gets the safe 500.
   - **Alternative considered:** arm `writeFallback` before the check. Rejected, because `writeFallback` reports only the second panic and writes the constant minimal body, so the hooks would never receive the original panic.
   - **Failure modes:**
     - The check panics: reported as a `*PanicError` with a nil problem; then `respondPanic` reports the original panic with the 500 (spec row 1).
     - Answering then panics too: the existing `writeFallback` still applies, because it is armed after the check as before.
     - A genuine EPIPE value: unchanged, nothing is written (spec row 2).
3. **gin: a local guard that reports through `respond(h, r, newPanicked(v))`.**
   - **Rationale:** this is the pattern `guardWrite` already uses. ginprblm can't call the core's unexported `safeCall`.
   - **Failure modes:**
     - Reporting panics: `respond` has its own recover, which returns a bare 500 that is discarded.
     - The writer is already written: unchanged, because `c.Writer.Written()` is checked first and short-circuits.

## Defaults

No configurable behavior changes. With zero configuration, every new path ends in the safe 500 problem (status 500, title only, no detail) or, for a broken connection, no write. No panic value or error text reaches the client.

## Hot Paths

- **Error resolution:** the status-hint step, both its match branch and its new panic branch. The panic branch is covered with and without built-ins, and with and without a hook.
- **net/http panic recovery:** the broken-connection check, with its broken, not-broken and panicking branches.
- **gin panic recovery:** the broken-connection check, with the same three branches.
- **Coverage:** checked with `go test -coverprofile` for the core and for ginprblm. Each new branch must be covered.

## Risks / Trade-offs

- **[Risk]** Cyclic errors still hang or overflow the stack in the same paths, and `safeCall` can't recover a fatal stack overflow. → Out of scope, as documented in proposal.md and planned for v0.3.0. The `doc.go` wording promises only what this change delivers: panics, not cycles.
- **[Risk]** One request now reports several `*PanicError`s, one per step. → This matches the existing behavior of steps 1–4. Collapsing them into one report is part of the v0.3.0 single walk.
- **[Risk]** The fiber adapter still answers an `As`-panicking error with a bare 500 and no hook call. → It doesn't escape or leak, and it's listed as later work.

## Migration Plan

No consumer action. Release core v0.2.1 and `adapters/ginprblm` v0.2.1, following RELEASING.md. ginprblm v0.2.1 keeps requiring core v0.2.0, so the two releases are independent. Rollback means pinning v0.2.0.
