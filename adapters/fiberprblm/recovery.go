package fiberprblm

import (
	"net/http"
	"runtime/debug"

	"github.com/gofiber/fiber/v3"
	"github.com/kartaladev/prblm"
)

// Recovery returns fiber middleware that turns a panic in the handlers after
// it into an error returned to the application's error handler. Paired with
// [ErrorHandler], the client gets the core's safe 500 problem: status 500,
// title "Internal Server Error" and no detail, whatever the panic value,
// and the hooks ([prblm.OnError] and [prblm.AddErrorHook]) receive an error that unwraps to a
// [*prblm.PanicError] holding the value and the stack.
//
// The returned error's message is the constant "Internal Server Error", so
// even fiber's default error handler, which sends the message as the body,
// never leaks the panic value. A panic is never mapped like a returned
// error, even when its value is an error a mapper recognizes.
//
// [http.ErrAbortHandler] has no meaning in fiber, so it is recovered like
// any other panic value.
//
// A panic is told apart by the handlers not returning, not by what recover
// returns, so panic(nil) is answered and reported too under
// GODEBUG=panicnil=1, where recover returns nil.
//
// A [runtime.Goexit] in a handler (t.FailNow in a test) cannot be answered:
// fasthttp writes the response, and fiber calls the error handler, on the
// handler's goroutine once the handler returns, and Goexit ends that
// goroutine first. The client gets no answer, and the hooks hear nothing;
// the app keeps serving other requests.
//
// Recovery is opt-in; without it the library does not recover panics.
// Recovery takes no problem handler, because it renders nothing itself.
func Recovery() fiber.Handler {
	return func(c fiber.Ctx) (err error) {
		returned := false

		defer recoverPanic(&err, &returned)

		err = c.Next()
		returned = true

		return err
	}
}

// recoverPanic is deferred by Recovery: it recovers a panic in the handlers
// into *err. *returned, set once the handlers returned normally, tells a
// panic apart rather than recover's result, so panic(nil) is answered too
// under GODEBUG=panicnil=1, where recover returns nil. A runtime.Goexit also
// leaves it unset, as in the core's Recover, but nothing receives the error:
// Goexit ends the goroutine before fiber can call the error handler.
func recoverPanic(err *error, returned *bool) {
	if *returned {
		return
	}

	// The panicking frames are still on the stack while a deferred function
	// runs, so the stack shows where the panic happened.
	*err = recoveredError{pe: &prblm.PanicError{Value: recover(), Stack: debug.Stack()}}
}

// recoveredError carries a recovered panic to the error handler. Its message
// is a constant, so no error handler can send the panic value; it describes
// itself as the safe 500 problem, so no mapper ever sees the value; and the
// hook still reaches the [*prblm.PanicError] through Unwrap.
type recoveredError struct {
	pe *prblm.PanicError
}

// Error returns a constant that is safe to send to a client.
func (recoveredError) Error() string { return http.StatusText(http.StatusInternalServerError) }

// Unwrap returns the panic error.
func (e recoveredError) Unwrap() error { return e.pe }

// Problem returns the safe 500 problem.
func (recoveredError) Problem() *prblm.Problem {
	return prblm.New(http.StatusInternalServerError)
}
