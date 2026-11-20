package errwalk_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/kartaladev/prblm/internal/errwalk"
)

// multiError wraps its entries as given, nil ones included, unlike
// errors.Join. It is a pointer type, so entries compare with ==.
type multiError struct {
	errs []error
}

func (*multiError) Error() string     { return "multi" }
func (e *multiError) Unwrap() []error { return e.errs }

// wrapError wraps next through Unwrap() error. A nil next ends the chain; a
// wrapError whose next is itself loops.
type wrapError struct {
	next error
}

func (*wrapError) Error() string   { return "wrap" }
func (e *wrapError) Unwrap() error { return e.next }

// countingError counts the calls of its Unwrap method.
type countingError struct {
	calls atomic.Int32
}

func (*countingError) Error() string { return "counting" }

func (e *countingError) Unwrap() error {
	e.calls.Add(1)

	return nil
}

// chain returns tail wrapped n times, so a chain of n+1 entries.
func chain(n int, tail error) error {
	err := tail
	for range n {
		err = &wrapError{next: err}
	}

	return err
}

// selfLoop returns an error whose Unwrap() error returns itself.
func selfLoop() error {
	e := &wrapError{}
	e.next = e

	return e
}

// selfList returns an error whose Unwrap() []error holds n copies of itself.
func selfList(n int) error {
	e := &multiError{}
	e.errs = slices.Repeat([]error{e}, n)

	return e
}

// joinCycle returns an errors.Join whose only entry is the join itself. It
// relies on the join's Unwrap returning its own slice.
func joinCycle(t *testing.T) error {
	t.Helper()

	j := errors.Join(errors.New("leaf"))

	u, ok := j.(interface{ Unwrap() []error })
	if !ok {
		t.Fatal("errors.Join has no Unwrap() []error")
	}

	u.Unwrap()[0] = j

	return j
}

// joinTree returns levels nested errors.Join(n, n) around a leaf.
func joinTree(levels int) error {
	err := errors.New("leaf")
	for range levels {
		err = errors.Join(err, err)
	}

	return err
}

