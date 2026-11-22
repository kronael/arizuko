package resreg

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

type codec struct {
	name   string
	encode func(v reflect.Value) (string, error)
	decode func(text string, into reflect.Value) error
}

var codecs = map[string]codec{
	"json": {name: "json", encode: encodeJSON, decode: decodeJSON},
	"bool": {name: "bool", encode: encodeBool, decode: decodeBool},
	"pad":  {name: padCodec, encode: encodePad, decode: decodePad},
}

const padCodec = "pad"

const padWidth = 10

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

func encodeBool(v reflect.Value) (string, error) {
	return strconv.Itoa(map[bool]int{false: 0, true: 1}[v.Bool()]), nil
}

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
