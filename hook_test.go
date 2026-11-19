package prblm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kartaladev/prblm"
)

// hookCall is one call of a hook.
type hookCall struct {
	err error
	// name is the name of the hook called, or "" for recorder.hook.
	name   string
	status int
}

// recorder records hook calls, in the order they happen.
type recorder struct {
	mu    sync.Mutex
	calls []hookCall
}

// hook is a hook that records its calls without a name.
func (r *recorder) hook(_ context.Context, err error, p *prblm.Problem) {
	r.record("", err, p)
}

// named returns a hook that records its calls under name.
func (r *recorder) named(name string) func(context.Context, error, *prblm.Problem) {
	return func(_ context.Context, err error, p *prblm.Problem) { r.record(name, err, p) }
}

func (r *recorder) record(name string, err error, p *prblm.Problem) {
	r.mu.Lock()
	defer r.mu.Unlock()

	status := 0
	if p != nil {
		status = p.Status
	}

	r.calls = append(r.calls, hookCall{err: err, name: name, status: status})
}

func (r *recorder) recorded() []hookCall {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.calls)
}

// write sends err through h.Wrap for GET /v1/vessels/7 and returns the
// recorded HTTP response.
func write(t *testing.T, h *prblm.Handler, err error) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/vessels/7", http.NoBody)
	h.Wrap(func(http.ResponseWriter, *http.Request) error { return err }).ServeHTTP(rec, req)

	return rec
}

// error-mapping / Scenario: Hook sees the original error.
func TestOnError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "unmapped", err: errors.New("pq: connection refused"), status: 500},
		{name: "mapped", err: errVesselNotFound, status: 404},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			h := prblm.NewHandler(prblm.WithMappers(prblm.Status(errVesselNotFound, 404)), prblm.OnError(r.hook))
			write(t, h, tt.err)

			calls := r.recorded()
			//nolint:errorlint // the hook must receive the original error value itself.
			if len(calls) != 1 || calls[0].err != tt.err || calls[0].status != tt.status {
				t.Errorf("hook calls = %+v, want one call with %v and status %d", calls, tt.err, tt.status)
			}
		})
	}
}

// error-mapping / Scenario: Nil error.
func TestNilError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "nil", err: nil},
		{name: "hint on nil", err: prblm.WithStatus(nil, 404)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			h := prblm.NewHandler(prblm.OnError(r.hook))

			// problem-response / Scenario: Respond without writing (nil row).
			req := prblm.Request{Method: http.MethodGet, Path: "/v1/vessels/7"}
			if res, ok := h.Respond(t.Context(), req, tt.err); ok {
				t.Errorf("Respond(nil) = %+v, true; want nothing to write", res)
			}

			rec := write(t, h, tt.err)

			if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
				t.Errorf("wrote %q with headers %v, want nothing", rec.Body.String(), rec.Header())
			}

			if calls := r.recorded(); len(calls) != 0 {
				t.Errorf("hook called %d times, want 0", len(calls))
			}

			if p := h.Resolve(t.Context(), tt.err); p != nil {
				t.Errorf("Resolve(nil) = %+v, want nil", p)
			}
		})
	}
}

// panickyError is an error whose self-description panics; it wraps
// errVesselNotFound.
type panickyError struct{}

func (panickyError) Error() string           { return "panicky" }
func (panickyError) Unwrap() error           { return errVesselNotFound }
func (panickyError) Problem() *prblm.Problem { panic("describer broke") }

