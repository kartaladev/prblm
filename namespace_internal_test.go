package prblm

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// problem-types / Scenario: Invalid relative type, the malformed sub-types
// as unit cases; the valid ones come from the definition scenarios.
func TestIsSubtype(t *testing.T) {
	t.Parallel()

	tests := []struct {
		subtype string
		valid   bool
	}{
		{subtype: "contracts/blacklisted-supplier", valid: true},
		{subtype: "a/b/c-2", valid: true},
		{subtype: "v2/limits/rate.exceeded", valid: true},
		{subtype: "x", valid: true},
		{subtype: "out_of_credit", valid: true},
		{subtype: ""},
		{subtype: "Contracts/X"},
		{subtype: "contracts//x"},
		{subtype: "contracts/"},
		{subtype: "/contracts/x"},
		{subtype: "../admin"},
		{subtype: "./x"},
		{subtype: "contracts/../x"},
		{subtype: "contracts/x?y=1"},
		{subtype: "contracts/x#y"},
		{subtype: "x y"},
		{subtype: "x--y"},
		{subtype: "x-"},
		{subtype: "-x"},
		{subtype: "x.-y"},
		{subtype: "https://kartala.id/x"},
		{subtype: "contracts/x\x00"},
		{subtype: "kontrak/pemasok-diblokir-é"},
	}

	for _, tt := range tests {
		t.Run(tt.subtype, func(t *testing.T) {
			t.Parallel()

			if got := isSubtype(tt.subtype); got != tt.valid {
				t.Errorf("isSubtype(%q) = %v, want %v", tt.subtype, got, tt.valid)
			}
		})
	}
}

// Murphy rows for problem-types / Scenario: Namespace validated: the edges
// of each check that the spec rows do not reach.
func TestParseNamespace_Edges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ns   string
		want string
		ok   bool
	}{
		{ns: "/v1/api/probs/", want: "/v1/api/probs", ok: true},
		{ns: "/v1/%zz/probs"},
		{ns: "/v1//probs"},
		{ns: "/v1/./probs"},
		{ns: "/v1/%2e%2e/probs"},
		{ns: "/v1/api/probs?"},
		{ns: "/v1/api/probs#"},
		{ns: "https://kartala.id/probs?"},
		{ns: "https://kartala.id/probs#"},
		{ns: "https://kartala.id/probs//"},
	}

	for _, tt := range tests {
		t.Run(tt.ns, func(t *testing.T) {
			t.Parallel()

			got, err := parseNamespace(tt.ns)
			if (err == nil) != tt.ok || got != tt.want {
				t.Errorf("parseNamespace(%q) = %q, %v; want %q, ok %v", tt.ns, got, err, tt.want, tt.ok)
			}
		})
	}
}

// Type resolution keeps a type with a scheme (problem-types / Scenario:
// Type resolved, the kept rows); these are the edges of the scheme grammar.
func TestHasScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		s    string
		want bool
	}{
		{s: "about:blank", want: true},
		{s: "urn:kartala:probs:x", want: true},
		{s: "https://kartala.id/x", want: true},
		{s: "coap+tcp://x", want: true},
		{s: "x-1.y:z", want: true},
		{s: ""},
		{s: "x"},
		{s: ":x"},
		{s: "1x:y"},
		{s: "-x:y"},
		{s: "contracts/x:y"},
		{s: "Bad Type"},
	}

	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			t.Parallel()

			if got := hasScheme(tt.s); got != tt.want {
				t.Errorf("hasScheme(%q) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}

// FuzzIsSubtype checks that an accepted sub-type can never leave the
// namespace or change the meaning of the URI it is appended to.
func FuzzIsSubtype(f *testing.F) {
	for _, seed := range []string{"contracts/blacklisted-supplier", "a/b/c-2", "../x", "x//y", "X", "x?y", "x#y", ""} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		if !isSubtype(s) {
			return
		}

		for _, forbidden := range []string{"..", "//", "?", "#", "%", " ", "\\"} {
			if strings.Contains(s, forbidden) {
				t.Errorf("isSubtype(%q) accepted a sub-type containing %q", s, forbidden)
			}
		}

		if strings.ToLower(s) != s || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") {
			t.Errorf("isSubtype(%q) accepted uppercase or a leading or trailing slash", s)
		}
	})
}

func BenchmarkIsSubtype(b *testing.B) {
	for b.Loop() {
		isSubtype("contracts/blacklisted-supplier")
	}
}

