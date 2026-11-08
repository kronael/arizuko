package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type preflightHost interface {
	Open(string, int) error
	Stat(string) (os.FileInfo, error)
	ReadFile(string) ([]byte, error)
	InspectImages(context.Context, []string) error
	Run(context.Context, string, ...string) ([]byte, error)
}

type realHost struct{}

func (realHost) Open(path string, flags int) error {
	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return err
	}
	return file.Close()
}
func (realHost) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }
func (realHost) ReadFile(path string) ([]byte, error)  { return os.ReadFile(path) }
func (realHost) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}
func (realHost) InspectImages(ctx context.Context, images []string) error {
	args := append([]string{"image", "inspect", "--format", "{{.Id}}"}, images...)
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		return fmt.Errorf("docker image inspect: %w", err)
	}
	if len(strings.Fields(string(out))) != len(images) {
		return errors.New("docker image inspect returned incomplete results")
	}
	return nil
}
func (realCLI) Preflight(ctx context.Context, cfg map[string]string) error {
	return preflight(ctx, realHost{}, cfg)
}

// Krons daemon containers used 291.441 MiB on 2026-11-08; no gateway ran.
// The total times 1.5 rounds up to 512 MiB.
const servicesMemoryKiB = 512 * 1024

func checkMemory(host preflightHost, cfg map[string]string) error {
	mem := strings.TrimSpace(cfg["RUNED_GUEST_MEM"])
	factor := uint64(1024)
	if strings.HasSuffix(mem, "G") || strings.HasSuffix(mem, "g") {
		factor *= 1024
		mem = mem[:len(mem)-1]
	} else if strings.HasSuffix(mem, "M") || strings.HasSuffix(mem, "m") {
		mem = mem[:len(mem)-1]
	}
	guest, err := strconv.ParseUint(mem, 10, 64)
	if err != nil || guest == 0 || guest > (^uint64(0)-servicesMemoryKiB)/factor {
		return fmt.Errorf("invalid RUNED_GUEST_MEM %q", cfg["RUNED_GUEST_MEM"])
	}
	guest *= factor
	data, err := host.ReadFile("/proc/meminfo")
	if err != nil {
		return fmt.Errorf("read /proc/meminfo: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "MemAvailable:" {
			if len(fields) != 3 || fields[2] != "kB" {
				return errors.New("invalid MemAvailable in /proc/meminfo")
			}
			available, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return errors.New("invalid MemAvailable in /proc/meminfo")
			}
			if available < servicesMemoryKiB+guest {
				return fmt.Errorf("insufficient memory: available %d KiB, services and gateway %d KiB, guest %d KiB", available, servicesMemoryKiB, guest)
			}
			return nil
		}
	}
	return errors.New("missing MemAvailable in /proc/meminfo")
}

