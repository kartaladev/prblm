# Tasks

This change edits specification text only, so it has no TDD behavior task. The failing check that demands it is `openspec validate --specs --strict`, which fails on `main` for `error-mapping`, `fiber-adapter`, `problem-response` and `validation-error-mapping`. The change is done when a dry-run of the archive makes that check pass.

## 1. Slice: the five requirements state one behavior each, and the main specs pass strict validation

- [x] 1.1 Confirm the RED state on `main`; verify `openspec validate --specs --strict` fails with "Requirement text is very long (>500 characters)" for exactly these five requirements: `error-mapping` [17], `fiber-adapter` [1] and [7], `problem-response` [3], `validation-error-mapping` [9]
- [x] 1.2 Check that the tightened requirements lose no rule (design D1); verify each phrase dropped from "Error values with panicking methods never break resolution" and "Translator panics are contained once" is stated by an Examples row of the same requirement
  - Error values with panicking methods never break resolution:
    - "`Unwrap`, `Is` or `As`" → the rows with `As`, `Is` and `Unwrap() error` panicking.
  - Translator panics are contained once:
    - "so the handler's `OnError` hook can find it" → rows 1, 4, 5 and 6 ("the `OnError` hook finds a `*prblm.PanicError`").
    - "as a `*prblm.PanicError` with a nil value" → rows 4 and 5.
    - "as the core does" → row 3, `http.ErrAbortHandler` with no response.
- [x] 1.3 Update the one test comment that names a split requirement (design D2); verify `grep -rn "Fiber errors are status hints without their message" --include='*_test.go' .` finds nothing, and in `adapters/fiberprblm`: `go vet ./...`, `golangci-lint run ./...` and `GOWORK=off go test -race ./...` pass
  - `adapters/fiberprblm/handler_test.go`: the comment's parenthesis names "Errors the adapter cannot walk get no fiber hint" instead.
- [x] 1.4 Dry-run the archive in a scratch copy of the repository; verify each of the following:
  - `openspec archive split-long-requirements --yes` succeeds in the scratch copy.
  - `openspec validate --specs --strict` passes there, 15 of 15 specs.
  - Each of the 14 scenarios of the five original requirements appears exactly once in the synced main specs, byte for byte as on `main`, under the requirement the deltas place it in.
  - No other requirement of the four specs changed.
- [x] 1.5 Verify the slice end to end; verify `openspec validate split-long-requirements --strict` passes, `go test -race ./...` passes in the core, gin and fiber modules, and no Go file other than the one test comment changed (`git diff --stat origin/main -- '*.go'`). No hot path changes, so no coverage change is expected; confirm `go test -coverprofile` totals for `adapters/fiberprblm` are unchanged at 100%.
