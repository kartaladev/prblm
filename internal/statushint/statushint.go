// Package statushint carries the status hint a framework adapter takes from
// the framework, such as gin's c.AbortWithError code or the code of a
// *fiber.Error, to the core.
//
// It exists so that the hint is not an entry of the error's chain. The
// public prblm.WithStatus wraps the error, and that wrapper is an entry the
// bounded walk charges, like any error the application builds. The adapter's
// hint is the library's own, not the application's: were it charged, an
// error of exactly the budget's size would abort only because an adapter
// answered it. The core walks the Err of an *Error at the root of the chain
// instead, and applies Status as the outermost hint.
package statushint

// Error is Err with a status hint an adapter attached around it.
//
// Only an *Error at the root of the chain is a hint: the adapters pass it
// straight to the core, which never finds one deeper. A nil *Error is no
// hint but an ordinary typed nil, which the core skips. Error and Unwrap keep
// it invisible to consumer code, which receives it as the error returned.
type Error struct {
	Err    error
	Status int
}

// Wrap returns err with status as its hint, or nil for a nil err, so an
// adapter never turns "no error" into one.
func Wrap(err error, status int) error {
	if err == nil {
		return nil
	}

	return &Error{Err: err, Status: status}
}

// Error returns the message of the wrapped error unchanged.
func (e *Error) Error() string { return e.Err.Error() }

// Unwrap returns the wrapped error.
func (e *Error) Unwrap() error { return e.Err }
