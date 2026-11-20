package ginprblm_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/adapters/ginprblm"
)

// gin-adapter / Scenario: Shared options across middleware and recovery.
func TestRecovery_Options(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler gin.HandlerFunc
		status  int
	}{
		{
			name:    "recorded error",
			handler: func(c *gin.Context) { _ = c.Error(ErrVesselNotFound) },
			status:  http.StatusNotFound,
		},
		{
			name:    "panic",
			handler: func(*gin.Context) { panic("boom") },
			status:  http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			opts := []prblm.Option{
				prblm.WithMappers(prblm.Status(ErrVesselNotFound, http.StatusNotFound)),
				hook.option(),
			}

			engine := gin.New()
			engine.Use(ginprblm.Recovery(opts...), ginprblm.Middleware(opts...))
			engine.GET("/v1/vessels/:id", tt.handler)

			rec := serve(t, engine, get(t, "/v1/vessels/7"))

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			if got := hook.received(); len(got) != 1 {
				t.Errorf("hook called %d times (%v), want 1", len(got), got)
			}
		})
	}
}

// gin-adapter / Scenario: Recovery placement, rows with recovery.
func TestRecovery_Placement(t *testing.T) {
	t.Parallel()

	type constructor = func(...prblm.Option) gin.HandlerFunc

	tests := []struct {
		name        string
		chain       []constructor
		accept      string
		contentType string
	}{
		{
			name:        "recovery then middleware",
			chain:       []constructor{ginprblm.Recovery, ginprblm.Middleware},
			contentType: "application/problem+json",
		},
		{
			name:        "middleware then recovery",
			chain:       []constructor{ginprblm.Middleware, ginprblm.Recovery},
			contentType: "application/problem+json",
		},
		{
			name:        "recovery only",
			chain:       []constructor{ginprblm.Recovery},
			contentType: "application/problem+json",
		},
		{
			name:        "recovery then middleware with xml",
			chain:       []constructor{ginprblm.Recovery, ginprblm.Middleware},
			accept:      "application/xml",
			contentType: "application/problem+xml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			engine := gin.New()
			for _, middleware := range tt.chain {
				engine.Use(middleware())
			}

			engine.GET("/x", func(*gin.Context) { panic("boom") })

			req := get(t, "/x")
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}

			rec := serve(t, engine, req)

			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}

			if ct := rec.Header().Get("Content-Type"); ct != tt.contentType {
				t.Errorf("Content-Type = %q, want %q", ct, tt.contentType)
			}

			if body := rec.Body.String(); strings.Contains(body, "boom") {
				t.Errorf("body %q leaks the panic value", body)
			}
		})
	}
}

// gin-adapter / Scenario: Recovery placement, middleware-only row: without
// Recovery the panic reaches gin's own handling.
func TestRecovery_Placement_NotInstalled(t *testing.T) {
	t.Parallel()

	engine := newEngine(func(*gin.Context) { panic("boom") })

	defer func() {
		if v := recover(); v != "boom" {
			t.Errorf("recovered %v, want the panic to reach gin as \"boom\"", v)
		}
	}()

	serve(t, engine, get(t, "/x"))
	t.Fatal("no panic reached gin")
}

// loadVessel is a handler that panics, named so the stack can be checked.
func loadVessel(*gin.Context) {
	panic(io.ErrClosedPipe)
}

// gin-adapter / Scenario: Panic details go to the hook.
func TestRecovery_Hook(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	engine := newRecoveryEngine(loadVessel, hook.option())

	serve(t, engine, get(t, "/x"))

	assertPanicHook(t, &hook, io.ErrClosedPipe)

	got := hook.received()
	pe, _ := errors.AsType[*prblm.PanicError](got[0])

	// The message is what a hook logs; it never reaches the client.
	if msg := got[0].Error(); !strings.Contains(msg, io.ErrClosedPipe.Error()) {
		t.Errorf("hook error message %q does not describe the panic value", msg)
	}

	if !strings.Contains(string(pe.Stack), "loadVessel") {
		t.Errorf("stack does not contain loadVessel:\n%s", pe.Stack)
	}
}

