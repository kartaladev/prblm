# Tasks

Every slice ends with:
- gopls `go_diagnostics` on the changed files, and `go_symbol_references` before any definition is changed
- `go vet`, `golangci-lint run` and `go test -race ./...` in every module the slice touches
- `GOWORK=off go test ./...` for the core, `ginprblm` and `fiberprblm`
- every benchmark the slice adds passing `go test -run '^$' -bench . -benchtime 1x` (the smoke run)

How this change applies TDD:
- **Benchmarks add no behavior.** A benchmark's RED step proves its pre-loop assertion (design D2) is not vacuous: temporarily expect a wrong status, run the smoke command, confirm `b.Fatalf` fires, then restore it.
- **Optimizations** (slice 5) start by recording the `benchstat` baseline and confirming the touched function's coverage. Then they follow design D6.
- **fiber:** correctness tests keep using `app.Test`. Benchmarks drive `app.Handler()` instead (design D2), so that `app.Test`'s network round trip isn't measured.

Tasks marked **(idle machine)** produce timings. Run them only when the machine is otherwise idle, following the protocol in design D4. Every other task can run at any time.

## 1. Slice: one net/http request-path benchmark, measured and published

- [x] 1.1 Add `BenchmarkWrap` with the `baseline` and `mapped-json` cases (design D2, D3); verify `go test -run '^$' -bench BenchmarkWrap -benchtime 1x -benchmem` passes in the core
  - RED: write the case table and the pre-loop assertion, then temporarily expect 500 for `mapped-json` and confirm the smoke run fails with the assertion's message
  - GREEN: the `discardWriter` (header map cleared per iteration), requests built before `b.Loop()`, `b.ReportAllocs()`, and the status and byte count written to a package-level sink
  - REFACTOR: look for Duplicated Code between the case setup and the parity test's configuration (Extract Function `benchOptions()`), then /simplify
- [x] 1.2 Write BENCHMARKS.md (design D4: machine preparation, run command, raw-file naming, `benchstat` summary) and the README "Performance → net/http" subsection with "results pending"; verify the command in BENCHMARKS.md runs as written
- [x] 1.3 **(idle machine)** Run the D4 protocol for `BenchmarkWrap`, commit `bench/results/core-<date>-<sha>.txt`, and fill the net/http table from `benchstat`; verify the table names the CPU, OS, Go version and SHA, and the variance is at most ±5%
- [x] 1.4 Verify slice 1 end to end: suites and lint clean in the core, `GOWORK=off` clean, and the smoke run passes

## 2. Slice: every net/http case and the core micro-benchmarks

- [x] 2.1 Add the `fallback-500`, `xml`, `resolved-type`, `discovery-200` and `discovery-304` cases to `BenchmarkWrap` (design D3); verify the smoke run passes
  - RED: the pre-loop assertion of each new case shown to catch a wrong status or Content-Type (for example, expect `application/problem+json` for `xml`)
  - GREEN: the cases. Discovery mounts `Handler.Discovery()` on a `ServeMux` exactly as `internal/paritytest` does. The 304 case reads the `ETag` from one request made before the loop.
  - REFACTOR: Extract Function for the shared request construction, then /simplify
- [x] 2.2 Add the core micro-benchmarks `BenchmarkResolve` (mapped, fallback), `BenchmarkRespond` (json, xml), `BenchmarkNegotiate` (empty, browser, xml, malformed Accept) and `BenchmarkResolveType` (resolved, kept, invalid, undocumented), next to `BenchmarkIsSubtype`. `BenchmarkRespond` and `BenchmarkNegotiate` replace the earlier `BenchmarkRespond_JSON` and `BenchmarkRespond_XML`. Verify the smoke run passes
  - RED: each sub-benchmark asserts its result once before the loop (resolved status, negotiated media type, resolved type); confirm one wrong expectation fails
  - GREEN: the benchmarks, internal ones in the core's internal test files
  - REFACTOR: name the shared fixtures, then /simplify
- [x] 2.3 **(idle machine)** Re-run the D4 protocol for the core, commit the raw file, and update the net/http table with every case and a micro-benchmark table; verify the variance is at most ±5%
- [x] 2.4 Verify slice 2 end to end: suites, lint, `GOWORK=off` and smoke run clean in the core