// checkList fails the test unless got holds exactly want, in order, compared
// by identity.
func checkList(t *testing.T, got, want []error) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("list has %d entries %v, want %d %v", len(got), got, len(want), want)
	}

	for i := range want {
		if got[i] != want[i] { //nolint:errorlint // each entry must be the very error visited
			t.Errorf("list[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// error-chain-walk / Scenario: First match follows errors.Is order (the
// walker's order): the root first, then depth first, each Unwrap() []error
// in order, as errors.Is visits the chain.
func TestNew_Order(t *testing.T) {
	t.Parallel()

	a, b, c := errors.New("a"), errors.New("b"), errors.New("c")
	wrapped := fmt.Errorf("one: %w", a)
	wrappedTwice := fmt.Errorf("two: %w", wrapped)
	inner := errors.Join(a, b)
	nested := errors.Join(inner, c)
	unwrapsNil := &wrapError{}
	empty := &multiError{}
	nilFirst := &multiError{errs: []error{nil, a}}

	tests := []struct {
		name string
		err  error
		want []error
	}{
		{name: "nil root", err: nil, want: nil},
		{name: "single error", err: a, want: []error{a}},
		{name: "%w chain", err: wrappedTwice, want: []error{wrappedTwice, wrapped, a}},
		{name: "nested join", err: nested, want: []error{nested, inner, a, b, c}},
		{name: "unwrap returns nil", err: unwrapsNil, want: []error{unwrapsNil}},
		{name: "empty unwrap list", err: empty, want: []error{empty}},
		{name: "nil list entry skipped", err: nilFirst, want: []error{nilFirst, a}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checkList(t, errwalk.New(tt.err).List(), tt.want)
		})
	}
}

// walkWithin returns the walk of err, crashing the test binary when the walk
// takes longer than a second: a hang must not stall the suite, and the
// runaway walk must not keep eating memory.
func walkWithin(t *testing.T, err error) *errwalk.Walk {
	t.Helper()

	done := make(chan *errwalk.Walk, 1)

	go func() { done <- errwalk.New(err) }()

	select {
	case w := <-done:
		return w
	case <-time.After(time.Second):
		panic(t.Name() + ": the walk did not return within 1s")
	}
}

// checkReason fails the test unless w aborted for reason, "" for not at all.
func checkReason(t *testing.T, w *errwalk.Walk, reason errwalk.Reason) {
	t.Helper()

	var got errwalk.Reason
	if abort := w.Aborted(); abort != nil {
		got = abort.Reason
	}

	if got != reason {
		t.Errorf("abort reason = %q, want %q", got, reason)
	}
}

// checkWalk fails the test unless w aborted for reason ("" for not at all)
// and listed length entries.
func checkWalk(t *testing.T, w *errwalk.Walk, reason errwalk.Reason, length int) {
	t.Helper()

	checkReason(t, w, reason)

	if n := len(w.List()); n != length {
		t.Errorf("list length = %d, want %d", n, length)
	}
}

// error-chain-walk / Scenario: Budget boundary (walker level). Every entry
// costs 1, nil and repeated ones included, and the walk aborts when the
// entries counted plus those waiting would exceed the budget.
func TestNew_Budget(t *testing.T) {
	t.Parallel()

	leaf := errors.New("leaf")
	late := &countingError{}
	tests := []struct {
		name   string
		err    error
		reason errwalk.Reason // "" when the walk completes
		length int
	}{
		{name: "1,000-entry chain", err: chain(999, leaf), length: 1000},
		{name: "1,001-entry chain", err: chain(1000, leaf), reason: errwalk.ReasonBudget, length: 1000},
		{name: "998 nil entries and an error", err: &multiError{errs: append(make([]error, 998), leaf)}, length: 2},
		{name: "1,000 children", err: &multiError{errs: slices.Repeat([]error{leaf}, 1000)}, reason: errwalk.ReasonBudget, length: 1},
		{name: "20,000 self copies", err: selfList(20_000), reason: errwalk.ReasonBudget, length: 1},
		{name: "unwrap of entry 1,001 never called", err: chain(1000, late), reason: errwalk.ReasonBudget, length: 1000},
		{name: "self-loop through Unwrap() error", err: selfLoop(), reason: errwalk.ReasonBudget, length: 1000},
		{name: "self-loop through Unwrap() []error", err: selfList(1), reason: errwalk.ReasonBudget, length: 1000},
		{name: "self-loop through errors.Join", err: joinCycle(t), reason: errwalk.ReasonBudget, length: 1000},
		{name: "25 Join(n, n) levels", err: joinTree(25), reason: errwalk.ReasonBudget, length: 980},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := walkWithin(t, tt.err)
			checkWalk(t, w, tt.reason, tt.length)
		})
	}

	// Cleanup runs once the parallel subtests are done.
	t.Cleanup(func() {
		if n := late.calls.Load(); n != 0 {
			t.Errorf("Unwrap of the entry past the budget called %d times, want 0", n)
		}
	})
}

// The fan-out is refused before a single child is pushed, so a huge
// Unwrap() []error costs only the Abort the walk records.
//
//nolint:paralleltest // AllocsPerRun counts every allocation in the process
func TestNew_BudgetAllocs(t *testing.T) {
	huge := selfList(20_000)

	if w := walkWithin(t, huge); w.Aborted() == nil {
		t.Fatal("walking 20,000 self copies did not abort")
	}

	if allocs := testing.AllocsPerRun(10, func() { errwalk.New(huge) }); allocs >= 2 {
		t.Errorf("walking 20,000 self copies allocates %v times, want under 2", allocs)
	}
}

// valueUnwrapError has a value-receiver Unwrap, so calling it through a nil
// *valueUnwrapError panics.
type valueUnwrapError struct{}

func (valueUnwrapError) Error() string { return "value unwrap" }
func (valueUnwrapError) Unwrap() error { return errors.New("never reached") }

// sliceError is an error of a slice kind: a nil sliceError is a valid error.
type sliceError []int

func (sliceError) Error() string { return "slice" }

// mapError is an error of a map kind: a nil mapError is a valid error.
type mapError map[string]int

func (mapError) Error() string { return "map" }

