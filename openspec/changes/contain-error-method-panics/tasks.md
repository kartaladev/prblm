# Tasks

## 1. Slice: the status-hint step contains panics from an error's own methods, through net/http

- [x] 1.1 A hostile error is answered and its panics are reported (error-mapping / Scenario: Error methods panic); verify `go test -race -run TestResolve_FaultyError ./` passes
  - RED: in `hook_test.go`, write `TestResolve_FaultyError` through `write(t, h, err)` (net/http), with one case per Examples row: zero configuration, the status-hint step, and another step still matches.
    - Assertions: the status, the exact body, no `boom` in the body, at least one `*prblm.PanicError` hook call before the last one, and a last hook call carrying the error itself.
    - A test helper turns a panic out of the handler into `t.Fatalf`, so a RED run fails as a test, not as a crashed binary.
    - Expected failure: the `As`-panic rows and the empty-target row panic out of `Respond` from `applicableHint`. The `Is` and typed-nil rows already pass, which records them as regression rows.
  - GREEN: in `match`, run the hint step through `safeCall`: `return safeCall(ctx, h, func() *Problem { return hinted(err) })`.
  - REFACTOR: look for Duplicated Code between `match`'s steps (each is `safeCall` plus a nil check), and only Extract Function if it reads better. Then `/simplify`.
- [x] 1.2 Without a hook, the hostile error is still answered (error-mapping / Scenario: Error methods panic without a hook); verify `go test -race -run TestResolve_FaultyError_NoHook ./` passes
  - RED: `TestResolve_FaultyError_NoHook` with both Examples rows. Run it against a stash of 1.1's GREEN, or reason from 1.1's RED output, and record the result. It fails on main for the same reason.
  - GREEN: none expected beyond 1.1.
  - REFACTOR: share the hostile-error fixtures with 1.1 (Extract Function), then `/simplify`.
- [x] 1.3 Update the `doc.go` package godoc and the `Resolve` godoc. They should say that a panic in an error value's own `Is`/`As` methods is contained like consumer code, at every step, and that a cyclic error chain is not covered. Verify with `go doc -all . | grep -n -i panic` and a read-through.
- [x] 1.4 Verify slice 1:
  - `go test -race -coverprofile=c.out ./... && go tool cover -func=c.out` shows `match`, `hinted`, `applicableHint` and `safeCall` covered.
  - `go vet ./...`, `golangci-lint run ./...` and gopls `go_diagnostics` on `handler.go`, `doc.go` and `hook_test.go` are clean.
  - `GOWORK=off go test ./...` passes.

## 2. Slice: net/http `Recover` answers a hostile panic value

- [x] 2.1 A hostile panic value gets the safe 500, and the original panic reaches the hook (panic-recovery / Scenario: Hostile panic value); verify `go test -race -run TestRecover_HostileValue ./` passes
  - RED: in `http_test.go`, write `TestRecover_HostileValue` through `httptest.NewServer` and `h.Recover(...)`, with the three Examples rows (hostile `Is`, EPIPE, `"boom"`).
    - Assertions: the status, `Content-Type` and body, or that no response was written for EPIPE; and the hook calls in order.
    - Expected failure: the hostile row gets a client `EOF`, because the panic escapes `Recover` from `isBrokenConnection`. The other two rows pass as regression rows.
  - GREEN: in `recoverPanic`, replace `isBrokenConnection(pe)` with `safeCall(ctx, h, func() bool { return isBrokenConnection(pe) })`.
  - REFACTOR: check whether `recoverPanic`'s condition now reads as a Long Expression, and Extract Variable (`broken := ...`) if so. Then `/simplify`.
- [x] 2.2 Without a hook, a hostile panic value still gets the 500 and the server keeps serving (panic-recovery / Scenario: Hostile panic value without a hook); verify `go test -race -run TestRecover_HostileValue_NoHook ./` passes
  - RED: `TestRecover_HostileValue_NoHook`: two requests in a row, both 500. On main, the first request gets `EOF`.
  - GREEN: none expected beyond 2.1.
  - REFACTOR: share the hostile panic value with slice 1's fixtures, then `/simplify`.
- [x] 2.3 Add a "Fixed" entry to `CHANGELOG.md` under `[Unreleased]` for slices 1 and 2. Verify by reading it against Keep a Changelog.
- [x] 2.4 Verify slice 2:
  - `go test -race -coverprofile=c.out ./... && go tool cover -func=c.out` shows `recoverPanic` covered on its broken, not-broken and panicking branches.
  - `go vet`, `golangci-lint` and gopls `go_diagnostics` are clean on the changed files.
  - `GOWORK=off go test ./...` passes, and `internal/paritytest` passes in both modes.

## 3. Slice: gin `Recovery` answers a hostile panic value

- [ ] 3.1 A hostile panic value under gin gets the safe 500 and the hook hears about both panics (gin-adapter / Scenario: Hostile panic value under gin); verify `go test -race -run TestRecovery_HostileValue ./` passes in `adapters/ginprblm`
  - RED: in `adapters/ginprblm`'s recovery test file, write `TestRecovery_HostileValue` through `httptest.NewServer`, with the three Examples rows (two chains for the hostile `Is` value, plus EPIPE).
    - Assertions: the status, `Content-Type` and body; and that the hook calls unwrap to panic errors in order.
    - Expected failure: the hostile rows get a client `EOF`, because the panic escapes from `isBrokenConnection` at recovery.go:69.
  - GREEN: a small guarded check in `recoverPanic`. A panic from `isBrokenConnection` counts as not broken, and is reported with `_, _ = respond(h, c.Request, newPanicked(v))`, the pattern `guardWrite` uses.
  - REFACTOR: look for Duplicated Code between the new guard and `guardWrite`, and Extract Function only if it removes duplication without hiding the difference. Then `/simplify`.
- [ ] 3.2 Add a "Fixed" entry to `adapters/ginprblm/CHANGELOG.md` under `[Unreleased]`, and correct the `respond` comment in `middleware.go` if it no longer matches the core's contained panics. Verify by reading.
- [ ] 3.3 Verify slice 3:
  - In `adapters/ginprblm`, `go test -race -coverprofile=c.out ./... && go tool cover -func=c.out` shows `recoverPanic` covered on all three branches.
  - `go vet`, `golangci-lint`, gopls `go_diagnostics`, and `GOWORK=off go test ./...` (against core v0.2.0) are clean.
  - `internal/paritytest` passes.

## Workflow follow-up

- Open one PR for the three slices. Wait for CI, read every check, and merge only if all pass.
- Archive the change after the merge.
- Tag core `v0.2.1` and `adapters/ginprblm/v0.2.1` per RELEASING.md, and publish the GitHub Release, only when the maintainer asks.
