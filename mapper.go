package prblm

import (
	"context"

	"github.com/kartaladev/prblm/internal/errwalk"
)

// Mapper turns an error into a problem.
//
// MapError returns nil when the mapper does not recognize err. A nil result
// is the only "no match" signal; no status code or other sentinel value is
// used for it.
type Mapper interface {
	MapError(ctx context.Context, err error) *Problem
}

// Describer is implemented by errors that know which problem they are. A
// describer in an error chain is used before any mapper; Problem may return
// nil to leave the error to the mappers.
type Describer interface {
	Problem() *Problem
}

// MapperFunc adapts a function to the [Mapper] interface.
type MapperFunc func(ctx context.Context, err error) *Problem

// MapError calls f(ctx, err).
func (f MapperFunc) MapError(ctx context.Context, err error) *Problem {
	return f(ctx, err)
}

// Status returns a mapper that maps any error matching target, as [Is]
// matches it, to a problem with the given status. It panics if target is nil.
func Status(target error, status int) Mapper {
	return Is(target, func(error) *Problem {
		return &Problem{Status: status}
	})
}

// Is returns a mapper that calls fn for any error matching target, in the
// order [errors.Is] visits the chain, including wrapped and joined errors. fn
// receives the error being resolved, as returned, and may return nil to
// decline it. Is panics if target or fn is nil, so a mistake shows up when
// the mapper is built.
//
// The mapper matches by the bounded walk the handler makes once per error,
// not by calling errors.Is: a typed nil (a nil pointer) in the chain is
// skipped without calling its methods, and a chain the walk cannot finish
// matches nothing (see [ErrUnwalkableError]). Called directly, outside a
// handler, MapError makes its own bounded walk, so it returns at once, and a
// panic in a method of the error never escapes it.
func Is(target error, fn func(err error) *Problem) Mapper {
	if target == nil {
		panic("prblm: Is called with a nil target error")
	}

	if fn == nil {
		panic("prblm: Is called with a nil function")
	}

	return walkFunc(func(_ context.Context, r resolution) *Problem {
		if r.walk.Is(target) < 0 {
			return nil
		}

		return fn(r.err)
	})
}

// As returns a mapper that calls fn with the first error in the chain of type
// T, in the order [errors.As] visits the chain, so the problem can use the
// error's data. fn may return nil to decline the error. As panics if fn is
// nil.
//
// The mapper matches by the same bounded walk as [Is], so fn is never called
// with a typed nil. An As method that reports a match but leaves a pointer or
// interface target nil is no match; a zero value of another kind, such as 0
// for an int error type, is a match, as errors.As counts it.
func As[T error](fn func(t T) *Problem) Mapper {
	if fn == nil {
		panic("prblm: As called with a nil function")
	}

	return walkFunc(func(_ context.Context, r resolution) *Problem {
		t, i := errwalk.As[T](r.walk)
		if i < 0 {
			return nil
		}

		return fn(t)
	})
}
