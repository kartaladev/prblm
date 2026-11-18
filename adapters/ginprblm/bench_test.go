package ginprblm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/adapters/ginprblm"
)

// The request-path benchmarks (design D2, D3 of add-benchmarks). Every
// adapter module defines the same cases, with the same names, errors,
// options and Accept values, so their rows compare by name with the core's
// BenchmarkWrap.

// benchResult is the observable result of one benchmarked request.
type benchResult struct {
	status  int
	written int
}

// benchSink receives each iteration's result, so the compiler cannot
// optimize the request away.
var benchSink benchResult

// discardWriter is the minimal reusable [http.ResponseWriter] the
// request-path benchmarks write to: it records the status and counts the
// body bytes. Unlike [httptest.ResponseRecorder], it neither grows a buffer
// nor clones headers, so what it allocates is not mistaken for the
// library's allocations.
type discardWriter struct {
	header  http.Header
	status  int
	written int
}

func newDiscardWriter() *discardWriter {
	return &discardWriter{header: make(http.Header)}
}

func (w *discardWriter) Header() http.Header { return w.header }

func (w *discardWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *discardWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	w.written += len(b)

	return len(b), nil
}

// reset prepares w for the next iteration. The header map is cleared, not
// replaced, so it does not grow across iterations and is not reallocated.
func (w *discardWriter) reset() {
	clear(w.header)
	w.status, w.written = 0, 0
}

// The paths the request-path cases are served at: the parity test's paths
// (internal/paritytest).
const (
	benchPathVessel   = "/v1/vessels/7"
	benchPathContract = "/v1/contracts/7"
	benchPathTypeDoc  = "/v1/api/probs/contracts/blacklisted-supplier"
)

var (
	// errBlacklisted is mapped to a problem of blacklistedSupplier.
	errBlacklisted = errors.New("supplier is blacklisted")
	// errUnmapped is recognized by no mapper. Its text must never be sent.
	errUnmapped = errors.New("pq: password=hunter2")
)

// blacklistedSupplier is the one registered type definition, as in the
// parity test.
var blacklistedSupplier = prblm.DefineType(
	"contracts/blacklisted-supplier",
	http.StatusConflict,
	"Supplier is blacklisted",
	"The supplier is on the procurement blacklist.",
)

// baselineBody is what the baseline handler sends: the same status as the
// mapped-json case, with a fixed JSON body and no prblm. It is a variable so
// that the baseline does not allocate it on every request.
var baselineBody = []byte(`{"error":"vessel not found"}`)

// benchOptions is the one configuration every prblm case is built from: the
// parity test's configuration (internal/paritytest), plus an OnError hook
// that does nothing, so the hook call is part of the measured path.
func benchOptions() []prblm.Option {
	return []prblm.Option{
		prblm.WithNamespace("/v1/api/probs"),
		prblm.WithTypes(blacklistedSupplier),
		prblm.WithMappers(
			prblm.Status(ErrVesselNotFound, http.StatusNotFound),
			prblm.Is(errBlacklisted, func(error) *prblm.Problem { return blacklistedSupplier.New() }),
		),
		prblm.WithEnrichers(func(_ context.Context, p *prblm.Problem) { _ = p.Set("trace_id", "t1") }),
		prblm.OnError(func(context.Context, error, *prblm.Problem) {}),
	}
}

// middlewareCase is one request-path case through [ginprblm.Middleware].
type middlewareCase struct {
	// err is what the routes record with c.Error.
	err    error
	name   string
	path   string
	accept string
	// contentType, marker and status are what the response must have.
	contentType string
	marker      string
	status      int
	// absent is text the body must not contain.
	absent string
	// baseline selects the handler that answers without prblm.
	baseline bool
	// emptyBody requires a response without a body.
	emptyBody bool
	// revalidate sends the ETag of a first response in If-None-Match.
	revalidate bool
}

// newBenchEngine returns the engine of tc, built once per case, as an
// application builds it at startup: Recovery and Middleware installed with
// engine.Use, as internal/paritytest does, and the vessel and contract
// routes recording tc.err. Middleware serves the type documentation. The
// baseline routes the vessel path to a handler without prblm.
func newBenchEngine(tc middlewareCase) *gin.Engine {
	engine := gin.New()

	if tc.baseline {
		engine.GET("/v1/vessels/:id", func(c *gin.Context) {
			c.Data(http.StatusNotFound, "application/json", baselineBody)
		})

		return engine
	}

	opts := benchOptions()
	engine.Use(ginprblm.Recovery(opts...), ginprblm.Middleware(opts...))

	route := func(c *gin.Context) { _ = c.Error(tc.err) }
	engine.GET("/v1/vessels/:id", route)
	engine.GET("/v1/contracts/:id", route)

	return engine
}

