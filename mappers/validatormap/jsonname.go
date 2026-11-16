package validatormap

import (
	"reflect"
	"strings"
	"unicode"
)

// notDecodedName is the name [JSONName] gives a field encoding/json never
// decodes. It holds quotes, which encoding/json rejects in a tag name and Go
// rejects in a field name, so no JSON member decoded into a struct has it,
// and neither "." nor "[", which separate the segments of a namespace.
const notDecodedName = `json:"-"`

// JSONName returns the name encoding/json gives the struct field fld, for
// use as the validator's tag name function, so that pointers name the
// location a field is decoded from:
//
//	v := validator.New(validator.WithRequiredStructEnabled(), validator.WithTagNameFuncBlankOmit())
//	v.RegisterTagNameFunc(validatormap.JSONName)
//	m := validatormap.Mapper(validatormap.WithJSONNames())
//
// The name is the one in the field's json tag, or the Go field name when the
// tag has none or a name encoding/json rejects (such as one holding a
// backslash). It is "" for an embedded struct, or pointer to struct, without
// a tag name, whose fields encoding/json promotes to the enclosing object.
// For a field the tag excludes ("-"), it is a reserved name no decoded JSON
// member can have; the mapper gives the entries of such a field, and of the
// fields under it, only the default detail, since they have no location in
// the request body and a translation would show the reserved name.
//
// The validator drops a "" segment from namespaces only when created with
// validator.WithTagNameFuncBlankOmit; without that option, it uses the Go
// name instead, and the pointer to a field of an embedded struct then
// includes the name of the struct, a location the JSON document does not
// have.
func JSONName(fld reflect.StructField) string {
	tag := fld.Tag.Get("json")
	if tag == "-" {
		return notDecodedName
	}

	if name, _, _ := strings.Cut(tag, ","); isValidTag(name) {
		return name
	}

	if fld.Anonymous && isStruct(fld.Type) {
		return ""
	}

	return fld.Name
}

// isStruct reports whether t is a struct type or a pointer to one, the
// embedded types whose fields encoding/json promotes.
func isStruct(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t.Kind() == reflect.Struct
}

// isValidTag reports whether encoding/json accepts name as the name in a
// json tag. It ignores a name that is empty or holds a character other than
// a letter, a digit or the punctuation below, and uses the Go field name
// instead. This copies encoding/json's own check.
func isValidTag(name string) bool {
	if name == "" {
		return false
	}

	for _, c := range name {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c):
			// Backslash and quote are reserved; other punctuation is allowed.
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			return false
		}
	}

	return true
}
