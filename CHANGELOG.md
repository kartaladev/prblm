# Changelog

All notable changes to this project are documented in this file. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/). Until v1.0.0 the public API may still change in minor
releases.

Each module has its own tags (see [RELEASING.md](RELEASING.md)). This file covers the root module,
`github.com/kartaladev/prblm`.

## [Unreleased]

### Changed (BREAKING)

- **A panicking `Unwrap`, `Is` or `As` method of the error being answered now fails closed.** In
  v0.2.1 the resolution step that met the panic counted it as no match and went on, so another step
  could still map the error. Now the panic aborts the error's walk, and the answer is the safe 500 even
  when another step would have matched: an error the handler cannot inspect is not safe to map.
  The hooks receive one note wrapping `ErrUnwalkableError` (which wraps the `*PanicError`), then
  the error, instead of one or more `*PanicError` reports.
- **A status hint and a sentinel side by side: the first in `errors.Is` order decides.** In
  v0.2.1 the sentinel step ran before the status-hint step, so
  `errors.Join(prblm.WithStatus(errors.New("x"), 418), prblm.ErrNotFound)` gave 404. Every step
  now scans the one walk of the chain, and the hint comes first in that order, so the answer is
  418. With the sentinel first, `errors.Join(prblm.ErrNotFound, prblm.WithStatus(...))`, it is
  still 404, and a hint wrapped around a sentinel still wins, as before.

### Added

- `ErrUnwalkableError`. The error chain is now walked once per error, before the first resolution
  step, within a budget of 1,000 entries. A chain that loops back on itself, nests too deep, fans
  out too wide, or whose `Unwrap`, `Is` or `As` method panics, is answered at once with the safe
  500 problem, and the hooks first receive a note wrapping `ErrUnwalkableError` with the reason,
  `budget` or `panic`. The note never contains the error's text: for reason `panic`, its message
  names only the panic value's type, as in `prblm: error chain cannot be walked: panic: *mypkg.Err`
  (`<nil>` for a nil value), and the `*PanicError` it wraps holds the value for a hook that chooses
  to inspect it.
- `Handler.RespondPanic`, the panic counterpart of `Respond` for framework adapters: it turns a
  recovered `*PanicError` into the safe 500 response `Recover` writes, without writing it, or
  reports false when the client connection is broken.
- `WithReplacedHeader`, a `Field` that sets a header on a problem and makes it replace the value
  the handler or a middleware already set, in every adapter. It lets a problem force
  `Cache-Control: no-store` over a route's `public, max-age=...`. `WithHeader` keeps the default
  merge rules. A mark whose header is deleted from the problem (by an enricher, say) replaces
  nothing: the library's `Vary` and `Cache-Control` defaults then merge as usual.

### Changed

- A typed nil (a nil pointer) in the error chain is skipped, and its methods are never called. The
  built-in mappers and the `Is`, `As` and `Status` helpers no longer match it.
- An `As` method that reports a match but leaves a pointer or interface target nil is no match. A
  zero value of another kind, such as 0 for an `int` error type, still matches, as `errors.As`
  counts it.
- A typed-nil `*Received` in the chain is skipped like any typed nil, so it no longer resolves as
  `ErrBadGateway`: on its own it gives the safe 500, not a 502.
- `Is`, `As` and `Status` mappers called directly through `MapError` no longer look up the status
  hint they never use, so an `As` method of the error past the match can no longer make them
  decline.
- Resolution walks the error once instead of about nine times for an unrecognized error. The
  benchmarks are in [BENCHMARKS.md](BENCHMARKS.md).

### Fixed

- `Respond` and `Wrap` no longer hang on an error whose `Unwrap() error` returns itself, or crash
  the process with a stack overflow on an `errors.Join` that contains itself, and a fan-out such as
  25 nested `errors.Join(n, n)` no longer takes seconds: such an error gets the safe 500 at once,
  with an `ErrUnwalkableError` note.
