package prblm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kartaladev/prblm"
)

// The request-path benchmarks (design D2, D3 of add-benchmarks). Every
// adapter module defines the same cases, with the same names, errors,
// options and Accept values, so their rows compare by name.

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

// The paths the request-path cases are served at.
const (
	benchPathVessel = "/v1/vessels/7"
)

// baselineBody is what the baseline handler sends: the same status as the
// mapped-json case, with a fixed JSON body and no prblm. It is a variable so
// that the baseline does not allocate it on every request.
var baselineBody = []byte(`{"error":"vessel not found"}`)

// benchOptions is the one configuration every prblm case is built from. It
// follows the parity test's configuration (internal/paritytest), with the
// mappers the cases need, and adds an OnError hook that does nothing, so the
// hook call is part of the measured path.
func benchOptions() []prblm.Option {
	return []prblm.Option{
		prblm.WithNamespace("/v1/api/probs"),
		prblm.WithTypes(blacklistedSupplier),
		prblm.WithMappers(prblm.Status(errVesselNotFound, http.StatusNotFound)),
		prblm.WithEnrichers(func(_ context.Context, p *prblm.Problem) { _ = p.Set("trace_id", "t1") }),
		prblm.OnError(func(context.Context, error, *prblm.Problem) {}),
	}
}

// wrapCase is one request-path case through [prblm.Handler.Wrap].
type wrapCase struct {
	// err is what the route returns.
	err         error
	name        string
	path        string
	contentType string
	marker      string
	status      int
	// baseline selects the handler that answers without prblm.
	baseline bool
}

// newWrapServer returns the server of tc: a ServeMux routing tc.path to a
// handler, built once per case, as an application builds it at startup.
func newWrapServer(tc wrapCase) http.Handler {
	var route http.Handler
	if tc.baseline {
		route = http.HandlerFunc(serveBaseline)
	} else {
		h := prblm.NewHandler(benchOptions()...)
		route = h.Wrap(func(http.ResponseWriter, *http.Request) error { return tc.err })
	}

	mux := http.NewServeMux()
	mux.Handle("GET /v1/vessels/{id}", route)

	return mux
}

// serveBaseline answers like a handler that does not use prblm.
func serveBaseline(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(baselineBody)
}

// expectResponse serves req once and fails b unless the response is what
// tc expects, so a benchmark never measures a broken path (for example a
// mapper that no longer matches, so every iteration is the 500 fallback).
func expectResponse(b *testing.B, server http.Handler, req *http.Request, tc wrapCase) {
	b.Helper()

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	contentType := rec.Header().Get("Content-Type")
	if rec.Code != tc.status || contentType != tc.contentType || !strings.Contains(rec.Body.String(), tc.marker) {
		b.Fatalf("got %d %q %s, want %d %q containing %s",
			rec.Code, contentType, rec.Body, tc.status, tc.contentType, tc.marker)
	}
}

// BenchmarkWrap measures the net/http request path: routing, the handler,
// Wrap, resolution, enrichment, encoding and writing the response.
func BenchmarkWrap(b *testing.B) {
	cases := []wrapCase{
		{
			name:        "baseline",
			baseline:    true,
			path:        benchPathVessel,
			status:      http.StatusNotFound,
			contentType: jsonType,
			marker:      string(baselineBody),
		},
		{
			name:        "mapped-json",
			err:         errVesselNotFound,
			path:        benchPathVessel,
			status:      http.StatusNotFound,
			contentType: problemJSONType,
			marker:      `"trace_id":"t1"`,
		},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			server := newWrapServer(tc)
			req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, tc.path, http.NoBody)

			expectResponse(b, server, req, tc)

			w := newDiscardWriter()

			b.ReportAllocs()

			for b.Loop() {
				w.reset()
				server.ServeHTTP(w, req)
				benchSink = benchResult{status: w.status, written: w.written}
			}
		})
	}
}
