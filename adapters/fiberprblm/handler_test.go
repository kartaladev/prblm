package fiberprblm_test

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/adapters/fiberprblm"
	"github.com/kartaladev/prblm/internal/statushint"
)

// fiber-adapter / Scenario: Options configure the error handler.
func TestErrorHandler_Options(t *testing.T) {
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

			app := newApp(tt.opts...)
			app.Get("/v1/vessels/:id", func(fiber.Ctx) error { return ErrVesselNotFound })

			res := test(t, app, get(t, http.MethodGet, "/v1/vessels/7"))

			if res.status != tt.status {
				t.Errorf("status = %d, want %d", res.status, tt.status)
			}

			if ct := res.header.Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}
		})
	}
}

// fiber-adapter / Scenario: Invalid options fail at construction.
func TestErrorHandler_InvalidOptions(t *testing.T) {
	t.Parallel()

	errorHandler := func(opts ...prblm.Option) { fiberprblm.ErrorHandler(opts...) }

	tests := []struct {
		name      string
		construct func(opts ...prblm.Option)
		opts      []prblm.Option
		named     string
	}{
		{name: "relative namespace", construct: errorHandler, opts: []prblm.Option{prblm.WithNamespace("v1/api/probs")}, named: "WithNamespace"},
		{name: "types and no namespace", construct: errorHandler, opts: []prblm.Option{prblm.WithTypes(OutOfCredit)}, named: "WithTypes"},
		{
			name:      "relative namespace in the discovery middleware",
			construct: func(opts ...prblm.Option) { fiberprblm.Discovery(opts...) },
			opts:      []prblm.Option{prblm.WithNamespace("v1/api/probs")},
			named:     "WithNamespace",
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

			tt.construct(tt.opts...)
		})
	}
}

// fiber-adapter / Scenario: Returned errors.
func TestErrorHandler_Returned(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "sentinel", err: ErrVesselNotFound, status: http.StatusNotFound},
		{name: "wrapped sentinel", err: fmt.Errorf("load: %w", ErrVesselNotFound), status: http.StatusNotFound},
		{name: "unmapped", err: errors.New("pq: password=hunter2"), status: http.StatusInternalServerError},
		{name: "problem", err: prblm.New(http.StatusConflict), status: http.StatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := newApp(prblm.WithMappers(prblm.Status(ErrVesselNotFound, http.StatusNotFound)))
			app.Get("/v1/vessels/:id", func(fiber.Ctx) error { return tt.err })

			res := test(t, app, get(t, http.MethodGet, "/v1/vessels/7"))

			if res.status != tt.status {
				t.Errorf("status = %d, want %d", res.status, tt.status)
			}

			if ct := res.header.Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}

			if p := problemOf(t, res); p.Instance != "/v1/vessels/7" {
				t.Errorf("instance = %q, want /v1/vessels/7", p.Instance)
			}
		})
	}
}

// fiber-adapter / Scenario: Unmapped error text never leaks.
func TestErrorHandler_Returned_NoLeak(t *testing.T) {
	t.Parallel()

	texts := []string{
		"dial tcp 10.0.0.5:5432: connection refused",
		"user alice@example.com not permitted",
	}

	for _, text := range texts {
		t.Run(text, func(t *testing.T) {
			t.Parallel()

			res := serveX(t, func(fiber.Ctx) error { return errors.New(text) })

			if res.status != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", res.status)
			}

			if strings.Contains(res.body, text) {
				t.Errorf("body %q contains the error text %q", res.body, text)
			}
		})
	}
}

