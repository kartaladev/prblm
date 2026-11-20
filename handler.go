package prblm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/kartaladev/prblm/internal/errwalk"
	"github.com/kartaladev/prblm/internal/statushint"
)

// ErrInvalidStatus is wrapped by the note the hooks ([OnError] and
// [AddErrorHook]) receive when a resolved problem had a status outside 400–599
// and 500 was sent instead. It is a mistake in a mapper or describer, so a hook
// can tell it apart from the error being answered with errors.Is.
var ErrInvalidStatus = errors.New("prblm: problem status is not an error status")

// ErrUnwalkableError is wrapped by the note the hooks ([OnError] and
// [AddErrorHook]) receive when the chain of the error being answered cannot be
// walked, and the safe 500 problem was sent instead.
//
// The handler walks each error once, in the order [errors.Is] visits it, and
// examines at most 1,000 entries: the root, each wrapped error, each repeated
// visit, each nil entry and each typed nil (a nil pointer stored in an error,
// skipped without calling its methods) count. The walk aborts on a chain
// that loops back on itself, nests too deep or fans out too wide (reason
// "budget"), and on a panic, whatever its value, in an Unwrap, Is or As
// method of the error or in comparing it with a target (reason "panic").
// Such an error is not trustworthy enough to map, so the answer is the safe
// 500 problem even when a step would have recognized it: the handler fails
// closed. The bound is fixed, with no option to raise or remove it, because
// a bypass would bring the hang back.
//
// The note comes first, with the 500 problem being sent, and the error is
// then reported as usual. Its message names the reason, as in
// "prblm: error chain cannot be walked: budget", and never formats the error
// itself. For reason "panic", the message adds only the type of the panic
// value, as the %T verb prints it ("prblm: error chain cannot be walked:
// panic: *mypkg.Err"; "<nil>" for a nil value): never the value's text, so a
// method that panics with its own error cannot leak that error's text through
// the note, and formatting the note calls no method of the value and cannot
// panic. The note also wraps a [*PanicError] with the panic value and the
// panicking frames, for a hook that chooses to inspect the value with
// [errors.As]. A request answered silently, such as a documentation request,
// reports nothing.
//
// The bound protects the handler only. A hook, mapper or enricher that calls
// errors.Is or errors.As on the error itself walks the raw chain and can still
// hang on a cycle, as [errors.Is] does. Telling the note apart with errors.Is,
// as the example does, walks the note and, for reason "panic", the panic
// value when it is an error.
var ErrUnwalkableError = errors.New("prblm: error chain cannot be walked")

// Handler resolves errors into problems. Create it with [NewHandler].
type Handler struct {
	mappers []Mapper
	// builtins are the built-in mappers, removed by WithoutBuiltins.
	builtins []Mapper
	// enrichers run on every resolved problem, after defaultInstance.
	enrichers []func(ctx context.Context, p *Problem)
	// onError is the hook set with OnError, or nil.
	onError func(ctx context.Context, err error, p *Problem)
	// hooks are the non-nil hooks added with AddErrorHook, in order. They run
	// after onError.
	hooks []func(ctx context.Context, err error, p *Problem)
	// namespace is the namespace set with WithNamespace: as given until
	// NewHandler checks it, then without its trailing slash, or "" for none.
	namespace string
	// hasNamespace is set by WithNamespace, so that NewHandler checks the
	// final namespace once, even an empty one.
	hasNamespace bool
	// registered maps the sub-type of every registered definition to its
	// resolved type, built once in NewHandler, so resolving a documented
	// sub-type allocates nothing. It is also the set of documented
	// sub-types the undocumented-type note checks.
	registered map[string]string
	// unregistered keeps the resolved types of other sub-types, bounded.
	unregistered typeCache
	// definitions are the non-nil types passed to WithTypes, in order.
	definitions []*Type
	// discovery is the rendered documentation of the definitions, or nil
	// when discovery is off.
	discovery *discovery
	// isDiscoveryDisabled is set by WithoutDiscovery.
	isDiscoveryDisabled bool
}

// NewHandler returns a handler configured by opts. Nil options are ignored.
// With no options it is ready to use: built-in mappers on, the instance set
// to the request path, and a safe 500 for anything unrecognized. The handler
// does not change after NewHandler returns and is safe for concurrent use.
//
// NewHandler panics when an option is invalid: a malformed [WithNamespace],
// or [WithTypes] with an invalid definition, a duplicate sub-type or without
// a namespace. That is a
// startup misconfiguration, so it fails when the program starts rather than
// inside a request. The checks run after all options are applied, so their
// order does not matter, except that the last WithNamespace replaces earlier
// ones and is the only one checked.
func NewHandler(opts ...Option) *Handler {
	h := &Handler{builtins: builtins}

	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}

	if h.hasNamespace {
		h.namespace = mustParseNamespace(h.namespace)
	}

	h.registered = h.checkTypes()
	h.discovery = h.newDiscovery()

	return h
}

// Resolving errors: the steps that turn an error into a problem, in the order
// they run.

