package ginprblm

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/internal/errwalk"
	"github.com/kartaladev/prblm/internal/statushint"
)

// Middleware returns gin middleware that writes the last error recorded with
// c.Error as a problem response once the handlers after it have returned.
//
// opts are the options of [prblm.NewHandler], which builds the problem
// handler once, here: no options give the core's safe defaults, and an
// invalid option panics at startup rather than inside a request. Pass
// [Recovery] the same options.
//
// While they run, the middleware holds back the status that gin's aborts
// would otherwise send at once, so the problem keeps full control of the
// status and Content-Type:
//
//   - c.AbortWithError(code, err) uses code as a status hint for err (see
//     [prblm.WithStatus]): a mapper or a self-describing error still takes
//     precedence, and a code below 400 is ignored.
//   - c.AbortWithStatus(code), or any status of 400 or above with no body
//     and no recorded error, becomes an about:blank problem for that status.
//   - Any other response, including statuses below 400 and every response
//     with a body, is sent as the handlers wrote it. A recorded error then
//     only reaches the hooks ([prblm.OnError] and [prblm.AddErrorHook]),
//     because appending a problem would corrupt the body. The problem they
//     receive is resolved from the error alone, not from the status the
//     client got: an error no mapper recognizes gives them a 500 problem,
//     even after a handler wrote a 200 body.
//   - When the client went away (the request context is canceled and the
//     error wraps [context.Canceled]), nothing is written and the error only
//     reaches the hooks.
//
// Register Middleware before middleware that aborts or writes: a header sent
// before it runs cannot be replaced, so the error then only reaches the hooks.
// With problem types registered, also register it before middleware that
// sets a status (c.Status): that replaces the 404 gin records for a path no
// route claims, and discovery is then not served.
//
// When opts register problem types (see [prblm.WithTypes]), Middleware also
// serves their documentation, exactly as [prblm.Handler.Discovery] does,
// for GET and HEAD requests under the namespace path that no route claims
// for any method. It must then be installed with engine.Use: gin runs global
// middleware for unmatched paths, even with a NoRoute handler, but not group
// middleware. A route registered at a documentation path takes precedence,
// and so does a route for another method when engine.HandleMethodNotAllowed
// is on: the client gets gin's 405. gin routes HEAD on its own, so with
// only a GET route at a documentation path, a HEAD there finds no route and
// gets the documentation's headers.
//
// With discovery on, Middleware also reports every request a route answers
// to [prblm.Handler.NoteShadowedRoute], with the route's pattern
// (c.FullPath()), so the hooks receive a note wrapping
// [prblm.ErrShadowedDocumentation], once per route, when a route hides
// documentation. The response is unchanged.
func Middleware(opts ...prblm.Option) gin.HandlerFunc {
	h := prblm.NewHandler(opts...)
	// DiscoveryPath renders the documentation now, so a rendering failure
	// shows at startup.
	isDiscoveryOn := h.DiscoveryPath() != ""

	return func(c *gin.Context) {
		if isDiscoveryOn && discover(h, c) {
			return
		}

		w := newDeferredWriter(c.Writer)
		c.Writer = w
		// Even when a panic unwinds through here, middleware outside this
		// one must find gin's own writer, or its status is only recorded.
		defer func() { c.Writer = w.ResponseWriter }()

		c.Next()

		finish(h, c, w)
	}
}

// discover handles c for discovery and reports whether it answered it.
// When a route answers c (c.FullPath() is its pattern), the route is
// reported to the shadowed-documentation check and c goes on. Otherwise
// discovery is served only when no route claims the path for any method:
// gin recorded 404 before running the global middleware. With
// engine.HandleMethodNotAllowed on, a path that has a route only for
// another method also has an empty FullPath, but gin recorded 405, and that
// answer stays gin's.
func discover(h *prblm.Handler, c *gin.Context) bool {
	if route := c.FullPath(); route != "" {
		// Only the method and the path are read: the headers matter only
		// to a response.
		h.NoteShadowedRoute(c.Request.Context(), route, prblm.DiscoveryRequest{
			Method: c.Request.Method,
			Path:   c.Request.URL.EscapedPath(),
		})

		return false
	}

	return c.Writer.Status() == http.StatusNotFound && serveDiscovery(h, c)
}

// serveDiscovery answers c with the core's discovery response, exactly as
// net/http would, and stops the chain. It reports false, and does nothing,
// when the request is not for discovery. The status is committed at once:
// a 304 or a HEAD response has no body, so nothing else would send it.
func serveDiscovery(h *prblm.Handler, c *gin.Context) bool {
	base := requestOf(c.Request)

	res, ok := h.RespondDiscovery(c.Request.Context(), prblm.DiscoveryRequest{
		Method: base.Method,
		Path:   c.Request.URL.EscapedPath(),
		Accept: base.Accept,
		// Repeated lines form one list (RFC 9110, section 5.3).
		IfNoneMatch: strings.Join(c.Request.Header.Values("If-None-Match"), ","),
	})
	if !ok {
		return false
	}

	send(c.Writer, res)
	c.Writer.WriteHeaderNow()
	c.Abort()

	return true
}