// error-mapping / Scenario: Consumer code panics.
func TestResolve_FaultyConsumer(t *testing.T) {
	t.Parallel()

	panicking := prblm.MapperFunc(func(context.Context, error) *prblm.Problem { panic("mapper broke") })
	conflict := prblm.Status(errVesselNotFound, 409)

	tests := []struct {
		name        string
		opts        []prblm.Option
		err         error
		hookPanics  bool
		panicReport bool // the hook first receives a *PanicError
	}{
		{name: "panicking mapper", opts: []prblm.Option{prblm.WithMappers(panicking, conflict)}, err: errVesselNotFound, panicReport: true},
		{name: "panicking describer", opts: []prblm.Option{prblm.WithMappers(conflict)}, err: panickyError{}, panicReport: true},
		{
			name: "panicking enricher",
			opts: []prblm.Option{prblm.WithMappers(conflict), prblm.WithEnrichers(func(context.Context, *prblm.Problem) {
				panic("enricher broke")
			})},
			err:         errVesselNotFound,
			panicReport: true,
		},
		{name: "panicking hook", opts: []prblm.Option{prblm.WithMappers(conflict)}, err: errVesselNotFound, hookPanics: true},
		{name: "nil mapper", opts: []prblm.Option{prblm.WithMappers(nil, conflict)}, err: errVesselNotFound},
		{name: "typed-nil mapper func", opts: []prblm.Option{prblm.WithMappers(prblm.MapperFunc(nil), conflict)}, err: errVesselNotFound},
		{name: "nil enricher", opts: []prblm.Option{prblm.WithMappers(conflict), prblm.WithEnrichers(nil)}, err: errVesselNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			hook := r.hook
			if tt.hookPanics {
				hook = func(ctx context.Context, err error, p *prblm.Problem) {
					r.hook(ctx, err, p)
					panic("hook broke")
				}
			}

			h := prblm.NewHandler(append(tt.opts, prblm.OnError(hook))...)
			rec := write(t, h, tt.err)

			if rec.Code != 409 {
				t.Errorf("status = %d, want 409", rec.Code)
			}

			calls := r.recorded()

			want := 1
			if tt.panicReport {
				want = 2

				if len(calls) == 2 {
					if _, ok := errors.AsType[*prblm.PanicError](calls[0].err); !ok {
						t.Errorf("first hook call got %v, want a *prblm.PanicError", calls[0].err)
					}
				}
			}

			if len(calls) != want {
				t.Fatalf("hook called %d times, want %d: %+v", len(calls), want, calls)
			}

			//nolint:errorlint // the last call must carry the original error value itself.
			if last := calls[len(calls)-1].err; last != tt.err {
				t.Errorf("last hook call got %v, want %v", last, tt.err)
			}
		})
	}
}

// Without a hook, a panicking mapper is still contained.
func TestResolve_FaultyConsumer_NoHook(t *testing.T) {
	t.Parallel()

	h := prblm.NewHandler(prblm.WithMappers(
		prblm.MapperFunc(func(context.Context, error) *prblm.Problem { panic("mapper broke") }),
		prblm.Status(errVesselNotFound, 409),
	))

	if rec := write(t, h, errVesselNotFound); rec.Code != 409 {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

// asPanicError is an error whose As method panics with "boom".
type asPanicError struct{}

func (asPanicError) Error() string { return "as panics" }
func (asPanicError) As(any) bool   { panic("boom") }

// isPanicError is an error whose Is method panics with "boom".
type isPanicError struct{}

func (isPanicError) Error() string { return "is panics" }
func (isPanicError) Is(error) bool { panic("boom") }

// valueIsError has value-receiver methods, so a nil *valueIsError panics
// when its Is method is called.
type valueIsError struct{}

func (valueIsError) Error() string { return "value is" }
func (valueIsError) Is(error) bool { return false }

// emptyAsError's As method reports a match without setting its target, so
// errors.As and errors.AsType hand back the target's zero value: a nil
// pointer or a nil interface.
type emptyAsError struct{}

func (emptyAsError) Error() string { return "empty as" }
func (emptyAsError) As(any) bool   { return true }

// writeContained is write, failing the test instead of crashing it when a
// panic escapes the handler.
func writeContained(t *testing.T, h *prblm.Handler, err error) *httptest.ResponseRecorder {
	t.Helper()

	defer func() {
		if v := recover(); v != nil {
			t.Fatalf("a panic escaped the handler: %v", v)
		}
	}()

	return write(t, h, err)
}

// error-mapping / Scenario: Error methods panic.
func TestResolve_FaultyError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		opts   []prblm.Option
		err    error
		status int
		body   string
		// boom is set when every panic error the hooks receive holds "boom".
		boom bool
	}{
		// Examples: zero configuration.
		{name: "as method panics", err: asPanicError{}, status: 500, body: safe500, boom: true},
		{name: "is method panics", err: isPanicError{}, status: 500, body: safe500, boom: true},
		{name: "nil pointer with a value-receiver is method", err: (*valueIsError)(nil), status: 500, body: safe500},
		// Examples: the status-hint step.
		{name: "as method panics without built-ins", opts: []prblm.Option{prblm.WithoutBuiltins()}, err: asPanicError{}, status: 500, body: safe500, boom: true},
		{name: "as method leaves its target empty", opts: []prblm.Option{prblm.WithoutBuiltins()}, err: emptyAsError{}, status: 500, body: safe500},
		// Examples: another step still matches.
		{
			name:   "consumer mapper matches",
			opts:   []prblm.Option{prblm.WithMappers(prblm.Status(errVesselNotFound, 409))},
			err:    errors.Join(errVesselNotFound, asPanicError{}),
			status: 409,
			body:   `{"title":"Conflict","status":409,"instance":"/v1/vessels/7"}`,
			boom:   true,
		},
		{
			name:   "status hint over an is method that panics",
			err:    prblm.WithStatus(isPanicError{}, 404),
			status: 404,
			body:   `{"title":"Not Found","status":404,"instance":"/v1/vessels/7"}`,
			boom:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			h := prblm.NewHandler(append(tt.opts, prblm.OnError(r.hook))...)
			rec := writeContained(t, h, tt.err)

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			// The exact body also proves that the panic value never reaches it.
			if got := rec.Body.String(); got != tt.body {
				t.Errorf("body = %s, want %s", got, tt.body)
			}

			calls := r.recorded()
			if len(calls) < 2 {
				t.Fatalf("hook called %d times, want panic errors and then the error: %+v", len(calls), calls)
			}

			for _, c := range calls[:len(calls)-1] {
				pe, ok := errors.AsType[*prblm.PanicError](c.err)
				if !ok {
					t.Errorf("hook call got %v, want a *prblm.PanicError", c.err)

					continue
				}

				if tt.boom && pe.Value != "boom" {
					t.Errorf("panic value = %v, want boom", pe.Value)
				}
			}

			//nolint:errorlint // the last call must carry the original error value itself.
			if last := calls[len(calls)-1]; last.err != tt.err || last.status != tt.status {
				t.Errorf("last hook call got %v with status %d, want %v with %d", last.err, last.status, tt.err, tt.status)
			}
		})
	}
}