// gin-adapter / Scenario: Recovery edge cases.
func TestRecovery_Edge(t *testing.T) {
	t.Parallel()

	t.Run("abort handler panic is passed on", func(t *testing.T) {
		t.Parallel()

		var hook hookRecorder

		engine := newRecoveryEngine(func(*gin.Context) { panic(http.ErrAbortHandler) }, hook.option())

		rec := httptest.NewRecorder()

		func() {
			defer func() {
				if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // the exact sentinel must be passed on.
					t.Errorf("recovered %v, want http.ErrAbortHandler passed on", v)
				}
			}()

			engine.ServeHTTP(rec, get(t, "/x"))
		}()

		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want nothing written", rec.Body.String())
		}

		assertHook(t, &hook, nil)
	})

	t.Run("partial body flushed before the panic", func(t *testing.T) {
		t.Parallel()

		var hook hookRecorder

		engine := newRecoveryEngine(func(c *gin.Context) {
			_, _ = c.Writer.WriteString(`{"partial":`)
			c.Writer.Flush()
			panic("boom")
		}, hook.option())

		rec := fetch(t, engine, "/x")

		if body := rec.Body.String(); body != `{"partial":` {
			t.Errorf("body = %q, want only the partial body", body)
		}

		assertPanicHook(t, &hook, "boom")

		// Unlike the core's Recover, which gives the hooks a nil problem,
		// Recovery learns that the response started only after
		// RespondPanic: the hooks get the 500 problem that is not sent.
		if got := hook.problemStatuses(); !slices.Equal(got, []int{http.StatusInternalServerError}) {
			t.Errorf("hook problem statuses = %v, want [500]", got)
		}
	})

	t.Run("broken connection", func(t *testing.T) {
		t.Parallel()

		var hook hookRecorder

		broken := fmt.Errorf("write: %w", syscall.EPIPE)

		engine := newRecoveryEngine(func(*gin.Context) { panic(broken) }, hook.option())

		rec := serve(t, engine, get(t, "/x"))

		if ct := rec.Header().Get("Content-Type"); ct != "" || rec.Body.Len() != 0 {
			t.Errorf("response %q %q, want nothing written", ct, rec.Body.String())
		}

		assertPanicHook(t, &hook, broken)
	})

	t.Run("enricher panics too", func(t *testing.T) {
		t.Parallel()

		panickingEnricher := prblm.WithEnrichers(func(context.Context, *prblm.Problem) { panic("enricher") })

		engine := newRecoveryEngine(func(*gin.Context) { panic("boom") }, panickingEnricher)
		engine.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

		rec := serve(t, engine, get(t, "/x"))

		if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("response %d %q, want a 500 problem", rec.Code, rec.Header().Get("Content-Type"))
		}

		if rec := serve(t, engine, get(t, "/ok")); rec.Code != http.StatusOK {
			t.Errorf("follow-up status = %d, want 200", rec.Code)
		}
	})

	// Murphy row: a panic is never mapped like a returned error, as with the
	// core's net/http Recover, even when its value is a mapped error.
	t.Run("mapped panic value", func(t *testing.T) {
		t.Parallel()

		mapper := prblm.WithMappers(prblm.Status(ErrVesselNotFound, http.StatusNotFound))

		engine := newRecoveryEngine(func(*gin.Context) { panic(ErrVesselNotFound) }, mapper)

		rec := serve(t, engine, get(t, "/x"))

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}

		if p := decodeRecorded(t, rec); p.Title != "Internal Server Error" || p.Detail != "" {
			t.Errorf("problem %q / %q, want the safe 500", p.Title, p.Detail)
		}
	})
}