// fiber-adapter / Scenario: Fiber-generated and handler fiber errors.
func TestErrorHandler_FiberErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		method, path string
		status       int
		title        string
		hidden       string // must not appear in the body
	}{
		// routing
		{name: "unknown route", method: http.MethodGet, path: "/v1/unknown", status: http.StatusNotFound, title: "Not Found", hidden: "Cannot GET"},
		{name: "method not allowed", method: http.MethodDelete, path: "/v1/vessels/7", status: http.StatusMethodNotAllowed, title: "Method Not Allowed"},
		// handler-returned
		{name: "fiber error with message", method: http.MethodGet, path: "/v1/vessels/7?e=forbidden", status: http.StatusForbidden, title: "Forbidden", hidden: "token abc"},
		{name: "fiber sentinel", method: http.MethodGet, path: "/v1/vessels/7?e=bad", status: http.StatusBadRequest, title: "Bad Request"},
		{name: "fiber error with a success code", method: http.MethodGet, path: "/v1/vessels/7?e=ok", status: http.StatusInternalServerError, title: "Internal Server Error"},
		// The walk skips a typed nil, so the fiber error after it is found.
		{name: "typed-nil fiber error before a fiber error", method: http.MethodGet, path: "/v1/vessels/7?e=typed-nil", status: http.StatusConflict, title: "Conflict"},
	}

	returned := map[string]error{
		"forbidden": fiber.NewError(http.StatusForbidden, "token abc expired"),
		"bad":       fiber.ErrBadRequest,
		"ok":        fiber.NewError(http.StatusOK, "ok"),
		"typed-nil": errors.Join((*fiber.Error)(nil), fiber.ErrConflict),
	}

	app := newApp()
	app.Get("/v1/vessels/:id", func(c fiber.Ctx) error { return returned[c.Query("e")] })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res := test(t, app, get(t, tt.method, tt.path))

			if res.status != tt.status {
				t.Errorf("status = %d, want %d", res.status, tt.status)
			}

			p := problemOf(t, res)

			if p.Title != tt.title {
				t.Errorf("title = %q, want %q", p.Title, tt.title)
			}

			// A fiber error's message never becomes the detail.
			if p.Detail != "" {
				t.Errorf("detail = %q, want none", p.Detail)
			}

			if tt.hidden != "" && strings.Contains(res.body, tt.hidden) {
				t.Errorf("body %q contains %q", res.body, tt.hidden)
			}
		})
	}
}

// fiber-adapter / Scenario: Mapper takes precedence over a fiber error code.
func TestErrorHandler_FiberErrors_MapperWins(t *testing.T) {
	t.Parallel()

	handler := func(fiber.Ctx) error { return fmt.Errorf("%w: %w", fiber.ErrBadRequest, ErrLocked) }
	mapper := prblm.Status(ErrLocked, http.StatusLocked)

	if res := serveX(t, handler, prblm.WithMappers(mapper)); res.status != http.StatusLocked {
		t.Errorf("status = %d, want 423", res.status)
	}
}

// The code of a fiber error is a hint around the whole error, so it decides
// even over a sentinel outside the fiber error (error-sentinels / Requirement: The
// outer of a sentinel and a status hint decides).
func TestErrorHandler_FiberErrors_OverSentinel(t *testing.T) {
	t.Parallel()

	handler := func(fiber.Ctx) error {
		return fmt.Errorf("%w: %w", prblm.ErrBadGateway, fiber.NewError(http.StatusGone, "vessel 7 scrapped"))
	}

	if res := serveX(t, handler); res.status != http.StatusGone {
		t.Errorf("status = %d, want 410", res.status)
	}
}

// Murphy row: a typed-nil *fiber.Error is a non-nil error with no code; it
// is answered like any unmapped error.
func TestErrorHandler_FiberErrors_TypedNil(t *testing.T) {
	t.Parallel()

	app := newApp()
	app.Get("/x", func(fiber.Ctx) error {
		var fe *fiber.Error

		return fe
	})

	res := test(t, app, get(t, http.MethodGet, "/x"))

	if res.status != http.StatusInternalServerError || res.header.Get("Content-Type") != "application/problem+json" {
		t.Errorf("response %d %q, want a 500 problem", res.status, res.header.Get("Content-Type"))
	}
}