- `Recover` no longer hangs on a panic value whose chain loops back on itself: it answers the safe
  500 after one bounded walk, with one note.
- A typed-nil `*Problem` joined ahead of a real problem no longer hides it: the real problem is
  sent instead of a 500. A typed-nil `*http.MaxBytesError` no longer gives a false 413, nor does
  an `As` method that reports a match and sets nothing.
- A `panic(nil)` in a mapper, describer or enricher is reported to the hooks as a `*PanicError`,
  also under `GODEBUG=panicnil=1`, where it used to be contained without a report.
- `Recover` answers a handler's `panic(nil)` under `GODEBUG=panicnil=1` with the safe 500 and
  reports it as a `*PanicError` with value nil. It used to tell a panic apart by `recover`
  returning non-nil, so the request got an empty 200 and the hooks heard nothing. The same holds
  for a writer that calls `panic(nil)` while the problem is written: the constant minimal problem
  is written and the panic reported. A `runtime.Goexit` in the handler, which `recover` cannot
  tell from that `panic(nil)`, is answered the same way, and the problem is flushed, because the
  goroutine then ends before net/http finishes the response. Over HTTP/1.x the client receives the
  problem complete; over HTTP/2, net/http then resets the stream of the handler that did not
  return, so the client receives the problem's bytes and then a stream reset (`INTERNAL_ERROR`).
  A writer that cannot flush keeps the problem unsent. The hooks are told in every case. Nested
  `Recover`s report a panic once, but a Goexit goes on through the outer ones, and each reports it
  again with a nil problem: under `GODEBUG=panicnil=1` they cannot tell it from a new `panic(nil)`
  in the code between them, which must never go unreported.
- `Response.MergeInto`, and so `Wrap`, `Write` and `Recover`, treats a header the handler set under
  a non-canonical key as that header. A handler that writes the map directly,
  `w.Header()["cache-control"] = ...`, makes net/http send that key as written, so a problem's
  `WithReplacedHeader("Cache-Control", "no-store")` used to go out next to the handler's
  `public, max-age=600`, an unmarked `Cache-Control` was added beside the handler's instead of
  yielding to it, and `Vary` got a second line instead of merging. Every spelling of a response
  header's name is now folded into the canonical name before the merge rules apply (the canonical
  key's values first, then the others in sorted key order), and the other spellings are deleted.
  Keys of headers the response does not set are left as they are. A handler that sets headers with
  `Header().Set` or `Add`, the common case, pays one pass over its header keys and no allocation.
- Building the `ErrUnwalkableError` note no longer formats the panic value, so a panic value whose
  `Error` method panics (an `Is` method that panics with such an error) can no longer make
  `Respond`, `Wrap`, `Recover` or `RespondPanic` panic. The note's message names only the value's
  type, so a hook can format it safely too, and an error that panics with itself no longer leaks
  its own text through the note.

## [0.2.1] - 2026-11-19

### Fixed

- A panic in the error's own methods no longer escapes the handler. An error whose `As` method
  panics, or reports a match without setting its target, used to escape `Respond` from the
  status-hint step: through `Wrap`, net/http dropped the connection, and the hooks never heard of
  the error. That step now counts the panic as no match, like every other step, reports it as a
  `*PanicError`, and the request gets the safe 500 problem. An error chain that loops back on
  itself is still not detected: resolving it never returns, or overflows the stack, as with
  `errors.Is`.
- `Recover` answers a panic whose value is an error with a panicking `Is` method. The broken
  connection check used to let that second panic escape `Recover`, so the client got no response.
  The connection now counts as working: the client gets the safe 500 problem, and the hooks receive
  the second panic and then the original one.

## [0.2.0] - 2026-11-18

### Added

- `AddErrorHook`, which adds error hooks that `OnError` never replaces, so a library that wraps prblm
  keeps its hook when its consumer passes `OnError`. Each report calls the `OnError` hook first, then
  the added hooks in the order added, and a hook that panics no longer stops the hooks after it.