// ErrStatusOnly is the error the hooks receive when the middleware answers a
// status-only abort: a handler set an error status, such as with
// c.AbortWithStatus(401), and recorded no error with c.Error. No mapper
// recognizes it, so its status hint makes it an about:blank problem with the
// recorded status.
//
// The hooks receive it wrapped in that status hint, so match it with
// errors.Is, never with ==. An error a handler recorded never matches it.
//
// It also covers c.Status(500) or any other 5xx status set without a body.
// A hook that logs status-only aborts as ordinary responses (an expected 401
// or 429) should therefore still treat a problem status of 500 or more as a
// failure.
//
// gin's own answer to an unrouted request matches it too: the 404 for a
// path no route claims, and the 405 for a wrong method when
// engine.HandleMethodNotAllowed is on. No handler ran for those, so a hook
// should not treat every ErrStatusOnly below 500 as a rejection a handler
// made on purpose.
var ErrStatusOnly = errors.New("ginprblm: response aborted with an error status and no error")

// finish decides, once the handlers have returned, what the response
// becomes. The cases are tried in order.
func finish(h *prblm.Handler, c *gin.Context, w *deferredWriter) {
	last := c.Errors.Last()
	recorded := w.Status()

	switch {
	case w.isCommitted || last != nil && isClientGone(c.Request, last.Err):
		// The body started, and appending a problem would corrupt it; or
		// the client went away, and nobody is left to read one. The error
		// only reaches the hook.
		if last != nil {
			_, _ = respond(h, c.Request, last.Err)
		}
	case last != nil:
		answer(h, c.Request, w, withHint(last.Err, recorded))
	case recorded >= http.StatusBadRequest:
		answer(h, c.Request, w, withHint(ErrStatusOnly, recorded))
	default:
		w.commitNow()
	}
}

// isClientGone reports whether err comes from the client going away: the
// request context is canceled and err wraps context.Canceled. A
// context.Canceled from elsewhere, with the client still connected, is
// answered like any other error.
//
// The chain is walked only once the context is done, so the common path
// pays nothing, and the walk is bounded: a chain this walk cannot finish
// counts as "not gone", and this walk reports nothing. The core then answers
// the error, with the safe 500 and a note when its own walk cannot finish
// either, as usual otherwise.
func isClientGone(r *http.Request, err error) bool {
	return r.Context().Err() != nil && errwalk.New(err).Is(context.Canceled) >= 0
}

// withHint attaches an abort status to err as a hint, as [prblm.WithStatus]
// would: mappers and self-describing errors still take precedence, and a
// status below 400 is no hint at all. The hint goes to the core outside the
// error's chain, so it costs nothing of the walk's budget: an error the core
// could walk alone is still walked.
func withHint(err error, status int) error {
	if status < http.StatusBadRequest {
		return err
	}

	return statushint.Wrap(err, status)
}

// answer writes the problem response for err to w. When there is nothing
// to answer (a recorded error holding a nil error), the recorded response
// is committed unchanged.
func answer(h *prblm.Handler, r *http.Request, w *deferredWriter, err error) {
	res, ok := respond(h, r, err)
	if !ok {
		w.commitNow()

		return
	}

	send(w, res)
}

// respond resolves err into the problem response for r. The core this
// module requires contains panics in mappers, enrichers, the hooks and the
// error's own methods, and bounds the walk of its chain, so Respond panics
// only on a request context whose Value panics, which breaks far more than
// this library. The recover is the last line of defense all the same:
// should Respond panic, the response is a bare 500 without a body, which
// cannot fail the same way, and the server keeps serving.
//
// A panic is told apart by Respond not returning, not by what recover
// returns, so panic(nil) under GODEBUG=panicnil=1 gets the bare 500 too.
func respond(h *prblm.Handler, r *http.Request, err error) (res prblm.Response, ok bool) {
	returned := false

	defer func() {
		if !returned {
			_ = recover()
			res, ok = prblm.Response{Status: http.StatusInternalServerError}, true
		}
	}()

	res, ok = h.Respond(r.Context(), requestOf(r), err)
	returned = true

	return res, ok
}

// requestOf describes r for [prblm.Handler.Respond]. It uses the URL path, so
// the query string never reaches the problem.
func requestOf(r *http.Request) prblm.Request {
	return prblm.Request{
		Method: r.Method,
		Path:   r.URL.Path,
		// Repeated Accept lines form one list (RFC 9110, section 5.3).
		Accept: strings.Join(r.Header.Values("Accept"), ","),
	}
}

// send writes res to w: its headers merged into those already set, its
// status and its body.
func send(w http.ResponseWriter, res prblm.Response) {
	res.MergeInto(w.Header())
	w.WriteHeader(res.Status)
	// A failed write means the client is gone; there is nobody left to tell.
	_, _ = w.Write(res.Body)
}
