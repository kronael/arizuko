package resreg

// codec.go is the ONE rendering of a field whose Go type is not text (spec 6/40
// §"What a registered resource still needs", answer 3).
//
// WHY IT EXISTS AT ALL. Every objstore attribute value is a string, so
// proxyd_routes' `preserve_headers []string` and `strip_prefix bool` have no
// representation as an item without one, and losing that rendering strips the
// preserved headers from every proxied route, read once per request over 26 live
// rows. Null handling is not what it does.
//
// WHY IT IS ONE RENDERING, and this is the part that is not optional. It is what
// the engine writes a value through and what it reads the value back through, so
// a stored text has a single meaning — including for a migration carrying a
// table into items, which has to land the same text the engine reads. Two
// renderings would be two paths, and two paths drift.
//
// WHERE IT IS DECLARED. In the `db:` tag, after the column name —
// `db:"preserve_headers,json"` — because the encoding is a fact about the FIELD
// and the field already carries its column there. The set is CLOSED: an
// encoding is a name this file knows, never a caller's function, so a resource
// cannot invent a rendering only it can read. An unknown name stops the process
// at Register.
//
// WHAT IT DOES NOT COVER. A string and an int ATTRIBUTE need no codec: a string
// is already text and an int is written with strconv. Declaring one on either is
// refused rather than accepted, because a second rendering of a value that
// already has one is the drift this file exists to prevent.
//
// THE ONE EXCEPTION IS A NUMBER IN A COORDINATE, and it is an exception because
// strconv's rendering is WRONG there rather than merely different: a sort key is
// compared as text, so "10" sorts before "2" and routes' (seq, target, match)
// would read back in an order the route table's own meaning contradicts. `pad`
// is that rendering, it is refused anywhere but a sort key, and no
// column-shaped resource may declare it — such a resource has no key to order.
//
// A codec'd COLUMN is TEXT, on both engines, because the read scans the stored
// text and hands it to the codec; an INTEGER column would arrive as a number the
// codec has no text to decode.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// codec is one encoding: a Go field value to the text it is stored as, and back.
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
	"pad":  {name: padCodec, encode: encodePad, decode: decodePad},
}

// padCodec is the one encoding that belongs to a COORDINATE and not to an
// attribute, and initEntity refuses it anywhere else.
//
// routes is keyed (seq, target, match) and `seq` is an integer, so the key it
// joins into is compared as TEXT: strconv's rendering puts 10 before 2 and the
// route table's whole meaning is "lower seq wins, first match". A fixed-width
// zero-padded decimal makes byte order and numeric order the same thing, which
// is the rule objstore.OrderedSort already enforces for a stored instant
// (BUGS L97) applied to a number.
const padCodec = "pad"

// padWidth is that width. Ten digits holds every non-negative int32, and the
// widest `seq` any live instance carries is 3 digits.
const padWidth = 10

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
// 0/1 on both engines (migrations 0015, 0021), an int attribute is written with
// strconv, and the two readers of a migrated value have to agree byte for byte.
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

// encodePad renders a non-negative int at the declared width.
//
// A NEGATIVE value is refused rather than rendered: "-000000001" sorts before
// "-000000002", so the padding inverts the order it exists to preserve, and a
// coordinate that sorts wrong is the failure this codec prevents. No live route
// carries one; a value that needs one needs a different encoding, chosen by
// whoever needs it.
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

// decodePad reads it back. Absent is the zero value, the one null this model
// has; anything that is not a number at all is a LOUD fault, because a
// coordinate read wrong names a row nobody wrote.
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
	if name == padCodec {
		// The one encoding an INT may declare, because an int's own rendering is
		// wrong for a coordinate rather than merely different (padCodec).
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