// Resolve turns err into a problem, or returns nil for a nil err.
//
// Resolve walks the chain of err once, in the order [errors.Is] visits it.
// Steps 1, 4 and 5 scan that walk, and so do the mappers of steps 2 and 3
// built with [Is], [As] and [Status]; other mappers receive err as returned.
// The steps run in this order, and the first one that recognizes err wins:
//
//  1. the outermost *Problem or [Describer] in the chain of err
//  2. the mappers scoped to ctx with [ScopeMappers], newest scope first
//  3. the handler's own mappers, in registration order
//  4. the built-in mappers: the sentinel errors such as [ErrNotFound], unless
//     the status hint of step 5 wraps them, then request body and deadline
//     errors (see [WithoutBuiltins])
//  5. a status hint attached with [WithStatus]
//  6. the fallback: a 500 problem that carries none of the error's text
//
// A typed nil (a nil pointer) in the chain is skipped: it never matches, and
// none of its methods is called. Consumer code still receives err as
// returned, typed nils included: a mapper, describer or enricher that panics
// counts as no match or is skipped, and the panic is reported to the hooks as
// a [*PanicError].
//
// When the walk of err cannot finish, the steps stop at once and the result
// is the step 6 fallback, even when a step would have matched, and the hooks
// receive a note first (see [ErrUnwalkableError]).
//
// The result is always a new problem that the caller may modify: a matched
// problem is copied, so problems shared between requests (for example
// package-level values) are never changed. A status outside 400–599 is replaced
// by 500, because a problem response must be an error; unless the status was
// missing, the mistake is reported to the hooks ([OnError] and [AddErrorHook]).
// An about:blank problem without a title gets the HTTP reason phrase of its
// status. Then the enrichers run (see [WithEnrichers]), and last the type is
// resolved against the namespace (see [WithNamespace]), so a type an enricher
// sets is resolved too. A type that becomes about:blank loses its title, which
// then becomes the reason phrase as well.
func (h *Handler) Resolve(ctx context.Context, err error) *Problem {
	if err == nil {
		return nil
	}

	p, abort := h.match(ctx, err)

	// A matched problem may be shared between requests, so it is copied; the
	// fallback is built fresh and needs no copy. An error whose chain cannot
	// be walked is not trustworthy enough to map: it fails closed.
	var resolved *Problem
	if p != nil && abort == nil {
		resolved = p.clone()
	} else {
		resolved = fallbackProblem()
	}

	h.complete(ctx, resolved)

	if abort != nil {
		h.noteUnwalkable(ctx, abort, resolved)
	}

	return resolved
}

// noteUnwalkable reports to the hooks why the chain of the error being
// resolved could not be walked, with p, the problem sent instead. The note
// never formats the error, nor the panic value, so the error's text cannot
// reach a log through the note, and it is built only when a hook will
// receive it.
func (h *Handler) noteUnwalkable(ctx context.Context, abort *errwalk.Abort, p *Problem) {
	if !h.isReporting(ctx) {
		return
	}

	note := &unwalkableNoteError{reason: abort.Reason}
	if abort.Reason == errwalk.ReasonPanic {
		note.panic = &PanicError{Value: abort.Value, Stack: abort.Stack}
	}

	h.callHook(ctx, note, p)
}

// unwalkableNoteError is the note reported when a walk aborts. Its message
// names the type of the panic value, never the value: a method that panics
// with its own error, such as Unwrap panicking with the receiver, would
// otherwise put the error's text in the note.
type unwalkableNoteError struct {
	reason errwalk.Reason
	// panic is the recovered panic for reason "panic", and nil otherwise.
	panic *PanicError
}

// Error names the reason and, for reason "panic", the type of the panic
// value as %T prints it, as in "prblm: error chain cannot be walked: panic:
// string", or "<nil>" for a nil value. %T calls no method of the value, so
// Error cannot panic, even for a value whose own Error method panics.
func (n *unwalkableNoteError) Error() string {
	if n.panic == nil {
		return ErrUnwalkableError.Error() + ": " + string(n.reason)
	}

	return fmt.Sprintf("%v: %s: %T", ErrUnwalkableError, n.reason, n.panic.Value)
}

// Unwrap returns ErrUnwalkableError and, for reason "panic", the panic error,
// so errors.Is and errors.As see both.
func (n *unwalkableNoteError) Unwrap() []error {
	if n.panic == nil {
		return []error{ErrUnwalkableError}
	}

	return []error{ErrUnwalkableError, n.panic}
}

// match walks err once and runs the resolution steps in order over the walk.
// It returns the problem of the first step that recognizes err, or nil when
// none does, and why the walk aborted, if it did: the steps stop at once.
func (h *Handler) match(ctx context.Context, err error) (*Problem, *errwalk.Abort) {
	r := newResolution(err)
	if r.stops(nil) {
		return nil, r.walk.Aborted()
	}

	if p := h.selfDescribed(ctx, r.walk); r.stops(p) {
		return p, r.walk.Aborted()
	}

	if p := h.firstMatch(ctx, scopedMappers(ctx), r); r.stops(p) {
		return p, r.walk.Aborted()
	}

	if p := h.firstMatch(ctx, h.mappers, r); r.stops(p) {
		return p, r.walk.Aborted()
	}

	// Looked up only now, so that an As method later in the chain cannot
	// abort a walk an earlier step already answered.
	r.findHint()

	if p := h.firstMatch(ctx, h.builtins, r); r.stops(p) {
		return p, r.walk.Aborted()
	}

	return r.hinted(), r.walk.Aborted()
}

// resolution is what the steps of one resolution share: the error as
// returned, its walk, and the status hint that applies to it, looked up once
// before the built-in mappers, which need it first. It is small and passed by
// value, so sharing it allocates nothing.
type resolution struct {
	err  error
	walk *errwalk.Walk
	// hintStatus is the status of the hint that decides when nothing else
	// recognizes the error, and hintAt is its index in walk, or -1 when no
	// hint applies; an adapter's hint, outside every entry, is at 0.
	hintStatus int
	hintAt     int
	// isHintKnown is set once the hint is known: at once for an adapter's
	// hint, and otherwise once findHint looked it up.
	isHintKnown bool
}

