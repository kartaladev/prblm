# Tasks

## 1. Slice: `AddErrorHook` beside `OnError` in the core, end to end through net/http

- [x] 1.1 Added hooks run after the `OnError` hook, in the order added (error-mapping / Scenario: Replaced and added hooks, Examples: registered); verify `go test -race -run TestHooks_Order ./` passes
  - RED: `TestHooks_Order`, a table test through `Handler.Wrap` and `httptest`, with the six "registered" rows. Each hook appends its name to a shared, mutex-guarded slice. Fails to compile because `AddErrorHook` doesn't exist; confirm that this is the only error.
  - GREEN: a `hooks` field on `Handler`. `AddErrorHook` appends, `isReporting` checks both slots, and `callHook` calls `onError` and then each hook.
  - REFACTOR: the Divergent Change risk in `callHook`. Extract Function `callOne(hook)` for the single call, then /simplify
- [x] 1.2 Nil hooks and `OnError(nil)` (error-mapping / Scenario: Replaced and added hooks, Examples: misuse); verify `go test -race -run TestHooks_Misuse ./` passes
  - Characterization test: `TestHooks_Misuse` with the four misuse rows. They pin the observable calls, not the nil guard: once each hook has its own `recover` (1.3), a nil entry's panic is swallowed, so every row also passes without the guard. The guard itself (a handler with only `AddErrorHook(nil)` builds no reports) is pinned by the white-box RED test in 4.2.
  - GREEN: `AddErrorHook` ignores nil. `OnError` stays unchanged.
  - REFACTOR: none expected; /simplify
- [x] 1.3 Each hook isolated from a panicking one (error-mapping / Scenario: A panicking hook does not silence the others); verify `go test -race -run TestHooks_Panic ./` passes
  - RED: `TestHooks_Panic` with the four rows: `"boom"`, `nil` (`*runtime.PanicNilError`) and `http.ErrAbortHandler`, with the panicking hook first, in the middle and last. It asserts the 409 response and the order of the recording hooks. The middle and first rows fail with a single shared `recover`.
  - GREEN: a `defer recover` per hook call (`callOne`).
  - REFACTOR: check that `safeCall` and `callHook` share no duplicated recovery (Extract Function only if both read better), then /simplify
- [x] 1.4 Every hook receives notes, and silent requests reach none (error-mapping / Scenario: Every hook receives notes and silent requests reach none); verify `go test -race -run TestHooks_Notes ./` passes
  - Characterization test (expected to pass on first run, since every note already goes through `callHook`): `TestHooks_Notes` with the five rows: the mapper panic, the invalid status, discovery's 404 on `GET /problems/unknown` with a namespace configured (with and without a panicking enricher), and a nil error. It asserts that A and B both receive each event. Confirm it is meaningful: temporarily make `callHook` skip the added hooks and see the note rows fail, then restore.
  - GREEN: none expected. If a row fails, route that note site through `callHook`; don't add a second dispatch path.
  - REFACTOR: /simplify
- [x] 1.5 The existing hook and faulty-code tests still pass unchanged (error-mapping / Scenario: Hook sees the original error, Scenario: Consumer code panics); verify `go test -race -run 'TestOnError|TestResolve_FaultyConsumer' ./` passes
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
  - Note: `onError` is referenced by the field (`handler.go:28`), `isReporting`, `callHook` and `OnError` (`options.go`). The report sites, all through `isReporting`/`callHook`, are `report` (called from `normalize`, `respond`, `encode` and the recovery paths), `noteType`, `NoteShadowedRoute` and `safeCall`.

## 2. Slice: the same hooks in every adapter

- [x] 2.1 Hooks in net/http, gin and fiber (error-mapping / Scenario: Same hooks in every adapter); verify `go test -race -run TestParity_Hooks ./...` passes in `internal/paritytest` in both CI modes
  - Characterization test: `TestParity_Hooks`, one row per adapter, through the existing parity harness. It fails to compile before slice 1 and is expected to pass on first run after it, because adapters pass options through. Confirm it is meaningful: temporarily drop the options in one adapter's handler construction and see that row fail, then restore.
  - GREEN: no adapter code change expected. If an adapter drops options, fix that adapter.
  - REFACTOR: reuse the harness in `parity_test.go`; /simplify
- [x] 2.2 Verify slice 2: `internal/paritytest` passes with `-race`, and `go vet` and `golangci-lint` are clean

## 3. Slice: `ginprblm.ErrStatusOnly`

- [x] 3.1 Status-only aborts are identifiable in the hooks (gin-adapter / Scenario: Classifying a status-only abort in a hook); verify `go test -race -run TestHook_StatusOnly ./` passes in `ginprblm`, in both CI modes
  - RED: `TestHook_StatusOnly` with all nine rows. The status-only rows fail to compile because `ginprblm.ErrStatusOnly` is unexported; confirm that this is the only compile error. Before the export, run the "handler errors" and "no problem written" rows against the current code (temporarily with the internal name in an internal test). That confirms the existing behavior those rows pin, especially `c.String(500, "plain")` with 0 calls. If a row disagrees with the code, stop and report it with the failing output (rule 8). Don't change the row to match.
  - GREEN: run gopls `go_rename_symbol` `errStatusOnly` → `ErrStatusOnly`, and write its godoc (D5).
  - REFACTOR: none expected; /simplify
