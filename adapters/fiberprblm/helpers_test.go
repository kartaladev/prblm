package fiberprblm_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/adapters/fiberprblm"
	"github.com/kartaladev/prblm/internal/statushint"
)

var (
	ErrVesselNotFound = errors.New("vessel not found")
	ErrLocked         = errors.New("locked")
)

// OutOfCredit is the type definition of the adapter scenarios.
var OutOfCredit = prblm.DefineType("out-of-credit", http.StatusForbidden, "Out of credit", "")

// hookRecorder records the errors an OnError hook receives, and the status
// of the problem each came with.
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

func (r *hookRecorder) received() []error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.errs)
}

// problemStatuses returns the status of the problem of each hook call, 0
// for a call without a problem.
func (r *hookRecorder) problemStatuses() []int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.statuses)
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

// newApp returns a fiber app whose error handler is the adapter with a
// problem handler built from opts.
func newApp(opts ...prblm.Option) *fiber.App {
	return fiber.New(fiber.Config{ErrorHandler: fiberprblm.ErrorHandler(opts...)})
}

// response is a fiber response read in full.
type response struct {
	header http.Header
	body   string
	status int
}

// test sends req through app.Test and reads the response.
func test(t *testing.T, app *fiber.App, req *http.Request) response {
	t.Helper()

	r, err := do(app, req)
	if err != nil {
		t.Fatal(err)
	}

	return r
}

// do sends req through app.Test, with config if any, and reads the
// response. It returns errors rather than failing the test, so it can run
// on another goroutine.
func do(app *fiber.App, req *http.Request, config ...fiber.TestConfig) (response, error) {
	res, err := app.Test(req, config...)
	if err != nil {
		return response{}, fmt.Errorf("app.Test: %w", err)
	}

	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return response{}, fmt.Errorf("reading body: %w", err)
	}

	return response{status: res.StatusCode, header: res.Header, body: string(body)}, nil
}

// testWithin is test under the guard of within. The timeout of app.Test is
// off, so the guard of within is the only one: a hang crashes the test
// binary rather than leaving a runaway walk behind.
func testWithin(t *testing.T, app *fiber.App, req *http.Request) response {
	t.Helper()

	type result struct {
		res response
		err error
	}

	got := within(t, "fiber", func() result {
		res, err := do(app, req, fiber.TestConfig{})

		return result{res: res, err: err}
	})
	if got.err != nil {
		t.Fatal(got.err)
	}

	return got.res
}

// get builds a request for target.
func get(t *testing.T, method, target string) *http.Request {
	t.Helper()

	return httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody)
}

// problemOf decodes the problem in r, failing the test when it is not one.
func problemOf(t *testing.T, r response) *prblm.Problem {
	t.Helper()

	res := &http.Response{
		StatusCode: r.status,
		Header:     r.header,
		Body:       io.NopCloser(strings.NewReader(r.body)),
	}

	return decodeProblem(t, res)
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

// listen serves app on a real loopback listener for the test and returns a
// client for it and its base URL. The client's idle connections are closed
// before the app shuts down, which otherwise waits for them.
func listen(t *testing.T, app *fiber.App) (*http.Client, string) {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()

	client := &http.Client{Transport: &http.Transport{}}

	// Cleanups run last first: idle connections close, then the app stops.
	t.Cleanup(func() { _ = app.Shutdown() })
	t.Cleanup(client.CloseIdleConnections)

	return client, "http://" + ln.Addr().String()
}

// serveX serves handler at GET /x in an app using the adapter, with a
// problem handler built from opts, and sends one request to it.
func serveX(t *testing.T, handler fiber.Handler, opts ...prblm.Option) response {
	t.Helper()

	app := newApp(opts...)
	app.Get("/x", handler)

	return test(t, app, get(t, http.MethodGet, "/x"))
}

// fetchProblem sends GET url with client and decodes the problem. It
// returns errors rather than failing the test, so goroutines can use it.
func fetchProblem(ctx context.Context, client *http.Client, url string) (int, *prblm.Problem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, nil, err
	}

	res, err := client.Do(req) //nolint:bodyclose // readProblem closes the body.
	if err != nil {
		return 0, nil, err
	}

	p, err := readProblem(res)

	return res.StatusCode, p, err
}

// safe500 is the body of the core's safe 500 problem for GET /v1/vessels/7.
const safe500 = `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`

