package validatormap_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/go-playground/locales/en"
	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/validator/v10"

	"github.com/kartaladev/prblm"
	"github.com/kartaladev/prblm/mappers/validatormap"
)

// ID is a named non-struct type, which encoding/json does not flatten when
// embedded.
type ID string

type names struct {
	Base
	ID
	Email  string `json:"email,omitempty"`
	Plain  string
	Opts   string `json:",omitempty"`
	Secret string `json:"-"`
	Dash   string `json:"-,"`   //nolint:staticcheck // encoding/json names this field "-", which JSONName must follow.
	Weird  string `json:"a\\b"` // not a valid name for encoding/json, which falls back to the Go name
	Tagged Base   `json:"base"`
}

func TestJSONName(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[names]()

	// Scenario: JSON names.
	tests := []struct {
		field string
		want  string
	}{
		{field: "Email", want: "email"},
		{field: "Opts", want: "Opts"},
		{field: "Plain", want: "Plain"},
		{field: "Secret", want: validatormap.NotDecodedName},
		{field: "Dash", want: "-"},
		{field: "Weird", want: "Weird"},
		{field: "Base", want: ""},
		{field: "Tagged", want: "base"},
		{field: "ID", want: "ID"},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			t.Parallel()

			fld, ok := typ.FieldByName(tt.field)
			if !ok {
				t.Fatalf("no field %s", tt.field)
			}

			if got := validatormap.JSONName(fld); got != tt.want {
				t.Errorf("JSONName(%s) = %q, want %q", tt.field, got, tt.want)
			}
		})
	}
}

// Base is a struct to embed.
type Base struct {
	Name string `json:"name" validate:"required"`
}

type embeddedOrder struct {
	Base
}

type embeddedPointerOrder struct {
	*Base
}

type taggedOrder struct {
	Base `json:"base"`
}

// TestMapper_EmbeddedStruct covers the scenario "Embedded structs": with
// JSONName on a validator that omits blank names, a pointer names the
// location encoding/json decodes the field from.
func TestMapper_EmbeddedStruct(t *testing.T) {
	t.Parallel()

	v := validator.New(validator.WithRequiredStructEnabled(), validator.WithTagNameFuncBlankOmit())
	v.RegisterTagNameFunc(validatormap.JSONName)

	tests := []struct {
		value any
		name  string
		want  string
	}{
		{name: "embedded", value: embeddedOrder{}, want: "#/name"},
		{name: "embedded pointer", value: embeddedPointerOrder{Base: &Base{}}, want: "#/name"},
		{name: "embedded with a json tag", value: taggedOrder{}, want: "#/base/name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := serve(t, prblm.NewHandler(prblm.WithMappers(jsonMapper())), "", v.Struct(tt.value))

			if got.status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", got.status)
			}

			want := `[{"detail":"failed validation: required","pointer":"` + tt.want + `"}]`
			if e := errorsMember(t, got.body); e != want {
				t.Errorf("errors = %s, want %s", e, want)
			}
		})
	}
}

type account struct {
	ID   string `json:"ID" validate:"required"`
	Name string `json:"name" validate:"required"`
}

// TestMapper_NamesEqualGoNames covers the scenario "JSON names equal to Go
// names": with WithJSONNames, a field whose JSON name equals its Go name
// still gets a pointer, whichever other fields fail.
func TestMapper_NamesEqualGoNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value account
		name  string
		want  string
	}{
		{
			name: "ID and Name",
			want: `[{"detail":"failed validation: required","pointer":"#/ID"},` +
				`{"detail":"failed validation: required","pointer":"#/name"}]`,
		},
		{
			name:  "ID only",
			value: account{Name: "Alice"},
			want:  `[{"detail":"failed validation: required","pointer":"#/ID"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := serve(t, prblm.NewHandler(prblm.WithMappers(jsonMapper())), "", newValidator().Struct(tt.value))

			if e := errorsMember(t, got.body); e != tt.want {
				t.Errorf("errors = %s, want %s", e, tt.want)
			}
		})
	}
}

// meta is a struct a server fills in, never decoded from the request.
type meta struct {
	Name string `json:"name" validate:"required"`
}

// base is an unexported struct to embed through a pointer.
type base struct {
	Name string `json:"name" validate:"required"`
}

type excludedField struct {
	Name   string `json:"name" validate:"required"`
	UserID string `json:"-" validate:"required"`
}

type excludedNested struct {
	Name     string `json:"name"`
	Internal meta   `json:"-"`
}

type embeddedUnexported struct {
	*base
}

// Review of PR #6 (second pass): a field encoding/json never decodes has no
// location in the request body, so its entry carries only the detail, never
// a pointer to the document or to another member.
func TestMapper_NotDecoded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value any
		name  string
		want  string
		opts  []validatormap.Option
	}{
		{
			name:  "json dash field",
			opts:  []validatormap.Option{validatormap.WithJSONNames()},
			value: excludedField{},
			want: `[{"detail":"failed validation: required","pointer":"#/name"},` +
				`{"detail":"failed validation: required"}]`,
		},
		{
			name:  "json dash nested struct",
			opts:  []validatormap.Option{validatormap.WithJSONNames()},
			value: excludedNested{Name: "Alice"},
			want:  `[{"detail":"failed validation: required"}]`,
		},
		{
			// encoding/json promotes the fields of an embedded pointer to an
			// unexported struct and decodes into it when it is allocated, so
			// this pointer is right.
			name:  "embedded pointer to an unexported struct",
			opts:  []validatormap.Option{validatormap.WithJSONNames()},
			value: embeddedUnexported{base: &base{}},
			want:  `[{"detail":"failed validation: required","pointer":"#/name"}]`,
		},
		{
			// Without WithJSONNames the other entries carry a field, but the
			// excluded one still carries only its detail.
			name:  "json dash field without JSON names",
			value: excludedField{},
			want: `[{"detail":"failed validation: required","field":"name"},` +
				`{"detail":"failed validation: required"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := serve(t, prblm.NewHandler(prblm.WithMappers(validatormap.Mapper(tt.opts...))), "", newValidator().Struct(tt.value))

			if e := errorsMember(t, got.body); e != tt.want {
				t.Errorf("errors = %s, want %s", e, tt.want)
			}
		})
	}
}

// Simplify review of the second-pass fixes: JSONName's reserved name is the
// field name the validator gives a translation, so a field encoding/json
// never decodes keeps the default detail instead of a translation showing
// that name to the client.
func TestMapper_NotDecodedTranslated(t *testing.T) {
	t.Parallel()

	uni := ut.New(en.New(), en.New())
	english, _ := uni.GetTranslator("en")

	v := newValidator()

	regErr := v.RegisterTranslation("required", english,
		func(trans ut.Translator) error { return trans.Add("required", "{0} is required", false) },
		func(trans ut.Translator, fe validator.FieldError) string {
			s, _ := trans.T("required", fe.Field())
			return s
		},
	)
	if regErr != nil {
		t.Fatalf("RegisterTranslation() error = %v", regErr)
	}

	mapper := jsonMapper(validatormap.WithTranslator(func(context.Context) ut.Translator { return english }))
	got := serve(t, prblm.NewHandler(prblm.WithMappers(mapper)), "", v.Struct(excludedField{Name: "Alice"}))

	// The field has no name a client knows, so its detail is the default
	// one, never a translation naming it.
	const want = `[{"detail":"failed validation: required"}]`
	if e := errorsMember(t, got.body); e != want {
		t.Errorf("errors = %s, want %s", e, want)
	}
}
