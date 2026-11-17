# Tasks

## 1. Slice: `AddErrorHook` beside `OnError` in the core, end to end through net/http

- [x] 1.1 Added hooks run after the `OnError` hook, in the order added (error-mapping / Scenario: Replaced and added hooks, Examples: registered); verify `go test -race -run TestHooks_Order ./` passes
  - RED: `TestHooks_Order`, a table test through `Handler.Wrap` and `httptest`, with the six "registered" rows. Each hook appends its name to a shared, mutex-guarded slice. Fails to compile because `AddErrorHook` doesn't exist; confirm that this is the only error.
  - GREEN: a `hooks` field on `Handler`. `AddErrorHook` appends, `isReporting` checks both slots, and `callHook` calls `onError` and then each hook.
  - REFACTOR: the Divergent Change risk in `callHook`. Extract Function `callOne(hook)` for the single call, then /simplify
- [x] 1.2 Nil hooks and `OnError(nil)` (error-mapping / Scenario: Replaced and added hooks, Examples: misuse); verify `go test -race -run TestHooks_Misuse ./` passes
  - RED: `TestHooks_Misuse` with the four misuse rows. `AddErrorHook(nil)` panics with a nil call before the fix, or adds a nil entry. Confirm the failure.
  - GREEN: `AddErrorHook` ignores nil. `OnError` stays unchanged.
  - REFACTOR: none expected; /simplify
- [x] 1.3 Each hook isolated from a panicking one (error-mapping / Scenario: A panicking hook does not silence the others); verify `go test -race -run TestHooks_Panic ./` passes
  - RED: `TestHooks_Panic` with the four rows: `"boom"`, `nil` (`*runtime.PanicNilError`) and `http.ErrAbortHandler`, with the panicking hook first, in the middle and last. It asserts the 409 response and the order of the recording hooks. The middle and first rows fail with a single shared `recover`.
  - GREEN: a `defer recover` per hook call (`callOne`).
  - REFACTOR: check that `safeCall` and `callHook` share no duplicated recovery (Extract Function only if both read better), then /simplify
- [x] 1.4 Every hook receives notes, and silent requests reach none (error-mapping / Scenario: Every hook receives notes and silent requests reach none); verify `go test -race -run TestHooks_Notes ./` passes
  - RED: `TestHooks_Notes` with the four rows: the mapper panic, the invalid status, discovery's 404 on `GET /problems/unknown` with a namespace configured, and a nil error. It asserts that A and B both receive each event.
  - GREEN: none expected if every note goes through `callHook`. If a row fails, route that note site through `callHook`; don't add a second dispatch path.
  - REFACTOR: /simplify
- [x] 1.5 The existing hook and faulty-code tests still pass unchanged (error-mapping / Scenario: Hook sees the original error, Scenario: Consumer code panics); verify `go test -race -run 'TestOnError|TestFaulty' ./` passes
- [x] 1.6 Documentation; verify `go test -run Example ./` passes
  - `AddErrorHook` godoc covers:
    - the order
    - isolation
    - the shared problem
    - "libraries use this"
  - `OnError` godoc: "replaces; see `AddErrorHook`".
  - Add `ExampleAddErrorHook`, with a library hook and an application hook both called, and the output showing their order.
  - Update the README hook section.
  - CHANGELOG `[Unreleased]` Added.
- [x] 1.7 Verify slice 1: `go test -race -coverprofile=c.out ./... && go tool cover -func=c.out` shows `AddErrorHook`, `isReporting`, `callHook` and `callOne` fully covered, including the no-hook, nil and panic branches. `go vet`, `golangci-lint` and gopls `go_diagnostics` on the changed files are clean. Run `go_symbol_references` on `onError` before the change, and record every report site it lists.

## 2. Slice: the same hooks in every adapter

- [x] 2.1 Hooks in net/http, gin and fiber (error-mapping / Scenario: Same hooks in every adapter); verify `go test -race -run TestParity_Hooks ./...` passes in `internal/paritytest` in both CI modes
  - RED: `TestParity_Hooks`, one row per adapter, through the existing parity harness. Fails to compile before slice 1, and passes after it.
  - GREEN: no adapter code change expected. If an adapter drops options, fix that adapter.
  - REFACTOR: reuse the harness in `parity_test.go`; /simplify
- [x] 2.2 Verify slice 2: `internal/paritytest` passes with `-race`, and `go vet` and `golangci-lint` are clean

## 3. Slice: `ginprblm.ErrStatusOnly`

- [ ] 3.1 Status-only aborts are identifiable in the hooks (gin-adapter / Scenario: Classifying a status-only abort in a hook); verify `go test -race -run TestHook_StatusOnly ./` passes in `ginprblm`, in both CI modes
  - RED: `TestHook_StatusOnly` with all nine rows. The status-only rows fail to compile because `ginprblm.ErrStatusOnly` is unexported; confirm that this is the only compile error. Before the export, run the "handler errors" and "no problem written" rows against the current code (temporarily with the internal name in an internal test). That confirms the existing behavior those rows pin, especially `c.String(500, "plain")` with 0 calls. If a row disagrees with the code, stop and report it with the failing output (rule 8). Don't change the row to match.
  - GREEN: run gopls `go_rename_symbol` `errStatusOnly` → `ErrStatusOnly`, and write its godoc (D5).
  - REFACTOR: none expected; /simplify
- [ ] 3.2 Documentation; verify `go test -run Example ./` passes in `ginprblm`
  - In `ginprblm/doc.go` and the doc example, show a hook that tells a status-only abort apart with `errors.Is`, and still treats a 5xx status as a failure.
  - `ginprblm` CHANGELOG entry, Added.
- [ ] 3.3 Verify slice 3: `GOWORK=off go mod tidy -diff`, `go vet`, `golangci-lint` and both test modes are clean in `ginprblm`, and `go tool cover -func` shows the status-only, recorded-error and no-problem branches of `finish` covered

## Workflow follow-up

- Reply on the srvr request: link this change, and point out the 5xx caveat of `ErrStatusOnly` (D5).
- Release `ginprblm` with slice 3 whenever needed (it works with core v0.1.0). The core's `AddErrorHook` ships in core v0.2.0 together with `isolate-parsed-problems`. Ask before tagging.
- Archive the change after review.
