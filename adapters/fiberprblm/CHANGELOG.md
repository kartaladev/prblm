# Changelog

All notable changes to the `github.com/kartaladev/prblm/adapters/fiberprblm` module are documented
in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
module uses [Semantic Versioning](https://semver.org/). Its tags are `adapters/fiberprblm/vX.Y.Z`
(see [RELEASING.md](../../RELEASING.md)). Version 0.1.0 was published as
`github.com/kartaladev/prblm/fiberprblm`, tagged `fiberprblm/v0.1.0`, and is described in the root
[CHANGELOG.md](../../CHANGELOG.md).

## [Unreleased]

## [0.3.0] - 2026-11-20

### Changed

- Requires core v0.3.0, which brings the bounded error walk (see the core's
  [CHANGELOG.md](../../CHANGELOG.md), version 0.3.0).

### Fixed

- `ErrorHandler` no longer hangs, or crashes the process with a stack overflow, on an error whose
  chain loops back on itself (an `Unwrap() error` that returns itself, an `errors.Join` that
  contains itself). Its search for a `*fiber.Error` walked that chain without a bound. The client
  now gets the safe 500 problem, and the hooks receive one `prblm.ErrUnwalkableError` note with
  reason `budget`, then the error.
- An error whose `As` method panics, or whose `Unwrap` method panics, now gets the safe 500
  problem, and the hooks receive an `ErrUnwalkableError` note with reason `panic`, then the error.
  Before, the client got a bare 500 without a body and the hooks heard nothing. This holds too when
  the panic value cannot be formatted, because the core's note names only the panic value's type,
  as in `prblm: error chain cannot be walked: panic: *mypkg.Err`.
- `ErrorHandler` no longer hangs, or lets a panic escape to fiber, when the client went away (the
  request context is canceled) or, with type documentation on, the request matched no route, and
  the error's chain cannot be walked: a cyclic chain, or an `Is` method that panics. These checks
  now walk the chain with a bound, and the client-gone check only once the context is done. Such an
  error counts as coming from a client still there and from a matched route, and the core answers
  it: with the safe 500 problem and an `ErrUnwalkableError` note first when its own walk cannot
  finish either, and as usual otherwise (an `As` method that panics only for a `*fiber.Error` gets
  no hint, and the error is mapped as if it held none).
- The code of a `*fiber.Error` no longer costs one entry of the core's 1,000-entry walk budget. It
  reached the core as a `prblm.WithStatus` wrapper, an entry of the chain, so an error of exactly
  1,000 entries holding a `*fiber.Error` aborted with the safe 500. The hint now goes to the core
  outside the chain; the hooks still receive the error with the hint around it.
- `Recovery` answers a handler's `panic(nil)` under `GODEBUG=panicnil=1`: the error handler gets
  the error and the client the safe 500 problem, and the hooks receive a `*prblm.PanicError` with
  value nil. It used to tell a panic apart by `recover` returning non-nil, so the client got an
  empty 200 and the hooks heard nothing. A `runtime.Goexit` in a handler still cannot be answered
  under fiber, which writes the response on the goroutine Goexit ends; `Recovery`'s godoc says so.
- With type documentation on, a panic that `Recovery` recovered on a documentation path no route
  claims is answered with the safe 500 problem and reported to the hooks, even when its value wraps
  `fiber.ErrNotFound`. The route-not-found check used to look through the recovered panic into its
  value, so the client got the 200 documentation page and the hooks never heard of the panic.
- `ErrorHandler` answers with a bare 500 when resolving the error panics with `panic(nil)` under
  `GODEBUG=panicnil=1` (a request context whose `Value` method panics is the known trigger). Its
  last line of defense told a panic apart by `recover` returning non-nil, so such a request got
  fiber's empty 200.
- With `fiber.Config{DisableHeaderNormalizing: true}`, a header the handler set under another
  spelling, such as `c.Set("cache-control", ...)`, is now merged as that header: a problem's
  `WithReplacedHeader` replaces it, an unmarked `Cache-Control` yields to it and `Vary` merges with
  it, and only the canonical name goes out. Before, `ErrorHandler` looked the handler's values up
  under the canonical name only, which fasthttp matches exactly in that mode, so both values were
  sent. It now visits the response headers once and compares names without regard to case,
  whatever the configuration, at no extra allocation.
- A typed-nil `*fiber.Error` ahead of a real one in the chain, such as
  `errors.Join((*fiber.Error)(nil), fiber.ErrConflict)`, no longer hides the real one's code: the
  response is a 409, not a 500.

## [0.2.0] - 2026-11-18

### Changed (BREAKING)

- The module moved to `github.com/kartaladev/prblm/adapters/fiberprblm`. Change the import path;
  the package name, `fiberprblm`, and its API are unchanged:

  ```go
  // Before
  import "github.com/kartaladev/prblm/fiberprblm"

  // After
  import "github.com/kartaladev/prblm/adapters/fiberprblm"
  ```

### Changed

- Requires `github.com/kartaladev/prblm` v0.2.0, so the hooks can be added with `prblm.AddErrorHook`
  and problem responses are cheaper (see the root CHANGELOG.md).
- `github.com/valyala/fasthttp` is a direct requirement, at the version fiber already required; no
  new module enters the graph.

[Unreleased]: https://github.com/kartaladev/prblm/compare/adapters/fiberprblm/v0.3.0...HEAD
[0.3.0]: https://github.com/kartaladev/prblm/compare/adapters/fiberprblm/v0.2.0...adapters/fiberprblm/v0.3.0
[0.2.0]: https://github.com/kartaladev/prblm/compare/fiberprblm/v0.1.0...adapters/fiberprblm/v0.2.0
