package validatormap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"slices"

	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/validator/v10"

	"github.com/kartaladev/prblm"
)

// entry is one failed field in the errors extension. It is encoded through
// its json tags in both JSON and XML. At most one of Pointer and Field is
// set, and neither for a field encoding/json never decodes.
type entry struct {
	Detail  string `json:"detail"`
	Pointer string `json:"pointer,omitempty"`
	Field   string `json:"field,omitempty"`
}

// Option configures the mapper returned by [Mapper].
type Option func(*config)

// config holds the settings of a mapper.
type config struct {
	// translator picks the translator of a request, or is nil.
	translator func(ctx context.Context) ut.Translator
	typ        string
	title      string
	status     int
	// jsonNames tells that the validator reports JSON names.
	jsonNames bool
}

// WithStatus sets the status of validation problems, 422 by default. It
// must be a client error status, from 400 to 499: a validation failure is
// never the server's fault. WithStatus panics on any other status.
func WithStatus(code int) Option {
	if code < http.StatusBadRequest || code > 499 {
		panic(fmt.Sprintf("validatormap: WithStatus needs a status from 400 to 499, got %d", code))
	}

	return func(c *config) {
		c.status = code
	}
}

// WithType sets the type of validation problems, a URI reference. By
// default it is empty, which means about:blank.
func WithType(uri string) Option {
	return func(c *config) {
		c.typ = uri
	}
}

// WithTitle sets the title of validation problems. By default it is empty,
// and the handler uses the reason phrase of the status for about:blank
// problems.
func WithTitle(title string) Option {
	return func(c *config) {
		c.title = title
	}
}

// WithTranslator sets the function that picks the translator for a
// request, for example from the locale a middleware stored in ctx. With a
// translator, each entry's detail is the validator's translation of the
// failed tag. Without one, because fn is nil or returns nil, or because fn
// panics, the detail is "failed validation: <tag>". The first panic, in fn
// or in a translation, is recorded as the problem's cause (see the package
// documentation), where the handler's OnError hook can find it, panic(nil)
// included under GODEBUG=panicnil=1, as a value of nil. A panic with
// [http.ErrAbortHandler] is passed on, so net/http aborts the response.
func WithTranslator(fn func(ctx context.Context) ut.Translator) Option {
	return func(c *config) {
		c.translator = fn
	}
}

// WithJSONNames declares that the validator reports JSON names, as it does
// with [JSONName] registered, so every entry gets a JSON Pointer to the
// failed field instead of a field member. Without it, every entry gets a
// field member: a pointer built from Go names would name a location the
// request body does not have. The validator has no getter for its tag name
// function, so the mapper can't find out by itself, and it never guesses
// from the errors, so the same field always has the same shape.
func WithJSONNames() Option {
	return func(c *config) {
		c.jsonNames = true
	}
}

// Mapper returns a mapper for validation errors, configured by opts (see
// the package documentation for the problem it builds). Nil options are
// ignored.
//
// The options are applied once, here; the returned mapper does not change
// afterwards and is safe for concurrent use, as long as the translators it
// is given are.
func Mapper(opts ...Option) prblm.Mapper {
	cfg := config{status: http.StatusUnprocessableEntity}

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	// The fields every validation problem shares, built once.
	fixed := []prblm.Field{
		prblm.WithType(cfg.typ),
		prblm.WithTitle(cfg.title),
		prblm.WithDetail("The request is not valid."),
	}

	return prblm.MapperFunc(func(ctx context.Context, err error) *prblm.Problem {
		ves, ok := errors.AsType[validator.ValidationErrors](err)
		if !ok || len(ves) == 0 {
			return nil
		}

		// The first panic of a translator or a translation, if any.
		var cause *prblm.PanicError

		trans := cfg.translatorFor(ctx, &cause)

		entries := make([]entry, 0, len(ves))
		for _, fe := range ves {
			entries = append(entries, cfg.entryOf(fe, trans, &cause))
		}

		// Clipping makes append copy, so concurrent calls
		// never share the backing array of fixed.
		fields := append(slices.Clip(fixed), prblm.WithExt("errors", entries))
		if cause != nil {
			fields = append(fields, prblm.WithCause(cause))
		}

		return prblm.New(cfg.status, fields...)
	})
}

