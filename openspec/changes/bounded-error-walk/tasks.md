# Tasks

## 1. Slice: one bounded walk per error; the core fails closed through net/http

- [x] 1.1 Flatten in errors.Is order (error-chain-walk / Scenario: First match follows errors.Is order — the walker's order); verify `go test -race -run TestNew_Order ./internal/errwalk` passes
  - RED: new package `internal/errwalk`, `errwalk_test.go`, `TestNew_Order`.
    - Table: a single error, a `%w` chain, a nested `errors.Join`, `Unwrap() error` returning nil, an empty `Unwrap() []error`, and `[nil, X]`.
    - Asserts the list order, which equals the order `errors.Is` visits. Fails: the package does not exist.
  - GREEN: `New(err) *Walk` with an explicit stack: pop, charge, push children in reverse. `List()` is test-only, in `export_test.go`.
  - REFACTOR: look for Long Function in the flatten loop and Extract Function (`push`), then `/simplify`.
- [x] 1.2 Budget of 1,000 entries (error-chain-walk / Scenario: Budget boundary — walker level); verify `go test -race -run TestNew_Budget ./internal/errwalk` passes
  - RED: `TestNew_Budget`. The rows give the reason and the list length:
    - a 1,000-entry chain;
    - a 1,001-entry chain;
    - 998 nil entries + X;
    - 1,000 children;
    - 20,000 self copies;
    - an entry at 1,001 whose `Unwrap` records calls (never called);
    - a self-loop through `Unwrap() error`, through `Unwrap() []error` and through `errors.Join`;
    - 25 `Join(n, n)` levels.
    
    A `testing.AllocsPerRun` assertion keeps the 20,000-copies case under 2 allocations.
  - GREEN: charge before calling any method. Check `examined+pending+len(children) > Budget` before pushing. `Abort{Reason: Budget}`.
  - REFACTOR: Replace Magic Number with the `Budget` constant; consider Introduce Parameter Object for the counters. `/simplify`.
- [x] 1.3 Typed nils are charged and skipped (error-chain-walk / Scenario: Typed nil in the chain — walker level); verify `go test -race -run TestNew_TypedNil ./internal/errwalk` passes
  - RED: rows:
    - a typed-nil root gives an empty list, not aborted;
    - a typed-nil entry whose value-receiver `Unwrap` would panic is not called;
    - a nil slice error is walked;
    - a nil map error is walked;
    - a typed nil is counted toward the budget (999 entries + 2 typed nils aborts).
  - GREEN: `isTypedNil` with reflect `Kind == Pointer && IsNil`.
  - REFACTOR: `/simplify`.
- [x] 1.4 Panics abort, including `panic(nil)` (error-chain-walk / Scenario: Error methods panic — walker level); verify `go test -race -run TestNew_Panic ./internal/errwalk` passes, and `GODEBUG=panicnil=1 go test -run TestNew_PanicNil ./internal/errwalk` passes
  - RED: rows:
    - `Unwrap() error` panics `"boom"`;
    - `Unwrap() []error` panics;
    - `Unwrap` calls `panic(nil)`, giving a `*runtime.PanicNilError` value.
    
    Asserts reason `Panic`, the value, and that the stack contains the panicking method's frame. `TestNew_PanicNil` is skipped unless `GODEBUG` contains `panicnil=1`, and asserts value `nil` with reason `Panic`.
  - GREEN: a `call` helper with a return-normally flag and `debug.Stack()` in its defer.
  - REFACTOR: look for Duplicated Code across the call sites and Extract Function into the single `call` helper. `/simplify`.
- [x] 1.5 `Is` and `As` scans with a sticky abort (error-chain-walk / Scenarios: Error methods panic, An As that sets nothing); verify `go test -race -run 'TestWalk_(Is|As)' ./internal/errwalk` passes
  - RED: `TestWalk_Is` gives the index of the first match. Rows:
    - an `==` match;
    - an `Is`-method match;
    - an uncomparable target (no `==`, no panic);
    - a comparable struct target whose `any` field holds a slice (`==` panics, abort);
    - an `Is` panic (abort);
    - a scan after an abort returns -1.

    `TestWalk_As` rows:
    - a direct type;
    - an `As` method;
    - an `As` panic (abort);
    - true with a nil pointer target (no match);
    - true with a nil interface target (no match);
    - true with a zero `int` target (match);
    - a typed nil in the list (never there).
  - GREEN: `(*Walk).Is(target) int`, `As[T any](w *Walk) (T, int)` and `(*Walk).Aborted() *Abort`.
  - REFACTOR: look for Duplicated Code between the two scans and extract the guarded per-entry loop. `/simplify`.
- [x] 1.6 Resolution scans one walk and fails closed with one note (error-chain-walk / Scenarios: Cycles and fan-out [net/http rows], Budget boundary, Typed nil in the chain, Error methods panic, An aborted walk is reported once, Note text; error-mapping / Scenario: Error methods panic; error-sentinels / Scenario: Degenerate inputs); verify `go test -race -run 'TestResolve_(Walk|Unwalkable|FaultyError|Degenerate)' ./` passes
  - RED: in `hook_test.go` and `sentinels_test.go`, rewrite `TestResolve_FaultyError` to the new rows: fail closed, the `ErrUnwalkableError` note first, then the error. Add:
    - `TestResolve_Unwalkable`, through `Handler.Wrap` with a 1 s timeout guard: cycles, fan-out, the budget rows, the one-note hook combinations, and the note text without `secret-dsn`;
    - `TestResolve_WalkOrder`.

    Expected failure: a timeout or hang on the cycle rows (guarded), a fatal stack overflow on the Join cycle (run that row in a subprocess for RED), 413 on the typed-nil `*http.MaxBytesError` row, 500 on the typed-nil `*Problem` row, and the old hook sequence.
  - GREEN:
    - `ErrUnwalkableError` with godoc.
    - `match` builds one `*errwalk.Walk`.
    - `selfDescribed` uses `errwalk.As[Describer]`.
    - The built-ins become an unexported `walkMapper`.
    - `mapSentinel` becomes "hint index, then first sentinel before it", and `firstSentinel` and `ownSentinel`'s recursion are deleted.
    - `hinted` reuses the hint index.
    - Fail closed after each step.
    - `Resolve` reports the note with the completed 500 problem.
  - REFACTOR: look for Feature Envy (steps reaching into the walk) and Move Function into a small per-resolution struct; Remove Dead Code (`firstSentinel`). `/simplify`.
- [x] 1.7 Concurrency (error-chain-walk / Scenario: Concurrent hostile errors); verify `go test -race -run TestResolve_ConcurrentHostile ./` passes
  - RED: 64 goroutines per row against one handler. It should pass once 1.6 is green, so run it against a stash of 1.6 and record that it hangs there.
  - GREEN: none expected.
  - REFACTOR: `/simplify`.
- [x] 1.8 Docs: `ErrUnwalkableError` and the `Resolve` godoc (steps, fail-closed); the `doc.go` paragraph at :57 now says cycles are answered with the safe 500; and "consumer code calling `errors.Is`/`errors.As` directly still sees typed nils". Add an `ExampleErrUnwalkableError` in a hook. Verify with `go test -run Example ./` and `go doc -all . | grep -n -i -e walk -e cycle`.
- [x] 1.9 Verify slice 1:
  - `go test -race -coverprofile=c.out ./... ./internal/errwalk && go tool cover -func=c.out` shows `New`, `call`, `Is`, `As`, `isTypedNil`, `match`, `selfDescribed`, `mapSentinel` and `hinted` covered, each error branch included.
  - `go vet ./...`, `golangci-lint run ./...` and gopls `go_diagnostics` on the changed files are clean.
  - `GOWORK=off go test ./...` passes in the core.

## 2. Slice: `prblm.Is`, `prblm.As` and `prblm.Status` match by the bounded walk

- [x] 2.1 The helpers scan the handler's walk (error-mapping / Scenarios: Sentinel helper, Typed helper exposes the value [typed nils and empty results]); verify `go test -race -run '^(TestIs|TestAs|TestMapError_Direct)$' ./` passes (`TestMapError_Direct` holds the `Status` rows; there is no `TestStatus`)
  - RED: add the typed-nil and empty-As rows to `mapper_test.go`. Expected failure: the typed-nil `*RefusedError` row matches the nil (panic or wrong reasons), and the empty-As row gives 409.
  - GREEN: `Is`, `As` and `Status` return an unexported `walkMapper` type. `MapError` builds its own walk. `mapWalk` scans the given one.
  - REFACTOR: Remove Middle Man if `Status` only forwards. `/simplify`.
- [x] 2.2 Helpers called directly are bounded (error-mapping / Scenario: Helper called directly); verify `go test -race -run TestMapError_Direct ./` passes
  - RED: the four rows with a 1 s timeout guard. Expected failure: the cycle row hangs.
  - GREEN: none beyond 2.1, unless `MapError` still uses `errors.Is`.
  - REFACTOR: `/simplify`.
- [x] 2.3 Consumer code sees the error as returned (error-chain-walk / Scenarios: What consumer code receives, Empty As results); verify `go test -race -run 'TestResolve_(ConsumerSeesOriginal|EmptyAs)' ./` passes
  - RED: the rows as written. The `MapperFunc` records its argument.
  - GREEN: none expected beyond 1.6 and 2.1.
  - REFACTOR: `/simplify`.
- [x] 2.4 Godoc of `Is`, `As` and `Status`: they match by the bounded walk, skip typed nils, and treat an empty pointer or interface `As` result as no match. Verify by reading `go doc . Is`.
- [x] 2.5 Verify slice 2: coverage of the helpers' `MapError` and `mapWalk`, both paths. Lint, vet, diagnostics and `GOWORK=off go test ./...` are clean.

## 3. Slice: `Handler.RespondPanic`, and net/http `Recover` uses it

- [x] 3.1 Framework-neutral panic response (panic-recovery / Scenario: Panic response without writing); verify `go test -race -run TestRespondPanic ./` passes
  - RED: the rows as written in `respond_test.go`:
    - `"boom"`;
    - EPIPE;
    - ECONNRESET;
    - a self-loop;
    - an `Is` panic;
    - a nil `*PanicError`.

    The panicnil row runs under `GODEBUG=panicnil=1` in a subprocess test. Fails: the method does not exist.
  - GREEN: `RespondPanic` per design D4. `brokenConnErrors` goes in `conn.go`, and `conn_plan9.go` returns nil.
  - REFACTOR: look for Duplicated Code between `respondPanic` and `RespondPanic`, and Inline Function on the old helper. `/simplify`.
- [x] 3.2 `Recover` answers hostile panic values (panic-recovery / Scenarios: Hostile panic value, Hostile panic value without a hook); verify `go test -race -run 'TestRecover_HostileValue' ./` passes
  - RED: update the rows (one note, then the panic), and add the self-loop, Join-cycle and typed-nil rows with a 1 s guard. Expected failure: the self-loop row hangs today.
  - GREEN: `recoverPanic` keeps the abort re-panic and the `tw.started` report, then calls `RespondPanic` under `writeFallback`. Delete `isBrokenConnection`.
  - REFACTOR: `/simplify`.
- [x] 3.3 Godoc for `RespondPanic` (with an Example) and the `Recover` godoc. Verify with `go test -run ExampleHandler_RespondPanic ./`.
- [x] 3.4 Verify slice 3:
  - `RespondPanic` and `recoverPanic` branches are covered.
  - The plan9 build compiles: `GOOS=plan9 GOARCH=amd64 go vet ./...`.
  - Lint, vet, diagnostics and `GOWORK=off` are clean.

## 4. Slice: `safeCall` reports `panic(nil)`

- [x] 4.1 Consumer code calling `panic(nil)` is reported (error-mapping / Scenarios: Consumer code panics [the panic(nil) row], panic(nil) in consumer code under GODEBUG=panicnil=1; error-chain-walk / Scenario: What consumer code receives [the panic(nil) row]); verify `go test -race -run TestResolve_ConsumerPanicNil ./` and `GODEBUG=panicnil=1 go test -run TestResolve_ConsumerPanicNil_PanicNil1 ./` pass
  - RED: the panicnil rows: a mapper, an enricher, and a describer's `Problem`. Expected failure, confirmed at origin/main by the proposal's probe: 0 hook reports under panicnil=1.
  - GREEN: the return-normally flag in `safeCall` (design D5).
  - REFACTOR: compare with `errwalk`'s `call` helper. Shared code is not worth a cross-package helper unless it simplifies; decide and record the choice. `/simplify`.
- [x] 4.2 Verify slice 4: `safeCall`'s branches are covered in both GODEBUG modes; lint, vet and diagnostics are clean.

## 5. Slice: ginprblm uses the bounded walk

- [x] 5.1 Wire the unreleased core: add `replace github.com/kartaladev/prblm => ../..` to `adapters/ginprblm/go.mod` (RELEASING.md allows this until the core tag), then run `GOWORK=off go mod tidy`. Verify `GOWORK=off go build ./...` in `adapters/ginprblm`.
- [x] 5.2 Recovery goes through `RespondPanic` (gin-adapter / Scenario: Hostile panic value under gin; error-chain-walk / Scenario: Cycles and fan-out [gin rows]); verify `go test -race -run 'TestRecovery_HostileValue|TestMiddleware_Unwalkable' ./` passes in `adapters/ginprblm`
  - RED: update the rows (one note, then the panic) and add the cyclic rows with a 1 s guard. Expected failure: the self-loop panic hangs in `isBrokenConnection`, and the `Is`-panic rows lack the note.
  - GREEN: `recoverPanic` builds the `*PanicError` and calls `h.RespondPanic`; it writes when the result is true and `!c.Writer.Written()`. Delete `conn.go` and `isBroken`. Keep `reportPanic` and `panickedError` for writer panics.
  - REFACTOR: Remove Dead Code. `/simplify`.
- [x] 5.3 Client-gone check is bounded (gin-adapter / Scenario: Client went away); verify `go test -race -run TestMiddleware_ClientGone ./` passes in `adapters/ginprblm`
  - RED: add the canceled self-loop and `Is`-panic rows with a 1 s guard. Expected failure: a hang.
  - GREEN: `isClientGone` uses `errwalk.New(err).Is(context.Canceled) >= 0` only when the context is done.
  - REFACTOR: `/simplify`.
- [x] 5.4 Update the gin godoc (`Recovery`, the `respond` comment about core v0.2.0) and `adapters/ginprblm/CHANGELOG.md` `[Unreleased]`. Verify with a read-through.
- [x] 5.5 Verify slice 5:
  - `go test -race -coverprofile` and `go tool cover -func` in `adapters/ginprblm` show `recoverPanic` and `isClientGone` covered.
  - `internal/paritytest` passes.
  - Lint, vet, diagnostics and `GOWORK=off go test ./...` are clean.

## 6. Slice: fiberprblm uses the bounded walk

- [x] 6.1 Wire the unreleased core as in 5.1, for `adapters/fiberprblm`. Verify `GOWORK=off go build ./...`.
- [x] 6.2 Hint, route-not-found and client-gone are bounded; hostile errors get the full safe 500 (fiber-adapter / Scenarios: Fiber-generated and handler fiber errors [the typed-nil row], Errors that cannot be walked, Client went away; error-chain-walk / Scenario: Cycles and fan-out [fiber rows]); verify `go test -race -run 'TestErrorHandler_(Unwalkable|FiberError|ClientGone)' ./` passes in `adapters/fiberprblm`
  - RED: the rows as written, with a 1 s guard. Expected failure, confirmed at origin/main by the proposal's probe: the `As`-panic row gives a bare 500 with no `Content-Type` and 0 hook calls; the cyclic rows hang.
  - GREEN: `withHint`, `isRouteNotFound` and `isClientGone` use `errwalk`. On an abort, each gives its safe answer. The bare-500 `recover` in `respond` stays and its comment is updated.
  - REFACTOR: look for Duplicated Code among the three predicates; Extract Function only if one walk can serve two of them in one call. `/simplify`.
- [x] 6.3 Update the fiber godoc and `adapters/fiberprblm/CHANGELOG.md` `[Unreleased]`.
- [x] 6.4 Verify slice 6:
  - Coverage of `withHint`, `isClientGone`, `isRouteNotFound` and `respond`.
  - `internal/paritytest` passes.
  - Lint, vet, diagnostics and `GOWORK=off go test ./...` are clean.

## 7. Slice: `WithReplacedHeader`

- [x] 7.1 Marking a header (problem-document / Scenarios: Marking a header, Package-level problems keep their marks); verify `go test -race -run 'TestWithReplacedHeader|TestResolve_ReplacedHeaderShared' ./` passes
  - RED: the default and opt-in rows, plus the 64-goroutine shared-problem row. Fails: undefined `WithReplacedHeader`.
  - GREEN: the `Field`; `p.replaced` (canonical, deduplicated); `clone` copies it.
  - REFACTOR: `/simplify`.
- [x] 7.2 Replaced headers win in every adapter (problem-response / Scenarios: Headers on the response, Replaced headers win over existing ones); verify `go test -race -run 'TestResponse_MergeInto|TestWrap_ReplacedHeader' ./`, plus the gin and fiber equivalents in their modules, all pass
  - RED: every row, including `Vary` (`Cookie, Accept`), the protected `Content-Type` and `Content-Length`, the encoding-fallback row, and the gin and fiber rows. Expected failure: `Cache-Control` stays `public, max-age=600`.
  - GREEN: `newResponse` takes the marks into `Response.replaced`, and `MergeInto` replaces the marked names.
  - REFACTOR: look for a Switch Statement smell in `MergeInto` and decide between Replace Conditional with Polymorphism and keeping the switch. `/simplify`.
- [x] 7.3 Godoc for `WithReplacedHeader` (reason: a problem forcing `no-store`) and `MergeInto`, plus an `ExampleWithReplacedHeader`. Verify with `go test -run ExampleWithReplacedHeader ./`.
- [x] 7.4 Verify slice 7: `MergeInto` and `newResponse` branches are covered in all three modules; lint, vet, diagnostics and `GOWORK=off` are clean.

## 8. Slice: benchmarks, documentation and changelogs

- [x] 8.1 Add the micro-benchmarks:
  - a `BenchmarkResolve` case `wrapped` (`fmt.Errorf("%w")` around an unmapped error, 3 entries);
  - `BenchmarkResolve` case `unwalkable` (a self-loop);
  - `BenchmarkNew` in `internal/errwalk` (1, 4 and 16 entries).

  Each checks its result before timing, per BENCHMARKS.md. List them in BENCHMARKS.md. Verify with `go test -run '^$' -bench . -benchtime 1x ./...`.
- [ ] 8.2 Benchmark on an idle machine following the BENCHMARKS.md protocol: v0.2.1 (5742e04) against this branch, `-count` as the protocol says, then `benchstat`. Commit the raw results under `bench/results/`. Update the README "Performance" numbers only if the protocol says so. Report every regression beyond noise in the PR, with its cause, and optimize before merging if the walk regresses `mapped-json` or `fallback-500`.
- [x] 8.3 Update the README, the root `CHANGELOG.md` `[Unreleased]` (the behavior change in bold: a panicking `Is`/`As` now fails closed; the new `ErrUnwalkableError`, `RespondPanic` and `WithReplacedHeader`) and RELEASING.md (adapters importing `internal/errwalk` are released in step with the core). Verify with a read-through.
- [x] 8.4 Full check, as RELEASING.md "Before tagging" step 1:
  - every module, with and without `GOWORK=off`;
  - `go test -race`;
  - `golangci-lint`;
  - the cross-OS `go vet` loop;
  - `GOEXPERIMENT=nojsonv2 go test ./...`;
  - `go_vulncheck`;
  - `openspec validate bounded-error-walk --strict`.

## 9. Review fixes

- [x] 9.1 The `ErrUnwalkableError` note formats nothing while answering (design D3); verify `go test -race -run 'TestRespond_UnformattableAbortValue|TestRespond_HookFormatsUnformattableNote|TestRespondPanic' ./`, gin `TestRecovery_HostileValue_UnformattableAbort` and fiber `TestErrorHandler_Unwalkable_UnformattableValue` pass
  - RED: against the `fmt.Errorf` note, `Respond`, `Wrap` and `RespondPanic` panic, gin crashes with "panic while printing panic value", and fiber sends a bare 500 with 0 hook calls.
  - GREEN: the unexported `unwalkableNoteError` with a lazy `Error()` and `Unwrap() []error`.
- [x] 9.2 A handler's `panic(nil)` under `GODEBUG=panicnil=1` is answered (panic-recovery / Scenario: Hostile panic value; gin-adapter / Scenario: Hostile panic value under gin; design D4); verify `go test -race -run 'TestRecover_HostileValue_PanicNil|TestRecover_Goexit|TestRecover_Faulty_WriterPanicsNil' ./` and gin `TestRecovery_HostileValue_PanicNil|TestRecovery_Goexit|TestRecovery_Edge_WriterPanicsNil` pass
  - RED: with `recover() != nil`, an empty 200 and 0 hook calls; the writer rows lose the second report.
  - GREEN: returned-normally flags in `recoverPanic`, `writeFallback`, gin `recoverPanic` and `reportPanic`. `runtime.Goexit` is answered as a panic with value nil.
- [x] 9.3 Missing rows: `TestResolve_FaultyError_PanicNil` (error-chain-walk / Scenario: panic(nil) under GODEBUG=panicnil=1), the typed-nil rows of `TestResolve_SelfDescribing` with mapper call counts, and fiber `TestRecovery_ErrorHandler_SelfLoopValue`.

## 10. Second review fixes

- [x] 10.1 fiber `Recovery` answers `panic(nil)` under `GODEBUG=panicnil=1` (fiber-adapter / Scenario: Recovery with the adapter's error handler [hostile panic values]; design D4 "fiber"); verify `go test -race -run 'TestRecovery_ErrorHandler_PanicNil|TestRecovery_ErrorHandler_SelfLoopValue|TestRecovery_Goexit' ./` in `adapters/fiberprblm`
  - RED: `TestRecovery_ErrorHandler_PanicNil` (rerun under panicnil=1): status 200, empty body, 0 hook calls.
  - GREEN: a returned-normally flag in `recoverPanic`. `runtime.Goexit` cannot be answered under fiber; `TestRecovery_Goexit` pins that and the godoc says so.
- [x] 10.2 `runtime.Goexit` over a real server reaches the client (panic-recovery / Scenario: Hostile panic value [Goexit row]; gin-adapter / Scenario: Hostile panic value under gin [Goexit rows]; design D4); verify `go test -race -run 'TestRecover_Goexit' ./` and gin `TestRecovery_Goexit` pass
  - RED: `TestRecover_Goexit_RealServer` and gin `TestRecovery_Goexit_RealServer`: the client got `EOF`; the problem stayed in net/http's buffer.
  - GREEN: flush after writing the problem when the recovered value is nil (`trackingWriter.FlushError`, gin `c.Writer.Flush`). Unflushable and panicking-`Flush` writers are covered.
- [x] 10.3 The adapters' status hint costs nothing of the budget (error-chain-walk / Scenarios: Budget boundary [`WithStatus` rows], Adapter status hints cost nothing; design D6); verify `go test -race -run 'TestResolve_WalkBudget|TestResolve_Unwalkable$' ./`, gin `TestMiddleware_AbortWithError_Budget` and fiber `TestErrorHandler_FiberError_Budget` pass
  - RED: the 1,000-entry rows gave the safe 500 with a budget note in both adapters.
  - GREEN: the adapters wrap the error in `internal/statushint.Error`; `newResolution` walks the error inside one at the root and keeps its hint outside the walk. No exported identifier is added.
- [x] 10.4 Only the adapter's walk aborting is answered as usual (fiber-adapter / Scenarios: Errors only the adapter cannot walk, Client went away [the `Is`-only row]; gin-adapter / Scenario: Client went away [the `Is`-only row]; design D6); verify fiber `TestErrorHandler_AdapterWalkAborts` and gin `TestMiddleware_ClientGone_OnlyTheCheckAborts` pass
  - The code was right; the spec, `adapters/fiberprblm/doc.go`, the `isClientGone` comments and the CHANGELOGs promised the safe 500, and are reworded.
- [x] 10.5 Helpers called directly do not look up the hint (error-mapping / Scenario: Helper called directly [the `As`-past-the-match row]; design D2); verify `go test -race -run 'TestMapError_Direct|TestWalkFunc_MapError' ./`
  - RED: `prblm.Status(errVesselNotFound, 404).MapError` on `errors.Join(errVesselNotFound, asPanicError{})` returned nil.
  - GREEN: `walkFunc.MapError` no longer calls `findHint`; `mapSentinel` calls it, and `findHint` looks the hint up once per resolution.
- [x] 10.6 Records: the `call`/`safeCall` decision (design D5), task 7.2's verify pattern, fiber-adapter rows for `TestRecovery_ErrorHandler_SelfLoopValue`, and design Open Questions closed. The coverage gate found `typeCache.store`'s skip branch (namespace.go, unchanged here) reached only when requests race; `TestTypeCache_StoreSkips` drives it directly.

## 11. Third review fixes

- [x] 11.1 A nil adapter hint wrapper is an ordinary typed nil (error-chain-walk / Scenario: Typed nil in the chain [the status-hint wrapper rows]; Requirement: A nil adapter hint wrapper is a typed nil, Scenario: Typed-nil adapter hint wrapper); verify `go test -race -run 'TestResolve_WalkTypedNil|TestMapError_Direct' ./`, fiber `TestErrorHandler_TypedNilStatusHint` and gin `TestMiddleware_TypedNilStatusHint` pass
  - RED: with a typed-nil `*statushint.Error` root, `Respond` and `Status(...).MapError` panicked with a nil pointer dereference in `newResolution`, and fiber and gin sent a bare 500 with 0 hook calls.
  - GREEN: `newResolution` takes the hint only from a non-nil `*statushint.Error`; a nil one gives an empty walk and the safe 500. The fiber test helper `isSame` had the same unchecked assertion and gets the same check. No other code asserts or dereferences a `*statushint.Error`.
- [x] 11.2 (fiber-adapter / Scenario: Route-not-found check cannot finish; design D6) The `isRouteNotFound` godoc says what D6 does: a route-not-found walk that cannot finish answers the error as usual, with no note; verify fiber `TestErrorHandler_AdapterWalkAborts` passes, with its "route-not-found walk aborts" row (an unmatched route, discovery on, an error whose `Is` panics only for `fiber.ErrNotFound`: a 404, one hook call, no note).
- [x] 11.3 Docs and test records:
  - the `Resolve` godoc says the `Is`, `As` and `Status` mappers of steps 2 and 3 scan the walk too;
  - `TestNew_PanicNil` in `internal/errwalk` reruns itself under `GODEBUG=panicnil=1` instead of skipping;
  - design Risks and RELEASING.md list the whole module-internal surface of `errwalk` (`IsAny`, `ReasonBudget`, `ReasonPanic` added) and of `statushint`;
  - gin `Recovery` checks `c.Writer.Written()` after `RespondPanic`, so on a started response the hooks receive the unsent 500 problem, where net/http `Recover` gives them a nil problem. The specs only require that the hook receives the panic, so the code stays; the `RespondPanic` and `Recovery` godocs, design D4 and the gin CHANGELOG say so, and gin `TestRecovery_Edge` pins it;
  - both adapter CHANGELOGs name core v0.3.0 instead of the design.

## 12. Fourth review fixes

- [x] 12.1 A recovered panic is never fiber's route-not-found (fiber-adapter / Requirement: Opt-in recovery returns panics to the error handler, Scenario: Panic on an unrouted documentation path; design D6); verify fiber `go test -race -run 'TestRecovery_PanicOnUnroutedDocPath|TestErrorHandler_Discovery_AsPanicNotFound|TestRecovery_ValueSeenThroughByAdapterChecks' .` and `go test -race -run TestWalk_Has ./internal/errwalk/` pass
  - RED: with discovery on, a middleware panicking with `fmt.Errorf("tenant lookup: %w", fiber.ErrNotFound)` (and with `fiber.ErrNotFound` itself) under `Recovery` on `/v1/api/probs/out-of-credit` got `200 text/html` and the hooks received nothing.
  - GREEN: `isRouteNotFound` returns false when the walk holds a `*prblm.PanicError` (Recovery's `recoveredError` unwraps to one; checking the panic error rather than the wrapper covers any panic carried as an error). A first fix with `errwalk.As` called the `As` methods of the error and turned a returned error wrapping `fiber.ErrNotFound` whose `As` panics into a 500 (`TestErrorHandler_Discovery_AsPanicNotFound` RED); the check now uses the new `errwalk.Has[T]`, a type assertion alone (`TestWalk_Has`, with its allocation row).
  - Checked, no change needed: fiber `withHint` (the self-describing `recoveredError` overrides the hint) and `isClientGone` (only the write to a done request is skipped; the hooks get the panic once), pinned by `TestRecovery_ValueSeenThroughByAdapterChecks`; gin's `Recovery` never records the panic with `c.Error`, so `Middleware`'s checks never see it.
- [x] 12.2 Goexit over HTTP/2 is documented as it behaves (panic-recovery / Scenario: Hostile panic value [the HTTP/2 Goexit row]; gin-adapter / Scenario: Hostile panic value under gin [the HTTP/2 row]; design D4); verify `go test -race -run TestRecover_Goexit_HTTP2 ./` and gin `TestRecovery_Goexit_HTTP2` pass
  - RED (review repro): over HTTP/2 the client read the 500 and the problem's bytes, then `stream error: stream ID 1; INTERNAL_ERROR; received from peer`, against the godocs' "the client receives it complete".
  - GREEN (docs only): `Recover`'s and `Recovery`'s godocs, design D4, both Goexit spec rows and the core and gin CHANGELOGs say the complete answer holds over HTTP/1.x, and over HTTP/2 the problem's bytes arrive followed by a stream reset, the hook report still happening. The tests pin that: status 500, `application/problem+json`, the exact body, then a stream error with `INTERNAL_ERROR`, one hook call (stable over `-count=200` with `-race`).
- [x] 12.3 Nested recovery reports a Goexit once (panic-recovery / Requirement: Nested recovery reports a panic once; gin-adapter / Requirement: Nested recovery reports a panic once; design D4). **Superseded by 13.1:** `internal/recovermark` was deleted and the requirement renamed "Nested recovery never loses a panic"; do not re-run the verify below, run 13.1's instead. Original verify: `go test -race -run 'TestRecover_Nested' ./`, `go test -race ./internal/recovermark/` and gin `go test -race -run 'TestRecovery_Nested' .` pass
  - RED: `h.Recover(h.Recover(exiting()))` reported `*PanicError{nil}` twice, the second with a nil problem; gin's `Recovery` twice, and the core's `Recover` around a gin engine with `Recovery`, also reported twice.
  - GREEN: new `internal/recovermark`: `Mark(w)` marks every writer holding a mark down `w`'s `Unwrap` chain (bounded at 32, panic-proof) and reports whether one was marked already. The core's `trackingWriter` holds the mark; `recoverPanic` returns without a report when a nil recovery finds it set. gin's `Recovery` marks its gin context and `c.Writer`'s chain. No exported identifier is added.
  - Limit, documented on `Recovery` and in D4 and pinned by `TestRecovery_Nested_CoreRecoverInsideHandler`: the core's `Recover` inside a gin handler under `Recovery` still reports a Goexit twice. fiber cannot answer a Goexit, so it is not affected.
- [x] 12.4 The note's message names only the panic value's type (error-chain-walk / Requirement: An aborted walk is reported once, Scenario: Note text; design D3); verify `go test -race -run 'TestResolve_UnwalkableNote|TestResolve_FaultyError_PanicNil|TestRespond_HookFormatsUnformattableNote|ExampleErrUnwalkableError' ./` and the gin and fiber suites (exact message in `assertNoted`) pass
  - RED: an error whose `Unwrap` panics with itself gave the note `prblm: error chain cannot be walked: panic: prblm: recovered panic: secret-dsn`; the unformattable-value row crashed the test binary (`panic while printing panic value`); with the old message, gin's and fiber's tightened `assertNoted` failed on every panic note.
  - GREEN: `unwalkableNoteError.Error` is `fmt.Sprintf("%v: %s: %T", ErrUnwalkableError, reason, value)`: `string`, `prblm_test.selfPanicUnwrapError`, `*prblm_test.selfPanicError`, `*runtime.PanicNilError` for `panic(nil)`, `<nil>` under `panicnil=1`. `%T` calls no method of the value, so the lazy-formatting caveat is gone from the godoc. `TestRespond_HookFormatsUnformattableNote` was vacuous (its formatting hook was replaced by a second `OnError`); it now uses `AddErrorHook` and checks the formatted messages.
- [x] 12.5 Records: task 2.1's verify pattern names `TestMapError_Direct` instead of the missing `TestStatus`; the two copies of `rerunUnderPanicNil` (`internal/errwalk`, fiberprblm) stay in their modules and were checked against the core's and gin's; design D3, D4, D6, Hot Paths and Risks, RELEASING.md and the three CHANGELOGs record 12.1 to 12.4. The review repro files are folded into the tests above and removed.

## 13. Fifth review fixes

- [x] 13.1 Nested recovery never loses a panic (panic-recovery / Requirement: Nested recovery never loses a panic; gin-adapter / Requirement: Nested recovery never loses a panic; design D4); verify `go test -race -run 'TestRecover_Nested' ./`, `GODEBUG=panicnil=1 go test -race -run 'TestRecover_Nested' ./` and gin `go test -race -run 'TestRecovery_Nested' .` pass
  - RED (review repro): under `GODEBUG=panicnil=1`, an inner `Recover` answered a real `panic(nil)` and set the mark; a middleware between the two then called `panic(nil)` again, and the outer `Recover` took it for a Goexit already reported: `hook calls = 1, want 2`. gin's `Recovery` twice, with a middleware between them panicking with nil after `c.Next()`, did the same.
  - Decision: remove the mark rather than refine it. Neither recovery can tell a Goexit still unwinding from a new `panic(nil)`: clearing the mark when the inner handler returns normally fixes the reproduced case, but a `panic(nil)` from a deferred function during the Goexit is recovered by the outer recovery with the mark set and the Goexit then resumes, exactly as for the Goexit alone (checked with a standalone program). A duplicate report is safer than a lost one, so every recovery reports a handler that did not return.
  - GREEN: `internal/recovermark` (package and tests), `trackingWriter.nilRecovered` and `MarkNilRecovered`, and gin's `nilRecoveredKey`/`markNilRecovered` are deleted. `TestRecover_Nested` and gin's `TestRecovery_Nested` now expect a Goexit to be reported by each recovery (the outer core one with a nil problem), a panic once; `TestRecovery_Nested_CoreRecoverInsideHandler` is no longer a documented limit but the same rule. New `TestRecover_Nested_SecondPanicNil` and gin `TestRecovery_Nested_SecondPanicNil`. The godocs of `Recover` and `Recovery`, D4, the specs, Risks, RELEASING.md, the proposal and the core and gin CHANGELOGs say so.
- [x] 13.2 A panicking `Respond` gets the bare 500 for `panic(nil)` too (gin-adapter / Requirement: The middleware answers a panicking Respond with a bare 500; fiber-adapter / Requirement: The error handler answers a panicking Respond with a bare 500; design D2); verify gin `GODEBUG=panicnil=1 go test -race -run TestMiddleware_RespondPanics .` and fiber `GODEBUG=panicnil=1 go test -race -run TestErrorHandler_RespondPanics .` pass (each test also reruns itself under `panicnil=1`)
  - RED (review repro): a request context whose `Value` panics with nil, under `panicnil=1`, made `Respond` panic in `scopedMappers`; gin's and fiber's `respond` tested `recover() != nil` and answered `status = 200, want the bare 500`.
  - GREEN: both `respond` functions use the returned-normally flag.
  - Decision: the core does not contain a panicking `ctx.Value` in `scopedMappers`. A context is not a library extension point, its `Value` is called everywhere else too, containment would add a closure and a defer to every resolution to hide a consumer bug as "no scoped mappers", and no spec asks for it. Recorded in design D2.
- [x] 13.3 A mark on a deleted header replaces nothing (problem-response / Scenario: Replaced headers win over existing ones [the marked header was deleted]; design D7); verify `go test -race -run TestWrap_ReplacedHeader ./`, gin `TestMiddleware_ReplacedHeader` and fiber `TestErrorHandler_ReplacedHeader` pass
  - RED (review repro): an enricher deleted a `WithReplacedHeader` header from the problem; `newResponse` then filled the defaults, and the leftover mark replaced the handler's values: `Vary = ["Accept"], want ["Origin" "Accept"]`, `Cache-Control = ["no-store"], want ["public, max-age=60"]`. The new adapter rows failed the same way in gin and fiber.
  - GREEN: `newResponse` keeps only the marks whose header the problem still has (`presentOnly`), before filling the defaults; it clones the list only when it drops a mark. All three adapters merge through `MergeInto`, so the one fix covers them.
- [x] 13.4 Records and docs: proposal.md (What Changes, Capabilities, Impact) covers nested recovery, fiber's route-not-found rule for recovered panics, Goexit over HTTP/1.1 and HTTP/2, the type-only note, 13.2 and 13.3, and the adapters' `internal/errwalk` and `internal/statushint` imports; design labels section 12 as the fourth review and this one as the fifth; D3 no longer claims a Goexit gives a `<nil>` note (`TestRecover_GoexitInErrorMethod` pins that a Goexit inside an error method gives no note); the gin and fiber `assertNoted`/`noteEntries` comments describe the note's `Unwrap() []error` entries and message; the gin and fiber CHANGELOGs illustrate the note with `*mypkg.Err`; the README describes Goexit (HTTP/1.x answered, HTTP/2 reset, fiber unanswerable); fiber's doc.go and `ErrorHandler` godoc say a recovered panic on an unrouted documentation path gets the safe 500. The review repro files are folded into the tests above and removed.

## 14. Sixth review fixes

- [x] 14.1 Every spelling of a header name is that header in the merge, net/http and gin (problem-response / Requirement: Response headers, Scenario: A handler header under a non-canonical key; design D7); verify `go test -race -run 'TestResponse_MergeInto|TestWrap_ReplacedHeader' ./` and gin `go test -race -run TestMiddleware_ReplacedHeader .` pass
  - RED (review repro, `TestReviewRepro_ReplacedHeader_NonCanonicalHandlerKey`): a handler wrote `w.Header()["cache-control"] = []string{"public, max-age=600"}` and the problem used `WithReplacedHeader("Cache-Control", "no-store")`; a real server sent `Cache-Control values sent = ["no-store" "public, max-age=600"], want only [no-store]`. New rows failed the same way for the default rules: an unmarked `Cache-Control` was added beside a lowercase handler key, a lowercase `vary` got a second `Vary: Accept`, two spellings of `Content-Type` both survived. The gin rows (direct writes to `c.Writer.Header()`) failed identically with the core fix disabled.
  - GREEN: `MergeInto` first folds every non-canonical key of one of the response's names into the canonical key (`foldSpellings`: canonical values first, then the other keys sorted, the folded keys deleted, values copied), then applies the rules unchanged; the `Cache-Control` rule reads `dst[name]` directly. Unrelated keys and invalid field names are left alone. `foldSpellings` takes a filter and now also backs `canonicalize`, which became deterministic.
  - Cost: `BenchmarkWrap` allocs/op and B/op identical with and without the fold (6 runs each, benchstat); ns/op differences were within this machine's noise.
- [x] 14.2 The same in fiber under `DisableHeaderNormalizing` (problem-response / Scenario: A handler header under a non-canonical key [fiber rows]; design D7); verify fiber `go test -race -run TestErrorHandler_ReplacedHeader .` passes (every row runs with normalizing on and off)
  - RED (review repro, `TestReviewRepro_ReplacedHeader_DisableHeaderNormalizing`): `c.Set("cache-control", ...)` with the config on gave `["public, max-age=600" "no-store"], want only [no-store]`; `send` read and deleted under the canonical name only, which fasthttp matches exactly in that mode. The new `raw=true` rows (replaced, unmarked `Cache-Control`, `LINK`) failed the same way.
  - GREEN: `send` visits the response headers once (`ResponseHeader.All`), keeps those whose name matches one of the problem's names in ASCII case (`spelling`: equal length and `strings.EqualFold`, returning the problem's own name string when the spelling is canonical), deletes every projected key before merging, then lets `MergeInto` fold and writes the canonical names back. One code path whatever the configuration.
  - Cost: a first version converted every matched key to a string (+1 alloc/op, +16 B/op on `BenchmarkErrorHandler`); returning the problem's name for a canonical spelling removed it, so allocs/op and B/op are identical to before.
- [x] 14.3 validatormap reports a translator's `panic(nil)` under `panicnil=1` (validation-error-mapping / Requirement: Translator panics are contained once; design D5); verify validatormap `go test -race -run TestMapper_TranslatorPanicNil .` and `GODEBUG=panicnil=1 go test -race -run TestMapper_TranslatorPanicNil .` pass (the test reruns itself under `panicnil=1`)
  - RED (review repro, `TestReviewRepro_TranslatorPanicNil_UnderPanicNil`): `hook found 0 panic errors, want the translator's panic(nil) reported once`; `recoverInto` tested `recover() == nil`. The new test failed for the translator and for a translation calling `panic(nil)`.
  - GREEN: `recoverInto` takes the returned-normally flag; `translatorFor` and `translate` call the guarded function through a small generic `guard`. `WithTranslator`'s godoc names `panic(nil)`. The module imports only the core's public API: `GOWORK=off` (core v0.1.0) passes in both modes, so it needs no core v0.3.0. New `mappers/validatormap/CHANGELOG.md` with an [Unreleased] entry.
  - Audit of every `recover()` in the 8 workspace modules (production code): core `safeCall` (panic.go), `recoverPanic` and `writeFallback` (handler.go) use the flag, `callOne` and the `writeFallback` write guard swallow everything by design; `internal/errwalk` `call` uses the flag; gin `recoverPanic`, `reportPanic` (recovery.go) and `respond` (middleware.go) use the flag; fiber `recoverPanic` (recovery.go) and `respond` (handler.go) use the flag; validatormap `recoverInto` fixed here. gormmap, pgxmap, `internal/paritytest` and `internal/plugintest` have no `recover`. Test-only `recover()` calls assert on panic values and are not recovery paths.
- [x] 14.4 Records: proposal.md names the modified `error-mapping` requirement "Received problems are never relayed implicitly" (a typed-nil `*Received` now gives 500, not 502), the header-spelling and validatormap changes, and validatormap, gin and fiber in Impact; task 12.3 is marked superseded by 13.1; the `replace` comments in the gin and fiber `go.mod` name `internal/statushint` too; the root CHANGELOG records that a hint before a sibling sentinel decides (`errors.Join(prblm.WithStatus(errors.New("x"), 418), prblm.ErrNotFound)`: 404 at v0.2.1, checked against a `git archive` of v0.2.1, 418 now), pinned by two new `TestResolve_WalkOrder` rows; the root, gin, fiber and validatormap CHANGELOGs, design D5, D7, Defaults and Hot Paths record 14.1 to 14.3; new delta `specs/validation-error-mapping`. The review repro files are folded into the tests above and removed. `/simplify` on this pass's production code: `foldSpellings` shared with `canonicalize`, `len(dst[name])` in the `Cache-Control` rule, one `EqualFold` per name with a length check in fiber's `spelling`, validatormap's `guard`; the per-module `returned` flag pattern stays duplicated across modules, since sharing it would need exported API.

## Workflow follow-up

- Open the PR (no session link in commits or body), wait for CI, read every check, and merge only if all pass.
- Before any shared walk rule changes, tell scrty (session `security-refactor`). After merge, run `/opsx:verify`, then archive the change.
- When the maintainer asks, tag core `v0.3.0` per RELEASING.md, with its GitHub Release. Then open a PR that drops the adapters' `replace` and requires core v0.3.0; after CI passes and it merges, tag `adapters/ginprblm/v0.3.0` and `adapters/fiberprblm/v0.3.0` with GitHub Releases, and tell scrty that v0.3.0 is tagged.
- Independently of the v0.3.0 tags, once this change is merged: tag `mappers/validatormap/v0.1.1` (it still requires core v0.1.0), with its GitHub Release.