// error-chain-walk / Scenario: Typed nil in the chain (walker level). A nil
// pointer is charged and skipped without calling any of its methods; nil
// slices and maps are walked like any other error.
func TestNew_TypedNil(t *testing.T) {
	t.Parallel()

	leaf := errors.New("leaf")
	typedNil := (*wrapError)(nil)
	valueNil := (*valueUnwrapError)(nil)
	joined := &multiError{errs: []error{valueNil, leaf}}
	wrapsTypedNil := &wrapError{next: typedNil}

	tests := []struct {
		name   string
		err    error
		reason errwalk.Reason // "" when the walk completes
		length int
		want   []error // checked by identity when set
	}{
		{name: "typed-nil root", err: typedNil, length: 0},
		{name: "typed nil with a value-receiver Unwrap", err: joined, length: 2, want: []error{joined, leaf}},
		{name: "Unwrap returns a typed nil", err: wrapsTypedNil, length: 1, want: []error{wrapsTypedNil}},
		{name: "nil slice error", err: sliceError(nil), length: 1},
		{name: "nil map error", err: mapError(nil), length: 1},
		{
			name:   "typed nils count toward the budget",
			err:    &multiError{errs: []error{typedNil, typedNil, chain(997, leaf)}},
			reason: errwalk.ReasonBudget,
			length: 998,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := walkWithin(t, tt.err)
			checkWalk(t, w, tt.reason, tt.length)

			if tt.want != nil {
				checkList(t, w.List(), tt.want)
			}
		})
	}
}

// panicUnwrapError's Unwrap() error panics with "boom".
type panicUnwrapError struct{}

func (panicUnwrapError) Error() string { return "panic unwrap" }
func (panicUnwrapError) Unwrap() error { panic("boom") }

// panicListError's Unwrap() []error panics with "boom".
type panicListError struct{}

func (panicListError) Error() string   { return "panic list" }
func (panicListError) Unwrap() []error { panic("boom") }

// nilPanicError's Unwrap() error calls panic(nil).
type nilPanicError struct{}

func (nilPanicError) Error() string { return "nil panic" }

func (nilPanicError) Unwrap() error {
	panic(nilValue)
}

// nilValue is a nil panic value, so panic(nilValue) is panic(nil).
var nilValue any

// checkPanicAbort fails the test unless w aborted for a panic whose stack
// holds frame, and returns the panic value.
func checkPanicAbort(t *testing.T, w *errwalk.Walk, frame string) any {
	t.Helper()

	abort := w.Aborted()
	if abort == nil || abort.Reason != errwalk.ReasonPanic {
		t.Fatalf("abort = %+v, want reason %q", abort, errwalk.ReasonPanic)
	}

	if !strings.Contains(string(abort.Stack), frame) {
		t.Errorf("stack does not hold the panicking frame %q:\n%s", frame, abort.Stack)
	}

	return abort.Value
}

// error-chain-walk / Scenario: Error methods panic (walker level). A panic
// in an Unwrap method aborts the walk, whatever the value.
func TestNew_Panic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		err   error
		frame string
		// check checks the recovered panic value.
		check func(v any) bool
	}{
		{
			name: "single Unwrap panics", err: &wrapError{next: panicUnwrapError{}}, frame: "panicUnwrapError.Unwrap",
			check: func(v any) bool { return v == "boom" },
		},
		{
			name: "list Unwrap panics", err: panicListError{}, frame: "panicListError.Unwrap",
			check: func(v any) bool { return v == "boom" },
		},
		{
			name: "Unwrap calls panic nil", err: nilPanicError{}, frame: "nilPanicError.Unwrap",
			check: isPanicNil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if v := checkPanicAbort(t, walkWithin(t, tt.err), tt.frame); !tt.check(v) {
				t.Errorf("panic value = %#v, not the expected one", v)
			}
		})
	}
}

// error-chain-walk / Scenario: panic(nil) under GODEBUG=panicnil=1 (walker
// level): the recovered value is nil, and the walk still aborts. Outside that
// setting the test runs itself again under it.
func TestNew_PanicNil(t *testing.T) {
	t.Parallel()

	if rerunUnderPanicNil(t) {
		return
	}

	if v := checkPanicAbort(t, walkWithin(t, nilPanicError{}), "nilPanicError.Unwrap"); v != nil {
		t.Errorf("panic value = %#v, want nil", v)
	}
}

// rerunUnderPanicNil runs the top-level test t again in a subprocess of this
// test binary with GODEBUG=panicnil=1, fails t unless it passes there, and
// reports true, so the caller returns. In a process that already has the
// setting it does nothing and reports false, and the caller runs its body.
func rerunUnderPanicNil(t *testing.T) bool {
	t.Helper()

	if isPanicNilMode() {
		return false
	}

	//nolint:gosec // os.Args[0] is this test binary, run again with fixed arguments.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")

	cmd.Env = append(os.Environ(), "GODEBUG=panicnil=1")

	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()+" ") {
		t.Fatalf("under GODEBUG=panicnil=1: %v\n%s", err, out)
	}

	return true
}