// newResolution starts the resolution of err: it walks err once. An
// adapter's hint at the root of err is not an entry: the error inside it is
// walked, so the hint costs nothing of the budget, and the hint, the
// outermost one, is known at once. A nil *statushint.Error is no hint: it
// is an ordinary typed-nil root, which gives an empty walk. Any other hint
// is looked up later.
func newResolution(err error) resolution {
	r := resolution{err: err, hintAt: -1}

	walked := err
	if outer, ok := err.(*statushint.Error); ok && outer != nil { //nolint:errorlint // only the root, where the adapters put it.
		walked, r.isHintKnown = outer.Err, true
		r.setHint(outer.Status, 0)
	}

	r.walk = errwalk.New(walked)

	return r
}

// stops reports whether resolution ends after a step that returned p: it
// recognized the error, or the walk aborted.
func (r resolution) stops(p *Problem) bool {
	return p != nil || r.walk.Aborted() != nil
}

// findHint looks up the status hint that applies, unless it is known: the
// outermost [WithStatus] in the walk, if its status is an error status. The
// sentinel mapper stops at the same hint, so the outer of a sentinel and
// this hint decides.
func (r *resolution) findHint() {
	if r.isHintKnown {
		return
	}

	r.isHintKnown = true

	if hint, i := errwalk.As[*statusError](r.walk); i >= 0 {
		r.setHint(hint.status, i)
	}
}

// setHint makes status, at index at, the hint that applies, if it is an
// error status. Another status is no hint at all, even over an inner one.
func (r *resolution) setHint(status, at int) {
	if isErrorStatus(status) {
		r.hintStatus, r.hintAt = status, at
	}
}

// hinted returns a problem for the status hint, or nil when none applies.
func (r resolution) hinted() *Problem {
	if r.hintAt < 0 {
		return nil
	}

	return &Problem{Status: r.hintStatus}
}

// firstMatch returns the problem from the first mapper that recognizes the
// error of r, or nil when none does or its walk aborts. A walkMapper scans
// the walk; any other mapper is consumer code and receives the error as
// returned. Nil mappers are skipped, wherever they were registered, and a
// mapper that panics counts as no match.
func (h *Handler) firstMatch(ctx context.Context, mappers []Mapper, r resolution) *Problem {
	for _, m := range mappers {
		if isNilMapper(m) {
			continue
		}

		p := safeCall(ctx, h, func() *Problem {
			if wm, ok := m.(walkMapper); ok {
				return wm.mapWalk(ctx, r)
			}

			return m.MapError(ctx, r.err)
		})

		if r.walk.Aborted() != nil {
			return nil
		}

		if p != nil {
			return p
		}
	}

	return nil
}

// complete applies the steps every problem goes through after it is
// matched: status normalization, title finalization, the enrichers and type
// resolution. Types are resolved after the enrichers, so a type an enricher
// sets is resolved too, and the title is finalized again at the end, for a
// problem the enrichers or type resolution left as about:blank without one.
// Enrichers see the title already finalized.
func (h *Handler) complete(ctx context.Context, p *Problem) {
	h.normalize(ctx, p)
	p.finalize()
	h.enrich(ctx, p)
	h.resolveType(ctx, p)
	p.finalize()
}

// normalize replaces a status outside 400–599 with 500. A missing status is
// an ordinary default; any other is a mistake, reported to the hooks.
func (h *Handler) normalize(ctx context.Context, p *Problem) {
	if isErrorStatus(p.Status) {
		return
	}

	invalid := p.Status
	p.Status = http.StatusInternalServerError

	if invalid != 0 {
		h.report(ctx, fmt.Errorf("%w: %d, sent 500 instead", ErrInvalidStatus, invalid), p)
	}
}

// enrich runs the default instance enricher and then the consumer's
// enrichers, in order, on p.
func (h *Handler) enrich(ctx context.Context, p *Problem) {
	defaultInstance(ctx, p)

	for _, enrich := range h.enrichers {
		if enrich == nil {
			continue
		}

		// A panicking enricher is skipped; changes it made before panicking
		// stay.
		safeCall(ctx, h, func() any { enrich(ctx, p); return nil })
	}
}

// resolveType turns the relative sub-type of p into a type under the
// handler's namespace. An empty type, about:blank, an absolute URI such as a
// urn: and an absolute path are kept. Any other relative type is a mistake:
// it becomes about:blank, its title is cleared so that it becomes the status
// phrase, and it is reported to the hook with a note wrapping
// ErrInvalidType. Without a namespace, every type is kept, and a relative
// one is reported with the same note, since it leads nowhere. With discovery
// on, a resolved sub-type without a definition is reported with a note
// wrapping ErrUndocumentedType, because its type URL leads to a 404.
//
// Only the configured namespace is used, never the request's Host header,
// which a client controls.
func (h *Handler) resolveType(ctx context.Context, p *Problem) {
	if isKeptType(p.Type) {
		return
	}

	if h.namespace == "" {
		// Passed through, as before namespaces existed and as RFC 9457
		// allows, but a client resolves it against the request URL.
		h.noteType(ctx, ErrInvalidType, p.Type, p)

		return
	}

	if typ, ok := h.registered[p.Type]; ok {
		p.Type = typ

		return
	}

	if isSubtype(p.Type) {
		subtype := p.Type
		p.Type = h.unregistered.resolve(h.namespace, subtype)

		if h.discovery != nil {
			h.noteType(ctx, ErrUndocumentedType, subtype, p)
		}

		return
	}

	// A title written for the lost type would now describe about:blank, so
	// it goes too, and the second finalize sets the status phrase, as RFC
	// 9457 (section 4.2.1) asks for about:blank.
	given := p.Type
	p.Type, p.Title = "", ""

	h.noteType(ctx, ErrInvalidType, given, p)
}

