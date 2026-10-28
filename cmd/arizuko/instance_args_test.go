package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInstanceArgs(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("ARIZUKO_INSTANCE", "default")
	for _, test := range []struct {
		args    []string
		dataDir string
		want    []string
		dir     string
		picked  bool
	}{
		{[]string{"list"}, "", []string{"default", "list"}, filepath.Join(base, "arizuko_default"), false},
		{[]string{"list"}, "/elsewhere", []string{"", "list"}, "/elsewhere", false},
		{[]string{"-i", "sloth", "list"}, "/elsewhere", []string{"sloth", "list"}, filepath.Join(base, "arizuko_sloth"), true},
		{[]string{"--instance=sloth", "export"}, "", []string{"sloth", "export"}, filepath.Join(base, "arizuko_sloth"), true},
		{[]string{"pair", "x", "-i", "sloth"}, "", []string{"sloth", "pair", "x"}, filepath.Join(base, "arizuko_sloth"), true},
		{[]string{"set", "main", "KEY", "--value=-i"}, "", []string{"default", "set", "main", "KEY", "--value=-i"}, filepath.Join(base, "arizuko_default"), false},
		{[]string{"pair", "x", "--", "-i", "y"}, "", []string{"default", "pair", "x", "--", "-i", "y"}, filepath.Join(base, "arizuko_default"), false},
	} {
		t.Setenv("DATA_DIR", test.dataDir)
		got, err := instanceArgs(test.args)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%v: got %v, %v; want %v", test.args, got, err, test.want)
		}
		if dir := mustInstanceDir(got[0]); dir != test.dir {
			t.Fatalf("%v: selected %q, want %q", test.args, dir, test.dir)
		}
		want := test.dataDir
		if test.picked {
			want = test.dir
		}
		if got := os.Getenv("DATA_DIR"); got != want {
			t.Fatalf("%v: DATA_DIR %q, want %q", test.args, got, want)
		}
	}
	if _, err := instanceArgs([]string{"-i"}); err == nil {
		t.Fatal("missing instance accepted")
	}
}
