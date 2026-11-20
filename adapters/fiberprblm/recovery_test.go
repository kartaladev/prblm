package fiberprblm_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/adapters/fiberprblm"
)

// loadVessel returns a handler that panics with v, named so the stack can
// be checked.
func loadVessel(v any) fiber.Handler {
	return func(fiber.Ctx) error { panic(v) }
}

// fiber-adapter / Scenario: Recovery with the adapter's error handler, rows
// with the recovery middleware.
func TestRecovery_ErrorHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		value       any
		accept      string
		contentType string
	}{
		{name: "string value", value: "boom", contentType: "application/problem+json"},
		{name: "error value as xml", value: io.ErrClosedPipe, accept: "application/xml", contentType: "application/problem+xml"},
		{name: "abort handler value", value: http.ErrAbortHandler, contentType: "application/problem+json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			app := newApp(hook.option())
			app.Use(fiberprblm.Recovery())
			app.Get("/x", loadVessel(tt.value))

			req := get(t, http.MethodGet, "/x")
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}

			res := test(t, app, req)

			if res.status != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", res.status)
			}

			if ct := res.header.Get("Content-Type"); ct != tt.contentType {
				t.Errorf("Content-Type = %q, want %q", ct, tt.contentType)
			}

			if s, ok := tt.value.(string); ok && strings.Contains(res.body, s) {
				t.Errorf("body %q leaks the panic value", res.body)
			}

			got := hook.received()
			if len(got) != 1 {
				t.Fatalf("hook received %v, want one call", got)
			}

			pe, ok := errors.AsType[*prblm.PanicError](got[0])
			if !ok || pe.Value != tt.value {
				t.Fatalf("hook received %v, want a panic error with value %v", got[0], tt.value)
			}

			if err, ok := tt.value.(error); ok && !errors.Is(got[0], err) {
				t.Errorf("hook error %v does not match %v", got[0], err)
			}

			if !strings.Contains(string(pe.Stack), "loadVessel") {
				t.Errorf("stack does not contain loadVessel:\n%s", pe.Stack)
			}
		})
	}
}

// fiber-adapter / Scenario: Recovery with the adapter's error handler, row
// without the recovery middleware: the library does not recover, so the
// panic reaches the middleware outside it.
func TestRecovery_ErrorHandler_NotInstalled(t *testing.T) {
	t.Parallel()

	var (
		hook    hookRecorder
		reached atomic.Value
	)

	app := newApp(hook.option())
	app.Use(func(c fiber.Ctx) (err error) {
		defer func() {
			if v := recover(); v != nil {
				reached.Store(v)

				err = c.SendStatus(http.StatusTeapot)
			}
		}()

		return c.Next()
	})
	app.Get("/x", loadVessel("boom"))

	test(t, app, get(t, http.MethodGet, "/x"))

	if v := reached.Load(); v != "boom" {
		t.Errorf("outer middleware recovered %v, want the panic \"boom\"", v)
	}

	assertHook(t, &hook, nil)
}

// fiber-adapter / Scenario: Recovery with the adapter's error handler, with
// a panic value whose Unwrap() error returns itself: the recovered error
// wraps it, so the core's walk aborts on the budget and the client gets the
// full safe 500 within 1 second, after one ErrUnwalkableError note.
func TestRecovery_ErrorHandler_SelfLoopValue(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	loop := &loopError{}
	app := newApp(hook.option())
	app.Use(fiberprblm.Recovery())
	app.Get("/v1/vessels/7", loadVessel(loop))

	res := testWithin(t, app, get(t, http.MethodGet, "/v1/vessels/7"))

	assertSafe500(t, res, true)
	assertNoted(t, &hook, "budget", nil, func(err error) bool {
		// Recovery's own error, then the panic error: neither walks loop.
		pe, ok := errors.Unwrap(err).(*prblm.PanicError) //nolint:errorlint // Recovery's own entry, by construction.

		return ok && pe.Value == loop
	})
}

// fiber-adapter / Scenario: Recovery with the adapter's error handler, the
// panic(nil) row. Under GODEBUG=panicnil=1, recover returns nil for
// panic(nil): Recovery tells the panic apart by the handlers not returning,
// so it is answered and reported like any other.
func TestRecovery_ErrorHandler_PanicNil(t *testing.T) {
	t.Parallel()

	if rerunUnderPanicNil(t) {
		return
	}

	var hook hookRecorder

	app := newApp(hook.option())
	app.Use(fiberprblm.Recovery())
	app.Get("/v1/vessels/7", loadVessel(nilPanicValue))

	res := testWithin(t, app, get(t, http.MethodGet, "/v1/vessels/7"))

	assertSafe500(t, res, true)
	assertHookOnce(t, &hook, isPanicOf(nil))
}

// A handler that calls runtime.Goexit (t.FailNow in a test) cannot be
// answered under fiber: fasthttp writes the response on the handler's
// goroutine once the handler returns, and Goexit ends that goroutine first,
// so the error handler never runs. The client gets no answer and the hooks
// hear nothing, as documented on Recovery, and the app keeps serving.
func TestRecovery_Goexit(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	app := newApp(hook.option())
	app.Use(fiberprblm.Recovery())
	app.Get("/v1/vessels/7", func(fiber.Ctx) error {
		runtime.Goexit()

		return nil
	})
	app.Get("/v1/vessels/8", func(fiber.Ctx) error { return ErrVesselNotFound })

	client, base := listen(t, app)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/vessels/7", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	if res, err := client.Do(req); err == nil {
		_ = res.Body.Close()

		t.Errorf("got status %d, want no answer: update Recovery's godoc if fiber now answers Goexit", res.StatusCode)
	}

	if got := hook.received(); len(got) != 0 {
		t.Errorf("hook received %s, want nothing", typesOf(got))
	}

	if status, _, err := fetchProblem(t.Context(), client, base+"/v1/vessels/8"); err != nil || status != http.StatusInternalServerError {
		t.Errorf("next request: %d, %v; want the app to keep serving", status, err)
	}
}

