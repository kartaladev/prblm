package prblm_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

// nilDescriberError is a describer whose Problem method calls panic(nil),
// wrapping errVesselNotFound.
type nilDescriberError struct{}

func (nilDescriberError) Error() string           { return "nil describer" }
func (nilDescriberError) Unwrap() error           { return errVesselNotFound }
func (nilDescriberError) Problem() *prblm.Problem { panic(nilValue) }

// checkConsumerPanicNil runs the rows where consumer code calls panic(nil)
// and a consumer mapper errVesselNotFound to 409 is registered after it:
// the status is 409, and the hook receives a panic error whose value
// isPanicValue accepts, then the error.
func checkConsumerPanicNil(t *testing.T, isPanicValue func(any) bool) {
	t.Helper()

	conflict := prblm.Status(errVesselNotFound, 409)

	tests := []struct {
		name string
		opts []prblm.Option
		err  error
	}{
		{
			name: "mapper",
			opts: []prblm.Option{prblm.WithMappers(
				prblm.MapperFunc(func(context.Context, error) *prblm.Problem { panic(nilValue) }),
				conflict,
			)},
			err: errVesselNotFound,
		},
		{
			name: "enricher",
			opts: []prblm.Option{
				prblm.WithMappers(conflict),
				prblm.WithEnrichers(func(context.Context, *prblm.Problem) { panic(nilValue) }),
			},
			err: errVesselNotFound,
		},
		{name: "describer", opts: []prblm.Option{prblm.WithMappers(conflict)}, err: nilDescriberError{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			rec := write(t, prblm.NewHandler(append(tt.opts, prblm.OnError(r.hook))...), tt.err)
			if rec.Code != 409 {
				t.Errorf("status = %d, want 409", rec.Code)
			}

			calls := r.recorded()
			assertHookCalls(t, calls, tt.err, 409, 1)

			if pe, ok := errors.AsType[*prblm.PanicError](calls[0].err); ok && !isPanicValue(pe.Value) {
				t.Errorf("panic value = %#v, not the expected one", pe.Value)
			}
		})
	}
}

// error-mapping / Scenario: Consumer code panics (the panic(nil) row): the
// runtime turns panic(nil) into a *runtime.PanicNilError. The enricher and
// describer rows are the same faults as in the GODEBUG=panicnil=1 scenario.
func TestResolve_ConsumerPanicNil(t *testing.T) {
	t.Parallel()

	checkConsumerPanicNil(t, isPanicNil)
}

