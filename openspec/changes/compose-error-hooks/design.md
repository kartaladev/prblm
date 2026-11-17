# Design

## Context

See proposal.md (Why). Today `Handler` has a single field, `onError func(ctx, err, p)`, which `OnError` assigns. All reports go through three helpers:
- `report`, which checks `isReporting` and calls `callHook`
- `isReporting`, which is "`onError` set, and the request isn't silent"
- `callHook`, which runs one `defer recover` around one call

Callers check `isReporting` before building a report, so nobody pays for a note that nobody reads. `safeCall` reports consumer panics through `callHook` too.

Options are applied once in `NewHandler`, and a `Handler` is immutable and shared by concurrent requests afterwards. Adapters build their handler from core options (`ginprblm.Middleware(opts...)`, `fiberprblm.ErrorHandler(opts...)`).

In `ginprblm/middleware.go`, `finish` answers a status-only abort with `prblm.WithStatus(errStatusOnly, recorded)`.

## Goals / Non-Goals

**Goals:**
- A library can add a hook that its consumer's options can't remove.
- Every hook keeps today's contract: the same arguments, the same notes, and a panic that never affects the response.
- Leave `OnError` callers on v0.1.0 semantics.

**Non-Goals:**
- Removing hooks, priorities, or hooks scoped per request.
- Reporting a hook's panic anywhere.
- Copying the problem for each hook.

## Slices

1. **`AddErrorHook` beside `OnError` in the core, through net/http.** A handler with `OnError(A)` and `AddErrorHook(B)` calls A and then B for an error written by `Handler.Wrap`, and a panic in one hook doesn't stop the next.
   - Satisfies `error-mapping`: Hook sees the original error, Replaced and added hooks, Every hook receives notes and silent requests reach none, Consumer code panics, A panicking hook does not silence the others.
   - Hot paths: `isReporting`, `callHook`, and every report site through them.
2. **Parity in gin and fiber.** The `error-mapping` scenario Same hooks in every adapter runs in `internal/paritytest`. That module replaces the core with the local one in both CI modes, so it can use `AddErrorHook` before the adapters require core v0.2.0. This needs no adapter code: the adapters pass options through.
3. **`ginprblm.ErrStatusOnly`.** A hook can classify status-only aborts with `errors.Is`.
   - Satisfies `gin-adapter`: Classifying a status-only abort in a hook.
   - It doesn't depend on slices 1–2 and runs against core v0.1.0 too, so it can ship in either order. It comes last only because it's the smallest.

## Decisions

### D1. `AddErrorHook` beside an unchanged `OnError`, in separate slots

The handler keeps `onError` (the replaceable slot) and adds `hooks []func(...)` (append only). A report calls `onError` if set, then each entry of `hooks` in order.

Alternatives considered:
- **Make repeated `OnError` options compose.** This was the earlier recommendation. It keeps the API smallest, but it silently changes v0.1.0 behavior: a consumer who overrides a default hook with a second `OnError` would log twice, and `OnError(nil)` would stop clearing. The additive option makes no silent change; the maintainer chose it, along with the name `AddErrorHook`.
- **A single list, where `OnError` clears the list and then appends.** Rejected: a consumer's `OnError` passed after a library's `AddErrorHook` would remove the library's hook. That reproduces the reported bug, and the outcome depends on the order of the options.
- **A single list in registration order, where `OnError` replaces its own entry in place.** Rejected: the call order would depend on where the first `OnError` appeared among the options. It's harder to explain and to test, and nothing needs it.

**Why `OnError` runs first:** it gives one fixed order whatever the order of the options (rows `AddErrorHook(B), OnError(A)` → `A, B`). The application's own hook sees the error before library hooks. Hooks are independent observers, so the order only matters for log ordering.

**Naming:** `AddErrorHook` is a verb phrase that says it accumulates. `OnError` stays the name for the one replaceable hook. Both godocs refer to each other.

**Failure modes:**
- `AddErrorHook(nil)` is ignored, like nil mappers and enrichers. `OnError(nil)` still clears its slot (v0.1.0 behavior). Both have rows.
- The same func added twice is called twice: options aren't deduplicated, and func values can't be compared. This has a row.
- Concurrent requests: `hooks` is built once in `NewHandler` and only read afterwards, so `-race` stays clean. The slice is built fresh for each handler, so two handlers never share a backing array.

### D2. Each hook is isolated by its own recover

`callHook` loops over the hooks and calls each one through a function with its own `defer recover`. A panicking hook is ignored, as today (the `panic-recovery` and `error-mapping` contract), and the hooks after it still run.