## 3. Slice: gin request-path benchmarks

- [x] 3.1 Add `BenchmarkMiddleware` in `ginprblm` with every case of design D3, on a `gin.Engine` built once, using `engine.Use(ginprblm.Recovery(opts...), ginprblm.Middleware(opts...))`; verify `go test -run '^$' -bench BenchmarkMiddleware -benchtime 1x` passes in `ginprblm`
  - RED: each case's pre-loop assertion shown to catch a wrong expectation
  - GREEN: the benchmark, with a gin-local `discardWriter` and the same case names as net/http
  - REFACTOR: Extract Function for the engine setup shared with the parity cases, then /simplify
- [x] 3.2 Add the README "Performance → gin" subsection and the gin commands in BENCHMARKS.md; verify the commands run as written
- [x] 3.3 **(idle machine)** Run the D4 protocol for `ginprblm` (`GOWORK=off`), commit the raw file, and fill the gin table; verify the variance is at most ±5%
- [x] 3.4 Verify slice 3 end to end: suites, lint, `GOWORK=off` and smoke run clean in `ginprblm`

## 4. Slice: fiber request-path benchmarks

- [x] 4.1 Add `BenchmarkErrorHandler` in `fiberprblm` with every case of design D3, driving `app.Handler()` with one reused `fasthttp.RequestCtx` whose response is reset and into which a request built once is copied before every request, as fasthttp does on a kept-alive connection, so context initialization is not counted (design D2); verify `go test -run '^$' -bench BenchmarkErrorHandler -benchtime 1x` passes in `fiberprblm`
  - RED: each case's pre-loop assertion shown to catch a wrong expectation. Add a smoke check that runs the same case twice and compares the results, proving no state leaks between iterations (strengthened in 7.1 to compare against a freshly initialized context).
  - GREEN: the benchmark. `github.com/valyala/fasthttp` becomes a direct requirement of `fiberprblm` (already in the graph through fiber); confirm with `go mod tidy` that no new module is added.
  - REFACTOR: Extract Function for the request-context setup, then /simplify
- [x] 4.2 Add the README "Performance → fiber" subsection and the fiber commands in BENCHMARKS.md; verify the commands run as written
- [x] 4.3 **(idle machine)** Run the D4 protocol for `fiberprblm` (`GOWORK=off`), commit the raw file, and fill the fiber table; verify the variance is at most ±5%
- [x] 4.4 Verify slice 4 end to end: suites, lint, `GOWORK=off` and smoke run clean in `fiberprblm`, and `go.mod` gains no new module

## 5. Slice: profile-driven optimizations

- [x] 5.1 **(idle machine)** Take CPU and memory profiles for each adapter's request-path benchmark (design D5, `-benchtime 5s`), and list every candidate meeting the design D6 thresholds in a "Profile findings" table in BENCHMARKS.md (function, module, flat CPU %, alloc_space %); verify every listed function is in this repository
  - Done 2026-11-18: memory profiles at `68ef42b`, CPU profiles at `8359978` on an idle machine; BENCHMARKS.md "Profile findings"
- [x] 5.2 For each candidate in 5.1, apply the design D6 policy: a separate structural commit when needed, then the optimization; verify the full suites (including `internal/paritytest`) pass with `-race` and `GOWORK=off`, and `go tool cover -func` still covers the function
  - RED: record the **(idle machine)** `benchstat` baseline (old) for the affected cases, and confirm the touched function's tests cover it. If coverage is missing, add a characterization test first.
  - GREEN: the minimal change. Keep it only if `benchstat` (old against new, `-count 10`, idle machine) shows p < 0.05 improvement in time/op or allocs/op on a request-path case and no significant regression elsewhere. Otherwise revert and record "examined, not changed" with the reason.
  - REFACTOR: confirm readability did not suffer for the gain (the refactoring rules), then /simplify
  - Done: kept `60d4b7c` (xmlenc string fast path), `8359978` (pooled XML writer), `d6dd793` (newResponse header array); reverted `65140e6` (MergeInto, slower); examined, not changed: `Problem.MarshalJSON` (Go 1.27 `nojsonv2`), `setExt`, fiberprblm `send`. BENCHMARKS.md "What was done with each candidate"
