package ginprblm_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/adapters/ginprblm"
)

// Design D3 (CLAUDE.md rule 8): gin commits the response header on abort,
// so a middleware that runs afterwards could no longer set the status or the
// Content-Type. Task 3.1 proved it against a plain middleware (see the task
// notes); through Middleware the problem keeps full control of both.
func TestGinAbort_CommitsHeaderEarly(t *testing.T) {
	t.Parallel()

	errX := errors.New("x")

	aborts := []struct {
		name    string
		handler gin.HandlerFunc
		status  int
	}{
		{
			name:    "abort with status",
			handler: func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) },
			status:  http.StatusUnauthorized,
		},
		{
			name:    "abort with error",
			handler: func(c *gin.Context) { _ = c.AbortWithError(http.StatusForbidden, errX) },
			status:  http.StatusForbidden,
		},
	}

	for _, tt := range aborts {
		engine := newEngine(tt.handler)

		t.Run(tt.name+"/recorder", func(t *testing.T) {
			t.Parallel()

			rec := serve(t, engine, get(t, "/x"))
			assertProblemHeader(t, rec.Code, rec.Header(), tt.status)
		})

		t.Run(tt.name+"/server", func(t *testing.T) {
			t.Parallel()

			rec := fetch(t, engine, "/x")
			assertProblemHeader(t, rec.Code, rec.Header(), tt.status)
		})
	}
}

func assertProblemHeader(t *testing.T, status int, header http.Header, want int) {
	t.Helper()

	if status != want {
		t.Errorf("status = %d, want %d", status, want)
	}

	if ct := header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
}

// gin-adapter / Scenario: AbortWithError.
func TestMiddleware_AbortWithError(t *testing.T) {
	t.Parallel()

	lockedMapper := prblm.Is(ErrLocked, func(error) *prblm.Problem {
		return prblm.New(http.StatusLocked, prblm.WithDetail("Vessel is locked."))
	})
	conflict := prblm.New(http.StatusConflict)

	tests := []struct {
		name        string
		mappers     []prblm.Mapper
		abortStatus int
		err         error
		status      int
		detail      string
		hidden      string // error text that must not reach the client
	}{
		// hint used
		{name: "hint 403", abortStatus: http.StatusForbidden, err: errors.New("nope"), status: http.StatusForbidden, hidden: "nope"},
		{name: "hint 400 hides text", abortStatus: http.StatusBadRequest, err: errors.New("secret"), status: http.StatusBadRequest, hidden: "secret"},
		{name: "hint 404 for unmapped sentinel", abortStatus: http.StatusNotFound, err: ErrVesselNotFound, status: http.StatusNotFound},
		// mapper wins
		{
			name:        "mapper over hint",
			mappers:     []prblm.Mapper{lockedMapper},
			abortStatus: http.StatusBadRequest,
			err:         ErrLocked,
			status:      http.StatusLocked,
			detail:      "Vessel is locked.",
		},
		{name: "self-describing over hint", abortStatus: http.StatusBadRequest, err: conflict, status: http.StatusConflict},
		// invalid hints
		{name: "success status is no hint", abortStatus: http.StatusOK, err: errors.New("x-text"), status: http.StatusInternalServerError, hidden: "x-text"},
		{name: "redirect status is no hint", abortStatus: http.StatusFound, err: errors.New("x-text"), status: http.StatusInternalServerError, hidden: "x-text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			abort := func(c *gin.Context) { _ = c.AbortWithError(tt.abortStatus, tt.err) }
			engine := newEngine(abort, prblm.WithMappers(tt.mappers...))

			rec := serve(t, engine, get(t, "/x"))

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}

			body := rec.Body.String()

			if tt.hidden != "" && strings.Contains(body, tt.hidden) {
				t.Errorf("body %q leaks the error text %q", body, tt.hidden)
			}

			if p := decodeRecorded(t, rec); p.Detail != tt.detail {
				t.Errorf("detail = %q, want %q", p.Detail, tt.detail)
			}
		})
	}
}

