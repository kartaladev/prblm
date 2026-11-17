package prblm

import (
	"errors"
	"testing"
)

// fallbackBody is a constant so it cannot fail; this keeps it in step with
// what Resolve produces for an unmapped error.
func TestFallbackBody_MatchesResolvedFallback(t *testing.T) {
	t.Parallel()

	encoded, err := NewHandler().Resolve(t.Context(), errors.New("unmapped")).MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}

	if string(encoded) != fallbackBody {
		t.Errorf("fallbackBody = %s, want %s", fallbackBody, encoded)
	}
}

func BenchmarkNegotiate(b *testing.B) {
	cases := []struct {
		name   string
		accept string
		want   string
	}{
		{name: "empty", accept: "", want: mediaTypeJSON},
		{name: "browser", accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", want: mediaTypeXML},
		{name: "xml", accept: mediaTypeXML, want: mediaTypeXML},
		{name: "malformed", accept: ";;;", want: mediaTypeJSON},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			if got := negotiateFormat(tc.accept).mediaType; got != tc.want {
				b.Fatalf("negotiateFormat(%q) = %q, want %q", tc.accept, got, tc.want)
			}

			b.ReportAllocs()

			for b.Loop() {
				negotiateFormat(tc.accept)
			}
		})
	}
}
