package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

type fakeHost struct {
	device       string
	images       []string
	imageError   bool
	inventory    map[string]string
	commandError string
	meminfo      string
}

func (f *fakeHost) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	if key == f.commandError {
		return nil, errors.New("inventory unavailable")
	}
	if value, ok := f.inventory[key]; ok {
		return []byte(value), nil
	}
	if name == "ip" {
		return []byte("[]"), nil
	}
	return nil, nil
}

func (f *fakeHost) Open(path string, flags int) error {
	if strings.HasPrefix(path, "/dev/") {
		if path == f.device {
			return errors.New("device unavailable")
		}
		return nil
	}
	return (realHost{}).Open(path, flags)
}
func (*fakeHost) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }
func (f *fakeHost) ReadFile(path string) ([]byte, error) {
	if path == "/proc/meminfo" {
		if f.meminfo != "" {
			return []byte(f.meminfo), nil
		}
		return []byte("MemAvailable: 999999999 kB\n"), nil
	}
	return os.ReadFile(path)
}
func (f *fakeHost) InspectImages(_ context.Context, images []string) error {
	f.images = images
	if f.imageError {
		return errors.New("image unavailable")
	}
	return nil
}

func preflightFixture(t *testing.T) (lifecycleOptions, map[string]string) {
	t.Helper()
	opts := lifecycleFixture(t)
	cfg, err := godotenv.Read(opts.envFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg["RUNED_KERNEL_DIR"] = t.TempDir()
	cfg["RUNED_ROOTFS_DIR"] = t.TempDir()
	cfg["HOST_APP_DIR"] = t.TempDir()
	cfg["RUNED_TAP_SUBNET"] = "10.97.0.0/24"
	for _, name := range []string{"bzImage", "initrd.gz"} {
		if err := os.WriteFile(filepath.Join(cfg["RUNED_KERNEL_DIR"], name), []byte("kernel"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfg["RUNED_ROOTFS_DIR"], ".image-env.json"), []byte(`["PATH=/bin"]`), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg["HOST_APP_DIR"], "template", "services", "litellm")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := "services:\n  litellm:\n    image: gateway:tag\n  litellm_db:\n    image: db:tag\n"
	if err := os.WriteFile(filepath.Join(dir, "litellm.yml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := godotenv.Write(cfg, opts.envFile); err != nil {
		t.Fatal(err)
	}
	return opts, cfg
}

func TestPreflightRefusesBeforeCreation(t *testing.T) {
	for _, failure := range []string{"kvm", "vsock", "tun", "kernel", "initrd", "rootfs", "image-env", "image-env-invalid", "fragment", "fragment-invalid", "image"} {
		t.Run(failure, func(t *testing.T) {
			opts, cfg := preflightFixture(t)
			host := &fakeHost{}
			var path string
			switch failure {
			case "kvm":
				host.device = "/dev/kvm"
			case "vsock":
				host.device = "/dev/vhost-vsock"
			case "tun":
				host.device = "/dev/net/tun"
			case "kernel":
				path = filepath.Join(cfg["RUNED_KERNEL_DIR"], "bzImage")
			case "initrd":
				path = filepath.Join(cfg["RUNED_KERNEL_DIR"], "initrd.gz")
			case "rootfs":
				path = cfg["RUNED_ROOTFS_DIR"]
			case "image-env", "image-env-invalid":
				path = filepath.Join(cfg["RUNED_ROOTFS_DIR"], ".image-env.json")
			case "fragment", "fragment-invalid":
				path = filepath.Join(cfg["HOST_APP_DIR"], "template", "services", "litellm", "litellm.yml")
			case "image":
				host.imageError = true
			}
			if strings.HasSuffix(failure, "invalid") {
				if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if path != "" {
				if failure == "rootfs" {
					cfg["RUNED_ROOTFS_DIR"] = filepath.Join(path, "missing")
					if err := godotenv.Write(cfg, opts.envFile); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			cli := &fakeLifecycle{host: host}
			if err := doLifecycle(context.Background(), cli, opts); err == nil {
				t.Fatal("accepted missing prerequisite")
			}
			if len(cli.calls) != 0 {
				t.Fatalf("lifecycle actions: %v", cli.calls)
			}
			entries, err := os.ReadDir(os.Getenv("ARIZUKO_DATA_DIR"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("private directory created: %v, %v", entries, err)
			}
		})
	}
}

func TestPreflightChecksGatewayImages(t *testing.T) {
	_, cfg := preflightFixture(t)
	host := &fakeHost{}
	if err := preflight(context.Background(), host, cfg); err != nil {
		t.Fatal(err)
	}
	want := []string{"arizuko:latest", "arizuko-vite:latest", "arizuko-davd:latest", "gateway:tag", "db:tag"}
	if !reflect.DeepEqual(host.images, want) {
		t.Fatalf("images = %v, want %v", host.images, want)
	}
}

func TestImageInspectCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\nprintf '%s' \"$IMAGE_IDS\"\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ARGS_FILE", filepath.Join(dir, "args"))
	t.Setenv("IMAGE_IDS", "sha256:one\nsha256:two\n")
	images := []string{"one:tag", "two:tag"}
	if err := (realHost{}).InspectImages(context.Background(), images); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "image\ninspect\n--format\n{{.Id}}\none:tag\ntwo:tag\n" {
		t.Fatalf("docker arguments: %q", args)
	}
	t.Setenv("IMAGE_IDS", "sha256:one\n")
	if err := (realHost{}).InspectImages(context.Background(), images); err == nil {
		t.Fatal("accepted incomplete image inventory")
	}
}

func TestAllocationRefusesBeforeCreation(t *testing.T) {
	const processes = "ps -eo args"
	const vsock = "ss -H -l -n -A vsock"
	const addresses = "ip -j -4 address show"
	const routes = "ip -j -4 route show table all"
	const tcp = "ss -H -l -n -t"
	for _, tc := range []struct {
		name, key, value string
		config           bool
	}{
		{"cid invalid", "RUNED_VSOCK_CID_BASE", "2", true},
		{"cid malformed", "RUNED_VSOCK_CID_BASE", "bad", true},
		{"vsock invalid", "RUNED_VSOCK_PORT_BASE", "4294967294", true},
		{"vsock malformed", "RUNED_VSOCK_PORT_BASE", "bad", true},
		{"proxy invalid", "RUNED_PROXY_PORT", "65536", true},
		{"subnet invalid", "RUNED_TAP_SUBNET", "bad", true},
		{"subnet too small", "RUNED_TAP_SUBNET", "10.97.0.0/29", true},
		{"cid occupied", processes, "qemu-system-x86_64 -device vhost-vsock-device,guest-cid=3\n", false},
		{"cid malformed inventory", processes, "qemu -device guest-cid=bad\n", false},
		{"vsock first", vsock, "LISTEN 0 128 *:5555 *:*\n", false},
		{"vsock second", vsock, "LISTEN 0 128 *:5556 *:*\n", false},
		{"vsock malformed inventory", vsock, "bad\n", false},
		{"address overlap", addresses, `[{"addr_info":[{"local":"10.97.0.9","prefixlen":28}]}]`, false},
		{"route overlap", routes, `[{"dst":"10.97.0.128/25","scope":"link"}]`, false},
		{"address invalid inventory", addresses, `null`, false},
		{"route invalid inventory", routes, `invalid`, false},
		{"proxy wildcard", tcp, "LISTEN 0 128 0.0.0.0:3128 0.0.0.0:*\n", false},
		{"proxy address", tcp, "LISTEN 0 128 10.97.0.2:3128 0.0.0.0:*\n", false},
		{"proxy ipv6 wildcard", tcp, "LISTEN 0 128 [::]:3128 [::]:*\n", false},
		{"tcp malformed inventory", tcp, "bad\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, cfg := preflightFixture(t)
			host := &fakeHost{inventory: map[string]string{}}
			if tc.config {
				cfg[tc.key] = tc.value
				if err := godotenv.Write(cfg, opts.envFile); err != nil {
					t.Fatal(err)
				}
			} else {
				host.inventory[tc.key] = tc.value
			}
			cli := &fakeLifecycle{host: host}
			if err := doLifecycle(context.Background(), cli, opts); err == nil {
				t.Fatal("accepted invalid allocation")
			}
			if len(cli.calls) != 0 {
				t.Fatalf("lifecycle actions: %v", cli.calls)
			}
			entries, err := os.ReadDir(os.Getenv("ARIZUKO_DATA_DIR"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("private directory created: %v, %v", entries, err)
			}
		})
	}
	for _, command := range []string{processes, vsock, addresses, routes, tcp} {
		t.Run(command+" error", func(t *testing.T) {
			opts, _ := preflightFixture(t)
			cli := &fakeLifecycle{host: &fakeHost{commandError: command}}
			if err := doLifecycle(context.Background(), cli, opts); err == nil || len(cli.calls) != 0 {
				t.Fatalf("inventory error: %v, actions %v", err, cli.calls)
			}
		})
	}
}

func TestAllocationAllowsUnrelatedResources(t *testing.T) {
	_, cfg := preflightFixture(t)
	host := &fakeHost{inventory: map[string]string{
		"ps -eo args":                   "qemu -device guest-cid=4\n",
		"ss -H -l -n -A vsock":          "LISTEN 0 128 *:5557 *:*\n",
		"ip -j -4 address show":         `[{"addr_info":[{"local":"10.98.0.1","prefixlen":24}]}]`,
		"ip -j -4 route show table all": `[{"dst":"default","scope":"link"},{"dst":"0.0.0.0/0","scope":"link"},{"dst":"10.99.0.0/24","scope":"link"}]`,
		"ss -H -l -n -t":                "LISTEN 0 128 10.98.0.2:3128 0.0.0.0:*\n",
	}}
	if err := preflight(context.Background(), host, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightMemory(t *testing.T) {
	_, cfg := preflightFixture(t)
	cfg["RUNED_GUEST_MEM"] = "3G"
	needed := servicesMemoryKiB + 3*1024*1024
	for _, tc := range []struct {
		name      string
		available int
		refuse    bool
	}{
		{"below", needed - 1, true}, {"equal", needed, false}, {"above", needed + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &fakeHost{meminfo: fmt.Sprintf("MemAvailable: %d kB\n", tc.available)}
			err := preflight(context.Background(), host, cfg)
			if tc.refuse {
				if err == nil {
					t.Fatal("accepted insufficient memory")
				}
				for _, number := range []int{tc.available, servicesMemoryKiB, 3 * 1024 * 1024} {
					if !strings.Contains(err.Error(), fmt.Sprint(number)) {
						t.Errorf("error %q lacks %d", err, number)
					}
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, value := range []string{"", "0", "bad", "99999999999999999999G"} {
		cfg["RUNED_GUEST_MEM"] = value
		if err := checkMemory(&fakeHost{}, cfg); err == nil {
			t.Errorf("accepted guest memory %q", value)
		}
	}
	cfg["RUNED_GUEST_MEM"] = "1M"
	for _, value := range []string{"MemTotal: 100 kB\n", "MemAvailable: bad kB\n", "MemAvailable: 3 MB\n"} {
		if err := checkMemory(&fakeHost{meminfo: value}, cfg); err == nil {
			t.Errorf("accepted meminfo %q", value)
		}
	}
}
