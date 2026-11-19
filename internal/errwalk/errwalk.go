// Package errwalk walks an error chain once, in the order errors.Is and
// errors.As visit it, so the library inspects every error through one list.
package errwalk

import (
	"reflect"
	"runtime/debug"
	"slices"
)

// Budget is the most entries a walk examines: the root, each node, each
// repeated visit of a node, each nil entry of an Unwrap() []error and each
// typed nil cost 1, and nothing is deduplicated. A real chain is a handful
// of entries, so the bound costs nothing in practice, while a cyclic, deep
// or exploding chain is cut off at once. The value is shared with other
// implementations of the same walk rules, so it is fixed.
const Budget = 1000

// Reason says why a walk aborted.
type Reason string

// ReasonBudget is the reason of a walk that would examine more entries than
// the budget allows.
const ReasonBudget Reason = "budget"

// ReasonPanic is the reason of a walk stopped by a panic in a method of an
// error in the chain, or in comparing an error with a target.
const ReasonPanic Reason = "panic"

// Abort describes why a walk stopped before the end of the chain.
type Abort struct {
	Reason Reason
	// Value is the recovered panic value for ReasonPanic: nil when the
	// method called panic(nil) under GODEBUG=panicnil=1.
	Value any
	// Stack is the stack captured where the panic was recovered, which still
	// holds the panicking frames, for ReasonPanic.
	Stack []byte
}

// Walk is the flattened chain of one error.
//
// It holds no pointer into itself, so a walk that is asked one question and
// dropped stays off the heap: the first entries are kept in buf, and only a
// longer chain moves them to more.
type Walk struct {
	// abort is why the walk stopped; its zero Reason means it did not. It
	// is kept by value, so aborting allocates nothing.
	abort Abort
	// more holds every entry once the chain outgrows buf, or is nil.
	more []error
	// buf holds the first n entries while the chain fits in it.
	buf [8]error
	n   int
}

// iser is an error with its own Is method.
type iser interface{ Is(target error) bool }

// aser is an error with its own As method.
type aser interface{ As(target any) bool }

// entries returns the flattened chain.
func (w *Walk) entries() []error {
	if w.more != nil {
		return w.more
	}

	return w.buf[:w.n]
}

// add appends e to the flattened chain.
func (w *Walk) add(e error) {
	switch {
	case w.more != nil:
		w.more = append(w.more, e)
	case w.n < len(w.buf):
		w.buf[w.n] = e
		w.n++
	default:
		w.more = append(append(make([]error, 0, 2*len(w.buf)), w.buf[:]...), e)
	}
}

// Aborted returns why the walk aborted, or nil when it walked the whole
// chain.
func (w *Walk) Aborted() *Abort {
	if w.abort.Reason == "" {
		return nil
	}

	return &w.abort
}

// Is returns the index of the first entry that is target or reports it
// through its Is method, as errors.Is tests one error, or -1. An entry is
// compared with == only when target's type is comparable; a panic in that
// comparison or in an Is method aborts the walk. A walk that aborted, before
// or during the scan, matches nothing.
//
// Only an Is method, or an == that may panic, runs through call, so nearly
// every entry of a real chain skips its deferred recover.
func (w *Walk) Is(target error) int {
	if target == nil || w.Aborted() != nil {
		return -1
	}

	eq := equalityOf(target)

	for i, e := range w.entries() {
		matched, ok := w.isEntry(e, target, eq)
		if !ok {
			return -1
		}

		if matched {
			return i
		}
	}

	return -1
}

// equality says how Is compares entries with a target of some type by ==.
type equality int

const (
	// eqDirect: a plain ==. == between values of different types is
	// false, and between values of the same type it cannot panic, as for a
	// pointer such as the *errors.errorString of errors.New.
	eqDirect equality = iota
	// eqGuarded: == through call, and only with an entry of the same type.
	// A struct or an array may hold an interface whose dynamic value is
	// uncomparable, so == between two of them may panic.
	eqGuarded
	// eqNone: no ==, as errors.Is does for an uncomparable type.
	eqNone
)

