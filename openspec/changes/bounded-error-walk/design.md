# Design

## Context

Today every resolution step walks the raw chain on its own. Each was observed at origin/main 5742e04:

- `selfDescribed` calls `errors.As(err, &Describer)` (handler.go:871).
- `mapSentinel` runs `applicableHint`, which is `errors.AsType`, and then `firstSentinel`, an unbounded recursion (builtins.go:91).
- The three built-in `As` mappers and the two built-in `Is` mappers call `errors.AsType`/`errors.Is` (mapper.go:55, :72).
- `hinted` repeats `applicableHint` (handler.go:880).
- `Recover`'s `isBrokenConnection` calls `errors.Is` twice (conn.go:13).

Outside the core, these also walk the raw chain:

- fiberprblm: `withHint` (handler.go:88), `isClientGone` (:78) and `isRouteNotFound` (discovery.go:63);
- ginprblm: `isClientGone` (middleware.go:183) and `isBrokenConnection` (conn.go:12).

`safeCall` (panic.go:39) contains a panic only when `recover() != nil`. Under `GODEBUG=panicnil=1`, a `panic(nil)` is then contained silently: the probe showed 0 hook reports.

The walk rules are fixed by the maintainer and shared with scrty. See proposal.md. prblm follows scrty's reading of two points:

- **Repeats:** no entry is deduplicated. Every visit costs 1 and calls that visit's `Unwrap`.
- **"Sets nothing":** this applies only to pointer and interface targets.

## Goals / Non-Goals

**Goals:**
- One bounded walk per error in the core. Every core step scans the walk's list. Each adapter inspection is one more bounded walk that never reports.
- Exactly one hook note per aborted walk of the error being answered.
- No new allocation count on the common path, which is a chain of 1 to 4 entries. Measured: allocs/op equal to v0.2.1, and +224 B/op, the heap-allocated `Walk` of each resolution. The bytes are accepted; see D1 "Allocation" for the rejected pooling.

**Non-Goals:**
- Changing what consumer mappers, describers, enrichers and hooks receive. They get the original error.
- A public walker API, or a configurable budget.
- Deduplicating entries, or detecting cycles beyond the budget.

## Slices

1. **Core walk, fail closed.**
   - Adds `internal/errwalk` with `Walk`, the flatten step, `Is` and `As` scans, the budget, typed-nil skipping and panic aborts.
   - `Handler.match` builds one walk. Steps 1, 4 and 5 (`selfDescribed`, `mapSentinel`, the built-in `Is`/`As` mappers, `hinted`) scan it. `Resolve` fails closed with the `ErrUnwalkableError` note.
   - **Works at the end:** a self-looping error through `Handler.Wrap` gets the safe 500 within 1 s, with one note.
   - **Specs:** `error-chain-walk` (all but the panicnil scenario and the gin/fiber rows), `error-mapping` "Error values with panicking methods…", `error-sentinels` "Degenerate inputs".
   - **Hot paths:** resolution (every step), the walk itself.
2. **Helpers scan the walk.**
   - `prblm.Is`, `prblm.As` and `prblm.Status` return a mapper that scans the handler's walk, or builds its own walk when `MapError` is called directly.
   - **Specs:** `error-mapping` "Sentinel and typed mapping helpers" (including "Helper called directly"), `error-chain-walk` "An As that sets nothing" and "Consumer code sees the error as returned".
   - **Hot path:** consumer and scoped mapper matching.
3. **`RespondPanic`; `Recover` uses it.**
   - `Handler.RespondPanic(ctx, req, pe)` walks the panic value once for EPIPE/ECONNRESET, reports at most one note, and returns the safe 500 or false.
   - `recoverPanic` delegates to it after its own `tw.started` check.
   - **Specs:** `panic-recovery` (both).
   - **Hot path:** panic recovery.
4. **`safeCall` reports `panic(nil)`.**
   - A "returned normally" flag replaces `recover() != nil`.
   - **Specs:** `error-mapping` "Faulty consumer code…" (panic(nil) rows and the panicnil scenario), `error-chain-walk` panicnil scenario.
5. **ginprblm.**
   - `Recovery` calls `RespondPanic`. `isClientGone` scans an `errwalk.Walk`. `go.mod` requires the unreleased core through `replace ../..` until core v0.3.0 is tagged.
   - **Specs:** `gin-adapter` (both), `error-chain-walk` gin rows.
   - **Hot path:** gin error answering, gin recovery.
6. **fiberprblm.**
   - `withHint`, `isClientGone` and `isRouteNotFound` scan an `errwalk.Walk`. The bare-500 `recover` in `respond` stays as the last line of defense.
   - **Specs:** `fiber-adapter` (both), `error-chain-walk` fiber rows.
   - **Hot path:** fiber error handler.
7. **`WithReplacedHeader`.**
   - A `Field` that marks a header. The mark is copied by `clone` and carried into `Response`. `MergeInto` replaces marked names.
   - **Specs:** `problem-document` (added), `problem-response` (modified).
   - **Hot path:** response writing in all three adapters (`MergeInto`).
