package prblm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/internal/xmlenc"
)

// mapping returns a handler whose only mapper returns p for every error.
func mapping(p *prblm.Problem) *prblm.Handler {
	return prblm.NewHandler(prblm.WithMappers(prblm.MapperFunc(func(context.Context, error) *prblm.Problem {
		return p
	})))
}

// problem-response / Scenario: Status consistency.
func TestRespond_Status(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		returned int
		status   int
	}{
		{name: "409", returned: 409, status: 409},
		{name: "404", returned: 404, status: 404},
		{name: "none", returned: 0, status: 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := write(t, mapping(&prblm.Problem{Status: tt.returned}), errors.New("x"))

			if rec.Code != tt.status {
				t.Errorf("HTTP status = %d, want %d", rec.Code, tt.status)
			}

			if got := decodeMembers(t, rec.Body.Bytes())["status"]; got != float64(tt.status) {
				t.Errorf("body status = %v, want %d", got, tt.status)
			}
		})
	}
}

// problem-response / Scenario: Headers on the response.
func TestRespond_Headers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		existing http.Header // set by the handler before it returns the error
		problem  http.Header
		expected http.Header // exact values per listed header
	}{
		{
			name:     "none",
			expected: http.Header{"Vary": {"Accept"}, "Cache-Control": {"no-store"}},
		},
		{
			name:     "existing vary kept",
			existing: http.Header{"Vary": {"Origin"}},
			expected: http.Header{"Vary": {"Origin", "Accept"}, "Cache-Control": {"no-store"}},
		},
		{
			name:     "existing vary accept not duplicated",
			existing: http.Header{"Vary": {"Accept"}},
			expected: http.Header{"Vary": {"Accept"}},
		},
		{
			name:     "existing cache control kept",
			existing: http.Header{"Cache-Control": {"private"}},
			expected: http.Header{"Cache-Control": {"private"}},
		},
		{
			name:     "problem cache control",
			problem:  http.Header{"Cache-Control": {"max-age=60"}},
			expected: http.Header{"Cache-Control": {"max-age=60"}, "Vary": {"Accept"}},
		},
		{
			name:     "problem retry after",
			problem:  http.Header{"Retry-After": {"5"}},
			expected: http.Header{"Retry-After": {"5"}, "Cache-Control": {"no-store"}},
		},
		// Murphy rows.
		{
			name:     "existing vary list with accept in other case",
			existing: http.Header{"Vary": {"origin, accept"}},
			expected: http.Header{"Vary": {"origin, accept"}},
		},
		{
			name:     "problem vary kept",
			problem:  http.Header{"Vary": {"Origin"}},
			expected: http.Header{"Vary": {"Origin", "Accept"}},
		},
		{
			name:     "problem and existing vary accept",
			existing: http.Header{"Vary": {"Accept"}},
			problem:  http.Header{"Vary": {"Accept"}},
			expected: http.Header{"Vary": {"Accept"}},
		},
		{
			name:     "non-canonical problem cache control",
			problem:  http.Header{"cache-control": {"max-age=60"}},
			expected: http.Header{"Cache-Control": {"max-age=60"}},
		},
		{
			name:     "existing cache control wins over the problem's",
			existing: http.Header{"Cache-Control": {"private"}},
			problem:  http.Header{"Cache-Control": {"max-age=60"}},
			expected: http.Header{"Cache-Control": {"private"}},
		},
		{
			name:     "problem header replaces the handler's",
			existing: http.Header{"Retry-After": {"1"}},
			problem:  http.Header{"Retry-After": {"5"}},
			expected: http.Header{"Retry-After": {"5"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := mapping(&prblm.Problem{Status: http.StatusNotFound, Header: tt.problem})
			srv := httptest.NewServer(h.Wrap(func(w http.ResponseWriter, _ *http.Request) error {
				maps.Copy(w.Header(), tt.existing)

				return errors.New("x")
			}))
			t.Cleanup(srv.Close)

			res := get(t, srv, "/v1/vessels/7")

			if res.status != http.StatusNotFound {
				t.Errorf("status = %d, want 404", res.status)
			}

			for name, expected := range tt.expected {
				if got := res.header.Values(name); !slices.Equal(got, expected) {
					t.Errorf("%s = %q, want %q", name, got, expected)
				}
			}
		})
	}
}

