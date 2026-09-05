package routd

// Connector loader: reads <data_dir>/connectors.toml (or $CONNECTORS_TOML) at
// routd boot and returns the validated specs. Discovery does NOT happen here —
// a remote MCP server authenticates tools/list and the credential is per folder,
// which routd's boot does not have, so the per-turn MCP socket discovers each
// connector's catalog with the broker-resolved secrets. The reusable half
// (ConnectorSpec + DiscoverConnectorTools + CallConnectorTool) lives in ipc.

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/kronael/arizuko/ipc"
)

// The two transports a [[connector]] block can declare (spec 6/19). They are
// mechanically distinct dispatch paths, which is the rule that keeps one shape
// from becoming a shape where a field means two things: `mcp` proxies a remote
// MCP server and discovers its catalogue, `rest` calls an HTTP API from a
// catalogue it declares. There is no third — the stdio connector was deleted
// 2026-08-30 because an endpoint needs no runtime and a subprocess does.
const (
	transportMCP  = "mcp"
	transportREST = "rest"
)

// connectorFile is the TOML wire shape. One file, many [[connector]] blocks.
// The rest half is read by LoadExtProviders off the same blocks.
type connectorFile struct {
	Connectors []ipc.ConnectorSpec `toml:"connector"`
}

// LoadConnectors reads connectors.toml under projectRoot (or $CONNECTORS_TOML)
// and returns the validated specs. Missing file → nil (connector path
// disabled). Bad TOML or an invalid block is an error (fail-fast at boot); an
// unreachable endpoint is not, because nothing is dialled here.
func LoadConnectors(projectRoot string) ([]ipc.ConnectorSpec, error) {
	path := os.Getenv("CONNECTORS_TOML")
	if path == "" {
		path = filepath.Join(projectRoot, "connectors.toml")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		slog.Info("no connectors.toml; broker connector path disabled", "path", path)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f connectorFile
	if err := decodeConnectors(path, data, &f); err != nil {
		return nil, err
	}
	callTimeout := connectorCallTimeout()
	var out []ipc.ConnectorSpec
	for i := range f.Connectors {
		spec := &f.Connectors[i]
		transport, err := connectorTransport(spec.Name, spec.Transport)
		if err != nil {
			return nil, err
		}
		if transport == transportREST {
			continue // LoadExtProviders owns the rest half of the same block
		}
		spec.Transport = transport
		if err := validateConnector(spec); err != nil {
			return nil, fmt.Errorf("connector[%d] %q: %w", i, spec.Name, err)
		}
		spec.CallTimeout = callTimeout
		slog.Info("connector configured", "name", spec.Name,
			"transport", transportMCP, "url", spec.URL, "secrets", spec.Secrets)
		out = append(out, *spec)
	}
	return out, nil
}

// decodeConnectors parses one connectors file and refuses any key the block
// shape does not declare. Cost of the lenient decode: the shipped templates
// wrote `[[mcp_connector]]` after the block became `[[connector]]`, parsed to
// zero blocks with no error, and configured nothing on every instance that
// copied them.
func decodeConnectors(src string, data []byte, v any) error {
	meta, err := toml.Decode(string(data), v)
	if err != nil {
		return fmt.Errorf("parse %s: %w", src, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return fmt.Errorf("parse %s: unknown key %q — a block is [[connector]] with transport = mcp | rest (spec 6/19)",
			src, undecoded[0].String())
	}
	return nil
}

// connectorTransport is the one reading of the discriminator for both loaders:
// empty means mcp, and anything but the two dispatch paths is refused, so a
// block can never be loaded by both loaders or by neither.
func connectorTransport(name, transport string) (string, error) {
	switch transport {
	case "", transportMCP:
		return transportMCP, nil
	case transportREST:
		return transportREST, nil
	}
	return "", fmt.Errorf("connector %q: transport %q is not mcp or rest", name, transport)
}

func validateConnector(s *ipc.ConnectorSpec) error {
	if s.Name == "" {
		return errors.New("name required")
	}
	if s.URL == "" {
		return errors.New("url required")
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return fmt.Errorf("url %q: %w", s.URL, err)
	}
	// The credential rides in a header, so a plaintext endpoint would put the
	// folder's token on the wire. Refuse at load rather than at first call.
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
		return fmt.Errorf("url %q: https required (a header credential must not travel in plaintext)", s.URL)
	}
	return nil
}

func connectorCallTimeout() time.Duration {
	v := os.Getenv("CONNECTOR_CALL_TIMEOUT_MS")
	if v == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(v + "ms")
	if err != nil || d <= 0 {
		return 30 * time.Second
	}
	return d
}
