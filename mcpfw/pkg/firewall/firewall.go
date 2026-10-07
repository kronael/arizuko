// Package firewall is the JSON-RPC gate: it parses an MCP message, decides
// tools/call against the ruleset, and filters tools/list so a denied tool is
// not advertised. It knows nothing about folders, grants or arizuko — the
// caller supplies a ruleset and an upstream (spec 6/12-mcp-firewall).
package firewall

import (
	"encoding/json"
	"fmt"

	"github.com/kronael/arizuko/mcpfw/pkg/rule"
)

// Upstream is the MCP server behind the gate. One method, so a stub in a test
// is three lines and the gate can be driven with no real server.
type Upstream interface {
	Call(raw json.RawMessage) (json.RawMessage, error)
}

// Logger records a decision. Nil means no logging.
type Logger func(tool string, allowed bool)

// Firewall gates one upstream.
type Firewall struct {
	Rules    rule.Set
	Upstream Upstream
	Log      Logger
	// FilterList removes denied tools from a tools/list reply. Off by default:
	// hiding a tool changes what the model believes exists, which is a
	// deliberate choice rather than a side effect of gating.
	FilterList bool
}

// jsonrpcError is the JSON-RPC 2.0 error code for a refused method. -32000 is
// the reserved implementation-defined server-error range.
const codeDenied = -32000

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type callParams struct {
	Name string `json:"name"`
}

// Handle gates one JSON-RPC message. A denied tools/call never reaches
// upstream — that is the whole point, so the upstream is not consulted first
// and cannot have a side effect before the refusal.
func (f *Firewall) Handle(raw json.RawMessage) (json.RawMessage, error) {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("mcpfw: parse request: %w", err)
	}
	switch req.Method {
	case "tools/call":
		var p callParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("mcpfw: parse tools/call params: %w", err)
		}
		d := f.Rules.Decide(p.Name)
		if f.Log != nil && (d.Logged || !d.Allowed) {
			f.Log(p.Name, d.Allowed)
		}
		if !d.Allowed {
			return denied(req.ID, p.Name), nil
		}
		return f.Upstream.Call(raw)
	case "tools/list":
		reply, err := f.Upstream.Call(raw)
		if err != nil || !f.FilterList {
			return reply, err
		}
		return f.filterTools(reply)
	}
	return f.Upstream.Call(raw)
}

func denied(id json.RawMessage, tool string) json.RawMessage {
	if id == nil {
		id = json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]any{
			"code":    codeDenied,
			"message": fmt.Sprintf("mcpfw: tool %q denied by ruleset", tool),
		},
	})
	return b
}

// filterTools drops denied entries from a tools/list result. A reply it cannot
// parse is returned untouched rather than emptied: the gate's job on this path
// is to hide, and hiding everything on a parse slip would look like an upstream
// with no tools.
func (f *Firewall) filterTools(reply json.RawMessage) (json.RawMessage, error) {
	var env struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Result  struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(reply, &env); err == nil && env.Result.Tools != nil {
		kept := make([]json.RawMessage, 0, len(env.Result.Tools))
		for _, t := range env.Result.Tools {
			var named struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(t, &named); err != nil {
				continue
			}
			if f.Rules.Decide(named.Name).Allowed {
				kept = append(kept, t)
			}
		}
		env.Result.Tools = kept
		return json.Marshal(env)
	}
	// Not a tools/list result this gate can read, so there is nothing to hide.
	return reply, nil
}