// problem-response / Scenario: Protected headers.
func TestRespond_HeadersProtected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header http.Header
	}{
		{name: "content type", header: http.Header{"Content-Type": {"text/html"}}},
		{name: "content length", header: http.Header{"Content-Length": {"1"}}},
		// Murphy rows: keys put straight into the map, bypassing canonicalization.
		{name: "non-canonical content type", header: http.Header{"content-type": {"text/html"}}},
		{name: "non-canonical content length", header: http.Header{"content-length": {"1"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := mapping(&prblm.Problem{Status: http.StatusNotFound, Header: tt.header})
			srv := httptest.NewServer(h.Wrap(func(http.ResponseWriter, *http.Request) error {
				return errors.New("x")
			}))
			t.Cleanup(srv.Close)

			res := get(t, srv, "/v1/vessels/7")

			if got := res.header.Values("Content-Type"); !slices.Equal(got, []string{problemJSONType}) {
				t.Errorf("Content-Type = %q, want exactly %q", got, problemJSONType)
			}

			if got, want := res.header.Values("Content-Length"), strconv.Itoa(len(res.body)); !slices.Equal(got, []string{want}) {
				t.Errorf("Content-Length = %q, want exactly the real body length %q", got, want)
			}

			if expected := `{"title":"Not Found","status":404,"instance":"/v1/vessels/7"}`; string(res.body) != expected {
				t.Errorf("body = %s, want %s", res.body, expected)
			}
		})
	}
}

const problemXMLType = "application/problem+xml"

// decodedProblem returns a problem decoded from JSON, whose extension names
// are not validated.
func decodedProblem(t *testing.T, doc string) *prblm.Problem {
	t.Helper()

	var p prblm.Problem
	if err := json.Unmarshal([]byte(doc), &p); err != nil {
		t.Fatal(err)
	}

	return &p
}

