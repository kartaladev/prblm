package statushint_test

import (
	"errors"
	"testing"

	"github.com/kartaladev/prblm/internal/statushint"
)

func TestWrap(t *testing.T) {
	t.Parallel()

	inner := errors.New("vessel 7 is locked")
	err := statushint.Wrap(inner, 409)

	hinted, ok := err.(*statushint.Error)                   //nolint:errorlint // the wrapper itself.
	if !ok || hinted.Err != inner || hinted.Status != 409 { //nolint:errorlint // inner itself.
		t.Fatalf("Wrap = %#v, want inner with status 409", err)
	}

	if err.Error() != inner.Error() {
		t.Errorf("Error() = %q, want the wrapped error's message %q", err.Error(), inner.Error())
	}

	if !errors.Is(err, inner) {
		t.Error("errors.Is does not see the wrapped error")
	}
}

// A nil error stays nil, so an adapter never turns "no error" into one.
func TestWrap_Nil(t *testing.T) {
	t.Parallel()

	if err := statushint.Wrap(nil, 409); err != nil {
		t.Errorf("Wrap(nil) = %#v, want nil", err)
	}
}