// noteType reports a type mistake to the hook as a note wrapping sentinel
// and naming typ. The note is built only when there is a hook to receive
// it, so resolving a type allocates nothing for notes nobody reads.
func (h *Handler) noteType(ctx context.Context, sentinel error, typ string, p *Problem) {
	if !h.isReporting(ctx) {
		return
	}

	h.callHook(ctx, fmt.Errorf("%w: %q", sentinel, typ), p)
}

// report calls the hooks (the OnError hook, then the added ones), if there
// are any and unless ctx is silenced. A hook that panics is ignored: there
// is nowhere left to report it, and the response must still go out.
func (h *Handler) report(ctx context.Context, err error, p *Problem) {
	if h.isReporting(ctx) {
		h.callHook(ctx, err, p)
	}
}

// isReporting reports whether a report in ctx reaches a hook: there is one,
// and the request is not answered silently (see silentRequest). Callers
// check it before building a report, so nobody pays for one that nobody
// reads.
func (h *Handler) isReporting(ctx context.Context) bool {
	return (h.onError != nil || len(h.hooks) > 0) && !isSilent(ctx)
}

// callHook calls the OnError hook, if any, and then the added hooks, in
// order. The caller has checked isReporting. A hook that panics is ignored,
// and the hooks after it are still called.
func (h *Handler) callHook(ctx context.Context, err error, p *Problem) {
	if h.onError != nil {
		callOne(ctx, h.onError, err, p)
	}

	for _, hook := range h.hooks {
		callOne(ctx, hook, err, p)
	}
}

// callOne calls one hook with the report and ignores its panic, whatever
// the value, http.ErrAbortHandler included: a hook runs outside the
// application's control flow, so it cannot abort the response, and there is
// nowhere left to report the panic.
func callOne(ctx context.Context, hook func(context.Context, error, *Problem), err error, p *Problem) {
	defer func() { _ = recover() }()

	hook(ctx, err, p)
}

// Responding: turning a resolved problem into a response.

// Respond turns err into a complete problem response without writing it, so
// any framework adapter can deliver the same output. It reports false when
// there is nothing to write, which is only the case for a nil err. A request
// whose context was canceled is answered like any other: a client that is
// still connected gets the problem, and the server drops a write to one that
// went away.
//
// The format follows req.Accept: application/problem+xml for a client that
// prefers XML (application/problem+xml, application/xml or text/xml), and
// application/problem+json otherwise, including when the header accepts
// neither or is malformed, so a client always gets the status and a document
// rather than a 406.
//
// The body is encoded in full before anything is returned, so a problem that
// cannot be encoded (for example an extension holding a channel or an
// infinite number, or a name XML cannot carry) is never sent in part: the
// response becomes the constant minimal JSON problem
// {"title":"Internal Server Error","status":500}, whatever the format, and
// the failure goes to the hooks ([OnError] and [AddErrorHook]).
func (h *Handler) Respond(ctx context.Context, req Request, err error) (Response, bool) {
	if err == nil {
		return Response{}, false
	}

	ctx = withRequest(ctx, req)

	return h.respond(ctx, req, err, h.Resolve(ctx, err)), true
}

// RespondPanic turns a recovered panic into a complete problem response
// without writing it, so a framework adapter answers panics as
// [Handler.Recover] does. The adapter recovers the panic, wraps the value in
// a [*PanicError] (capturing the stack in its deferred function, where the
// panicking frames are still on it), and writes the response when the second
// result is true. A nil pe is answered as a panic with value nil.
//
// The problem is the safe fallback, whatever the panic value: status 500,
// title "Internal Server Error" and no detail, completed by the enrichers and
// rendered like any other problem in the format req.Accept negotiates. The
// value never reaches the client; the hooks ([OnError] and [AddErrorHook])
// receive pe with the problem being sent.
//
// RespondPanic reports false, and the hooks receive pe with a nil problem,
// when the panic shows the client connection is broken (EPIPE or
// ECONNRESET, on every platform but Plan 9): nothing can be written to it.
//
// The chain of pe is walked once, as the handler walks a returned error (see
// [ErrUnwalkableError]). A value whose chain loops back on itself, or whose
// methods panic, is answered with the safe 500 like any other: the hooks
// first receive an [ErrUnwalkableError] note, then pe. A hostile panic value
// cannot make RespondPanic panic or hang, and a panicking enricher or hook is
// contained as everywhere else.
//
// It does not handle [http.ErrAbortHandler], which the adapter passes on
// before calling RespondPanic. Nor does it know whether the response has
// already started: an adapter decides that, before or after calling it, and
// writes nothing then. An adapter that checks after, as ginprblm does,
// leaves the hooks with the 500 problem that was not sent, where
// [Handler.Recover], which checks before, gives them a nil problem.
//
// RespondPanic panics when h is nil.
func (h *Handler) RespondPanic(ctx context.Context, req Request, pe *PanicError) (Response, bool) {
	h.mustExist("RespondPanic")

	return h.respondPanic(withRequest(ctx, req), req, pe)
}

