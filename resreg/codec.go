package resreg

// codec.go is the ONE rendering of a field whose Go type is not text, used by
// BOTH shapes (spec 6/40 §"What a registered resource still needs", answer 3).
//
// WHY IT EXISTS AT ALL. Every objstore attribute value is a string, so
// proxyd_routes' `preserve_headers []string` and `strip_prefix bool` have no
// representation on the object path without one. Its two Hooks ARE that codec
// today — a BeforeInsert that json.Marshals into a shadow column and an
// AfterScan that unmarshals back — which is why dropping them at the cutover
// would strip the preserved headers from every proxied route, read once per
// request over 26 live rows. Null handling is not what they do.
//
// WHY IT APPLIES TO BOTH SHAPES, and this is the part that is not optional. The
// codec is what makes a column-backed and an entity-backed resource agree about
// what a value IS: one renderer, so the text a column holds and the text an
// attribute holds are the SAME text, and the migration that carries a table from
// one to the other cannot change a value on the way. Two renderings would be two
// paths, and two paths drift.
//
// WHERE IT IS DECLARED. In the `db:` tag, after the column name —
// `db:"preserve_headers,json"` — because the encoding is a fact about the FIELD
// and the field already carries its column there. The set is CLOSED: an
// encoding is a name this file knows, never a caller's function, so a resource
// cannot invent a rendering only it can read. An unknown name stops the process
// at Register.
//
// WHAT IT DOES NOT COVER. A string and an int need no codec: both paths already
// render them the same way, SQL binds them natively and the object path writes
// an int with strconv. Declaring one on either is refused rather than accepted,
// because a second rendering of a value that already has one is the drift this
// file exists to prevent.
//
// A codec'd COLUMN is TEXT, on both engines, because the codec's output is the
// bind value. proxyd_routes' `preserve_headers TEXT NOT NULL DEFAULT '[]'` is;
// its `strip_prefix INTEGER` and acl's `grant_option INTEGER` are not, so those
// two can carry a codec only once they are attributes and the column is gone.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
)

// codec is one encoding: a Go field value to the single text both shapes store,
// and back.
//
// It is a value in a closed table rather than a pair of caller-supplied
// functions, so every reader of a stored value can name the rendering that
// produced it.
type codec struct {
	name string
	// encode renders the field's value. It never returns "" for a value the row
	// STATES: a Go bool and a Go slice have no unset, the way an int does not,
	// so the zero value renders as its own text ("0", "[]") rather than as an
	// absent attribute.
	encode func(v reflect.Value) (string, error)
	// decode parses stored text into the field. The empty string is the ABSENT
	// attribute and the NULL column, and both mean the zero value — the one null
	// this model has (objstore.Item).
	decode func(text string, into reflect.Value) error
}

// codecs is the closed set. A name outside it is refused at Register.
var codecs = map[string]codec{
	"json": {name: "json", encode: encodeJSON, decode: decodeJSON},
	"bool": {name: "bool", encode: encodeBool, decode: decodeBool},
}

// encodeJSON renders a slice, map or struct field as its JSON text.
//
// A nil slice or map is normalised to the EMPTY one first, because
// json.Marshal writes nil as `null` and the stored form has to stay a document
// a second reader can parse: proxyd_routes' column is `TEXT NOT NULL DEFAULT
// '[]'` and store.ProxydRoutes json.Unmarshals it on every proxyd start.
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

// decodeJSON parses that text back. Absent leaves the zero value; anything else
// that is not the document it claims to be is a LOUD fault, because a row read
// wrong goes into an export the operator then applies.
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

// encodeBool renders a bool as "0" or "1".
//
// Those two and not "true"/"false": the columns this codec is for are INTEGER
// 0/1 on both engines (migrations 0015, 0021), an int attribute is already
// written with strconv on the object path, and the two readers of a migrated
// value have to agree byte for byte.
func encodeBool(v reflect.Value) (string, error) {
	return strconv.Itoa(map[bool]int{false: 0, true: 1}[v.Bool()]), nil
}

// decodeBool takes those two and the absent attribute, and nothing else. A
// third spelling is a value something else wrote, and guessing at it is how a
// gate reads open.
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

// codecFor resolves one field's declared encoding, or refuses the declaration.
//
// It runs inside initMeta, so a refusal is a panic at Register — a misdeclared
// field is a caller fault and must stop the binary rather than answer a wrong
// value later.
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
