# Proposal

## Why

Resolution trusts every error chain to be finite and well-behaved, and walks it about nine times for each unrecognised error. Every defect below was reproduced at v0.2.1 (origin/main 5742e04):

- An error whose `Unwrap() error` returns itself makes `Respond` hang, and so does a net/http `Recover` given a cyclic panic value. A cycle through `errors.Join` is a fatal stack overflow, which `recover` cannot catch.
- A fan-out chain of 25 `errors.Join(n, n)` levels takes 4.9 s for one `Respond`.
- A typed-nil `*Problem` joined ahead of a real problem gives a 500, and the real problem is lost. A typed-nil `*http.MaxBytesError` gives a false 413. So does an `As` method that reports a match and sets nothing.
- In fiberprblm, an error whose `As` method panics gets a bare 500, with no body and no hook call.
- Under `GODEBUG=panicnil=1`, a `panic(nil)` in consumer code is contained but never reported.

Go's maintainers declined cycle detection in `errors` (golang/go #34957, #40589, #70746), so the library must bound the walk itself. scrty, a consumer, implements the same walk rules. The two implementations must agree.

## What Changes

- **One bounded walk per error.** The chain is flattened once: the root first, then depth first, in the order `errors.Is` and `errors.As` use. Every resolution step then scans that list.
  - The walk examines at most 1,000 entries. Every visit costs 1: the root, each node, each repeat visit, each nil entry in an `Unwrap() []error`, and each typed nil. No entry is deduplicated.
  - A typed nil (a nil pointer) is skipped, and its methods are never called.
  - A panic in an `Unwrap`, `Is` or `As` call, or in an `==` against a target, aborts the walk.
  - The walker lives in the core module at `internal/errwalk`, unexported to consumers.
- **An aborted walk fails closed.** The client gets the safe 500. The hooks receive a note wrapping the new sentinel **`ErrUnwalkableError`**, with the reason (`budget` or `panic`) and the 500 problem, then the original error as usual. A cycle, a deep chain or an exploding fan-out is now answered at once.
- **BREAKING (behavior):** a panicking `Is` or `As` method of the error being answered no longer counts as "no match, continue" (v0.2.1). It aborts the walk, so the answer is the safe 500 even when another step would have matched. The report changes from "one or more `*PanicError`" to one `ErrUnwalkableError` note that also wraps the `*PanicError`.
- **Typed nils and empty `As` results never match.** Built-in mappers and the `Is`, `As` and `Status` helpers skip typed nils. For a pointer or interface target, an `As` method that reports true and leaves the target nil counts as no match.
- **New `Handler.RespondPanic(ctx, req, *PanicError) (Response, bool)`.** It is the panic counterpart of `Respond`: the logic the net/http `Recover` already uses, without writing. It walks the panic value once for the broken-connection check, reports at most one note, and returns the safe 500, or false when the connection is broken. The core `Recover` and ginprblm's `Recovery` both use it, so a cyclic or panicking panic value is answered with the safe 500 after exactly one walk and one note.
- **The adapters use the bounded walk** for every chain inspection they do themselves:
  - fiberprblm: the `*fiber.Error` hint, the client-gone check and the route-not-found check;
  - ginprblm: the client-gone check (`Recovery` now goes through `RespondPanic`).
  
  An error whose methods panic in fiberprblm now gets the full safe 500 problem and the hook calls.