// error-mapping / Scenario: Error methods panic without a hook.
func TestResolve_FaultyError_NoHook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []prblm.Option
	}{
		{name: "zero configuration"},
		{name: "built-ins disabled", opts: []prblm.Option{prblm.WithoutBuiltins()}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := writeContained(t, prblm.NewHandler(tt.opts...), asPanicError{})

			if rec.Code != 500 {
				t.Errorf("status = %d, want 500", rec.Code)
			}
		})
	}
}

// names returns the names of the hooks called, in order.
func names(calls []hookCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.name)
	}

	return out
}

// panicHook returns a hook that panics with value, nil included.
func panicHook(value any) func(context.Context, error, *prblm.Problem) {
	return func(context.Context, error, *prblm.Problem) { panic(value) }
}

// hookCase is a row of a scenario about the order of the hooks.
type hookCase struct {
	opts  func(r *recorder) []prblm.Option
	name  string
	calls []string
}

// checkHookCases runs each case: a handler built with the case's options
// and then extra writes err, the response status must be status, and the
// recording hooks must be called in the order of calls, each with err and
// status.
func checkHookCases(t *testing.T, err error, status int, extra []prblm.Option, tests []hookCase) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			rec := write(t, prblm.NewHandler(append(tt.opts(&r), extra...)...), err)
			if rec.Code != status {
				t.Errorf("status = %d, want %d", rec.Code, status)
			}

			calls := r.recorded()
			if got := names(calls); !slices.Equal(got, tt.calls) {
				t.Fatalf("hooks called %v, want %v", got, tt.calls)
			}

			for _, c := range calls {
				//nolint:errorlint // each hook must receive the original error value itself.
				if c.err != err || c.status != status {
					t.Errorf("hook %s got %v with status %d, want %v with %d", c.name, c.err, c.status, err, status)
				}
			}
		})
	}
}

