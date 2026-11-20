package ginprblm_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/adapters/ginprblm"
	"github.com/kartaladev/prblm/internal/statushint"
)

// gin-adapter / Scenario: Options configure the middleware.
func TestMiddleware_Options(t *testing.T) {
	t.Parallel()

	mapper := prblm.WithMappers(prblm.Status(ErrVesselNotFound, http.StatusNotFound))

	tests := []struct {
		name   string
		opts   []prblm.Option
		status int
	}{
		{name: "none", status: http.StatusInternalServerError},
		{name: "mapper", opts: []prblm.Option{mapper}, status: http.StatusNotFound},
		{name: "nil options around a mapper", opts: []prblm.Option{nil, mapper, nil}, status: http.StatusNotFound},
		{name: "single nil", opts: []prblm.Option{nil}, status: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			engine := gin.New()
			engine.Use(ginprblm.Middleware(tt.opts...))
			engine.GET("/v1/vessels/:id", func(c *gin.Context) { _ = c.Error(ErrVesselNotFound) })

			rec := serve(t, engine, get(t, "/v1/vessels/7"))

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}
		})
	}
}

// gin-adapter / Scenario: Invalid options fail at construction.
func TestMiddleware_InvalidOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		constructor func(...prblm.Option) gin.HandlerFunc
		opts        []prblm.Option
		named       string
	}{
		{
			name:        "error middleware with a relative namespace",
			constructor: ginprblm.Middleware,
			opts:        []prblm.Option{prblm.WithNamespace("v1/api/probs")},
			named:       "WithNamespace",
		},
		{
			name:        "recovery middleware with a relative namespace",
			constructor: ginprblm.Recovery,
			opts:        []prblm.Option{prblm.WithNamespace("v1/api/probs")},
			named:       "WithNamespace",
		},
		{
			name:        "error middleware with types and no namespace",
			constructor: ginprblm.Middleware,
			opts:        []prblm.Option{prblm.WithTypes(OutOfCredit)},
			named:       "WithTypes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if msg := fmt.Sprint(recover()); !strings.Contains(msg, tt.named) {
					t.Errorf("panic = %q, want a message naming %s", msg, tt.named)
				}
			}()

			tt.constructor(tt.opts...)
		})
	}
}

// gin-adapter / Scenario: Recorded errors.
func TestMiddleware_RecordedErrors(t *testing.T) {
	t.Parallel()

	leaky := errors.New("pq: password=hunter2")

	tests := []struct {
		name       string
		middleware gin.HandlerFunc
		handler    gin.HandlerFunc
		status     int
		hookErr    error
	}{
		{
			name:    "sentinel",
			handler: func(c *gin.Context) { _ = c.Error(ErrVesselNotFound) },
			status:  http.StatusNotFound,
			hookErr: ErrVesselNotFound,
		},
		{
			name:    "unmapped",
			handler: func(c *gin.Context) { _ = c.Error(leaky) },
			status:  http.StatusInternalServerError,
			hookErr: leaky,
		},
		{
			name: "last of two",
			handler: func(c *gin.Context) {
				_ = c.Error(ErrVesselNotFound)
				_ = c.Error(ErrLocked)
			},
			status:  http.StatusLocked,
			hookErr: ErrLocked,
		},
		{
			name: "handler after middleware",
			middleware: func(c *gin.Context) {
				_ = c.Error(ErrLocked)
				c.Next()
			},
			handler: func(c *gin.Context) { _ = c.Error(ErrVesselNotFound) },
			status:  http.StatusNotFound,
			hookErr: ErrVesselNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			engine := gin.New()
			engine.Use(ginprblm.Middleware(hook.option(), prblm.WithMappers(
				prblm.Status(ErrVesselNotFound, http.StatusNotFound),
				prblm.Status(ErrLocked, http.StatusLocked),
			)))

			if tt.middleware != nil {
				engine.Use(tt.middleware)
			}

			engine.GET("/v1/vessels/:id", tt.handler)

			rec := serve(t, engine, get(t, "/v1/vessels/7"))

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}

			if p := decodeRecorded(t, rec); p.Instance != "/v1/vessels/7" {
				t.Errorf("instance = %q, want /v1/vessels/7", p.Instance)
			}

			if got := hook.last(); !errors.Is(got, tt.hookErr) {
				t.Errorf("hook received %v, want %v", got, tt.hookErr)
			}
		})
	}
}

