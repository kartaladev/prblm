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

The adapters also run with `GOWORK=off`, against the released core in their `go.mod`, the
configuration the published numbers are measured in:

```sh
(cd ginprblm && GOWORK=off go test -run '^$' -bench . -benchtime 1x ./...)
(cd fiberprblm && GOWORK=off go test -run '^$' -bench . -benchtime 1x ./...)
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

- Record the module versions each adapter is measured against, as its own `go.mod` resolves them
  (step 2 runs with `GOWORK=off`). From the repository root:

  ```sh
  (cd ginprblm && GOWORK=off go list -m github.com/kartaladev/prblm github.com/gin-gonic/gin)
  (cd fiberprblm && GOWORK=off go list -m github.com/kartaladev/prblm github.com/gofiber/fiber/v3 github.com/valyala/fasthttp)
  ```

### 2. Run and save the raw output

Run each module with `GOWORK=off`, so it is measured as released, against the dependency versions
in its own `go.mod`. An adapter module is then measured with the released core its `go.mod`
requires, not the core in the repository, so the published adapter tables are measured again after
every core release. To compare a change to the core in the adapters before it is released, run the
adapters in workspace mode (without `GOWORK=off`), old against new. The raw output goes to
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

## Profiles

Profiles show where a request's time and memory go, so optimizations target measured costs.
They are taken per adapter from the request-path benchmark, over all its cases, and are not
committed: they are binary and specific to one machine.

### Memory profiles

Allocation counts do not depend on machine load, so memory profiles can be taken at any time.
`-memprofilerate 1` records every allocation instead of a sample. From the repository root, into a
scratch directory `$PROF`:

```sh
go test -run '^$' -bench BenchmarkWrap -benchtime 5s -memprofilerate 1 \
  -memprofile "$PROF/core.mem" -o "$PROF/core.test" .
(cd ginprblm && go test -run '^$' -bench BenchmarkMiddleware -benchtime 5s -memprofilerate 1 \
  -memprofile "$PROF/gin.mem" -o "$PROF/gin.test" .)
(cd fiberprblm && go test -run '^$' -bench BenchmarkErrorHandler -benchtime 5s -memprofilerate 1 \
  -memprofile "$PROF/fiber.mem" -o "$PROF/fiber.test" .)
```

They run in workspace mode, so the adapters use the core in the repository, the code an
optimization changes. Only functions in this repository can be changed, so every allocation is
counted against the deepest function of this repository on its stack, including the allocations the
standard library or a framework makes on its behalf:

```sh
go tool pprof -sample_index alloc_space -show '^github\.com/kartaladev/prblm' -top \
  "$PROF/core.test" "$PROF/core.mem"