// gin-adapter / Scenario: Hostile panic value under gin. Within a second, a
// value whose chain cannot be walked gets the safe 500 problem, and the hook
// receives one ErrUnwalkableError note, then the panic.
func TestRecovery_HostileValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		middleware bool // Middleware installed after Recovery
		value      any
		isBroken   bool   // the value shows a broken connection: nothing is written
		reason     string // the note's reason, "" for no note
		noteValue  any    // the panic value the note wraps, for reason "panic"
	}{
		{
			name:       "recovery, then middleware: is panics",
			middleware: true,
			value:      isPanicError{},
			reason:     "panic",
			noteValue:  "boom",
		},
		{name: "recovery only: is panics", value: isPanicError{}, reason: "panic", noteValue: "boom"},
		{name: "recovery only: unwrap returns itself", value: &loopError{}, reason: "budget"},
		{name: "recovery, then middleware: join holds itself", middleware: true, value: joinCycle(t), reason: "budget"},
		{
			name:       "recovery, then middleware: broken connection",
			middleware: true,
			value:      fmt.Errorf("write: %w", syscall.EPIPE),
			isBroken:   true,
		},
		{name: "recovery only: string value", value: "boom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			engine := gin.New()
			engine.Use(ginprblm.Recovery(hook.option()))

			if tt.middleware {
				engine.Use(ginprblm.Middleware(hook.option()))
			}

			engine.GET("/v1/vessels/7", func(*gin.Context) { panic(tt.value) })

			rec := serveWithin(t, engine, get(t, "/v1/vessels/7"))

			assertSafe500(t, rec, !tt.isBroken)

			if tt.reason != "" {
				assertNoted(t, &hook, tt.reason, tt.noteValue, isPanicOf(tt.value))
			} else {
				assertHookOnce(t, &hook, isPanicOf(tt.value))
			}
		})
	}
}

// A panic value whose Is method panics with a value that cannot be
// formatted: the note is built without formatting it, so Recovery still
// answers with the safe 500, and the hook receives the note, then the panic.
func TestRecovery_HostileValue_UnformattableAbort(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	engine := gin.New()
	engine.Use(ginprblm.Recovery(hook.option()))
	engine.GET("/v1/vessels/7", func(*gin.Context) { panic(hostileIsError{}) })

	rec := serveWithin(t, engine, get(t, "/v1/vessels/7"))

	assertSafe500(t, rec, true)

	got := hook.received()
	if len(got) != 2 {
		t.Fatalf("hook received %d calls (%s), want a note, then the panic", len(got), typesOf(got))
	}

	// The note's message would format the hostile value, so only its
	// entries are checked.
	entries, isNote := noteEntries(got[0])
	if !isNote || len(entries) != 2 {
		t.Fatalf("first hook call is a %T, want an ErrUnwalkableError note wrapping a panic error", got[0])
	}

	if pe, ok := entries[1].(*prblm.PanicError); !ok { //nolint:errorlint // the note's own entry, by construction.
		t.Errorf("note wraps a %T, want a panic error", entries[1])
	} else if _, ok := pe.Value.(*selfPanicError); !ok {
		t.Errorf("note panic value is a %T, want the *selfPanicError", pe.Value)
	}

	if !isPanicOf(hostileIsError{})(got[1]) {
		t.Errorf("last hook call is a %T, want the panic", got[1])
	}
}

// gin-adapter / Scenario: Hostile panic value under gin, the panic(nil) row.
// Under GODEBUG=panicnil=1, recover returns nil for panic(nil): Recovery
// tells the panic apart by the handlers not returning.
func TestRecovery_HostileValue_PanicNil(t *testing.T) {
	t.Parallel()

	if rerunUnderPanicNil(t) {
		return
	}

	var hook hookRecorder

	engine := gin.New()
	engine.Use(ginprblm.Recovery(hook.option()))
	engine.GET("/v1/vessels/7", func(*gin.Context) { panic(nilPanicValue) })

	rec := serveWithin(t, engine, get(t, "/v1/vessels/7"))

	assertSafe500(t, rec, true)
	assertHookOnce(t, &hook, isPanicOf(nil))
}

// A handler that calls runtime.Goexit (t.FailNow in a test) does not return
// either, and recover cannot tell it from panic(nil) under panicnil=1: it is
// answered as a panic with value nil, as the core's Recover answers it, and
// the problem is flushed, since gin and net/http never finish the response.
func TestRecovery_Goexit(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	rec := httptest.NewRecorder()
	serveGoexit(t, newGoexitEngine(hook.option()), rec)

	assertSafe500(t, rec, true)
	assertHookOnce(t, &hook, isPanicOf(nil))

	if !rec.Flushed {
		t.Error("the problem was not flushed, so net/http would never send it")
	}
}

