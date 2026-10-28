package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLIUniform runs the real main() in a child for each L631 measurement:
// the shared flag, help and argument-count paths must treat every command alike.
func TestCLIUniform(t *testing.T) {
	if args := os.Getenv("ARIZUKO_CLI_ARGS"); args != "" {
		os.Args = append([]string{"arizuko"}, strings.Split(args, "\x1f")...)
		main()
		return
	}
	base := t.TempDir()
	work := t.TempDir()
	bin := t.TempDir()
	mustWrite(t, filepath.Join(base, "arizuko_sloth", "docker-compose.yml"), "services: {}\n")
	mustWrite(t, filepath.Join(bin, "docker"), "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CLI_CAPTURE\"\n")
	if err := os.Chmod(filepath.Join(bin, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		args   []string
		exit   int
		stdout string
		stderr string
	}{
		{"pair honors -i after the service", []string{"pair", "x", "-i", "sloth"}, 0, "", ""},
		{"pair refuses a container flag before --", []string{"pair", "x", "--tty"}, 1, "", "usage: arizuko pair"},
		{"send -h prints usage", []string{"send", "main", "-h"}, 0, "usage: arizuko send", ""},
		{"probe -h prints usage", []string{"probe", "-h"}, 0, "usage: arizuko probe", ""},
		{"export -h prints usage", []string{"export", "-h"}, 0, "usage: arizuko export", ""},
		{"arizuko -h prints usage", []string{"-h"}, 0, "usage: arizuko <command>", ""},
		{"unknown flag is refused", []string{"archive", "apply", "x.tar", "--focre"}, 1, "", "usage: arizuko archive"},
		{"extra argument is refused", []string{"status", "extra"}, 1, "", "usage: arizuko status"},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "docker.args")
			cmd := exec.Command(os.Args[0], "-test.run=^TestCLIUniform$")
			cmd.Dir = work
			cmd.Env = append(os.Environ(),
				"ARIZUKO_PKG_ARGS=cli-uniform",
				"ARIZUKO_CLI_ARGS="+strings.Join(test.args, "\x1f"),
				"ARIZUKO_DATA_DIR="+base,
				"ARIZUKO_INSTANCE=default",
				"DATA_DIR=",
				"CLI_CAPTURE="+capture,
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			exit := 0
			if ee, ok := err.(*exec.ExitError); ok {
				exit = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if exit != test.exit || !strings.Contains(stdout.String(), test.stdout) || !strings.Contains(stderr.String(), test.stderr) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit %d, stdout %q, stderr %q",
					exit, stdout.String(), stderr.String(), test.exit, test.stdout, test.stderr)
			}
			if test.stdout != "" && test.exit == 0 && stderr.Len() != 0 {
				t.Fatalf("help wrote to stderr: %q", stderr.String())
			}
			if _, err := os.Stat(filepath.Join(work, "-h")); err == nil {
				t.Fatal("a file named -h was written")
			}
			if test.name != "pair honors -i after the service" {
				return
			}
			got, err := os.ReadFile(capture)
			want := "compose\n-f\n" + filepath.Join(base, "arizuko_sloth", "docker-compose.yml") + "\nrun\n--rm\nx\n"
			if err != nil || string(got) != want {
				t.Fatalf("docker args %q, %v; want %q", got, err, want)
			}
		})
	}
}