- [x] 3.2 Documentation; verify `go test -run Example ./` passes in `ginprblm`
  - In `ginprblm/doc.go` and the doc example, show a hook that tells a status-only abort apart with `errors.Is`, and still treats a 5xx status as a failure.
  - `ginprblm` CHANGELOG entry, Added.
- [x] 3.3 Verify slice 3: `GOWORK=off go mod tidy -diff`, `go vet`, `golangci-lint` and both test modes are clean in `ginprblm`, and `go tool cover -func` shows the status-only, recorded-error and no-problem branches of `finish` covered

## 4. Review fixes

- [x] 4.1 gin's own unrouted 404 and 405 reach the hooks as `ErrStatusOnly`, and a recorded error after the body still reaches them (gin-adapter / Scenario: Classifying a status-only abort in a hook, Examples: gin's own aborts, and the "error after body" row); verify `go test -race -run TestHook_StatusOnly ./` passes in `ginprblm`
  - Characterization test (expected to pass on first run; the behavior already exists): add the unrouted `GET` (404) and wrong-method with `HandleMethodNotAllowed` (405) rows as positive assertions, and the "error after body" row. Confirm they are meaningful: flip each row's expectation temporarily and see it fail, then restore.
  - Docs: the `ErrStatusOnly` godoc (`ginprblm/middleware.go`) and `ginprblm/doc.go` say gin's unrouted 404/405 also match, so a hook shouldn't treat every `ErrStatusOnly` below 500 as an intentional rejection (D5).
- [x] 4.2 `AddErrorHook(nil)` leaves a handler not reporting (error-mapping / Examples: misuse, `AddErrorHook(nil)`); verify `go test -race -run TestAddErrorHook_NilIsNotReporting ./` passes
  - RED: a white-box test through `export_test.go` asserting that `NewHandler(AddErrorHook(nil))` has `isReporting` false. Remove the `if hook != nil` guard temporarily and confirm the test fails, then restore it (GREEN).
- [x] 4.3 The discovery-404-with-panicking-enricher row of `TestHooks_Notes` is a spec row (error-mapping / Scenario: Every hook receives notes and silent requests reach none); remove its "Not a spec row" comment
- [x] 4.4 Documentation fixes; verify `go test -run Example ./...` passes
  - The `report` comment (`handler.go`) covers all hooks, not only `OnError`.
  - README hook section, `ginprblm/middleware.go` and `fiberprblm/handler.go` say "the hooks (`OnError` and `AddErrorHook`)" instead of "the `OnError` hook".
  - The `AddErrorHook` godoc notes that hooks may run during resolution (type notes) before the response is written, so modifying the problem changes the response.
  - RELEASING.md step 2: "the module's CHANGELOG.md (root, or `<module>/CHANGELOG.md` when it has one)".
- [x] 4.5 Verify the review fixes: in every touched module `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...`, `GOWORK=off go test ./...` and `GOWORK=off go mod tidy -diff` are clean, and `openspec validate compose-error-hooks --strict` passes

## 5. Review fixes, round 2

- [ ] 5.1 The documented example hook survives a nil problem (D3, nil problem failure mode); verify `go test -race -run 'TestExampleHook_NilProblem|ExampleAddErrorHook' ./` passes
  - RED: factor the `ExampleAddErrorHook` library hook into a package-level function in `example_test.go` that the example uses, and add `TestExampleHook_NilProblem` (core test package) that registers it with `AddErrorHook` and triggers a panic note (a panicking mapper). It fails because the hook dereferences the nil problem, and the panic note's record is lost.
  - GREEN: guard `p != nil` in the example hook and in the README `AddErrorHook` snippet. The `OnError` and `AddErrorHook` godocs say that `p` may be nil (panic notes and `ErrShadowedDocumentation`).
- [ ] 5.2 Docs: the remaining "the OnError hook" wording says "the hooks ([OnError] and [AddErrorHook])" in `handler.go` (`Resolve`, `Respond`, `Recover`, `NoteShadowedRoute`, `Discovery` and the internal comments), `panic.go` (`PanicError`, `safeCall`) and `ginprblm/middleware.go`
- [ ] 5.3 `ginprblm/CHANGELOG.md` says gin's own unrouted 404/405 also match `ErrStatusOnly`
- [ ] 5.4 `ginprblm/middleware_test.go`: no blank line between the scenario comment and `TestHook_StatusOnly`
- [ ] 5.5 ginprblm godoc notes that when a handler writes a body and then records an error, the hooks get a 500 problem, not the status the client received (existing behavior; doc only)
- [ ] 5.6 Verify round 2: in every touched module `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...`, `GOWORK=off go test ./...` and `GOWORK=off go mod tidy -diff` are clean, and `openspec validate compose-error-hooks --strict` passes

## Workflow follow-up

- Reply on the srvr request: link this change, and point out the 5xx caveat of `ErrStatusOnly` (D5).
- Release `ginprblm` with slice 3 whenever needed (it works with core v0.1.0). The core's `AddErrorHook` ships in core v0.2.0 together with `isolate-parsed-problems`. Ask before tagging.
- Archive the change after review.