### Changed (BREAKING)

- `Parse` returns a `*Received` instead of a `*Problem`. `Received` holds the decoded problem in
  its `Problem` field. It is an error, but not a `Describer`, and it reports `ErrBadGateway`
  through `errors.Is`, so `errors.Is(err, prblm.ErrBadGateway)` is now true for an error that
  wraps a received problem. To migrate, read the problem through the field:

  ```go
  // Before
  p, err := prblm.Parse(res)
  fmt.Println(p.Status)

  // After
  r, err := prblm.Parse(res)
  fmt.Println(r.Problem.Status) // or p := &r.Problem
  ```

  Client code that takes a `Parse` result out of an error chain with `errors.As(err, &p)`, where
  `p` is a `*prblm.Problem`, no longer matches: a `*Received` doesn't unwrap to its problem. Use
  `r *prblm.Received` as the target, then read `r.Problem`.

### Changed

- Problem responses are cheaper, with the same output bytes. An XML problem takes 38 allocations
  and about 2.2 KiB instead of 86 and 9 KiB, and is about 57% faster on the net/http request path;
  every problem response makes 3 fewer allocations and is 3% to 8% faster. The benchmarks, the
  measurement protocol and the comparisons are in [BENCHMARKS.md](BENCHMARKS.md).
- The gin and fiber adapters moved to `github.com/kartaladev/prblm/adapters/ginprblm` and
  `github.com/kartaladev/prblm/adapters/fiberprblm`, released as v0.2.0 alongside this version.
  Their package names and APIs are unchanged; see their CHANGELOG.md files.

### Fixed

