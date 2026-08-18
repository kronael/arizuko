// crackbox runs a command in a sandbox that can only reach the hosts you
// name. It is the composition of two components that also ship on their own:
// egress (the forward proxy) and sandbox (the KVM library).
//
//	crackbox run --allow github.com -- curl https://github.com
//	crackbox run --kvm --data-dir /var/lib/crackbox --allow github.com -- uname -a
//
// For the proxy alone, run `egress` (egress/cmd/egress). There is no daemon
// mode, no admin API and no registry: one proxy per sandbox, allowlist fixed
// at start (spec 6/24).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kronael/arizuko/crackbox/pkg/run"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `crackbox — run a command in a sandbox with a filtered egress path

usage:
  crackbox run --allow <hosts> [--image <img>] [--subnet <cidr>] -- <cmd>...
  crackbox run --kvm --data-dir <dir> --allow <hosts> [-- <cmd>...]

The container sandbox is filtered by its own egress proxy; the KVM sandbox is
filtered by netfilter rules on its tap. Run the proxy on its own with `+"`egress`"+`.
`)
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	allow := fs.String("allow", "", "comma-separated allowlist (required)")
	id := fs.String("id", "", "id label for logs (default: random)")
	image := fs.String("image", "alpine:3.20", "container image to run the command in")
	proxyImg := fs.String("proxy-image", envOr("EGRESS_IMAGE", "arizuko-egress:latest"), "image holding the egress binary")
	subnet := fs.String("subnet", envOr("CRACKBOX_SUBNET", "10.88.0.0/24"), "sandbox network CIDR")
	uplink := fs.String("uplink", envOr("CRACKBOX_UPLINK", "bridge"), "docker network giving the proxy its route out")
	kvm := fs.Bool("kvm", false, "run in a KVM VM instead of a container (needs qemu + root)")
	dataDir := fs.String("data-dir", os.Getenv("CRACKBOX_DATA_DIR"), "VM state root (--kvm only)")
	quiet := fs.Bool("quiet", false, "suppress crackbox-prefixed status lines")
	_ = fs.Parse(args)

	if *allow == "" {
		fmt.Fprintln(os.Stderr, "--allow is required")
		os.Exit(2)
	}
	cmdAndArgs := fs.Args()
	if len(cmdAndArgs) == 0 && !*kvm {
		fmt.Fprintln(os.Stderr, "command required after flags (use -- to separate)")
		os.Exit(2)
	}

	code, err := run.Run(run.Args{
		Image:     *image,
		Cmd:       cmdAndArgs,
		Allow:     splitCSV(*allow),
		ID:        *id,
		ProxyImg:  *proxyImg,
		NetSubnet: *subnet,
		Uplink:    *uplink,
		KVM:       *kvm,
		DataDir:   *dataDir,
		Quiet:     *quiet,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "crackbox run:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