// isPanicNilMode reports whether this process runs with GODEBUG=panicnil=1,
// where recover returns nil for panic(nil).
func isPanicNilMode() bool {
	return strings.Contains(os.Getenv("GODEBUG"), "panicnil=1")
}

// isPanicNil reports whether v is what recover returns for panic(nil) in
// this process: a *runtime.PanicNilError, or nil under GODEBUG=panicnil=1.
func isPanicNil(v any) bool {
	if isPanicNilMode() {
		return v == nil
	}

	err, ok := v.(error)
	_, isPanicNilError := errors.AsType[*runtime.PanicNilError](err)

	return ok && isPanicNilError
}

// isTargetError reports target through its Is method.
type isTargetError struct {
	target error
}

func (*isTargetError) Error() string          { return "is target" }
func (e *isTargetError) Is(target error) bool { return target == e.target }

// isPanicError's Is method panics with "boom".
type isPanicError struct{}

func (isPanicError) Error() string { return "is panics" }
func (isPanicError) Is(error) bool { panic("boom") }

// anyError is comparable as a type, but == panics when v holds a slice.
type anyError struct {
	v any
}

func (anyError) Error() string { return "any" }

// asTargetError's As method sets a *wrapError target to set.
type asTargetError struct {
	set *wrapError
}

func (*asTargetError) Error() string { return "as target" }

func (e *asTargetError) As(target any) bool {
	p, ok := target.(**wrapError)
	if ok {
		*p = e.set
	}

	return ok
}

// asPanicError's As method panics with "boom".
type asPanicError struct{}

func (asPanicError) Error() string { return "as panics" }
func (asPanicError) As(any) bool   { panic("boom") }

// emptyAsError's As method reports a match without setting its target.
type emptyAsError struct{}

func (emptyAsError) Error() string { return "empty as" }
func (emptyAsError) As(any) bool   { return true }

// declineAsError's As method sets a *wrapError target and still reports no
// match.
type declineAsError struct{}

func (declineAsError) Error() string { return "decline as" }

func (declineAsError) As(target any) bool {
	if p, ok := target.(**wrapError); ok {
		*p = &wrapError{}
	}

	return false
}

// code is a target of an int kind, whose zero value is a valid result.
type code int

// timeouter is an interface target for As.
type timeouter interface {
	Timeout() bool
}

// error-chain-walk / Scenario: Error methods panic; errors.Is semantics of
// the scan: the index of the first entry that is target (==, only for a
// comparable target) or reports it through its Is method. A panic aborts the
// walk, and a scan of an aborted walk matches nothing.
func TestWalk_Is(t *testing.T) {
	t.Parallel()

	target := errors.New("target")
	wrapped := fmt.Errorf("x: %w", target)
	slice := sliceError{1}
	poisoned := anyError{v: []int{1}}
	loop := selfLoop() // its first entry is itself, so == would match it

	tests := []struct {
		name   string
		err    error
		target error
		want   int
		reason errwalk.Reason // "" when the walk did not abort
	}{
		{name: "== match", err: wrapped, target: target, want: 1},
		{name: "Is method match", err: &wrapError{next: &isTargetError{target: target}}, target: target, want: 1},
		{name: "no match", err: wrapped, target: errors.New("other"), want: -1},
		{name: "nil target", err: wrapped, target: nil, want: -1},
		{name: "uncomparable target is not compared", err: slice, target: slice, want: -1},
		{name: "== panics on a comparable type", err: poisoned, target: anyError{v: []int{1}}, want: -1, reason: errwalk.ReasonPanic},
		{name: "Is method panics", err: &wrapError{next: isPanicError{}}, target: target, want: -1, reason: errwalk.ReasonPanic},
		{name: "scan after an abort", err: loop, target: loop, want: -1, reason: errwalk.ReasonBudget},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := walkWithin(t, tt.err)

			if got := w.Is(tt.target); got != tt.want {
				t.Errorf("Is = %d, want %d", got, tt.want)
			}

			checkReason(t, w, tt.reason)
		})
	}
}

