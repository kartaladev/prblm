package ginprblm_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/adapters/ginprblm"
)

var (
	ErrVesselNotFound = errors.New("vessel not found")
	ErrLocked         = errors.New("locked")
)

// OutOfCredit is the type definition of the adapter scenarios.
var OutOfCredit = prblm.DefineType("out-of-credit", http.StatusForbidden, "Out of credit", "")

// hookRecorder records the errors an OnError hook receives, with the
// status of each problem (0 for a note without one).
type hookRecorder struct {
	errs     []error
	statuses []int
	mu       sync.Mutex
}

func (r *hookRecorder) option() prblm.Option {
	return prblm.OnError(func(_ context.Context, err error, p *prblm.Problem) {
		r.mu.Lock()
		defer r.mu.Unlock()

		status := 0
		if p != nil {
			status = p.Status
		}

		r.errs = append(r.errs, err)
		r.statuses = append(r.statuses, status)
	})
}

// problemStatuses returns the problem statuses the hook received, in order.
func (r *hookRecorder) problemStatuses() []int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.statuses)
}

func (r *hookRecorder) received() []error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.errs)
}

// last returns the last error the hook received, or nil.
func (r *hookRecorder) last() error {
	errs := r.received()
	if len(errs) == 0 {
		return nil
	}

	return errs[len(errs)-1]
}

// serve sends req to engine and returns the recorded response.
func serve(t *testing.T, engine *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	return rec
}

// get builds a GET request for target.
func get(t *testing.T, target string) *http.Request {
	t.Helper()

	return httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
}

// decodeProblem decodes the problem in res, failing the test when it is not
// one. It closes the body.
func decodeProblem(t *testing.T, res *http.Response) *prblm.Problem {
	t.Helper()

	p, err := readProblem(res)
	if err != nil {
		t.Fatalf("decode problem: %v", err)
	}

	return p
}

// decodeRecorded decodes the problem recorded in rec.
func decodeRecorded(t *testing.T, rec *httptest.ResponseRecorder) *prblm.Problem {
	t.Helper()

	return decodeProblem(t, rec.Result())
}

// readProblem decodes the problem document in res with encoding/json or
// encoding/xml, chosen by its Content-Type, and closes the body. It returns
// an error for any other media type. It doesn't use prblm.Parse, whose result
// type differs between core versions, so the tests build against the
// released core and the local one alike.
func readProblem(res *http.Response) (*prblm.Problem, error) {
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var unmarshal func([]byte, any) error

	contentType := res.Header.Get("Content-Type")

	switch mediaType, _, _ := mime.ParseMediaType(contentType); mediaType {
	case "application/problem+json":
		unmarshal = json.Unmarshal
	case "application/problem+xml":
		unmarshal = xml.Unmarshal
	default:
		return nil, fmt.Errorf("content type %q is not a problem document (body %q)", contentType, body)
	}

	var p prblm.Problem
	if err := unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("decode %q: %w", body, err)
	}

	return &p, nil
}

// do sends req to srv and returns the response as a recorder, with the
// body read and closed.
func do(t *testing.T, srv *httptest.Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	req.RequestURI = ""

	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	defer func() { _ = res.Body.Close() }()

	rec := httptest.NewRecorder()
	maps.Copy(rec.Header(), res.Header)
	rec.Code = res.StatusCode

	if _, err := io.Copy(rec.Body, res.Body); err != nil {
		t.Fatalf("reading body: %v", err)
	}

	return rec
}

// assertHook checks that the hook was called once with an error matching
// want, or not at all for a nil want.
func assertHook(t *testing.T, hook *hookRecorder, want error) {
	t.Helper()

	got := hook.received()

	switch {
	case want == nil && len(got) != 0:
		t.Errorf("hook received %v, want no call", got)
	case want != nil && (len(got) != 1 || !errors.Is(got[0], want)):
		t.Errorf("hook received %v, want one call with %v", got, want)
	}
}

// newEngine returns an engine that uses Middleware, with a problem handler
// built from opts, and serves handler at GET /x.
func newEngine(handler gin.HandlerFunc, opts ...prblm.Option) *gin.Engine {
	engine := gin.New()
	engine.Use(ginprblm.Middleware(opts...))
	engine.GET("/x", handler)

	return engine
}

// fetch serves engine over a real server for the test and sends GET path.
func fetch(t *testing.T, engine *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()

	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)

	return do(t, srv, get(t, srv.URL+path))
}