// Over a real server, Goexit ends the goroutine before gin and net/http
// finish the response: the problem reaches the client only because Recovery
// flushes it, wherever Middleware sits.
func TestRecovery_Goexit_RealServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		chain func(prblm.Option) []gin.HandlerFunc
	}{
		{name: "recovery only", chain: func(o prblm.Option) []gin.HandlerFunc {
			return []gin.HandlerFunc{ginprblm.Recovery(o)}
		}},
		{name: "recovery, then middleware", chain: func(o prblm.Option) []gin.HandlerFunc {
			return []gin.HandlerFunc{ginprblm.Recovery(o), ginprblm.Middleware(o)}
		}},
		{name: "middleware, then recovery", chain: func(o prblm.Option) []gin.HandlerFunc {
			return []gin.HandlerFunc{ginprblm.Middleware(o), ginprblm.Recovery(o)}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			engine := gin.New()
			engine.Use(tt.chain(hook.option())...)
			engine.GET("/v1/vessels/7", func(*gin.Context) { runtime.Goexit() })

			srv := httptest.NewServer(engine)
			t.Cleanup(srv.Close)

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v1/vessels/7", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}

			res, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("GET: %v (the client got no answer)", err)
			}

			defer func() { _ = res.Body.Close() }()

			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("reading the body: %v", err)
			}

			ct := res.Header.Get("Content-Type")
			if res.StatusCode != http.StatusInternalServerError || ct != "application/problem+json" || string(body) != safe500 {
				t.Errorf("got %d %q %q, want 500 application/problem+json %s", res.StatusCode, ct, body, safe500)
			}

			assertHookOnce(t, &hook, isPanicOf(nil))
		})
	}
}

// gin-adapter / Scenario: Hostile panic value under gin, the runtime.Goexit
// rows over HTTP/2. net/http's HTTP/2 server resets the stream of a handler
// that did not return, so the client receives the flushed problem, status,
// headers and every byte of the body, and then a stream reset with
// INTERNAL_ERROR. The hooks are still told once.
func TestRecovery_Goexit_HTTP2(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	srv := httptest.NewUnstartedServer(newGoexitEngine(hook.option()))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/vessels/7", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET over HTTP/2: %v (the client got no answer)", err)
	}

	defer func() { _ = res.Body.Close() }()

	if res.ProtoMajor != 2 {
		t.Fatalf("proto = %s, want HTTP/2", res.Proto)
	}

	body, err := io.ReadAll(res.Body)

	ct := res.Header.Get("Content-Type")
	if res.StatusCode != http.StatusInternalServerError || ct != "application/problem+json" || string(body) != safe500 {
		t.Errorf("got %d %q %q, want 500 application/problem+json %s", res.StatusCode, ct, body, safe500)
	}

	// net/http does not export its HTTP/2 stream error type, so the message
	// is matched.
	if err == nil || !strings.Contains(err.Error(), "stream error") || !strings.Contains(err.Error(), "INTERNAL_ERROR") {
		t.Errorf("reading the body: %v, want a stream reset with INTERNAL_ERROR after the body", err)
	}

	assertHookOnce(t, &hook, isPanicOf(nil))
}

// unflushableWriter is a response writer with no Flush method and nothing
// to unwrap, so it cannot flush.
type unflushableWriter struct {
	http.ResponseWriter
}

// A writer that cannot flush still gets the problem written: gin's Flush
// does nothing for it, nothing panics and nothing more is reported.
func TestRecovery_Goexit_UnflushableWriter(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	rec := httptest.NewRecorder()
	serveGoexit(t, newGoexitEngine(hook.option()), unflushableWriter{ResponseWriter: rec})

	assertSafe500(t, rec, true)
	assertHookOnce(t, &hook, isPanicOf(nil))

	if rec.Flushed {
		t.Error("the recorder was flushed through a writer that cannot flush")
	}
}

// flushPanicWriter is a response writer whose Flush panics.
type flushPanicWriter struct {
	*httptest.ResponseRecorder
}

func (flushPanicWriter) Flush() { panic("flush broke") }

// A writer whose Flush panics is broken like one whose Write panics: the
// problem is already written, and the second panic only reaches the hook.
func TestRecovery_Goexit_FlushPanics(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	rec := httptest.NewRecorder()
	serveGoexit(t, newGoexitEngine(hook.option()), flushPanicWriter{ResponseRecorder: rec})

	assertSafe500(t, rec, true)

	got := hook.received()
	if len(got) != 2 || !isPanicOf(nil)(got[0]) || !isPanicOf("flush broke")(got[1]) {
		t.Errorf("hook received %s, want the Goexit, then the writer's panic", typesOf(got))
	}
}