// Murphy row: a panic is never mapped like a returned error, as with the
// core's net/http Recover, even when its value is a mapped error.
func TestRecovery_ErrorHandler_MappedValue(t *testing.T) {
	t.Parallel()

	app := newApp(prblm.WithMappers(prblm.Status(ErrVesselNotFound, http.StatusNotFound)))
	app.Use(fiberprblm.Recovery())
	app.Get("/x", loadVessel(ErrVesselNotFound))

	res := test(t, app, get(t, http.MethodGet, "/x"))

	if res.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", res.status)
	}

	if p := problemOf(t, res); p.Title != "Internal Server Error" || p.Detail != "" {
		t.Errorf("problem %q / %q, want the safe 500", p.Title, p.Detail)
	}
}

// fiber-adapter / Scenario: Recovery with fiber's default error handler.
func TestRecovery_DefaultHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		text  string
	}{
		{name: "string value", value: "db password=hunter2", text: "hunter2"},
		{name: "error value", value: errors.New("secret"), text: "secret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New() // fiber's default error handler sends err.Error()
			app.Use(fiberprblm.Recovery())
			app.Get("/x", loadVessel(tt.value))

			res := test(t, app, get(t, http.MethodGet, "/x"))

			if res.status != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", res.status)
			}

			if strings.Contains(res.body, tt.text) {
				t.Errorf("body %q leaks the panic value", res.body)
			}
		})
	}
}

// Slice 7 end to end: route-group scopes and recovered panics over a real
// listener, with Recovery and the error handler together.
func TestRecovery_EndToEnd(t *testing.T) {
	t.Parallel()

	app := newApp()
	app.Use(fiberprblm.Recovery())
	contracts := app.Group("/v1/contracts", fiberprblm.Scope(prblm.Status(ErrLocked, http.StatusLocked)))
	contracts.Get("/:id", func(fiber.Ctx) error { return ErrLocked })
	contracts.Get("/:id/panic", loadVessel("boom"))

	client, base := listen(t, app)

	tests := []struct {
		path   string
		status int
	}{
		{path: "/v1/contracts/1", status: http.StatusLocked},
		{path: "/v1/contracts/1/panic", status: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			status, p, err := fetchProblem(t.Context(), client, base+tt.path)
			if err != nil {
				t.Fatal(err)
			}

			if status != tt.status || p.Status != tt.status || p.Instance != tt.path {
				t.Errorf("response %d, problem %d %q, want %d %q", status, p.Status, p.Instance, tt.status, tt.path)
			}
		})
	}
}

// fiber-adapter / Scenario: Panic on an unrouted documentation path. A
// recovered panic is never taken for fiber's route-not-found error, even
// when its value wraps fiber.ErrNotFound: the client gets the safe 500, not
// the documentation, and the hooks receive the panic.
func TestRecovery_PanicOnUnroutedDocPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value error
	}{
		{name: "wrapping fiber.ErrNotFound", value: fmt.Errorf("tenant lookup: %w", fiber.ErrNotFound)},
		{name: "fiber.ErrNotFound itself", value: fiber.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			app := discoveryApp(hook.option())
			app.Use(fiberprblm.Recovery())
			app.Use(func(fiber.Ctx) error { panic(tt.value) })

			res := testWithin(t, app, get(t, http.MethodGet, "/v1/api/probs/out-of-credit"))

			if res.status != http.StatusInternalServerError || res.header.Get("Content-Type") != "application/problem+json" {
				t.Errorf("got %d %q, want the safe 500 problem for a panic", res.status, res.header.Get("Content-Type"))
			}

			assertHookOnce(t, &hook, isPanicOf(tt.value))
		})
	}
}

// Murphy rows: the error handler's other checks see through a recovered
// panic into its value, yet the panic still gets the safe 500 (the hint
// loses to the self-describing panic) and is reported once.
func TestRecovery_ValueSeenThroughByAdapterChecks(t *testing.T) {
	t.Parallel()

	t.Run("fiber error value is no status hint", func(t *testing.T) {
		t.Parallel()

		var hook hookRecorder

		app := newApp(hook.option())
		app.Use(fiberprblm.Recovery())
		app.Get("/v1/vessels/7", loadVessel(fiber.ErrBadRequest))

		assertSafe500(t, testWithin(t, app, get(t, http.MethodGet, "/v1/vessels/7")), true)
		assertHookOnce(t, &hook, isPanicOf(fiber.ErrBadRequest))
	})

	t.Run("canceled value with the client gone", func(t *testing.T) {
		t.Parallel()

		var hook hookRecorder

		value := fmt.Errorf("query: %w", context.Canceled)

		app := newApp(hook.option())
		app.Use(func(c fiber.Ctx) error {
			ctx, cancel := context.WithCancel(c.Context())
			cancel()
			c.SetContext(ctx)

			return c.Next()
		})
		app.Use(fiberprblm.Recovery())
		app.Get("/v1/vessels/7", loadVessel(value))

		_ = testWithin(t, app, get(t, http.MethodGet, "/v1/vessels/7"))

		assertHookOnce(t, &hook, isPanicOf(value))
	})
}
