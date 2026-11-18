package fiberprblm

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// fasthttp's Set replaces only the first value of a header that has several,
// and keeps the others; Del removes them all. send relies on this when it
// deletes each merged header before adding its values back: with Set, a
// header the handler set twice would keep its second value.
func TestFasthttpSetKeepsLaterValues(t *testing.T) {
	t.Parallel()

	var h fasthttp.ResponseHeader

	h.Add("X-A", "1")
	h.Add("X-A", "2")
	h.Set("X-A", "3")

	var got []string
	for _, v := range h.PeekAll("X-A") {
		got = append(got, string(v))
	}

	if len(got) != 2 || got[0] != "3" || got[1] != "2" {
		t.Errorf("after Add 1, Add 2, Set 3: X-A = %q, want [\"3\" \"2\"] (Set replaces the first value only)", got)
	}
}