// respondPanic is RespondPanic for a ctx that already carries req, as
// recoverPanic's does.
func (h *Handler) respondPanic(ctx context.Context, req Request, pe *PanicError) (Response, bool) {
	if pe == nil {
		pe = &PanicError{}
	}

	w := errwalk.New(pe)
	if isBroken(w) {
		h.report(ctx, pe, nil)

		return Response{}, false
	}

	// Whatever its value, a panic gets the safe 500 problem. The note, if
	// any, comes before rendering, as in Resolve, so it precedes any
	// encoding note and the report of pe.
	p := fallbackProblem()
	h.complete(ctx, p)

	if abort := w.Aborted(); abort != nil {
		h.noteUnwalkable(ctx, abort, p)
	}

	return h.respond(ctx, req, pe, p), true
}

// isBroken reports whether w shows that the client connection is gone: an
// entry matches one of brokenConnErrors. A walk that aborts matches nothing.
func isBroken(w *errwalk.Walk) bool {
	return slices.ContainsFunc(brokenConnErrors, func(target error) bool { return w.Is(target) >= 0 })
}

// respond renders p, the problem resolved for err, as the response for req:
// it negotiates the format, encodes p (falling back to the minimal problem)
// and reports err to the hooks with the problem being sent.
func (h *Handler) respond(ctx context.Context, req Request, err error, p *Problem) Response {
	res, sent := h.render(ctx, req, p)
	h.report(ctx, err, sent)

	return res
}

// render renders p as the response for req: it negotiates the format and
// encodes p, falling back to the minimal problem when that fails. It
// returns the response and the problem it carries.
func (h *Handler) render(ctx context.Context, req Request, p *Problem) (Response, *Problem) {
	f := negotiateFormat(req.Accept)

	body, ok := h.encode(ctx, f, p)
	if !ok {
		// Nothing of p is sent, not even its headers: the minimal problem is
		// a constant JSON document, so it cannot fail too.
		f, body = &jsonFormat, []byte(fallbackBody)
		p = fallbackProblem()
		p.finalize()
	}

	return newResponse(p.Status, p.Header, p.replaced, f.mediaType, body), p
}

// encode encodes p in format f and reports whether it succeeded. A failure,
// including a panic in the encoder of an extension value, is reported to the
// hooks, so the response can fall back to the minimal problem without
// losing the reason.
func (h *Handler) encode(ctx context.Context, f *format, p *Problem) ([]byte, bool) {
	var (
		body []byte
		err  error
	)

	// safeCall returns false when the encoder panicked, after reporting it.
	if completed := safeCall(ctx, h, func() bool { body, err = f.encode(p); return true }); !completed {
		return nil, false
	}

	if err != nil {
		h.report(ctx, fmt.Errorf("%w: %w", ErrEncoding, err), p)

		return nil, false
	}

	return body, true
}

// The net/http adapter.

// Wrap turns a handler function that returns an error into an [http.Handler].
// A non-nil error is written as a problem response; a nil error leaves the
// response as fn wrote it.
//
// Wrap watches what fn writes. When fn has already started the response (it
// wrote a status other than 1xx, wrote body bytes, flushed or hijacked the
// connection), the error is still reported to the hooks ([OnError] and
// [AddErrorHook]) but nothing more is written: the client has the start of
// another response, and appending a problem would corrupt it. Headers fn set
// without writing are kept, except those the problem response controls.
//
// Wrap panics when h or fn is nil, so the mistake shows at startup rather
// than on the first request.
func (h *Handler) Wrap(fn func(http.ResponseWriter, *http.Request) error) http.Handler {
	h.mustExist("Wrap")

	if fn == nil {
		panic("prblm: Handler.Wrap called with a nil handler function")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := track(w)
		h.answer(tw, r, fn(tw, r))
	})
}

// Write writes err as a problem response to w, for handlers that keep the
// standard signature. A nil err writes nothing.
//
// Write cannot see what was written to w before it was called, so it assumes
// nothing was, unless w comes from [Handler.Wrap]: then a response that has
// already started is left alone, and err only reaches the hooks ([OnError] and
// [AddErrorHook]).
//
// Write panics when h is nil.
func (h *Handler) Write(w http.ResponseWriter, r *http.Request, err error) {
	h.mustExist("Write")
	h.answer(track(w), r, err)
}

// answer writes the problem response for err to tw, unless tw has already
// started its response; the error reaches the hooks even then. The
// response goes through tw, which marks it started, so an error is never
// answered twice.
func (h *Handler) answer(tw *trackingWriter, r *http.Request, err error) {
	res, ok := h.Respond(r.Context(), requestOf(r), err)
	if !ok || tw.started {
		return
	}

	tw.send(res)
}