- **Every recovery detects panics with a "returned normally" flag**, not `recover() != nil`: `safeCall`, the core `Recover` and its fallback writer, gin's `Recovery`, fiber's `Recovery`, and the last-line `respond` of gin's `Middleware` and fiber's `ErrorHandler`. So `panic(nil)` under `GODEBUG=panicnil=1` is answered (the safe 500, or the adapters' bare 500 when `Respond` itself panics) and reported as a `*PanicError` with value nil, where it used to give an empty 200. The core does not contain a request context whose `Value` method panics, the one way left for `Respond` to panic: the adapters' bare 500 covers it (design D2).
- **`runtime.Goexit` in a handler** (`t.FailNow` in a test) cannot be told from that `panic(nil)`, so it is answered the same way, and the problem is flushed because the goroutine ends before the server finishes the response. Over HTTP/1.x the client receives it complete; over HTTP/2, net/http then resets the stream (`INTERNAL_ERROR`) after the problem's bytes. fiber cannot answer a Goexit at all. A Goexit inside an error method produces no note, since the goroutine exits before the note is reported.
- **Nested recovery never loses a panic.** A panic that stops at the innermost recovery is reported once. A Goexit goes on through the outer recoveries, and under `panicnil=1` they cannot tell it from a new `panic(nil)` in the code between them, so each reports it: a duplicate report is safer than a lost one (design D4).
- **The note names only the panic value's type** (`%T`), so it never carries the error's text and formatting it can never panic.
- **In fiberprblm, a recovered panic is never route-not-found**: with type documentation on, a panic on an unrouted documentation path gets the safe 500 and reaches the hooks, even when its value wraps `fiber.ErrNotFound`.
- **Every spelling of a header name is that header when merging.** net/http sends a key written directly into the header map (`w.Header()["cache-control"]`) as written, and fiber keeps the handler's spelling under `DisableHeaderNormalizing`. `Response.MergeInto` folds such keys of the problem's header names into the canonical name before applying the rules, and fiber's `send` finds the handler's values under any spelling. A replaced header then replaces them, an unmarked `Cache-Control` yields to them, and `Vary` merges with them, instead of both values going out. The common path (canonical keys only) allocates nothing more.
- **validatormap tells a translator panic apart with the returned-normally flag**, like the rest of the library, so a translator's `panic(nil)` under `panicnil=1` reaches the `OnError` hook as its godoc promises.
- **New opt-in `prblm.WithReplacedHeader(name, value)`.** It is a `Field` that sets a header on a problem and makes it replace the value the handler already set, in every adapter. This lets a problem force `Cache-Control: no-store` over a handler's `public, max-age=...`. `WithHeader` keeps today's merge rules. A mark whose header is deleted from the problem before it is written replaces nothing, so the `Vary` and `Cache-Control` defaults then merge as usual.

## Defaults and Customization

- **Zero configuration:** every error is walked once within the 1,000-entry budget. A chain the walker cannot finish gets the safe 500, which carries no error text, no stack and no internals. The hooks receive the reason. Nothing to configure, and no option disables the bound or raises the budget.
- **The budget is fixed** at 1,000, the value shared with scrty. A real chain is a handful of entries, so the bound costs nothing in practice. A configurable budget would add surface without need (Gall's Law).
- **Typed nils seen by consumer code:** a consumer `Mapper` or hook that calls `errors.Is` or `errors.As` itself still sees typed nils and the raw chain, and this is documented. Mappers built with `prblm.Is`, `prblm.As` and `prblm.Status` get the bounded semantics.
- **Adapter API:** `Handler.RespondPanic` is for framework adapters, like `Respond`. It changes no default.
- **Opt-in:** `WithReplacedHeader` is the only new customization. It tightens a response, typically toward `no-store`, and loosens no safety default. It cannot replace `Content-Type` or `Content-Length`. A replaced `Vary` still lists `Accept`.

## Capabilities

### New Capabilities
- `error-chain-walk`: how the library walks an error chain. This covers the order, the 1,000-entry budget, typed nils, panics, the abort and its fail-closed answer with the `ErrUnwalkableError` note (whose text names only the panic value's type), the empty-`As` rule, and the guarantee that every core and adapter inspection uses the same bounded walk.

### Modified Capabilities
- `error-mapping`: "Error values with panicking methods never break resolution" changes from no-match-and-continue to fail-closed with one note. "Self-describing errors" and "Sentinel and typed mapping helpers" skip typed nils and empty `As` results. "Faulty consumer code never breaks resolution" adds a `panic(nil)` row under `panicnil=1`. "Received problems are never relayed implicitly" skips a typed-nil `*Received` like any typed nil, so `fmt.Errorf("op: %w", (*prblm.Received)(nil))` gives the safe 500 instead of v0.2.1's 502.
- `error-sentinels`: in "Degenerate errors never break resolution", the panicking `Is` row now reports an `ErrUnwalkableError` note, and a typed nil is skipped.
- `panic-recovery`: in "Panic values with panicking methods are answered", the inspection is bounded and a cyclic panic value is answered. The report is one note. It gains `panic(nil)` and `runtime.Goexit` rows, the Goexit one over HTTP/1.1 and over HTTP/2. A new requirement, "Framework-neutral panic response", covers `RespondPanic`; another, "Nested recovery never loses a panic", covers nested recoveries, a second `panic(nil)` after the inner one returned, and a Goexit inside an error method.
- `gin-adapter`: "Recovery answers panic values with panicking methods" and "Client disconnects are not answered" use the bounded walk, with cyclic, `panic(nil)` and Goexit (HTTP/1.1 and HTTP/2) rows added. New requirements: "Nested recovery never loses a panic" and "The middleware answers a panicking Respond with a bare 500".
- `fiber-adapter`: in "Fiber errors are status hints without their message", an error whose methods panic, or a cyclic one, gets the full safe 500 problem with the hook calls when the core's walk cannot finish either, and the hint costs nothing of the budget. "Client disconnects are not answered" uses the bounded walk. "Opt-in recovery returns panics to the error handler" answers `panic(nil)` under `panicnil=1`, with a cyclic-value row, and a recovered panic on an unrouted documentation path is never route-not-found. A new requirement, "The error handler answers a panicking Respond with a bare 500".
- `problem-document`: new requirement "Problems can mark headers to replace those already set" (`WithReplacedHeader`).
- `problem-response`: "Response headers" gives a replaced header precedence over a header the handler already set, and a mark whose header was deleted from the problem replaces nothing (net/http, gin and fiber rows). A header the handler set under a non-canonical key (a direct write to net/http's header map, or fiber's `DisableHeaderNormalizing`) counts as that header for every merge rule, replaced or default, and the merged values go out under the canonical name alone (new scenario, net/http, gin and fiber rows).
- `validation-error-mapping`: "Translator panics are contained once" tells a panic apart by the call not returning, so a translator's or translation's `panic(nil)` under `panicnil=1` is still the problem's cause.

## Scope by Gall's Law

The simplest version that works end to end is one slice. The core flattens the error once with the bounded walker, and `Resolve` fails closed on an abort. A self-looping error returned through `Handler.Wrap` is then answered with a 500 and an `ErrUnwalkableError` note instead of hanging. Each later slice adds one capability to that working system:

1. walker with budget and abort, `Resolve` fails closed, and every core step scans the list;
2. typed nils and empty `As` results never match (built-ins and the `Is`, `As` and `Status` helpers);
3. `RespondPanic`, so the core `Recover` checks the broken connection over the walk;
4. `safeCall` reports `panic(nil)`;
5. ginprblm uses the walk (`Recovery` through `RespondPanic`, client gone);
6. fiberprblm uses the walk (hint, client gone, route not found);
7. `WithReplacedHeader`;
8. benchmarks, documentation and changelogs.

Deliberately left for later:
- exporting the walker as a public package;
- a configurable budget;
- cycle reporting beyond the reason.

## Impact

- **Core (`github.com/kartaladev/prblm`) → v0.3.0:**
  - new `internal/errwalk`;
  - new `internal/statushint`, which carries the adapters' status hint outside the error's chain;
  - `handler.go` (resolution steps, `Recover`), `http.go` (the tracking writer), `builtins.go`, `mapper.go`, `panic.go`, `conn.go`/`conn_plan9.go`, `problem.go` (`WithReplacedHeader`, clone) and `respond.go` (`MergeInto`, `Response`, `newResponse`) change;
  - new exported names: `ErrUnwalkableError`, `WithReplacedHeader` and `Handler.RespondPanic`.
- **ginprblm → v0.3.0** and **fiberprblm → v0.3.0.** Both import `prblm/internal/errwalk` and `prblm/internal/statushint`, and must require core v0.3.0. They are released in step with the core from now on (RELEASING.md). No other internal package is shared: the nested-recovery mark the fourth review added (`internal/recovermark`) was removed in the fifth.
- **validatormap → next patch release (v0.1.1):** `recoverInto` uses the returned-normally flag. It imports only the core's public API, so it still requires core v0.1.0 and needs no core v0.3.0. It gets its first CHANGELOG.
- **gormmap and pgxmap are unchanged:** they contain no `recover`.
- **ginprblm and fiberprblm** also pick up the case-insensitive header merge: gin through the core's `MergeInto`, fiber in its own `send`.
- **Behavior worth knowing:** nested recoveries report a `runtime.Goexit` once each (design Risks).
- **Hot path:** error resolution trades about nine chain walks for one walk plus list scans. It is benchmarked against v0.2.1 following BENCHMARKS.md.
- **scrty** (session `security-refactor`) implements the same rules. prblm adopts scrty's readings of "repeat" (no deduplication) and of "sets nothing" (pointer and interface targets only), so no shared rule changes.
