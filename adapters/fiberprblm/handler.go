package fiberprblm

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/internal/errwalk"
	"github.com/kartaladev/prblm/internal/statushint"
)

// ErrorHandler returns a fiber error handler, for [fiber.Config], that
// writes every error returned by a handler or middleware, and fiber's own
// routing errors, as a problem response.
//
// opts are the options of [prblm.NewHandler], which builds the problem
// handler once, here: no options give the core's safe defaults, and an
// invalid option panics at startup rather than inside a request.
//
// The code of a [*fiber.Error] in the chain is a status hint; its message is
// never sent. A buffered body is replaced by the problem, while a body stream
// is left alone, as is the response to a client that went away (the request
// context is canceled and the error wraps [context.Canceled]); the error then
// only reaches the hooks ([prblm.OnError] and [prblm.AddErrorHook]). These
// looks into the error chain are bounded: an error whose chain cannot be
// walked gets no hint and counts as coming from a client still there. When
// the core's walk aborts too, which it does for a chain that loops or whose
// Unwrap panics, the error gets the safe 500 problem and the hooks receive
// an [prblm.ErrUnwalkableError] note first; an error whose Is or As method
// panics only for the adapter's targets is otherwise answered as usual.
//
// When opts register problem types (see [prblm.WithTypes]), ErrorHandler
// also serves their documentation, exactly as [prblm.Handler.Discovery]
// does, for GET and HEAD requests under the namespace path that reach it as
// fiber's route-not-found error. A route registered at a documentation path
// takes precedence, and a handler that returns fiber.ErrNotFound gets a 404
// problem as usual. A path that has a route only for another method gets
// fiber's 405 as a problem, not the documentation. A panic that [Recovery]
// recovered on an unrouted documentation path gets the safe 500 problem,
// even when its value wraps fiber.ErrNotFound.
func ErrorHandler(opts ...prblm.Option) fiber.ErrorHandler {
	h := prblm.NewHandler(opts...)
	// DiscoveryPath renders the documentation now, so a rendering failure
	// shows at startup.
	isDiscoveryOn := h.DiscoveryPath() != ""

	return func(c fiber.Ctx, err error) error {
		if isDiscoveryOn && isRouteNotFound(c, err) && serveDiscovery(h, c) {
			return nil
		}

		res, ok := respond(h, c, err)
		// Fiber sends nothing before the handler returns, so a buffered
		// body is replaced. A body stream is the handler's own response, and
		// a client that went away reads nothing; in both cases the error has
		// already reached the hook.
		if !ok || c.Response().IsBodyStream() || isClientGone(c, err) {
			return nil
		}

		send(c, res)

		return nil
	}
}

// respond resolves err into the problem response for c. The core this
// module requires contains panics in mappers, enrichers, the hooks and the
// error's own methods, and bounds the walk of its chain, as withHint bounds
// its own, so Respond panics only on a request context whose Value panics,
// which breaks far more than this library. The recover is the last line of
// defense all the same: should Respond panic, the response is a bare 500
// without a body, which cannot fail the same way, and the app keeps serving.
//
// A panic is told apart by Respond not returning, not by what recover
// returns, so panic(nil) under GODEBUG=panicnil=1 gets the bare 500 too.
func respond(h *prblm.Handler, c fiber.Ctx, err error) (res prblm.Response, ok bool) {
	returned := false

	defer func() {
		if !returned {
			_ = recover()
			res, ok = prblm.Response{Status: http.StatusInternalServerError}, true
		}
	}()

	res, ok = h.Respond(c.Context(), requestOf(c), withHint(err))
	returned = true

	return res, ok
}

// isClientGone reports whether err comes from the client going away: the
// request context is canceled and err wraps context.Canceled. A
// context.Canceled from elsewhere is answered like any other error.
//
// The chain is walked only once the context is done, so the common path
// pays nothing, and the walk is bounded: a chain this walk cannot finish
// counts as "not gone", and this walk reports nothing. The core then answers
// the error, with the safe 500 and a note when its own walk cannot finish
// either, as usual otherwise.
func isClientGone(c fiber.Ctx, err error) bool {
	return c.Context().Err() != nil && errwalk.New(err).Is(context.Canceled) >= 0
}

// withHint attaches the code of a [*fiber.Error] in the chain of err as a
// status hint. Only the code is used: the core never sends error text, so
// fiber's own messages ("Cannot GET /x") and handler messages stay private.
// Mappers and self-describing errors still take precedence, and a code
// outside 400–599 is ignored by the core.
//
// The hint goes to the core outside the error's chain, so it costs nothing
// of the walk's budget: an error the core could walk alone is still walked.
// The hint works as [prblm.WithStatus] around err otherwise.
//
// The walk is bounded and skips a typed-nil *fiber.Error, which carries no
// code. A chain this walk cannot finish gets no hint, and this walk reports
// nothing: the core's own walk answers it, with the safe 500 and a note when
// it cannot finish either, as usual otherwise.
func withHint(err error) error {
	if fe, i := errwalk.As[*fiber.Error](errwalk.New(err)); i >= 0 {
		return statushint.Wrap(err, fe.Code)
	}

	return err
}

// requestOf describes the request of c for [prblm.Handler.Respond]. The
// path leaves out the query string, so it never reaches the problem.
func requestOf(c fiber.Ctx) prblm.Request {
	return prblm.Request{
		Method: c.Method(),
		Path:   c.Path(),
		// Repeated Accept lines form one list (RFC 9110, section 5.3).
		Accept: string(bytes.Join(c.Request().Header.PeekAll(fiber.HeaderAccept), []byte(","))),
	}
}

// send writes res to c: its headers merged into those already set, by the
// rules of [prblm.Response.MergeInto], its status and its body.
//
// With fiber's DisableHeaderNormalizing, a handler's c.Set("cache-control",
// ...) keeps that spelling, and fasthttp looks keys up as written, so the
// current values are found by visiting every response header and comparing
// names without regard to case, whatever the configuration.
func send(c fiber.Ctx, res prblm.Response) {
	header := &c.Response().Header

	// Project the current values of the problem's header names under any
	// spelling, keyed by that spelling. MergeInto folds the spellings into
	// the canonical names, so deleting every projected key and writing the
	// merged names back leaves one spelling of each.
	current := make(http.Header, len(res.Header))
	for key, value := range header.All() {
		if k, ok := spelling(res.Header, key); ok {
			current[k] = append(current[k], string(value))
		}
	}

	// The header must not change while All visits it.
	for key := range current {
		header.Del(key)
	}

	res.MergeInto(current)

	for name, values := range current {
		for _, v := range values {
			header.Add(name, v)
		}
	}

	c.Status(res.Status)
	// Send only fails for a body stream; this body is in memory.
	_ = c.Send(res.Body)
}

// spelling returns key as a string when it spells one of the names of h,
// in any ASCII case, as the core's MergeInto folds spellings. The lengths
// must match, so a non-ASCII rune that folds to an ASCII letter (the Kelvin
// sign) never matches. A key spelled as the name itself, the common case, is
// returned as that name, so it costs no allocation.
func spelling(h http.Header, key []byte) (string, bool) {
	for name := range h {
		if len(name) != len(key) || !strings.EqualFold(name, string(key)) {
			continue
		}

		if name == string(key) {
			return name, true
		}

		return string(key), true
	}

	return "", false
}