// gin-adapter / Scenario: Unmapped error text never leaks.
func TestMiddleware_NoLeak(t *testing.T) {
	t.Parallel()

	texts := []string{
		"dial tcp 10.0.0.5:5432: connection refused",
		"user alice@example.com not permitted",
	}

	for _, text := range texts {
		t.Run(text, func(t *testing.T) {
			t.Parallel()

			engine := newEngine(func(c *gin.Context) { _ = c.Error(errors.New(text)) })

			rec := serve(t, engine, get(t, "/x"))

			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}

			if body := rec.Body.String(); strings.Contains(body, text) {
				t.Errorf("body %q contains the error text %q", body, text)
			}
		})
	}
}

// gin-adapter / Scenario: Faulty consumer code under gin.
func TestMiddleware_FaultyConsumer(t *testing.T) {
	t.Parallel()

	errX := errors.New("x")
	mapX := prblm.Status(errX, http.StatusConflict)

	panickingMapper := prblm.MapperFunc(func(context.Context, error) *prblm.Problem { panic("mapper") })
	nilMapper := prblm.MapperFunc(func(context.Context, error) *prblm.Problem { return nil })

	tests := []struct {
		name string
		opts []prblm.Option
	}{
		{name: "mapper panics", opts: []prblm.Option{prblm.WithMappers(panickingMapper, mapX)}},
		{name: "mapper returns nil", opts: []prblm.Option{prblm.WithMappers(nilMapper, mapX)}},
		{
			name: "enricher panics",
			opts: []prblm.Option{
				prblm.WithEnrichers(func(context.Context, *prblm.Problem) { panic("enricher") }),
				prblm.WithMappers(mapX),
			},
		},
		{
			name: "hook panics",
			opts: []prblm.Option{
				prblm.OnError(func(context.Context, error, *prblm.Problem) { panic("hook") }),
				prblm.WithMappers(mapX),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			engine := newEngine(func(c *gin.Context) { _ = c.Error(errX) }, tt.opts...)

			// The second request shows the engine keeps serving.
			for i := range 2 {
				if rec := serve(t, engine, get(t, "/x")); rec.Code != http.StatusConflict {
					t.Errorf("request %d: status = %d, want 409", i+1, rec.Code)
				}
			}
		})
	}
}

// panickyUnwrapError is an error whose Unwrap panics. The core's bounded
// walk contains the panic and answers the safe 500 problem.
type panickyUnwrapError struct{}

func (panickyUnwrapError) Error() string { return "panicky" }
func (panickyUnwrapError) Unwrap() error { panic("unwrap") }

// An error whose Unwrap panics gets the safe 500 problem without the panic
// value, and the engine keeps serving.
func TestMiddleware_FaultyConsumer_UnwrapPanics(t *testing.T) {
	t.Parallel()

	engine := gin.New()
	engine.Use(ginprblm.Middleware())
	engine.GET("/x", func(c *gin.Context) {
		record(c, panickyUnwrapError{})
	})
	engine.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	rec := serve(t, engine, get(t, "/x"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}

	if body := rec.Body.String(); body != `{"title":"Internal Server Error","status":500,"instance":"/x"}` {
		t.Errorf("body = %q, want the safe 500 problem", body)
	}

	if rec := serve(t, engine, get(t, "/ok")); rec.Code != http.StatusOK {
		t.Errorf("follow-up status = %d, want 200", rec.Code)
	}
}

// error-chain-walk / Scenario: Cycles and fan-out (gin rows). Within a
// second, the client receives the safe 500 problem, and the hook receives an
// ErrUnwalkableError note with reason budget, then the error.
func TestMiddleware_Unwalkable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "unwrap returns itself", err: &loopError{}},
		{name: "join holds itself", err: joinCycle(t)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			engine := gin.New()
			engine.Use(ginprblm.Middleware(hook.option()))
			engine.GET("/v1/vessels/7", func(c *gin.Context) { record(c, tt.err) })

			rec := serveWithin(t, engine, get(t, "/v1/vessels/7"))

			assertSafe500(t, rec, true)
			assertNoted(t, &hook, "budget", nil, isSame(tt.err))
		})
	}
}

