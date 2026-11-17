# Tasks

## 1. Slice: sibling test helpers decode problems without `Parse`, green against core v0.1.0 and the local core

- [x] 1.1 Replace `prblm.Parse` in the test helpers of `ginprblm`, `fiberprblm`, `gormmap`, `pgxmap` and `validatormap` with a decoder that picks `encoding/json` or `encoding/xml` by `Content-Type` and fails the test for anything else; verify `go test -race ./...` (workspace) and `GOWORK=off go test ./...` pass in each of these modules
  - RED: none; behavior is unchanged (a preparatory refactoring). The existing tests are the safety net and must stay green before and after the change.
  - GREEN: one `decodeProblem(t, res)` helper per module, replacing each `prblm.Parse` call. Use gopls `go_symbol_references` on `Parse` to find every call.
  - REFACTOR: look for Duplicated Code between the helpers in the same module (Extract Function), then /simplify
- [x] 1.2 Verify slice 1: `GOWORK=off go mod tidy -diff`, `go vet`, `golangci-lint` and both test modes are clean in every touched module, and `grep -rn "prblm.Parse(" --include=*_test.go` shows calls only in the core module

## 2. Slice: `Parse` returns `*Received`, which reports `ErrBadGateway`, end to end through net/http

- [x] 2.1 Make `Parse` return a received problem (problem-document / Scenario: Parse response); verify `go test -run TestParse ./` passes
  - RED: change `parse_test.go` and `example_test.go` to expect `*prblm.Received` and read `r.Problem.Status` for the "parsed" rows; keep the "refused" rows. It fails to compile because `Received` doesn't exist. That is the expected RED for an API change, so confirm no other compile error appears.
  - GREEN: `type Received struct{ Problem Problem }`; `Parse` returns `&Received{Problem: p}`; godoc on both. The `Parse` godoc says that a received problem never reaches a client, and that `return &r.Problem` relays it.
  - REFACTOR: check for a Long Function in `Parse` after the change, then /simplify
- [x] 2.2 `Received` is an error that doesn't describe itself (problem-document / Scenario: Received problem as an error); verify `go test -run TestReceived_Error ./` passes
  - RED: `TestReceived_Error` with the three rows: a held 404 with detail, an empty held problem, and a nil `*Received`. Plus a `Describer` assertion row per case. Fails because `Error` is missing.
  - GREEN: a nil-safe `Error()` built from `Problem.Error()`, with no `Problem()` method and no embedding
  - REFACTOR: Inline Variable where the prefix is built, then /simplify
- [x] 2.3 A received problem reports `ErrBadGateway` only (error-sentinels / Scenario: errors.Is on a received problem); verify `go test -run TestReceived_Is ./` passes
  - RED: `TestReceived_Is` with all six Examples rows: wrapped, other sentinels, held status 0 and the nil pointer. The `true` rows fail.
  - GREEN: `func (r *Received) Is(target error) bool { return target == ErrBadGateway }`
  - REFACTOR: none expected; /simplify
- [x] 2.4 A received problem in the chain is not self-describing, and `&r.Problem` is (error-mapping / Scenario: Problem or describer in the chain); verify `go test -run TestResolve_SelfDescribing ./` passes
  - RED: add the two new rows (a received 404 gives 502 with 1 mapper call; `&r.Problem` gives 404 with 0 calls) to the existing table test. Confirm the received row fails before 2.3 is merged in, or reason from 2.3 that it now passes. Either way, record the run output.
  - GREEN: no production change expected beyond 2.1–2.3. If the row fails, fix the cause; do not special-case `selfDescribed`.
  - REFACTOR: /simplify on the test table
- [x] 2.5 The gateway cases are safe by default and consumer code decides (error-mapping / Scenario: Gateway returns a received problem); verify `go test -run TestResolve_Received ./` passes
  - RED: `TestResolve_Received` through `httptest` and the net/http adapter. It has one case per row of "safe by default", "consumer decides" and "explicit relay", including the panicking and nil-returning `prblm.As` mappers, `WithoutBuiltins`, `WithStatus(R, 503)`, `errors.Join` and the typed nil. It asserts the status, type, detail, that there is no `table` member except in the relay row, and the hook calls. Confirm it reproduces issue #8 (the `ErrBadGateway`-wrapped row was 404 with `table` before 2.1).
  - GREEN: none expected; the rows hold through 2.1–2.3.
  - Proof for #8: on main, `fmt.Errorf("%w: %w", prblm.ErrBadGateway, parsed)` through net/http gave `404 {"title":"Not Found","status":404,"detail":"vessel 7 missing in db shard-3","instance":"/vessels/7","table":"vessels_shard3"}`. After the fix, `TestResolve_Received/inside_ErrBadGateway` and `TestParity_Received` give 502 without `table`.
  - REFACTOR: Extract Function for the shared received-problem fixture, then /simplify
- [x] 2.6 Update the existing `sentinels_test.go` row "problem inside an outer sentinel" only if its fixture uses `Parse` (it uses `prblm.New`, so it should stay 422). Add a comment that ties it to issue #8: an application-built problem still describes itself. Verify `go test -run TestSentinels_Override ./` passes
- [x] 2.7 Replace the issue #8 paragraph in `sentinels.go` with the new rule (a received problem reports `ErrBadGateway`), so the godoc matches the behavior at the end of this slice (done in commit 70025bd)
- [x] 2.8 Verify slice 2: `go test -race -coverprofile=c.out ./... && go tool cover -func=c.out` shows `Parse`, `Received.Error`, `Received.Is`, `selfDescribed`, `mapSentinel` and `firstSentinel` covered, with every branch exercised. `go vet`, `golangci-lint` and gopls `go_diagnostics` on the changed files are clean. Both CI test modes stay green in every module.

