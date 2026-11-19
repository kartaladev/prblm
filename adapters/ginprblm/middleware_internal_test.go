package ginprblm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Design D2 failure mode: Respond panics despite its contract, as core
// v0.2.0 does for an error whose Unwrap panics. A nil handler, which Respond
// dereferences, is the one trigger every core version has: respond answers a
// bare 500 that cannot fail the same way, instead of letting the panic escape.
func TestRespond_Panics(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", http.NoBody)

	res, ok := respond(nil, req, errors.New("boom"))

	if !ok || res.Status != http.StatusInternalServerError || res.Body != nil || res.Header != nil {
		t.Errorf("respond = %+v, %v; want a bare 500 and true", res, ok)
	}
}