8. **Benchmarks, documentation, changelogs.**
   - A new `BenchmarkResolve` case for the walk, plus the BENCHMARKS.md protocol against v0.2.1.
   - Package doc (doc.go:57 currently says cycles hang), README, the three CHANGELOGs, and the doc note that consumer code still sees typed nils.

Each slice ends green and releasable. Slice 1 alone already removes every hang and stack overflow in the core.

## Decisions

### D1. The walker: `internal/errwalk`, a list plus a sticky abort
`errwalk.New(err) *Walk` flattens the chain immediately. Its methods scan the result:

- `Is(target) int` returns the index of the first match, or -1;
- `As[T](w) (T, int)`;
- `Aborted() *Abort` reports why the walk aborted, if it did.

`Abort` carries a `Reason` (budget or panic), the panic value, and the stack captured inside the deferred recover. The abort is sticky: once set, every scan returns no match. The handler checks `Aborted()` after each step.

- **Flatten:** an explicit stack, not recursion, so depth costs no goroutine stack.
  - Before pushing the children of an `Unwrap() []error`, it checks `examined + pending + len(children) > Budget`. This makes a node with 20,000 children abort at once.
  - Each pop charges 1 before any method is called.
  - A typed nil (reflect `Kind == Pointer && IsNil`) is charged and skipped.
  - A nil entry is charged and skipped.
- **Return-normally guard:** every call into error code (`Unwrap`, `Is`, `As`, `==`) runs inside one `call` helper. The helper sets `ok = true` after the call and recovers in a defer. If `ok` is still false, that is an abort with the recovered value and `debug.Stack()`.
- **Inline buffer:** `Walk` embeds an `[8]error` array as the list's first backing store, and holds no pointer into itself. A longer chain moves the entries to a slice. An `Unwrap() error` child is returned without a slice, so a `%w` chain allocates nothing beyond the `Walk`.
- **Fast paths:** only a call that can panic runs through `call` and its deferred recover.
  - `Is` classifies the target once (`equalityOf`): a plain `==` for kinds where `==` cannot panic (pointers, strings, numbers), a guarded `==` through `call` only against an entry of the same struct or array type, and no `==` for an uncomparable type. An entry without an `Is` method and with a plain `==` never enters `call`.
  - `As[T]` uses a type assertion, which cannot panic, and calls only an entry's `As` method through `call`.
  - `IsAny(targets) (at, which)` answers the sentinel step: the lowest index matching any target, ties to the earlier target. When no entry is special (no `Is` method, plain `==`), one pass over the entries replaces a scan per target, because the order of the comparisons cannot be observed. Otherwise it calls `Is` per target, in order, so a panicking `Is` past the first match still aborts, as a scan per target would.
- **Allocation:** the handler's `Walk` escapes (the steps share it through the resolution struct), so each resolution allocates one `Walk` on the heap: +224 B/op, with allocs/op equal to v0.2.1, which made its own allocations that the walk now replaces. Pooling the `Walk` with `sync.Pool` was rejected: it saves bytes, not allocations, and a pooled walk would retain the last error's entries, and through them consumer values, until reuse, or need clearing on every return path, a new failure mode for a gain the benchmark does not justify.

Alternatives considered:
- Recursion with a depth counter. Rejected: the budget counts entries, not depth, and recursion still pays stack growth.
- A callback-style `Walk(err, func(e) bool)` per step. Rejected: it walks once per step again.
- An exported package. Deferred by the maintainer.

**Failure modes:**
- A panicking `Unwrap`, `Is`, `As` or `==`, including `panic(nil)`: caught by the flag, giving reason `panic`.
- A cycle, deep nesting or fan-out: reason `budget`.
- Reflection on an exotic kind: only `Pointer` is treated as nil. Nil maps, slices, funcs and chans are walked normally, as in the spec row "nil slice … implementing `Is`".
- An uncomparable target: `==` runs only when the target's dynamic type is comparable (`reflect.TypeOf(target).Comparable()`), as `errors.Is` does. A runtime panic inside that `==` (an `any` field holding a slice) is an abort.
- A consumer `Mapper` that keeps the `*Walk`: impossible, because the walk is never handed to consumer code.

### D2. Resolution scans one walk and fails closed
`match(ctx, err)` builds the walk first. If the walk aborted, `match` stops before any step. Otherwise:

- **Step 1 (describer):** scans with `As[Describer]`, then calls `Problem()` through `safeCall` on the outermost describer only. That is consumer code, so a panic there is no match.
- **The resolution:** `newResolution(err)` returns a small struct, passed by value: the error as returned, the `*Walk`, and the hint's status with its index (`hintStatus`, `hintAt`, -1 when none applies; `isHintKnown` once known). An adapter's hint at the root (D6) is known at once, at index 0.
- **Steps 2–4 (mappers):** a mapper that implements the unexported interface `walkMapper { mapWalk(ctx, r resolution) *Problem }` scans `r.walk`. This covers built-ins and helpers, through the `walkFunc` adapter. Any other mapper gets `MapError(ctx, err)` through `safeCall`. `walkFunc.MapError`, for a helper called directly, builds its own resolution and does not look up the hint: only `mapSentinel` uses it, and it calls `findHint` itself, which does nothing when the handler already looked the hint up. So `prblm.Is`, `prblm.As` and `prblm.Status` called directly never call the `As` methods of the whole chain for a hint they ignore (a review found that an `As` method past the match then aborted the helper's walk; spec row "Helper called directly").
- **The hint:** `findHint` runs once, after the consumer mappers (steps 2 and 3) and before the built-ins, so an `As` method later in the chain cannot abort a walk that an earlier step already answered. The outermost hint is an adapter's when there is one, set by `newResolution` at index 0 (outside every entry, so it stops every sentinel); otherwise `findHint` takes the first `*statusError` in the walk. A non-error status is no hint, even over an inner one, as before.
- **`mapSentinel`:** it is now `IsAny(sentinels)` restricted to indexes before `hintAt`. The recursion and the double `applicableHint` go away.
- **Step 5 (`hinted`):** reuses the hint found by `findHint`.
- **After each step:** `r.stops(p)` stops on a match or on `walk.Aborted() != nil`, and an abort fails closed.

`Resolve` turns an abort into `fallbackProblem()`, runs `complete`, then reports the note with that problem. `Respond` then reports the original error as today, so the note comes first.

Alternatives considered:
- Pass the walk through `ctx`. Rejected: it allocates, and a consumer could see it.
- Make the built-ins plain functions. Rejected: `WithoutBuiltins` and `h.builtins` already model them as mappers.

**Failure modes:**
- An abort in step 4 after scoped mappers declined: fail closed, not "continue". This matches the spec rows under "another step would have matched".
- No hook: the note is not built (`isReporting`), so nothing allocates.
- A silenced context (discovery): no note, as today.
- `Resolve` is called directly without `Respond`: the note is still reported once, and the original error is not reported, as today.
- A request context whose `Value` method panics (fifth review). `scopedMappers` calls `ctx.Value(scopeKey{})` outside every `safeCall`, and the key lookup goes past the library's own `withRequest` value to the consumer's context, so `Respond` panics. Decision: the core does not contain it. A `context.Context` is not an extension point of this library the way mappers, enrichers and hooks are: its contract is that `Value` is safe to call, and net/http, gin, fiber and every other middleware call it freely, so such a context breaks the request far beyond `Respond`. Containing this one call would cost a closure and a deferred recover on every resolution, the hot path, to make one consumer bug look like "no scoped mappers" and silently drop the mappers the context was meant to carry. The specs ask for no containment there (the faulty-consumer requirements name mappers, enrichers, hooks and the error's own methods). The adapters' last line of defense covers it instead: gin's `Middleware` and fiber's `ErrorHandler` wrap `Respond` in `respond`, which answers a bare 500. That `respond` told a panic apart with `recover() != nil`, so a `panic(nil)` under `panicnil=1` gave an empty 200; it now uses the returned-normally flag (gin-adapter and fiber-adapter "Respond panics", `TestMiddleware_RespondPanics`, `TestErrorHandler_RespondPanics`, both with a context whose `Value` panics). In net/http, `Wrap` lets the panic reach `Recover`, which answers the safe 500.

### D3. The note
`ErrUnwalkableError = errors.New("prblm: error chain cannot be walked")`. The note is an unexported `*unwalkableNoteError{reason, panic *PanicError}`:

- `Error()` is built on each call: `"prblm: error chain cannot be walked: budget"`, or `"prblm: error chain cannot be walked: panic: <T>"`, where `<T>` is the panic value's type as `%T` prints it (`string`, `*mypkg.Err`, `*runtime.PanicNilError` for `panic(nil)`, `<nil>` for `panic(nil)` under `panicnil=1`). The text is the one the spec names. A `runtime.Goexit` inside an error method never produces a note: `errwalk`'s `call` records the abort, but the goroutine goes on exiting before `Resolve` can report it, and a recovery around it reports the Goexit as a panic with value nil (`TestRecover_GoexitInErrorMethod`, fifth review).
- `Unwrap() []error` returns `[ErrUnwalkableError]`, or `[ErrUnwalkableError, *PanicError]` for reason `panic`, so `errors.Is` and `errors.As` find both.

It never formats the original error, so the error's text cannot reach a log through the note. It is built only when a hook will receive it (`isReporting`).

`fmt.Errorf` was the first design and was rejected after a review: it formats eagerly, so a panic value whose own `Error()` panics with a value that also cannot be formatted made `fmt` re-panic inside `noteUnwalkable`. That panic escaped `Respond`, `Wrap`, `RespondPanic` and gin `Recovery`, and fiber fell back to its bare 500. The lazy type moved the formatting into the hook.

**Only the panic value's type (fourth review, owner's decision).** The message used to carry `PanicError.Error()`, so the panic value's text. A method that panics with its own receiver (`Unwrap` panicking with the error) then put the error's text, `secret-dsn` in the repro, into the note, against the spec's "SHALL NOT include the error's text". The owner chose to fix the code: the message names the value's type with `%T` and nothing else. `%T` calls no method of the value (no `Error`, no `String`), so formatting the note can no longer panic, whatever the value; the hostile-value machinery is now just a direct `fmt.Sprintf` in `Error()`, and a hook that formats the note no longer panics in `callOne`. The `*PanicError` stays wrapped, so a hook that chooses to inspect the value reaches it with `errors.As`. Pinned by `TestResolve_UnwalkableNote` (string, self-panicking `Unwrap`, unformattable value, `panic(nil)`), `TestResolve_FaultyError_PanicNil` (`<nil>`), `TestRespond_HookFormatsUnformattableNote` and the exact message checks of gin's and fiber's `assertNoted`.

**Failure modes:**
- A panic value whose `Error()` panics, when the hook formats the note: `%T` never calls it, so the note formats without panicking.
- A panic value that is itself a hostile error: it is reachable from the note through `PanicError.Unwrap`, but the library never walks the note.

### D4. `Handler.RespondPanic(ctx, req, pe *PanicError) (Response, bool)`
It does the following, in order:

1. `mustExist`; a nil `pe` becomes `&PanicError{}`.
2. `ctx = withRequest`.
3. It walks `pe` once. `broken` is a match for any target in `brokenConnErrors`, which is `[EPIPE, ECONNRESET]`, and nil on plan9. If the walk aborted, `broken` is false.
4. If `broken`, it reports `pe` with a nil problem and returns false.
5. Otherwise it builds the fallback and completes it, reports the note if any with that problem, then renders it and reports `pe` with the sent problem, and returns true. The note comes before rendering, as in `Resolve`, so it precedes any encoding note and the report of `pe`.

`recoverPanic` keeps its `ErrAbortHandler` re-panic and its `tw.started` early report. After those it calls `RespondPanic` under the existing `writeFallback` defer.

**Telling a panic apart.** `recoverPanic` (net/http `Recover`), `writeFallback`, gin's `recoverPanic` and gin's `reportPanic` decide that a panic happened from a "returned normally" flag, as `safeCall` does (D5), not from `recover() != nil`. Under `GODEBUG=panicnil=1`, `recover()` returns nil for `panic(nil)`, and the old check left the request with an empty 200 and no hook call. A review found this; it is fixed in this change, with a row in each "Hostile panic value" scenario.

**`runtime.Goexit`.** A handler that calls `runtime.Goexit` (`t.FailNow` in a test) also leaves the flag unset, and `recover()` returns nil, exactly as for `panic(nil)` under `panicnil=1`: the two cannot be told apart. Decision: answer it as a panic with value nil (the safe 500 and a `*PanicError{Value: nil}` report). The alternative, returning silently when `recover()` is nil and Goexit is suspected, would bring back the `panic(nil)` swallow. `recover` does not stop `Goexit`, so the goroutine still exits after the deferred answer. Covered by `TestRecover_Goexit` and gin's `TestRecovery_Goexit`, and documented on `Recover` and `Recovery`.

Because the goroutine exits, net/http never finishes the response: the problem stays in the response's buffer and, over a real server, the client got EOF (a review reproduced it). So when the recovered value is nil, `recoverPanic` (net/http) flushes after `tw.send` through `trackingWriter.FlushError`, and gin's `recoverPanic` calls `c.Writer.Flush()`. The response carries its `Content-Length`, so the flushed response is complete, and the connection's own close then sends it. For `panic(nil)` under `panicnil=1` the flush is only early, which is harmless. Covered over a real server by `TestRecover_Goexit_RealServer` and gin's `TestRecovery_Goexit_RealServer` (three middleware orders).

- **A writer that cannot flush:** `FlushError` returns `http.ErrNotSupported`, which is ignored: there is no other way to send the problem, and nobody to tell. gin's `Flush` does nothing for such a writer. The problem is written but unsent, and the hooks were told (`TestRecover_Goexit_UnflushableWriter`, gin `TestRecovery_Goexit_UnflushableWriter`).
- **A `Flush` that panics:** it runs before `answered` (net/http) or `written` (gin) is set, so `writeFallback` or `reportPanic` reports the second panic as for a panicking `Write` (`TestRecover_Goexit_FlushPanics`, gin `TestRecovery_Goexit_FlushPanics`).
- **HTTP/2 (fourth review).** The complete answer holds over HTTP/1.x only. net/http's HTTP/2 server treats a handler that did not return as a panic and resets the stream with `INTERNAL_ERROR`, after the flushed frames. The client receives the status, the headers and every byte of the problem, and then the reset, which Go's client reports as `stream error: … INTERNAL_ERROR` while reading the body. No code in `Recover` or `Recovery` can prevent it: the reset comes from the server's own handler wrapper, after the goroutine has gone. The hooks are still told once. The godocs, the panic-recovery and gin-adapter Goexit rows and the CHANGELOGs say so, and `TestRecover_Goexit_HTTP2` and gin's `TestRecovery_Goexit_HTTP2` pin it (500, the problem bytes, then the stream error; one hook call). The order is deterministic: the HTTP/2 server's flush waits until the DATA frames are written, and the reset is queued behind them on the same stream.
- **Nested recovery (fourth review, revised in the fifth).** A real panic stops at the innermost recovery, so an outer one sees its handler return and reports nothing. A Goexit does not stop: the outer recovery sees its handler not return, recovers nil and reports a second `*PanicError{nil}`, with a nil problem since the response has started (gin's `Recovery`, which checks `Written()` after `RespondPanic`, gives it the unsent 500 problem).
  - *Fourth review.* An internal mark on the request's response writers (`internal/recovermark`, `trackingWriter.MarkNilRecovered`, plus a gin context key) let an outer recovery that recovered nil skip its report when an inner one had set the mark.
  - *Fifth review: the mark is removed (owner's direction: simplify, a duplicate report is safer than a lost one).* The mark could not tell a Goexit still unwinding from a new, real `panic(nil)` under `panicnil=1`: an inner recovery that answered a `panic(nil)` and returned left the mark set, and a middleware between the two recoveries that then called `panic(nil)` was swallowed by the outer one, never reaching the hooks (`TestRecover_Nested_SecondPanicNil`, gin `TestRecovery_Nested_SecondPanicNil`). Two narrower fixes were weighed. Setting the mark only for a Goexit is impossible, since the inner recovery cannot tell Goexit from `panic(nil)` either. Clearing the mark once the inner recovery's handler returns normally (only a Goexit keeps it) fixes the reproduced case but not a `panic(nil)` raised by a deferred function while the Goexit unwinds: Go recovers it in the outer recovery with `recover()` returning nil, the mark still set, and then resumes the Goexit, so the outer recovery sees exactly what it sees for the Goexit alone (checked with a standalone program under `panicnil=1`). No same-unwind dedup can therefore be proven safe, so every recovery reports a handler that did not return, and a Goexit through nested recoveries is reported once per recovery. `internal/recovermark`, `MarkNilRecovered`, `trackingWriter.nilRecovered` and gin's context key are deleted. A non-nil panic value was never deduplicated and still is not. Pinned by `TestRecover_Nested` (panic once; Goexit twice, the second with a nil problem, also through a wrapping middleware), gin's `TestRecovery_Nested` and `TestRecovery_Nested_CoreRecoverInsideHandler`, and the two `SecondPanicNil` tests. The godocs of `Recover` and `Recovery` say so. Nested recovery is rare outside tests, and `Goexit` in a handler is `t.FailNow` in a test, so the extra report costs little; a lost report of a real panic would hide a bug.

**fiber.** `Recovery` uses the same returned-normally flag, so a `panic(nil)` under `panicnil=1` is answered with the safe 500 and reported (fiber-adapter "Recovery with the adapter's error handler", `TestRecovery_ErrorHandler_PanicNil`). A `runtime.Goexit` cannot be answered under fiber: fasthttp writes the response, and fiber calls the error handler, on the handler's goroutine after the handler returns, and Goexit ends it first, so a flush has nothing to send. The client gets no answer and the hooks hear nothing; the app keeps serving. Writing to the connection from `Recovery` was rejected: `Recovery` cannot tell Goexit from `panic(nil)`, after which fasthttp writes the response itself, so the client would get two. Documented on `Recovery` and pinned by `TestRecovery_Goexit`.

For gin, `Recovery` builds the `*PanicError` (stack captured in its defer) and calls `RespondPanic`. If `c.Writer.Written()` or the result is false, it writes nothing. Because `Written()` is checked after `RespondPanic`, the hooks of a started response receive the unsent 500 problem, where net/http `Recover`, which checks first, gives them a nil problem. The specs only require that the hook receives the panic, so this stays; the `RespondPanic` and `Recovery` godocs say so, and gin's `TestRecovery_Edge` "partial body flushed before the panic" pins it. The hook today receives `panickedError`, which unwraps to `*PanicError`, and will now receive the `*PanicError` itself. Both satisfy "an error that unwraps to a panic error". `panickedError` stays only for gin's second-panic report, `reportPanic`.

**Failure modes:**
- The writer panics while `RespondPanic` is answering: `writeFallback` in net/http and `reportPanic` in gin are unchanged.
- An enricher panics: it is contained by `safeCall`.
- A hook panics: it is contained by `callOne`.
- A started response: gin writes nothing, and its hook sees the 500 problem as today.

### D5. `safeCall` uses a return-normally flag
The pattern:

```go
ok := false
defer func() {
	if !ok {
		v := recover()
		...
	}
}()
r := fn()
ok = true
return r
```

This catches `panic(nil)` under `panicnil=1`, where `v == nil`. The report is a `*PanicError{Value: nil}`.

**Not shared with `errwalk`'s `call` (task 4.1).** Both use the flag, but nothing else is common: `call` records an `Abort` on the walk and always captures the stack; `safeCall` reports to the hooks, captures the stack only when a hook will receive it, and returns the zero value. They also live in different packages, so a shared helper would be exported from an internal package and take the panic action as a callback, adding a closure on hot paths to save about five lines. Decision: keep both, each documenting the flag.

The same flag is used by every `recover` in the workspace (sixth review audit): `safeCall`, `recoverPanic` and `writeFallback` in the core, `errwalk`'s `call`, gin's `recoverPanic`, `reportPanic` and `respond`, fiber's `recoverPanic` and `respond`, and validatormap's `recoverInto`, which still tested `recover() == nil` and so contained a translator's `panic(nil)` under `panicnil=1` without reporting it; `translatorFor` and `translate` now call it through a small generic `guard`, the shape of the core's `safeCall`, which also tells `translate` whether the translation returned. The two guards that swallow whatever they recover (the core's `callOne` for hooks and the `writeFallback` write guard) need no flag, since they treat every outcome alike. gormmap and pgxmap contain no `recover`. validatormap imports only the core's public API, so its fix needs no core v0.3.0.

It cannot tell `runtime.Goexit` from that `panic(nil)`: both leave `ok` false with `recover` returning nil, and the goroutine keeps exiting. The defer then reports a panic with value nil. That is acceptable: `Goexit` in consumer code is `t.FailNow` in a test, and the report is harmless. The handler-level recovery makes the same choice (D4).

### D6. Adapter inspections never report
In ginprblm and fiberprblm, each chain question builds its own `errwalk.Walk`:

- client gone;
- the hint;
- route not found.

A recovered panic is never route-not-found (fourth review): fiber's `isRouteNotFound` walked through `recoveredError` into the panic value, so a panic wrapping `fiber.ErrNotFound` on an unrouted documentation path got the 200 documentation and the hooks never received it. It now returns false when the walk holds a `*prblm.PanicError` (what `recoveredError` unwraps to, so any panic carried as an error counts), found with the new `errwalk.Has[T]`, a type assertion alone that calls no `As` method, so a returned error whose `As` panics is still route-not-found as before (`TestRecovery_PanicOnUnroutedDocPath`, `TestErrorHandler_Discovery_AsPanicNotFound`). The other checks that see through a recovered panic need no change: fiber's `withHint` gives a hint that the self-describing `recoveredError` overrides, and `isClientGone` only skips the write to a client whose request is done, while the hooks still receive the panic once (`TestRecovery_ValueSeenThroughByAdapterChecks`). gin's `Recovery` never records the panic with `c.Error`, so `Middleware`'s checks never see it.

On an abort the question gets its safe answer:

- "not gone": answer the request;
- "no hint";
- "not route-not-found".

The adapter walk reports nothing, because the core's own walk of the same error reports exactly once. `isClientGone` walks only when the request context is already done, so the common path pays nothing. `withHint` adds one walk per fiber error. That is acceptable: the cost is a few entries, and the benchmark shows it.

**The adapters' status hint is not an entry.** gin's `c.AbortWithError` code and fiber's `*fiber.Error` code used to reach the core through `prblm.WithStatus`, whose wrapper is an entry the walk charges, so a 1,000-entry error answered by an adapter became 1,001 entries and aborted (a review reproduced it in both adapters). The adapters now wrap the error in `internal/statushint.Error`, and the core's `newResolution` walks the `Err` inside an `*statushint.Error` at the root, and knows the hint at once (D2). Only a non-nil `*statushint.Error` is a hint: a nil one, which a consumer can make with `reflect.Zero` from the wrapper its hook receives, is an ordinary typed-nil root (an empty walk, the safe 500), where an unchecked `outer.Err` once made the core panic (task 11.1). Consumer code still receives the wrapper, as it received `WithStatus`'s, with the same message and `Unwrap`. A consumer's own `WithStatus` stays an entry, specified by the `WithStatus` rows of "Budget boundary" and the requirement "Only the application's status hints are entries". No exported identifier is added.

Alternatives considered:
- The walker does not charge `*statusError`. Rejected: a consumer's own `WithStatus` would become free too, and a chain of nested `WithStatus` would be unbounded.
- Passing the hint in the context. Rejected: it allocates a context per error, and the hooks would receive the error without the hint, a behavior change.
- A public field or option for the hint (for example on `prblm.Request`). Rejected: new public API for adapter plumbing.

**Failure modes:**
- The adapter's walk aborts while the core's walk succeeds. This can happen when a different target makes an `Is` method panic. The request is then answered normally, with no note, because the core saw nothing wrong. This is what the fiber-adapter and gin-adapter specs say (an `As` panicking only for a `*fiber.Error`, an `Is` panicking only for `context.Canceled`), pinned by fiber's `TestErrorHandler_AdapterWalkAborts` and gin's `TestMiddleware_ClientGone_OnlyTheCheckAborts`.
- Version skew: the adapters import `prblm/internal/errwalk` and `prblm/internal/statushint`, so ginprblm and fiberprblm v0.3.x require core ≥ v0.3.0. `errwalk`'s API is kept backward compatible across core minor versions, or the adapters are released together with the core (see Risks).

### D7. `WithReplacedHeader(name, value string) Field`
The field does three things:

- it canonicalizes `name`;
- it calls `Header.Add`;
- it appends `name` to `p.replaced` (unexported, deduplicated).

`clone` copies `replaced`. `render` passes `p.replaced` to `newResponse`, and `Response` gains an unexported `replaced []string`.

`MergeInto` replaces `dst[name]` for any name in `r.replaced`, with `Vary` and `Cache-Control` included. `Vary` in `r.Header` already lists `Accept`, because `newResponse` added it. `Content-Type` and `Content-Length` were always replaced. The constant fallback response carries no marks, so its headers merge by default rules.

Alternatives considered:
- A `Problem.ReplaceHeader` method. Rejected: `Field` is how one-expression problems are built.
- A handler option listing header names. Rejected: it is per problem, not per handler.

**Failure modes:**
- The consumer deletes the header from `p.Header` after marking it (an enricher, for example). The mark must replace nothing, as `WithReplacedHeader`'s godoc says. `MergeInto` only iterates `r.Header`, but `newResponse` fills `Vary` and `Cache-Control` with the library's defaults (`Accept`, `no-store`), so a leftover mark on either made the default replace the handler's values (a fifth-review repro: `Vary` became `["Accept"]` instead of `["Origin" "Accept"]`). `newResponse` now keeps only the marks whose header the problem still has (`presentOnly`), before filling the defaults, so the defaults merge by the default rules. It clones the list only when it drops a mark, so the common path allocates nothing and the problem's own list is never changed. The adapters all merge through `MergeInto`, so the fix covers net/http, gin and fiber (`TestWrap_ReplacedHeader`, `TestMiddleware_ReplacedHeader`, `TestErrorHandler_ReplacedHeader`, "the marked header was deleted" rows).
- The handler set the header under a non-canonical key (sixth review). net/http sends a key written directly into the header map (`w.Header()["cache-control"]`) as written, and fiber keeps the handler's spelling under `DisableHeaderNormalizing`, where fasthttp's `PeekAll`/`Del` match the key exactly. `MergeInto` only looked at `dst[name]`, so a replaced `Cache-Control` went out next to the handler's (`["no-store" "public, max-age=600"]`), an unmarked one did not yield to it, and `Vary` got a second line; fiber had the same gap in its own projection. The same gap hit the default rules, so the fix is not specific to marks: `MergeInto` first folds every non-canonical spelling of one of the response's header names into the canonical key (`foldSpellings`: the canonical key's values first, then the other keys in sorted order so the result is deterministic, the folded keys deleted), and then applies the rules unchanged. `foldSpellings` takes a filter and also backs `canonicalize`, which folds every key of the problem's own header the same way; that fold used to append in map order, so two spellings of one problem header now merge in a deterministic order too. Keys of headers the response does not set, and keys that are not valid field names (which `http.CanonicalHeaderKey` returns unchanged), are left alone. Cost: one pass over `dst`'s keys; `CanonicalHeaderKey` returns a canonical key without allocating, so the common path allocates nothing (BenchmarkWrap allocs/op and B/op unchanged), and only a handler that wrote a non-canonical key pays for the sort and the copies. fiber's `send` now projects the current values by visiting the response headers once (`ResponseHeader.All`) and matching names with `strings.EqualFold` on equal lengths (ASCII folding only, as `CanonicalHeaderKey`; a Kelvin sign never matches `k`), keyed by the spelling found (the problem's own name string when the spelling is canonical, so no allocation: BenchmarkErrorHandler allocs/op and B/op unchanged), deletes every projected key before merging, since the header must not change during `All`, and writes back the merged canonical names. It does so whatever the configuration, rather than reading fiber's config, so one code path is tested with normalizing on and off (`TestResponse_MergeInto`, `TestResponse_MergeInto_Replaced`, `TestWrap_ReplacedHeader`, gin `TestMiddleware_ReplacedHeader`, fiber `TestErrorHandler_ReplacedHeader`, the non-canonical rows).
- A mark set from a `Received` problem: impossible, because decoding never sets marks.
- A `Response` built by an adapter by hand, such as the bare 500: no marks, as before.

## Defaults

- **Bounded walk:** always on, with the budget fixed at 1,000. No option exists, because a bypass would reopen the hang.
- **Abort answer:** the safe 500 with no detail. It cannot be changed: the error is not trustworthy enough to map.
- **Header merge:** the existing rules, applied to every spelling of a header name the handler used. `WithReplacedHeader` is the explicit, named opt-in, set per problem.
- **`RespondPanic`:** adapter API. With no hooks it allocates no note.

## Hot Paths

| Path | Error branches to cover |
|---|---|
| `errwalk.New` (flatten) | nil root, typed-nil root, nil entry, typed-nil entry, `Unwrap() error` nil, `Unwrap() []error` empty, budget exactly 1,000 and 1,001, children push over budget, `Unwrap` panic, `panic(nil)` under panicnil |
| `Walk.Is` / `As` / `Has` | `Has` by type alone past a panicking `As`, `==` match, `Is` method match, uncomparable target, `==` panic, `Is` panic, `As` panic, `As` true with nil pointer/interface target, `As` true with zero value-type target, scan after abort |
| `Handler.match` | abort before step 1, abort in each step, describer typed nil, describer `Problem()` panic, walk mappers versus plain mappers, hint stop index, adapter hint at the root (error and non-error status) |
| `Resolve` / `Respond` | note with a hook, no hook, silenced context, note order |
| `RespondPanic` / `Recover` | nil `pe`, broken, not broken, aborted walk, unformattable abort value, started response, writer panic, `panic(nil)` under panicnil (handler and writer), `runtime.Goexit` (flushed, unflushable writer, panicking `Flush`, real server over HTTP/1.1 and HTTP/2, nested: reported by each recovery, a second `panic(nil)` after the inner one returned, inside an error method), plan9 build |
| `safeCall` | normal return, panic value, `panic(nil)` default, `panic(nil)` under panicnil |
| gin `Recovery` / `isClientGone` | each D4/D6 branch, `panic(nil)` under panicnil (handler and writer), `runtime.Goexit` (flushed, unflushable writer, panicking `Flush`, real server over HTTP/1.1 and HTTP/2, nested in gin and in core: reported by each recovery, a second `panic(nil)` after the inner one returned), only the adapter's walk aborting |
| gin `respond`, fiber `respond` | `Respond` panicking with a value and with `panic(nil)` under panicnil (a context whose `Value` panics), a nil handler |
| gin `withHint`, fiber `withHint` | hint around 1,000 and 1,001 entries, only the adapter's walk aborting |
| fiber `Recovery` | panic value, `panic(nil)` under panicnil, `runtime.Goexit` (not answerable) |
| fiber `withHint` / `isClientGone` / `isRouteNotFound` / `respond` | each D6 branch, a recovered panic on an unrouted documentation path, the bare-500 recover |
| `MergeInto` / `newResponse` | replaced and unreplaced headers, `Vary`, `Cache-Control`, nil dst, nil header, fallback response, a marked header deleted from the problem (`Vary`, `Cache-Control`, another header, no handler value), a handler value under a non-canonical key (replaced, `Cache-Control` kept, `Vary` merged, several spellings, a spelling without values, an unrelated key, an invalid field name) |
| fiber `send` | header normalizing on and off, a handler value under a lowercase or upper-case key, the problem's names in canonical spelling |
| validatormap `recoverInto` | translator and translation panicking with a value, with `http.ErrAbortHandler`, and with `panic(nil)` by default and under panicnil |

Coverage is checked with `go test -coverprofile` per module, not assumed.

## Risks / Trade-offs

- [`internal/errwalk` and `internal/statushint` couple adapter releases to the core] → The adapters require core ≥ v0.3.0. `errwalk`'s exported-to-module surface (`New`, `Walk` and its methods `Is`, `IsAny` and `Aborted`, `As`, `Has`, `Abort`, `Reason` with `ReasonBudget` and `ReasonPanic`, `Budget`) and `statushint`'s (`Wrap`, `Error` with its fields `Err` and `Status`) are treated as stable across core v0.x minors. A breaking change there means releasing all three modules together. RELEASING.md gets a note.
- [Behavior change: a panicking `Is`/`As` now fails closed where v0.2.1 continued] → It is called out as a behavior change in the proposal and CHANGELOG. Pre-1.0 minor bump. The change is the maintainer's shared rule.
- [Per-entry reflection for the typed-nil check costs time] → One `reflect` kind check per entry; the scans skip `call` for entries that cannot panic (D1 "Fast paths"). The measured change goes in the PR (task 8.2).
- [The heap-allocated `Walk` adds 224 B/op per resolution] → Accepted: allocs/op stays equal to v0.2.1. Pooling was rejected (D1 "Allocation").
- [The adapter and the core walk the same error twice in fiber (hint)] → A short chain costs nanoseconds. It is bounded by the budget anyway.
- [A consumer `Mapper` written by hand can still hang on a cycle] → It never runs: the walk aborts before any mapper. A consumer hook that walks the error itself can still hang. This is documented in the package doc and on `ErrUnwalkableError`.
- [Nested recovery reports a `runtime.Goexit` once per recovery (D4)] → Accepted: no dedup can tell it from a new `panic(nil)` under `panicnil=1`, and a duplicate report is safer than a lost one. Goexit in a handler is `t.FailNow` in a test.
- [Go `stdversion`/vet] → No new API beyond Go 1.27.

## Migration Plan

1. One PR with all slices. Adapters use `replace github.com/kartaladev/prblm => ../..`. CI runs both modes.
2. When the maintainer asks, tag core `v0.3.0` per RELEASING.md and publish its GitHub Release.
3. Second PR: drop the `replace`, require core v0.3.0 in ginprblm and fiberprblm, then `GOWORK=off go mod tidy` and the test suite.
4. Tag `adapters/ginprblm/v0.3.0` and `adapters/fiberprblm/v0.3.0` with their GitHub Releases. Tell scrty (session `security-refactor`).
5. Rollback: consumers pin v0.2.1. Nothing is persisted, so there is no data migration.

## Open Questions

None. fiber's `Recovery` and `panic(nil)` under `panicnil=1`, open in the first review, is decided and fixed (D4 "fiber").
