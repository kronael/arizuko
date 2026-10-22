package runed

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type bootVariant struct {
	name, level, machine string
	kernel               string
	trace                bool
	times                []time.Duration
}

func bootVariants(mode string) ([]*bootVariant, error) {
	switch mode {
	case "", "loglevel":
		return []*bootVariant{{name: "baseline"}, {name: "loglevel5", level: "5"}}, nil
	case "trace":
		return []*bootVariant{{name: "untraced", level: "5"}, {name: "traced", level: "5", trace: true}}, nil
	case "microvm-trace":
		return []*bootVariant{
			{name: "q35", level: "5", machine: "q35", trace: true},
			{name: "microvm", level: "5", machine: "microvm", trace: true},
		}, nil
	case "compression", "compression-trace":
		return []*bootVariant{
			{name: "gzip-kernel", level: "5", kernel: "/bench/kernel/gzip/bzImage", trace: mode == "compression-trace"},
			{name: "lz4-kernel", level: "5", kernel: "/bench/kernel/lz4/bzImage", trace: mode == "compression-trace"},
		}, nil
	case "microvm":
		return []*bootVariant{
			{name: "q35", level: "5", machine: "q35"},
			{name: "microvm", level: "5", machine: "microvm"},
		}, nil
	default:
		return nil, fmt.Errorf("unknown boot benchmark mode %q", mode)
	}
}

func bootArgs(args []string, v bootVariant) ([]string, error) {
	var out []string
	for i := 0; i < len(args); i++ {
		key := args[i]
		if key == "-append" && v.level == "" {
			line, ok := strings.CutSuffix(args[i+1], " loglevel=5")
			if !ok {
				return nil, fmt.Errorf("production command line has no loglevel=5 baseline token")
			}
			out = append(out, key, line)
			i++
			continue
		}
		if key == "-kernel" && v.kernel != "" {
			out = append(out, key, v.kernel)
			i++
			continue
		}
		if key == "-append" && v.kernel != "" && v.trace {
			out = append(out, key, args[i+1]+" earlyprintk=serial,ttyS0,115200 debug loglevel=5")
			i++
			continue
		}
		if v.machine != "" {
			if key == "-global" && args[i+1] == "virtio-mmio.force-legacy=false" {
				i++
				continue
			}
			if key == "-numa" {
				i++
				continue
			}
			if key == "-M" {
				machine := v.machine
				if machine == "microvm" {
					machine += ",acpi=on,pcie=off,usb=off,memory-backend=mem"
				}
				out = append(out, key, machine)
				i++
				continue
			}
			from, to := "-pci,", "-device,"
			if v.machine == "q35" {
				from, to = to, from
			}
			for _, device := range []string{"vhost-user-fs", "vhost-vsock", "virtio-net", "virtio-balloon"} {
				key = strings.ReplaceAll(key, device+from, device+to)
			}
		}
		out = append(out, key)
		if key == "-serial" && v.trace {
			out = append(out, "stdio")
			i++
		}
	}
	switch v.machine {
	case "microvm":
		out = append(out, "-global", "virtio-mmio.force-legacy=false")
	case "q35":
		out = append(out, "-numa", "node,memdev=mem")
	}
	if v.trace {
		out = append(out, "-msg", "timestamp=on", "-trace", "enable=fw_cfg_select", "-trace", "enable=runstate_set")
	}
	return out, nil
}

type bootChunk struct {
	ElapsedNS int64  `json:"elapsed_ns"`
	Text      string `json:"text"`
}

type bootStream struct {
	start   time.Time
	mu      sync.Mutex
	chunks  []bootChunk
	pending string
	markers []bootChunk
}

func (s *bootStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending += string(p)
	for i := range s.markers {
		if s.markers[i].ElapsedNS == 0 && strings.Contains(s.pending, s.markers[i].Text) {
			s.markers[i].ElapsedNS = time.Since(s.start).Nanoseconds()
		}
	}
	for {
		line, rest, found := strings.Cut(s.pending, "\n")
		if !found {
			break
		}
		s.chunks = append(s.chunks, bootChunk{time.Since(s.start).Nanoseconds(), line + "\n"})
		s.pending = rest
	}
	return len(p), nil
}