// newBenchRequest returns the request of tc, built once, before the loop.
// Its context is never canceled and it has no body. A revalidating request
// carries the ETag that engine sends for the same request without it.
func newBenchRequest(b *testing.B, engine http.Handler, tc middlewareCase) *http.Request {
	b.Helper()

	req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, tc.path, http.NoBody)
	if tc.accept != "" {
		req.Header.Set("Accept", tc.accept)
	}

	if tc.revalidate {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		etag := rec.Header().Get("ETag")
		if etag == "" {
			b.Fatalf("got %d without an ETag to revalidate", rec.Code)
		}

		req.Header.Set("If-None-Match", etag)
	}

	return req
}

// expectResponse serves req once and fails b unless the response is what
// tc expects, so a benchmark never measures a broken path (for example a
// mapper that no longer matches, so every iteration is the 500 fallback).
func expectResponse(b *testing.B, engine http.Handler, req *http.Request, tc middlewareCase) {
	b.Helper()

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	contentType := rec.Header().Get("Content-Type")
	if rec.Code != tc.status || contentType != tc.contentType || !strings.Contains(rec.Body.String(), tc.marker) {
		b.Fatalf("got %d %q %s, want %d %q containing %s",
			rec.Code, contentType, rec.Body, tc.status, tc.contentType, tc.marker)
	}

	if tc.emptyBody && rec.Body.String() != "" {
		b.Fatalf("got body %s, want none", rec.Body.String())
	}

	if tc.absent != "" && strings.Contains(rec.Body.String(), tc.absent) {
		b.Fatalf("got %s, want it without %q", rec.Body.String(), tc.absent)
	}
}

// middlewareCases are the request-path cases of design D3 (add-benchmarks).
var middlewareCases = []middlewareCase{
	{
		name:        "baseline",
		baseline:    true,
		path:        benchPathVessel,
		status:      http.StatusNotFound,
		contentType: "application/json",
		marker:      string(baselineBody),
	},
	{
		name:        "mapped-json",
		err:         ErrVesselNotFound,
		path:        benchPathVessel,
		status:      http.StatusNotFound,
		contentType: "application/problem+json",
		marker:      `"trace_id":"t1"`,
	},
	{
		name:        "fallback-500",
		err:         errUnmapped,
		path:        benchPathVessel,
		status:      http.StatusInternalServerError,
		absent:      "hunter2",
		contentType: "application/problem+json",
		marker:      `"title":"Internal Server Error"`,
	},
	{
		name:        "xml",
		err:         ErrVesselNotFound,
		path:        benchPathVessel,
		accept:      "application/problem+xml",
		status:      http.StatusNotFound,
		contentType: "application/problem+xml",
		marker:      "<trace_id>t1</trace_id>",
	},
	{
		name:        "resolved-type",
		err:         errBlacklisted,
		path:        benchPathContract,
		status:      http.StatusConflict,
		contentType: "application/problem+json",
		marker:      `"type":"/v1/api/probs/contracts/blacklisted-supplier"`,
	},
	{
		name:        "discovery-200",
		path:        benchPathTypeDoc,
		accept:      "application/json",
		status:      http.StatusOK,
		contentType: "application/json",
		marker:      `"description":"The supplier is on the procurement blacklist."`,
	},
	{
		name:       "discovery-304",
		path:       benchPathTypeDoc,
		accept:     "application/json",
		revalidate: true,
		emptyBody:  true,
		status:     http.StatusNotModified,
	},
}

// BenchmarkMiddleware measures the gin request path: gin's routing and
// context, Recovery and Middleware, resolution, enrichment, encoding and
// writing the response, and the type documentation Middleware serves.
func BenchmarkMiddleware(b *testing.B) {
	for _, tc := range middlewareCases {
		b.Run(tc.name, func(b *testing.B) {
			engine := newBenchEngine(tc)
			req := newBenchRequest(b, engine, tc)

			expectResponse(b, engine, req, tc)

			w := newDiscardWriter()

			b.ReportAllocs()

			for b.Loop() {
				w.reset()
				engine.ServeHTTP(w, req)
				benchSink = benchResult{status: w.status, written: w.written}
			}
		})
	}
}
