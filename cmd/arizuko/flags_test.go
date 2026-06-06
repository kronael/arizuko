package main

import (
	"flag"
	"reflect"
	"testing"
)

// newTestFS builds a FlagSet with a bool flag (wait/-w), a string flag
// (value/-v), and an int flag (seq/-s) so each case can assert parsed flag
// values plus fs.Args() positionals. Short aliases bind to the same vars via
// a second registration.
func newTestFS() (fs *flag.FlagSet, wait *bool, value *string, seq *int) {
	fs = flag.NewFlagSet("test", flag.ContinueOnError)
	wait = fs.Bool("wait", false, "")
	fs.BoolVar(wait, "w", false, "")
	value = fs.String("value", "", "")
	fs.StringVar(value, "v", "", "")
	seq = fs.Int("seq", 0, "")
	fs.IntVar(seq, "s", 0, "")
	return
}

func TestFlexParse(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantErr   bool
		wantWait  bool
		wantValue string
		wantSeq   int
		wantArgs  []string
	}{
		{name: "flags before positionals", args: []string{"--seq", "5", "a", "b"}, wantSeq: 5, wantArgs: []string{"a", "b"}},
		{name: "flags after positionals (bug case)", args: []string{"a", "b", "--seq", "5"}, wantSeq: 5, wantArgs: []string{"a", "b"}},
		{name: "flags interspersed", args: []string{"a", "--seq", "5", "b"}, wantSeq: 5, wantArgs: []string{"a", "b"}},
		{name: "short form after positionals", args: []string{"a", "b", "-s", "5"}, wantSeq: 5, wantArgs: []string{"a", "b"}},
		{name: "equals form long", args: []string{"a", "b", "--seq=5"}, wantSeq: 5, wantArgs: []string{"a", "b"}},
		{name: "equals form short", args: []string{"a", "b", "-s=5"}, wantSeq: 5, wantArgs: []string{"a", "b"}},
		{name: "negative int value long", args: []string{"a", "b", "--seq", "-10"}, wantSeq: -10, wantArgs: []string{"a", "b"}},
		{name: "negative int value short", args: []string{"a", "b", "-s", "-10"}, wantSeq: -10, wantArgs: []string{"a", "b"}},
		{name: "bool no value after positionals", args: []string{"a", "b", "--wait"}, wantWait: true, wantArgs: []string{"a", "b"}},
		{name: "bool short after positionals", args: []string{"a", "b", "-w"}, wantWait: true, wantArgs: []string{"a", "b"}},
		{name: "bool then positionals", args: []string{"--wait", "a", "b"}, wantWait: true, wantArgs: []string{"a", "b"}},
		{name: "string value interspersed", args: []string{"a", "--value", "hello", "b"}, wantValue: "hello", wantArgs: []string{"a", "b"}},
		// "--" terminator: everything after is positional, so --seq and 5 are
		// args (not parsed), seq stays 0.
		{name: "double-dash terminator", args: []string{"a", "--", "--seq", "5"}, wantSeq: 0, wantArgs: []string{"a", "--seq", "5"}},
		{name: "unknown flag", args: []string{"a", "--bogus", "1", "b"}, wantErr: true},
		{name: "multiple flags mixed", args: []string{"a", "-w", "b", "--seq", "5", "c", "-v", "x"}, wantWait: true, wantSeq: 5, wantValue: "x", wantArgs: []string{"a", "b", "c"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, wait, value, seq := newTestFS()
			err := flexParse(fs, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("flexParse(%v) = nil error, want error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("flexParse(%v) error: %v", tc.args, err)
			}
			if *wait != tc.wantWait {
				t.Errorf("wait = %v, want %v", *wait, tc.wantWait)
			}
			if *value != tc.wantValue {
				t.Errorf("value = %q, want %q", *value, tc.wantValue)
			}
			if *seq != tc.wantSeq {
				t.Errorf("seq = %d, want %d", *seq, tc.wantSeq)
			}
			if !reflect.DeepEqual(fs.Args(), tc.wantArgs) {
				t.Errorf("args = %v, want %v", fs.Args(), tc.wantArgs)
			}
		})
	}
}