// isEveryError reports every target through its Is method.
type isEveryError struct{}

func (isEveryError) Error() string   { return "is every" }
func (isEveryError) Is(error) bool   { return true }
func (isEveryError) Unwrap() []error { return nil }

// IsAny answers as Is called for each target in turn, keeping the lowest
// index and, on a tie, the earlier target, including which methods it calls:
// a panicking Is past the first match still aborts the walk.
func TestWalk_IsAny(t *testing.T) {
	t.Parallel()

	first, second := errors.New("first"), errors.New("second")
	targets := []error{first, second}
	poisoned := anyError{v: []int{1}}

	tests := []struct {
		name    string
		err     error
		targets []error
		at      int
		which   int
		reason  errwalk.Reason // "" when the walk did not abort
	}{
		{name: "lowest index wins", err: errors.Join(errors.New("x"), second, first), targets: targets, at: 2, which: 1},
		{name: "tie goes to the earlier target", err: isEveryError{}, targets: targets, at: 0, which: 0},
		{name: "Is method match", err: &wrapError{next: &isTargetError{target: second}}, targets: targets, at: 1, which: 1},
		{name: "no match", err: fmt.Errorf("x: %w", errors.New("y")), targets: targets, at: -1, which: -1},
		{name: "no targets", err: first, targets: nil, at: -1, which: -1},
		{name: "nil target is skipped", err: first, targets: []error{nil, first}, at: 0, which: 1},
		{name: "uncomparable target is not compared", err: errors.Join(first), targets: []error{sliceError{1}, first}, at: 1, which: 1},
		{
			name: "Is panic past the first match aborts", err: errors.Join(first, isPanicError{}),
			targets: targets, at: -1, which: -1, reason: errwalk.ReasonPanic,
		},
		{
			name: "== panics on a comparable type", err: errors.Join(first, poisoned),
			targets: []error{poisoned, first}, at: -1, which: -1, reason: errwalk.ReasonPanic,
		},
		{name: "scan after an abort", err: selfLoop(), targets: targets, at: -1, which: -1, reason: errwalk.ReasonBudget},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := walkWithin(t, tt.err)

			if at, which := w.IsAny(tt.targets); at != tt.at || which != tt.which {
				t.Errorf("IsAny = %d, %d, want %d, %d", at, which, tt.at, tt.which)
			}

			checkReason(t, w, tt.reason)
		})
	}
}

// error-chain-walk / Scenario: An As that sets nothing; Error methods panic.
// The scan finds the first entry of type T or whose As method sets one. An
// As method reporting true while leaving a pointer or interface target nil
// does not match; for other kinds a zero value is a match.
func TestWalk_As(t *testing.T) {
	t.Parallel()

	inner := &wrapError{}
	set := &wrapError{}

	t.Run("pointer targets", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name   string
			err    error
			want   *wrapError
			index  int
			reason errwalk.Reason
		}{
			{name: "direct type", err: fmt.Errorf("x: %w", inner), want: inner, index: 1},
			{name: "As method", err: &asTargetError{set: set}, want: set, index: 0},
			{name: "As method panics", err: asPanicError{}, want: nil, index: -1, reason: errwalk.ReasonPanic},
			{name: "As true with a nil pointer target", err: emptyAsError{}, want: nil, index: -1},
			{
				name: "declined As leaves nothing behind", err: &multiError{errs: []error{declineAsError{}, emptyAsError{}}},
				want: nil, index: -1,
			},
			{name: "typed nil never in the list", err: &multiError{errs: []error{(*wrapError)(nil)}}, want: nil, index: -1},
			{name: "scan after an abort", err: selfLoop(), want: nil, index: -1, reason: errwalk.ReasonBudget},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				w := walkWithin(t, tt.err)

				got, index := errwalk.As[*wrapError](w)
				if got != tt.want || index != tt.index {
					t.Errorf("As = %p, %d, want %p, %d", got, index, tt.want, tt.index)
				}

				checkReason(t, w, tt.reason)
			})
		}
	})

	t.Run("As true with a nil interface target", func(t *testing.T) {
		t.Parallel()

		w := walkWithin(t, emptyAsError{})

		if got, index := errwalk.As[timeouter](w); got != nil || index != -1 {
			t.Errorf("As = %v, %d, want nil, -1", got, index)
		}

		checkReason(t, w, "")
	})

	t.Run("As true with a zero int target", func(t *testing.T) {
		t.Parallel()

		w := walkWithin(t, emptyAsError{})

		if got, index := errwalk.As[code](w); got != 0 || index != 0 {
			t.Errorf("As = %v, %d, want 0, 0", got, index)
		}

		checkReason(t, w, "")
	})
}

