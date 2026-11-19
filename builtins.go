package prblm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// builtins map the package's sentinel errors, then the standard library
// errors a handler commonly returns while reading a request body. Each
// detail is a fixed sentence: offsets, field names and the error text never
// reach the client.
//
// The first match wins, so the order matters for an error with several
// causes. The package's sentinels come first: the application chose them,
// while a standard library error in the same chain is usually their cause.
// Then errors whose type identifies their source come before standard
// library sentinels that could come from anywhere.
//
// A bare io.EOF is deliberately not mapped: it comes from any reader,
// including an upstream service closing early, and a 400 would blame the
// client for a server failure. It falls to the safe 500; a consumer that
// knows its io.EOF means an empty body maps it with its own mapper.
var builtins = []Mapper{
	walkFunc(mapSentinel),
	As(func(*http.MaxBytesError) *Problem {
		return &Problem{Status: http.StatusRequestEntityTooLarge, Detail: "The request body is too large."}
	}),
	As(func(*json.UnmarshalTypeError) *Problem {
		return &Problem{Status: http.StatusUnprocessableEntity, Detail: "The request body has a value of the wrong type."}
	}),
	As(func(*json.SyntaxError) *Problem {
		return &Problem{Status: http.StatusBadRequest, Detail: "The request body is not valid JSON."}
	}),
	Is(io.ErrUnexpectedEOF, func(error) *Problem {
		return &Problem{Status: http.StatusBadRequest, Detail: "The request body is incomplete."}
	}),
	Status(context.DeadlineExceeded, http.StatusGatewayTimeout),
}

// walkMapper is a mapper that matches over the resolution the handler made
// of the error, rather than walking the chain again.
type walkMapper interface {
	mapWalk(ctx context.Context, r resolution) *Problem
}

// walkFunc adapts a function over a resolution to [Mapper] and walkMapper.
type walkFunc func(ctx context.Context, r resolution) *Problem

// MapError resolves err on its own: it walks err, within the bounds of the
// walk. The hint is left to the function that uses it (mapSentinel), so a
// helper never calls the As methods of the whole chain for a hint it ignores.
func (f walkFunc) MapError(ctx context.Context, err error) *Problem {
	return f(ctx, newResolution(err))
}

func (f walkFunc) mapWalk(ctx context.Context, r resolution) *Problem {
	return f(ctx, r)
}

// sentinel is the problem a sentinel error resolves to.
type sentinel struct {
	err    error
	status int
	detail string
}

// sentinels lists the sentinel errors with their problems, by non-descending
// status. The order only breaks a tie within one error whose Is method
// reports several sentinels: the lowest status wins.
var sentinels = []sentinel{
	{err: ErrInvalidInput, status: http.StatusBadRequest, detail: "The request is invalid."},
	{err: ErrUnauthenticated, status: http.StatusUnauthorized, detail: "Authentication is required."},
	{err: ErrPermissionDenied, status: http.StatusForbidden, detail: "You are not allowed to perform this action."},
	{err: ErrNotFound, status: http.StatusNotFound, detail: "The requested resource was not found."},
	{err: ErrConflict, status: http.StatusConflict, detail: "The request conflicts with the current state of the resource."},
	{err: ErrPreconditionFailed, status: http.StatusPreconditionFailed, detail: "A precondition of the request was not met."},
	{err: ErrRateLimited, status: http.StatusTooManyRequests, detail: "Too many requests."},
	{err: ErrBadGateway, status: http.StatusBadGateway, detail: "An upstream service returned an invalid response."},
	{err: ErrUnavailable, status: http.StatusServiceUnavailable, detail: "The service is temporarily unavailable."},
	{err: ErrUpstreamTimeout, status: http.StatusGatewayTimeout, detail: "An upstream service did not respond in time."},
}

// sentinelErrs holds the errors of sentinels, in the same order, for one
// scan of the walk.
var sentinelErrs = func() []error {
	errs := make([]error, len(sentinels))
	for i, s := range sentinels {
		errs[i] = s.err
	}

	return errs
}()

// mapSentinel maps the first sentinel found in the walk of r to an about:blank
// problem with the sentinel's status and fixed detail, or returns nil when the
// walk holds none before the status hint that would apply, which then decides
// instead. The detail never comes from the error text. The first sentinel is
// the outermost, and the earlier in sentinels on a tie.
func mapSentinel(_ context.Context, r resolution) *Problem {
	// Within a handler the hint is already known; called through MapError,
	// the sentinel mapper looks it up itself.
	r.findHint()

	at, which := r.walk.IsAny(sentinelErrs)
	if at < 0 || r.hintAt >= 0 && at >= r.hintAt {
		return nil
	}

	found := sentinels[which]

	return &Problem{Status: found.status, Detail: found.detail}
}