- [x] 5.3 Record each kept optimization's `benchstat` comparison in BENCHMARKS.md; verify each entry names its commit and the comparison command
- [x] 5.4 Verify slice 5 end to end: suites, lint, `GOWORK=off` and smoke runs clean in every module, the parity test green, and hot-path coverage unchanged or better

## 6. Slice: final results published and smoke-tested in CI

- [x] 6.1 **(idle machine)** Run the D4 protocol once more for all three modules on the final code, commit the raw files, and replace the README tables with the final `benchstat` output (baseline next to prblm per adapter); verify every table names machine, OS, Go version, module versions and SHA
  - Done 2026-11-18 at `940c33f`, the final code: one D4 run per module also serves 1.3, 2.3, 3.3 and 4.3, since measuring the earlier slices' code would publish numbers the final code replaces. fiberprblm was measured twice: its `baseline` row was ±47% in the first run, ±2% in the second. The core was measured again at `91d888b`, after the code review's change to `marshalXML`; the adapters run the released core, so their tables stand
- [x] 6.2 Add the benchmark smoke step to `.github/workflows/ci.yml` for the core, `ginprblm` and `fiberprblm` (`go test -run '^$' -bench . -benchtime 1x ./...`, design D7); verify it passes locally with the same command, and that CI time grows by seconds, not minutes (the `GOWORK=off` run for the adapters is added in 7.6)
- [x] 6.3 Verify the change end to end: every module's suites with `-race`, `GOWORK=off` for the published modules, lint clean, `go_vulncheck` shows no new findings, and no `go.mod` gains a new module

## 7. Review fixes

Found by verifying and reviewing the code tasks above. Each strengthened check is first shown to fail on the defect it targets (RED), then restored.

- [x] 7.1 fiber leak check (design D2): `expectResponse` in `fiberprblm/bench_test.go` compares the response of a freshly initialized `RequestCtx` with the response of the reused context after it served a different request (the 200 that supplies the ETag, or another request); verify the smoke run passes
  - RED: temporarily delete `s.ctx.Response.Reset()` from `serve` and confirm the new comparison (not only the 304 expectation) fails; restore it
- [x] 7.2 net/http parity (design D2): `newWrapServer` in the core's `bench_test.go` builds the route as `h.Recover(h.Wrap(...))`, as `internal/paritytest` does, and its comment says so; verify the smoke run passes
- [x] 7.3 Stronger pre-loop assertions (design D2, D3) in all three adapters: `discovery-304` asserts an empty body, and `fallback-500` asserts the body does not contain the error's text (`hunter2`); verify the smoke runs pass
  - RED: in each adapter, confirm a wrong expectation (a non-empty 304 body expected, the error text expected absent from a body that contains it) fails the smoke run; restore it
- [x] 7.4 `BenchmarkNegotiate` gains the `*/*` and `application/json` Accept cases; BENCHMARKS.md lists them; verify the smoke run passes
  - RED: a wrong expected media type for a new case fails; restore it
- [x] 7.5 BENCHMARKS.md step 1 records the module versions (`GOWORK=off go list -m` of `github.com/kartaladev/prblm` and the framework, in each adapter's directory), step 2 says adapter tables measure the released core in their `go.mod` and are re-measured after a core release, and the README "Performance" intro names machine, OS, Go version, module versions and commit (design D4); verify the commands run as written
- [x] 7.6 CI (design D7): the benchmark smoke step also runs with `GOWORK=off` for `ginprblm` and `fiberprblm`; verify the same commands pass locally in seconds
- [x] 7.7 Verify section 7 end to end: `gofmt -l`, `go vet`, `golangci-lint run`, `go test -race ./...`, `GOWORK=off go test ./...`, `GOWORK=off go mod tidy -diff` and the smoke runs (workspace and `GOWORK=off` for the adapters) clean in the core, `ginprblm`, `fiberprblm` and `internal/paritytest`; `openspec validate add-benchmarks --strict` passes

## Workflow follow-up

- Run `/opsx:verify add-benchmarks`, then archive.
- If `add-mapper-plugins` is still in flight, its release step comes after this change, so the published numbers match the released code.
