package resreg

// codec.go is the ONE rendering of a field whose Go type is not text (spec
// 6/40 answer 3). Every attribute value is a string, so a []string or a bool
// has no representation as an item without one. It is what the engine writes
// a value through AND reads it back through, so a stored text has a single
// meaning; two renderings would drift.
//
// It is declared in the `db:` tag after the attribute name
// (`db:"preserve_headers,json"`), and the set is CLOSED: an unknown name stops
// the process at Register. A string or int ATTRIBUTE declaring one is refused,
// since a second rendering of a value that has one is the drift this file
// prevents. THE ONE EXCEPTION IS A NUMBER IN A COORDINATE: a sort key is
// compared as text, so "10" sorts before "2"; `pad` is that rendering, and it
// is refused anywhere but a sort key.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// codec is one encoding: a Go field value to the text it is stored as, and
// back.
type codec struct {
	name string
	// encode renders the field's value. It never returns "" for a value the
	// row STATES: a bool and a slice have no unset, so the zero value renders
	// as its own text ("0", "[]") rather than as an absent attribute.
	encode func(v reflect.Value) (string, error)
	// decode parses stored text into the field. The empty string is the ABSENT
	// attribute and means the zero value.
	decode func(text string, into reflect.Value) error
}

// codecs is the closed set. A name outside it is refused at Register.
var codecs = map[string]codec{
	"json": {name: "json", encode: encodeJSON, decode: decodeJSON},
	"bool": {name: "bool", encode: encodeBool, decode: decodeBool},
	"pad":  {name: padCodec, encode: encodePad, decode: decodePad},
}

// padCodec is the one encoding that belongs to a COORDINATE, and initEntity
// refuses it anywhere else. routes is keyed (seq, target, match) and the key
// is compared as TEXT, so a fixed-width zero-padded decimal makes byte order
// and numeric order the same thing (BUGS L97, applied to a number).
const padCodec = "pad"

// padWidth is that width; ten digits holds every non-negative int32.
const padWidth = 10

// encodeJSON renders a slice, map or struct field as its JSON text. A nil
// slice or map is normalised to the EMPTY one first: json.Marshal writes nil
// as `null`, and the stored form has to stay a document a second reader can
// parse.
func encodeJSON(v reflect.Value) (string, error) {
	if (v.Kind() == reflect.Slice || v.Kind() == reflect.Map) && v.IsNil() {
		if v.Kind() == reflect.Slice {
			v = reflect.MakeSlice(v.Type(), 0, 0)
		} else {
			v = reflect.MakeMap(v.Type())
		}
	}
	b, err := json.Marshal(v.Interface())
	if err != nil {
		return "", fmt.Errorf("json: %w", err)
	}
	return string(b), nil
}

// decodeJSON parses that text back. Absent is the zero value; anything else
// that is not the document it claims to be is a LOUD fault, because a row
// read wrong goes into an export the operator then applies.
func decodeJSON(text string, into reflect.Value) error {
	if text == "" {
		into.Set(reflect.Zero(into.Type()))
		return nil
	}
	if err := json.Unmarshal([]byte(text), into.Addr().Interface()); err != nil {
		return fmt.Errorf("%q is not the JSON this field holds: %w", text, err)
	}
	return nil
}

// encodeBool renders a bool as "0" or "1", not "true"/"false": an int
// attribute is written with strconv, and the two readers of a migrated value
// have to agree byte for byte.
func encodeBool(v reflect.Value) (string, error) {
	return strconv.Itoa(map[bool]int{false: 0, true: 1}[v.Bool()]), nil
}

// decodeBool takes those two and the absent attribute, and nothing else:
// guessing at a third spelling is how a gate reads open.
func decodeBool(text string, into reflect.Value) error {
	switch text {
	case "", "0":
		into.SetBool(false)
	case "1":
		into.SetBool(true)
	default:
		return fmt.Errorf("%q is not a bool; this codec writes %q and %q", text, "0", "1")
	}
	return nil
}

// encodePad renders a non-negative int at the declared width. A NEGATIVE
// value is refused: "-000000001" sorts before "-000000002", so the padding
// would invert the order it exists to preserve.
func encodePad(v reflect.Value) (string, error) {
	n := v.Int()
	if n < 0 {
		return "", fmt.Errorf("%d is negative, and this encoding pads to sort in numeric "+
			"order, which padding a negative does not do", n)
	}
	text := strconv.FormatInt(n, 10)
	if len(text) > padWidth {
		return "", fmt.Errorf("%d is wider than the %d digits this encoding pads to, so it "+
			"would sort below every shorter value", n, padWidth)
	}
	return strings.Repeat("0", padWidth-len(text)) + text, nil
}

// decodePad reads it back. Absent is the zero value; a non-number is a LOUD
// fault, because a coordinate read wrong names a row nobody wrote.
func decodePad(text string, into reflect.Value) error {
	if text == "" {
		into.SetInt(0)
		return nil
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return fmt.Errorf("%q is not a number: %w", text, err)
	}
	into.SetInt(int64(n))
	return nil
}

// codecFor resolves one field's declared encoding, or refuses the declaration
// with a panic at Register.
func codecFor(resource, field, name string, kind reflect.Kind) *codec {
	if name == "" {
		return nil
	}
	c, ok := codecs[name]
	if !ok {
		panic(fmt.Sprintf("resreg: %s.%s declares the encoding %q, and the set is "+
			"json and bool; an encoding a caller invents is a rendering only it can read",
			resource, field, name))
	}
	if name == padCodec {
		// The one encoding an INT may declare (padCodec).
		if kind != reflect.Int {
			panic(fmt.Sprintf("resreg: %s.%s is a %s and declares the %q encoding, which "+
				"pads a number", resource, field, kind, name))
		}
		return &c
	}
	switch kind {
	case reflect.String, reflect.Int:
		panic(fmt.Sprintf("resreg: %s.%s is a %s and declares the encoding %q; both shapes "+
			"already render a string and an int the same way, and a second rendering of a "+
			"value that has one is what drifts", resource, field, kind, name))
	}
	if name == "bool" && kind != reflect.Bool {
		panic(fmt.Sprintf("resreg: %s.%s is a %s and declares the bool encoding",
			resource, field, kind))
	}
	return &c
}
