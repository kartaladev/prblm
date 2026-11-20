# Changelog

All notable changes to the `github.com/kartaladev/prblm/mappers/validatormap` module are documented
in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
module uses [Semantic Versioning](https://semver.org/). Its tags are `mappers/validatormap/vX.Y.Z`
(see [RELEASING.md](../../RELEASING.md)).

## [Unreleased]

## [0.1.1] - 2026-11-20

### Fixed

- A translator function, or a translation, that calls `panic(nil)` under `GODEBUG=panicnil=1` is
  now reported: the problem's cause is a `*prblm.PanicError` with value nil, so the handler's
  `OnError` hook learns of it, as the `WithTranslator` godoc promises. The mapper told a panic apart
  by `recover` returning non-nil, which it does not for `panic(nil)` in that mode, so the panic was
  contained without a trace. It now tells it apart by the call not returning normally, like the
  core. The response is unchanged: a 422 with the default message for the entries.
  Still requires core v0.1.0: the fix uses only the core's public API.

## [0.1.0] - 2026-11-16

First release, with core v0.1.0.

[Unreleased]: https://github.com/kartaladev/prblm/compare/mappers/validatormap/v0.1.1...HEAD
[0.1.1]: https://github.com/kartaladev/prblm/compare/mappers/validatormap/v0.1.0...mappers/validatormap/v0.1.1
[0.1.0]: https://github.com/kartaladev/prblm/releases/tag/mappers/validatormap/v0.1.0
