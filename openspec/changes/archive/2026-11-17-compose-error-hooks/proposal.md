# Proposal

## Why

A consumer reported this: `kartaladev/srvr` is a server library that renders every error through prblm on net/http, gin and fiber, and it writes one access record per request. Two parts of prblm v0.1.0 force workarounds on it, and would on any library that wraps prblm:

1. **`OnError` holds a single hook** (`options.go`), and a later `OnError` silently replaces an earlier one. When a library and its consumer both pass `OnError`, one of them loses its hook. srvr works around this by owning the hook and re-exporting a hook option of its own.
2. **ginprblm's status-only error is unexported.** For `c.AbortWithStatus(401)` with no `c.Error`, the hook receives `errStatusOnly` wrapped in a status hint. A hook can't tell that case apart from a handler error with `errors.Is`, so srvr classifies from `c.Errors` instead.

## What Changes

- New core option `prblm.AddErrorHook(hook)`. It adds a hook, while `OnError` keeps its meaning: it sets the one replaceable hook, and a later `OnError` replaces an earlier one, as in v0.1.0. On every report:
  - the `OnError` hook runs first, then the added hooks in the order they were added
  - each hook receives the same arguments, including every note (`*PanicError`, `ErrInvalidStatus`, `ErrShadowedDocumentation`, …)
  - a hook that panics is ignored, and the hooks after it still run
- The `OnError` godoc says that it replaces, and that libraries wrapping prblm should use `AddErrorHook`, so their hook can't be replaced by their consumer's.
- New `ginprblm.ErrStatusOnly`, the exported form of the error behind a status-only abort, so `errors.Is(err, ginprblm.ErrStatusOnly)` works in a hook.

No breaking change. Both additions are new API (a compatibility commitment), and `OnError`'s behavior is unchanged.

## Defaults and Customization

- **Zero configuration:** no hooks. The library never logs, as before. Nothing reaches the client either way, because hooks only observe.
- **Opt-in:** `OnError` for an application's own hook, and `AddErrorHook` for any number of extra hooks. A library adds its hook with `AddErrorHook`.
- No opt-in loosens a safety default.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `error-mapping`: the `OnError hook` requirement grows to cover added hooks, their order and their independence from `OnError`. The faulty-consumer-code requirement isolates each hook, so a panicking hook no longer stops the hooks after it.
- `gin-adapter`: a status-only abort reaches the hooks as an error that matches the exported `ErrStatusOnly`. gin's own 404 and 405 for an unrouted request are status-only aborts too; this is documented, not changed.

## Scope by Gall's Law

The simplest version that works end to end is `AddErrorHook` beside `OnError` in the core, through net/http. The gin and fiber adapters take core options, so they inherit it, and slice 2 only proves parity. `ErrStatusOnly` is independent and touches `ginprblm` alone, so it's its own slice and can be released on core v0.1.0.

Deliberately left out:
- Removing or replacing a single added hook. Nobody asked for it, and options are applied once, at handler construction.
- Telling the other hooks about a hook's panic.
- Giving each hook its own copy of the problem.
- Sentinels for fiber or net/http: neither adapter turns a bare status into a problem, so neither has a status-only case.

## Impact

- Core:
  - `options.go`: `AddErrorHook`, and the `OnError` godoc
  - `handler.go`: a list of hooks with per-hook recovery in `callHook`, and `isReporting` on the list
  - the README section on hooks, and the CHANGELOG (Added)
- `ginprblm`: `middleware.go` (the `ErrStatusOnly` export), its godoc and doc example, and the CHANGELOG (Added).
- Tests: `error-mapping` scenarios in the core, parity in `internal/paritytest`, and gin hook classification.
- Release: core v0.2.0 (together with `isolate-parsed-problems`), and ginprblm in its next release. `ErrStatusOnly` needs no new core API.
- No new dependencies.