// equalityOf returns the equality of v's type; v is not nil.
func equalityOf(v any) equality {
	t := reflect.TypeOf(v)

	switch k := t.Kind(); {
	case !t.Comparable():
		return eqNone
	case k == reflect.Struct || k == reflect.Array:
		return eqGuarded
	default:
		return eqDirect
	}
}

// isEntry reports whether e is target or reports it through its Is method,
// and false for ok when that aborted w.
func (w *Walk) isEntry(e, target error, eq equality) (matched, ok bool) {
	if eq == eqDirect && e == target { //nolint:errorlint // one link of the chain, as errors.Is tests it
		return true, true
	}

	x, hasIs := e.(iser)
	guarded := eq == eqGuarded && reflect.TypeOf(e) == reflect.TypeOf(target)

	if !hasIs && !guarded {
		return false, true
	}

	return call(w, func() bool {
		return guarded && e == target || hasIs && x.Is(target) //nolint:errorlint // one link of the chain, as errors.Is tests it
	})
}

// IsAny returns the lowest index of an entry that Is matches with one of
// targets, and the index of that target, or -1, -1. It answers as Is called
// for each target in turn, keeping the lowest index and, on a tie, the
// earlier target, and it calls the same methods in the same order: a
// panicking Is method past the first match still aborts the walk.
//
// When no entry is special, with an Is method or a type not compared by a
// plain == (see equalityOf), Is calls no method and no == can panic, whatever
// the target: an entry's == with a target of another type is false, which is
// also Is's answer when it does not compare them. The order of the
// comparisons then cannot be observed, so one pass that stops at the first
// matching entry replaces a scan per target, each looking for Is methods.
func (w *Walk) IsAny(targets []error) (at, which int) {
	if w.Aborted() != nil {
		return -1, -1
	}

	if !slices.ContainsFunc(w.entries(), isSpecial) {
		for i, e := range w.entries() {
			for k, target := range targets {
				if target != nil && e == target { //nolint:errorlint // one link of the chain, as errors.Is tests it
					return i, k
				}
			}
		}

		return -1, -1
	}

	at, which = -1, -1

	for k, target := range targets {
		if i := w.Is(target); i >= 0 && (at < 0 || i < at) {
			at, which = i, k
		}
	}

	if w.Aborted() != nil {
		return -1, -1
	}

	return at, which
}

// isSpecial reports whether e has an Is method, or a type that is not
// compared with a plain == (see equalityOf).
func isSpecial(e error) bool {
	if _, hasIs := e.(iser); hasIs {
		return true
	}

	return equalityOf(e) != eqDirect
}

// As returns the first entry of type T, or whose As method sets a T, with its
// index, or the zero T and -1. An As method that reports true and leaves a
// pointer or interface target nil does not match; for other kinds of T, a
// zero value it reports is a match, as errors.As counts it. A panic in an As
// method aborts the walk, and a walk that aborted, before or during the scan,
// matches nothing.
//
// Only an As method runs through call: a type assertion cannot panic, so the
// entries without one skip its deferred recover.
func As[T any](w *Walk) (T, int) {
	var zero T

	if w.Aborted() != nil {
		return zero, -1
	}

	for i, e := range w.entries() {
		if v, ok := e.(T); ok {
			return v, i
		}

		x, hasAs := e.(aser)
		if !hasAs {
			continue
		}

		t, ok := call(w, func() *T { return asMethod[T](x) })
		if !ok {
			return zero, -1
		}

		if t != nil {
			return *t, i
		}
	}

	return zero, -1
}

// Has reports whether an entry has type T. Unlike As, it uses a type
// assertion alone and calls no As method, so it never aborts the walk: use
// it to find an error of the library's own making, whatever the methods of
// the errors around it do. A walk that aborted matches nothing.
func Has[T any](w *Walk) bool {
	if w.Aborted() != nil {
		return false
	}

	for _, e := range w.entries() {
		if _, ok := e.(T); ok {
			return true
		}
	}

	return false
}