// Recover returns middleware that turns a panic in next into a 500 problem
// response. It is opt-in: [Handler.Wrap] alone lets panics reach net/http,
// so existing recovery middleware keeps working.
//
// The problem is the safe fallback, whatever the panic value: status 500, title
// "Internal Server Error" and no detail, rendered like any other problem
// (enrichers, negotiated format, headers). The value never reaches the client;
// the hooks ([OnError] and [AddErrorHook]) receive it as a [*PanicError].
//
// A panic with [http.ErrAbortHandler] itself is passed on unchanged, so
// net/http aborts the response as designed; it is not reported. When the
// handler has already started the response, or the panic shows the client
// connection is broken (EPIPE, ECONNRESET), nothing is written and the hooks
// receive the panic with a nil problem.
//
// The answer is the one [Handler.RespondPanic] gives, so adapters built on it
// answer panics the same way. A panic value whose chain cannot be walked
// (its methods panic, or it loops back on itself) still gets the safe 500
// problem, and the hooks first receive an [ErrUnwalkableError] note: no
// panic escapes Recover, and the request does not hang.
//
// A panic is told apart by next not returning, not by what recover returns,
// so panic(nil) is answered and reported too under GODEBUG=panicnil=1, where
// recover returns nil. A [runtime.Goexit] in next (t.FailNow in a test) does
// not return either, and recover cannot tell it from that panic(nil): it is
// answered the same way, as a panic with value nil, rather than leaving the
// request without an answer or a report. Goexit still ends the goroutine
// once Recover is done, so net/http never finishes the response: for a
// recovered value of nil, Recover therefore flushes the problem it wrote.
// Over HTTP/1.x the client receives it complete. Over HTTP/2, net/http then
// resets the stream of the handler that did not return (INTERNAL_ERROR):
// the client receives the status, the headers and the problem's bytes, and
// then that reset, which a client may report as an error. Through a writer
// that cannot flush (see [http.ResponseController]), the problem stays
// unsent and the client gets no answer. In every case the hooks are told.
//
// Enrichers and the hooks see the context of the request Recover received,
// not values that middleware inside it added before the panic: install
// Recover inside middleware whose context values they need.
//
// Recover installs the same tracking writer as [Handler.Wrap], and Wrap
// reuses it, so Recover(h.Wrap(fn)) keeps one record of what was written.
// Nested Recovers report a panic once: the innermost answers it. A
// runtime.Goexit is not stopped there and goes on through the outer ones,
// and each reports it again, with a nil problem since the response has
// started. Under GODEBUG=panicnil=1, recover cannot tell that Goexit from a
// new panic(nil) in the code between two Recovers, and a duplicate report
// is safer than a lost one.
//
// Recover panics when h or next is nil.
func (h *Handler) Recover(next http.Handler) http.Handler {
	h.mustExist("Recover")

	if next == nil {
		panic("prblm: Handler.Recover called with a nil next handler")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := track(w)
		returned := false

		defer h.recoverPanic(tw, r, &returned)

		next.ServeHTTP(tw, r)

		returned = true
	})
}

// recoverPanic is deferred by Recover: it recovers a panic in the handler
// and answers it on tw. *returned, set once the handler returned normally,
// tells a panic apart rather than recover's result, so panic(nil) is
// answered too under GODEBUG=panicnil=1, where recover returns nil. A
// runtime.Goexit in the handler also leaves it unset and is answered as a
// panic with value nil, which is better than leaving the request unanswered;
// the answer is then flushed, since net/http will not finish it.
func (h *Handler) recoverPanic(tw *trackingWriter, r *http.Request, returned *bool) {
	if *returned {
		return
	}

	v := recover()

	// net/http aborts the response on this exact value, by design.
	//nolint:errorlint // only the sentinel itself aborts, not an error wrapping it.
	if err, ok := v.(error); ok && err == http.ErrAbortHandler {
		panic(v)
	}

	// The panicking frames are still on the stack while a deferred function
	// runs, so the stack shows where the panic happened.
	pe := &PanicError{Value: v, Stack: debug.Stack()}

	// A started response cannot take a problem any more: the panic only goes
	// to the hook.
	req := requestOf(r)
	ctx := withRequest(r.Context(), req)

	if tw.started {
		h.report(ctx, pe, nil)

		return
	}

	// A panic while answering is a bug in the library or in the writer; the
	// constant problem cannot fail the same way.
	answered := false
	defer h.writeFallback(ctx, tw, &answered)

	if res, ok := h.respondPanic(ctx, req, pe); ok {
		tw.send(res)

		// A nil value may be a runtime.Goexit rather than panic(nil) under
		// GODEBUG=panicnil=1. Goexit goes on ending the goroutine once this
		// answer is done, so net/http never finishes the response, and the
		// problem would stay in its buffer. Flushing sends it complete: it
		// carries its Content-Length. A writer that cannot flush has no
		// other way to send it, and the error has nobody to go to.
		if v == nil {
			_ = tw.FlushError()
		}
	}

	answered = true
}

// writeFallback is deferred while a panic is answered: when answering panics
// too, which *answered being unset tells, panic(nil) included, it writes the
// constant minimal problem if nothing was written yet. It never panics
// itself.
func (h *Handler) writeFallback(ctx context.Context, tw *trackingWriter, answered *bool) {
	if *answered {
		return
	}

	v := recover()

	// report already contains a panicking hook; this guards the write,
	// should the writer itself panic again.
	defer func() { _ = recover() }()

	h.report(ctx, &PanicError{Value: v, Stack: debug.Stack()}, nil)

	if tw.started {
		return
	}

	tw.send(newResponse(http.StatusInternalServerError, nil, nil, mediaTypeJSON, []byte(fallbackBody)))
}

// mustExist panics when h is nil, naming the method, so that a missing
// NewHandler call fails where the mistake is rather than inside a request.
func (h *Handler) mustExist(method string) {
	if h == nil {
		panic("prblm: Handler." + method + " called on a nil problem handler; create it with NewHandler")
	}
}

// Problem types: checking the registered definitions and serving their
// documentation.