// translatorFor returns the translator for the request of ctx, or nil when
// there is none or the consumer's function panics. A panic is contained
// here rather than by the handler: the translation is an enhancement, and
// losing the whole validation problem to a 500 would be worse for clients.
// The panic is kept in *cause, the problem's cause, so the handler's
// OnError hook still learns of it.
func (c *config) translatorFor(ctx context.Context, cause **prblm.PanicError) ut.Translator {
	if c.translator == nil {
		return nil
	}

	trans, _ := guard(cause, func() ut.Translator { return c.translator(ctx) })

	return trans
}

// guard calls fn and returns its result and true, or the zero value and
// false when fn panics: the panic is recovered into *cause by recoverInto.
func guard[T any](cause **prblm.PanicError, fn func() T) (result T, returned bool) {
	defer recoverInto(cause, &returned)

	result = fn()
	returned = true

	return result, returned
}

// recoverInto, deferred by guard, recovers a panic. The first one of a
// problem is kept in *cause as a *prblm.PanicError with the stack of the
// panicking frames, as the handler describes the panics it contains. Later
// ones are dropped without capturing a stack: a request with many failing
// entries would otherwise cost a stack each. guard then returns the zero
// value, since the panic came before it assigned its result.
//
// *returned, set once the guarded call returned normally, tells a panic
// apart rather than recover's result, as in the core, so panic(nil) is
// reported too under GODEBUG=panicnil=1, where recover returns nil: the
// cause is then a *prblm.PanicError with a nil Value. A runtime.Goexit in the
// consumer's code (t.FailNow in a test) also leaves it unset and records a
// nil Value; Goexit goes on ending the goroutine, so nobody reads it.
//
// A panic with [http.ErrAbortHandler] itself is passed on, as the handler
// does, so net/http aborts the response.
func recoverInto(cause **prblm.PanicError, returned *bool) {
	if *returned {
		return
	}

	v := recover()

	//nolint:errorlint // only the sentinel itself aborts, not an error wrapping it.
	if err, ok := v.(error); ok && err == http.ErrAbortHandler {
		panic(v)
	}

	if *cause == nil {
		*cause = &prblm.PanicError{Value: v, Stack: debug.Stack()}
	}
}

// entryOf returns the entry of fe. A field encoding/json never decodes (see
// [JSONName]) has no location in the request body and no name the client
// knows, only the reserved one, which a translation would show; its entry
// has only the default detail.
func (c *config) entryOf(fe validator.FieldError, trans ut.Translator, cause **prblm.PanicError) entry {
	if notDecoded(fe.Namespace()) {
		return entry{Detail: message(fe, nil, cause)}
	}

	p, f := location(fe, c.jsonNames)

	return entry{Detail: message(fe, trans, cause), Pointer: p, Field: f}
}

// message returns the detail of an entry: the translation of fe by trans,
// or "failed validation: <tag>". It never uses the rejected value, the
// parameter or fe.Error(), which can carry them and the Go struct names.
func message(fe validator.FieldError, trans ut.Translator, cause **prblm.PanicError) string {
	if text, ok := translate(fe, trans, cause); ok {
		return text
	}

	return "failed validation: " + fe.Tag()
}

// translate returns the translation of fe by trans, and false when there is
// no translator, the translator has no translation for the tag (the
// validator then falls back to fe.Error(), which names the Go struct), or
// the translation panics. A panic is recovered into *cause.
func translate(fe validator.FieldError, trans ut.Translator, cause **prblm.PanicError) (string, bool) {
	if trans == nil {
		return "", false
	}

	text, ok := guard(cause, func() string { return fe.Translate(trans) })
	if !ok {
		return "", false
	}

	return text, text != fe.Error()
}