// error-chain-walk / Scenario: Typed-nil adapter hint wrapper. A handler
// that returns a nil of the hint wrapper's type (a consumer can make one with
// reflect.Zero from the error its hook receives) gets the core's safe 500
// and one hook call, never a bare status.
func TestErrorHandler_TypedNilStatusHint(t *testing.T) {
	t.Parallel()

	var typedNil error = (*statushint.Error)(nil)

	hook := &hookRecorder{}
	app := newApp(hook.option())
	app.Get("/v1/vessels/7", func(fiber.Ctx) error { return typedNil })

	assertSafe500(t, test(t, app, get(t, http.MethodGet, "/v1/vessels/7")), true)
	assertHookOnce(t, hook, isSame(typedNil))
}

// fiber-adapter / Scenario: Request facts.
func TestErrorHandler_RequestFacts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		target      string
		accept      string
		contentType string
	}{
		{name: "query left out", target: "/v1/vessels/7?token=abc", contentType: "application/problem+json"},
		{name: "xml preferred", target: "/v1/vessels/7", accept: "application/xml", contentType: "application/problem+xml"},
		{name: "unsupported type", target: "/v1/vessels/7", accept: "image/png", contentType: "application/problem+json"},
	}

	app := newApp()
	app.Get("/v1/vessels/:id", func(fiber.Ctx) error {
		return prblm.WithStatus(errors.New("x"), http.StatusNotFound)
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := get(t, http.MethodGet, tt.target)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}

			res := test(t, app, req)

			if ct := res.header.Get("Content-Type"); ct != tt.contentType {
				t.Errorf("Content-Type = %q, want %q", ct, tt.contentType)
			}

			if p := problemOf(t, res); p.Instance != "/v1/vessels/7" || p.Status != http.StatusNotFound {
				t.Errorf("problem %d %q, want 404 /v1/vessels/7", p.Status, p.Instance)
			}

			if strings.Contains(res.body, "token") {
				t.Errorf("body %q leaks the query string", res.body)
			}
		})
	}
}

// fiber-adapter / Scenario: Handler wrote before returning an error.
func TestErrorHandler_Buffered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		does      func(c fiber.Ctx)
		requestID string // expected X-Request-Id on the problem
		isStream  bool
	}{
		{
			name: "buffered json replaced",
			does: func(c fiber.Ctx) { _ = c.Status(http.StatusCreated).JSON(fiber.Map{"id": 7}) },
		},
		{
			name:      "header kept",
			does:      func(c fiber.Ctx) { c.Set("X-Request-Id", "r1") },
			requestID: "r1",
		},
		{
			name: "stream kept",
			does: func(c fiber.Ctx) {
				_ = c.SendStreamWriter(func(w *bufio.Writer) {
					_, _ = w.WriteString("data: 1\n\n")
				})
			},
			isStream: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			app := newApp(hook.option(), prblm.WithMappers(prblm.Status(ErrVesselNotFound, http.StatusNotFound)))
			app.Get("/x", func(c fiber.Ctx) error {
				tt.does(c)

				return ErrVesselNotFound
			})

			res := test(t, app, get(t, http.MethodGet, "/x"))

			assertHook(t, &hook, ErrVesselNotFound)

			if tt.isStream {
				if res.body != "data: 1\n\n" {
					t.Errorf("body = %q, want the stream unchanged", res.body)
				}

				return
			}

			if res.status != http.StatusNotFound || res.header.Get("Content-Type") != "application/problem+json" {
				t.Errorf("response %d %q, want a 404 problem", res.status, res.header.Get("Content-Type"))
			}

			if p := problemOf(t, res); p.Status != http.StatusNotFound {
				t.Errorf("problem status = %d, want 404", p.Status)
			}

			if got := res.header.Get("X-Request-Id"); got != tt.requestID {
				t.Errorf("X-Request-Id = %q, want %q", got, tt.requestID)
			}
		})
	}
}

