# Benchmarks

The benchmarks measure what prblm costs on the request path of each adapter, next to a baseline
handler that answers the same request without prblm. The numbers in the README's "Performance"
section are produced only by the protocol below, and every one of them can be reproduced from a
raw result file committed in `bench/results/`.

## The cases

Every adapter module runs the same request-path cases, with the same names, errors, options and
`Accept` values, so their rows compare by name. Each case runs with one enricher (`trace_id`) and
an `OnError` hook that does nothing, the shape of a real configuration.

| Case | Request | Expected response |
|---|---|---|
| `baseline` | `GET /v1/vessels/7` | 404 with a fixed JSON body, without prblm |
| `mapped-json` | `GET /v1/vessels/7`, the handler returns a mapped error | 404 `application/problem+json` |
| `fallback-500` | `GET /v1/vessels/7`, the handler returns an error no mapper recognizes | 500 `application/problem+json` |
| `xml` | as `mapped-json`, with `Accept: application/problem+xml` | 404 `application/problem+xml` |
| `resolved-type` | `GET /v1/contracts/7`, mapped to a registered type under the namespace `/v1/api/probs` | 409, with the resolved `type` |
| `discovery-200` | `GET /v1/api/probs/contracts/blacklisted-supplier`, `Accept: application/json` | 200 `application/json` |
| `discovery-304` | as `discovery-200`, with the page's `ETag` in `If-None-Match` | 304 |

| Module | Benchmark | Source |
|---|---|---|
| `github.com/kartaladev/prblm` (net/http) | `BenchmarkWrap` | `bench_test.go` |
| `github.com/kartaladev/prblm/ginprblm` | `BenchmarkMiddleware` | `ginprblm/bench_test.go` |
| `github.com/kartaladev/prblm/fiberprblm` | `BenchmarkErrorHandler` | `fiberprblm/bench_test.go` |

The core also has micro-benchmarks of its hot paths:

| Benchmark | Cases | Source |
|---|---|---|
| `BenchmarkResolve` | `mapped`, `fallback` | `bench_test.go` |
| `BenchmarkRespond` | `json`, `xml` | `bench_test.go` |
| `BenchmarkNegotiate` | `empty`, `any` (`*/*`), `json` (`application/json`), `browser`, `xml`, `malformed` (the `Accept` header) | `respond_internal_test.go` |
| `BenchmarkIsSubtype` | | `namespace_internal_test.go` |
| `BenchmarkResolveType` | `resolved`, `kept`, `invalid`, `undocumented` | `namespace_internal_test.go` |

Before it is timed, each case serves one request and fails unless the status, `Content-Type` and
a body marker are what the case expects, so a benchmark never measures a broken path. The
`discovery-304` response must also have an empty body, and the `fallback-500` body must not contain
the error's text.

The fiber benchmark calls the app's fasthttp handler (`app.Handler()`) directly, not `app.Test`,
whose in-memory network round trip would be measured too. Like fasthttp's server on a kept-alive
connection, it reuses one `fasthttp.RequestCtx`: before every request, the response is reset and a
request built once is copied in. Its check serves a different request (the type documentation as
an HTML page) on that context first, then the case's request, and fails unless the response is the
same as the one a freshly initialized `RequestCtx` gets, so no state leaks from one request into the
next.

## Checking that the benchmarks run

Every benchmark runs once, without timing, in each module:

```sh
go test -run '^$' -bench . -benchtime 1x ./...
(cd ginprblm && go test -run '^$' -bench . -benchtime 1x ./...)
(cd fiberprblm && go test -run '^$' -bench . -benchtime 1x ./...)
```

This is the smoke run. It needs no idle machine, and its timings mean nothing.

## Measurement protocol

Numbers are measured only on an idle machine; a loaded machine gives noisy, unrepeatable numbers.

### 1. Prepare the machine

- Close builds, browsers and anything that indexes files (IDEs, search).
- Run on mains power, and let the machine rest for 2 minutes.
- Record the environment, which every published table names:

  ```sh
  go version
  go env GOOS GOARCH
  sysctl -n machdep.cpu.brand_string   # macOS
  lscpu | grep 'Model name'            # Linux
  git rev-parse --short HEAD
  ```

### 2. Run and save the raw output

Run each module with `GOWORK=off`, so it is measured as released, against the dependency versions
in its own `go.mod`. An adapter module is then measured with the core version its `go.mod`
requires, not the core in the repository. The raw output goes to
`bench/results/<module>-<yyyy-mm-dd>-<short-sha>.txt`, where `<module>` is `core` for the root
module and the directory name for the others. From the repository root:

```sh
mkdir -p bench/results
GOWORK=off go test -run '^$' -bench . -benchmem -count 10 -benchtime 1s ./... \
  | tee "bench/results/core-$(date +%F)-$(git rev-parse --short HEAD).txt"
```

gin:

```sh
(cd ginprblm && GOWORK=off go test -run '^$' -bench . -benchmem -count 10 -benchtime 1s ./...) \
  | tee "bench/results/ginprblm-$(date +%F)-$(git rev-parse --short HEAD).txt"
```

fiber:

```sh
(cd fiberprblm && GOWORK=off go test -run '^$' -bench . -benchmem -count 10 -benchtime 1s ./...) \
  | tee "bench/results/fiberprblm-$(date +%F)-$(git rev-parse --short HEAD).txt"
```

Commit the raw file: later comparisons with `benchstat` need it, not only the summary.

### 3. Summarize with benchstat

Install `benchstat` once; it is a developer tool and is not added to any `go.mod`:

```sh
go install golang.org/x/perf/cmd/benchstat@latest
```

The README tables are its output for the raw file:

```sh
benchstat bench/results/core-<yyyy-mm-dd>-<short-sha>.txt
benchstat bench/results/ginprblm-<yyyy-mm-dd>-<short-sha>.txt
benchstat bench/results/fiberprblm-<yyyy-mm-dd>-<short-sha>.txt
```

`benchstat` reports the spread of every row (`± n%`). A row above ±5% is measured again. If it
stays above, the published table marks the variance.

### Comparing two runs

Compare only files from the same machine, old against new:

```sh
benchstat bench/results/core-<old>.txt bench/results/core-<new>.txt
```

A change is an improvement only when `benchstat` reports it as significant (p < 0.05); a `~`
means no measurable difference.
