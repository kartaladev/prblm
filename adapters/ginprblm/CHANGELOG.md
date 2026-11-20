# Changelog

All notable changes to the `github.com/kartaladev/prblm/adapters/ginprblm` module are documented in
this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
module uses [Semantic Versioning](https://semver.org/). Its tags are `adapters/ginprblm/vX.Y.Z` (see
[RELEASING.md](../../RELEASING.md)). Version 0.1.0 was published as
`github.com/kartaladev/prblm/ginprblm`, tagged `ginprblm/v0.1.0`, and is described in the root
[CHANGELOG.md](../../CHANGELOG.md).

## [Unreleased]

### Changed

- Requires core v0.3.0, which brings the bounded error walk (listed under [Unreleased] in the
  core's [CHANGELOG.md](../../CHANGELOG.md) until it is tagged). Until then, `go.mod` replaces the
  core with `../..`, so this module is not releasable yet.
- `Recovery` answers panics through the core's `prblm.Handler.RespondPanic`, so a panic is
  answered as the core's net/http `Recover` answers it. The hooks now receive the
  `*prblm.PanicError` itself rather than an error that unwraps to it, so `errors.As` still finds it.
  On a response that has already started, nothing is written, as before, and the hooks receive the
  500 problem that was not sent, where `Recover` gives them a nil problem.

### Fixed

- `Recovery` no longer hangs, or crashes the process with a stack overflow, on a panic value whose
  error chain loops back on itself (an `Unwrap() error` that returns itself, an `errors.Join` that
  contains itself). Its broken connection check walked that chain without a bound. The client now
  gets the safe 500 problem, and the hooks receive one `prblm.ErrUnwalkableError` note with reason
  `budget`, then the panic.
- A panic value whose `Is` method panics now gives the hooks one `prblm.ErrUnwalkableError` note
  with reason `panic`, then the panic, as the core does. Before, the hooks received the panic, then
  a second panic report for the `Is` method.
- `Recovery` answers a handler's `panic(nil)` under `GODEBUG=panicnil=1` with the safe 500 problem
  and reports it as a `*prblm.PanicError` with value nil. It used to tell a panic apart by `recover`
  returning non-nil, so the request got an empty 200 and the hooks heard nothing. A writer that
  calls `panic(nil)` while the problem is written is reported too. A `runtime.Goexit` in a handler,
  which `recover` cannot tell from that `panic(nil)`, is answered the same way, and the problem is
  flushed, because the goroutine then ends before gin and net/http finish the response. Over
  HTTP/1.x the client receives the problem complete; over HTTP/2, net/http then resets the stream of
  the handler that did not return, so the client receives the problem's bytes and then a stream
  reset (`INTERNAL_ERROR`). A writer that cannot flush keeps the problem unsent. The hooks are told
  in every case.
- `Recovery` nested in itself, or with the core's `Recover` around the engine or inside a
  handler, reports a panic once. A `runtime.Goexit` goes on through the outer recoveries, and each
  reports it again: under `GODEBUG=panicnil=1` they cannot tell it from a new `panic(nil)` in the
  code between them, which must never go unreported. `Recovery`'s godoc says so.
- A header a handler wrote into `c.Writer.Header()` under a non-canonical key, such as
  `c.Writer.Header()["cache-control"]`, is now merged as that header: a problem's
  `WithReplacedHeader` replaces it, an unmarked `Cache-Control` yields to it and `Vary` merges with
  it, and only the canonical key goes out. Before, net/http sent both values. The fix is in the
  core's `prblm.Response.MergeInto`, which `Middleware` and `Recovery` use.
- `Middleware` answers with a bare 500 when resolving the error panics with `panic(nil)` under
  `GODEBUG=panicnil=1` (a request context whose `Value` method panics is the known trigger). Its
  last line of defense told a panic apart by `recover` returning non-nil, so such a request got an
  empty 200.
- A panic value whose `Is` method panics with a value that cannot be formatted no longer escapes
  `Recovery`: the core's `prblm.ErrUnwalkableError` note names only the panic value's type, as in
  `prblm: error chain cannot be walked: panic: *mypkg.Err`.
- `Middleware` no longer hangs, or lets a panic escape, when the client went away (the request
  context is canceled) and the recorded error's chain cannot be walked: a cyclic chain, or an `Is`
  method that panics. The client-gone check now walks the chain with a bound, and only once the
  context is done. Such an error counts as coming from a client still there, and the core answers
  it: with the safe 500 problem and an `ErrUnwalkableError` note first when its own walk cannot
  finish either, and as usual otherwise (an `Is` method that panics only for `context.Canceled`).
- The status of `c.AbortWithError` no longer costs one entry of the core's 1,000-entry walk budget.
  It reached the core as a `prblm.WithStatus` wrapper, an entry of the chain, so an error of exactly
  1,000 entries aborted with the safe 500. The hint now goes to the core outside the chain; the
  hooks still receive the error with the hint around it.

## [0.2.1] - 2026-11-19

### Fixed

- `Recovery` answers a panic whose value is an error with a panicking `Is` method. Its broken
  connection check used to let that second panic escape `Recovery` to net/http, so the client got
  no response. The connection now counts as working: the client gets the safe 500 problem, and the
  hooks receive the original panic and then the second one. This fix doesn't need a new core: the
  module still requires `github.com/kartaladev/prblm` v0.2.0.

## [0.2.0] - 2026-11-18

### Changed (BREAKING)

- The module moved to `github.com/kartaladev/prblm/adapters/ginprblm`. Change the import path; the
  package name, `ginprblm`, and its API are unchanged:

  ```go
  // Before
  import "github.com/kartaladev/prblm/ginprblm"

  // After
  import "github.com/kartaladev/prblm/adapters/ginprblm"
  ```

### Changed

- Requires `github.com/kartaladev/prblm` v0.2.0, so the hooks can be added with `prblm.AddErrorHook`
  and problem responses are cheaper (see the root CHANGELOG.md).

### Added

- `ErrStatusOnly`, the error the hooks receive for a status-only abort such as
  `c.AbortWithStatus(401)`, so a hook can classify it with `errors.Is`. It also covers a 5xx status
  set without a body, so a hook should still treat a problem status of 500 or more as a failure.
  gin's own answers to an unrouted request (the 404 for a path no route claims, and the 405 for a
  wrong method with `HandleMethodNotAllowed`) match it too, though no handler ran.

[Unreleased]: https://github.com/kartaladev/prblm/compare/adapters/ginprblm/v0.2.1...HEAD
[0.2.1]: https://github.com/kartaladev/prblm/compare/adapters/ginprblm/v0.2.0...adapters/ginprblm/v0.2.1
[0.2.0]: https://github.com/kartaladev/prblm/compare/ginprblm/v0.1.0...adapters/ginprblm/v0.2.0