## 3. Slice: parity in gin and fiber, and documentation

- [x] 3.1 Same response for a wrapped received problem in every adapter (error-mapping / Scenario: Same response in every adapter); verify `go test -race -run TestParity_Received ./...` passes in `internal/paritytest` in both CI modes
  - RED: `TestParity_Received` in `internal/paritytest`, with one row per adapter (net/http, gin, fiber) asserting the exact body from the Examples table. That module replaces the core with the local one in both modes, so it can use `prblm.Received` before the adapters require v0.2.0. Before 2.1 lands, the same case written with v0.1.0's `*Problem` shows the 404 with `table` in each adapter. Record that output as the proof for #8.
  - GREEN: no adapter code change expected.
  - REFACTOR: reuse the existing parity helpers in `parity_test.go`; /simplify
- [x] 3.2 Documentation; verify `go test -run Example ./` passes and `go doc prblm.Received` reads correctly
  - The `Received` godoc covers four things:
    - why it isn't a `Describer`
    - the `ErrBadGateway` classification
    - the explicit relay
    - a consumer mapper that passes through a 4xx status
  - Add a warning to the `Problem.UnmarshalJSON`/`UnmarshalXML` godoc (D4).
  - The issue #8 paragraph in `sentinels.go` was already replaced in slice 2 (task 2.7).
  - Update the README sections "Sentinels" and "Parse".
  - Add `ExampleReceived` with the gateway pattern, the output 502, and the relay.
  - CHANGELOG `[Unreleased]`: **Changed (BREAKING)** for `Parse` returning `*Received`, with a before/after snippet; **Fixed** for #8 (received problems no longer leak).
- [x] 3.3 Verify the change end to end: all modules pass in both CI modes, `golangci-lint` is clean, and a scratch module built against the local core reproduces the scenario from the #8 issue comment with a 502 and no `table` member

## 4. Review fixes

- [x] 4.1 `ExampleReceived` shows a consumer mapper as its third case (design Risks); verify `go test -run ExampleReceived ./` passes
  - RED: add the case with a `prblm.As(func(r *prblm.Received) *prblm.Problem {...})` mapper that passes an upstream 4xx status through with the application's own detail, and extend `// Output:` with the expected line; run it and confirm the output mismatch is the failure.
  - GREEN: make the example produce the expected output; update the README snippet that mirrors `ExampleReceived` to match.
  - REFACTOR: /simplify
- [x] 4.2 Characterize `errors.As` on a wrapped received problem; verify `go test -run TestReceived_As ./` passes
  - `errors.As(fmt.Errorf("call: %w", r), &target)` with `target *prblm.Received` succeeds and `target.Problem` holds the upstream fields.
  - `errors.As(..., &p)` with `p *prblm.Problem` does not match (pins the migration note).
  - Characterization tests: show they are meaningful by breaking the behavior temporarily (for example, adding an `As` method or embedding) and recording the failure, then restoring.
  - Evidence: adding `func (r *Received) Unwrap() error { return &r.Problem }` made `problem_target_does_not_match` fail with `errors.As(*Problem) = true with Not Found: vessel 7 missing in db shard-3, want false`; making `Parse` return `&Received{}` made `received_target_matches` fail with `target.Problem = 0 "" "", want 404 ...` and `Ext("table") = <nil>, false`. Both were restored.
- [x] 4.3 CHANGELOG `[Unreleased]` migration: client code that does `errors.As(err, &p)` with `p *prblm.Problem` on a `Parse` result no longer matches; use `r *prblm.Received`, then `r.Problem`
- [x] 4.4 Verify: in every touched module `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...`, `GOWORK=off go test ./...`, `GOWORK=off go mod tidy -diff`; and `openspec validate isolate-parsed-problems --strict`

## 5. Review fixes, round 2

- [x] 5.1 Narrow the 4xx pass-through mapper to an explicit allow-list (design Risks); verify `go test -run ExampleReceived ./` passes
  - The mapper in `ExampleReceived`, the `Received` godoc in `parse.go` and the README snippet forwards every upstream 4xx. 401, 403, 405, 407 and 429 describe this service's relationship with the upstream and need headers the problem doesn't carry.
  - Change the condition to `r != nil && (s == 404 || s == 409 || s == 422)` with a one-line comment why. The `r != nil` guard stops a typed-nil `*Received` from panicking in the mapper (reported to the hook).
  - Keep all three in sync; `// Output:` must still pass.
  - Done as an `if r == nil` guard followed by a `switch` on 404, 409 and 422: in `if s := r.Problem.Status; r != nil && ...` the init statement dereferences `r` before the guard runs. A temporary probe confirmed a typed nil gives 502 without a panic, 401 and 429 give 502, and 409 passes through.
- [x] 5.2 `Received` godoc: warn that relaying `&r.Problem` with a relative upstream `type` resolves it against this service's namespace (`WithNamespace`), which may not document it; resolve it against the upstream URL first if needed
- [x] 5.3 Record the test-break evidence for the characterization tests under task 4.2
- [x] 5.4 Verify: in every touched module `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...`, `GOWORK=off go test ./...`, `GOWORK=off go mod tidy -diff`; and `openspec validate isolate-parsed-problems --strict`

## Workflow follow-up

- Close issue #8 from the PR that merges slice 2.
- Ask before tagging core v0.2.0. Each sibling module then moves to it in its own release.
- When a sibling module moves to core v0.2.0, its `decodeProblem` test helper can go back to `prblm.Parse` (`r, err := prblm.Parse(res)`, then `&r.Problem`).
- Archive the change after review.