// error-mapping / Scenario: Replaced and added hooks (Examples: registered).
func TestHooks_Order(t *testing.T) {
	t.Parallel()

	checkHookCases(t, errors.New("boom"), http.StatusInternalServerError, nil, []hookCase{
		{
			name:  "OnError only",
			opts:  func(r *recorder) []prblm.Option { return []prblm.Option{prblm.OnError(r.named("A"))} },
			calls: []string{"A"},
		},
		{
			name:  "AddErrorHook only",
			opts:  func(r *recorder) []prblm.Option { return []prblm.Option{prblm.AddErrorHook(r.named("B"))} },
			calls: []string{"B"},
		},
		{
			name: "OnError then two added",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.OnError(r.named("A")), prblm.AddErrorHook(r.named("B")), prblm.AddErrorHook(r.named("C"))}
			},
			calls: []string{"A", "B", "C"},
		},
		{
			name: "added before OnError",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.AddErrorHook(r.named("B")), prblm.OnError(r.named("A"))}
			},
			calls: []string{"A", "B"},
		},
		{
			name: "OnError replaced",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.OnError(r.named("A")), prblm.OnError(r.named("A2")), prblm.AddErrorHook(r.named("B"))}
			},
			calls: []string{"A2", "B"},
		},
		{
			name: "same hook added twice",
			opts: func(r *recorder) []prblm.Option {
				b := r.named("B")

				return []prblm.Option{prblm.AddErrorHook(b), prblm.AddErrorHook(b)}
			},
			calls: []string{"B", "B"},
		},
	})
}

// error-mapping / Scenario: Replaced and added hooks (Examples: misuse).
func TestHooks_Misuse(t *testing.T) {
	t.Parallel()

	checkHookCases(t, errors.New("boom"), http.StatusInternalServerError, nil, []hookCase{
		{
			name: "OnError(nil) keeps added hooks",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.AddErrorHook(r.named("B")), prblm.OnError(nil)}
			},
			calls: []string{"B"},
		},
		{
			name: "OnError(nil) clears OnError",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.OnError(r.named("A")), prblm.OnError(nil)}
			},
		},
		{
			name: "nil added hook",
			opts: func(*recorder) []prblm.Option { return []prblm.Option{prblm.AddErrorHook(nil)} },
		},
		{
			name: "nil added hook among others",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.OnError(r.named("A")), prblm.AddErrorHook(nil), prblm.AddErrorHook(r.named("C"))}
			},
			calls: []string{"A", "C"},
		},
	})
}

// error-mapping / Scenario: Replaced and added hooks (Examples: misuse,
// AddErrorHook(nil)). With no non-nil hook a handler reports nothing, so
// nobody builds a note that nobody reads.
func TestAddErrorHook_NilIsNotReporting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []prblm.Option
		want bool
	}{
		{name: "no hooks"},
		{name: "nil added hook", opts: []prblm.Option{prblm.AddErrorHook(nil)}},
		{name: "added hook", opts: []prblm.Option{prblm.AddErrorHook(func(context.Context, error, *prblm.Problem) {})}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := prblm.IsReporting(t.Context(), prblm.NewHandler(tt.opts...)); got != tt.want {
				t.Errorf("isReporting = %t, want %t", got, tt.want)
			}
		})
	}
}

// error-mapping / Scenario: A panicking hook does not silence the others.
func TestHooks_Panic(t *testing.T) {
	t.Parallel()

	conflict := []prblm.Option{prblm.WithMappers(prblm.Status(errVesselNotFound, 409))}

	checkHookCases(t, errVesselNotFound, http.StatusConflict, conflict, []hookCase{
		{
			name: "OnError hook panics first",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.OnError(panicHook("boom")), prblm.AddErrorHook(r.named("B"))}
			},
			calls: []string{"B"},
		},
		{
			name: "added hook panics in the middle",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.OnError(r.named("A")), prblm.AddErrorHook(panicHook("boom")), prblm.AddErrorHook(r.named("C"))}
			},
			calls: []string{"A", "C"},
		},
		{
			name: "last hook panics with nil",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.AddErrorHook(r.named("B")), prblm.AddErrorHook(panicHook(nil))}
			},
			calls: []string{"B"},
		},
		{
			name: "added hook panics with ErrAbortHandler",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.OnError(r.named("A")), prblm.AddErrorHook(panicHook(http.ErrAbortHandler)), prblm.AddErrorHook(r.named("C"))}
			},
			calls: []string{"A", "C"},
		},
	})
}

