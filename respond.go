package prblm

import (
	"errors"
	"iter"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/kartaladev/prblm/internal/negotiate"
)

const (
	// mediaTypeJSON is the media type of a JSON problem document.
	mediaTypeJSON = "application/problem+json"
	// mediaTypeXML is the media type of an XML problem document.
	mediaTypeXML = "application/problem+xml"
	// fallbackBody is the encoded fallbackProblem, sent when encoding the
	// resolved problem fails. It is a constant so that it cannot fail itself.
	fallbackBody = `{"title":"Internal Server Error","status":500}`
)

// ErrEncoding is wrapped by the note the hooks ([OnError] and [AddErrorHook])
// receive when a resolved problem could not be encoded (for example an
// extension holding a channel) and the minimal 500 problem was sent instead.
// The note also wraps the encoder's own error.
var ErrEncoding = errors.New("prblm: problem could not be encoded")

// Request describes the HTTP request an error occurred in, independently of
// any web framework.
type Request struct {
	// Method is the HTTP method.
	Method string
	// Path is the URL path, without the query string.
	Path string
	// Accept is the value of the Accept header.
	Accept string
}

// Response is a problem response ready to be written by a framework adapter.
type Response struct {
	// Header holds the headers to set on the response.
	Header http.Header
	// Body is the encoded problem document.
	Body []byte
	// replaced lists the canonical names of the headers the problem marked
	// with [WithReplacedHeader]; MergeInto replaces them in its destination.
	replaced []string
	// Status is the HTTP status code.
	Status int
}

// MergeInto merges the response headers into dst, the headers a handler or
// a framework already set on the response, so that every adapter applies the
// same rules: response headers replace those in dst, except that Vary keeps
// the field names already in dst (adding Accept without duplicates), and a
// Cache-Control already in dst is kept, because the handler already chose how
// its response may be cached.
//
// A header the problem marked with [WithReplacedHeader] replaces dst's
// values in every case, Vary and Cache-Control included: the problem's author
// decided that this problem must win, for example to force no-store on a
// route the handler made cacheable. A replaced Vary still lists Accept,
// because the response's own Vary always does.
//
// A handler may have written dst's map directly, under a key that is not
// in canonical form, such as "cache-control": net/http sends such a key as
// written, so it is the same header to the client. The rules apply to every
// spelling of a response header's name: its values count as dst's, after
// those under the canonical name, and the merged values go under the
// canonical name alone. Keys of other headers are left as they are.
//
// The values are copied, so dst never shares storage with r. A nil dst has
// nothing to merge into, and a nil r.Header has nothing to merge: both leave
// dst unchanged.
func (r Response) MergeInto(dst http.Header) {
	if dst == nil {
		return
	}

	foldSpellings(dst, func(name string) bool {
		_, ok := r.Header[name]

		return ok
	})

	for name, values := range r.Header {
		switch {
		case slices.Contains(r.replaced, name):
			dst[name] = slices.Clone(values)
		case name == "Vary":
			addVary(dst, values...)
		case name == "Cache-Control":
			if len(dst[name]) == 0 {
				dst[name] = slices.Clone(values)
			}
		default:
			dst[name] = slices.Clone(values)
		}
	}
}

// format is a problem document format the handler can answer with.
type format struct {
	// encode encodes a problem as a complete document.
	encode func(p *Problem) ([]byte, error)
	// mediaType is the Content-Type of the document.
	mediaType string
	// accepts lists the media types an Accept header may name to select the
	// format, most specific first.
	accepts []string
}

var (
	// jsonFormat is the default format, and the format of the fallback.
	jsonFormat = format{
		encode:    (*Problem).MarshalJSON,
		mediaType: mediaTypeJSON,
		accepts:   []string{mediaTypeJSON, "application/json"},
	}
	// xmlFormat is sent to clients that prefer XML.
	xmlFormat = format{
		encode:    marshalXML,
		mediaType: mediaTypeXML,
		accepts:   []string{mediaTypeXML, "application/xml", "text/xml"},
	}
	// formats are the formats in order of preference: JSON comes first, so
	// it wins ties.
	formats = []*format{&jsonFormat, &xmlFormat}
	// offers are the media types of formats, in order, for negotiation.
	offers = acceptedTypes(formats)
)

// acceptedTypes returns the media types that select one of formats, in order.
func acceptedTypes(fs []*format) []string {
	var types []string
	for _, f := range fs {
		types = append(types, f.accepts...)
	}

	return types
}

