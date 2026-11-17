// Package ginprblm renders errors in gin handlers as RFC 9457 problem
// details, with the same output as the net/http adapter of
// [github.com/kartaladev/prblm].
//
// # Quick start
//
// Install [Middleware] with the same options [prblm.NewHandler] takes, and
// record errors with c.Error, as gin handlers usually do:
//
//	engine := gin.New()
//	engine.Use(ginprblm.Middleware(prblm.WithMappers(
//		prblm.Status(ErrVesselNotFound, http.StatusNotFound),
//	)))
//	engine.GET("/v1/vessels/:id", func(c *gin.Context) {
//		vessel, err := loadVessel(c.Request.Context(), c.Param("id"))
//		if err != nil {
//			_ = c.Error(err) // written as a problem response
//			return
//		}
//		c.JSON(http.StatusOK, vessel)
//	})
//
// Once the handlers after it have returned, the middleware resolves the last
// recorded error with a problem handler built from those options, so
// mappers, enrichers, the hooks (OnError and AddErrorHook) and content
// negotiation behave exactly as with net/http.
//
// # Aborts
//
// gin's aborts send the status at once. [Middleware] holds it back, so an
// abort still gets a complete problem: c.AbortWithError(403, err) answers
// with err resolved by the handler, using 403 only when no mapper
// recognizes err, and c.AbortWithStatus(401) answers with an about:blank
// 401 problem. Responses with a body, and statuses below 400, are sent as
// written.
//
// Register [Middleware] before middleware that aborts or writes: a header
// sent before it runs can no longer be replaced.
//
// For a status-only abort, the hooks receive an error that matches
// [ErrStatusOnly] with errors.Is, so a hook can tell an expected 401 or 429
// apart from a handler error. A status of 500 or more set without a body,
// such as c.Status(503), is a status-only abort too, so a hook should still
// treat it as a failure:
//
//	prblm.OnError(func(ctx context.Context, err error, p *prblm.Problem) {
//		if p != nil && p.Status < 500 && errors.Is(err, ginprblm.ErrStatusOnly) {
//			logger.InfoContext(ctx, "request rejected", "status", p.Status)
//			return
//		}
//		logger.ErrorContext(ctx, "request failed", "err", err)
//	})
//
// gin's own answer to an unrouted request is a status-only abort as well,
// so it matches [ErrStatusOnly] too: the 404 for a path no route claims,
// and the 405 for a wrong method when engine.HandleMethodNotAllowed is on.
// No handler ran for those, so a hook should not treat every ErrStatusOnly
// below 500 as a rejection a handler made on purpose.
//
// # Route groups
//
// [Scope] attaches mappers to the routes of a group only, ahead of the
// handler's own mappers:
//
//	contracts := engine.Group("/v1/contracts", ginprblm.Scope(
//		prblm.Status(ErrLocked, http.StatusLocked),
//	))
//
// # Panic recovery
//
// Recovery is opt-in. [Recovery] turns a panic into the core's safe 500
// problem and hands the panic value and stack to the hooks. Use it
// instead of gin.Recovery(), before or after [Middleware], with the same
// options, so panics and errors share one hook and one set of enrichers:
//
//	opts := []prblm.Option{prblm.OnError(logProblem)}
//
//	engine := gin.New()
//	engine.Use(ginprblm.Recovery(opts...), ginprblm.Middleware(opts...))
//
// # Type documentation
//
// When the options register problem types with [prblm.WithTypes],
// [Middleware] serves their documentation for GET and HEAD requests under
// the namespace path that no route claims for any method, exactly as the
// core's [prblm.Handler.Discovery]. With engine.HandleMethodNotAllowed on, a
// path that has a route only for another method keeps gin's 405. A route at
// a documentation path wins, and the hooks receive a note wrapping
// [prblm.ErrShadowedDocumentation] for it, once. Install Middleware with
// engine.Use, before middleware that sets a status, so it also runs for
// unmatched paths:
//
//	engine.Use(ginprblm.Middleware(
//		prblm.WithNamespace("/v1/api/probs"),
//		prblm.WithTypes(OutOfCredit),
//	))
//
// # Safe defaults
//
// The problem semantics are the core's, unchanged: an error no mapper
// recognizes becomes a 500 problem without its text, the instance is the
// request path without the query string, and the format is JSON unless the
// client prefers XML. Should the core itself panic while resolving an error
// (for example on an error whose Unwrap method panics), the response is a
// bare 500 without a body and the engine keeps serving.
//
// A configuration mistake, such as an invalid option, is a programming
// mistake: [Middleware] and [Recovery] panic when they are created, so it
// shows at startup and never inside a request.
package ginprblm