// fiber-adapter / Scenario: Client went away. An error whose chain cannot be
// walked is answered with the safe 500, as if the client were still there,
// and the hook receives a note, then the error.
func TestErrorHandler_ClientGone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		canceled  bool
		err       error
		isWritten bool
		// reason is the reason of the note the hook receives first; empty
		// when the hook only receives the error.
		reason    string
		noteValue any
	}{
		{name: "canceled request", canceled: true, err: fmt.Errorf("query: %w", context.Canceled), isWritten: false},
		{name: "active request", canceled: false, err: context.Canceled, isWritten: true},
		{name: "canceled, unwrap returns itself", canceled: true, err: &loopError{}, isWritten: true, reason: "budget"},
		{name: "canceled, is panics", canceled: true, err: isPanicError{}, isWritten: true, reason: "panic", noteValue: "boom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			app := newApp(hook.option())
			app.Use(func(c fiber.Ctx) error {
				ctx, cancel := context.WithCancel(c.Context())
				if tt.canceled {
					cancel()
				} else {
					// The error handler runs after this middleware returns,
					// so the context must outlive it.
					t.Cleanup(cancel)
				}

				c.SetContext(ctx)

				return c.Next()
			})
			app.Get("/v1/vessels/7", func(fiber.Ctx) error { return tt.err })

			res := testWithin(t, app, get(t, http.MethodGet, "/v1/vessels/7"))

			assertSafe500(t, res, tt.isWritten)

			if tt.reason == "" {
				assertHookOnce(t, &hook, isSame(tt.err))
			} else {
				assertNoted(t, &hook, tt.reason, tt.noteValue, isSame(tt.err))
			}
		})
	}
}

// fiber-adapter / Scenario: Errors that cannot be walked, and
// error-chain-walk / Scenario: Cycles and fan-out (fiber rows). Within a
// second, the client receives the safe 500 problem, and the hook receives an
// ErrUnwalkableError note, then the error.
//
// The rows with isRouteUnmatched are Murphy rows: discovery is on, and
// middleware returns the error for a path no route claims, so the error
// handler first asks whether it is fiber's route-not-found error. A chain
// that cannot be walked is not, so the client receives the safe 500
// problem, not the documentation's 404.
func TestErrorHandler_Unwalkable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		err              error
		isRouteUnmatched bool
		reason           string
		noteValue        any
	}{
		{name: "as panics", err: asPanicError{}, reason: "panic", noteValue: "boom"},
		{
			name:      "fiber error before an as that panics",
			err:       fmt.Errorf("%w: %w", fiber.ErrBadRequest, asPanicError{}),
			reason:    "panic",
			noteValue: "boom",
		},
		{name: "unwrap returns itself", err: &loopError{}, reason: "budget"},
		{name: "join holds itself", err: joinCycle(t), reason: "budget"},
		{name: "join holds a fiber error and itself", err: joinCycle(t, fiber.ErrBadRequest), reason: "budget"},
		{name: "unwrap panics", err: panickyUnwrapError{}, reason: "panic", noteValue: "unwrap"},
		// Murphy rows: no route matched, discovery on.
		{name: "unmatched, unwrap returns itself", err: &loopError{}, isRouteUnmatched: true, reason: "budget"},
		{
			name:             "unmatched, join holds not found and itself",
			err:              joinCycle(t, fiber.ErrNotFound),
			isRouteUnmatched: true,
			reason:           "budget",
		},
		{name: "unmatched, is panics", err: isPanicError{}, isRouteUnmatched: true, reason: "panic", noteValue: "boom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			returnErr := func(fiber.Ctx) error { return tt.err }

			var app *fiber.App
			if tt.isRouteUnmatched {
				app = newApp(hook.option(), prblm.WithNamespace("/v1/vessels"), prblm.WithTypes(OutOfCredit))
				app.Use(returnErr)
			} else {
				app = newApp(hook.option())
				app.Get("/v1/vessels/7", returnErr)
			}

			res := testWithin(t, app, get(t, http.MethodGet, "/v1/vessels/7"))

			assertSafe500(t, res, true)
			assertNoted(t, &hook, tt.reason, tt.noteValue, isSame(tt.err))
		})
	}
}