// Slice 2 end to end: a recorded error becomes a problem over a real server,
// with negotiation and the query string left out of the instance.
func TestMiddleware_EndToEnd(t *testing.T) {
	t.Parallel()

	engine := gin.New()
	engine.Use(ginprblm.Middleware(prblm.WithMappers(
		prblm.Status(ErrVesselNotFound, http.StatusNotFound),
	)))
	engine.GET("/v1/vessels/:id", func(c *gin.Context) { _ = c.Error(ErrVesselNotFound) })

	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)

	tests := []struct {
		name        string
		accept      string
		contentType string
	}{
		{name: "json by default", accept: "", contentType: "application/problem+json"},
		{name: "xml when preferred", accept: "application/xml", contentType: "application/problem+xml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := get(t, srv.URL+"/v1/vessels/7?token=abc")
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}

			rec := do(t, srv, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}

			if ct := rec.Header().Get("Content-Type"); ct != tt.contentType {
				t.Errorf("Content-Type = %q, want %q", ct, tt.contentType)
			}

			if vary := rec.Header().Get("Vary"); vary != "Accept" {
				t.Errorf("Vary = %q, want Accept", vary)
			}

			if p := decodeRecorded(t, rec); p.Instance != "/v1/vessels/7" {
				t.Errorf("instance = %q, want /v1/vessels/7", p.Instance)
			}
		})
	}
}

// Murphy row: a recorded error holding a nil error has nothing to answer, so
// the library writes nothing and the handler's response stands.
func TestMiddleware_NilRecordedError(t *testing.T) {
	t.Parallel()

	engine := gin.New()
	engine.Use(ginprblm.Middleware())
	engine.GET("/x", func(c *gin.Context) {
		c.Status(http.StatusAccepted)
		c.Errors = append(c.Errors, &gin.Error{Err: nil, Type: gin.ErrorTypePrivate})
	})

	rec := serve(t, engine, get(t, "/x"))

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want the recorded 202", rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "" {
		t.Errorf("Content-Type = %q, want none", ct)
	}
}

// error-chain-walk / Scenario: Typed-nil adapter hint wrapper. A recorded
// error that is a nil of the hint wrapper's type (a consumer can make one
// with reflect.Zero from the error its hook receives) gets the core's safe
// 500 and one hook call, never a bare status. It is appended directly: gin's
// own c.Error panics on it first, in its errors.As.
func TestMiddleware_TypedNilStatusHint(t *testing.T) {
	t.Parallel()

	hook := &hookRecorder{}
	engine := newEngine(func(c *gin.Context) { record(c, (*statushint.Error)(nil)) }, hook.option())

	rec := serve(t, engine, get(t, "/x"))

	if ct := rec.Header().Get("Content-Type"); rec.Code != http.StatusInternalServerError || ct != "application/problem+json" {
		t.Errorf("response = %d %q %q, want 500 application/problem+json", rec.Code, ct, rec.Body.String())
	}

	if got := len(hook.received()); got != 1 {
		t.Errorf("hook calls = %d, want 1", got)
	}
}

// Design D3 failure mode: a panic unwinding through the middleware must
// leave gin's own writer in place, so recovery middleware outside it can
// still send its status.
func TestMiddleware_PanicPassesThrough(t *testing.T) {
	t.Parallel()

	engine := gin.New()
	engine.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		c.AbortWithStatus(http.StatusInternalServerError)
	}))
	engine.Use(ginprblm.Middleware())
	engine.GET("/x", func(*gin.Context) { panic("boom") })

	if rec := serve(t, engine, get(t, "/x")); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 from the outer recovery", rec.Code)
	}
}