// negotiateFormat returns the format the Accept header prefers, or JSON when it
// accepts none: a client that cannot read the problem still gets the status.
func negotiateFormat(accept string) *format {
	selected := negotiate.Select(accept, offers)

	for _, f := range formats {
		if slices.Contains(f.accepts, selected) {
			return f
		}
	}

	return &jsonFormat
}

// newResponse builds a problem response. It takes ownership of header,
// which must not be shared; Respond passes the header of the problem that
// Resolve copied for this request. replaced lists the header names the
// problem marked to replace (nil when nothing is marked, as for the
// fallback); the response only reads it, and keeps only the marks whose
// header the problem still has (see presentOnly). The library owns
// Content-Type and Content-Length, so a problem's own values for them are
// replaced: a wrong length would corrupt the response.
//
// Every problem response varies on Accept, and is not stored by caches
// unless the problem sets its own Cache-Control: an error is a statement
// about one moment, and an intermediary replaying it would hide recovery.
func newResponse(status int, header http.Header, replaced []string, mediaType string, body []byte) Response {
	header = canonicalize(header)
	replaced = presentOnly(replaced, header)

	// The values share one array instead of one per header, as net/http
	// does when it parses headers. Each is capped at its length, so an
	// append to one copies it rather than overwriting the next.
	values := [...]string{mediaType, strconv.Itoa(len(body)), "Accept", "no-store"}
	header["Content-Type"] = values[0:1:1]
	header["Content-Length"] = values[1:2:2]

	if len(header["Vary"]) == 0 {
		header["Vary"] = values[2:3:3]
	} else {
		addVary(header, "Accept")
	}

	if len(header["Cache-Control"]) == 0 {
		header["Cache-Control"] = values[3:4:4]
	}

	return Response{Status: status, Header: header, Body: body, replaced: replaced}
}

// presentOnly returns the names in replaced that header has values for: a
// mark whose header was deleted from the problem replaces nothing, so the
// Vary and Cache-Control defaults newResponse fills in then merge by the
// default rules instead of replacing the handler's values. It returns
// replaced itself when header has them all, the common case, and a new slice
// otherwise, so the problem's own list is never changed.
func presentOnly(replaced []string, header http.Header) []string {
	isAbsent := func(name string) bool { return len(header[name]) == 0 }
	if !slices.ContainsFunc(replaced, isAbsent) {
		return replaced
	}

	return slices.DeleteFunc(slices.Clone(replaced), isAbsent)
}

// canonicalize moves the values of non-canonical keys in h to their
// canonical keys, so that a key put straight into the map, such as
// "content-type", cannot slip past the headers the library controls. It
// returns a new header for a nil h.
func canonicalize(h http.Header) http.Header {
	if h == nil {
		return make(http.Header)
	}

	foldSpellings(h, func(string) bool { return true })

	return h
}

// foldSpellings moves the values h holds under a non-canonical key to the
// canonical key, for each canonical name folds accepts, and deletes that key.
// They go after the values already under the canonical key, in the sorted
// order of the keys, so the result is deterministic, and never share storage
// with them. A key that is not a valid field name has no canonical form and
// is left alone, as net/http leaves it.
//
// It runs on every response, and keys are almost always canonical, so the
// common case is one pass over h that allocates nothing:
// [http.CanonicalHeaderKey] returns a canonical key as it is.
func foldSpellings(h http.Header, folds func(name string) bool) {
	// An empty h, as before a handler writes any header, has nothing to
	// fold; returning at once saves ranging over the map.
	if len(h) == 0 {
		return
	}

	var spellings []string

	for key := range h {
		if name := http.CanonicalHeaderKey(key); name != key && folds(name) {
			spellings = append(spellings, key)
		}
	}

	slices.Sort(spellings)

	for _, key := range spellings {
		name := http.CanonicalHeaderKey(key)
		h[name] = slices.Concat(h[name], h[key])
		delete(h, key)
	}
}

// addVary adds each field name listed in values to the Vary header of h,
// unless h already lists it. Field names compare case-insensitively, and a
// value may list several names separated by commas.
func addVary(h http.Header, values ...string) {
	for name := range listElements(values) {
		if !varies(h, name) {
			h.Add("Vary", name)
		}
	}
}

// varies reports whether the Vary header of h lists name.
func varies(h http.Header, name string) bool {
	for listed := range listElements(h.Values("Vary")) {
		if strings.EqualFold(listed, name) {
			return true
		}
	}

	return false
}

// listElements yields the non-empty elements of comma-separated header
// values, without surrounding whitespace.
func listElements(values []string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, value := range values {
			for element := range strings.SplitSeq(value, ",") {
				if element = strings.TrimSpace(element); element != "" && !yield(element) {
					return
				}
			}
		}
	}
}
