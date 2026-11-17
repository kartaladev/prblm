package fiberprblm_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/fiberprblm"
	"github.com/valyala/fasthttp"
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

// errorHandlerCase is one request-path case through
// [fiberprblm.ErrorHandler].
type errorHandlerCase struct {
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

// newBenchHandler returns the fasthttp handler of the app of tc, built once
// per case, as an application builds it at startup: ErrorHandler and
// Recovery installed as internal/paritytest does, and the vessel and
// contract routes returning tc.err. ErrorHandler serves the type
// documentation. The baseline routes the vessel path to a handler without
// prblm.
//
// The benchmarks drive the handler directly, not through app.Test, whose
// in-memory network round trip would be measured too.
func newBenchHandler(tc errorHandlerCase) fasthttp.RequestHandler {
	if tc.baseline {
		app := fiber.New()
		app.Get("/v1/vessels/:id", func(c fiber.Ctx) error {
			c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)

			return c.Status(http.StatusNotFound).Send(baselineBody)
		})

		return app.Handler()
	}

	app := newApp(benchOptions()...)
	app.Use(fiberprblm.Recovery())

	route := func(fiber.Ctx) error { return tc.err }
	app.Get("/v1/vessels/:id", route)
	app.Get("/v1/contracts/:id", route)

	return app.Handler()
}

// benchServer serves one request at a time to a fasthttp handler, from a
// request built once, before the loop.
type benchServer struct {
	handler fasthttp.RequestHandler
	req     fasthttp.Request
	ctx     fasthttp.RequestCtx
}

// newBenchServer returns the server of tc. Its request has no body. A
// revalidating request carries the ETag that the handler sends for the same
// request without it.
func newBenchServer(b *testing.B, tc errorHandlerCase) *benchServer {
	b.Helper()

	s := &benchServer{handler: newBenchHandler(tc)}
	s.ctx.Init(&s.req, nil, nil)
	s.req.Header.SetMethod(http.MethodGet)
	s.req.SetRequestURI(tc.path)

	if tc.accept != "" {
		s.req.Header.Set(fiber.HeaderAccept, tc.accept)
	}

	if tc.revalidate {
		s.serve()

		etag := s.ctx.Response.Header.Peek(fiber.HeaderETag)
		if len(etag) == 0 {
			b.Fatalf("got %d without an ETag to revalidate", s.ctx.Response.StatusCode())
		}

		s.req.Header.SetBytesV(fiber.HeaderIfNoneMatch, etag)
	}

	return s
}

// serve serves the request once.
func (s *benchServer) serve() { s.serveRequest(&s.req) }

// serveRequest serves req once. The context, initialized once, is reused,
// as fasthttp's server reuses it for the requests of a connection, and starts
// every request from an empty response and a copy of the request, so no
// state leaks between requests.
func (s *benchServer) serveRequest(req *fasthttp.Request) {
	s.ctx.Response.Reset()
	req.CopyTo(&s.ctx.Request)
	s.handler(&s.ctx)
}

// observeFresh serves the request once on a freshly initialized context, as
// the first request of a connection, and returns what the client gets.
func (s *benchServer) observeFresh() observed {
	var ctx fasthttp.RequestCtx

	ctx.Init(&s.req, nil, nil)
	s.handler(&ctx)

	return observeResponse(&ctx.Response)
}

// observed is the part of a response the pre-loop checks compare.
type observed struct {
	// header holds the header lines as sent, sorted, without Date, which
	// changes every second.
	header      string
	contentType string
	body        string
	status      int
}

// observe serves the request once on the reused context and returns what
// the client gets.
func (s *benchServer) observe() observed {
	s.serve()

	return observeResponse(&s.ctx.Response)
}

// observeResponse returns what the client gets of resp.
func observeResponse(resp *fasthttp.Response) observed {
	o := observed{status: resp.StatusCode(), body: string(resp.Body())}

	// The header as sent: fasthttp leaves out the Content-Type of a
	// response without a body, such as a 304, which ContentType reports
	// with its default.
	var lines []string

	for line := range strings.SplitSeq(resp.Header.String(), "\r\n") {
		name, value, ok := strings.Cut(line, ": ")
		if !ok || name == fiber.HeaderDate {
			continue
		}

		if name == fiber.HeaderContentType {
			o.contentType = value
		}

		lines = append(lines, line)
	}

	slices.Sort(lines)
	o.header = strings.Join(lines, "\n")

	return o
}

// otherRequest returns a request unlike every case's: the type
// documentation as an HTML page. Served on the reused context before a case's
// request, it leaves a response (status, headers and body) that a leak
// would carry into the case's response.
func otherRequest() *fasthttp.Request {
	req := new(fasthttp.Request)
	req.Header.SetMethod(http.MethodGet)
	req.SetRequestURI(benchPathTypeDoc)
	req.Header.Set(fiber.HeaderAccept, "text/html")

	return req
}

// expectResponse fails b unless the response of the reused context, after it
// served a different request, is the same as the response of a freshly
// initialized context, and is what tc expects. A benchmark then never
// measures a broken path (for example a mapper that no longer matches, so
// every iteration is the 500 fallback), nor a context that leaks state from
// one request into the next.
func expectResponse(b *testing.B, s *benchServer, tc errorHandlerCase) {
	b.Helper()

	s.serveRequest(otherRequest())

	reused, fresh := s.observe(), s.observeFresh()
	if reused != fresh {
		b.Fatalf("the reused context's response differs from a fresh context's:\n got  %+v\n want %+v", reused, fresh)
	}

	if reused.status != tc.status || reused.contentType != tc.contentType || !strings.Contains(reused.body, tc.marker) {
		b.Fatalf("got %d %q %s, want %d %q containing %s",
			reused.status, reused.contentType, reused.body, tc.status, tc.contentType, tc.marker)
	}

	if tc.emptyBody && reused.body != "" {
		b.Fatalf("got body %s, want none", reused.body)
	}

	if tc.absent != "" && strings.Contains(reused.body, tc.absent) {
		b.Fatalf("got %s, want it without %q", reused.body, tc.absent)
	}
}

// errorHandlerCases are the request-path cases of design D3
// (add-benchmarks).
var errorHandlerCases = []errorHandlerCase{
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

// BenchmarkErrorHandler measures the fiber request path: fiber's routing
// and context, Recovery, ErrorHandler, resolution, enrichment, encoding and
// writing the response, and the type documentation ErrorHandler serves.
func BenchmarkErrorHandler(b *testing.B) {
	for _, tc := range errorHandlerCases {
		b.Run(tc.name, func(b *testing.B) {
			s := newBenchServer(b, tc)

			expectResponse(b, s, tc)

			b.ReportAllocs()

			for b.Loop() {
				s.serve()
				benchSink = benchResult{status: s.ctx.Response.StatusCode(), written: len(s.ctx.Response.Body())}
			}
		})
	}
}