// fiber-adapter / Scenario: Errors that cannot be walked, Murphy row: the
// walk aborts with a panic value that cannot be formatted. The core builds
// the note without formatting it, so the answer is the full safe 500 and the
// hook calls, not fiber's bare 500. The note is not formatted here either.
func TestErrorHandler_Unwalkable_UnformattableValue(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	app := newApp(hook.option())
	app.Get("/v1/vessels/7", func(fiber.Ctx) error { return hostileIsError{} })

	res := testWithin(t, app, get(t, http.MethodGet, "/v1/vessels/7"))

	assertSafe500(t, res, true)

	got := hook.received()
	if len(got) != 2 || !errors.Is(got[0], prblm.ErrUnwalkableError) || !isSame(hostileIsError{})(got[1]) {
		t.Fatalf("hook received %d calls (%s), want the note, then the error", len(got), typesOf(got))
	}

	if pe, ok := errors.AsType[*prblm.PanicError](got[0]); !ok || pe.Value != unformattable {
		t.Errorf("the note does not wrap the unformattable panic value")
	}
}

// fiber-adapter / Scenario: Faulty consumer code under fiber.
func TestErrorHandler_Faulty(t *testing.T) {
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

			app := newApp(tt.opts...)
			app.Get("/x", func(fiber.Ctx) error { return errX })

			// The second request shows the app keeps serving.
			for i := range 2 {
				if res := test(t, app, get(t, http.MethodGet, "/x")); res.status != http.StatusConflict {
					t.Errorf("request %d: status = %d, want 409", i+1, res.status)
				}
			}
		})
	}
}

// fiber-adapter / Scenario: Concurrent fiber requests.
func TestErrorHandler_Concurrent(t *testing.T) {
	t.Parallel()

	const requests = 200

	app := newApp(prblm.WithMappers(prblm.Status(ErrVesselNotFound, http.StatusNotFound)))
	app.Get("/a", func(fiber.Ctx) error { return ErrVesselNotFound })
	app.Get("/b", func(fiber.Ctx) error { return fiber.ErrUnauthorized })

	client, base := listen(t, app)
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
			status, p, err := fetchProblem(t.Context(), client, base+path)
			if err != nil {
				t.Errorf("%s: %v", path, err)

				return
			}

			if status != want[path] || p.Status != want[path] || p.Instance != path {
				t.Errorf("%s: response %d, problem %d %q, want %d %q", path, status, p.Status, p.Instance, want[path], path)
			}
		})
	}

	wg.Wait()
}

// Murphy row: called directly with a nil error, the error handler has
// nothing to answer and writes nothing.
func TestErrorHandler_NilError(t *testing.T) {
	t.Parallel()

	handle := fiberprblm.ErrorHandler()

	app := fiber.New()
	app.Get("/x", func(c fiber.Ctx) error {
		c.Status(http.StatusAccepted)

		return handle(c, nil)
	})

	res := test(t, app, get(t, http.MethodGet, "/x"))

	if res.status != http.StatusAccepted || res.body != "" || strings.HasPrefix(res.header.Get("Content-Type"), "application/problem") {
		t.Errorf("response %d %q %q, want the handler's 202 untouched", res.status, res.header.Get("Content-Type"), res.body)
	}
}

// problem-types / Scenario: Hostile host headers, through fiber: the type
// comes from the namespace only, never from the request's host.
func TestErrorHandler_HostHeaders(t *testing.T) {
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

			app := newApp(prblm.WithNamespace(tt.namespace), blacklisted)
			app.Get("/x", func(fiber.Ctx) error { return errBlacklisted })

			req := get(t, http.MethodGet, "/x")
			maps.Copy(req.Header, tt.header)

			if host := tt.header.Get("Host"); host != "" {
				req.Host = host
			}

			if got := problemOf(t, test(t, app, req)).Type; got != tt.rendered {
				t.Errorf("type = %q, want %q", got, tt.rendered)
			}
		})
	}
}

