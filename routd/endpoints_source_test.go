package routd

// Single-source guard for the spec 5/16 + 5/17 fold: each folded routd resource
// mounts the canonical resources.<X>Endpoints — the SAME slice the resreg
// registry emits into /openapi.json — so the mounted REST faces, the derived
// MCP tools, and the published doc can never drift. Reverting any of these to an
// inline Endpoints literal breaks this test.

import (
	"reflect"
	"testing"

	"github.com/kronael/arizuko/resreg/resources"
)

func TestResourceEndpoints_SingleSource(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, fakeVerifier{}, 0, "")

	if !reflect.DeepEqual(srv.routesResource(nil).Endpoints, resources.RoutesEndpoints) {
		t.Error("routes: mounted Endpoints != resources.RoutesEndpoints")
	}
	if !reflect.DeepEqual(srv.webRoutesResource().Endpoints, resources.WebRoutesEndpoints) {
		t.Error("web_routes: mounted Endpoints != resources.WebRoutesEndpoints")
	}
	if !reflect.DeepEqual(srv.scheduledTasksResource(nil).Endpoints, resources.ScheduledTasksEndpoints) {
		t.Error("scheduled_tasks: mounted Endpoints != resources.ScheduledTasksEndpoints")
	}
	if !reflect.DeepEqual(srv.aclResource().Endpoints, resources.ACLEndpoints) {
		t.Error("acl: mounted Endpoints != resources.ACLEndpoints")
	}
	if !reflect.DeepEqual(srv.networkRulesResource().Endpoints, resources.NetworkRulesEndpoints) {
		t.Error("network_rules: mounted Endpoints != resources.NetworkRulesEndpoints")
	}
	if !reflect.DeepEqual(srv.routeTokensResource().Endpoints, resources.RouteTokensEndpoints) {
		t.Error("route_tokens: mounted Endpoints != resources.RouteTokensEndpoints")
	}
	if !reflect.DeepEqual(srv.groupsResource(nil).Endpoints, resources.GroupsAgentEndpoints) {
		t.Error("groups: mounted Endpoints != resources.GroupsAgentEndpoints")
	}
}
