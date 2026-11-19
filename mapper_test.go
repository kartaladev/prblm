package prblm_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/internal/statushint"
)

// refusedError is a domain error that carries data for the problem.
type refusedError struct{ Reasons []string }

func (e *refusedError) Error() string { return "refused: " + strings.Join(e.Reasons, ", ") }

// error-mapping / Scenario: Sentinel helper.
func TestIs(t *testing.T) {
	t.Parallel()

	h := prblm.NewHandler(prblm.WithMappers(prblm.Is(errVesselNotFound, func(error) *prblm.Problem {
		return prblm.New(404)
	})))

	tests := []struct {
		name     string
		err      error
		expected int
	}{
		{name: "sentinel", err: errVesselNotFound, expected: 404},
		{name: "wrapped", err: fmt.Errorf("load: %w", errVesselNotFound), expected: 404},
		{name: "joined", err: errors.Join(io.EOF, errVesselNotFound), expected: 404},
		{name: "same text, other error", err: errors.New("vessel not found"), expected: 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := h.Resolve(t.Context(), tt.err).Status; got != tt.expected {
				t.Errorf("Status = %d, want %d", got, tt.expected)
			}
		})
	}
}

// error-mapping / Scenario: Typed helper exposes the value. The function
// is never called with a typed nil or an empty As result, so it never
// panics, and the hook receives no panic report.
func TestAs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected int
		reasons  []string
	}{
		{name: "value", err: &refusedError{Reasons: []string{"closed"}}, expected: 409, reasons: []string{"closed"}},
		{name: "wrapped", err: fmt.Errorf("x: %w", &refusedError{Reasons: []string{"full"}}), expected: 409, reasons: []string{"full"}},
		{name: "other error", err: errors.New("refused"), expected: 500},
		// Examples: typed nils and empty results.
		{
			name:     "typed nil before a value",
			err:      errors.Join((*refusedError)(nil), &refusedError{Reasons: []string{"full"}}),
			expected: 409,
			reasons:  []string{"full"},
		},
		{name: "typed nil before a sentinel", err: errors.Join((*refusedError)(nil), prblm.ErrNotFound), expected: 404},
		{name: "as method leaves the target nil", err: emptyAsError{}, expected: 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			h := prblm.NewHandler(
				prblm.WithMappers(prblm.As(func(e *refusedError) *prblm.Problem {
					return prblm.New(409, prblm.WithExt("reasons", e.Reasons))
				})),
				prblm.OnError(r.hook),
			)

			p := h.Resolve(t.Context(), tt.err)

			// Resolve reports only panics to the hook: the function must not panic.
			if calls := r.recorded(); len(calls) != 0 {
				t.Errorf("hook calls = %+v, want none", calls)
			}

			if p.Status != tt.expected {
				t.Errorf("Status = %d, want %d", p.Status, tt.expected)
			}

			v, ok := p.Ext("reasons")
			if tt.reasons == nil {
				if ok {
					t.Errorf("Ext(reasons) = %v, want none", v)
				}

				return
			}

			if got, isSlice := v.([]string); !isSlice || !slices.Equal(got, tt.reasons) {
				t.Errorf("Ext(reasons) = %v, want %v", v, tt.reasons)
			}
		})
	}
}

func TestMapperHelpers_Misuse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		build    func()
		argument string
	}{
		{name: "Is with nil target", build: func() { prblm.Is(nil, func(error) *prblm.Problem { return nil }) }, argument: "target"},
		{name: "Is with nil function", build: func() { prblm.Is(errVesselNotFound, nil) }, argument: "function"},
		{name: "Status with nil target", build: func() { prblm.Status(nil, 404) }, argument: "target"},
		{name: "As with nil function", build: func() { prblm.As[*refusedError](nil) }, argument: "function"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("construction did not panic")
				}

				if !strings.Contains(fmt.Sprint(r), tt.argument) {
					t.Errorf("panic %q does not name the %s", r, tt.argument)
				}
			}()

			tt.build()
		})
	}
}

// error-mapping / Scenario: Helper called directly. Outside any handler,
// MapError makes its own bounded walk: it returns within a second, and a
// panic in a method of the error never escapes it.
func TestMapError_Direct(t *testing.T) {
	t.Parallel()

	status := prblm.Status(errVesselNotFound, 404)
	as := prblm.As(func(*refusedError) *prblm.Problem { return prblm.New(409) })

	tests := []struct {
		name     string
		mapper   prblm.Mapper
		err      error
		expected int // 0 for a nil result
	}{
		{name: "wrapped sentinel", mapper: status, err: fmt.Errorf("load: %w", errVesselNotFound), expected: 404},
		{name: "unwrap returns itself", mapper: status, err: newLoop("loop")},
		{name: "as method panics", mapper: as, err: asPanicError{}},
		{name: "typed nil", mapper: as, err: (*refusedError)(nil)},
		{name: "typed-nil adapter hint", mapper: status, err: (*statushint.Error)(nil)},
		// A helper never uses the status hint, so it does not look one up:
		// an As method past the match is never called.
		{name: "as method panics past the match", mapper: status, err: errors.Join(errVesselNotFound, asPanicError{}), expected: 404},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := mapWithin(t, tt.mapper, tt.err); got != tt.expected {
				t.Errorf("MapError status = %d, want %d (0: nil)", got, tt.expected)
			}
		})
	}
}

// mapWithin calls m.MapError(err) under the guard of within and returns
// the status of its result, 0 for nil.
func mapWithin(t *testing.T, m prblm.Mapper, err error) int {
	t.Helper()

	p := within(t, "MapError", func() *prblm.Problem { return m.MapError(context.Background(), err) })
	if p == nil {
		return 0
	}

	return p.Status
}