// problem-response / Scenarios: Replaced headers win over existing ones
// (fiber rows), plus Vary, the response that falls back and the deleted
// marked header; A handler header under a non-canonical key. Every row runs
// with fiber's header normalizing on and off: with it off, fiber keeps the
// handler's spelling, and the merge must still treat it as the same header.
func TestErrorHandler_ReplacedHeader(t *testing.T) {
	t.Parallel()

	errUnavailable := errors.New("unavailable")

	tests := []struct {
		name   string
		header string
		// key is the spelling the handler sets the header under, header
		// when empty.
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
			name:     "marked link replaces an upper-case handler key",
			header:   "Link",
			key:      "LINK",
			existing: "</old>; rel=x",
			fields:   []prblm.Field{prblm.WithReplacedHeader("Link", "</new>; rel=x")},
			expected: []string{"</new>; rel=x"},
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
		for _, raw := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/raw=%t", tt.name, raw), func(t *testing.T) {
				t.Parallel()

				app := fiber.New(fiber.Config{
					DisableHeaderNormalizing: raw,
					ErrorHandler: fiberprblm.ErrorHandler(prblm.WithMappers(prblm.Is(errUnavailable, func(error) *prblm.Problem {
						return prblm.New(http.StatusServiceUnavailable, tt.fields...)
					})), prblm.WithEnrichers(func(_ context.Context, p *prblm.Problem) {
						if tt.deleted {
							p.Header.Del(tt.header)
						}
					})),
				})
				app.Get("/x", func(c fiber.Ctx) error {
					c.Set(cmp.Or(tt.key, tt.header), tt.existing)

					return errUnavailable
				})

				if got := test(t, app, get(t, http.MethodGet, "/x")).header.Values(tt.header); !slices.Equal(got, tt.expected) {
					t.Errorf("%s = %q, want %q", tt.header, got, tt.expected)
				}
			})
		}
	}
}

// wrapN returns err wrapped n times with %w.
func wrapN(err error, n int) error {
	for range n {
		err = fmt.Errorf("w: %w", err)
	}

	return err
}

// error-chain-walk / Scenario: Budget boundary, the adapter rows. The status
// hint the adapter takes from a *fiber.Error is not an entry of the error's
// chain, so it costs nothing of the budget: a 1,000-entry error holding the
// *fiber.Error at its end gets its code, and a 1,001-entry error still
// aborts.
func TestErrorHandler_FiberError_Budget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries int
		status  int
	}{
		{name: "1,000 entries", entries: 1000, status: http.StatusConflict},
		{name: "1,001 entries", entries: 1001, status: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			err := wrapN(fiber.NewError(http.StatusConflict, "conflict"), tt.entries-1)
			app := newApp(hook.option())
			app.Get("/v1/vessels/7", func(fiber.Ctx) error { return err })

			res := testWithin(t, app, get(t, http.MethodGet, "/v1/vessels/7"))

			if tt.status == http.StatusInternalServerError {
				// The adapter's own walk aborted too, so err got no hint.
				assertSafe500(t, res, true)
				assertNoted(t, &hook, "budget", nil, isSame(err))

				return
			}

			if res.status != tt.status {
				t.Errorf("status = %d, want %d", res.status, tt.status)
			}

			assertHookOnce(t, &hook, isSame(err))
		})
	}
}

// fiberTargetPanicError wraps err, and its As method panics only when
// asked for a *fiber.Error, so only the adapter's hint walk aborts.
type fiberTargetPanicError struct{ err error }

func (fiberTargetPanicError) Error() string { return "hostile to the hint walk" }

func (e fiberTargetPanicError) Unwrap() error { return e.err }

func (fiberTargetPanicError) As(target any) bool {
	if _, ok := target.(**fiber.Error); ok {
		panic("boom")
	}

	return false
}

// notFoundTargetPanicError wraps err, and its Is method panics only when
// asked for fiber.ErrNotFound, so only the adapter's route-not-found walk
// aborts.
type notFoundTargetPanicError struct{ err error }

func (notFoundTargetPanicError) Error() string { return "hostile to the route-not-found walk" }

func (e notFoundTargetPanicError) Unwrap() error { return e.err }

func (notFoundTargetPanicError) Is(target error) bool {
	if target == fiber.ErrNotFound {
		panic("boom")
	}

	return false
}

