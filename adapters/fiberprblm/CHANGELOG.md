# Changelog

All notable changes to the `github.com/kartaladev/prblm/adapters/fiberprblm` module are documented
in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
module uses [Semantic Versioning](https://semver.org/). Its tags are `adapters/fiberprblm/vX.Y.Z`
(see [RELEASING.md](../../RELEASING.md)). Version 0.1.0 was published as
`github.com/kartaladev/prblm/fiberprblm`, tagged `fiberprblm/v0.1.0`, and is described in the root
[CHANGELOG.md](../../CHANGELOG.md).

## [Unreleased]

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

[Unreleased]: https://github.com/kartaladev/prblm/compare/adapters/fiberprblm/v0.2.0...HEAD
[0.2.0]: https://github.com/kartaladev/prblm/compare/fiberprblm/v0.1.0...adapters/fiberprblm/v0.2.0
