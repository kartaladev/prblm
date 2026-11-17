# Changelog

All notable changes to the `github.com/kartaladev/prblm/ginprblm` module are documented in this
file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module
uses [Semantic Versioning](https://semver.org/). Its tags are `ginprblm/vX.Y.Z` (see
[RELEASING.md](../RELEASING.md)). Version 0.1.0 is described in the root
[CHANGELOG.md](../CHANGELOG.md).

## [Unreleased]

### Added

- `ErrStatusOnly`, the error the hooks receive for a status-only abort such as
  `c.AbortWithStatus(401)`, so a hook can classify it with `errors.Is`. It also covers a 5xx status
  set without a body, so a hook should still treat a problem status of 500 or more as a failure.

[Unreleased]: https://github.com/kartaladev/prblm/compare/ginprblm/v0.1.0...HEAD