func (s *bootStream) dump(t *testing.T, path string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, timed strings.Builder
	for _, c := range s.chunks {
		raw.WriteString(c.Text)
		b, err := json.Marshal(c)
		if err != nil {
			t.Error(err)
			return
		}
		timed.Write(b)
		timed.WriteByte('\n')
	}
	raw.WriteString(s.pending)
	if len(s.markers) > 0 {
		b, err := json.MarshalIndent(s.markers, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		if err := os.WriteFile(path+".markers.json", b, 0o644); err != nil {
			t.Error(err)
		}
	}
	if err := os.WriteFile(path+".log", []byte(raw.String()), 0o644); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(path+".jl", []byte(timed.String()), 0o644); err != nil {
		t.Error(err)
	}
}

type bootCapture struct {
	console, trace *bootStream
	path           string
}

func newBootCapture(path string) *bootCapture {
	return &bootCapture{console: &bootStream{markers: []bootChunk{
		{Text: "early console in setup code"},
		{Text: "early console in extract_kernel"},
		{Text: "Decompressing Linux..."},
		{Text: "Parsing ELF..."},
		{Text: "Booting the kernel"},
	}}, trace: &bootStream{}, path: path}
}

func (c *bootCapture) dump(t *testing.T) {
	c.console.dump(t, c.path+".console")
	c.trace.dump(t, c.path+".trace")
}

func TestBootProfileArgs(t *testing.T) {
	args := qemuArgs(qemuSpec{SMP: 2, Mem: "2G"})
	for _, mode := range []string{"trace", "microvm-trace"} {
		variants, err := bootVariants(mode)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := bootArgs(args, *variants[1])
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(rendered, " ")
		for _, want := range []string{
			"loglevel=5", "reboot=t", "-no-reboot", "share=on",
			"fw_cfg_select", "runstate_set",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s missing %s: %s", mode, want, got)
			}
		}
		invalid := strings.Contains(got, "-numa") ||
			!strings.Contains(got, "force-legacy=false") ||
			!strings.Contains(got, "memory-backend=mem")
		if mode != "trace" && invalid {
			t.Errorf("invalid modern MMIO memory/transport: %s", got)
		}
	}
	for _, source := range []string{"q35", "microvm"} {
		base, err := bootArgs(args, bootVariant{machine: source, level: "5"})
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{"q35", "microvm"} {
			rendered, err := bootArgs(base, bootVariant{machine: target, level: "5"})
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(rendered, " ")
			numa, modern, suffix := 1, 0, "-pci,"
			if target == "microvm" {
				numa, modern, suffix = 0, 1, "-device,"
			}
			if strings.Count(got, "-numa ") != numa ||
				strings.Count(got, "virtio-mmio.force-legacy=false") != modern {
				t.Errorf("%s to %s has invalid memory/transport: %s", source, target, got)
			}
			for _, device := range []string{"vhost-user-fs", "vhost-vsock", "virtio-net", "virtio-balloon"} {
				if !strings.Contains(got, device+suffix) {
					t.Errorf("%s to %s lacks %s%s", source, target, device, suffix)
				}
			}
		}
	}
}

func TestBootVariantsRenderDifferentArguments(t *testing.T) {
	args := qemuArgs(qemuSpec{SMP: 2, Mem: "2G"})
	for _, mode := range []string{
		"", "loglevel", "trace", "microvm", "microvm-trace",
		"compression", "compression-trace",
	} {
		t.Run(mode, func(t *testing.T) {
			variants, err := bootVariants(mode)
			if err != nil {
				t.Fatal(err)
			}
			first, err := bootArgs(args, *variants[0])
			if err != nil {
				t.Fatal(err)
			}
			second, err := bootArgs(args, *variants[1])
			if err != nil {
				t.Fatal(err)
			}
			if slices.Equal(first, second) {
				t.Fatalf("%s and %s render identical arguments", variants[0].name, variants[1].name)
			}
		})
	}
}

func TestBootBaselineRequiresTheProductionLoglevel(t *testing.T) {
	_, err := bootArgs([]string{"-append", "console=ttyS0"}, bootVariant{})
	if err == nil || !strings.Contains(err.Error(), "no loglevel=5 baseline token") {
		t.Fatalf("missing baseline token: %v", err)
	}
}

func TestBootStreamRecordsCompleteLines(t *testing.T) {
	s := &bootStream{start: time.Now()}
	for _, part := range []string{"gu", "est-init: ali", "ve\nnext", " line\n"} {
		if _, err := s.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.chunks) != 2 || s.chunks[0].Text != "guest-init: alive\n" ||
		s.chunks[1].Text != "next line\n" || s.pending != "" {
		t.Fatalf("fragmented lines: %+v pending=%q", s.chunks, s.pending)
	}
	if s.chunks[1].ElapsedNS < s.chunks[0].ElapsedNS {
		t.Fatal("clock moved backwards")
	}
}

func TestBootStreamTimestampsIncompleteMarkersOnce(t *testing.T) {
	s := &bootStream{start: time.Now(), markers: []bootChunk{{Text: "Decompressing Linux..."}}}
	for _, part := range []string{"Decomp", "ressing Linux... "} {
		_, _ = s.Write([]byte(part))
	}
	stamp := s.markers[0].ElapsedNS
	if stamp <= 0 || len(s.chunks) != 0 {
		t.Fatalf("marker must precede complete line: %+v", s)
	}
	_, _ = s.Write([]byte("done.\nDecompressing Linux... again\n"))
	if s.markers[0].ElapsedNS != stamp || len(s.chunks) != 2 {
		t.Fatalf("marker repeated or raw lines changed: %+v", s)
	}
}
