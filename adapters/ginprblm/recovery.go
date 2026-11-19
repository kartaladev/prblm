package ginprblm

import (
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"github.com/kartaladev/prblm"
)

// Recovery returns gin middleware that turns a panic in the handlers after
// it into the core's safe 500 problem: status 500, title "Internal Server
// Error" and no detail, whatever the panic value, rendered by the problem
// handler like any other problem (enrichers, negotiated format, headers).
// The value never reaches the client; the hooks ([prblm.OnError] and
// [prblm.AddErrorHook]) receive a [*prblm.PanicError] holding the value and
// the stack. Recovery answers as the core's net/http [prblm.Handler.Recover]
// does, through [prblm.Handler.RespondPanic], with one difference on a
// response that has already started (see below).
//
// The panic value is walked once, with a bound. A value whose chain cannot
// be walked, because it loops back on itself or one of its methods panics,
// is answered like any other panic: the hooks first receive an
// [prblm.ErrUnwalkableError] note, then the panic. A hostile panic value
// cannot make Recovery hang or let a panic escape to gin or net/http.
//
// A panic is told apart by the handlers not returning, not by what recover
// returns, so panic(nil) is answered and reported too under
// GODEBUG=panicnil=1, where recover returns nil. A [runtime.Goexit] in a
// handler (t.FailNow in a test) does not return either, and recover cannot
// tell it from that panic(nil): it is answered the same way, as a panic with
// value nil, rather than leaving the request without an answer or a report.
// Goexit still ends the goroutine once Recovery is done, so neither gin nor
// net/http finishes the response: for a recovered value of nil, Recovery
// therefore flushes the problem it wrote. Over HTTP/1.x the client receives
// it complete. Over HTTP/2, net/http then resets the stream of the handler
// that did not return (INTERNAL_ERROR): the client receives the status, the
// headers and the problem's bytes, and then that reset, which a client may
// report as an error. Through a writer that cannot flush, the problem stays
// unsent and the client gets no answer. In every case the hooks are told.
//
// It is opt-in, and it works whether it is registered before or after
// [Middleware], or without it. It replaces gin.Recovery(); when both are
// installed, the one registered later (closer to the handler) handles the
// panic.
//
// A panic with [http.ErrAbortHandler] itself is passed on unchanged, so
// net/http aborts the response as designed; it is not reported. When the
// response has already started (a body was written, flushed or the
// connection hijacked), or the panic shows the client connection is broken
// (EPIPE, ECONNRESET), nothing is written and only the hook hears of the
// panic. On a started response the hooks receive the 500 problem that would
// have been sent, where Recover gives them a nil problem: Recovery checks
// the response after RespondPanic has answered and reported the panic.
// Should writing the problem panic in turn, the writer is broken:
// that second panic only reaches the hooks too, as an error that unwraps to
// a [*prblm.PanicError].
//
// Recovery never records the panic with c.Error, so [Middleware] never
// answers it a second time. Nested in itself, or with the core's
// [prblm.Handler.Recover] around the engine or inside a handler, it reports
// a panic once: the innermost recovery answers it. A runtime.Goexit is not
// stopped there and goes on through the outer ones, and each reports it
// again, on a response that has already started. Under GODEBUG=panicnil=1,
// recover cannot tell that Goexit from a new panic(nil) in the code between
// two recoveries, and a duplicate report is safer than a lost one.
//
// opts are the options of [prblm.NewHandler], as for [Middleware]. Pass both
// the same options, so panics and errors share one hook and one set of
// enrichers; each builds its own handler from them, which costs only a
// second copy of the configuration.
func Recovery(opts ...prblm.Option) gin.HandlerFunc {
	h := prblm.NewHandler(opts...)

	return func(c *gin.Context) {
		returned := false

		defer recoverPanic(h, c, &returned)

		c.Next()

		returned = true
	}
}

// recoverPanic is deferred by Recovery: it recovers a panic in the handlers
// and answers it. *returned, set once the handlers returned normally, tells a
// panic apart rather than recover's result, so panic(nil) is answered too
// under GODEBUG=panicnil=1, where recover returns nil. A runtime.Goexit in a
// handler also leaves it unset and is answered as a panic with value nil,
// which is better than leaving the request unanswered; the answer is then
// flushed, since gin and net/http will not finish it.
func recoverPanic(h *prblm.Handler, c *gin.Context, returned *bool) {
	if *returned {
		return
	}

	v := recover()

	// net/http aborts the response on this exact value, by design.
	//nolint:errorlint // only the sentinel itself aborts, not an error wrapping it.
	if err, ok := v.(error); ok && err == http.ErrAbortHandler {
		panic(v)
	}

	c.Abort()

	res, ok := h.RespondPanic(c.Request.Context(), requestOf(c.Request), newPanicError(v))

	// A broken connection cannot take anything, and a started response
	// cannot take a problem any more: the panic only went to the hook,
	// which for a started response received the unsent 500 problem.
	if !ok || c.Writer.Written() {
		return
	}

	written := false
	defer reportPanic(h, c.Request, &written)

	send(c.Writer, res)

	// A nil value may be a runtime.Goexit, after which neither gin nor
	// net/http finishes the response: flushed, it is sent complete, as the
	// core's Recover sends it. gin's Flush does nothing for a writer that
	// cannot flush.
	if v == nil {
		c.Writer.Flush()
	}

	written = true
}

// reportPanic is deferred around writing the problem for a panic: a writer
// that panics in turn, which *written being unset tells, panic(nil)
// included, is broken, and that second panic only goes to the hook instead
// of escaping Recovery.
func reportPanic(h *prblm.Handler, r *http.Request, written *bool) {
	if *written {
		return
	}

	v := recover()

	_, _ = respond(h, r, newPanicked(v))
}

// panickedError carries a recovered panic to the handler. It describes
// itself as the safe 500 problem, so no mapper ever sees the panic value, as
// with the core's net/http Recover, while the hook still reaches the
// [*prblm.PanicError] through Unwrap.
type panickedError struct {
	pe *prblm.PanicError
}

// newPanicked captures a recovered panic value with the current stack, as
// newPanicError does.
func newPanicked(v any) panickedError {
	return panickedError{pe: newPanicError(v)}
}

// newPanicError captures a recovered panic value with the current stack. It
// is called while a deferred function runs, when the panicking frames are
// still on the stack, so the stack shows where the panic happened.
func newPanicError(v any) *prblm.PanicError {
	return &prblm.PanicError{Value: v, Stack: debug.Stack()}
}

// Error describes the panic; it only reaches the hook.
func (e panickedError) Error() string { return e.pe.Error() }

// Unwrap returns the panic error.
func (e panickedError) Unwrap() error { return e.pe }

// Problem returns the safe 500 problem.
func (panickedError) Problem() *prblm.Problem {
	return prblm.New(http.StatusInternalServerError)
}