// gin-adapter / Scenario: Concurrent gin requests.
func TestMiddleware_Concurrent(t *testing.T) {
	t.Parallel()

	const requests = 200

	engine := gin.New()
	engine.Use(ginprblm.Middleware(prblm.WithMappers(
		prblm.Status(ErrVesselNotFound, http.StatusNotFound),
	)))
	engine.GET("/a", func(c *gin.Context) { _ = c.Error(ErrVesselNotFound) })
	engine.GET("/b", func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) })

	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)

	want := map[string]int{"/a": http.StatusNotFound, "/b": http.StatusUnauthorized}

	var wg sync.WaitGroup

	for i := range requests {
		path := "/a"
		if i%2 == 1 {
			path = "/b"
		}

		// Each request reports with t.Errorf only: t.Fatal must not be
		// called outside the test goroutine.
		wg.Go(func() {
			req := get(t, srv.URL+path)
			req.RequestURI = ""

			res, err := srv.Client().Do(req) //nolint:bodyclose // readProblem closes the body.
			if err != nil {
				t.Errorf("%s: request: %v", path, err)

				return
			}

			if res.StatusCode != want[path] {
				t.Errorf("%s: status = %d, want %d", path, res.StatusCode, want[path])
			}

			p, err := readProblem(res)
			if err != nil {
				t.Errorf("%s: decode problem: %v", path, err)

				return
			}

			if p.Status != want[path] || p.Instance != path {
				t.Errorf("%s: problem status %d instance %q, want %d %q", path, p.Status, p.Instance, want[path], path)
			}
		})
	}

	wg.Wait()
}

// problem-types / Scenario: Hostile host headers, through gin: the type
// comes from the namespace only, never from the request's host.
func TestMiddleware_HostHeaders(t *testing.T) {
	t.Parallel()

	errBlacklisted := errors.New("blacklisted")
	blacklisted := prblm.WithMappers(prblm.Is(errBlacklisted, func(error) *prblm.Problem {
		return prblm.New(http.StatusConflict, prblm.WithType("contracts/blacklisted-supplier"))
	}))

	tests := []struct {
		name      string
		namespace string
		header    http.Header
		rendered  string
	}{
		{
			name: "host on a path namespace", namespace: "/v1/api/probs",
			header: http.Header{"Host": {"evil.example"}}, rendered: "/v1/api/probs/contracts/blacklisted-supplier",
		},
		{
			name: "forwarded headers on a path namespace", namespace: "/v1/api/probs",
			header:   http.Header{"X-Forwarded-Host": {"evil.example"}, "X-Forwarded-Proto": {"http"}},
			rendered: "/v1/api/probs/contracts/blacklisted-supplier",
		},
		{
			name: "host on a uri namespace", namespace: "https://kartala.id/probs",
			header: http.Header{"Host": {"evil.example"}}, rendered: "https://kartala.id/probs/contracts/blacklisted-supplier",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			engine := newEngine(func(c *gin.Context) { _ = c.Error(errBlacklisted) }, prblm.WithNamespace(tt.namespace), blacklisted)

			req := get(t, "/x")
			maps.Copy(req.Header, tt.header)

			if host := tt.header.Get("Host"); host != "" {
				req.Host = host
			}

			if got := decodeRecorded(t, serve(t, engine, req)).Type; got != tt.rendered {
				t.Errorf("type = %q, want %q", got, tt.rendered)
			}
		})
	}
}

