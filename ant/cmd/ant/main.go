// ant CLI: drive a Claude agent against an ant-folder.
//
// Foundation skeleton. Flag parsing only; the runtime port replaces
// ant/src/index.ts in a later pass (see specs/5/b-ant-standalone.md).
// Body is a stub returning EX_USAGE (64) so any caller wiring against
// this binary fails loud rather than silently appearing to work.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

const exUsage = 64

func main() {
	fs := flag.NewFlagSet("ant", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `ant — run a Claude agent against an ant-folder

usage:
  ant <folder> [--prompt=<text>] [--mcp [--socket=<path>]] [--sandbox=<backend>]

flags:
  --prompt   one-shot prompt; omit for interactive REPL
  --mcp      expose this agent as an MCP server (stdio by default)
  --socket   unix socket path for --mcp (default: stdio)
  --sandbox  isolation backend: none | dockbox | crackbox (default: none)

status:
  Foundation skeleton — runtime port not yet shipped. See
  specs/5/b-ant-standalone.md.
`)
	}

	prompt := fs.String("prompt", "", "one-shot prompt; omit for interactive REPL")
	mcp := fs.Bool("mcp", false, "expose this agent as an MCP server")
	socket := fs.String("socket", "", "unix socket path for --mcp (empty = stdio)")
	sandbox := fs.String("sandbox", "none", "isolation backend: none|dockbox|crackbox")

	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(exUsage)
	}

	args := fs.Args()
	if len(args) < 1 {
		fs.Usage()
		os.Exit(exUsage)
	}
	folder := args[0]

	// Silence unused-var warnings while the runtime is unimplemented.
	_ = folder
	_ = *prompt
	_ = *mcp
	_ = *socket
	_ = *sandbox

	fmt.Fprintln(os.Stderr, "ant: TODO: drive claude CLI per spec; not yet implemented")
	os.Exit(exUsage)
}