func preflight(ctx context.Context, host preflightHost, cfg map[string]string) error {
	if err := checkMemory(host, cfg); err != nil {
		return err
	}
	for _, path := range []string{"/dev/kvm", "/dev/vhost-vsock", "/dev/net/tun"} {
		if err := host.Open(path, os.O_RDWR); err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
	}
	for _, name := range []string{"bzImage", "initrd.gz"} {
		path := filepath.Join(cfg["RUNED_KERNEL_DIR"], name)
		info, err := host.Stat(path)
		if err != nil {
			return fmt.Errorf("kernel artifact %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("kernel artifact %s is not a regular file", path)
		}
		if err := host.Open(path, os.O_RDONLY); err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
	}
	root := cfg["RUNED_ROOTFS_DIR"]
	info, err := host.Stat(root)
	if err != nil {
		return fmt.Errorf("rootfs %s: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("rootfs %s is not a directory", root)
	}
	path := filepath.Join(root, ".image-env.json")
	data, err := host.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var env []string
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	valid := false
	for _, entry := range env {
		if strings.Contains(entry, "=") {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("%s has no environment entries", path)
	}
	path = filepath.Join(cfg["HOST_APP_DIR"], "template", "services", "litellm", "litellm.yml")
	data, err = host.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var fragment struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &fragment); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	images := []string{"arizuko:latest", "arizuko-vite:latest", "arizuko-davd:latest"}
	for _, name := range []string{"litellm", "litellm_db"} {
		image := fragment.Services[name].Image
		if image == "" || strings.ContainsAny(image, "$ \t\r\n") {
			return fmt.Errorf("invalid %s image in %s", name, path)
		}
		images = append(images, image)
	}
	if err := host.InspectImages(ctx, images); err != nil {
		return err
	}
	return checkAllocation(ctx, host, cfg)
}

func checkAllocation(ctx context.Context, host preflightHost, cfg map[string]string) error {
	cid, err := strconv.ParseUint(cfg["RUNED_VSOCK_CID_BASE"], 10, 32)
	if err != nil || cid < 3 || cid == 1<<32-1 {
		return errors.New("invalid RUNED_VSOCK_CID_BASE")
	}
	port, err := strconv.ParseUint(cfg["RUNED_VSOCK_PORT_BASE"], 10, 32)
	if err != nil || port == 0 || port >= 1<<32-2 {
		return errors.New("invalid RUNED_VSOCK_PORT_BASE")
	}
	proxyPort, err := strconv.ParseUint(cfg["RUNED_PROXY_PORT"], 10, 16)
	if err != nil || proxyPort == 0 {
		return errors.New("invalid RUNED_PROXY_PORT")
	}
	_, subnet, err := net.ParseCIDR(cfg["RUNED_TAP_SUBNET"])
	if err != nil || subnet.IP.To4() == nil {
		return errors.New("invalid RUNED_TAP_SUBNET")
	}
	ones, _ := subnet.Mask.Size()
	if ones > 28 {
		return errors.New("RUNED_TAP_SUBNET is too small")
	}
	proxyIP := append(net.IP(nil), subnet.IP.To4()...)
	proxyIP[3] += 2

	out, err := host.Run(ctx, "ps", "-eo", "args")
	if err != nil {
		return err
	}
	for _, match := range regexp.MustCompile(`guest-cid=([^,[:space:]]*)`).FindAllSubmatch(out, -1) {
		used, err := strconv.ParseUint(string(match[1]), 10, 32)
		if err != nil {
			return fmt.Errorf("invalid guest CID in process inventory: %w", err)
		}
		if used == cid {
			return fmt.Errorf("guest CID %d is occupied", cid)
		}
	}
	out, err = host.Run(ctx, "ss", "-H", "-l", "-n", "-A", "vsock")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return fmt.Errorf("invalid vsock inventory: %q", line)
		}
		_, value, ok := strings.Cut(fields[len(fields)-2], ":")
		if !ok {
			return fmt.Errorf("invalid vsock address: %q", line)
		}
		used, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return fmt.Errorf("invalid vsock port: %w", err)
		}
		if used == port || used == port+1 {
			return fmt.Errorf("vsock port %d is occupied", used)
		}
	}
	if err := checkNetworks(ctx, host, subnet); err != nil {
		return err
	}
	out, err = host.Run(ctx, "ss", "-H", "-l", "-n", "-t")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return fmt.Errorf("invalid TCP inventory: %q", line)
		}
		address, value, err := net.SplitHostPort(fields[len(fields)-2])
		if err != nil {
			return fmt.Errorf("invalid TCP address: %w", err)
		}
		used, err := strconv.ParseUint(value, 10, 16)
		if err != nil {
			return fmt.Errorf("invalid TCP port: %w", err)
		}
		ipAddress, _, _ := strings.Cut(address, "%")
		ip := net.ParseIP(ipAddress)
		if address != "*" && ip == nil {
			return fmt.Errorf("invalid TCP IP %q", address)
		}
		if used == proxyPort && (address == "*" || ip.IsUnspecified() || ip.Equal(proxyIP)) {
			return fmt.Errorf("proxy address %s:%d is occupied", proxyIP, proxyPort)
		}
	}
	return nil
}

func checkNetworks(ctx context.Context, host preflightHost, subnet *net.IPNet) error {
	out, err := host.Run(ctx, "ip", "-j", "-4", "address", "show")
	if err != nil {
		return err
	}
	var links []struct {
		Addresses []struct {
			Local  string `json:"local"`
			Prefix *int   `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if len(out) == 0 || strings.TrimSpace(string(out)) == "null" {
		return errors.New("missing address inventory")
	}
	if err := json.Unmarshal(out, &links); err != nil {
		return fmt.Errorf("parse address inventory: %w", err)
	}
	for _, link := range links {
		for _, address := range link.Addresses {
			ip := net.ParseIP(address.Local).To4()
			if ip == nil || address.Prefix == nil || *address.Prefix < 0 || *address.Prefix > 32 {
				return fmt.Errorf("invalid address inventory: %q", address.Local)
			}
			mask := net.CIDRMask(*address.Prefix, 32)
			network := &net.IPNet{IP: ip.Mask(mask), Mask: mask}
			if subnet.Contains(network.IP) || network.Contains(subnet.IP) {
				return fmt.Errorf("tap subnet %s overlaps address %s", subnet, network)
			}
		}
	}
	out, err = host.Run(ctx, "ip", "-j", "-4", "route", "show", "table", "all")
	if err != nil {
		return err
	}
	var routes []struct {
		Destination string `json:"dst"`
		Scope       string `json:"scope"`
		Gateway     string `json:"gateway"`
	}
	if len(out) == 0 || strings.TrimSpace(string(out)) == "null" {
		return errors.New("missing route inventory")
	}
	if err := json.Unmarshal(out, &routes); err != nil {
		return fmt.Errorf("parse route inventory: %w", err)
	}
	for _, route := range routes {
		if route.Destination == "default" || route.Destination == "0.0.0.0/0" || route.Scope != "link" || route.Gateway != "" {
			continue
		}
		destination := route.Destination
		if !strings.Contains(destination, "/") {
			destination += "/32"
		}
		_, network, err := net.ParseCIDR(destination)
		if err != nil || network.IP.To4() == nil {
			return fmt.Errorf("invalid route inventory: %q", route.Destination)
		}
		if subnet.Contains(network.IP) || network.Contains(subnet.IP) {
			return fmt.Errorf("tap subnet %s overlaps route %s", subnet, network)
		}
	}
	return nil
}