// problem-response / Scenarios: Replaced headers win over existing ones
// (gin rows), plus Vary, the response that falls back and the deleted marked
// header; A handler header under a non-canonical key.
func TestMiddleware_ReplacedHeader(t *testing.T) {
	t.Parallel()

	errUnavailable := errors.New("unavailable")

	tests := []struct {
		name   string
		header string
		// key, when set, is the spelling the handler writes the header map
		// under directly, which net/http sends as written.
		key      string
		existing string
		fields   []prblm.Field
		expected []string
		// deleted makes an enricher delete header from the problem.
		deleted bool
	}{
		{
			name:     "marked cache-control replaces a lowercase handler key",
			header:   "Cache-Control",
			key:      "cache-control",
			existing: "public, max-age=600",
			fields:   []prblm.Field{prblm.WithReplacedHeader("Cache-Control", "no-store")},
			expected: []string{"no-store"},
		},
		{
			name:     "ordinary cache-control keeps a lowercase handler key",
			header:   "Cache-Control",
			key:      "cache-control",
			existing: "public, max-age=600",
			fields:   []prblm.Field{prblm.WithHeader("Cache-Control", "no-store")},
			expected: []string{"public, max-age=600"},
		},
		{
			name:     "vary under a lowercase handler key gains accept",
			header:   "Vary",
			key:      "vary",
			existing: "Origin",
			expected: []string{"Origin", "Accept"},
		},
		{
			name:     "marked cache-control replaces the handler's",
			header:   "Cache-Control",
			existing: "public, max-age=600",
			fields:   []prblm.Field{prblm.WithReplacedHeader("Cache-Control", "no-store")},
			expected: []string{"no-store"},
		},
		{
			name:     "ordinary cache-control keeps the handler's",
			header:   "Cache-Control",
			existing: "public, max-age=600",
			fields:   []prblm.Field{prblm.WithHeader("Cache-Control", "no-store")},
			expected: []string{"public, max-age=600"},
		},
		{
			name:     "marked vary still lists accept",
			header:   "Vary",
			existing: "Origin",
			fields:   []prblm.Field{prblm.WithReplacedHeader("Vary", "Cookie")},
			expected: []string{"Cookie", "Accept"},
		},
		{
			name:     "fallback carries no marks",
			header:   "Cache-Control",
			existing: "public, max-age=600",
			fields: []prblm.Field{
				prblm.WithReplacedHeader("Cache-Control", "no-store"),
				prblm.WithExt("bad", make(chan int)),
			},
			expected: []string{"public, max-age=600"},
		},
		// A mark whose header was deleted from the problem replaces
		// nothing: the library's defaults merge as usual.
		{
			name:     "deleted marked cache-control keeps the handler's",
			header:   "Cache-Control",
			existing: "public, max-age=60",
			fields:   []prblm.Field{prblm.WithReplacedHeader("Cache-Control", "x")},
			expected: []string{"public, max-age=60"},
			deleted:  true,
		},
		{
			name:     "deleted marked vary adds accept to the handler's",
			header:   "Vary",
			existing: "Origin",
			fields:   []prblm.Field{prblm.WithReplacedHeader("Vary", "x")},
			expected: []string{"Origin", "Accept"},
			deleted:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			engine := newEngine(func(c *gin.Context) {
				if tt.key != "" {
					c.Writer.Header()[tt.key] = []string{tt.existing}
				} else {
					c.Header(tt.header, tt.existing)
				}

				_ = c.Error(errUnavailable)
			}, prblm.WithMappers(prblm.Is(errUnavailable, func(error) *prblm.Problem {
				return prblm.New(http.StatusServiceUnavailable, tt.fields...)
			})), prblm.WithEnrichers(func(_ context.Context, p *prblm.Problem) {
				if tt.deleted {
					p.Header.Del(tt.header)
				}
			}))

			if got := sentValues(serve(t, engine, get(t, "/x")).Header(), tt.header); !slices.Equal(got, tt.expected) {
				t.Errorf("%s = %q, want %q", tt.header, got, tt.expected)
			}
		})
	}
}

// sentValues returns the values net/http sends for the header name of h:
// those of every key that spells name in any case, a direct map write
// included, in the order of the keys on the wire (sorted).
func sentValues(h http.Header, name string) []string {
	var values []string

	for _, key := range slices.Sorted(maps.Keys(h)) {
		if strings.EqualFold(key, name) {
			values = append(values, h[key]...)
		}
	}

	return values
}

// routeOK answers 200: a route that, if it ran, would give the hooks nothing.
func routeOK(c *gin.Context) { c.Status(http.StatusOK) }

