package prblm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kartaladev/prblm/internal/statushint"
)

// The sentinel table is ordered by non-descending status, so the lowest status
// wins within one error whose Is method reports several sentinels
// (error-sentinels / Requirement: Order when an error matches several
// built-ins). Each sentinel appears once.
func TestSentinelTable(t *testing.T) {
	t.Parallel()

	seen := make(map[error]bool, len(sentinels))

	for i, s := range sentinels {
		if i > 0 && s.status < sentinels[i-1].status {
			t.Errorf("sentinels[%d] (%v) has status %d, want at least %d", i, s.err, s.status, sentinels[i-1].status)
		}

		if seen[s.err] {
			t.Errorf("sentinels[%d] (%v) is listed twice", i, s.err)
		}

		seen[s.err] = true
	}
}

// selfWrapError's Unwrap returns itself.
type selfWrapError struct{}

func (*selfWrapError) Error() string   { return "self" }
func (e *selfWrapError) Unwrap() error { return e }

// A built-in called through MapError, outside a handler, makes its own
// bounded walk and looks up the hint itself, so it answers as it does within
// a handler, and returns within a second even for a cycle.
func TestWalkFunc_MapError(t *testing.T) {
	t.Parallel()

	sentinel := walkFunc(mapSentinel)

	tests := []struct {
		name   string
		err    error
		status int // 0 for no match
	}{
		{name: "wrapped sentinel", err: fmt.Errorf("x: %w", ErrNotFound), status: 404},
		{name: "sentinel inside a hint", err: WithStatus(ErrNotFound, 410)},
		{name: "sentinel inside an adapter's hint", err: statushint.Wrap(ErrNotFound, 410)},
		{name: "sentinel inside a success status", err: statushint.Wrap(ErrNotFound, 200), status: 404},
		{name: "self-loop", err: &selfWrapError{}},
		{name: "unrelated error", err: errors.New("x")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done := make(chan *Problem, 1)

			go func() { done <- sentinel.MapError(context.Background(), tt.err) }()

			select {
			case p := <-done:
				status := 0
				if p != nil {
					status = p.Status
				}

				if status != tt.status {
					t.Errorf("MapError status = %d, want %d (0: no match)", status, tt.status)
				}
			case <-time.After(time.Second):
				panic(t.Name() + ": MapError did not return within 1s")
			}
		})
	}
}