// error-mapping / Scenario: Every hook receives notes and silent requests
// reach none.
func TestHooks_Notes(t *testing.T) {
	t.Parallel()

	isPanic := func(err error) bool { _, ok := errors.AsType[*prblm.PanicError](err); return ok }
	isInvalidStatus := func(err error) bool { return errors.Is(err, prblm.ErrInvalidStatus) }
	//nolint:errorlint // the answered error must be the original value itself.
	isErrX := func(err error) bool { return err == errVesselNotFound }

	// writeErrX has the handler write ErrX through net/http.
	writeErrX := func(t *testing.T, h *prblm.Handler) { t.Helper(); write(t, h, errVesselNotFound) }

	// discoverUnknown asks discovery for an unknown type, which is a 404.
	discoverUnknown := func(t *testing.T, h *prblm.Handler) {
		t.Helper()

		if rec := discover(t, h, http.MethodGet, "/problems/unknown", nil); rec.Code != http.StatusNotFound {
			t.Errorf("discovery status = %d, want 404", rec.Code)
		}
	}

	tests := []struct {
		event    func(t *testing.T, h *prblm.Handler)
		name     string
		opts     []prblm.Option
		received []func(error) bool
	}{
		{
			name: "registered mapper panics",
			opts: []prblm.Option{prblm.WithMappers(
				prblm.MapperFunc(func(context.Context, error) *prblm.Problem { panic("mapper broke") }),
				prblm.Status(errVesselNotFound, 409),
			)},
			event:    writeErrX,
			received: []func(error) bool{isPanic, isErrX},
		},
		{
			name:     "mapper returns status 200",
			opts:     []prblm.Option{prblm.WithMappers(prblm.Status(errVesselNotFound, 200))},
			event:    writeErrX,
			received: []func(error) bool{isInvalidStatus, isErrX},
		},
		{
			name:  "discovery answers an unknown type with 404",
			opts:  []prblm.Option{prblm.WithNamespace("/problems"), prblm.WithTypes(blacklistedSupplier)},
			event: discoverUnknown,
		},
		{
			// A plain discovery 404 reports nothing at all, so this row
			// makes the silent request produce a note first.
			name: "discovery 404 with a panicking enricher",
			opts: []prblm.Option{
				prblm.WithNamespace("/problems"),
				prblm.WithTypes(blacklistedSupplier),
				prblm.WithEnrichers(func(context.Context, *prblm.Problem) { panic("enricher broke") }),
			},
			event: discoverUnknown,
		},
		{
			name:  "nil error",
			event: func(t *testing.T, h *prblm.Handler) { t.Helper(); write(t, h, nil) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			h := prblm.NewHandler(append(tt.opts, prblm.OnError(r.named("A")), prblm.AddErrorHook(r.named("B")))...)
			tt.event(t, h)

			byHook := map[string][]error{}
			for _, c := range r.recorded() {
				byHook[c.name] = append(byHook[c.name], c.err)
			}

			for _, name := range []string{"A", "B"} {
				got := byHook[name]
				if len(got) != len(tt.received) {
					t.Fatalf("hook %s received %v, want %d calls", name, got, len(tt.received))
				}

				for i, matches := range tt.received {
					if !matches(got[i]) {
						t.Errorf("hook %s call %d received %v, which does not match", name, i, got[i])
					}
				}
			}
		})
	}
}

// The documented library hook of ExampleAddErrorHook must survive the nil
// problem of a panic note, or a consumer who copies it loses that record.
func TestExampleHook_NilProblem(t *testing.T) {
	t.Parallel()

	var log strings.Builder

	h := prblm.NewHandler(
		prblm.AddErrorHook(libraryAccessLog(&log)),
		prblm.WithMappers(
			prblm.MapperFunc(func(context.Context, error) *prblm.Problem { panic("mapper broke") }),
			prblm.Status(errVesselNotFound, 409),
		),
	)

	if rec := write(t, h, errVesselNotFound); rec.Code != 409 {
		t.Fatalf("status = %d, want 409", rec.Code)
	}

	lines := strings.Split(strings.TrimSuffix(log.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("access log = %q, want a line for the panic note and one for the 409", lines)
	}

	if !strings.HasPrefix(lines[0], "library note: ") {
		t.Errorf("first line = %q, want the panic note", lines[0])
	}

	if want := "library access log: 409"; lines[1] != want {
		t.Errorf("last line = %q, want %q", lines[1], want)
	}
}