// assertPanicHook checks that the hook was called once per value, in order,
// each time with an error unwrapping to a panic error holding that value.
func assertPanicHook(t *testing.T, hook *hookRecorder, values ...any) {
	t.Helper()

	got := hook.received()
	if len(got) != len(values) {
		t.Fatalf("hook received %v, want %d calls", got, len(values))
	}

	for i, value := range values {
		if pe, ok := errors.AsType[*prblm.PanicError](got[i]); !ok || pe.Value != value {
			t.Errorf("hook call %d = %v, want a panic error with value %v", i, got[i], value)
		}
	}
}

// newRecoveryEngine returns an engine that uses only Recovery, built from
// opts, and serves handler at GET /x.
func newRecoveryEngine(handler gin.HandlerFunc, opts ...prblm.Option) *gin.Engine {
	engine := gin.New()
	engine.Use(ginprblm.Recovery(opts...))
	engine.GET("/x", handler)

	return engine
}

// safe500 is the body of the core's safe 500 problem for GET /v1/vessels/7.
const safe500 = `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`

// unwalkablePrefix starts the message of every ErrUnwalkableError note.
const unwalkablePrefix = "prblm: error chain cannot be walked: "

// loopError's Unwrap() error returns itself.
type loopError struct{}

func (*loopError) Error() string   { return "loop" }
func (e *loopError) Unwrap() error { return e }

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

// isPanicError is an error whose Is method panics with "boom".
type isPanicError struct{}

func (isPanicError) Error() string { return "is panics" }
func (isPanicError) Is(error) bool { panic("boom") }

// record appends err to c.Errors as c.Error does, but without the
// errors.As of c.Error, which would walk a cyclic error before the
// middleware ever saw it.
func record(c *gin.Context, err error) {
	c.Errors = append(c.Errors, &gin.Error{Err: err, Type: gin.ErrorTypePrivate})
}

// serveWithin is serve under the guard of within.
func serveWithin(t *testing.T, engine *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	return within(t, "gin", func() *httptest.ResponseRecorder { return serve(t, engine, req) })
}

// within returns the result of f, failing the test instead of crashing it
// when a panic escapes f, whatever its value. A call that takes longer than
// a second crashes the test binary: a hang must not stall the suite, and the
// runaway walk must not keep running. what names f in the failure.
func within[T any](t *testing.T, what string, f func() T) T {
	t.Helper()

	type result struct {
		value      T
		escaped    any
		isPanicked bool
	}

	done := make(chan result, 1)

	go func() {
		res := result{isPanicked: true}

		defer func() {
			if res.isPanicked {
				res.escaped = recover()
			}

			done <- res
		}()

		res.value = f()
		res.isPanicked = false
	}()

	select {
	case res := <-done:
		if res.isPanicked {
			t.Fatalf("a panic escaped %s: %v", what, res.escaped)
		}

		return res.value
	case <-time.After(time.Second):
		panic(t.Name() + ": " + what + " did not return within 1s")
	}
}

// assertSafe500 checks that rec is the safe 500 problem for GET
// /v1/vessels/7, or that nothing was written when isWritten is false. The
// body never holds "boom", the panic value of the hostile errors.
func assertSafe500(t *testing.T, rec *httptest.ResponseRecorder, isWritten bool) {
	t.Helper()

	ct, body := rec.Header().Get("Content-Type"), rec.Body.String()

	switch {
	case !isWritten && (ct != "" || body != ""):
		t.Errorf("response %q %q, want nothing written", ct, body)
	case isWritten && (rec.Code != http.StatusInternalServerError || ct != "application/problem+json" || body != safe500):
		t.Errorf("response = %d %q %s, want 500 application/problem+json %s", rec.Code, ct, body, safe500)
	}

	if strings.Contains(body, "boom") {
		t.Errorf("body %q leaks the panic", body)
	}
}

