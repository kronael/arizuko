package gateway

// Connector loader: reads <data_dir>/connectors.toml at gated boot,
// discovers each connector's tools by spawning the subprocess once,
// returns a namespaced ConnectorTool catalog for the MCP layer to
// register through the broker chain. Spec 9/11 M6.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/kronael/arizuko/ipc"
)

// connectorFile is the TOML wire shape. One file, many [[mcp_connector]]
// blocks.
type connectorFile struct {
	Connectors []ipc.ConnectorSpec `toml:"mcp_connector"`
}

// LoadConnectors reads connectors.toml under projectRoot (and the env
// override CONNECTORS_TOML if set), spawns each one to harvest its
// tool catalog, returns the flattened namespaced list. Missing file
// is not an error — returns nil. Bad TOML, unknown scope, or
// discovery failure ARE errors (fail-fast at boot per spec 9/11).
func LoadConnectors(ctx context.Context, projectRoot string) ([]ipc.ConnectorTool, error) {
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
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	callTimeout := envCallTimeout()
	var out []ipc.ConnectorTool
	for i := range f.Connectors {
		spec := &f.Connectors[i]
		if err := validateConnector(spec); err != nil {
			return nil, fmt.Errorf("connector[%d] %q: %w", i, spec.Name, err)
		}
		spec.CallTimeout = callTimeout
		dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		tools, err := ipc.DiscoverConnectorTools(dctx, spec)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("connector %q discover: %w", spec.Name, err)
		}
		slog.Info("connector discovered",
			"name", spec.Name, "tools", len(tools), "secrets", spec.Secrets)
		out = append(out, tools...)
	}
	return out, nil
}

func validateConnector(s *ipc.ConnectorSpec) error {
	if s.Name == "" {
		return errors.New("name required")
	}
	if len(s.Command) == 0 {
		return errors.New("command required")
	}
	if s.Scope != "" && s.Scope != "per_call" {
		return fmt.Errorf("scope=%q not supported (v1: per_call only)", s.Scope)
	}
	return nil
}

func envCallTimeout() time.Duration {
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