// problem-response / Scenario: Respond without writing (XML row).
func TestRespond_XML(t *testing.T) {
	t.Parallel()

	const xmlHead = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

	full := prblm.New(http.StatusForbidden,
		prblm.WithExt("balance", 30),
		prblm.WithExt("accounts", []string{"/a/1", "/a/2"}),
	)
	full.Type = "https://example.com/probs/out-of-credit"
	full.Title = "You do not have enough credit."
	full.Detail = "Your balance is 30 & that costs 50."

	tests := []struct {
		name        string
		problem     func(t *testing.T) *prblm.Problem
		accept      string
		status      int
		contentType string
		body        string
	}{
		{
			name:        "mapped error",
			problem:     func(*testing.T) *prblm.Problem { return prblm.New(http.StatusNotFound) },
			accept:      "application/xml",
			status:      404,
			contentType: problemXMLType,
			body:        xmlHead + `<problem xmlns="urn:ietf:rfc:7807"><title>Not Found</title><status>404</status><instance>/v1/vessels/7</instance></problem>`,
		},
		// Murphy rows.
		{
			name:        "every member",
			problem:     func(*testing.T) *prblm.Problem { return full },
			accept:      "application/problem+xml",
			status:      403,
			contentType: problemXMLType,
			body: xmlHead + `<problem xmlns="urn:ietf:rfc:7807">` +
				`<type>https://example.com/probs/out-of-credit</type>` +
				`<title>You do not have enough credit.</title><status>403</status>` +
				`<detail>Your balance is 30 &amp; that costs 50.</detail><instance>/v1/vessels/7</instance>` +
				`<accounts><i>/a/1</i><i>/a/2</i></accounts><balance>30</balance></problem>`,
		},
		{
			name: "about:blank omitted",
			problem: func(*testing.T) *prblm.Problem {
				return &prblm.Problem{Type: "about:blank", Status: http.StatusConflict}
			},
			accept:      "text/xml",
			status:      409,
			contentType: problemXMLType,
			body:        xmlHead + `<problem xmlns="urn:ietf:rfc:7807"><title>Conflict</title><status>409</status><instance>/v1/vessels/7</instance></problem>`,
		},
		{
			name:        "unacceptable accept falls back to JSON",
			problem:     func(*testing.T) *prblm.Problem { return prblm.New(http.StatusNotFound) },
			accept:      "image/png",
			status:      404,
			contentType: problemJSONType,
			body:        `{"title":"Not Found","status":404,"instance":"/v1/vessels/7"}`,
		},
		{
			name: "decoded name invalid in XML",
			problem: func(t *testing.T) *prblm.Problem {
				t.Helper()

				return decodedProblem(t, `{"status":409,"a b":1}`)
			},
			accept:      "application/xml",
			status:      500,
			contentType: problemJSONType,
			body:        `{"title":"Internal Server Error","status":500}`,
		},
		{
			name: "decoded name kept in JSON",
			problem: func(t *testing.T) *prblm.Problem {
				t.Helper()

				return decodedProblem(t, `{"status":409,"a b":1}`)
			},
			accept:      "application/json",
			status:      409,
			contentType: problemJSONType,
			body:        `{"title":"Conflict","status":409,"instance":"/v1/vessels/7","a b":1}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := mapping(tt.problem(t))
			req := prblm.Request{Method: http.MethodGet, Path: "/v1/vessels/7", Accept: tt.accept}

			res, ok := h.Respond(t.Context(), req, errVesselNotFound)
			if !ok {
				t.Fatal("Respond() reported nothing to write")
			}

			if res.Status != tt.status || res.Header.Get("Content-Type") != tt.contentType {
				t.Errorf("Respond() = %d %q, want %d %q", res.Status, res.Header.Get("Content-Type"), tt.status, tt.contentType)
			}

			if string(res.Body) != tt.body {
				t.Errorf("body = %s\nwant   %s", res.Body, tt.body)
			}

			if got, want := res.Header.Get("Content-Length"), strconv.Itoa(len(res.Body)); got != want {
				t.Errorf("Content-Length = %q, want %q", got, want)
			}

			if res.Header.Get("Vary") != "Accept" || res.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("Vary = %q, Cache-Control = %q; want Accept, no-store", res.Header.Get("Vary"), res.Header.Get("Cache-Control"))
			}
		})
	}
}

// XML documents encoded one after another, or concurrently, never share
// output: a document that failed or panicked halfway, or that outgrew the
// encoder's 4 KiB buffer, leaves nothing in the next one.
func TestRespond_XMLIsolated(t *testing.T) {
	t.Parallel()

	const (
		xmlHead  = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
		requests = 64
	)

	type outcome struct {
		problem *prblm.Problem
		body    string
	}

	outcomes := make([]outcome, requests)
	for i := range outcomes {
		p := prblm.New(http.StatusConflict)
		p.Detail = fmt.Sprintf("request %d %s", i, strings.Repeat("x", i%3*3000))

		body := xmlHead + `<problem xmlns="urn:ietf:rfc:7807"><title>Conflict</title><status>409</status>` +
			`<detail>` + p.Detail + `</detail><instance>/v1/vessels/7</instance></problem>`

		switch i % 4 {
		case 1: // fails after the members were written
			if err := p.Set("meta", map[string]int{"file.name": 1}); err != nil {
				t.Fatal(err)
			}

			body = `{"title":"Internal Server Error","status":500}`
		case 2: // panics after the members were written
			if err := p.Set("meta", panickyValue{}); err != nil {
				t.Fatal(err)
			}

			body = `{"title":"Internal Server Error","status":500}`
		}

		outcomes[i] = outcome{problem: p, body: body}
	}

	respond := func(t *testing.T, o outcome) {
		t.Helper()

		req := prblm.Request{Method: http.MethodGet, Path: "/v1/vessels/7", Accept: problemXMLType}

		res, ok := mapping(o.problem).Respond(t.Context(), req, errVesselNotFound)
		if !ok {
			t.Error("Respond() reported nothing to write")

			return
		}

		if string(res.Body) != o.body {
			t.Errorf("body = %.200s\nwant   %.200s", res.Body, o.body)
		}
	}

	t.Run("sequential", func(t *testing.T) {
		t.Parallel()

		for _, o := range outcomes {
			respond(t, o)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		t.Parallel()

		var wg sync.WaitGroup
		for _, o := range outcomes {
			wg.Go(func() { respond(t, o) })
		}

		wg.Wait()
	})
}

// panickyValue is an extension value whose JSON encoding panics.
type panickyValue struct{}

func (panickyValue) MarshalJSON() ([]byte, error) { panic("marshaler broke") }

// problem-response / Scenario: Encoding fails.
func TestRespond_EncodeFailure(t *testing.T) {
	t.Parallel()

	isType := func(err error) bool { _, ok := errors.AsType[*json.UnsupportedTypeError](err); return ok }
	isValue := func(err error) bool { _, ok := errors.AsType[*json.UnsupportedValueError](err); return ok }
	isPanic := func(err error) bool { _, ok := errors.AsType[*prblm.PanicError](err); return ok }

	isName := func(err error) bool { return errors.Is(err, xmlenc.ErrInvalidName) }

	tests := []struct {
		name     string
		accept   string
		ext      string
		value    any
		noHook   bool
		encoding func(error) bool // recognizes the encoding error the hook receives
		panics   bool             // the encoder panics: the note is a *PanicError, not ErrEncoding
	}{
		{name: "chan", accept: "application/json", ext: "bad", value: make(chan int), encoding: isType},
		{name: "xml name", accept: "application/xml", ext: "meta", value: map[string]int{"file.name": 1}, encoding: isName},
		{name: "infinity", ext: "ratio", value: math.Inf(1), encoding: isValue},
		// Murphy rows.
		{name: "panicking marshaler", ext: "meta", value: panickyValue{}, encoding: isPanic, panics: true},
		{name: "panicking marshaler without hook", ext: "meta", value: panickyValue{}, noHook: true, panics: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			opts := []prblm.Option{prblm.WithMappers(prblm.Status(errVesselNotFound, http.StatusConflict))}
			if !tt.noHook {
				opts = append(opts, prblm.OnError(r.hook))
			}

			h := prblm.NewHandler(append(opts, prblm.WithEnrichers(func(_ context.Context, p *prblm.Problem) {
				if err := p.Set(tt.ext, tt.value); err != nil {
					t.Errorf("Set(%q) = %v", tt.ext, err)
				}
			}))...)

			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/vessels/7", http.NoBody)
			req.Header.Set("Accept", tt.accept)
			h.Write(rec, req, errVesselNotFound)

			assertMinimalProblem(t, rec)

			if tt.noHook {
				return
			}

			calls := r.recorded()
			if len(calls) != 2 {
				t.Fatalf("hook called %d times, want 2 (encoding error, then the error): %+v", len(calls), calls)
			}

			if got := errors.Is(calls[0].err, prblm.ErrEncoding); got == tt.panics {
				t.Errorf("errors.Is(first hook call, ErrEncoding) = %v, want %v", got, !tt.panics)
			}

			if !tt.encoding(calls[0].err) {
				t.Errorf("first hook call got %v (%T), want the encoding error", calls[0].err, calls[0].err)
			}

			//nolint:errorlint // the last call must carry the original error value itself.
			if last := calls[1]; last.err != errVesselNotFound || last.status != http.StatusInternalServerError {
				t.Errorf("last hook call = %+v, want the original error with the 500 that was sent", last)
			}
		})
	}
}

// assertMinimalProblem checks that rec holds the minimal 500 problem sent
// when the resolved problem could not be encoded.
func assertMinimalProblem(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}

	if got := rec.Header().Get("Content-Type"); got != problemJSONType {
		t.Errorf("Content-Type = %q, want %q", got, problemJSONType)
	}

	if got := rec.Body.String(); got != `{"title":"Internal Server Error","status":500}` {
		t.Errorf("body = %s, want the minimal problem", got)
	}

	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(rec.Body.Len()); got != want {
		t.Errorf("Content-Length = %q, want %q", got, want)
	}
}

// error-mapping / Scenario: Request context canceled; problem-response /
// Scenario: Respond without writing (canceled row).
func TestRespond_Canceled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		canceled bool
		err      error
		status   int
	}{
		{name: "canceled request, canceled error", canceled: true, err: fmt.Errorf("query: %w", context.Canceled), status: 500},
		{name: "active request, canceled error", canceled: false, err: context.Canceled, status: 500},
		{name: "canceled request, other error", canceled: true, err: errVesselNotFound, status: 404},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			if tt.canceled {
				cancel()
			}

			var r recorder

			h := prblm.NewHandler(prblm.WithMappers(prblm.Status(errVesselNotFound, 404)), prblm.OnError(r.hook))
			res, ok := h.Respond(ctx, prblm.Request{Method: http.MethodGet, Path: "/v1/vessels/7"}, tt.err)

			if !ok || res.Status != tt.status || res.Header.Get("Content-Type") != problemJSONType {
				t.Errorf("Respond() = %d %q, %v; want a %d problem", res.Status, res.Header.Get("Content-Type"), ok, tt.status)
			}

			if calls := r.recorded(); len(calls) != 1 {
				t.Errorf("hook called %d times, want 1", len(calls))
			}
		})
	}
}

// problem-response / Scenario: Headers on the response, as adapters merge
// them into a framework's header (design D2 of add-framework-adapters).
func TestResponse_MergeInto(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dst      http.Header
		src      http.Header
		expected http.Header
	}{
		{
			name:     "response header replaces the handler's",
			dst:      http.Header{"Content-Type": {"text/plain"}, "X-Request-Id": {"r1"}},
			src:      http.Header{"Content-Type": {"application/problem+json"}},
			expected: http.Header{"Content-Type": {"application/problem+json"}, "X-Request-Id": {"r1"}},
		},
		{
			name:     "vary merged without duplicates",
			dst:      http.Header{"Vary": {"Origin, accept"}},
			src:      http.Header{"Vary": {"Accept"}},
			expected: http.Header{"Vary": {"Origin, accept"}},
		},
		{
			name:     "vary adds accept to the handler's names",
			dst:      http.Header{"Vary": {"Origin"}},
			src:      http.Header{"Vary": {"Accept"}},
			expected: http.Header{"Vary": {"Origin", "Accept"}},
		},
		{
			name:     "handler cache-control kept",
			dst:      http.Header{"Cache-Control": {"max-age=5"}},
			src:      http.Header{"Cache-Control": {"no-store"}},
			expected: http.Header{"Cache-Control": {"max-age=5"}},
		},
		{
			name:     "response cache-control used when the handler set none",
			dst:      http.Header{},
			src:      http.Header{"Cache-Control": {"no-store"}},
			expected: http.Header{"Cache-Control": {"no-store"}},
		},
		// A handler may write the header map directly, under a key net/http
		// sends as written: every spelling of a response header name is that
		// header (problem-response / Scenario: A handler header under a
		// non-canonical key).
		{
			name:     "handler cache-control under a lowercase key kept",
			dst:      http.Header{"cache-control": {"max-age=5"}},
			src:      http.Header{"Cache-Control": {"no-store"}},
			expected: http.Header{"Cache-Control": {"max-age=5"}},
		},
		{
			name:     "vary under a lowercase key gains accept",
			dst:      http.Header{"vary": {"Origin"}},
			src:      http.Header{"Vary": {"Accept"}},
			expected: http.Header{"Vary": {"Origin", "Accept"}},
		},
		{
			name:     "vary under a lowercase key already listing accept",
			dst:      http.Header{"vary": {"accept"}},
			src:      http.Header{"Vary": {"Accept"}},
			expected: http.Header{"Vary": {"accept"}},
		},
		{
			name:     "every spelling of a replaced header removed",
			dst:      http.Header{"content-type": {"text/plain"}, "CONTENT-TYPE": {"text/html"}},
			src:      http.Header{"Content-Type": {"application/problem+json"}},
			expected: http.Header{"Content-Type": {"application/problem+json"}},
		},
		{
			name:     "spellings combined, canonical first, then sorted",
			dst:      http.Header{"cache-control": {"c"}, "Cache-Control": {"a"}, "CACHE-CONTROL": {"b"}},
			src:      http.Header{"Cache-Control": {"no-store"}},
			expected: http.Header{"Cache-Control": {"a", "b", "c"}},
		},
		{
			name:     "spelling without values counts as no value",
			dst:      http.Header{"cache-control": nil},
			src:      http.Header{"Cache-Control": {"no-store"}},
			expected: http.Header{"Cache-Control": {"no-store"}},
		},
		{
			name:     "unrelated non-canonical key untouched",
			dst:      http.Header{"x-request-id": {"r1"}},
			src:      http.Header{"Content-Type": {"application/problem+json"}},
			expected: http.Header{"x-request-id": {"r1"}, "Content-Type": {"application/problem+json"}},
		},
		{
			name:     "spelling of a long name replaced",
			dst:      http.Header{strings.ToLower(longHeaderName): {"handler"}},
			src:      http.Header{longHeaderName: {"problem"}},
			expected: http.Header{longHeaderName: {"problem"}},
		},
		{
			name:     "key that is no valid field name untouched",
			dst:      http.Header{"cache control": {"max-age=5"}},
			src:      http.Header{"Cache-Control": {"no-store"}},
			expected: http.Header{"cache control": {"max-age=5"}, "Cache-Control": {"no-store"}},
		},
		{
			name:     "empty response header changes nothing",
			dst:      http.Header{"X-Request-Id": {"r1"}},
			src:      nil,
			expected: http.Header{"X-Request-Id": {"r1"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res := prblm.Response{Header: tt.src}
			res.MergeInto(tt.dst)

			if !maps.EqualFunc(tt.dst, tt.expected, slices.Equal[[]string]) {
				t.Errorf("MergeInto: header = %v, want %v", tt.dst, tt.expected)
			}
		})
	}
}

// problem-response / Scenario: Replaced headers win over existing ones, at
// the merge every adapter shares. The marks only reach a Response through
// Respond, so each response is rendered from a problem built with fields.
func TestResponse_MergeInto_Replaced(t *testing.T) {
	t.Parallel()

	// dropLink deletes Link after it was marked, leaving the mark inert.
	dropLink := prblm.Field(func(p *prblm.Problem) { p.Header.Del("Link") })

	tests := []struct {
		name     string
		dst      http.Header
		fields   []prblm.Field
		header   string
		expected []string
	}{
		{
			name:     "marked cache-control replaces the destination's",
			dst:      http.Header{"Cache-Control": {"public, max-age=600"}},
			fields:   []prblm.Field{prblm.WithReplacedHeader("Cache-Control", "no-store")},
			header:   "Cache-Control",
			expected: []string{"no-store"},
		},
		{
			name:     "unmarked cache-control keeps the destination's",
			dst:      http.Header{"Cache-Control": {"public, max-age=600"}},
			fields:   []prblm.Field{prblm.WithHeader("Cache-Control", "no-store")},
			header:   "Cache-Control",
			expected: []string{"public, max-age=600"},
		},
		{
			name:     "marked vary replaces the destination's and lists accept",
			dst:      http.Header{"Vary": {"Origin"}},
			fields:   []prblm.Field{prblm.WithReplacedHeader("Vary", "Cookie")},
			header:   "Vary",
			expected: []string{"Cookie", "Accept"},
		},
		{
			name:     "unmarked vary adds to the destination's",
			dst:      http.Header{"Vary": {"Origin"}},
			fields:   []prblm.Field{prblm.WithHeader("Vary", "Cookie")},
			header:   "Vary",
			expected: []string{"Origin", "Cookie", "Accept"},
		},
		{
			name:     "marked cache-control replaces every spelling in the destination",
			dst:      http.Header{"cache-control": {"public, max-age=600"}, "Cache-Control": {"private"}},
			fields:   []prblm.Field{prblm.WithReplacedHeader("Cache-Control", "no-store")},
			header:   "Cache-Control",
			expected: []string{"no-store"},
		},
		{
			name:     "marked vary replaces a lowercase destination key",
			dst:      http.Header{"vary": {"Origin"}},
			fields:   []prblm.Field{prblm.WithReplacedHeader("Vary", "Cookie")},
			header:   "Vary",
			expected: []string{"Cookie", "Accept"},
		},
		// Murphy rows.
		{
			name:     "mark of a deleted header is inert",
			dst:      http.Header{"Link": {"</old>; rel=x"}},
			fields:   []prblm.Field{prblm.WithReplacedHeader("Link", "</new>; rel=x"), dropLink},
			header:   "Link",
			expected: []string{"</old>; rel=x"},
		},
		{
			name: "fallback response carries no marks",
			dst:  http.Header{"Cache-Control": {"public, max-age=600"}},
			fields: []prblm.Field{
				prblm.WithReplacedHeader("Cache-Control", "no-store"),
				prblm.WithExt("bad", make(chan int)),
			},
			header:   "Cache-Control",
			expected: []string{"public, max-age=600"},
		},
		{
			name:   "nil destination",
			fields: []prblm.Field{prblm.WithReplacedHeader("Cache-Control", "no-store")},
			header: "Cache-Control",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := prblm.Request{Method: http.MethodGet, Path: "/v1/vessels/7"}

			res, ok := mapping(prblm.New(http.StatusServiceUnavailable, tt.fields...)).Respond(t.Context(), req, errVesselNotFound)
			if !ok {
				t.Fatal("Respond() reported nothing to write")
			}

			res.MergeInto(tt.dst)

			if got := sentValues(tt.dst, tt.header); !slices.Equal(got, tt.expected) {
				t.Errorf("%s = %q, want %q", tt.header, got, tt.expected)
			}
		})
	}
}

func TestResponse_MergeInto_NilDestination(t *testing.T) {
	t.Parallel()

	res := prblm.Response{Header: http.Header{"Content-Type": {"application/problem+json"}}}

	// Nothing to merge into: it must not panic.
	res.MergeInto(nil)
}

// Each merged header's values stand alone: appending to one never changes
// another, nor the response's own header, whatever storage the merge used.
func TestResponse_MergeInto_ValuesIndependent(t *testing.T) {
	t.Parallel()

	// Values with spare capacity, so a merge that kept the response's slices
	// would let an append write into them.
	withRoom := func(values ...string) []string { return append(make([]string, 0, 8), values...) }

	res := prblm.Response{Header: http.Header{
		"X-A":           withRoom("a1"),
		"X-B":           withRoom("b1", "b2"),
		"X-C":           withRoom("c1"),
		"Cache-Control": withRoom("no-store"),
	}}
	before := res.Header.Clone()
	dst := http.Header{}

	res.MergeInto(dst)

	for name := range res.Header {
		dst[name] = append(dst[name], "added")
	}

	for name, values := range before {
		if want := append(slices.Clone(values), "added"); !slices.Equal(dst[name], want) {
			t.Errorf("%s = %q after appending to every header, want %q", name, dst[name], want)
		}

		if got := res.Header[name][:cap(res.Header[name])][len(values)]; got != "" {
			t.Errorf("response header %s gained %q in its spare capacity after appending to the merged one", name, got)
		}
	}
}

// A header the response holds without values is merged as it is: present,
// with nil values, and an empty list stays empty.
func TestResponse_MergeInto_NoValues(t *testing.T) {
	t.Parallel()

	res := prblm.Response{Header: http.Header{"X-Nil": nil, "X-Empty": {}}}
	dst := http.Header{"X-Nil": {"old"}, "X-Empty": {"old"}}

	res.MergeInto(dst)

	if values, ok := dst["X-Nil"]; !ok || values != nil {
		t.Errorf("X-Nil = %q (present %v), want present with nil values", values, ok)
	}

	if values, ok := dst["X-Empty"]; !ok || values == nil || len(values) != 0 {
		t.Errorf("X-Empty = %#v (present %v), want present and empty", values, ok)
	}
}

// The headers of a response stand alone the same way.
func TestRespond_HeaderValuesIndependent(t *testing.T) {
	t.Parallel()

	req := prblm.Request{Method: http.MethodGet, Path: "/v1/vessels/7"}

	res, ok := mapping(prblm.New(http.StatusNotFound)).Respond(t.Context(), req, errVesselNotFound)
	if !ok {
		t.Fatal("Respond() reported nothing to write")
	}

	before := res.Header.Clone()

	for name := range res.Header {
		res.Header[name] = append(res.Header[name], "added")
	}

	for name, values := range before {
		if want := append(slices.Clone(values), "added"); !slices.Equal(res.Header[name], want) {
			t.Errorf("%s = %q after appending to every header, want %q", name, res.Header[name], want)
		}
	}
}

func TestResponse_MergeInto_NoAliasing(t *testing.T) {
	t.Parallel()

	res := prblm.Response{Header: http.Header{"X-Trace": {"t1"}, "Cache-Control": {"no-store"}}}
	dst := http.Header{}

	res.MergeInto(dst)

	for name, want := range map[string]string{"X-Trace": "t1", "Cache-Control": "no-store"} {
		dst[name][0] = "changed"

		if got := res.Header.Get(name); got != want {
			t.Errorf("response header %s = %q after changing the destination, want %q", name, got, want)
		}
	}
}

// panicRequest is the request the panic-recovery scenarios answer.
var panicRequest = prblm.Request{Method: http.MethodGet, Path: "/v1/vessels/7"}

// panicAnswer is what RespondPanic returns.
type panicAnswer struct {
	res     prblm.Response
	isWrite bool
}

// respondPanicWithin calls h.RespondPanic for panicRequest under the guard
// of within.
func respondPanicWithin(t *testing.T, h *prblm.Handler, pe *prblm.PanicError) panicAnswer {
	t.Helper()

	return within(t, "RespondPanic", func() panicAnswer {
		res, ok := h.RespondPanic(t.Context(), panicRequest, pe)

		return panicAnswer{res: res, isWrite: ok}
	})
}

// assertSafe500 checks that a is the safe 500 problem, to be written.
func assertSafe500(t *testing.T, a panicAnswer) {
	t.Helper()

	if !a.isWrite {
		t.Fatal("RespondPanic reported nothing to write, want the safe 500 problem")
	}

	if a.res.Status != 500 || string(a.res.Body) != safe500 {
		t.Errorf("response = %d %s, want 500 %s", a.res.Status, a.res.Body, safe500)
	}

	if got := a.res.Header.Get("Content-Type"); got != problemJSONType {
		t.Errorf("Content-Type = %q, want %s", got, problemJSONType)
	}
}

// assertPanicHooks checks that the hooks received pe with the 500 problem,
// after an ErrUnwalkableError note naming reason, unless reason is "". The
// note of reason "panic" wraps the "boom" panic.
func assertPanicHooks(t *testing.T, calls []hookCall, pe error, reason string) {
	t.Helper()

	if reason == "" {
		assertHookCalls(t, calls, pe, 500, 0)

		return
	}

	if note := assertNoted(t, calls, pe, reason); note != nil && !isBoom(note.Value) {
		t.Errorf("note panic value = %#v, want boom", note.Value)
	}
}

// assertNilPanic checks that the only hook call is a panic error with value
// nil, with the 500 problem.
func assertNilPanic(t *testing.T, calls []hookCall) {
	t.Helper()

	if len(calls) != 1 {
		t.Fatalf("hook calls = %+v, want one", calls)
	}

	if pe, ok := errors.AsType[*prblm.PanicError](calls[0].err); !ok || pe.Value != nil || calls[0].status != 500 {
		t.Errorf("hook call = %v with status %d, want a panic error with value nil and 500", calls[0].err, calls[0].status)
	}
}

// panic-recovery / Scenario: Panic response without writing. The broken
// connection rows are TestRespondPanic_Broken, which Plan 9 cannot build,
// and the panic(nil) row is TestRespondPanic_PanicNil.
func TestRespondPanic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		// reason is the reason of the ErrUnwalkableError note, "" for none.
		reason string
	}{
		{name: "plain value", value: "boom"},
		{name: "self-loop", value: newLoop("loop"), reason: "budget"},
		{name: "is method panics", value: isPanicError{}, reason: "panic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			pe := &prblm.PanicError{Value: tt.value}
			assertSafe500(t, respondPanicWithin(t, prblm.NewHandler(prblm.OnError(r.hook)), pe))

			assertPanicHooks(t, r.recorded(), pe, tt.reason)
		})
	}

	// Murphy row: the walk aborts with a panic value that cannot be
	// formatted; building the note must not format it.
	t.Run("unformattable abort value", func(t *testing.T) {
		t.Parallel()

		var r recorder

		pe := &prblm.PanicError{Value: hostileIsError{}}
		assertSafe500(t, respondPanicWithin(t, prblm.NewHandler(prblm.OnError(r.hook)), pe))

		if calls := r.recorded(); len(calls) != 2 || !errors.Is(calls[0].err, prblm.ErrUnwalkableError) || !isSameError(calls[1].err, pe) {
			t.Errorf("hook calls = %d, want the note, then the panic error", len(calls))
		}
	})

	t.Run("nil panic error", func(t *testing.T) {
		t.Parallel()

		var r recorder

		assertSafe500(t, respondPanicWithin(t, prblm.NewHandler(prblm.OnError(r.hook)), nil))
		assertNilPanic(t, r.recorded())
	})

	// Murphy row: a handler that was never created.
	t.Run("nil problem handler", func(t *testing.T) {
		t.Parallel()

		msg := mustPanic(t, func() {
			(*prblm.Handler)(nil).RespondPanic(t.Context(), panicRequest, &prblm.PanicError{Value: "boom"})
		})
		if !strings.Contains(msg, "Handler.RespondPanic") {
			t.Errorf("panic %q does not name RespondPanic", msg)
		}
	})
}

// panic-recovery / Scenario: Panic response without writing (the panic(nil)
// row). The row needs GODEBUG=panicnil=1, so the test runs itself again in
// a subprocess with that setting.
func TestRespondPanic_PanicNil(t *testing.T) {
	t.Parallel()

	if rerunUnderPanicNil(t) {
		return
	}

	var r recorder

	h := prblm.NewHandler(prblm.OnError(r.hook))

	// What an adapter's recovery does: recover, then answer.
	recovered := func() (a panicAnswer) {
		defer func() {
			a = respondPanicWithin(t, h, &prblm.PanicError{Value: recover(), Stack: debug.Stack()})
		}()

		panic(nilValue)
	}

	assertSafe500(t, recovered())
	assertNilPanic(t, r.recorded())
}

// longHeaderName is a canonical header name of 73 bytes: a spelling of a
// long name folds like any other.
var longHeaderName = "X-" + strings.Repeat("Long-", 13) + "Header"