// checkTypes checks the definitions passed to WithTypes, once all options
// are applied, and returns the resolved type of each, by sub-type, or nil
// when none is registered. It panics on a missing namespace, an invalid
// definition or a duplicate sub-type. Every definition is checked again as
// DefineType checks it, because a Type such as &Type{} can be built without
// it.
func (h *Handler) checkTypes() map[string]string {
	if len(h.definitions) == 0 {
		return nil
	}

	if h.namespace == "" {
		panic("prblm: WithTypes: types need WithNamespace")
	}

	resolved := make(map[string]string, len(h.definitions))

	for _, t := range h.definitions {
		if err := validateType(t.subtype, t.status, t.title); err != nil {
			panic("prblm: WithTypes: invalid definition: " + err.Error())
		}

		if _, ok := resolved[t.subtype]; ok {
			panic(fmt.Sprintf("prblm: WithTypes: duplicate sub-type %q", t.subtype))
		}

		resolved[t.subtype] = h.namespace + "/" + t.subtype
	}

	return resolved
}

// newDiscovery returns the discovery of the handler's types, or nil when
// discovery is off. The pages are rendered on first use, so a handler that
// never serves discovery, such as the one gin's Recovery builds, never
// renders them.
func (h *Handler) newDiscovery() *discovery {
	if len(h.definitions) == 0 || h.isDiscoveryDisabled {
		return nil
	}

	ns, definitions := h.namespace, h.definitions

	d := newLazyDiscovery(namespacePath(ns), func() site { return renderSite(ns, definitions) })
	d.documented = h.registered

	return d
}

// renderSite renders the documentation of definitions under the namespace
// ns: a page per type and the index, sorted by sub-type.
func renderSite(ns string, definitions []*Type) site {
	sorted := slices.SortedFunc(slices.Values(definitions), func(a, b *Type) int {
		return cmp.Compare(a.subtype, b.subtype)
	})

	docs := make(map[string]pages, len(sorted))
	index := indexDoc{Types: make([]indexEntry, 0, len(sorted))}

	for _, t := range sorted {
		doc := typeDoc{
			Type:        ns + "/" + t.subtype,
			Subtype:     t.subtype,
			Status:      t.status,
			Title:       t.title,
			Description: t.description,
		}
		docs[t.subtype] = pages{html: htmlPage(t.subtype, docTemplate, doc), json: jsonPage(t.subtype, doc)}
		index.Types = append(index.Types, indexEntry{Type: doc.Type, Subtype: doc.Subtype, Status: doc.Status, Title: doc.Title})
	}

	return site{
		docs:  docs,
		index: pages{html: htmlPage("index", indexTemplate, index), json: jsonPage("index", index)},
	}
}

// DiscoveryPath returns the path the documentation of the registered types
// is served under, which is the path of the namespace, or "" when discovery
// is off (no types registered, or [WithoutDiscovery]). Mount
// [Handler.Discovery] there:
//
//	if path := h.DiscoveryPath(); path != "" {
//		mux.Handle("GET "+path+"/", h.Discovery())
//	}
//
// DiscoveryPath panics when h is nil.
func (h *Handler) DiscoveryPath() string {
	h.mustExist("DiscoveryPath")

	if h.discovery == nil {
		return ""
	}

	// Every adapter and every net/http mount calls DiscoveryPath when it is
	// set up, so rendering here makes a rendering failure surface at
	// startup.
	h.discovery.site()

	return h.discovery.path
}

// Discovery returns an [http.Handler] that serves the documentation of the
// registered types at their type URLs, so a client can dereference a
// problem's type, as RFC 9457 recommends. It is never nil. It answers with
// [Handler.RespondDiscovery], and otherwise with a problem: with discovery
// off, a 404 to every request; with discovery on, a 405 with Allow: GET,
// HEAD to any method other than GET and HEAD, and a 404 to a GET or HEAD
// outside the discovery path. It is meant to be mounted at the discovery
// path, where every request is for it.
//
// Discovery panics when h is nil.
func (h *Handler) Discovery() http.Handler {
	h.mustExist("Discovery")

	// Rendered here, when the handler is mounted, so a rendering failure
	// surfaces at startup rather than on the first request.
	if h.discovery != nil {
		h.discovery.site()
	}

	return http.HandlerFunc(h.serveDiscovery)
}

// serveDiscovery serves one discovery request over net/http.
func (h *Handler) serveDiscovery(w http.ResponseWriter, r *http.Request) {
	base := requestOf(r)
	req := DiscoveryRequest{
		Method:      base.Method,
		Path:        r.URL.EscapedPath(),
		Accept:      base.Accept,
		IfNoneMatch: strings.Join(r.Header.Values("If-None-Match"), ","),
	}

	res, ok := h.RespondDiscovery(r.Context(), req)
	if !ok {
		var p *Problem
		if h.discovery == nil || isReadMethod(r.Method) {
			p = New(http.StatusNotFound)
		} else {
			p = New(http.StatusMethodNotAllowed, WithHeader("Allow", "GET, HEAD"))
		}

		res = headless(req.Method, h.discoveryProblem(r.Context(), req, p))
	}

	track(w).send(res)
}