// asMethod returns the T that x's As method sets, or nil when it declines or
// leaves a pointer or interface T nil. The target escapes, since As receives
// it as any, so it is made only here, for an entry that has an As method. A
// declined As may have written its target, so each call gets a fresh one.
func asMethod[T any](x aser) *T {
	t := new(T)
	if !x.As(t) {
		return nil
	}

	if v := any(*t); v == nil || isTypedNil(v) {
		return nil
	}

	return t
}

// New flattens the chain of err: the root first, then depth first, the
// entries of each Unwrap() []error in order, as errors.Is visits them.
//
// Each entry is charged before any of its methods is called, and the walk
// aborts with [ReasonBudget] when the entries charged plus those waiting would
// exceed [Budget]. A nil entry and a typed nil (a nil pointer) are charged and
// skipped, so no method of a typed nil is ever called, and a typed-nil root
// gives an empty, complete walk.
//
// New is small enough to be inlined, so the walk of a caller that does not
// keep it is not allocated on the heap.
func New(err error) *Walk {
	w := &Walk{}
	w.flatten(err)

	return w
}

// flatten walks err into w, which is empty.
func (w *Walk) flatten(err error) {
	stack := [8]error{err}
	pending := stack[:1]
	examined := 0

	// one holds a single child until push copies it to pending.
	var one [1]error

	for len(pending) > 0 {
		e := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		examined++

		if e == nil || isTypedNil(e) {
			continue
		}

		w.add(e)

		children, ok := w.unwrap(e, &one)
		if !ok {
			break
		}

		// Checked before any child is pushed, so a huge fan-out costs
		// nothing, and before any child's method is called.
		if examined+len(pending)+len(children) > Budget {
			w.abort = Abort{Reason: ReasonBudget}

			break
		}

		pending = push(pending, children)
	}
}

// unwrap returns the errors e wraps, as errors.Is unwraps it: a nil from
// Unwrap() error gives none, and a single child is returned in one, so it
// needs no allocation. It reports false when the Unwrap method panicked, and
// w has then aborted.
func (w *Walk) unwrap(e error, one *[1]error) ([]error, bool) {
	switch u := e.(type) { //nolint:errorlint // one link of the chain, as errors.Is unwraps it
	case interface{ Unwrap() error }:
		child, ok := call(w, u.Unwrap)
		if !ok || child == nil {
			return nil, ok
		}

		one[0] = child

		return one[:], true
	case interface{ Unwrap() []error }:
		return call(w, u.Unwrap)
	}

	return nil, true
}

// call runs fn, a method of an error in the chain, and reports whether it
// returned normally. When it did not, w aborts with ReasonPanic. The flag,
// not recover's result, tells a panic apart, so panic(nil) aborts too under
// GODEBUG=panicnil=1, where recover returns nil. The stack is captured in the
// deferred function, while the panicking frames are still on it. A
// runtime.Goexit in fn also leaves the flag unset and records an abort with a
// nil value; the goroutine exits anyway, so nobody reads it.
func call[T any](w *Walk, fn func() T) (result T, ok bool) {
	defer func() {
		if !ok {
			w.abort = Abort{Reason: ReasonPanic, Value: recover(), Stack: debug.Stack()}
		}
	}()

	result = fn()
	ok = true

	return result, ok
}

// isTypedNil reports whether v holds a nil pointer. Only pointers count:
// a nil map, slice, func or channel can be a valid error, and calling its
// methods is safe.
func isTypedNil(v any) bool {
	rv := reflect.ValueOf(v)

	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

// push puts children on the pending stack in reverse, so the first child is
// popped, and so walked, first.
func push(pending, children []error) []error {
	for _, child := range slices.Backward(children) {
		pending = append(pending, child)
	}

	return pending
}