// canceledTargetPanicError wraps err, and its Is method panics only when asked
// for context.Canceled, so only the adapter's client-gone walk aborts.
type canceledTargetPanicError struct{ err error }

func (canceledTargetPanicError) Error() string { return "hostile to the client-gone walk" }

func (e canceledTargetPanicError) Unwrap() error { return e.err }

func (canceledTargetPanicError) Is(target error) bool {
	if target == context.Canceled {
		panic("boom")
	}

	return false
}

// fiber-adapter / Scenarios: Errors whose chain only the adapter cannot walk
// (Fiber errors are status hints without their message; Client went away;
// Route-not-found check cannot finish).
// The adapter's own walk aborts on a panic for its target alone, so the
// error gets no hint, or counts as from a client still there, and the core,
// whose walk finishes, answers it as usual: a 404 for the ErrNotFound inside
// it, and the hook receives the error alone, with no note. On an unmatched
// route with discovery on, the route-not-found walk aborts the same way: the
// error is not route-not-found, so no documentation is served, and the core
// answers it with the 404 of the fiber.ErrNotFound inside it.
func TestErrorHandler_AdapterWalkAborts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		canceled bool
		// unrouted sends the request to the discovery documentation path,
		// which has no route, with discovery on; a middleware returns err
		// in place of fiber's own route-not-found error.
		unrouted bool
	}{
		{name: "hint walk aborts", err: fiberTargetPanicError{err: prblm.ErrNotFound}},
		{name: "client-gone walk aborts", err: canceledTargetPanicError{err: prblm.ErrNotFound}, canceled: true},
		{name: "route-not-found walk aborts", err: notFoundTargetPanicError{err: fiber.ErrNotFound}, unrouted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			path := "/v1/vessels/7"
			app := newApp(hook.option())

			if tt.unrouted {
				path = "/v1/api/probs/out-of-credit"
				app = discoveryApp(hook.option())
			}

			app.Use(func(c fiber.Ctx) error {
				ctx, cancel := context.WithCancel(c.Context())
				if tt.canceled {
					cancel()
				} else {
					// The error handler runs after this middleware returns,
					// so the context must outlive it.
					t.Cleanup(cancel)
				}

				c.SetContext(ctx)

				if err := c.Next(); !tt.unrouted {
					return err
				}

				return tt.err
			})
			app.Get("/v1/vessels/7", func(fiber.Ctx) error { return tt.err })

			res := testWithin(t, app, get(t, http.MethodGet, path))

			if ct := res.header.Get("Content-Type"); res.status != http.StatusNotFound || ct != "application/problem+json" {
				t.Errorf("response = %d %q, want a 404 application/problem+json problem", res.status, ct)
			}

			assertHookOnce(t, &hook, isSame(tt.err))
		})
	}
}

// valuePanicCtx is a request context whose Value panics with v.
type valuePanicCtx struct {
	context.Context //nolint:containedctx // a hostile context wrapping the request one

	v any
}

func (c valuePanicCtx) Value(any) any { panic(c.v) }

// fiber-adapter / Scenario: Respond panics. Respond calls the request
// context's Value outside its containment, so a context whose Value panics
// makes it panic: ErrorHandler answers the bare 500, for panic(nil) under
// GODEBUG=panicnil=1 too, rather than leaving fiber's empty 200.
func TestErrorHandler_RespondPanics(t *testing.T) {
	t.Parallel()

	if rerunUnderPanicNil(t) {
		return
	}

	for _, v := range []any{"boom", nilPanicValue} {
		app := newApp()
		app.Get("/v1/vessels/7", func(c fiber.Ctx) error {
			c.SetContext(valuePanicCtx{Context: c.Context(), v: v})

			return errors.New("db down")
		})

		r := test(t, app, get(t, http.MethodGet, "/v1/vessels/7"))

		if r.status != http.StatusInternalServerError {
			t.Errorf("Respond panicking with %v: status = %d, want the bare 500", v, r.status)
		}
	}
}
