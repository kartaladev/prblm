# Proposal

## Why

Issue #8: `Parse` returns a `*Problem`, and a `*Problem` describes itself. When a gateway wraps a problem it received from an upstream service, even inside an outer sentinel (`fmt.Errorf("%w: %w", prblm.ErrBadGateway, p)`), resolution step 1 finds it and answers with the upstream's status, detail and extension members. A consumer mapper can't override it, because step 1 runs before the mappers. As a result, an upstream 404 tells the client that its own request was wrong, and the upstream's internals (for example `"table":"vessels_shard3"`) reach the client. That breaks the rule that nothing leaks with zero configuration. v0.1.0 shipped with this as a known issue. v0.x allows the fix to break the API, and the longer we wait, the more callers we break.

## What Changes

- **BREAKING:** `Parse` returns a new `*Received` instead of a `*Problem`. `Received` is an error that holds the decoded problem in its `Problem` field. It does not implement `Describer`, so a received problem in an error chain is never relayed to the client unless the application asks for that. To migrate, read the fields as `r.Problem.Status` instead of `p.Status`.
- A received problem reports `prblm.ErrBadGateway` through `errors.Is`, so with zero configuration a chain holding one resolves like that sentinel: a 502 `about:blank` problem with the sentinel's fixed detail. The existing sentinel rules then apply unchanged:
  - the outermost sentinel in the chain wins
  - an outer status hint decides
  - consumer and scoped mappers take precedence
  - `WithoutBuiltins` turns the mapping off, leaving the safe 500
- Relaying stays possible, but only as an explicit act: `return &r.Problem`.
- The `OnError` hook still receives the error as returned. `errors.As(err, &r)` gives the hook the upstream problem for logging.
- The godoc of `Problem.UnmarshalJSON`/`UnmarshalXML` warns that a problem decoded by hand is a `*Problem` and describes itself. It points to `Parse` for responses from other services.

## Defaults and Customization

- **Zero configuration:** an upstream problem anywhere in the chain gives a 502 `about:blank` problem with the fixed detail `An upstream service returned an invalid response.` None of the upstream's title, detail, type, instance or extension members leak. An application sentinel outside it still decides, for example `ErrNotFound` gives 404.
- **Opt-in, loosens a safety default:** returning `&r.Problem` relays the upstream problem as it is, detail and extensions included. That is a deliberate act at the call site, and the godoc names it as one.
- **Opt-in:** a consumer mapper `prblm.As(func(r *prblm.Received) *prblm.Problem { ... })` can map received problems in a different way, for example by passing through an upstream 4xx status with the application's own detail.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `problem-document`: `Parse` returns a received problem, which holds the decoded problem, is an error and does not describe itself.
- `error-mapping`: a received problem in the chain is not self-describing and resolves like `ErrBadGateway`; an explicitly relayed problem still describes itself.
- `error-sentinels`: a received problem reports `ErrBadGateway`, so the sentinel order rules (outermost wins, outer status hint decides) cover it.

## Scope by Gall's Law

The simplest version that works end to end has three parts: `Parse` returns `*Received`, `*Received` reports `ErrBadGateway`, and nothing else in the resolution order changes. The fix reuses the sentinel machinery and adds no new resolution step.

Deliberately left for later:
- Carrying the HTTP response status or headers in `Received`. The problem's own status is all that resolution needs today.
- Option 3 of issue #8 (the position in the chain decides across all steps). Nothing in this change prevents it.
- Marking hand-decoded problems (`json.Unmarshal` into a `Problem`). The library can't know where those bytes came from, so this change only documents it.

## Impact

- Core: `parse.go` (new `Received` type, `Parse` signature) and the godoc of the `Unmarshal*` methods, `sentinels.go` and `handler.go`. Also `ExampleParse`, the README sections "Sentinels" and "Parse", and the CHANGELOG (a **Changed/BREAKING** entry and a Fixed entry for #8).
- Tests in every module that read responses with `Parse`: the core (including `mappers/sqlmap`, which is part of the core module), `ginprblm`, `fiberprblm`, `gormmap`, `pgxmap` and `validatormap`. Because of the release order (see the design), the sibling modules' test helpers stop depending on the return type of `Parse` first.
- Release: core v0.2.0. Sibling modules move their requirement to core v0.2.0 when they next release. Their code doesn't use `Parse`, so they keep working on v0.1.0 until then.
- No new dependencies.
