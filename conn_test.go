//go:build !plan9

package prblm_test

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/kartaladev/prblm"
)

// panic-recovery / Scenario: Panic after output or on broken connection
// (broken connection rows).
func TestRecover_Broken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value error
	}{
		{name: "broken pipe", value: fmt.Errorf("write: %w", syscall.EPIPE)},
		{name: "connection reset", value: fmt.Errorf("write: %w", syscall.ECONNRESET)},
		// Murphy row: the shape net/http produces.
		{name: "net error", value: &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.EPIPE)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			h := prblm.NewHandler(prblm.OnError(r.hook))
			rec := serveRecorded(t, h.Recover(panicking(tt.value)), "")

			if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
				t.Errorf("wrote %q with headers %v, want nothing", rec.Body, rec.Header())
			}

			calls := r.recorded()
			if len(calls) != 1 || !errors.Is(calls[0].err, tt.value) {
				t.Errorf("hook calls = %+v, want one with the panic", calls)
			}
		})
	}
}

// panic-recovery / Scenario: Panic response without writing (the broken
// connection rows): nothing is to be written, and the hooks receive the
// panic error with a nil problem.
func TestRespondPanic_Broken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value error
	}{
		{name: "broken pipe", value: fmt.Errorf("write: %w", syscall.EPIPE)},
		{name: "connection reset", value: syscall.ECONNRESET},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			pe := &prblm.PanicError{Value: tt.value}
			if a := respondPanicWithin(t, prblm.NewHandler(prblm.OnError(r.hook)), pe); a.isWrite {
				t.Errorf("RespondPanic = %d %s to write, want nothing", a.res.Status, a.res.Body)
			}

			assertHookCalls(t, r.recorded(), pe, 0, 0)
		})
	}
}
