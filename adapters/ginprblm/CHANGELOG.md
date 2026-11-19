# Changelog

All notable changes to the `github.com/kartaladev/prblm/adapters/ginprblm` module are documented in
this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
module uses [Semantic Versioning](https://semver.org/). Its tags are `adapters/ginprblm/vX.Y.Z` (see
[RELEASING.md](../../RELEASING.md)). Version 0.1.0 was published as
`github.com/kartaladev/prblm/ginprblm`, tagged `ginprblm/v0.1.0`, and is described in the root
[CHANGELOG.md](../../CHANGELOG.md).

## [Unreleased]

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