// BenchmarkResolveType measures resolving a problem's type against the
// namespace, with discovery on and a hook to receive the notes.
func BenchmarkResolveType(b *testing.B) {
	h := NewHandler(
		WithNamespace("/v1/api/probs"),
		WithTypes(DefineType("contracts/blacklisted-supplier", http.StatusConflict, "Supplier is blacklisted", "")),
		OnError(func(context.Context, error, *Problem) {}),
	)

	cases := []struct {
		name string
		typ  string
		want string
	}{
		{name: "resolved", typ: "contracts/blacklisted-supplier", want: "/v1/api/probs/contracts/blacklisted-supplier"},
		{name: "kept", typ: "https://kartala.id/probs/contracts/locked", want: "https://kartala.id/probs/contracts/locked"},
		// Reported to the hook, and replaced by about:blank.
		{name: "invalid", typ: "Contracts/Locked", want: ""},
		// Resolved, and reported to the hook: its type URL leads to a 404.
		{name: "undocumented", typ: "contracts/unknown", want: "/v1/api/probs/contracts/unknown"},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			ctx := b.Context()
			p := New(http.StatusConflict)

			// resolveType changes p, so each resolution starts from tc.typ.
			p.Type, p.Title = tc.typ, "Locked"
			if h.resolveType(ctx, p); p.Type != tc.want {
				b.Fatalf("resolveType(%q) type = %q, want %q", tc.typ, p.Type, tc.want)
			}

			b.ReportAllocs()

			for b.Loop() {
				p.Type, p.Title = tc.typ, "Locked"
				h.resolveType(ctx, p)
			}
		})
	}
}

// Design D2, task 8.7: a valid sub-type is resolved once per handler, so a
// later resolution of it allocates nothing, and the cache stays bounded.
func TestTypeCache_Resolve(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", maxCachedSubtypeLen+1)

	tests := []struct {
		name     string
		subtype  string
		isCached bool
	}{
		{name: "short sub-type", subtype: "contracts/unknown", isCached: true},
		{name: "longest cached sub-type", subtype: strings.Repeat("x", maxCachedSubtypeLen), isCached: true},
		{name: "too long to cache", subtype: long},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var c typeCache

			for range 2 {
				if got, want := c.resolve("/v1/api/probs", tt.subtype), "/v1/api/probs/"+tt.subtype; got != want {
					t.Errorf("resolve = %q, want %q", got, want)
				}
			}

			if _, ok := c.load(tt.subtype); ok != tt.isCached {
				t.Errorf("cached = %v, want %v", ok, tt.isCached)
			}
		})
	}
}

// Murphy row for task 8.7: once the cache is full, a new sub-type is still
// resolved, but not kept, so a consumer that puts request data into types
// cannot grow it without bound.
func TestTypeCache_Bounded(t *testing.T) {
	t.Parallel()

	var c typeCache

	for i := range maxCachedSubtypes + 10 {
		subtype := "s" + strconv.Itoa(i)
		if got, want := c.resolve("/p", subtype), "/p/"+subtype; got != want {
			t.Fatalf("resolve = %q, want %q", got, want)
		}
	}

	if n := c.len(); n != maxCachedSubtypes {
		t.Errorf("cached %d sub-types, want the bound %d", n, maxCachedSubtypes)
	}

	if _, ok := c.load("s" + strconv.Itoa(maxCachedSubtypes)); ok {
		t.Error("a sub-type past the bound was cached")
	}
}

// Murphy row for task 8.7: concurrent requests resolving the same and
// different sub-types get correct types, and the cache stays bounded.
func TestTypeCache_Concurrent(t *testing.T) {
	t.Parallel()

	var (
		c  typeCache
		wg sync.WaitGroup
	)

	for g := range 32 {
		wg.Go(func() {
			for i := range 2 * maxCachedSubtypes {
				subtype := "s" + strconv.Itoa((g*7+i)%(2*maxCachedSubtypes))
				if got, want := c.resolve("/p", subtype), "/p/"+subtype; got != want {
					t.Errorf("resolve = %q, want %q", got, want)
				}
			}
		})
	}

	wg.Wait()

	if n := c.len(); n > maxCachedSubtypes {
		t.Errorf("cached %d sub-types, want at most %d", n, maxCachedSubtypes)
	}
}

// Task 8.7: resolving a cached sub-type allocates nothing.
//
//nolint:paralleltest // AllocsPerRun counts allocations process-wide; parallel tests would skew it.
func TestTypeCache_NoAllocs(t *testing.T) {
	var c typeCache

	if allocs := testing.AllocsPerRun(100, func() { c.resolve("/v1/api/probs", "contracts/unknown") }); allocs != 0 {
		t.Errorf("resolving a cached sub-type allocates %v times, want 0", allocs)
	}
}

// store keeps the first type stored for a sub-type, as when another request
// stored it first, and stores nothing once the cache is full. resolve only
// meets these branches when requests race, so they are driven directly.
func TestTypeCache_StoreSkips(t *testing.T) {
	t.Parallel()

	var c typeCache

	c.store("s", "/first/s")
	c.store("s", "/second/s")

	if typ, _ := c.load("s"); typ != "/first/s" {
		t.Errorf("type = %q, want the first one stored", typ)
	}

	for i := range maxCachedSubtypes {
		c.store("x"+strconv.Itoa(i), "/x")
	}

	if n := c.len(); n != maxCachedSubtypes {
		t.Errorf("cached %d sub-types, want the bound %d", n, maxCachedSubtypes)
	}
}
