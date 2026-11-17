package prblm_test

import (
	"context"
	"errors"
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
			prblm.Status(errVesselNotFound, http.StatusNotFound),
			prblm.Is(errBlacklisted, func(error) *prblm.Problem { return blacklistedSupplier.New() }),
		),
		prblm.WithEnrichers(func(_ context.Context, p *prblm.Problem) { _ = p.Set("trace_id", "t1") }),
		prblm.OnError(func(context.Context, error, *prblm.Problem) {}),
	}
}

// wrapCase is one request-path case through [prblm.Handler.Wrap].
type wrapCase struct {
	// err is what the routes return.
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

// newWrapServer returns the server of tc, built once per case, as an
// application builds it at startup: a ServeMux routing the vessel and
// contract paths to a handler returning tc.err through Wrap, with the type
// documentation mounted at the discovery path, as internal/paritytest does.
// The baseline routes the vessel path to a handler without prblm.
func newWrapServer(tc wrapCase) http.Handler {
	mux := http.NewServeMux()

	if tc.baseline {
		mux.HandleFunc("GET /v1/vessels/{id}", serveBaseline)

		return mux
	}

	h := prblm.NewHandler(benchOptions()...)
	route := h.Wrap(func(http.ResponseWriter, *http.Request) error { return tc.err })

	mux.Handle("GET /v1/vessels/{id}", route)
	mux.Handle("GET /v1/contracts/{id}", route)
	mux.Handle("GET "+h.DiscoveryPath()+"/", h.Discovery())

	return mux
}

// serveBaseline answers like a handler that does not use prblm.
func serveBaseline(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(baselineBody)
}

// newWrapRequest returns the request of tc, built once, before the loop. Its
// context is never canceled and it has no body. A revalidating request
// carries the ETag that server sends for the same request without it.
func newWrapRequest(b *testing.B, server http.Handler, tc wrapCase) *http.Request {
	b.Helper()

	req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, tc.path, http.NoBody)
	if tc.accept != "" {
		req.Header.Set("Accept", tc.accept)
	}

	if tc.revalidate {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

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
func expectResponse(b *testing.B, server http.Handler, req *http.Request, tc wrapCase) {
	b.Helper()

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

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

// wrapCases are the request-path cases of design D3 (add-benchmarks).
var wrapCases = []wrapCase{
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
	{
		name:        "fallback-500",
		err:         errUnmapped,
		path:        benchPathVessel,
		status:      http.StatusInternalServerError,
		absent:      "hunter2",
		contentType: problemJSONType,
		marker:      `"title":"Internal Server Error"`,
	},
	{
		name:        "xml",
		err:         errVesselNotFound,
		path:        benchPathVessel,
		accept:      problemXMLType,
		status:      http.StatusNotFound,
		contentType: problemXMLType,
		marker:      "<trace_id>t1</trace_id>",
	},
	{
		name:        "resolved-type",
		err:         errBlacklisted,
		path:        benchPathContract,
		status:      http.StatusConflict,
		contentType: problemJSONType,
		marker:      `"type":"/v1/api/probs/contracts/blacklisted-supplier"`,
	},
	{
		name:        "discovery-200",
		path:        benchPathTypeDoc,
		accept:      "application/json",
		status:      http.StatusOK,
		contentType: jsonType,
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

// BenchmarkWrap measures the net/http request path: routing, the handler,
// Wrap, resolution, enrichment, encoding and writing the response, and the
// type documentation served at the discovery path.
func BenchmarkWrap(b *testing.B) {
	for _, tc := range wrapCases {
		b.Run(tc.name, func(b *testing.B) {
			server := newWrapServer(tc)
			req := newWrapRequest(b, server, tc)

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

// BenchmarkResolve measures resolution, finalization and enrichment of an
// error, without encoding it.
func BenchmarkResolve(b *testing.B) {
	h := prblm.NewHandler(benchOptions()...)

	cases := []struct {
		err    error
		name   string
		status int
	}{
		{name: "mapped", err: errVesselNotFound, status: http.StatusNotFound},
		{name: "fallback", err: errUnmapped, status: http.StatusInternalServerError},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			ctx := b.Context()

			if p := h.Resolve(ctx, tc.err); p.Status != tc.status {
				b.Fatalf("Resolve() status = %d, want %d", p.Status, tc.status)
			}

			b.ReportAllocs()

			for b.Loop() {
				_ = h.Resolve(ctx, tc.err)
			}
		})
	}
}

// BenchmarkRespond measures Respond for a mapped error: resolution,
// negotiation and encoding, as JSON and as XML. BenchmarkNegotiate measures
// the negotiation of other Accept values.
func BenchmarkRespond(b *testing.B) {
	h := prblm.NewHandler(benchOptions()...)

	cases := []struct {
		name        string
		accept      string
		contentType string
	}{
		{name: "json", contentType: problemJSONType},
		{name: "xml", accept: problemXMLType, contentType: problemXMLType},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			ctx := b.Context()
			req := prblm.Request{Method: http.MethodGet, Path: benchPathVessel, Accept: tc.accept}

			if res, ok := h.Respond(ctx, req, errVesselNotFound); !ok || res.Header.Get("Content-Type") != tc.contentType {
				b.Fatalf("Respond() = %q, %v; want %q", res.Header.Get("Content-Type"), ok, tc.contentType)
			}

			b.ReportAllocs()

			for b.Loop() {
				h.Respond(ctx, req, errVesselNotFound)
			}
		})
	}
}