// error-mapping / Scenario: panic(nil) in consumer code under
// GODEBUG=panicnil=1: recover returns nil, and the panic is still reported.
// The test runs itself again in a subprocess with that setting.
func TestResolve_ConsumerPanicNil_PanicNil1(t *testing.T) {
	t.Parallel()

	if rerunUnderPanicNil(t) {
		return
	}

	checkConsumerPanicNil(t, func(v any) bool { return v == nil })
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

// unwrapPanicError is an error whose Unwrap() error panics with "boom".
type unwrapPanicError struct{}

func (unwrapPanicError) Error() string { return "unwrap panics" }
func (unwrapPanicError) Unwrap() error { panic("boom") }

// listPanicError is an error whose Unwrap() []error panics with "boom".
type listPanicError struct{}

func (listPanicError) Error() string   { return "list panics" }
func (listPanicError) Unwrap() []error { panic("boom") }

// nilPanicError is an error whose Unwrap() error calls panic(nil).
type nilPanicError struct{}

// nilValue is a nil panic value, so panic(nilValue) is panic(nil).
var nilValue any

func (nilPanicError) Error() string { return "nil panic" }
func (nilPanicError) Unwrap() error { panic(nilValue) }

// unwalkablePrefix starts the message of every ErrUnwalkableError note.
const unwalkablePrefix = "prblm: error chain cannot be walked: "

// assertNoted checks that the hooks received exactly one ErrUnwalkableError
// note naming reason, then err, both with the 500 problem being sent. It
// returns the panic error the note wraps, nil for reason "budget".
func assertNoted(t *testing.T, calls []hookCall, err error, reason string) *prblm.PanicError {
	t.Helper()

	if len(calls) != 2 {
		t.Fatalf("hook calls = %+v, want a note, then the error", calls)
	}

	note := calls[0]
	if !errors.Is(note.err, prblm.ErrUnwalkableError) || !strings.HasPrefix(note.err.Error(), unwalkablePrefix+reason) {
		t.Errorf("first hook call = %v, want an ErrUnwalkableError note with reason %s", note.err, reason)
	}

	if note.status != 500 {
		t.Errorf("note problem status = %d, want 500", note.status)
	}

	if last := calls[1]; !isSameError(last.err, err) || last.status != 500 {
		t.Errorf("last hook call = %v with status %d, want the returned error with 500", last.err, last.status)
	}

	pe, isPanic := errors.AsType[*prblm.PanicError](note.err)
	if isPanic != (reason == "panic") {
		t.Errorf("note wraps a panic error: %t, want %t", isPanic, reason == "panic")
	}

	return pe
}

// uncomparableError is a comparable struct type whose field can hold an
// uncomparable value, so comparing two of them with == can panic.
type uncomparableError struct{ v any }

func (uncomparableError) Error() string { return "uncomparable" }

// isUncomparable reports whether v is the runtime error of comparing an
// uncomparable type.
func isUncomparable(v any) bool {
	err, ok := v.(runtime.Error)

	return ok && strings.Contains(err.Error(), "comparing uncomparable type")
}

// isSameError reports whether got is want itself. An error whose value
// cannot be compared, because a field holds an uncomparable value, has no
// identity to check, so the same type and message count as the same error.
func isSameError(got, want error) (same bool) {
	defer func() {
		if recover() != nil {
			same = reflect.TypeOf(got) == reflect.TypeOf(want) && got.Error() == want.Error()
		}
	}()

	return got == want //nolint:errorlint // the hook must receive the original error value itself.
}

// isBoom reports whether v is the "boom" panic value.
func isBoom(v any) bool { return v == "boom" }

// rerunUnderPanicNil runs the top-level test t again in a subprocess of this
// test binary with GODEBUG=panicnil=1, fails t unless it passes there, and
// reports true, so the caller returns. In a process that already has the
// setting it does nothing and reports false, and the caller runs its rows.
func rerunUnderPanicNil(t *testing.T) bool {
	t.Helper()

	if isPanicNilMode() {
		return false
	}

	//nolint:gosec // os.Args[0] is this test binary, run again with fixed arguments.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")

	cmd.Env = append(os.Environ(), "GODEBUG=panicnil=1")

	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()+" ") {
		t.Fatalf("under GODEBUG=panicnil=1: %v\n%s", err, out)
	}

	return true
}

// nilPanicType is the type the note names for a panic(nil) in this process:
// *runtime.PanicNilError, or <nil> under GODEBUG=panicnil=1, where recover
// returns nil.
func nilPanicType() string {
	if isPanicNilMode() {
		return "<nil>"
	}

	return "*runtime.PanicNilError"
}

// isPanicNilMode reports whether this process runs with GODEBUG=panicnil=1,
// where recover returns nil for panic(nil).
func isPanicNilMode() bool {
	return strings.Contains(os.Getenv("GODEBUG"), "panicnil=1")
}

// isPanicNil reports whether v is what recover returns for panic(nil) in
// this process: a *runtime.PanicNilError, or nil under GODEBUG=panicnil=1.
func isPanicNil(v any) bool {
	if isPanicNilMode() {
		return v == nil
	}

	err, ok := v.(error)
	if !ok {
		return false
	}

	_, ok = errors.AsType[*runtime.PanicNilError](err)

	return ok
}

