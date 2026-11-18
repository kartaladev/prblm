package ginprblm_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/gin-gonic/gin"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/adapters/ginprblm"
)

func ExampleMiddleware() {
	errVesselNotFound := errors.New("vessel not found")

	engine := gin.New()
	engine.Use(ginprblm.Middleware(prblm.WithMappers(
		prblm.Status(errVesselNotFound, http.StatusNotFound),
	)))
	engine.GET("/v1/vessels/:id", func(c *gin.Context) {
		_ = c.Error(errVesselNotFound) // written as a problem response
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/vessels/7", http.NoBody)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	fmt.Println(rec.Code, rec.Header().Get("Content-Type"))
	fmt.Println(rec.Body.String())
	// Output:
	// 404 application/problem+json
	// {"title":"Not Found","status":404,"instance":"/v1/vessels/7"}
}

func ExampleMiddleware_abort() {
	engine := gin.New()
	engine.Use(ginprblm.Middleware())
	engine.GET("/v1/vessels/:id", func(c *gin.Context) {
		// The error's text is never sent; 403 is used as the status.
		_ = c.AbortWithError(http.StatusForbidden, errors.New("token abc expired"))
	})
	engine.GET("/v1/reports", func(c *gin.Context) {
		c.AbortWithStatus(http.StatusUnauthorized)
	})

	for _, target := range []string{"/v1/vessels/7", "/v1/reports"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		fmt.Println(rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	// Output:
	// 403 application/problem+json {"title":"Forbidden","status":403,"instance":"/v1/vessels/7"}
	// 401 application/problem+json {"title":"Unauthorized","status":401,"instance":"/v1/reports"}
}

func ExampleErrStatusOnly() {
	hook := prblm.OnError(func(_ context.Context, err error, p *prblm.Problem) {
		switch {
		case p == nil:
			// A note without a problem, such as a panic in a mapper.
			fmt.Println("note:", err)
		case errors.Is(err, ginprblm.ErrStatusOnly) && p.Status < http.StatusInternalServerError:
			// An expected abort, such as a 401: an ordinary response.
			fmt.Println("status-only abort:", p.Status)
		default:
			// A handler error, or a bare 5xx status: a failure.
			fmt.Println("failure:", p.Status, err)
		}
	})

	engine := gin.New()
	engine.Use(ginprblm.Middleware(hook))
	engine.GET("/v1/reports", func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) })
	engine.GET("/v1/exports", func(c *gin.Context) { c.Status(http.StatusServiceUnavailable) })
	engine.GET("/v1/vessels/:id", func(c *gin.Context) { _ = c.Error(errors.New("pq: connection refused")) })

	for _, target := range []string{"/v1/reports", "/v1/exports", "/v1/vessels/7"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
		engine.ServeHTTP(httptest.NewRecorder(), req)
	}
	// Output:
	// status-only abort: 401
	// failure: 503 ginprblm: response aborted with an error status and no error
	// failure: 500 pq: connection refused
}

func ExampleRecovery() {
	logProblem := func(_ context.Context, err error, _ *prblm.Problem) {
		if pe, ok := errors.AsType[*prblm.PanicError](err); ok {
			fmt.Println("hook:", pe.Value) // log the value and pe.Stack here
		}
	}
	opts := []prblm.Option{prblm.OnError(logProblem)}

	engine := gin.New()
	engine.Use(ginprblm.Recovery(opts...), ginprblm.Middleware(opts...))
	engine.GET("/v1/vessels/:id", func(*gin.Context) {
		panic("db password=hunter2")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/vessels/7", http.NoBody)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	fmt.Println(rec.Code, rec.Body.String())
	// Output:
	// hook: db password=hunter2
	// 500 {"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}
}

func ExampleScope() {
	errLocked := errors.New("locked")

	engine := gin.New()
	engine.Use(ginprblm.Middleware())
	engine.Group("/v1/contracts", ginprblm.Scope(prblm.Status(errLocked, http.StatusLocked))).
		GET("/:id", func(c *gin.Context) { _ = c.Error(errLocked) })
	engine.GET("/v1/vessels/:id", func(c *gin.Context) { _ = c.Error(errLocked) })

	for _, target := range []string{"/v1/contracts/1", "/v1/vessels/1"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		fmt.Println(target, rec.Code)
	}
	// Output:
	// /v1/contracts/1 423
	// /v1/vessels/1 500
}

func ExampleMiddleware_discovery() {
	outOfCredit := prblm.DefineType("out-of-credit", http.StatusForbidden, "Out of credit",
		"The account balance is below the price of the request.")

	engine := gin.New()
	// Installed with engine.Use, the middleware also serves the
	// documentation of the registered types for paths no route claims.
	engine.Use(ginprblm.Middleware(
		prblm.WithNamespace("/v1/api/probs"),
		prblm.WithTypes(outOfCredit),
	))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/api/probs/out-of-credit", http.NoBody)
	req.Header.Set("Accept", "application/json")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	fmt.Println(rec.Code, rec.Header().Get("Content-Type"))
	fmt.Println(rec.Body.String())
	// Output:
	// 200 application/json
	// {"type":"/v1/api/probs/out-of-credit","subtype":"out-of-credit","status":403,"title":"Out of credit","description":"The account balance is below the price of the request."}
}