```

`-sample_index alloc_objects` gives the allocation counts, and `-list <function>` shows them per
line. To profile one case, narrow the filter, for example `-bench 'BenchmarkWrap/xml$'`.

### CPU profiles

CPU profiles need an idle machine (see "Measurement protocol"). They use the same commands with
`-cpuprofile "$PROF/<module>.cpu"` in place of the memory flags, and are read the same way, without
`-sample_index`. A plain `-top` puts most samples in the runtime (`kevent`, `madvise`, scheduler
waits) and in the frameworks; `-show` counts that time against the function of this repository
that caused it.

### Profile findings

A function is a candidate for optimization when it has at least 5% flat CPU or at least 10% of
`alloc_space` in a request-path profile. The table lists every candidate, all of them in this
repository. Shares are counted against the deepest function of this repository on the stack, as
described under "Memory profiles", for net/http, gin and fiber in that order. The benchmarks' own
frames (the request and the discarding writer they build) are left out: they are not prblm code.

Memory profiles: commit `68ef42b`. CPU profiles: commit `8359978`, after the two XML optimizations
below, on an idle machine. Both 2026-11-18, Go 1.27.4, darwin/arm64, Apple M4 Pro.

| Function | Module | Flat CPU % | `alloc_space` % | What costs |
|---|---|---|---|---|
| `Problem.MarshalJSON` | core | 2.9 · 2.7 · 2.6 | 16.0 · 15.3 · 17.3 | Two `json.Marshal` calls (standard members, then extensions) and the join of their outputs: 9 of the 28 allocations of `mapped-json`. |
| `marshalXML` | core | 0.2 · 0.4 · 0.3 | 13.8 · 13.6 · 14.0 | `xml.NewEncoder` makes a 4 KB `bufio.Writer` for every document: 52% of the bytes of the `xml` case. |
| `newResponse` | core | 0.8 · 1.1 · 1.2 | 13.4 · 12.8 · 14.4 | Every `Header.Set` and `Add` makes a one-element slice: 6 allocations per problem response. |
| `(*Problem).setExt` | core | < 1 | 11.2 · 10.7 · 12.0 | The extension map, made when the `trace_id` enricher adds the request's first extension. |
| `xmlenc.decodeTree` | core | < 1 | 7.5 · 7.4 · 7.6; `xml` case: 28.1 | Every member goes through `json.Marshal` and back, plain strings included: 32 of the 86 allocations of the `xml` case. Listed from the `xml` case's own profile. |
| `Response.MergeInto` | core | 3.5 · 5.9 · 3.3 | 6.3 · 8.8 · 5.9 | Copies every header value of the problem response, one slice per header, into the response writer's header: 18% to 26% of the allocations. |
| `send` | `fiberprblm` | 11.3 (fiber) | 3.4 (fiber) | Moves the problem's headers through an `http.Header` to merge them by the core's rules, then deletes and re-adds each one in fasthttp, whose header-key normalization is most of the time. |

### What was done with each candidate

Each optimization is compared with `benchstat`, old against new, `-count 10`, on the same machine.
Allocation counts do not depend on machine load, so they are compared at any time with a fixed
iteration count:

```sh
go test -run '^$' -bench 'Benchmark(Wrap|Respond)$' -benchmem -benchtime 2000x -count 10 . > new.txt
benchstat old.txt new.txt
```

Time is compared on an idle machine, with test binaries built from each commit (`go test -c`, in
workspace mode for the adapters, so they run the core under test) and run in turn, ten rounds of
every version, so that drift affects them all alike. Each round runs, per version:

```sh
./core.test -test.run '^$' -test.bench 'Benchmark(Wrap|Respond)$' -test.benchmem -test.benchtime 1s
./gin.test -test.run '^$' -test.bench 'BenchmarkMiddleware$' -test.benchmem -test.benchtime 1s
./fiber.test -test.run '^$' -test.bench 'BenchmarkErrorHandler$' -test.benchmem -test.benchtime 1s
```

An optimization is kept for a significant improvement (p < 0.05) on a request-path case and no
significant regression in any other case, step by step.

| Candidate | Decision | Commit |
|---|---|---|
| `xmlenc.decodeTree` | Kept. A string value skips the JSON round trip: the XML is the same, because the XML encoder replaces invalid UTF-8 as `encoding/json` does (tested under both Go 1.27 JSON implementations, the default and `GOEXPERIMENT=nojsonv2`). | `60d4b7c` |
| `marshalXML` | Kept. The 4 KB `bufio.Writer` comes from a `sync.Pool`, as in net/http: `xml.NewEncoder` reuses a `*bufio.Writer` of at least 4 KB. The writer is fixed-size and never reaches consumer code; `TestRespond_XMLIsolated` covers failed, panicking and large documents concurrently. | `8359978` |
| `Problem.MarshalJSON` | Examined, not changed. Go 1.27's `encoding/json/v2` (`MarshalerTo`, `MarshalWrite`) would write the members in one pass instead of two `Marshal` calls and a join, but importing it breaks every build with `GOEXPERIMENT=nojsonv2`, which Go 1.27 still supports. Revisit when that opt-out is removed. | |
| `newResponse` | Kept. The header values it sets share one array instead of one slice each, as net/http does when it parses headers; each is capped at its length, so an `append` copies instead of overwriting the next (`TestRespond_HeaderValuesIndependent`). 3 fewer allocations per problem response. | `d6dd793` |
| `Response.MergeInto` | Examined, not changed. Copying every value into one shared array saved 2 to 4 allocations per response but was significantly slower in every adapter (+1.3% to +6.5%): the counting pass and the zeroing of one larger array cost more than the small allocations they replaced, which Go 1.27's allocator makes cheap. Reverted. | `65140e6`, reverted in `2c32282` |
| `send` (`fiberprblm`) | Examined, not changed. Most of its time is fasthttp normalizing header keys in `PeekAll`, `Del` and `Add`. Replacing `Del` and `Add` with `Set` would normalize less, but `Set` replaces only the first of several values with the same name, so the merge rules would break for a header the handler set twice. | |
| `(*Problem).setExt` | Examined, not changed. The map holds the extension the enricher adds; making it is the cost of having an extension. | |

#### XML optimizations

The raw files are in `bench/results/compare-2026-11-18-xml/`, one per module and commit:
`15e98f2` (before), `60d4b7c` (strings skip the JSON round trip) and `8359978` (pooled writer). No
other case changed significantly in either step. Fiber's `resolved-type` row is 1.3% slower from
`15e98f2` to `8359978` (p = 0.014), but neither step is significant on its own, and that case runs
no XML code.

```sh
benchstat bench/results/compare-2026-11-18-xml/core-{15e98f2,60d4b7c,8359978}.txt
```

| `xml` case | `15e98f2` | `60d4b7c` | `8359978` |
|---|---|---|---|
| net/http, time/op | 3.446µ ± 3% | 1.884µ ± 2% (−45.33%, p=0.000) | 1.561µ ± 3% (−17.17% against `60d4b7c`, p=0.000) |
| gin, time/op | 3.829µ ± 3% | 2.195µ ± 3% (−42.67%, p=0.000) | 1.718µ ± 2% (−21.75%, p=0.000) |
| fiber, time/op | 4.064µ ± 5% | 2.527µ ± 6% (−37.82%, p=0.000) | 2.174µ ± 1% (−13.97%, p=0.000) |
| net/http, B/op | 8.945Ki | 6.297Ki | 2.237Ki |
| net/http, allocs/op | 86 | 44 | 42 |

`Respond/xml` moved alike: 3.083µ, 1.590µ, 1.276µ.

#### Header optimizations

The raw files are in `bench/results/compare-2026-11-18-headers/`: `8359978` (before), `d6dd793`
(`newResponse`) and `65140e6` (`MergeInto`, reverted).

```sh
benchstat bench/results/compare-2026-11-18-headers/core-{8359978,d6dd793}.txt
benchstat bench/results/compare-2026-11-18-headers/core-{d6dd793,65140e6}.txt
```

| Case, time/op | `8359978` | `d6dd793` | `65140e6` (reverted) |
|---|---|---|---|
| net/http `mapped-json` | 1.251µ ± 2% | 1.185µ ± 2% (−5.32%, p=0.000) | 1.211µ ± 2% (+2.24%, p=0.007) |
| net/http `discovery-304` | 576.1n ± 2% | 573.0n ± 1% (~) | 601.4n ± 1% (+4.94%, p=0.000) |
| gin `mapped-json` | 1.394µ ± 2% | 1.331µ ± 3% (−4.52%, p=0.000) | 1.353µ ± 2% (~) |
| gin `discovery-304` | 426.3n ± 2% | 426.0n ± 2% (~) | 453.9n ± 1% (+6.54%, p=0.000) |
| fiber `mapped-json` | 1.808µ ± 1% | 1.736µ ± 1% (−3.96%, p=0.000) | 1.759µ ± 1% (+1.30%, p=0.001) |
| fiber `discovery-304` | 872.9n ± 1% | 868.1n ± 1% (~) | 895.4n ± 1% (+3.15%, p=0.000) |

`d6dd793` made every problem case 3% to 8% faster in every adapter and slowed none; discovery pages
are built by `newResponse` once, not per request. `Respond/json` went from 979.9n to 903.0n (−7.85%).
