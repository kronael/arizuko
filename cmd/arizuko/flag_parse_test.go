package main

import "testing"

// TestParseCreateFlags proves flexParse + the -p alias on `create`: --product/-p
// parses whether before, after, or absent relative to the <name> positional, and
// a wrong positional count errors instead of silently dropping a misplaced flag.
func TestParseCreateFlags(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantErr     bool
		wantName    string
		wantProduct string
	}{
		{name: "no product", args: []string{"inst"}, wantName: "inst"},
		{name: "long flag before positional", args: []string{"--product", "trip", "inst"}, wantName: "inst", wantProduct: "trip"},
		{name: "short flag after positional", args: []string{"inst", "-p", "trip"}, wantName: "inst", wantProduct: "trip"},
		{name: "long flag after positional", args: []string{"inst", "--product", "trip"}, wantName: "inst", wantProduct: "trip"},
		{name: "no positional errors", args: []string{"-p", "trip"}, wantErr: true},
		{name: "two positionals errors", args: []string{"inst", "other", "-p", "trip"}, wantErr: true},
		{name: "unknown flag errors", args: []string{"inst", "--nope", "1"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, product, err := parseCreateFlags(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseCreateFlags(%v) = nil error, want error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCreateFlags(%v) error: %v", tc.args, err)
			}
			if name != tc.wantName || product != tc.wantProduct {
				t.Errorf("parseCreateFlags(%v) = (%q, %q), want (%q, %q)",
					tc.args, name, product, tc.wantName, tc.wantProduct)
			}
		})
	}
}

// TestParseInviteCreate proves flexParse + the -n/-e aliases on `invite create`:
// --max-uses/-n and --expires/-e parse whether before, after, or interspersed
// with the <target_glob> positional; a wrong positional count, a max-uses < 1, or
// an unknown flag errors instead of silently dropping a misplaced flag.
func TestParseInviteCreate(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantErr     bool
		wantGlob    string
		wantMax     int
		wantExpires bool
	}{
		{name: "default max-uses", args: []string{"tg:*"}, wantGlob: "tg:*", wantMax: 1},
		{name: "long flags before positional", args: []string{"--max-uses", "5", "--expires", "1h", "tg:*"}, wantGlob: "tg:*", wantMax: 5, wantExpires: true},
		{name: "short flags after positional", args: []string{"tg:*", "-n", "5", "-e", "1h"}, wantGlob: "tg:*", wantMax: 5, wantExpires: true},
		{name: "short max after positional", args: []string{"tg:*", "-n", "3"}, wantGlob: "tg:*", wantMax: 3},
		{name: "flag interspersed", args: []string{"-n", "2", "tg:*"}, wantGlob: "tg:*", wantMax: 2},
		{name: "no positional errors", args: []string{"-n", "5"}, wantErr: true},
		{name: "two positionals errors", args: []string{"tg:*", "extra", "-n", "5"}, wantErr: true},
		{name: "max-uses below one errors", args: []string{"tg:*", "-n", "0"}, wantErr: true},
		{name: "unknown flag errors", args: []string{"tg:*", "--nope", "1"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			glob, max, expires, err := parseInviteCreate(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseInviteCreate(%v) = nil error, want error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseInviteCreate(%v) error: %v", tc.args, err)
			}
			if glob != tc.wantGlob || max != tc.wantMax {
				t.Errorf("parseInviteCreate(%v) = (%q, %d), want (%q, %d)",
					tc.args, glob, max, tc.wantGlob, tc.wantMax)
			}
			if (expires != nil) != tc.wantExpires {
				t.Errorf("parseInviteCreate(%v) expires set = %v, want %v", tc.args, expires != nil, tc.wantExpires)
			}
		})
	}
}

// TestParseIdentityLink proves flexParse + the -n/-i aliases on `identity link`:
// --name/-n and --id/-i parse whether before, after, or interspersed with the
// <sub> positional, and a wrong positional count errors instead of silently
// dropping a misplaced flag.
func TestParseIdentityLink(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantErr  bool
		wantSub  string
		wantID   string
		wantName string
	}{
		{name: "bare sub", args: []string{"tg:1"}, wantSub: "tg:1"},
		{name: "long flags before positional", args: []string{"--name", "alice", "--id", "id7", "tg:1"}, wantSub: "tg:1", wantID: "id7", wantName: "alice"},
		{name: "short flags after positional", args: []string{"tg:1", "-n", "alice", "-i", "id7"}, wantSub: "tg:1", wantID: "id7", wantName: "alice"},
		{name: "short name after positional", args: []string{"tg:1", "-n", "alice"}, wantSub: "tg:1", wantName: "alice"},
		{name: "no positional errors", args: []string{"-n", "alice"}, wantErr: true},
		{name: "two positionals errors", args: []string{"tg:1", "tg:2", "-n", "alice"}, wantErr: true},
		{name: "unknown flag errors", args: []string{"tg:1", "--nope", "1"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub, id, name, err := parseIdentityLink(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseIdentityLink(%v) = nil error, want error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIdentityLink(%v) error: %v", tc.args, err)
			}
			if sub != tc.wantSub || id != tc.wantID || name != tc.wantName {
				t.Errorf("parseIdentityLink(%v) = (%q, %q, %q), want (%q, %q, %q)",
					tc.args, sub, id, name, tc.wantSub, tc.wantID, tc.wantName)
			}
		})
	}
}