// unwalkablePrefix starts the message of every ErrUnwalkableError note.
const unwalkablePrefix = "prblm: error chain cannot be walked: "

// selfPanicError's Error method panics with the error itself, so fmt fails
// formatting it twice in a row and re-panics.
type selfPanicError struct{}

func (e *selfPanicError) Error() string { panic(e) }

// unformattable is the panic value of hostileIsError.
var unformattable = &selfPanicError{}

// hostileIsError's Is method panics with unformattable, a value that cannot
// be formatted.
type hostileIsError struct{}

func (hostileIsError) Error() string { return "is panics with a hostile value" }
func (hostileIsError) Is(error) bool { panic(unformattable) }

// loopError's Unwrap() error returns itself.
type loopError struct{}

func (*loopError) Error() string   { return "loop" }
func (e *loopError) Unwrap() error { return e }

// joinCycle returns an errors.Join of entries, then the join itself. It
// relies on the join's Unwrap returning its own slice.
func joinCycle(t *testing.T, entries ...error) error {
	t.Helper()

	j := errors.Join(append(entries, errors.New("leaf"))...)

	u, ok := j.(interface{ Unwrap() []error })
	if !ok {
		t.Fatal("errors.Join has no Unwrap() []error")
	}

	u.Unwrap()[len(entries)] = j

	return j
}

// isPanicError is an error whose Is method panics with "boom".
type isPanicError struct{}

func (isPanicError) Error() string { return "is panics" }
func (isPanicError) Is(error) bool { panic("boom") }

// panickyUnwrapError is an error whose Unwrap method panics with "unwrap".
type panickyUnwrapError struct{}

func (panickyUnwrapError) Error() string { return "panicky" }
func (panickyUnwrapError) Unwrap() error { panic("unwrap") }

// asPanicError is an error whose As method panics with "boom".
type asPanicError struct{}

func (asPanicError) Error() string { return "as panics" }
func (asPanicError) As(any) bool   { panic("boom") }

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

// assertSafe500 checks that r is the safe 500 problem for GET
// /v1/vessels/7, or, when isWritten is false, that the library wrote
// nothing. The body never holds "boom", the panic value of the hostile
// errors.
func assertSafe500(t *testing.T, r response, isWritten bool) {
	t.Helper()

	ct := r.header.Get("Content-Type")

	switch {
	case !isWritten && (strings.HasPrefix(ct, "application/problem") || r.body != ""):
		t.Errorf("response %d %q %q, want nothing written by the library", r.status, ct, r.body)
	case isWritten && (r.status != http.StatusInternalServerError || ct != "application/problem+json" || r.body != safe500):
		t.Errorf("response = %d %q %s, want 500 application/problem+json %s", r.status, ct, r.body, safe500)
	}

	if strings.Contains(r.body, "boom") {
		t.Errorf("body %q leaks the panic", r.body)
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
// chain cannot be walked: panic: <type of the value>"). It is only called
// on the first hook call, which the core makes a note, and never unwraps
// further.
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

// isSame returns a matcher of want itself, compared with ==, or of the
// adapter's status hint around want. Only the hint's own field is read, so
// a hostile error is never walked.
func isSame(want error) func(error) bool {
	return func(err error) bool {
		if hinted, ok := err.(*statushint.Error); ok && hinted != nil { //nolint:errorlint // the adapter's own wrapper, at the root; a nil one is a plain typed nil.
			err = hinted.Err
		}

		return err == want //nolint:errorlint // the very error, not a match in its chain.
	}
}

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

// isPanicOf returns a matcher of a panic error holding value, or of an
// error whose Unwrap() error returns one, such as Recovery's, possibly in
// the adapter's status hint. Like assertNoted, it never walks the received
// error: past the hint, one step, no more.
func isPanicOf(value any) func(error) bool {
	return func(err error) bool {
		if hinted, ok := err.(*statushint.Error); ok && hinted != nil { //nolint:errorlint // the adapter's own wrapper, at the root.
			err = hinted.Err
		}

		pe, ok := err.(*prblm.PanicError) //nolint:errorlint // a bounded look: a full walk could loop.
		if u, isWrapper := err.(interface{ Unwrap() error }); !ok && isWrapper {
			pe, ok = u.Unwrap().(*prblm.PanicError) //nolint:errorlint // one step further, no more.
		}

		return ok && pe.Value == value
	}
}