// gin-adapter / Scenario: AbortWithStatus and plain statuses.
func TestMiddleware_StatusOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler gin.HandlerFunc
		status  int
		title   string // set for problem rows
		body    string // expected body for pass-through rows
	}{
		// problems
		{name: "abort 401", handler: func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }, status: http.StatusUnauthorized, title: "Unauthorized"},
		{name: "abort 429", handler: func(c *gin.Context) { c.AbortWithStatus(http.StatusTooManyRequests) }, status: http.StatusTooManyRequests, title: "Too Many Requests"},
		{name: "status 500 without body", handler: func(c *gin.Context) { c.Status(http.StatusInternalServerError) }, status: http.StatusInternalServerError, title: "Internal Server Error"},
		// passed through
		{name: "status 204", handler: func(c *gin.Context) { c.Status(http.StatusNoContent) }, status: http.StatusNoContent},
		{name: "status 304", handler: func(c *gin.Context) { c.Status(http.StatusNotModified) }, status: http.StatusNotModified},
		{name: "abort 200", handler: func(c *gin.Context) { c.AbortWithStatus(http.StatusOK) }, status: http.StatusOK},
		{name: "json 200", handler: func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"id": 7}) }, status: http.StatusOK, body: `{"id":7}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			engine := newEngine(tt.handler)

			rec := fetch(t, engine, "/x")
			ct := rec.Header().Get("Content-Type")

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			if tt.title == "" {
				if strings.HasPrefix(ct, "application/problem") {
					t.Errorf("Content-Type = %q, want no problem", ct)
				}

				if body := rec.Body.String(); body != tt.body {
					t.Errorf("body = %q, want %q", body, tt.body)
				}

				return
			}

			if ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}

			if p := decodeRecorded(t, rec); p.Title != tt.title {
				t.Errorf("title = %q, want %q", p.Title, tt.title)
			}
		})
	}
}

// gin-adapter / Scenario: Body written before or after an error.
func TestMiddleware_BodyWritten(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler gin.HandlerFunc
		status  int
		body    string
		hookErr error // nil: the hook is not called
	}{
		{
			name:    "abort with status json",
			handler: func(c *gin.Context) { c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"code": "x"}) },
			status:  http.StatusBadRequest,
			body:    `{"code":"x"}`,
		},
		{
			name: "error after body",
			handler: func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
				_ = c.Error(ErrLocked)
			},
			status:  http.StatusOK,
			body:    `{"ok":true}`,
			hookErr: ErrLocked,
		},
		{
			name: "body after error",
			handler: func(c *gin.Context) {
				_ = c.Error(ErrLocked)
				c.String(http.StatusConflict, "locked")
			},
			status:  http.StatusConflict,
			body:    "locked",
			hookErr: ErrLocked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			engine := newEngine(tt.handler, hook.option())

			rec := fetch(t, engine, "/x")

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			if body := rec.Body.String(); body != tt.body {
				t.Errorf("body = %q, want %q", body, tt.body)
			}

			assertHook(t, &hook, tt.hookErr)
		})
	}
}

// gin-adapter / Scenario: Client went away. An error whose chain cannot be
// walked is answered with the safe 500, as if the client were still there:
// the hook receives a note, then the error.
func TestMiddleware_ClientGone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		canceled  bool
		err       error
		isWritten bool
		reason    string // the note's reason, "" for no note
		noteValue any    // the panic value the note wraps, for reason "panic"
	}{
		{name: "canceled request", canceled: true, err: fmt.Errorf("query: %w", context.Canceled), isWritten: false},
		{name: "active request", canceled: false, err: context.Canceled, isWritten: true},
		{name: "canceled, unwrap returns itself", canceled: true, err: &loopError{}, isWritten: true, reason: "budget"},
		{
			name:      "canceled, is panics",
			canceled:  true,
			err:       isPanicError{},
			isWritten: true,
			reason:    "panic",
			noteValue: "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			engine := gin.New()
			engine.Use(ginprblm.Middleware(hook.option()))
			engine.GET("/v1/vessels/7", func(c *gin.Context) { record(c, tt.err) })

			ctx, cancel := context.WithCancel(t.Context())
			if tt.canceled {
				cancel()
			} else {
				t.Cleanup(cancel)
			}

			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/vessels/7", http.NoBody)
			rec := serveWithin(t, engine, req)

			assertSafe500(t, rec, tt.isWritten)

			if tt.reason != "" {
				assertNoted(t, &hook, tt.reason, tt.noteValue, isSame(tt.err))
			} else {
				assertHookOnce(t, &hook, isSame(tt.err))
			}
		})
	}
}

// canceledTargetPanicError wraps err, and its Is method panics only when asked
// for context.Canceled, so only the middleware's client-gone walk aborts.
type canceledTargetPanicError struct{ err error }

func (canceledTargetPanicError) Error() string { return "hostile to the client-gone walk" }

func (e canceledTargetPanicError) Unwrap() error { return e.err }

func (canceledTargetPanicError) Is(target error) bool {
	if target == context.Canceled {
		panic("boom")
	}

	return false
}

// gin-adapter / Scenario: Client went away, the row whose chain only the
// middleware cannot walk. The client-gone walk aborts on a panic for
// context.Canceled alone, so the error counts as from a client still there,
// and the core, whose walk finishes, answers it as usual: a 404 for the
// ErrNotFound inside it, and the hook receives the error alone, with no
// note.
func TestMiddleware_ClientGone_OnlyTheCheckAborts(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	err := canceledTargetPanicError{err: prblm.ErrNotFound}
	engine := gin.New()
	engine.Use(ginprblm.Middleware(hook.option()))
	engine.GET("/v1/vessels/7", func(c *gin.Context) { record(c, err) })

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rec := serveWithin(t, engine, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/vessels/7", http.NoBody))

	if ct := rec.Header().Get("Content-Type"); rec.Code != http.StatusNotFound || ct != "application/problem+json" {
		t.Errorf("response = %d %q, want a 404 application/problem+json problem", rec.Code, ct)
	}

	assertHookOnce(t, &hook, isSame(err))
}

// Design D3 failure mode: middleware registered before ours commits the
// header early. Nothing malformed is written and the error reaches the hook.
func TestMiddleware_RegisteredLate(t *testing.T) {
	t.Parallel()

	var hook hookRecorder

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Status(http.StatusUnauthorized)
		c.Writer.WriteHeaderNow()
		c.Next()
	})
	engine.Use(ginprblm.Middleware(hook.option()))
	engine.GET("/x", func(c *gin.Context) { _ = c.Error(ErrLocked) })

	rec := fetch(t, engine, "/x")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want the early 401", rec.Code)
	}

	if body := rec.Body.String(); body != "" {
		t.Errorf("body = %q, want nothing written after the committed header", body)
	}

	assertHook(t, &hook, ErrLocked)
}

// wrapN returns err wrapped n times with %w.
func wrapN(err error, n int) error {
	for range n {
		err = fmt.Errorf("w: %w", err)
	}

	return err
}

// error-chain-walk / Scenario: Budget boundary, the adapter rows. The status
// hint of c.AbortWithError is not an entry of the error's chain, so it costs
// nothing of the budget: a 1,000-entry error is walked to its end, where a
// mapper recognizes ErrLocked, and a 1,001-entry error still aborts.
func TestMiddleware_AbortWithError_Budget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries int
		status  int
	}{
		{name: "1,000 entries", entries: 1000, status: http.StatusLocked},
		{name: "1,001 entries", entries: 1001, status: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hook hookRecorder

			err := wrapN(ErrLocked, tt.entries-1)
			engine := gin.New()
			engine.Use(ginprblm.Middleware(hook.option(), prblm.WithMappers(prblm.Status(ErrLocked, http.StatusLocked))))
			engine.GET("/v1/vessels/7", func(c *gin.Context) { _ = c.AbortWithError(http.StatusConflict, err) })

			rec := serveWithin(t, engine, get(t, "/v1/vessels/7"))

			// The hook receives err with the hint around it, and nothing
			// here walks it.
			isHinted := func(e error) bool {
				u, ok := e.(interface{ Unwrap() error })

				return ok && u.Unwrap() == err //nolint:errorlint // err itself.
			}

			if tt.status == http.StatusInternalServerError {
				assertSafe500(t, rec, true)
				assertNoted(t, &hook, "budget", nil, isHinted)

				return
			}

			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}

			assertHookOnce(t, &hook, isHinted)
		})
	}
}