// assertNoted checks that the hook received an ErrUnwalkableError note
// naming reason, then an error last accepts, both with a 500 problem. For
// reason "panic", the note must wrap a panic error holding noteValue, and
// its message names only the type of noteValue.
//
// The hook may have received the hostile error itself, so nothing here
// walks a received error, calls its methods or formats it: failures name
// types only.
func assertNoted(t *testing.T, hook *hookRecorder, reason string, noteValue any, last func(error) bool) {
	t.Helper()

	got, statuses := hook.received(), hook.problemStatuses()
	if len(got) != 2 {
		t.Fatalf("hook received %d calls (%s), want a note, then the error", len(got), typesOf(got))
	}

	entries, isNote := noteEntries(got[0])
	if !isNote {
		t.Fatalf("first hook call is a %T, want an ErrUnwalkableError note with reason %s", got[0], reason)
	}

	// The note's message calls no method of the panic value, so formatting
	// it here is safe.
	wantMessage := unwalkablePrefix + reason
	if reason == "panic" {
		wantMessage += fmt.Sprintf(": %T", noteValue)
	}

	if msg := got[0].Error(); msg != wantMessage {
		t.Errorf("note message = %q, want %q", msg, wantMessage)
	}

	// A budget note unwraps to the sentinel alone; a panic note to the
	// sentinel, then the panic error.
	var pe *prblm.PanicError
	if len(entries) == 2 {
		pe, _ = entries[1].(*prblm.PanicError) //nolint:errorlint // the note's own entry, by construction.
	}

	switch isPanic := pe != nil; {
	case isPanic != (reason == "panic"):
		t.Errorf("note wraps a panic error: %t, want %t", isPanic, reason == "panic")
	case isPanic && pe.Value != noteValue:
		t.Errorf("note panic value = %v, want %v", pe.Value, noteValue)
	}

	if !last(got[1]) {
		t.Errorf("last hook call is a %T, want the error", got[1])
	}

	if statuses[0] != http.StatusInternalServerError || statuses[1] != http.StatusInternalServerError {
		t.Errorf("hook problem statuses = %v, want 500 for both", statuses)
	}
}

// assertHookOnce checks that the hook received exactly one error, which
// match accepts. Like assertNoted, it never walks or formats that error.
func assertHookOnce(t *testing.T, hook *hookRecorder, match func(error) bool) {
	t.Helper()

	if got := hook.received(); len(got) != 1 || !match(got[0]) {
		t.Errorf("hook received %s, want exactly one matching error", typesOf(got))
	}
}

// noteEntries returns the entries of err when it is an ErrUnwalkableError
// note, from its Unwrap() []error: ErrUnwalkableError alone for reason
// budget ("prblm: error chain cannot be walked: budget"), and
// ErrUnwalkableError, then the panic error, for reason panic ("prblm: error
// chain cannot be walked: panic: <type of the value>"). It never unwraps
// further, so a hostile error passed to the hook in its place is not walked.
func noteEntries(err error) ([]error, bool) {
	var entries []error

	switch u := err.(type) { //nolint:errorlint // one level, by construction of the note.
	case interface{ Unwrap() []error }:
		entries = u.Unwrap()
	case interface{ Unwrap() error }:
		entries = []error{u.Unwrap()}
	}

	return entries, len(entries) > 0 && entries[0] == prblm.ErrUnwalkableError //nolint:errorlint // the sentinel itself.
}

// typesOf names the dynamic types of errs, without calling any of their
// methods.
func typesOf(errs []error) string {
	names := make([]string, 0, len(errs))
	for _, err := range errs {
		names = append(names, fmt.Sprintf("%T", err))
	}

	return strings.Join(names, ", ")
}

// isPanicOf returns a matcher of an error that is, or unwraps in one step
// to, a panic error holding value. It never looks further, so a cyclic
// value is safe to match.
func isPanicOf(value any) func(error) bool {
	return func(err error) bool {
		pe, ok := err.(*prblm.PanicError) //nolint:errorlint // a bounded look: a full walk could loop.
		if u, isWrapper := err.(interface{ Unwrap() error }); !ok && isWrapper {
			pe, ok = u.Unwrap().(*prblm.PanicError) //nolint:errorlint // one step further, no more.
		}

		return ok && pe.Value == value
	}
}

// isSame returns a matcher of want itself, compared with ==, so a cyclic
// error is never walked.
func isSame(want error) func(error) bool {
	return func(err error) bool { return err == want } //nolint:errorlint // the very error, not a match in its chain.
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

// nilPanicValue is a nil panic value, so panic(nilPanicValue) is panic(nil).
var nilPanicValue any

// rerunUnderPanicNil runs the calling test again in a child process with
// GODEBUG=panicnil=1, where recover returns nil for panic(nil), and fails
// it unless the child passes. It reports true in the parent, which must then
// return, and false in the child, which runs the test body.
func rerunUnderPanicNil(t *testing.T) bool {
	t.Helper()

	if strings.Contains(os.Getenv("GODEBUG"), "panicnil=1") {
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