// error-mapping / Scenario: Error methods panic; error-chain-walk / Scenario:
// Error methods panic. A panic in a method of the error aborts the walk: the
// answer is the safe 500, even when another step would have matched, and the
// hooks receive one ErrUnwalkableError note wrapping the panic, then the
// error.
func TestResolve_FaultyError(t *testing.T) {
	t.Parallel()

	withoutBuiltins := []prblm.Option{prblm.WithoutBuiltins()}

	tests := []struct {
		name   string
		opts   []prblm.Option
		err    error
		status int
		body   string
		// value checks the panic value of the note; nil when the hooks
		// receive the error alone.
		value func(any) bool
	}{
		// Examples: zero configuration.
		{name: "as method panics", err: asPanicError{}, status: 500, body: safe500, value: isBoom},
		{name: "is method panics", err: isPanicError{}, status: 500, body: safe500, value: isBoom},
		{name: "unwrap error panics", err: unwrapPanicError{}, status: 500, body: safe500, value: isBoom},
		{name: "unwrap list panics", err: listPanicError{}, status: 500, body: safe500, value: isBoom},
		{name: "unwrap calls panic nil", err: nilPanicError{}, status: 500, body: safe500, value: isPanicNil},
		{name: "nil pointer with a value-receiver is method", err: (*valueIsError)(nil), status: 500, body: safe500},
		// Examples: the status-hint step.
		{name: "as method panics without built-ins", opts: withoutBuiltins, err: asPanicError{}, status: 500, body: safe500, value: isBoom},
		{name: "as method leaves its target empty", opts: withoutBuiltins, err: emptyAsError{}, status: 500, body: safe500},
		// Examples: another step would have matched.
		{
			name:   "consumer mapper would match",
			opts:   []prblm.Option{prblm.WithMappers(prblm.Status(errVesselNotFound, 409))},
			err:    errors.Join(errVesselNotFound, asPanicError{}),
			status: 500,
			body:   safe500,
			value:  isBoom,
		},
		{name: "status hint over an is method that panics", err: prblm.WithStatus(isPanicError{}, 404), status: 500, body: safe500, value: isBoom},
		{
			name:   "status target that cannot be compared",
			opts:   []prblm.Option{prblm.WithMappers(prblm.Status(uncomparableError{v: []int{1}}, 409))},
			err:    uncomparableError{v: []int{1}},
			status: 500,
			body:   safe500,
			value:  isUncomparable,
		},
		{
			name:   "problem before an as method that panics",
			err:    errors.Join(prblm.New(404), asPanicError{}),
			status: 404,
			body:   `{"title":"Not Found","status":404,"instance":"/v1/vessels/7"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			h := prblm.NewHandler(append(tt.opts, prblm.OnError(r.hook))...)
			rec := writeWithin(t, h, tt.err)

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			// The exact body also proves that the panic value never reaches it.
			if got := rec.Body.String(); got != tt.body {
				t.Errorf("body = %s, want %s", got, tt.body)
			}

			calls := r.recorded()
			if tt.value == nil {
				assertHookCalls(t, calls, tt.err, tt.status, 0)

				return
			}

			pe := assertNoted(t, calls, tt.err, "panic")
			if pe != nil && !tt.value(pe.Value) {
				t.Errorf("panic value = %#v, not the expected one", pe.Value)
			}

			if pe != nil && len(pe.Stack) == 0 {
				t.Error("the panic error has no stack")
			}
		})
	}
}

// error-chain-walk / Scenario: panic(nil) under GODEBUG=panicnil=1. recover
// returns nil for an Unwrap that calls panic(nil): the walk still aborts, and
// the handler answers with the safe 500 and one note whose panic value is
// nil.
func TestResolve_FaultyError_PanicNil(t *testing.T) {
	t.Parallel()

	if rerunUnderPanicNil(t) {
		return
	}

	var r recorder

	h := prblm.NewHandler(prblm.OnError(r.hook))
	rec := writeWithin(t, h, nilPanicError{})

	if rec.Code != 500 || rec.Body.String() != safe500 {
		t.Errorf("got %d %s, want 500 %s", rec.Code, rec.Body, safe500)
	}

	calls := r.recorded()

	pe := assertNoted(t, calls, nilPanicError{}, "panic")
	if pe != nil && pe.Value != nil {
		t.Errorf("panic value = %#v, want nil", pe.Value)
	}

	// error-chain-walk / Scenario: Note text, the panicnil row.
	if want := "prblm: error chain cannot be walked: panic: <nil>"; calls[0].err.Error() != want {
		t.Errorf("note message = %q, want %q", calls[0].err.Error(), want)
	}
}

// selfPanicError's Error method panics with the error itself, so fmt fails
// formatting it twice in a row and re-panics.
type selfPanicError struct{}

func (e *selfPanicError) Error() string { panic(e) }

// hostileIsError's Is method panics with a *selfPanicError, a value that
// cannot be formatted.
type hostileIsError struct{}

func (hostileIsError) Error() string { return "is panics with a hostile value" }
func (hostileIsError) Is(error) bool { panic(&selfPanicError{}) }

// error-chain-walk / Scenario: Error methods panic, with a panic value that
// cannot be formatted. Building the ErrUnwalkableError note formats nothing,
// so neither the error answer nor the panic answer panics: the client gets
// the safe 500, and the hooks the note, then the error.
func TestRespond_UnformattableAbortValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		serve func(*prblm.Handler) http.Handler
		// last matches the last hook call: the error, or the panic.
		last func(error) bool
	}{
		{
			name: "returned error",
			serve: func(h *prblm.Handler) http.Handler {
				return h.Wrap(func(http.ResponseWriter, *http.Request) error { return hostileIsError{} })
			},
			last: func(err error) bool { return err == hostileIsError{} }, //nolint:errorlint // the very error.
		},
		{
			name:  "panic value",
			serve: func(h *prblm.Handler) http.Handler { return h.Recover(panicking(hostileIsError{})) },
			last: func(err error) bool {
				pe, ok := errors.AsType[*prblm.PanicError](err)

				return ok && pe.Value == hostileIsError{}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			h := prblm.NewHandler(prblm.OnError(r.hook))
			rec := serveWithin(t, tt.serve(h))

			if rec.Code != 500 || rec.Body.String() != safe500 {
				t.Errorf("got %d %s, want 500 %s", rec.Code, rec.Body, safe500)
			}

			calls := r.recorded()
			if len(calls) != 2 || !errors.Is(calls[0].err, prblm.ErrUnwalkableError) || !tt.last(calls[1].err) {
				t.Fatalf("hook calls = %d, want the note, then the error", len(calls))
			}

			pe, ok := errors.AsType[*prblm.PanicError](calls[0].err)
			if _, unformattable := pe.Value.(*selfPanicError); !ok || !unformattable {
				t.Errorf("the note does not wrap the unformattable panic value")
			}
		})
	}
}

// A hook can format an ErrUnwalkableError note whose panic value cannot be
// formatted: the message names only the value's type, so formatting calls no
// method of the value and does not panic. The response is still the safe
// 500, and the next hook still runs.
func TestRespond_HookFormatsUnformattableNote(t *testing.T) {
	t.Parallel()

	var (
		r        recorder
		messages []string
	)

	formatting := func(_ context.Context, err error, _ *prblm.Problem) { messages = append(messages, err.Error()) }
	h := prblm.NewHandler(prblm.OnError(formatting), prblm.AddErrorHook(r.hook))
	rec := writeWithin(t, h, hostileIsError{})

	if rec.Code != 500 || rec.Body.String() != safe500 {
		t.Errorf("got %d %s, want 500 %s", rec.Code, rec.Body, safe500)
	}

	if calls := r.recorded(); len(calls) != 2 || !errors.Is(calls[0].err, prblm.ErrUnwalkableError) {
		t.Errorf("hook calls after the formatting hook = %+v, want the note, then the error", calls)
	}

	want := []string{"prblm: error chain cannot be walked: panic: *prblm_test.selfPanicError", "is panics with a hostile value"}
	if !slices.Equal(messages, want) {
		t.Errorf("formatted messages = %q, want %q", messages, want)
	}
}

// error-mapping / Scenario: Error methods panic without a hook;
// error-chain-walk / Scenario: Error methods panic without a hook.
func TestResolve_FaultyError_NoHook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []prblm.Option
		err  error
	}{
		{name: "as method panics", err: asPanicError{}},
		{name: "as method panics without built-ins", opts: []prblm.Option{prblm.WithoutBuiltins()}, err: asPanicError{}},
		{name: "unwrap error panics", err: unwrapPanicError{}},
		{name: "self-loop", err: newLoop("loop")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if rec := writeWithin(t, prblm.NewHandler(tt.opts...), tt.err); rec.Code != 500 {
				t.Errorf("status = %d, want 500", rec.Code)
			}
		})
	}
}

// loopError wraps next through Unwrap() error; one whose next is itself
// loops. Its message is text.
type loopError struct {
	next error
	text string
}

func (e *loopError) Error() string { return e.text }
func (e *loopError) Unwrap() error { return e.next }

// newLoop returns an error with message text whose Unwrap() error returns
// itself.
func newLoop(text string) *loopError {
	e := &loopError{text: text}
	e.next = e

	return e
}

// listLoopError wraps its entries as given through Unwrap() []error. It is
// a pointer type, so it can hold itself.
type listLoopError struct {
	errs []error
}

func (*listLoopError) Error() string     { return "list" }
func (e *listLoopError) Unwrap() []error { return e.errs }

// selfCopies returns an error whose Unwrap() []error holds n copies of
// itself.
func selfCopies(n int) error {
	e := &listLoopError{}
	e.errs = slices.Repeat([]error{e}, n)

	return e
}

// joinCycle returns an errors.Join whose only entry is the join itself. It
// relies on the join's Unwrap returning its own slice.
func joinCycle(t *testing.T) error {
	t.Helper()

	j := errors.Join(errors.New("leaf"))

	u, ok := j.(interface{ Unwrap() []error })
	if !ok {
		t.Fatal("errors.Join has no Unwrap() []error")
	}

	u.Unwrap()[0] = j

	return j
}

// joinTree returns levels nested errors.Join(n, n) around a leaf.
func joinTree(levels int) error {
	err := errors.New("leaf")
	for range levels {
		err = errors.Join(err, err)
	}

	return err
}

// wrapN returns err wrapped n times with %w.
func wrapN(err error, n int) error {
	for range n {
		err = fmt.Errorf("w: %w", err)
	}

	return err
}

// writeWithin is write, under the guard of within.
func writeWithin(t *testing.T, h *prblm.Handler, err error) *httptest.ResponseRecorder {
	t.Helper()

	return within(t, "the handler", func() *httptest.ResponseRecorder { return write(t, h, err) })
}

// within returns the result of f, failing the test instead of crashing it
// when a panic escapes f, whatever its value. A call that takes longer than
// a second crashes the test binary: a hang must not stall the suite, and the
// runaway walk must not keep running. what names f in the failure.
func within[T any](t *testing.T, what string, f func() T) T {
	t.Helper()

	type result struct {
		value    T
		escaped  any
		panicked bool
	}

	done := make(chan result, 1)

	go func() {
		res := result{panicked: true}

		defer func() {
			if res.panicked {
				res.escaped = recover()
			}

			done <- res
		}()

		res.value = f()
		res.panicked = false
	}()

	select {
	case res := <-done:
		if res.panicked {
			t.Fatalf("a panic escaped %s: %v", what, res.escaped)
		}

		return res.value
	case <-time.After(time.Second):
		panic(t.Name() + ": " + what + " did not return within 1s")
	}
}

// error-chain-walk / Scenarios: Cycles and fan-out (net/http rows); Budget
// boundary (past the budget). Within a second, the client receives the safe
// 500 problem, and the hooks receive an ErrUnwalkableError note with reason
// budget, then the error.
func TestResolve_Unwalkable(t *testing.T) {
	t.Parallel()

	mutual := &loopError{text: "a"}
	mutual.next = &loopError{next: mutual, text: "b"}

	tests := []struct {
		name string
		err  error
	}{
		// Examples: net/http.
		{name: "unwrap returns itself", err: newLoop("loop")},
		{name: "two errors unwrap to each other", err: mutual},
		{name: "unwrap list holds itself", err: selfCopies(1)},
		{name: "join holds itself", err: joinCycle(t)},
		{name: "25 nested joins", err: joinTree(25)},
		// Budget boundary: past the budget.
		{name: "1,001 entries", err: wrapN(prblm.ErrNotFound, 1000)},
		{name: "1,000 children", err: &listLoopError{errs: slices.Repeat([]error{errors.New("x")}, 1000)}},
		{name: "20,000 self copies", err: selfCopies(20_000)},
		{name: "1,000 entries, then a panicking unwrap", err: wrapN(unwrapPanicError{}, 1000)},
		{name: "a status hint around 1,000 entries", err: prblm.WithStatus(wrapN(prblm.ErrNotFound, 999), 410)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			rec := writeWithin(t, prblm.NewHandler(prblm.OnError(r.hook)), tt.err)

			if rec.Code != 500 || rec.Header().Get("Content-Type") != "application/problem+json" || rec.Body.String() != safe500 {
				t.Errorf("response = %d %q %s, want 500 application/problem+json %s",
					rec.Code, rec.Header().Get("Content-Type"), rec.Body, safe500)
			}

			_ = assertNoted(t, r.recorded(), tt.err, "budget")
		})
	}
}

// error-chain-walk / Scenario: Budget boundary (within the budget).
func TestResolve_WalkBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "1,000 entries", err: wrapN(prblm.ErrNotFound, 999), status: 404},
		{name: "998 nil entries and the sentinel", err: &listLoopError{errs: append(make([]error, 998), prblm.ErrNotFound)}, status: 404},
		// The application's own status hint is an entry like any other.
		{name: "a status hint around 999 entries", err: prblm.WithStatus(wrapN(prblm.ErrNotFound, 998), 410), status: 410},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			if rec := writeWithin(t, prblm.NewHandler(prblm.OnError(r.hook)), tt.err); rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			assertHookCalls(t, r.recorded(), tt.err, tt.status, 0)
		})
	}
}

// error-chain-walk / Scenario: One note per error. Each hook receives the
// note, then the error; a hook that panics does not stop the others.
func TestResolve_UnwalkableHooks(t *testing.T) {
	t.Parallel()

	err := newLoop("loop")

	tests := []struct {
		name  string
		opts  func(r *recorder) []prblm.Option
		calls []string // hook name, then n for the note or e for the error
	}{
		{
			name:  "OnError only",
			opts:  func(r *recorder) []prblm.Option { return []prblm.Option{prblm.OnError(r.named("A"))} },
			calls: []string{"A:n", "A:e"},
		},
		{
			name: "OnError and an added hook",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.OnError(r.named("A")), prblm.AddErrorHook(r.named("B"))}
			},
			calls: []string{"A:n", "B:n", "A:e", "B:e"},
		},
		{
			name: "panicking OnError and an added hook",
			opts: func(r *recorder) []prblm.Option {
				return []prblm.Option{prblm.OnError(panicHook("hook broke")), prblm.AddErrorHook(r.named("B"))}
			},
			calls: []string{"B:n", "B:e"},
		},
		{name: "no hook", opts: func(*recorder) []prblm.Option { return nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			if rec := writeWithin(t, prblm.NewHandler(tt.opts(&r)...), err); rec.Code != 500 || rec.Body.String() != safe500 {
				t.Errorf("response = %d %s, want 500 %s", rec.Code, rec.Body, safe500)
			}

			var got []string

			for _, c := range r.recorded() {
				// errors.Is would loop on the error itself, so it is told
				// apart by identity first.
				kind := "?"

				switch {
				case c.err == error(err): //nolint:errorlint // the hook must receive the error value itself
					kind = "e"
				case errors.Is(c.err, prblm.ErrUnwalkableError):
					kind = "n"
				}

				if c.status != 500 {
					t.Errorf("hook %s got status %d, want 500", c.name, c.status)
				}

				got = append(got, c.name+":"+kind)
			}

			if !slices.Equal(got, tt.calls) {
				t.Errorf("hook calls = %v, want %v", got, tt.calls)
			}
		})
	}
}

// error-chain-walk / Scenario: Concurrent hostile errors. The walk keeps no
// shared state, so concurrent requests with cyclic or panicking errors get
// independent responses, under the race detector.
func TestResolve_ConcurrentHostile(t *testing.T) {
	t.Parallel()

	h := prblm.NewHandler()

	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "self-loop", err: newLoop("loop"), status: 500},
		{name: "as method panics", err: asPanicError{}, status: 500},
		{name: "wrapped sentinel", err: fmt.Errorf("x: %w", prblm.ErrNotFound), status: 404},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// A hang crashes the test binary rather than stalling the suite.
			guard := time.AfterFunc(time.Second, func() { panic(t.Name() + ": no responses within 1s") })
			defer guard.Stop()

			var wg sync.WaitGroup

			for range 64 {
				wg.Go(func() {
					if rec := write(t, h, tt.err); rec.Code != tt.status {
						t.Errorf("status = %d, want %d", rec.Code, tt.status)
					}
				})
			}

			wg.Wait()
		})
	}
}

// asPanicTextError has text secret-dsn, and its As method panics with "boom".
type asPanicTextError struct{}

func (asPanicTextError) Error() string { return "secret-dsn" }
func (asPanicTextError) As(any) bool   { panic("boom") }

// selfPanicUnwrapError has text secret-dsn, and its Unwrap method panics
// with the error itself.
type selfPanicUnwrapError struct{}

func (selfPanicUnwrapError) Error() string { return "secret-dsn" }

func (e selfPanicUnwrapError) Unwrap() error { panic(e) }

// error-chain-walk / Scenario: Note text. The note names the reason and, for
// a panic, only the type of the panic value: never the error's text, even
// when the error panics with itself, and never a method of the value.
func TestResolve_UnwalkableNote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		message string
	}{
		{name: "self-loop", err: newLoop("secret-dsn"), message: "prblm: error chain cannot be walked: budget"},
		{name: "as method panics", err: asPanicTextError{}, message: "prblm: error chain cannot be walked: panic: string"},
		{
			name:    "unwrap panics with the error itself",
			err:     selfPanicUnwrapError{},
			message: "prblm: error chain cannot be walked: panic: prblm_test.selfPanicUnwrapError",
		},
		{
			name:    "panic value that cannot be formatted",
			err:     hostileIsError{},
			message: "prblm: error chain cannot be walked: panic: *prblm_test.selfPanicError",
		},
		{name: "panic(nil)", err: nilPanicError{}, message: "prblm: error chain cannot be walked: panic: " + nilPanicType()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			writeWithin(t, prblm.NewHandler(prblm.OnError(r.hook)), tt.err)

			calls := r.recorded()
			if len(calls) == 0 {
				t.Fatal("hook not called")
			}

			if got := calls[0].err.Error(); got != tt.message {
				t.Errorf("note message = %q, want %q", got, tt.message)
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

// error-chain-walk / Scenario: What consumer code receives. The walk skips
// typed nils for matching, but a consumer MapperFunc receives the error as
// returned, the helpers never call their function with a typed nil, and a
// consumer panic is contained and reported, not an abort of the walk.
func TestResolve_ConsumerSeesOriginal(t *testing.T) {
	t.Parallel()

	joined := errors.Join((*refusedError)(nil), prblm.ErrNotFound)

	tests := []struct {
		name string
		// mapper builds the consumer mapper, which may record into probe.
		mapper func(probe *consumerProbe) prblm.Mapper
		err    error
		// received is the error the MapperFunc must have received, or nil
		// when the row does not check it.
		received error
		// panicValue checks the value of the panic error the hook receives
		// before the error, or is nil when the hook receives the error alone.
		panicValue func(any) bool
	}{
		{
			name: "mapper func records its argument",
			mapper: func(probe *consumerProbe) prblm.Mapper {
				return prblm.MapperFunc(func(_ context.Context, err error) *prblm.Problem {
					probe.received = err

					return nil
				})
			},
			err:      joined,
			received: joined,
		},
		{
			name: "as helper over a typed nil",
			mapper: func(probe *consumerProbe) prblm.Mapper {
				return prblm.As(func(*refusedError) *prblm.Problem {
					probe.called = true

					return prblm.New(409)
				})
			},
			err: joined,
		},
		{
			name: "mapper func panics",
			mapper: func(*consumerProbe) prblm.Mapper {
				return prblm.MapperFunc(func(context.Context, error) *prblm.Problem { panic("boom") })
			},
			err:        prblm.ErrNotFound,
			panicValue: isBoom,
		},
		{
			name: "mapper func calls panic nil",
			mapper: func(*consumerProbe) prblm.Mapper {
				return prblm.MapperFunc(func(context.Context, error) *prblm.Problem { panic(nilValue) })
			},
			err:        prblm.ErrNotFound,
			panicValue: isPanicNil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var (
				r     recorder
				probe consumerProbe
			)

			h := prblm.NewHandler(prblm.WithMappers(tt.mapper(&probe)), prblm.OnError(r.hook))

			if rec := writeWithin(t, h, tt.err); rec.Code != 404 {
				t.Errorf("status = %d, want 404", rec.Code)
			}

			//nolint:errorlint // the mapper must receive the returned error value itself.
			if tt.received != nil && probe.received != tt.received {
				t.Errorf("mapper received %v, want the returned error unchanged", probe.received)
			}

			if probe.called {
				t.Error("the helper's function was called")
			}

			calls := r.recorded()
			if tt.panicValue == nil {
				assertHookCalls(t, calls, tt.err, 404, 0)

				return
			}

			assertHookCalls(t, calls, tt.err, 404, 1)

			if pe, ok := errors.AsType[*prblm.PanicError](calls[0].err); ok && !tt.panicValue(pe.Value) {
				t.Errorf("panic value = %#v, not the expected one", pe.Value)
			}
		})
	}
}

// consumerProbe records what a consumer mapper saw: the error a MapperFunc
// received, and whether a helper's function was called.
type consumerProbe struct {
	received error
	called   bool
}

// codeError is an error type whose kind is int, not a pointer or an
// interface, so its zero value is a valid error.
type codeError int

func (c codeError) Error() string { return fmt.Sprintf("code %d", int(c)) }

// zeroCodeAsError's As method reports a match for a *codeError target and
// sets it to 0.
type zeroCodeAsError struct{}

func (zeroCodeAsError) Error() string { return "zero code" }

func (zeroCodeAsError) As(target any) bool {
	c, ok := target.(*codeError)
	if ok {
		*c = 0
	}

	return ok
}

// error-chain-walk / Scenario: Empty As results. An As method that reports a
// match with a nil pointer or interface target is no match, and not a panic:
// the hook receives the error alone. A zero value of another kind is a
// match, as errors.As counts it.
func TestResolve_EmptyAs(t *testing.T) {
	t.Parallel()

	refused := prblm.As(func(*refusedError) *prblm.Problem { return prblm.New(409) })
	code := prblm.As(func(codeError) *prblm.Problem { return prblm.New(409) })

	tests := []struct {
		name   string
		opts   []prblm.Option
		err    error
		status int
	}{
		{name: "zero configuration", err: emptyAsError{}, status: 500},
		{name: "built-ins disabled", opts: []prblm.Option{prblm.WithoutBuiltins()}, err: emptyAsError{}, status: 500},
		{name: "joined with a problem", err: errors.Join(emptyAsError{}, prblm.New(404)), status: 404},
		{name: "consumer as leaves a pointer nil", opts: []prblm.Option{prblm.WithMappers(refused)}, err: emptyAsError{}, status: 500},
		{name: "consumer as sets an int kind to zero", opts: []prblm.Option{prblm.WithMappers(code)}, err: zeroCodeAsError{}, status: 409},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			h := prblm.NewHandler(append(tt.opts, prblm.OnError(r.hook))...)

			if rec := writeWithin(t, h, tt.err); rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			assertHookCalls(t, r.recorded(), tt.err, tt.status, 0)
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