- Received problems no longer leak (#8). A problem from `Parse` in an error chain, even inside an
  outer sentinel such as `fmt.Errorf("%w: %w", prblm.ErrBadGateway, r)`, used to answer with the
  upstream's status, detail and extension members. It now resolves like `ErrBadGateway`, a safe
  502, under the usual sentinel rules (see the `Received` godoc). Relaying it is an explicit
  `return &r.Problem`. The godoc of `Problem.UnmarshalJSON` and `Problem.UnmarshalXML` warns that
  a problem decoded by hand still describes itself.

## [0.1.0] - 2026-11-16

The first release.

### Added

- `Problem`, the RFC 9457 problem details document, built in one expression with `New` and field
  options (`WithType`, `WithTitle`, `WithDetail`): standard members, `about:blank` and title
  defaults, validated extension members (`Set`, `Ext`, `WithExt`), response headers that are never
  serialized (`WithHeader`), and a hidden cause for `errors.Is` and `errors.As` (`WithCause`).
- Deterministic JSON encoding and lenient JSON decoding.
- XML encoding and lenient decoding per RFC 9457 Appendix B, with the same structure as the JSON.
- `Handler` with a fixed resolution order: self-describing errors (`Describer`), request-scoped
  mappers (`ScopeMappers`), consumer mappers (`WithMappers`, `Is`, `As`, `Status`, `MapperFunc`),
  built-in mappers for malformed, truncated and oversized request bodies and deadline errors
  (`WithoutBuiltins`; a bare `io.EOF` is not mapped, because any reader can return it), status hints
  (`WithStatus`) and a safe 500 fallback.
- Sentinel errors for common outcomes, to return or wrap: `ErrInvalidInput` (400),
  `ErrUnauthenticated` (401), `ErrPermissionDenied` (403), `ErrNotFound` (404), `ErrConflict`
  (409), `ErrPreconditionFailed` (412), `ErrRateLimited` (429), `ErrBadGateway` (502),
  `ErrUnavailable` (503) and `ErrUpstreamTimeout` (504). Each maps, with no configuration, to an
  `about:blank` problem with a fixed detail; the wrap text never reaches the client. They are
  built-in mappers, so consumer code overrides them and `WithoutBuiltins` turns them off. A
  sentinel beats a standard library error in the same chain; between two sentinels, or a sentinel
  and a `WithStatus` hint, the outer one decides. No sentinel problem carries `Retry-After` or
  `WWW-Authenticate`; the godoc shows how to add them.
- Enrichers (`WithEnrichers`) with a default instance from the request path, and an `OnError` hook
  for logging. Panics in mappers, enrichers and the hook are contained and reported. Notes about
  mistakes the handler worked around wrap `ErrInvalidStatus` (a status outside 400–599) or
  `ErrEncoding` (a problem that could not be encoded).
- `Respond`, the framework-neutral response step, with `Accept` negotiation between JSON and XML,
  `Vary` and `Cache-Control` headers, protected content headers, and a constant fallback when
  encoding fails. `Response.MergeInto` merges a response's headers into a framework's headers by
  the same rules as the net/http adapter, so every adapter produces the same headers.
- The net/http adapter: `Wrap` for handlers that return errors, `Write` for standard handlers, and
  opt-in panic recovery with `Recover` and `PanicError`. `io.Copy` through `Wrap` keeps the
  underlying writer's `ReadFrom`, so net/http still uses sendfile.
- `Parse`, which decodes a problem from an HTTP response, and `ErrNotProblem`.
- `WithNamespace`, an application-wide namespace for problem types: relative sub-types such as
  `contracts/blacklisted-supplier` are resolved under it, absolute URIs and paths are kept, and an
  invalid relative type becomes `about:blank`, titled with the status phrase, with a hook note
  wrapping `ErrInvalidType`. Without a namespace, a relative type is written as given and also gets
  that note. Notes are only built when a hook is set. Types are never built from the request's
  `Host` header. `NewHandler` panics on an invalid namespace; the last `WithNamespace` wins.
- `DefineType`, which defines a problem type once (sub-type, status, title and description) as an
  immutable `Type`, and `Type.New`, which builds problems of that type for mappers. `WithTypes`
  registers definitions with the handler; an invalid definition (such as a zero-value `Type`), a
  duplicate sub-type or types without a namespace panic in `NewHandler`. A nil or zero-value `Type`
  builds the safe 500 problem, and its accessors return zero values.
- Discovery: with types registered, `Handler.Discovery` serves each type's documentation at its type
  URL (HTML, or JSON when preferred) and an index at `Handler.DiscoveryPath`, rendered once on
  first use, escaped, sent with `nosniff` and a restrictive CSP, and cacheable with a strong `ETag`.
  `Handler.RespondDiscovery` and `DiscoveryRequest` give framework adapters the same response
  without net/http. `WithoutDiscovery` turns it off. A sub-type without a definition gets a hook
  note wrapping `ErrUndocumentedType`. Discovery's own 404 and 405 problems never reach the hook.
- Shadowed documentation: an application route inside the namespace can hide a documentation page
  or the index. `Handler.NoteShadowedRoute` gives the hook a note wrapping
  `ErrShadowedDocumentation`, naming the route and the path, once per route (at most 256 routes),
  and builds nothing without a hook. This note arrives on a request that succeeded, so the hook is
  no longer called only for answered errors. The gin and fiber adapters report through it; net/http
  has no hook point, so there the conflict is only documented.
- `mappers/sqlmap`, an opt-in mapper for database/sql errors that uses only the standard library:
  `sql.ErrNoRows` becomes a 404 problem, and `driver.ErrBadConn` and `sql.ErrConnDone` become a 503
  problem, each with a fixed, generic detail. `sql.ErrTxDone` and other errors are left to later
  mappers. `WithNotFound` and `WithUnavailable` replace either problem.

[Unreleased]: https://github.com/kartaladev/prblm/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/kartaladev/prblm/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/kartaladev/prblm/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/kartaladev/prblm/releases/tag/v0.1.0