// gin-adapter / Scenario: Classifying a status-only abort in a hook.
func TestHook_StatusOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		handler gin.HandlerFunc
		name    string
		// method and path default to GET /x, the route the handler serves.
		method           string
		path             string
		calls            int
		statusOnly       bool
		status           int
		methodNotAllowed bool
	}{
		// status-only
		{name: "abort 401", handler: func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }, calls: 1, statusOnly: true, status: http.StatusUnauthorized},
		{name: "abort 429", handler: func(c *gin.Context) { c.AbortWithStatus(http.StatusTooManyRequests) }, calls: 1, statusOnly: true, status: http.StatusTooManyRequests},
		{name: "status 500 without body", handler: func(c *gin.Context) { c.Status(http.StatusInternalServerError) }, calls: 1, statusOnly: true, status: http.StatusInternalServerError},
		// gin's own aborts: no handler runs, yet gin records a bare status
		{name: "unrouted GET", handler: routeOK, path: "/nope", calls: 1, statusOnly: true, status: http.StatusNotFound},
		{
			name:    "wrong method with HandleMethodNotAllowed",
			handler: routeOK, method: http.MethodPut, methodNotAllowed: true,
			calls: 1, statusOnly: true, status: http.StatusMethodNotAllowed,
		},
		// handler errors
		{
			name:    "abort with error 401",
			handler: func(c *gin.Context) { _ = c.AbortWithError(http.StatusUnauthorized, errors.New("token expired")) },
			calls:   1, status: http.StatusUnauthorized,
		},
		{name: "recorded error", handler: func(c *gin.Context) { _ = c.Error(errors.New("boom")) }, calls: 1, status: http.StatusInternalServerError},
		{
			name:    "recorded sentinel",
			handler: func(c *gin.Context) { _ = c.Error(fmt.Errorf("vessel 7: %w", prblm.ErrNotFound)) },
			calls:   1, status: http.StatusNotFound,
		},
		{
			name: "error after body",
			handler: func(c *gin.Context) {
				c.String(http.StatusOK, "partial")
				_ = c.Error(errors.New("boom"))
			},
			calls: 1, status: http.StatusInternalServerError,
		},
		// no problem written
		{name: "abort 200", handler: func(c *gin.Context) { c.AbortWithStatus(http.StatusOK) }},
		{name: "status 204", handler: func(c *gin.Context) { c.Status(http.StatusNoContent) }},
		{name: "string 500", handler: func(c *gin.Context) { c.String(http.StatusInternalServerError, "plain") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			engine := newEngine(tt.handler, hook.option())
			engine.HandleMethodNotAllowed = tt.methodNotAllowed

			method, path := cmp.Or(tt.method, http.MethodGet), cmp.Or(tt.path, "/x")
			srv := httptest.NewServer(engine)
			t.Cleanup(srv.Close)
			do(t, srv, httptest.NewRequestWithContext(t.Context(), method, srv.URL+path, http.NoBody))

			errs, statuses := hook.received(), hook.problemStatuses()
			if len(errs) != tt.calls {
				t.Fatalf("hook called %d times with %v, want %d", len(errs), errs, tt.calls)
			}

			if tt.calls == 0 {
				return
			}

			if got := errors.Is(errs[0], ginprblm.ErrStatusOnly); got != tt.statusOnly {
				t.Errorf("errors.Is(%v, ErrStatusOnly) = %t, want %t", errs[0], got, tt.statusOnly)
			}

			if statuses[0] != tt.status {
				t.Errorf("problem status = %d, want %d", statuses[0], tt.status)
			}
		})
	}
}

// valuePanicCtx is a request context whose Value panics with v.
type valuePanicCtx struct {
	context.Context //nolint:containedctx // a hostile context wrapping the request one

	v any
}

func (c valuePanicCtx) Value(any) any { panic(c.v) }

// gin-adapter / Scenario: Respond panics. Respond calls the request
// context's Value outside its containment, so a context whose Value panics
// makes it panic: Middleware answers the bare 500, for panic(nil) under
// GODEBUG=panicnil=1 too, rather than committing the recorded 200.
func TestMiddleware_RespondPanics(t *testing.T) {
	t.Parallel()

	if rerunUnderPanicNil(t) {
		return
	}

	for _, v := range []any{"boom", nilPanicValue} {
		engine := gin.New()
		engine.Use(ginprblm.Middleware())
		engine.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(valuePanicCtx{Context: c.Request.Context(), v: v})
		})
		engine.GET("/v1/vessels/7", func(c *gin.Context) { _ = c.Error(errors.New("db down")) })

		rec := serve(t, engine, get(t, "/v1/vessels/7"))

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("Respond panicking with %v: status = %d, want the bare 500", v, rec.Code)
		}
	}
}