// RespondDiscovery returns the discovery response to req without writing
// it, so every framework adapter serves discovery by the same rules, and
// sends the same bytes as [Handler.Discovery]. It reports false when req is
// not for discovery: discovery is off, the method is not GET or HEAD, or
// req.Path is neither the discovery path (see [Handler.DiscoveryPath]),
// that path with a trailing "/", nor under it. The adapter then handles the
// request as usual.
//
// Otherwise the response is the documentation or the index, negotiated by
// req.Accept, a 304 when req.IfNoneMatch matches its ETag, or a 404 problem for
// a path that names no registered type. The path is matched exactly in its
// escaped form, so a sub-type spelled with escapes ("%2D"), a malformed escape
// ("%zz") or a traversal ("%2e%2e") is a 404, and every page has exactly one
// URL. For HEAD the body is empty and the headers are those of the GET. The
// problems discovery sends never reach the hooks ([OnError] and
// [AddErrorHook]), because no application error occurred.
//
// RespondDiscovery panics when h is nil.
func (h *Handler) RespondDiscovery(ctx context.Context, req DiscoveryRequest) (Response, bool) {
	h.mustExist("RespondDiscovery")

	d := h.discoveryFor(req)
	if d == nil {
		return Response{}, false
	}

	var res Response

	if doc, ok := d.lookup(req.Path); ok {
		res = doc.negotiate(req.Accept).response(req.IfNoneMatch)
	} else {
		res = h.discoveryProblem(ctx, req, New(http.StatusNotFound))
	}

	return headless(req.Method, res), true
}

// NoteShadowedRoute reports that req was answered by the application route with
// the pattern route, so the hooks ([OnError] and [AddErrorHook]) learn when
// that route hides type documentation. A framework adapter calls it for every
// request a route answers, with the route's pattern (gin's c.FullPath(),
// fiber's c.Route().Path); net/http gives no such hook point. Only req.Method
// and req.Path are read, so an adapter need not read the headers.
//
// When discovery would have served a documentation page or the index for
// req, the hooks receive a note wrapping [ErrShadowedDocumentation] that
// names the route and the path, with a nil problem. Each route is noted
// once, at its first conflicting request, so one conflict cannot flood the
// log, and at most 256 routes are noted. There is no note when discovery is
// off, the method is not GET or HEAD, the path is outside the discovery
// path, or discovery would have answered it with a 404, since an unknown
// sub-type hides nothing. Without a hook nothing is built or remembered.
//
// NoteShadowedRoute panics when h is nil.
func (h *Handler) NoteShadowedRoute(ctx context.Context, route string, req DiscoveryRequest) {
	h.mustExist("NoteShadowedRoute")

	d := h.discoveryFor(req)
	if d == nil || !h.isReporting(ctx) {
		return
	}

	if !d.hasPage(req.Path) || !d.noted.add(route) {
		return
	}

	h.callHook(ctx, fmt.Errorf("%w: route %q hides %q", ErrShadowedDocumentation, route, req.Path), nil)
}

// discoveryFor returns the discovery that req is for, or nil when it is
// not for discovery: discovery is off, the method is not GET or HEAD, or
// the path is outside the discovery path. It is the one rule every adapter
// relies on, through RespondDiscovery and NoteShadowedRoute.
func (h *Handler) discoveryFor(req DiscoveryRequest) *discovery {
	d := h.discovery
	if d == nil || !isReadMethod(req.Method) || !d.isUnder(req.Path) {
		return nil
	}

	return d
}

// discoveryProblem renders p as the answer to the discovery request req,
// completed and rendered like any other problem, but answered silently:
// no hook is called, not even for a note about a faulty
// enricher or a failed encoding, because no application error occurred.
// The instance is the path as the client sent it.
func (h *Handler) discoveryProblem(ctx context.Context, req DiscoveryRequest, p *Problem) Response {
	r := Request{Method: req.Method, Path: req.Path, Accept: req.Accept}
	ctx = silentRequest(ctx, r)

	h.complete(ctx, p)
	res, _ := h.render(ctx, r, p)

	return res
}

// headless returns res as the answer to a request with method: for HEAD,
// the same headers and no body.
func headless(method string, res Response) Response {
	if method == http.MethodHead {
		res.Body = nil
	}

	return res
}

// isReadMethod reports whether method is GET or HEAD, the only methods
// discovery serves.
func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// defaultInstance sets the instance of p to the path of the request being
// answered, when p has no instance. The path excludes the query string, so
// tokens in it cannot leak.
func defaultInstance(ctx context.Context, p *Problem) {
	if p.Instance != "" {
		return
	}

	if req, ok := RequestFrom(ctx); ok {
		p.Instance = req.Path
	}
}

// isErrorStatus reports whether status is a client or server error status.
func isErrorStatus(status int) bool {
	return status >= 400 && status <= 599
}

// selfDescribed returns the problem of the outermost [Describer] in the walk
// w, or nil when there is none or it returns nil. A *Problem is a Describer
// of itself. Problem is consumer code: a panic there counts as no match.
func (h *Handler) selfDescribed(ctx context.Context, w *errwalk.Walk) *Problem {
	d, i := errwalk.As[Describer](w)
	if i < 0 {
		return nil
	}

	return safeCall(ctx, h, d.Problem)
}

// isNilMapper reports whether m is nil or a nil [MapperFunc]. A nil
// MapperFunc can never match: calling it panics, which would send a panic
// note to the hook on every request. Other typed-nil mappers are called,
// because a method on a nil pointer may be valid; a panic there is contained
// like any other.
func isNilMapper(m Mapper) bool {
	if m == nil {
		return true
	}

	f, ok := m.(MapperFunc)

	return ok && f == nil
}

// fallbackProblem is the safe answer for an error that nothing recognizes:
// a 500 with no detail, so no error text, stack or internals reach the client.
func fallbackProblem() *Problem {
	return &Problem{Status: http.StatusInternalServerError}
}