// gin-adapter / Scenario: Nested gin recovery. A real panic stops at the
// inner recovery and is reported once. A runtime.Goexit goes on through the
// outer recoveries, which cannot tell it from a new panic(nil) (see
// TestRecovery_Nested_SecondPanicNil), so each reports it. The client gets
// the one problem the inner recovery wrote.
func TestRecovery_Nested(t *testing.T) {
	t.Parallel()

	goexit := func(*gin.Context) { runtime.Goexit() }
	boom := func(*gin.Context) { panic("boom") }

	tests := []struct {
		name    string
		handler gin.HandlerFunc
		// inCore puts the core's Recover around an engine using Recovery
		// once, instead of an engine using Recovery twice.
		inCore bool
		value  any
		// statuses are the problem statuses the hooks receive, in order.
		statuses []int
	}{
		{name: "recovery twice, goexit", handler: goexit, statuses: []int{500, 500}},
		{name: "recovery twice, panic", handler: boom, value: "boom", statuses: []int{500}},
		{name: "core recover around recovery, goexit", handler: goexit, inCore: true, statuses: []int{500, 0}},
		{name: "core recover around recovery, panic", handler: boom, inCore: true, value: "boom", statuses: []int{500}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			engine := gin.New()
			engine.Use(ginprblm.Recovery(hook.option()))

			var served http.Handler = engine

			if tt.inCore {
				served = prblm.NewHandler(hook.option()).Recover(engine)
			} else {
				engine.Use(ginprblm.Recovery(hook.option()))
			}

			engine.GET("/v1/vessels/7", tt.handler)

			rec := httptest.NewRecorder()
			serveGoexit(t, served, rec)

			assertSafe500(t, rec, true)

			got := hook.received()
			if len(got) != len(tt.statuses) || !slices.Equal(hook.problemStatuses(), tt.statuses) {
				t.Fatalf("hook received %s with problem statuses %v, want statuses %v", typesOf(got), hook.problemStatuses(), tt.statuses)
			}

			for i, err := range got {
				if !isPanicOf(tt.value)(err) {
					t.Errorf("hook call %d received %v, want a panic error with value %v", i+1, err, tt.value)
				}
			}
		})
	}
}

// The core's Recover inside a gin handler, under Recovery: a
// runtime.Goexit goes on through Recovery, which reports it too, as nested
// recoveries do. The client still gets the one problem the core wrote.
func TestRecovery_Nested_CoreRecoverInsideHandler(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	engine := gin.New()
	engine.Use(ginprblm.Recovery(hook.option()))
	engine.GET("/v1/vessels/7", gin.WrapH(prblm.NewHandler(hook.option()).Recover(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { runtime.Goexit() }),
	)))

	rec := httptest.NewRecorder()
	serveGoexit(t, engine, rec)

	assertSafe500(t, rec, true)

	if got := hook.received(); len(got) != 2 || !isPanicOf(nil)(got[0]) || !isPanicOf(nil)(got[1]) {
		t.Errorf("hook received %s, want the Goexit reported by both recoveries", typesOf(got))
	}
}

// gin-adapter / Scenario: A second panic(nil) after nested gin recovery.
// Under GODEBUG=panicnil=1, the inner Recovery answers a real panic(nil) and
// returns; a middleware between the two then panics with nil again. That is
// a new panic, and the outer Recovery reports it.
func TestRecovery_Nested_SecondPanicNil(t *testing.T) {
	t.Parallel()

	if rerunUnderPanicNil(t) {
		return
	}

	var hook hookRecorder

	engine := gin.New()
	engine.Use(ginprblm.Recovery(hook.option()))
	engine.Use(func(c *gin.Context) {
		c.Next()
		// The inner panic was answered; this is a new one.
		panic(nilPanicValue)
	})
	engine.Use(ginprblm.Recovery(hook.option()))
	engine.GET("/v1/vessels/7", func(*gin.Context) { panic(nilPanicValue) })

	_ = serve(t, engine, get(t, "/v1/vessels/7"))

	if got := hook.received(); len(got) != 2 || !isPanicOf(nil)(got[0]) || !isPanicOf(nil)(got[1]) {
		t.Errorf("hook received %s, want two panic errors with value nil: the handler's, then the middleware's", typesOf(got))
	}
}