Alternatives considered:
- **One `recover` around the loop.** Rejected: one consumer's broken hook would silently drop the hooks after it, including a library's access log, which is exactly what the issue is about.
- **Telling the other hooks about the panic as a `*PanicError` note.** Rejected for now (Gall's Law). It adds a re-entrancy rule (that note mustn't go back to the hook that panicked) for a case nobody asked about. It can be added later without changing the specs written here.

A panic with `http.ErrAbortHandler` inside a hook is also swallowed, as `safeCall` already does for mappers. Only `Recover` re-panics it, and only for the application handler's own panic. A hook runs outside the application's control flow, so aborting the response from a logger would be a surprise. This has a row.

**Failure modes:**
- `panic(nil)`: since Go 1.21 it becomes `*runtime.PanicNilError`, `recover` returns non-nil, and it is isolated. This has a row.
- A hook that blocks: not handled, the same as today. Hooks run synchronously on the request goroutine, as the godoc already says.

### D3. Every hook receives the same problem pointer

The hooks get the same `*Problem`. The existing contract, "the hook must not modify the problem", now applies to every hook, and the godoc says that the hooks share it.

Alternative considered: a clone for each hook. Rejected: it allocates on the error path for every extra hook, for a contract that already forbids modifying.

**Failure mode:** a hook that modifies the problem can affect the hooks after it. That breaks the documented contract, the same as today's single hook modifying the problem before it is written. It's left to documentation.

### D4. `isReporting` is "any hook"

`isReporting` becomes `(h.onError != nil || len(h.hooks) > 0) && !isSilent(ctx)`. It's computed from fields that never change after construction. Silent requests (discovery's 404 and 405) still reach no hook. This has a row.

### D5. Export `ginprblm.ErrStatusOnly`

Rename `errStatusOnly` to `ErrStatusOnly` with gopls `go_rename_symbol`, keeping the error string. Its godoc says:
- it is wrapped in a status hint, so match it with `errors.Is`
- the problem status is the recorded one
- it also covers `c.Status(5xx)` without a body, so a hook that logs status-only aborts as ordinary responses should still treat `p.Status >= 500` as a failure
- it also covers gin's own answer to an unrouted request: a 404 for a path no route claims, and a 405 for a wrong method when `HandleMethodNotAllowed` is on. No handler ran, so nothing was rejected on purpose; a hook shouldn't treat every `ErrStatusOnly` below 500 as an intentional rejection. This is documented, not changed: gin records these statuses exactly like `c.AbortWithStatus`, and the middleware can't tell them apart without inspecting gin's routing.

Alternatives considered:
- **The names `ErrAbortedWithStatus` and `ErrNoError`.** `ErrStatusOnly` matches the request, the internal name and the spec wording.
- **A core sentinel shared by all adapters.** Rejected: only gin turns a bare status into a problem.
- **Passing a nil error to the hook.** Rejected: hooks rely on a non-nil error, and the nil-error requirement says no hook call for nil.

**Which responses reach the hooks:** a response with no error status and no recorded error never calls them. A recorded error always does, even when the body was already written and no problem can be (the existing "error after body" behavior of `finish`): the hook still hears about the failure.

**Failure mode:** a consumer compares with `==`. The godoc says to use `errors.Is`, because the hook receives the sentinel wrapped in a status hint.

## Defaults

- With no hooks, nothing is reported and nothing is built for a report. `isReporting` is false.
- `OnError` replaces; `AddErrorHook` adds. The godoc of `OnError` points libraries to `AddErrorHook`.

## Hot Paths

- `isReporting` on every resolution and note site, including the silent-request branch.
- `callHook`, covering five cases:
  - only `OnError`
  - only added hooks
  - both
  - a panicking hook first, middle and last
  - no hooks (it isn't called)
- Each adapter's error path calling the hooks (parity).
- ginprblm `finish`, for the status-only, recorded-error and no-problem branches.

## Risks / Trade-offs

- [Two ways to register a hook confuse users] → the godocs cross-reference each other and the README hook section shows both. The rule of thumb: applications use `OnError`, libraries use `AddErrorHook`.
- [A library still uses `OnError` and gets replaced] → the `OnError` godoc says so explicitly.
- [Many hooks slow the error path] → one call per hook, with no allocation beyond today's report. Only error responses pay for it.

## Migration Plan

This change is additive, so there's nothing to migrate. srvr can replace its owned hook with `AddErrorHook` once core v0.2.0 is released, and its `c.Errors` classification with `errors.Is(err, ginprblm.ErrStatusOnly)` once ginprblm is released. Slice 3 can go out in a ginprblm release on core v0.1.0 before the core is tagged.
