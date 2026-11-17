package prblm

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
)

// maxParseBytes bounds how much of a response body Parse reads. Problem
// documents are small; the bound keeps a misbehaving server from making a
// client buffer an unbounded body.
const maxParseBytes = 1 << 20

// errTooLarge is returned by Parse for a body larger than maxParseBytes.
var errTooLarge = fmt.Errorf("prblm: problem body larger than %d bytes", maxParseBytes)

// ErrNotProblem is returned by [Parse] for a response that is not a problem
// document: its Content-Type is neither application/problem+json nor
// application/problem+xml.
var ErrNotProblem = errors.New("prblm: response is not a problem")

// Received is a problem that the program received from another service, as
// [Parse] returns it. Problem holds the decoded document.
//
// A Received is an error, but not a [Describer], and it doesn't unwrap to
// its problem: a problem that another service sent describes that service's
// failure, often with its internals in the detail and extension members, and
// relaying it would tell this service's client that its own request was
// wrong. So a gateway can return the received problem, or wrap it, and the
// handler never answers with it.
//
// A Received reports [ErrBadGateway] through [errors.Is]: another service
// answered with an error of its own, which this service could not use. With
// no configuration, an error chain that holds one resolves like that
// sentinel, to a 502 about:blank problem with the sentinel's fixed detail,
// and the sentinel rules apply at its position in the chain: an outer
// sentinel or status hint decides, consumer and scoped mappers come first,
// and [WithoutBuiltins] leaves the safe 500. The [OnError] hook receives the
// error as returned; take the upstream problem out with [errors.As] to log
// it.
//
// To relay the upstream problem as it is, detail and extension members
// included, return &r.Problem: it is a deliberate choice, made at the call
// site, to pass on what the other service wrote. A relative upstream type
// is then resolved against this service's namespace (see [WithNamespace]),
// which may not document it; resolve it against the upstream URL first if
// needed.
//
// To answer with an upstream status such as 404 but none of its content,
// register a consumer mapper for received problems that gives the
// application's own detail for an explicit list of statuses (see the
// example):
//
//	prblm.As(func(r *prblm.Received) *prblm.Problem {
//		if r == nil {
//			return nil // the default 502
//		}
//
//		// Only statuses about the resource itself: 401, 403, 405, 407 and 429
//		// describe this service's access to the upstream and stay 502.
//		switch s := r.Problem.Status; s {
//		case http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
//			return prblm.New(s, prblm.WithDetail("The vessel service rejected the request."))
//		default:
//			return nil // the default 502
//		}
//	})
type Received struct { //nolint:errname // named for where the problem came from, not for being an error
	// Problem is the problem as the other service sent it.
	Problem Problem
}

// Error returns "prblm: received problem: " followed by the held problem's
// message (see [Problem.Error]). It is safe to call on a nil Received.
func (r *Received) Error() string {
	if r == nil {
		return "<nil received problem>"
	}

	return "prblm: received problem: " + r.Problem.Error()
}

// Is reports whether target is [ErrBadGateway], whatever status the held
// problem has and also for a nil Received: another service answered with an
// error of its own, which this service could not use.
func (r *Received) Is(target error) bool {
	return target == ErrBadGateway
}

// Parse decodes the problem in res, choosing JSON or XML by its
// Content-Type, for clients and tests of services that answer with problem
// details. Decoding is lenient (see [Problem.UnmarshalJSON] and
// [Problem.UnmarshalXML]), and the problem's status is the one in the
// document, not the response status.
//
// The result is a [*Received], not a [*Problem]: a problem that another
// service sent never reaches this service's client by accident. Returning
// the received problem, or an error that wraps it, from a handler answers
// with a safe 502 (see [Received]). To relay the other service's problem as
// it is, detail and extension members included, return &r.Problem, which is
// a deliberate choice to pass its content on.
//
// Parse consumes and always closes the body. It reads at most 1 MiB: problem
// documents are small, and the bound keeps a misbehaving server from making
// the client buffer an unbounded body.
//
// It returns an error wrapping [ErrNotProblem] when the response is not a
// problem document, the read error when the body cannot be read, an error
// when the body is larger than 1 MiB, and a decode error when the body is not
// a valid document.
func Parse(res *http.Response) (*Received, error) {
	if res == nil {
		return nil, errors.New("prblm: parsing a nil response")
	}

	if res.Body == nil {
		return nil, errors.New("prblm: parsing a response without a body")
	}

	defer func() { _ = res.Body.Close() }()

	decode, err := decoderFor(res.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxParseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("prblm: reading problem: %w", err)
	}

	if len(body) > maxParseBytes {
		return nil, errTooLarge
	}

	var r Received
	if err := decode(body, &r.Problem); err != nil {
		return nil, err
	}

	return &r, nil
}

// decoderFor returns the decoder for a problem document of contentType, or
// an error wrapping ErrNotProblem.
func decoderFor(contentType string) (func([]byte, any) error, error) {
	mediaType, _, err := mime.ParseMediaType(contentType)

	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: Content-Type %q: %w", ErrNotProblem, contentType, err)
	case mediaType == mediaTypeJSON:
		return json.Unmarshal, nil
	case mediaType == mediaTypeXML:
		return xml.Unmarshal, nil
	default:
		return nil, fmt.Errorf("%w: Content-Type %q", ErrNotProblem, contentType)
	}
}