// newGoexitEngine returns a gin engine with Recovery, built with opts, and a
// handler at GET /v1/vessels/7 that calls runtime.Goexit.
func newGoexitEngine(opts ...prblm.Option) *gin.Engine {
	engine := gin.New()
	engine.Use(ginprblm.Recovery(opts...))
	engine.GET("/v1/vessels/7", func(*gin.Context) { runtime.Goexit() })

	return engine
}

// serveGoexit serves GET /v1/vessels/7 to w with engine, on a goroutine of
// its own, since a handler may end it with runtime.Goexit, and fails the
// test unless it is done within 1 second.
func serveGoexit(t *testing.T, engine http.Handler, w http.ResponseWriter) {
	t.Helper()

	req := get(t, "/v1/vessels/7")
	done := make(chan struct{})

	go func() {
		defer close(done)

		engine.ServeHTTP(w, req)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gin did not finish within 1s")
	}
}

// Slice 5 end to end: route-group scopes and recovered panics over a real
// server, with Recovery and Middleware together.
func TestRecovery_EndToEnd(t *testing.T) {
	t.Parallel()

	engine := gin.New()
	engine.Use(ginprblm.Recovery(), ginprblm.Middleware())
	contracts := engine.Group("/v1/contracts", ginprblm.Scope(prblm.Status(ErrLocked, http.StatusLocked)))
	contracts.GET("/:id", func(c *gin.Context) { _ = c.Error(ErrLocked) })
	contracts.GET("/:id/panic", func(c *gin.Context) {
		c.Status(http.StatusUnauthorized) // recorded, then replaced by the panic's 500
		panic("boom")
	})

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

			rec := fetch(t, engine, tt.path)

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			if p := decodeRecorded(t, rec); p.Status != tt.status || p.Instance != tt.path {
				t.Errorf("problem %d %q, want %d %q", p.Status, p.Instance, tt.status, tt.path)
			}
		})
	}
}

// panickingWriter is a response writer whose Write panics, standing in for
// a broken writer further down the stack.
type panickingWriter struct {
	*httptest.ResponseRecorder
}

func (panickingWriter) Write([]byte) (int, error) { panic("writer") }

// Murphy row (CLAUDE.md rule 5): a panic while Recovery writes the problem
// does not escape, and the hook still hears of the original panic and of
// the failed write.
func TestRecovery_Edge_WriterPanics(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	engine := newRecoveryEngine(func(*gin.Context) { panic("boom") }, hook.option())

	func() {
		defer func() {
			if v := recover(); v != nil {
				t.Errorf("panic escaped Recovery: %v", v)
			}
		}()

		engine.ServeHTTP(panickingWriter{httptest.NewRecorder()}, get(t, "/x"))
	}()

	var values []any

	for _, err := range hook.received() {
		if pe, ok := errors.AsType[*prblm.PanicError](err); ok {
			values = append(values, pe.Value)

			// The message is what a hook logs, for the writer's panic too.
			if msg := err.Error(); !strings.Contains(msg, fmt.Sprint(pe.Value)) {
				t.Errorf("hook error message %q does not describe the panic value %v", msg, pe.Value)
			}
		}
	}

	if !slices.Contains(values, any("boom")) || !slices.Contains(values, any("writer")) {
		t.Errorf("hook received panic values %v, want both \"boom\" and \"writer\"", values)
	}
}

// nilPanickingWriter is a response writer whose Write calls panic(nil).
type nilPanickingWriter struct {
	*httptest.ResponseRecorder
}

func (nilPanickingWriter) Write([]byte) (int, error) { panic(nilPanicValue) }

// A writer that calls panic(nil) while Recovery writes the problem, under
// GODEBUG=panicnil=1, is a second panic like any other: it does not escape,
// and the hook hears of both panics.
func TestRecovery_Edge_WriterPanicsNil(t *testing.T) {
	t.Parallel()

	if rerunUnderPanicNil(t) {
		return
	}

	var hook hookRecorder

	engine := newRecoveryEngine(func(*gin.Context) { panic("boom") }, hook.option())

	within(t, "gin", func() struct{} {
		engine.ServeHTTP(nilPanickingWriter{httptest.NewRecorder()}, get(t, "/x"))

		return struct{}{}
	})

	got := hook.received()
	if len(got) != 2 || !isPanicOf("boom")(got[0]) || !isPanicOf(nil)(got[1]) {
		t.Errorf("hook received %s, want the panic \"boom\", then the writer's panic with value nil", typesOf(got))
	}
}
