package resreg

import (
	"strings"
	"testing"
)

// TestRegisterRejectsUnknownMCPArgType: mcpArgOption falls through its default
// to a STRING property for any Type it does not know, so a typo declares the
// wrong wire type and the argument silently never arrives — `grant_option`
// was declared "boolean" and its write was a no-op with no error anywhere.
// Registration is init-time, so this panic is a build failure.
func TestRegisterRejectsUnknownMCPArgType(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register accepted an unknown MCPArg.Type")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "unknown MCPArg.Type") || !strings.Contains(msg, "boolean") {
			t.Fatalf("panic message does not name the bad type: %v", r)
		}
	}()
	Register(Resource{
		Name: "mcp_arg_type_probe",
		MCPArgs: map[Action][]MCPArg{
			Action("add"): {{Name: "flag", Type: "boolean"}},
		},
	})
}

// TestRegisterAcceptsKnownMCPArgTypes: every spelling mcpArgOption switches on,
// plus the empty default, registers cleanly.
func TestRegisterAcceptsKnownMCPArgTypes(t *testing.T) {
	args := make([]MCPArg, 0, len(mcpArgTypes))
	for typ := range mcpArgTypes {
		args = append(args, MCPArg{Name: "a_" + typ, Type: typ})
	}
	Register(Resource{
		Name:    "mcp_arg_type_ok_probe",
		MCPArgs: map[Action][]MCPArg{Action("add"): args},
	})
}

// TestRegisteredResourcesDeclareKnownArgTypes: the guard covers the resources
// this binary actually serves, not only synthetic ones. Every resource in the
// registry passed initMeta, so this re-states the invariant against the live
// catalog and fails loudly if the check is ever bypassed.
func TestRegisteredResourcesDeclareKnownArgTypes(t *testing.T) {
	for _, r := range All() {
		for action, args := range r.MCPArgs {
			for _, a := range args {
				if !mcpArgTypes[a.Type] {
					t.Errorf("%s %s arg %q: unknown Type %q", r.Name, action, a.Name, a.Type)
				}
			}
		}
	}
}
