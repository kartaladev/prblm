package prblm

import (
	"context"
	"fmt"
	"runtime/debug"
)

// PanicError is a recovered panic, reported to the hooks ([OnError] and
// [AddErrorHook]). It never reaches the client.
type PanicError struct {
	// Value is the value passed to panic. It is nil when the code called
	// panic(nil) under GODEBUG=panicnil=1, and a *runtime.PanicNilError
	// for panic(nil) otherwise.
	Value any
	// Stack is the stack trace captured where the panic was recovered,
	// which still contains the panicking frames.
	Stack []byte
}

// Error describes the panic value.
func (e *PanicError) Error() string {
	return fmt.Sprintf("prblm: recovered panic: %v", e.Value)
}

// Unwrap returns the panic value when it is an error, so [errors.Is] and
// [errors.As] see through the panic.
func (e *PanicError) Unwrap() error {
	err, _ := e.Value.(error)

	return err
}

// safeCall runs consumer code, including code that calls the Is or As methods
// of an error value, which come from the error's author. A panic in fn is
// recovered and reported to the hooks as a *PanicError, and safeCall then
// returns the zero value, so faulty consumer code cannot break the response.
// The stack is only captured when a hook will receive it.
//
// The returned flag, not recover's result, tells a panic apart, so panic(nil) is
// reported too under GODEBUG=panicnil=1, where recover returns nil: the
// report is then a *PanicError with a nil Value. A runtime.Goexit in fn also
// leaves the flag unset and is reported with a nil Value; Goexit in consumer
// code is t.FailNow in a test, so the spurious report is harmless.
func safeCall[T any](ctx context.Context, h *Handler, fn func() T) (result T) {
	returned := false

	defer func() {
		if returned {
			return
		}

		v := recover()
		if h.isReporting(ctx) {
			h.callHook(ctx, &PanicError{Value: v, Stack: debug.Stack()}, nil)
		}
	}()

	result = fn()
	returned = true

	return result
}