// Has finds an entry by its type alone: unlike As, it calls no As method,
// so a chain with a panicking As method is scanned to the end and the walk
// does not abort. A scan of an aborted walk matches nothing, as for Is and
// As.
func TestWalk_Has(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		want   bool
		reason errwalk.Reason
	}{
		{name: "root", err: &wrapError{}, want: true},
		{name: "wrapped", err: fmt.Errorf("x: %w", &wrapError{}), want: true},
		{name: "after a panicking As method", err: &multiError{errs: []error{asPanicError{}, &wrapError{}}}, want: true},
		{name: "As method that would set it", err: &asTargetError{set: &wrapError{}}, want: false},
		{name: "absent", err: errors.New("leaf"), want: false},
		{name: "typed nil never in the list", err: &multiError{errs: []error{(*wrapError)(nil)}}, want: false},
		{name: "nil root", err: nil, want: false},
		{name: "scan after an abort", err: selfLoop(), want: false, reason: errwalk.ReasonBudget},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := walkWithin(t, tt.err)

			if got := errwalk.Has[*wrapError](w); got != tt.want {
				t.Errorf("Has = %t, want %t", got, tt.want)
			}

			checkReason(t, w, tt.reason)
		})
	}
}

// Walking and scanning a chain that fits the inline buffer costs no
// allocation: a target is made only for an entry whose As method is called,
// and a walk asked one question and dropped, as the adapters ask whether the
// client went away or for a framework error, stays off the heap.
//
//nolint:paralleltest // AllocsPerRun counts every allocation in the process
func TestWalk_Allocs(t *testing.T) {
	err := chain(3, errors.New("leaf"))
	w := walkWithin(t, err)

	tests := []struct {
		name string
		run  func()
	}{
		{name: "scan/Is", run: func() { _ = w.Is(os.ErrNotExist) }},
		{name: "scan/IsAny", run: func() { _, _ = w.IsAny([]error{os.ErrNotExist, os.ErrExist}) }},
		{name: "scan/As pointer", run: func() { _, _ = errwalk.As[*os.PathError](w) }},
		{name: "scan/As interface", run: func() { _, _ = errwalk.As[interface{ Timeout() bool }](w) }},
		{name: "scan/Has", run: func() { _ = errwalk.Has[*os.PathError](w) }},
		{name: "one-shot/Is", run: func() { _ = errwalk.New(err).Is(os.ErrNotExist) }},
		{name: "one-shot/As", run: func() { _, _ = errwalk.As[*os.PathError](errwalk.New(err)) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if allocs := testing.AllocsPerRun(100, tt.run); allocs != 0 {
				t.Errorf("%s over 4 entries without methods allocates %v times, want 0", tt.name, allocs)
			}
		})
	}
}

// The handler's walk escapes, so every resolution allocates one Walk: it
// must stay in the 112-byte size class.
func TestWalk_Size(t *testing.T) {
	t.Parallel()

	const maxSize = 112

	if size := unsafe.Sizeof(errwalk.Walk{}); size > maxSize {
		t.Errorf("unsafe.Sizeof(Walk{}) = %d bytes, want at most %d", size, maxSize)
	}
}

// walkSink receives each iteration's walk, so the compiler cannot optimize
// the walk away.
var walkSink *errwalk.Walk

// BenchmarkNew measures flattening a chain of 1, 4 and 16 entries: one
// error, and errors wrapped through Unwrap() error around it. 1 to 4 entries
// is the common path and fits the walk's inline buffer; 16 does not.
func BenchmarkNew(b *testing.B) {
	for _, entries := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("entries=%d", entries), func(b *testing.B) {
			err := chain(entries-1, errors.New("leaf"))

			if w := errwalk.New(err); w.Aborted() != nil || len(w.List()) != entries {
				b.Fatalf("New() walked %d entries, aborted: %v; want %d, not aborted",
					len(w.List()), w.Aborted(), entries)
			}

			b.ReportAllocs()

			for b.Loop() {
				walkSink = errwalk.New(err)
			}
		})
	}
}
