package prblm

import "context"

// Option configures a [Handler].
type Option func(*Handler)

// WithMappers registers consumer mappers. They run in registration order and
// the first one that returns a problem wins. Nil mappers, including a nil
// [MapperFunc], are skipped.
func WithMappers(mappers ...Mapper) Option {
	return func(h *Handler) {
		h.mappers = append(h.mappers, mappers...)
	}
}

// WithoutBuiltins disables the built-in mappers: those for the sentinel
// errors such as [ErrNotFound], for malformed, truncated and oversized
// request bodies, and for deadline errors. Without them those errors resolve
// like any other unmapped error, to their [WithStatus] hint or a 500.
func WithoutBuiltins() Option {
	return func(h *Handler) {
		h.builtins = nil
	}
}

// WithEnrichers registers functions that run, in order, on every resolved
// problem before it is written, for example to add a trace ID from the
// request context. They receive the request's own copy of the problem.
// Before them, a default enricher sets the instance to the request path
// (without the query string) when the problem has none.
func WithEnrichers(enrichers ...func(ctx context.Context, p *Problem)) Option {
	return func(h *Handler) {
		h.enrichers = append(h.enrichers, enrichers...)
	}
}

// OnError registers a hook that is called once for every error the handler
// answers, with the request context, the original error and the problem
// being sent. It is the place to log: the library never logs by itself, and
// the problem sent to the client deliberately leaves out the error text.
// The hook must not modify the problem.
//
// Before that call, the hook may also receive notes about mistakes the
// handler worked around: a [*PanicError] from consumer code that panicked
// (with a nil problem), an error wrapping [ErrInvalidStatus] for a status
// outside 400–599 that was replaced, an error wrapping [ErrInvalidType] for a
// relative type that is not a valid sub-type and was replaced by
// about:blank, or that was sent unchanged because the handler has no
// namespace, an error wrapping [ErrUndocumentedType] for a sub-type
// without a registered definition while discovery is on, or an error
// wrapping [ErrEncoding] for a problem that could not be encoded. Use
// errors.Is and errors.As to tell notes from the error being answered. The
// encoding note carries the problem that failed to encode; the call that
// follows it carries the minimal 500 problem that was sent instead.
//
// One note arrives without an answered error: an error wrapping
// [ErrShadowedDocumentation], with a nil problem, when an application route
// hides type documentation (see [Handler.NoteShadowedRoute]). The request
// it comes from succeeded, so no call follows it. A hook that panics is
// ignored.
//
// The problem p is therefore nil for some calls (the panic and the shadowed
// documentation notes), so a hook checks it before reading it.
//
// OnError sets the one replaceable hook: a later OnError replaces an earlier
// one, and OnError(nil) removes it. It is meant for the application. A
// library that wraps prblm and builds a handler from its consumer's options
// should use [AddErrorHook] instead, so that the consumer's OnError cannot
// replace the library's hook.
func OnError(hook func(ctx context.Context, err error, p *Problem)) Option {
	return func(h *Handler) {
		h.onError = hook
	}
}

// AddErrorHook adds a hook that receives the same calls as the [OnError]
// hook: every answered error and every note, with the same arguments. Added
// hooks accumulate, and OnError never replaces or removes them, so a library
// that wraps prblm uses AddErrorHook for its own hook (an access log, for
// example) and leaves OnError to its consumer. A nil hook is ignored.
//
// Each report calls the OnError hook first, if there is one, and then the
// added hooks in the order they were added, whatever the order of the
// options. The same hook added twice is called twice.
//
// Each hook is isolated: a hook that panics, whatever the value, is ignored,
// and the hooks after it are still called. Hooks run synchronously on the
// request goroutine.
//
// All the hooks of a report share the same problem, so none of them may
// modify it. Like the OnError hook, an added hook receives a nil problem
// with some reports (the panic and shadowed-documentation notes, and a
// handler panic [Handler.Recover] catches after the response started), so
// it checks p before reading it. Hooks can run during resolution, before
// the response is written (a note about a problem type, for example), so a hook that
// modifies the problem changes the response too.
func AddErrorHook(hook func(ctx context.Context, err error, p *Problem)) Option {
	return func(h *Handler) {
		if hook != nil {
			h.hooks = append(h.hooks, hook)
		}
	}
}
