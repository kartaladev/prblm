# Design

## Context

See proposal.md (Why). Resolution step 1 (`selfDescribed`, `handler.go`) runs `errors.As(err, &d Describer)` over the whole chain. `*Problem` implements `Describer` through `Problem() *Problem` (`problem.go`), and `Parse` returns `*Problem`, so anything parsed wins step 1.

Sentinels are built-ins, matched by `mapSentinel` through `firstSentinel`, which honors chain order and the applicable status hint. Every `Parse` caller in the repository is a test helper or an example:
- core: `example_test.go`, `parse_test.go`, `mappers/sqlmap`
- `ginprblm`, `fiberprblm`, `gormmap`, `pgxmap`, `validatormap`

CI runs each module twice: once in workspace mode (`go.work`, local core) and once with `GOWORK=off`. In the second run, the published sibling modules use the released core v0.1.0.

## Goals / Non-Goals

**Goals:**
- No received problem reaches the client unless the application relays it explicitly.
- Reuse the existing sentinel rules instead of adding a resolution step.
- CI stays green in both module modes at every commit.

**Non-Goals:**
- Changing step 1 for problems the application builds (option 3 of issue #8).
- Detecting where a hand-decoded `Problem` came from.
- Carrying response metadata (HTTP status, headers) in `Received`.

## Slices

1. **The sibling test helpers decode without `Parse`.** Each sibling module's test helper decodes the body by its `Content-Type`, using `encoding/json` or `encoding/xml` into `prblm.Problem` (the lenient decoders exist in v0.1.0). Behavior doesn't change. Everything is green against both the local core and v0.1.0, and nothing new is exported. This slice exists only so that slice 2 can't break the `GOWORK=off` runs.
2. **`Parse` returns `*Received`, which reports `ErrBadGateway`.** This is the end-to-end fix through net/http: a gateway handler that wraps a received problem answers 502 and leaks nothing.
   - Satisfies:
     - `problem-document`: Parse response, Received problem as an error
     - `error-sentinels`: errors.Is on a received problem
     - `error-mapping`: Problem or describer in the chain, and the "safe by default", "consumer decides" and "explicit relay" rows of Gateway returns a received problem
   - Updates the core's own tests and `ExampleParse`.
   - Hot paths: `selfDescribed` (unchanged code, new no-match case) and `mapSentinel`/`firstSentinel` (a new `Is` implementer).
3. **Parity in gin and fiber, and docs.** The `error-mapping` scenario Same response in every adapter runs in `internal/paritytest`, which replaces the core with the local one in both CI modes, so it can use `Received` before the adapters require core v0.2.0. This adds no adapter code; it proves that the adapters inherit the fix. Docs:
   - README sections "Sentinels" and "Parse"
   - the godoc warnings on `Unmarshal*`
   - the `sentinels.go` paragraph that currently documents issue #8
   - CHANGELOG
   - a new `ExampleReceived` that shows the gateway pattern and the explicit relay

## Decisions

### D1. A distinct `Received` type, not a hidden flag on `Problem`

`type Received struct { Problem Problem }` holds the problem by value, in a named field.

Alternatives considered:
- **(a) An unexported `received` flag set by `Parse` and `Unmarshal*`, which `selfDescribed` skips.** It doesn't break the API, and it also covers problems decoded by hand.
  - Rejected: two `*Problem` values that print identically would resolve differently.
  - The flag would have to survive `clone` and copies, but not a rebuild through `New`. That is invisible state that every future edit of `Problem` must keep right.
  - A problem the application loads from its own JSON (a catalog of problems, a fixture) would silently stop describing itself.
- **Embedding `Problem`, or `*Problem`, in `Received`.** Rejected: embedding promotes `Problem()`, so `*Received` would be a `Describer` again, which reintroduces the bug.
- **A pointer field `Problem *Problem`.** Rejected: it admits a nil `Problem` inside a non-nil `Received`, one more nil case for every caller. The value is never nil.
- **Making only step 1 skip problems that are not outermost (option 3).** Out of scope; see the Non-Goals.

The name `Received` describes where the problem came from (a response the program received), not a role such as "upstream". Test clients use `Parse` too, and for them "upstream" would be wrong.

**Failure modes:**
- A future edit adds a `Problem() *Problem` method to `Received`, or embeds the problem. A compile-time test pins this: it asserts that `any(&Received{})` is not a `Describer`, plus a resolution row for it.
- A nil `*Received` in the chain. `Error` and `Is` never dereference the receiver: `Error` returns `<nil received problem>`, and `Is` reports `ErrBadGateway`. Both have spec rows.
- A caller relays by mistake with `return &r.Problem`. The godoc of `Received` and of `Parse` name this as relaying and say what leaks. It's the explicit opt-in.

### D2. `Received` reports `ErrBadGateway` through an `Is` method, rather than a new built-in mapper

`func (r *Received) Is(target error) bool { return target == ErrBadGateway }`

With that, `mapSentinel` already gives:
- the right outcome: outermost sentinel wins, and the outer status hint decides
- the fixed detail
- `WithoutBuiltins` turning it off
- consumer and scoped overrides through `prblm.Is(prblm.ErrBadGateway, ...)`

No code in the handler changes.

Alternatives considered:
- **A built-in `As(func(*Received) *Problem)` mapper placed after `mapSentinel`.**
  - It would have to copy the sentinel's hint deferral by hand to keep "an outer `WithStatus` decides".
  - Because built-ins run sentinels first, `fmt.Errorf("%w: %w", ErrNotFound, R)` would give 404 either way. But `errors.Join(R, ErrNotFound)` would ignore chain order and give 404 instead of 502, against the rule that chain order decides.
  - More code for a less consistent result.
- **Leaving a received problem to the 500 fallback.** Safe, but less accurate. The service recognizes the error (a dependency answered with an error), and `ErrBadGateway`'s godoc already defines that case: "a dependency answered, but with a response the service could not use: … an error of its own".

**Failure modes:**
- Client code that classifies with `errors.Is(err, prblm.ErrBadGateway)` now matches errors that wrap a received problem. That is the intended meaning, and the CHANGELOG states it.
- An upstream that answered 503 with Retry-After becomes a 502 without the header. It's a safe default; a consumer `prblm.As(func(*Received) ...)` mapper can pass through the status (an Examples row). The godoc of `Received` shows how.
- The status held by the received problem is 0 or invalid. It doesn't matter: `Is` ignores the held problem. This has a spec row.

### D3. Release order: slice 1 before slice 2

Workspace mode resolves the siblings against the local core, while `GOWORK=off` uses core v0.1.0.
- If slice 2 changed the sibling helpers to `r.Problem`, the `GOWORK=off` runs would fail to compile.
- If slice 2 left them alone, the workspace runs would fail.

Helpers that decode with `encoding/json`/`encoding/xml` compile against both versions.

Alternative considered: put `replace` directives back in the published modules until core v0.2.0 is released. Rejected: the `require` cleanup for v0.1.0 removed them on purpose, and a forgotten `replace` breaks consumers.

**Failure modes:**
- A helper decodes the wrong format. It picks JSON or XML by `Content-Type` and fails the test on anything else, the same as `Parse` does.
- The helpers stop exercising `Parse` in the sibling modules. That's acceptable: the core tests own `Parse`.

### D4. Hand-decoded problems are documented, not detected

`Problem.UnmarshalJSON`/`UnmarshalXML` keep building a `*Problem`, which describes itself. Their godoc points to `Parse` for responses from other services, and states that returning a decoded problem relays it.

Rationale: decoding into a `Problem` is constructing one, and the library can't tell an upstream body from the application's own fixtures. See D1(a).

**Failure mode:** a gateway that decodes by hand still leaks. It's mitigated by the godoc, the README "Parse" section and `ExampleReceived`. The proposal records this as a deliberate scope limit.

## Defaults

- **Received problem in the chain:** a 502 `about:blank` problem with the fixed `ErrBadGateway` detail. It's the most accurate status that leaks nothing.
- **Relaying:** explicit only, with `return &r.Problem`.
- **Custom mapping:** a consumer mapper for `*Received`, or `prblm.Is(prblm.ErrBadGateway, ...)`.

## Hot Paths

- Resolution step 1 `selfDescribed`: a received problem is a no-match, with no `Describer` in the chain.
- `mapSentinel`/`firstSentinel`, covering four cases:
  - a received problem alone
  - one inside an outer sentinel
  - one beside a status hint
  - one in a joined error
- `Received.Error` and `Received.Is`, including a nil receiver.
- `Parse`: every existing branch, now returning `*Received`.
- Each adapter's write path, for the parity scenario.

## Risks / Trade-offs

- [The `Parse` signature break hits every caller] → v0.x, a one-line migration (`r.Problem.X`, or `p := &r.Problem`), and a CHANGELOG entry with a before/after snippet.
- [A gateway that wants to pass through upstream 4xx statuses gets 502] → `ExampleReceived` shows a consumer mapper that does it without leaking the upstream detail.
- [`errors.Is(err, ErrBadGateway)` becomes true for wrapped received problems] → intended. Documented on `Received` and in the CHANGELOG.

## Migration Plan

1. Slice 1 merges on its own. It changes no behavior, and all modules stay on core v0.1.0.
2. Slices 2 and 3 merge. The core is tagged v0.2.0 (ask before pushing the tag).
3. Each sibling module moves its requirement to core v0.2.0 when it next releases. Their code doesn't use `Parse`, so nothing else changes.

Rollback: revert slice 2. Slice 1's helpers work with either core.
