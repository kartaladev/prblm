package fiberprblm

import (
	"errors"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
)

// Design D2 failure mode: Respond panics despite its contract. A nil
// handler, which Respond rejects with a panic, is a trigger that needs no
// core bug: respond answers a bare 500 that cannot fail the same way,
// instead of letting the panic escape.
func TestRespond_Panics(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	c := app.AcquireCtx(&fasthttp.RequestCtx{})

	t.Cleanup(func() { app.ReleaseCtx(c) })

	res, ok := respond(nil, c, errors.New("boom"))

	if !ok || res.Status != http.StatusInternalServerError || res.Body != nil || res.Header != nil {
		t.Errorf("respond = %+v, %v; want a bare 500 and true", res, ok)
	}
}
